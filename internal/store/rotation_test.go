package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/nicholashu/courtops/internal/store"
)

// Simulate two courts releasing their original groups six minutes apart. The
// board must predict the same mixed four that the locked write actually uses.
func TestStaggeredCourtsMixGroupsAndPreviewMatchesAssignment(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	e := liveEvent(t, st, []string{"A", "B"}, entries(8, 1000)...)
	checkInAll(t, st, e.ID)
	courts := courtIDs(t, st, e.ID)
	assign := func(c int64) store.Match {
		m, created, err := st.AssignNext(ctx, e.ID, c)
		if err != nil || !created {
			t.Fatalf("assign: created=%v err=%v", created, err)
		}
		return m
	}
	a, b := assign(courts[0]), assign(courts[1])
	base := time.Now().Add(-8 * time.Minute)
	first := map[int64]bool{}
	for i, m := range []store.Match{a, b} {
		if _, _, err := st.CompleteMatch(ctx, e.ID, m.ID, 1); err != nil {
			t.Fatal(err)
		}
		for _, p := range m.Players {
			if i == 0 {
				first[p.PlayerID] = true
			}
			if _, err := st.Pool().Exec(ctx, "UPDATE event_players SET ready_since=$1 WHERE event_id=$2 AND player_id=$3", base.Add(time.Duration(i)*6*time.Minute), e.ID, p.PlayerID); err != nil {
				t.Fatal(err)
			}
		}
	}
	s := snapshot(t, st, e.ID)
	if len(s.NextUp) != 4 || !s.NextUp[s.Queue[0].PlayerID] {
		t.Fatalf("missing oldest or incorrect prediction: %v", s.NextUp)
	}
	n := 0
	for _, p := range assign(courts[0]).Players {
		if !s.NextUp[p.PlayerID] {
			t.Fatalf("board prediction missed player %d", p.PlayerID)
		}
		if first[p.PlayerID] {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("got %d players from first court, want 2", n)
	}
}
