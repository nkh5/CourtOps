package store_test

import (
	"context"
	"sync"
	"testing"

	"github.com/nicholashu/courtops/internal/store"
)

// The tests in this file are the reason the store locks the event row. Each one
// runs real concurrent requests on separate pooled connections against real
// PostgreSQL; none of them would prove anything against a fake.

func TestConcurrentAssignmentsOnOneCourtCreateOneMatch(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	event := liveEvent(t, st, []string{"Court 1"}, entries(8, 1000)...)
	checkInAll(t, st, event.ID)
	court := courtIDs(t, st, event.ID)[0]

	const requests = 8
	type result struct {
		match   store.Match
		created bool
		err     error
	}
	results := make([]result, requests)

	// Release every goroutine at once so the requests genuinely overlap.
	var start, done sync.WaitGroup
	start.Add(1)
	done.Add(requests)
	for i := 0; i < requests; i++ {
		go func(i int) {
			defer done.Done()
			start.Wait()
			m, created, err := st.AssignNext(ctx, event.ID, court)
			results[i] = result{m, created, err}
		}(i)
	}
	start.Done()
	done.Wait()

	createdCount := 0
	var matchID int64
	for i, r := range results {
		if r.err != nil {
			t.Fatalf("request %d failed: %v", i, r.err)
		}
		if r.created {
			createdCount++
		}
		if matchID == 0 {
			matchID = r.match.ID
		} else if r.match.ID != matchID {
			t.Errorf("request %d got match %d, another got %d: every caller must see the same match",
				i, r.match.ID, matchID)
		}
	}

	if createdCount != 1 {
		t.Errorf("created = %d, want exactly one of %d concurrent requests to create the match", createdCount, requests)
	}
	if n := countActiveMatches(t, st, event.ID); n != 1 {
		t.Errorf("active matches = %d, want 1", n)
	}
	if n := countRows(t, st, "match_players"); n != 4 {
		t.Errorf("match memberships = %d, want 4", n)
	}
	if snap := snapshot(t, st, event.ID); len(snap.Queue) != 4 {
		t.Errorf("queue = %d, want the other 4 players still waiting", len(snap.Queue))
	}
}

func TestConcurrentAssignmentsOnTwoCourtsShareTheQueue(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	event := liveEvent(t, st, []string{"Court 1", "Court 2"}, entries(8, 1000)...)
	checkInAll(t, st, event.ID)
	courts := courtIDs(t, st, event.ID)

	var wg sync.WaitGroup
	matches := make([]store.Match, len(courts))
	errs := make([]error, len(courts))
	wg.Add(len(courts))
	for i, court := range courts {
		go func(i int, court int64) {
			defer wg.Done()
			matches[i], _, errs[i] = st.AssignNext(ctx, event.ID, court)
		}(i, court)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("court %d: %v", i, err)
		}
	}

	// Eight ready players, two courts: both matches happen and nobody is on
	// both of them.
	seen := map[int64]bool{}
	for _, m := range matches {
		for _, p := range m.Players {
			if seen[p.PlayerID] {
				t.Errorf("player %d (%s) was put on two courts at once", p.PlayerID, p.DisplayName)
			}
			seen[p.PlayerID] = true
		}
	}
	if len(seen) != 8 {
		t.Errorf("distinct players on court = %d, want 8", len(seen))
	}
	if n := countActiveMatches(t, st, event.ID); n != 2 {
		t.Errorf("active matches = %d, want 2", n)
	}
}

func TestTwoCourtsCannotShareTooFewPlayers(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	event := liveEvent(t, st, []string{"Court 1", "Court 2"}, entries(4, 1000)...)
	checkInAll(t, st, event.ID)
	courts := courtIDs(t, st, event.ID)

	var wg sync.WaitGroup
	created := make([]bool, len(courts))
	errs := make([]error, len(courts))
	wg.Add(len(courts))
	for i, court := range courts {
		go func(i int, court int64) {
			defer wg.Done()
			_, created[i], errs[i] = st.AssignNext(ctx, event.ID, court)
		}(i, court)
	}
	wg.Wait()

	successes, refusals := 0, 0
	for i := range courts {
		switch {
		case errs[i] == nil && created[i]:
			successes++
		case store.KindOf(errs[i]) == store.KindPrecondition:
			refusals++
		default:
			t.Fatalf("court %d: unexpected outcome created=%v err=%v", i, created[i], errs[i])
		}
	}
	if successes != 1 || refusals != 1 {
		t.Errorf("got %d matches and %d refusals, want exactly one of each", successes, refusals)
	}
	if n := countActiveMatches(t, st, event.ID); n != 1 {
		t.Errorf("active matches = %d, want 1", n)
	}
}

// TestFailedAssignmentLeavesNothingBehind forces a failure after the match row
// has been inserted but before the transaction commits.
//
// The setup is realistic rather than contrived: the same person is on two
// events' rosters and is already playing in the first one. The per-player
// partial unique index rejects the second live membership, and the whole
// assignment must disappear.
func TestFailedAssignmentLeavesNothingBehind(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	people := entries(8, 1000)
	eventA := liveEvent(t, st, []string{"Court A"}, people[:4]...)
	eventB := liveEvent(t, st, []string{"Court B"}, people...)

	// Everyone is ready in B, and the first four are also ready in A.
	checkInAll(t, st, eventB.ID)
	checkInAll(t, st, eventA.ID)

	if _, created, err := st.AssignNext(ctx, eventA.ID, courtIDs(t, st, eventA.ID)[0]); err != nil || !created {
		t.Fatalf("assign in event A: created=%v err=%v", created, err)
	}

	matchesBefore := countRows(t, st, "matches")
	membershipsBefore := countRows(t, st, "match_players")

	// In event B those same four players are still marked ready, so they are the
	// longest waiting and the matcher will pick them. The database refuses.
	_, _, err := st.AssignNext(ctx, eventB.ID, courtIDs(t, st, eventB.ID)[0])
	mustKind(t, err, store.KindConflict)

	if n := countRows(t, st, "matches"); n != matchesBefore {
		t.Errorf("matches = %d after the failed assignment, want %d: a half-created match was left behind", n, matchesBefore)
	}
	if n := countRows(t, st, "match_players"); n != membershipsBefore {
		t.Errorf("match memberships = %d, want %d", n, membershipsBefore)
	}
	if n := countActiveMatches(t, st, eventB.ID); n != 0 {
		t.Errorf("event B has %d active matches, want 0", n)
	}

	// Nobody in event B was left marked as playing by the rolled-back attempt.
	snapB := snapshot(t, st, eventB.ID)
	if len(snapB.Queue) != 8 {
		t.Errorf("event B queue = %d, want all 8 still ready", len(snapB.Queue))
	}
}

func TestConcurrentCompletionsRecordOneResult(t *testing.T) {
	st := newTestStore(t)
	st.RatingsEnabled = true
	ctx := context.Background()
	event := liveEvent(t, st, []string{"Court 1"}, entries(4, 1000)...)
	checkInAll(t, st, event.ID)

	match, _, err := st.AssignNext(ctx, event.ID, courtIDs(t, st, event.ID)[0])
	if err != nil {
		t.Fatalf("assign: %v", err)
	}

	const attempts = 6
	changes := make([]bool, attempts)
	errs := make([]error, attempts)

	var start, done sync.WaitGroup
	start.Add(1)
	done.Add(attempts)
	for i := 0; i < attempts; i++ {
		go func(i int) {
			defer done.Done()
			start.Wait()
			_, changes[i], errs[i] = st.CompleteMatch(ctx, event.ID, match.ID, 1)
		}(i)
	}
	start.Done()
	done.Wait()

	changed := 0
	for i := range changes {
		if errs[i] != nil {
			t.Fatalf("attempt %d: %v", i, errs[i])
		}
		if changes[i] {
			changed++
		}
	}
	if changed != 1 {
		t.Errorf("changed = %d, want exactly one of %d identical submissions to write", changed, attempts)
	}

	// The rating must have moved exactly once, not six times.
	rating, games := ratingOf(t, st, "player01@example.com")
	if games != 1 {
		t.Errorf("rated games = %d, want 1", games)
	}
	if rating != 1016 && rating != 984 {
		t.Errorf("rating = %v, want one single 16-point move from 1000", rating)
	}
}
