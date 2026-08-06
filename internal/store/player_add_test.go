package store_test

import (
	"context"
	"reflect"

	"sync"
	"testing"

	"github.com/nicholashu/courtops/internal/roster"
	"github.com/nicholashu/courtops/internal/store"
)

func arrival(t *testing.T, ref string) roster.Entry {
	t.Helper()
	entry, err := roster.NewEntry("Late arrival", ref, "beginner", "7:15 PM", "yes")
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

func TestAddPlayerDuringLiveMatch(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	event := liveEvent(t, st, []string{"Court 1"}, entries(6, 1000)...)
	checkInAll(t, st, event.ID)
	match, _, err := st.AssignNext(ctx, event.ID, courtIDs(t, st, event.ID)[0])
	if err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, st, event.ID)
	player, err := st.AddPlayer(ctx, event.ID, arrival(t, "  LATE@example.com  "), true)
	if err != nil {
		t.Fatal(err)
	}
	after := snapshot(t, st, event.ID)
	if player.State != store.StateReady || player.ReadySince == nil || player.CheckedInAt == nil {
		t.Fatalf("new arrival not checked in: %+v", player)
	}
	if len(after.Queue) != 3 || after.Queue[2].PlayerID != player.PlayerID || !player.ReadySince.After(*before.Queue[1].ReadySince) {
		t.Fatalf("arrival did not join end of queue: %+v", after.Queue)
	}
	unchanged, err := st.GetMatch(ctx, event.ID, match.ID)
	if err != nil || !reflect.DeepEqual(unchanged, match) {
		t.Fatalf("adding arrival changed an ongoing match: %+v / %v", unchanged, err)
	}
	_, err = st.AddPlayer(ctx, event.ID, arrival(t, "late@EXAMPLE.com"), true)
	mustKind(t, err, store.KindConflict)
	if again := snapshot(t, st, event.ID); !reflect.DeepEqual(again.Queue, after.Queue) || len(again.Roster) != 7 {
		t.Fatalf("duplicate changed attendance or queue: %+v", again.Queue)
	}
	if n := countRows(t, st, "players"); n != 7 {
		t.Fatalf("duplicate created an identity: %d", n)
	}
}

func TestAddPlayerPreservesSharedIdentityAndEventSpecificDetails(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	a := liveEvent(t, st, []string{"Court 1"})
	b := liveEvent(t, st, []string{"Court 1"})
	entry := arrival(t, "returning@example.com")
	first, err := st.AddPlayer(ctx, a.ID, entry, false)
	if err != nil {
		t.Fatal(err)
	}
	if first.State != store.StateResting || first.ReadySince != nil || first.CheckedInAt != nil {
		t.Fatalf("unchecked arrival should only be registered: %+v", first)
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE players SET rating = 1375, rated_games = 12 WHERE id = $1`, first.PlayerID); err != nil {
		t.Fatal(err)
	}
	entry.DisplayName = "An accidental rename"
	entry.Band = "advanced"
	entry.EstimatedArrival = "8 PM"
	no := false
	entry.NeedsPaddle = &no
	second, err := st.AddPlayer(ctx, b.ID, entry, true)
	if err != nil {
		t.Fatal(err)
	}
	if second.PlayerID != first.PlayerID || second.DisplayName != first.DisplayName || second.Rating != 1375 || second.RatedGames != 12 {
		t.Fatalf("returning identity or earned rating changed: %+v", second)
	}
	for _, check := range []struct {
		id      int64
		arrival string
		paddle  bool
	}{{a.ID, "7:15 PM", true}, {b.ID, "8 PM", false}} {
		registrations, err := st.RegistrationDetails(ctx, check.id)
		if err != nil {
			t.Fatal(err)
		}
		registration := registrations[first.PlayerID]
		if registration.EstimatedArrival != check.arrival || registration.NeedsPaddle == nil || *registration.NeedsPaddle != check.paddle {
			t.Errorf("wrong event registration for %d: %+v", check.id, registration)
		}
	}
}

func TestAddPlayerRejectsFinishedAndInvalidWithoutCreatingIdentity(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	event := liveEvent(t, st, []string{"Court 1"})
	bad := arrival(t, "bad@example.com")
	bad.Band = "unknown"
	_, err := st.AddPlayer(ctx, event.ID, bad, true)
	mustKind(t, err, store.KindInvalid)
	if _, err := st.SetEventState(ctx, event.ID, store.EventFinished); err != nil {
		t.Fatal(err)
	}
	_, err = st.AddPlayer(ctx, event.ID, arrival(t, "late@example.com"), true)
	mustKind(t, err, store.KindPrecondition)
	if n := countRows(t, st, "players"); n != 0 {
		t.Fatalf("refused registration left identities: %d", n)
	}
}

func TestConcurrentAddPlayerCreatesOneMembership(t *testing.T) {
	st := newTestStore(t)
	event := liveEvent(t, st, []string{"Court 1"})
	entry := arrival(t, "late@example.com")
	var wg sync.WaitGroup
	errors := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := st.AddPlayer(context.Background(), event.ID, entry, true)
			errors <- err
		}()
	}
	wg.Wait()
	close(errors)
	created := 0
	for err := range errors {
		if err == nil {
			created++
		} else {
			mustKind(t, err, store.KindConflict)
		}
	}
	if created != 1 || len(snapshot(t, st, event.ID).Roster) != 1 || countRows(t, st, "players") != 1 {
		t.Fatalf("concurrent duplicate additions: %d successful", created)
	}
}
