package store_test

import (
	"context"

	"testing"

	"github.com/nicholashu/courtops/internal/store"
)

func TestRetriesDoNotDuplicateWork(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	event := liveEvent(t, st, []string{"Court 1"}, entries(8, 1000)...)
	checkInAll(t, st, event.ID)
	court := courtIDs(t, st, event.ID)[0]

	first, created, err := st.AssignNext(ctx, event.ID, court)
	if err != nil || !created {
		t.Fatalf("first assignment: created=%v err=%v", created, err)
	}

	t.Run("a retried assignment returns the same match", func(t *testing.T) {
		again, created, err := st.AssignNext(ctx, event.ID, court)
		if err != nil {
			t.Fatalf("retry: %v", err)
		}
		if created {
			t.Error("the retry reported that it created a new match")
		}
		if again.ID != first.ID {
			t.Errorf("retry returned match %d, want the existing %d", again.ID, first.ID)
		}
		if n := countActiveMatches(t, st, event.ID); n != 1 {
			t.Errorf("active matches = %d, want 1", n)
		}
		if snap := snapshot(t, st, event.ID); len(snap.Queue) != 4 {
			t.Errorf("queue = %d, want the retry to have taken nobody new", len(snap.Queue))
		}
	})

	t.Run("a repeated result is stored once", func(t *testing.T) {
		if _, changed, err := st.CompleteMatch(ctx, event.ID, first.ID, 2); err != nil || !changed {
			t.Fatalf("complete: changed=%v err=%v", changed, err)
		}
		done, changed, err := st.CompleteMatch(ctx, event.ID, first.ID, 2)
		if err != nil {
			t.Fatalf("repeat complete: %v", err)
		}
		if changed {
			t.Error("submitting the same winner again reported a change")
		}
		if *done.WinningTeam != 2 {
			t.Errorf("winning team = %d, want 2", *done.WinningTeam)
		}
		if n := countRows(t, st, "matches"); n != 1 {
			t.Errorf("matches = %d, want 1", n)
		}
	})

	t.Run("a conflicting result is refused", func(t *testing.T) {
		_, _, err := st.CompleteMatch(ctx, event.ID, first.ID, 1)
		mustKind(t, err, store.KindConflict)

		match, err := st.GetMatch(ctx, event.ID, first.ID)
		if err != nil {
			t.Fatalf("get match: %v", err)
		}
		if *match.WinningTeam != 2 {
			t.Errorf("winning team = %d after a refused edit, want the saved 2", *match.WinningTeam)
		}
	})
}
