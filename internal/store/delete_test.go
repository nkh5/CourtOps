package store_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/nicholashu/courtops/internal/store"
)

func TestDeleteFinishedEventRetainsSharedPlayersAndOtherEvents(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	people := entries(4, 1000)
	event := liveEvent(t, st, []string{"Court 1"}, people...)
	checkInAll(t, st, event.ID)
	match, _, err := st.AssignNext(ctx, event.ID, courtIDs(t, st, event.ID)[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CompleteMatch(ctx, event.ID, match.ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetEventState(ctx, event.ID, store.EventFinished); err != nil {
		t.Fatal(err)
	}

	other := liveEvent(t, st, []string{"Other court"}, people...)
	checkInAll(t, st, other.ID)
	otherMatch, _, err := st.AssignNext(ctx, other.ID, courtIDs(t, st, other.ID)[0])
	if err != nil {
		t.Fatal(err)
	}

	if err := st.DeleteEvent(ctx, event.ID); err != nil {
		t.Fatalf("delete unrated finished event: %v", err)
	}
	_, err = st.GetEvent(ctx, event.ID)
	mustKind(t, err, store.KindNotFound)
	for table, want := range map[string]int{"events": 1, "courts": 1, "event_players": 4, "matches": 1, "match_players": 4, "players": 4} {
		if got := countRows(t, st, table); got != want {
			t.Errorf("%s count = %d, want %d", table, got, want)
		}
	}
	retained, err := st.GetMatch(ctx, other.ID, otherMatch.ID)
	if err != nil || retained.State != store.MatchActive || len(retained.Players) != 4 {
		t.Fatalf("other event match changed: %+v, %v", retained, err)
	}
	for _, p := range people {
		if rating, games := ratingOf(t, st, p.PrivateRef); rating != 1000 || games != 0 {
			t.Errorf("shared player rating changed: rating=%v games=%d", rating, games)
		}
	}
	mustKind(t, st.DeleteEvent(ctx, event.ID), store.KindNotFound)
}

func TestDeleteEventRequiresFinishedState(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	event, err := st.CreateEvent(ctx, "Still running", time.Now(), []string{"Court 1"})
	if err != nil {
		t.Fatal(err)
	}
	mustKind(t, st.DeleteEvent(ctx, event.ID), store.KindPrecondition)
	if _, err := st.SetEventState(ctx, event.ID, store.EventLive); err != nil {
		t.Fatal(err)
	}
	mustKind(t, st.DeleteEvent(ctx, event.ID), store.KindPrecondition)
	if got := countRows(t, st, "events"); got != 1 {
		t.Fatalf("refused deletion removed event: %d", got)
	}
	if got := countRows(t, st, "courts"); got != 1 {
		t.Fatalf("refused deletion removed court: %d", got)
	}
}

func TestDeleteEventPreservesPersistedEloEvenWhenRatingsDisabled(t *testing.T) {
	st := newTestStore(t)
	st.RatingsEnabled = true
	ctx := context.Background()
	people := entries(4, 1000)
	event := liveEvent(t, st, []string{"Court 1"}, people...)
	checkInAll(t, st, event.ID)
	match, _, err := st.AssignNext(ctx, event.ID, courtIDs(t, st, event.ID)[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CompleteMatch(ctx, event.ID, match.ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetEventState(ctx, event.ID, store.EventFinished); err != nil {
		t.Fatal(err)
	}
	earned := make(map[string]float64)
	for _, p := range people {
		earned[p.PrivateRef], _ = ratingOf(t, st, p.PrivateRef)
	}
	st.RatingsEnabled = false
	mustKind(t, st.DeleteEvent(ctx, event.ID), store.KindPrecondition)
	for table, want := range map[string]int{"events": 1, "courts": 1, "event_players": 4, "matches": 1, "match_players": 4} {
		if got := countRows(t, st, table); got != want {
			t.Errorf("refusal modified %s: count=%d want=%d", table, got, want)
		}
	}
	for _, p := range people {
		if rating, games := ratingOf(t, st, p.PrivateRef); rating != earned[p.PrivateRef] || games != 1 {
			t.Errorf("refusal changed Elo: rating=%v games=%d", rating, games)
		}
	}
}

func TestDeleteEventRollsBackAllRecordsIfFinalDeleteFails(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	event := liveEvent(t, st, []string{"Court 1"}, entries(4, 1000)...)
	checkInAll(t, st, event.ID)
	match, _, err := st.AssignNext(ctx, event.ID, courtIDs(t, st, event.ID)[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CancelMatch(ctx, event.ID, match.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetEventState(ctx, event.ID, store.EventFinished); err != nil {
		t.Fatal(err)
	}
	// This trigger exists only in the test's disposable database. Force failure
	// after dependent rows are deleted to prove that the transaction restores all.
	if _, err := st.Pool().Exec(ctx, `
		CREATE FUNCTION refuse_test_delete() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'test deletion failure'; END $$;
		CREATE TRIGGER refuse_test_delete BEFORE DELETE ON events
		FOR EACH ROW EXECUTE FUNCTION refuse_test_delete();`); err != nil {
		t.Fatal(err)
	}
	mustKind(t, st.DeleteEvent(ctx, event.ID), store.KindInternal)
	for table, want := range map[string]int{"events": 1, "courts": 1, "event_players": 4, "matches": 1, "match_players": 4, "players": 4} {
		if got := countRows(t, st, table); got != want {
			t.Errorf("failed deletion left %s count=%d, want %d", table, got, want)
		}
	}
}

func TestConcurrentEventDeletionCommitsOnce(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	event := liveEvent(t, st, []string{"Court 1"}, entries(4, 1000)...)
	if _, err := st.SetEventState(ctx, event.ID, store.EventFinished); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, 6)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = st.DeleteEvent(ctx, event.ID)
		}(i)
	}
	close(start)
	wg.Wait()
	successes := 0
	for _, err := range errs {
		if err == nil {
			successes++
		} else {
			mustKind(t, err, store.KindNotFound)
		}
	}
	if successes != 1 {
		t.Fatalf("successful deletions=%d, want 1", successes)
	}
	if got := countRows(t, st, "players"); got != 4 {
		t.Fatalf("shared players deleted: count=%d", got)
	}
}
