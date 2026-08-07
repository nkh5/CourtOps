package store_test

import (
	"context"
	"fmt"

	"testing"
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
