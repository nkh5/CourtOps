package store_test

import (
	"context"
	"math"
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

func TestRatingsAreAppliedWithTheResult(t *testing.T) {
	st := newTestStore(t)
	st.RatingsEnabled = true
	ctx := context.Background()

	// Two 1200s and two 800s: the matcher splits them 1200+800 against
	// 1200+800, so both team means are 1000 and a win is worth 16 points.
	people := entries(4, 1000)
	people[0].Seed, people[1].Seed = 1200, 1200
	people[2].Seed, people[3].Seed = 800, 800

	event := liveEvent(t, st, []string{"Court 1"}, people...)
	checkInAll(t, st, event.ID)

	match, _, err := st.AssignNext(ctx, event.ID, courtIDs(t, st, event.ID)[0])
	if err != nil {
		t.Fatalf("assign: %v", err)
	}

	done, _, err := st.CompleteMatch(ctx, event.ID, match.ID, 1)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}

	var total float64
	for _, p := range done.Players {
		if p.RatingBefore == nil || p.RatingAfter == nil {
			t.Fatalf("%s has no rating history recorded", p.DisplayName)
		}
		delta := *p.RatingAfter - *p.RatingBefore
		want := 16.0
		if p.Team == 2 {
			want = -16.0
		}
		if math.Abs(delta-want) > 1e-9 {
			t.Errorf("%s (team %d) moved %v, want %v", p.DisplayName, p.Team, delta, want)
		}
		total += delta
	}
	if math.Abs(total) > 1e-9 {
		t.Errorf("the four ratings moved by %v in total, want 0", total)
	}

	for _, e := range people {
		_, games := ratingOf(t, st, e.PrivateRef)
		if games != 1 {
			t.Errorf("%s has %d rated games, want 1", e.DisplayName, games)
		}
	}
}

func TestRatingsCanBeTurnedOff(t *testing.T) {
	st := newTestStore(t)
	st.RatingsEnabled = false
	ctx := context.Background()
	event := liveEvent(t, st, []string{"Court 1"}, entries(4, 1000)...)
	checkInAll(t, st, event.ID)

	match, _, err := st.AssignNext(ctx, event.ID, courtIDs(t, st, event.ID)[0])
	if err != nil {
		t.Fatalf("assign: %v", err)
	}
	if _, _, err := st.CompleteMatch(ctx, event.ID, match.ID, 1); err != nil {
		t.Fatalf("complete: %v", err)
	}

	rating, games := ratingOf(t, st, "player01@example.com")
	if games != 0 || rating != 1000 {
		t.Errorf("rating = %v over %d games, want the seed 1000 and 0 games while ratings are off", rating, games)
	}
}
