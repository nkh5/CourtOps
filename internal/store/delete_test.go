package store_test

import (
	"context"

	"testing"

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
