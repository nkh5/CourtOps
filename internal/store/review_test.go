package store_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

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

func TestAddCourtUsesCreationLimitsUnderConcurrency(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	event := liveEvent(t, st, []string{"Court 1", "Court 2", "Court 3", "Court 4", "Court 5", "Court 6", "Court 7"})
	_, err := st.AddCourt(ctx, event.ID, " court 1 ")
	mustKind(t, err, store.KindInvalid)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) { defer wg.Done(); _, errs[i] = st.AddCourt(ctx, event.ID, fmt.Sprintf("Extra %d", i)) }(i)
	}
	wg.Wait()
	successes := 0
	for _, err := range errs {
		if err == nil {
			successes++
		} else {
			mustKind(t, err, store.KindInvalid)
		}
	}
	if successes != 1 || len(snapshot(t, st, event.ID).Courts) != 8 {
		t.Fatal("concurrent additions bypassed the eight-court limit")
	}
}

// Pause the board at its courts query, commit a whole assignment, then let it
// finish. The response must describe either side of that commit consistently.
func TestSnapshotDoesNotMixConcurrentAssignmentStates(t *testing.T) {
	st := newTestStore(t)
	event := liveEvent(t, st, []string{"Court 1"}, entries(4, 1000)...)
	checkInAll(t, st, event.ID)
	courtID := courtIDs(t, st, event.ID)[0]
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	blocker, err := st.Pool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err = blocker.Exec(ctx, `LOCK TABLE courts IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	type result struct {
		snap store.Snapshot
		err  error
	}
	done := make(chan result, 1)
	go func() { snap, err := st.Snapshot(ctx, event.ID); done <- result{snap, err} }()
	for {
		var blocked bool
		err = st.Pool().QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
    WHERE datname = current_database() AND wait_event_type = 'Lock'
    AND query LIKE '%FROM courts WHERE event_id%')`).Scan(&blocked)
		if err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("board never reached the courts query")
		case <-time.After(10 * time.Millisecond):
		}
	}
	var matchID int64
	if err = blocker.QueryRow(ctx, `INSERT INTO matches(event_id,court_id) VALUES($1,$2) RETURNING id`, event.ID, courtID).Scan(&matchID); err != nil {
		t.Fatal(err)
	}
	if _, err = blocker.Exec(ctx, `INSERT INTO match_players(match_id,player_id,team,active)
   SELECT $1,player_id,CASE WHEN row_number() OVER (ORDER BY player_id)<=2 THEN 1 ELSE 2 END,true
   FROM event_players WHERE event_id=$2`, matchID, event.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = blocker.Exec(ctx, `UPDATE event_players SET state='playing',ready_since=NULL WHERE event_id=$1`, event.ID); err != nil {
		t.Fatal(err)
	}
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.err != nil {
		t.Fatal(got.err)
	}
	if len(got.snap.Queue) != 4 || got.snap.Courts[0].ActiveMatch != nil {
		t.Fatalf("mixed snapshot: %d queued, active match=%v", len(got.snap.Queue), got.snap.Courts[0].ActiveMatch)
	}
	fresh := snapshot(t, st, event.ID)
	if len(fresh.Queue) != 0 || fresh.Courts[0].ActiveMatch == nil || len(fresh.Courts[0].ActiveMatch.Players) != 4 {
		t.Fatal("next snapshot did not see the committed match")
	}
}
