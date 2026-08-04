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
