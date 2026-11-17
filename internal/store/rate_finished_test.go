package store_test

import (
	"context"
	"math"
	"testing"

	"github.com/nicholashu/courtops/internal/ratings"
	"github.com/nicholashu/courtops/internal/store"
)

// playUnrated runs a short event with ratings off and finishes it, the way an
// event played before Elo was switched on looks in the database.
func playUnrated(t *testing.T, st *store.Store, rounds int) store.Event {
	t.Helper()
	ctx := context.Background()
	people := entries(6, 800)
	people[0].Seed, people[1].Seed = 1200, 1200
	people[4].Seed, people[5].Seed = 400, 400
	event := liveEvent(t, st, []string{"Court 1"}, people...)
	checkInAll(t, st, event.ID)
	court := courtIDs(t, st, event.ID)[0]
	for i := 0; i < rounds; i++ {
		m, _, err := st.AssignNext(ctx, event.ID, court)
		if err != nil {
			t.Fatalf("assign %d: %v", i, err)
		}
		if _, _, err := st.CompleteMatch(ctx, event.ID, m.ID, 1+i%2); err != nil {
			t.Fatalf("complete %d: %v", i, err)
		}
	}
	if _, err := st.SetEventState(ctx, event.ID, store.EventFinished); err != nil {
		t.Fatalf("finish: %v", err)
	}
	return event
}

func rerunMigration(t *testing.T, st *store.Store, version int) {
	t.Helper()
	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx, `DELETE FROM schema_migrations WHERE version = $1`, version); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestFinishedEventsPlayedUnratedAreRatedInOrder(t *testing.T) {
	st := newTestStore(t)
	st.RatingsEnabled = false
	ctx := context.Background()
	event := playUnrated(t, st, 5)

	before, err := st.Results(ctx, event.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Replay with the app's own maths, oldest match first.
	want := map[int64]float64{}
	for _, p := range before.Players {
		want[p.ID] = p.Rating
	}
	for i := len(before.Matches) - 1; i >= 0; i-- {
		m := before.Matches[i]
		a, b := m.Team(1), m.Team(2)
		u := ratings.TeamUpdate(
			[2]float64{want[a[0].PlayerID], want[a[1].PlayerID]},
			[2]float64{want[b[0].PlayerID], want[b[1].PlayerID]},
			*m.WinningTeam == 1)
		for _, p := range a {
			want[p.PlayerID] += u.DeltaA
		}
		for _, p := range b {
			want[p.PlayerID] += u.DeltaB()
		}
	}

	rerunMigration(t, st, 6)
	after, err := st.Results(ctx, event.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range after.Players {
		if math.Abs(p.Rating-want[p.ID]) > 1e-9 {
			t.Errorf("%s: rating %v, want %v", p.Name, p.Rating, want[p.ID])
		}
		if p.Games > 0 && (!p.EventRated || math.Abs(p.EventElo-want[p.ID]) > 1e-9 || p.RatedGames != p.Games) {
			t.Errorf("%s: event Elo %v (rated %v) over %d rated games, want %v over %d", p.Name, p.EventElo, p.EventRated, p.RatedGames, want[p.ID], p.Games)
		}
	}
	for _, m := range after.Matches {
		for _, p := range m.Players {
			if p.Delta() == nil {
				t.Fatalf("match %d: %s has no rating change", m.ID, p.DisplayName)
			}
		}
	}

	// Running it again changes nothing: every match now has its history.
	rerunMigration(t, st, 6)
	again, err := st.Results(ctx, event.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range again.Players {
		if p.Rating != after.Players[i].Rating {
			t.Errorf("%s moved on a second run: %v -> %v", p.Name, after.Players[i].Rating, p.Rating)
		}
	}
}

func TestRatingBackfillLeavesEarnedHistoryAlone(t *testing.T) {
	st := newTestStore(t)
	st.RatingsEnabled = false
	ctx := context.Background()
	old := playUnrated(t, st, 1)

	// The same players then play a rated game in a later event.
	st.RatingsEnabled = true
	later, err := st.CreateEvent(ctx, "Later", old.Date, []string{"Court 1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ImportRoster(ctx, later.ID, entries(6, 800)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetEventState(ctx, later.ID, store.EventLive); err != nil {
		t.Fatal(err)
	}
	checkInAll(t, st, later.ID)
	m, _, err := st.AssignNext(ctx, later.ID, courtIDs(t, st, later.ID)[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CompleteMatch(ctx, later.ID, m.ID, 1); err != nil {
		t.Fatal(err)
	}
	ratingBefore, gamesBefore := ratingOf(t, st, "player01@example.com")

	rerunMigration(t, st, 6)
	res, err := st.Results(ctx, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Any player of the old match who has since played rated keeps the old
	// match unrated, so the whole match stays unrated.
	for _, mt := range res.Matches {
		for _, p := range mt.Players {
			if p.Delta() != nil {
				t.Errorf("old match %d was rated out of order for %s", mt.ID, p.DisplayName)
			}
		}
	}
	if r, g := ratingOf(t, st, "player01@example.com"); r != ratingBefore || g != gamesBefore {
		t.Errorf("player01 changed from %v/%d to %v/%d", ratingBefore, gamesBefore, r, g)
	}
}
