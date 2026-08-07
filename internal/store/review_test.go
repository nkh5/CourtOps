package store_test

import (
	"context"
	"fmt"

	"testing"

	"github.com/nicholashu/courtops/internal/store"
)

func TestDisplayedQueueMatchesAssignmentOnTiedWaits(t *testing.T) {
	st := newTestStore(t)
	people := entries(8, 1000)
	for i := range people {
		people[i].DisplayName = fmt.Sprintf("Player %d", 8-i)
	}
	// Duplicate display names must also have a stable order.
	people[5].DisplayName = people[6].DisplayName
	event := liveEvent(t, st, []string{"Court 1"}, people...)
	queue := checkInAll(t, st, event.ID)
	for i := 1; i < len(queue); i++ {
		if queue[i-1].PlayerID >= queue[i].PlayerID {
			t.Fatalf("tied queue is not ordered by player ID: %v", queue)
		}
	}
	match, _, err := st.AssignNext(context.Background(), event.ID, courtIDs(t, st, event.ID)[0])
	if err != nil {
		t.Fatal(err)
	}
	next := map[int64]bool{}
	for _, p := range queue[:4] {
		next[p.PlayerID] = true
	}
	for _, p := range match.Players {
		if !next[p.PlayerID] {
			t.Errorf("assigned %s, who was not shown as next", p.DisplayName)
		}
	}
}

func TestPlayerStateRetryWithOriginalPrecondition(t *testing.T) {
	st := newTestStore(t)
	event := liveEvent(t, st, []string{"Court 1"}, entries(1, 1000)...)
	player := snapshot(t, st, event.ID).Roster[0]
	ctx := context.Background()
	first, err := st.SetPlayerState(ctx, event.ID, player.PlayerID, store.StateReady, store.StateResting)
	if err != nil {
		t.Fatal(err)
	}
	again, err := st.SetPlayerState(ctx, event.ID, player.PlayerID, store.StateReady, store.StateResting)
	if err != nil {
		t.Fatalf("identical request retry: %v", err)
	}
	if !first.ReadySince.Equal(*again.ReadySince) {
		t.Fatal("retry changed queue position")
	}
	_, err = st.SetPlayerState(ctx, event.ID, player.PlayerID, store.StateLeft, store.StateResting)
	mustKind(t, err, store.KindConflict)
}

func TestFinishedEventRejectsPlayerAndCourtChanges(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	event := liveEvent(t, st, []string{"Court 1"}, entries(2, 1000)...)
	before := snapshot(t, st, event.ID)
	if _, err := st.SetEventState(ctx, event.ID, store.EventFinished); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		run  func() error
	}{
		{"player state", func() error {
			_, err := st.SetPlayerState(ctx, event.ID, before.Roster[0].PlayerID, store.StateReady, store.StateResting)
			return err
		}},
		{"bulk check-in", func() error { _, err := st.CheckInAll(ctx, event.ID); return err }},
		{"add court", func() error { _, err := st.AddCourt(ctx, event.ID, "Court 2"); return err }},
		{"close court", func() error { _, err := st.SetCourtAvailable(ctx, event.ID, before.Courts[0].ID, false); return err }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { mustKind(t, tt.run(), store.KindPrecondition) })
	}
	after := snapshot(t, st, event.ID)
	if len(after.Courts) != 1 || !after.Courts[0].Available || after.CountByState(store.StateResting) != 2 {
		t.Fatal("finished event was changed")
	}
	if _, err := st.SetEventState(ctx, event.ID, store.EventFinished); err != nil {
		t.Fatalf("finish retry: %v", err)
	}
}

func TestLiveEventCannotReturnToSetup(t *testing.T) {
	st := newTestStore(t)
	event := liveEvent(t, st, []string{"Court 1"})
	_, err := st.SetEventState(context.Background(), event.ID, store.EventSetup)
	mustKind(t, err, store.KindConflict)
}
