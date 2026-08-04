package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/nicholashu/courtops/internal/roster"
	"github.com/nicholashu/courtops/internal/store"
)

func TestEventSurvivesReconnect(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	event, err := st.CreateEvent(ctx, "Thursday doubles",
		time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC), []string{"Court 1", "Court 2"})
	if err != nil {
		t.Fatalf("create event: %v", err)
	}

	// A new pool is the closest thing to an application restart: same database,
	// no shared in-memory state.
	reopened, err := store.Open(ctx, st.Pool().Config().ConnString())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()

	got, err := reopened.GetEvent(ctx, event.ID)
	if err != nil {
		t.Fatalf("get event after restart: %v", err)
	}
	if got.Title != "Thursday doubles" || got.State != store.EventSetup {
		t.Errorf("event after restart = %+v", got)
	}

	snap := snapshot(t, reopened, event.ID)
	if len(snap.Courts) != 2 {
		t.Errorf("courts after restart = %d, want 2", len(snap.Courts))
	}
}

func TestForeignKeyRejectsUnknownPlayer(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	event := liveEvent(t, st, []string{"Court 1"})

	_, err := st.Pool().Exec(ctx,
		`INSERT INTO event_players (event_id, player_id) VALUES ($1, $2)`, event.ID, 999999)
	if err == nil {
		t.Fatal("expected the foreign key to reject a player that does not exist")
	}
}

func TestDuplicateEventMembershipIsRejected(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	event := liveEvent(t, st, []string{"Court 1"}, entries(4, 1000)...)

	var playerID int64
	if err := st.Pool().QueryRow(ctx,
		`SELECT player_id FROM event_players WHERE event_id = $1 LIMIT 1`, event.ID).Scan(&playerID); err != nil {
		t.Fatalf("read a membership: %v", err)
	}

	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO event_players (event_id, player_id) VALUES ($1, $2)`, event.ID, playerID); err == nil {
		t.Fatal("expected the primary key to reject a duplicate membership")
	}
}

func TestOneActiveMatchPerCourtIndex(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	event := liveEvent(t, st, []string{"Court 1"}, entries(8, 1000)...)
	checkInAll(t, st, event.ID)
	court := courtIDs(t, st, event.ID)[0]

	if _, _, err := st.AssignNext(ctx, event.ID, court); err != nil {
		t.Fatalf("assign: %v", err)
	}

	// Bypass the service entirely: the database itself must refuse a second
	// live match on the same court.
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO matches (event_id, court_id, state) VALUES ($1, $2, 'active')`, event.ID, court); err == nil {
		t.Fatal("expected the partial unique index to reject a second active match on the court")
	}
}

func TestImportRoster(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	event, err := st.CreateEvent(ctx, "Import test", time.Now(), []string{"Court 1"})
	if err != nil {
		t.Fatalf("create event: %v", err)
	}

	people := entries(6, 1000)
	first, err := st.ImportRoster(ctx, event.ID, people)
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	if first.PlayersCreated != 6 || first.MembershipsAdded != 6 {
		t.Fatalf("first import = %+v, want 6 created and 6 added", first)
	}

	t.Run("reimporting the same roster adds nothing", func(t *testing.T) {
		second, err := st.ImportRoster(ctx, event.ID, people)
		if err != nil {
			t.Fatalf("second import: %v", err)
		}
		if second.PlayersCreated != 0 || second.MembershipsAdded != 0 || second.MembershipsSkipped != 6 {
			t.Errorf("second import = %+v, want nothing created or added", second)
		}
		if n := countRows(t, st, "players"); n != 6 {
			t.Errorf("players = %d, want 6", n)
		}
		if n := countRows(t, st, "event_players"); n != 6 {
			t.Errorf("memberships = %d, want 6", n)
		}
	})

	t.Run("a second event reuses the same player identities", func(t *testing.T) {
		other, err := st.CreateEvent(ctx, "Next week", time.Now().AddDate(0, 0, 7), []string{"Court 1"})
		if err != nil {
			t.Fatalf("create second event: %v", err)
		}
		res, err := st.ImportRoster(ctx, other.ID, people)
		if err != nil {
			t.Fatalf("import into second event: %v", err)
		}
		if res.PlayersCreated != 0 || res.PlayersMatched != 6 || res.MembershipsAdded != 6 {
			t.Errorf("second event import = %+v, want 6 matched and 6 added", res)
		}
		if n := countRows(t, st, "players"); n != 6 {
			t.Errorf("players = %d, want the same 6 people", n)
		}
	})

	t.Run("import is refused once the event is live", func(t *testing.T) {
		if _, err := st.SetEventState(ctx, event.ID, store.EventLive); err != nil {
			t.Fatalf("start event: %v", err)
		}
		_, err := st.ImportRoster(ctx, event.ID, entries(1, 1000))
		mustKind(t, err, store.KindPrecondition)
	})
}

func TestImportRollsBackCompletely(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	event, err := st.CreateEvent(ctx, "Rollback test", time.Now(), []string{"Court 1"})
	if err != nil {
		t.Fatalf("create event: %v", err)
	}

	// The second and third rows are fine; the fourth carries a seed the CHECK
	// constraint rejects. Nothing at all should be written.
	bad := entries(3, 1000)
	bad = append(bad, roster.Entry{Row: 5, DisplayName: "Impossible", PrivateRef: "x@example.com", Band: "advanced", Seed: 99999})

	if _, err := st.ImportRoster(ctx, event.ID, bad); err == nil {
		t.Fatal("expected the import to fail")
	}
	if n := countRows(t, st, "players"); n != 0 {
		t.Errorf("players = %d after a failed import, want 0", n)
	}
	if n := countRows(t, st, "event_players"); n != 0 {
		t.Errorf("memberships = %d after a failed import, want 0", n)
	}
}

func TestSetPlayerState(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	event := liveEvent(t, st, []string{"Court 1"}, entries(4, 1000)...)

	snap := snapshot(t, st, event.ID)
	player := snap.Roster[0]

	ready, err := st.SetPlayerState(ctx, event.ID, player.PlayerID, store.StateReady, "")
	if err != nil {
		t.Fatalf("check in: %v", err)
	}
	if ready.State != store.StateReady || ready.ReadySince == nil {
		t.Fatalf("after check-in = %+v, want ready with a wait time", ready)
	}

	t.Run("repeating check-in keeps the original place in the queue", func(t *testing.T) {
		again, err := st.SetPlayerState(ctx, event.ID, player.PlayerID, store.StateReady, store.StateReady)
		if err != nil {
			t.Fatalf("repeat check in: %v", err)
		}
		if !again.ReadySince.Equal(*ready.ReadySince) {
			t.Errorf("ready_since moved from %v to %v on a repeated command", *ready.ReadySince, *again.ReadySince)
		}
	})

	t.Run("a stale expected state is refused", func(t *testing.T) {
		// The organizer's page still said "resting", but the player is ready.
		_, err := st.SetPlayerState(ctx, event.ID, player.PlayerID, store.StateLeft, store.StateResting)
		mustKind(t, err, store.KindConflict)

		current := snapshot(t, st, event.ID)
		for _, p := range current.Roster {
			if p.PlayerID == player.PlayerID && p.State != store.StateReady {
				t.Errorf("state = %s after a refused write, want it unchanged", p.State)
			}
		}
	})

	t.Run("a player on court cannot be edited directly", func(t *testing.T) {
		checkInAll(t, st, event.ID)
		if _, _, err := st.AssignNext(ctx, event.ID, courtIDs(t, st, event.ID)[0]); err != nil {
			t.Fatalf("assign: %v", err)
		}
		_, err := st.SetPlayerState(ctx, event.ID, player.PlayerID, store.StateLeft, "")
		mustKind(t, err, store.KindPrecondition)
	})
}

func TestAssignNextRequiresFourReadyPlayers(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	event := liveEvent(t, st, []string{"Court 1"}, entries(3, 1000)...)
	checkInAll(t, st, event.ID)

	_, _, err := st.AssignNext(ctx, event.ID, courtIDs(t, st, event.ID)[0])
	mustKind(t, err, store.KindPrecondition)

	if n := countActiveMatches(t, st, event.ID); n != 0 {
		t.Errorf("active matches = %d, want 0", n)
	}
}

func TestAssignNextTakesTheLongestWaiting(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	event := liveEvent(t, st, []string{"Court 1"}, entries(6, 1000)...)

	// Check people in one at a time so the wait order is unambiguous.
	snap := snapshot(t, st, event.ID)
	var order []store.EventPlayer
	for _, p := range snap.Roster {
		got, err := st.SetPlayerState(ctx, event.ID, p.PlayerID, store.StateReady, "")
		if err != nil {
			t.Fatalf("check in %s: %v", p.DisplayName, err)
		}
		order = append(order, got)
		time.Sleep(2 * time.Millisecond)
	}

	match, created, err := st.AssignNext(ctx, event.ID, courtIDs(t, st, event.ID)[0])
	if err != nil || !created {
		t.Fatalf("assign: created=%v err=%v", created, err)
	}

	onCourt := map[int64]bool{}
	for _, p := range match.Players {
		onCourt[p.PlayerID] = true
	}
	for i, p := range order[:4] {
		if !onCourt[p.PlayerID] {
			t.Errorf("player %d in the queue (%s) was skipped", i+1, p.DisplayName)
		}
	}
	for _, p := range order[4:] {
		if onCourt[p.PlayerID] {
			t.Errorf("%s played before everyone ahead of them", p.DisplayName)
		}
	}
}

func TestCompleteMatchReturnsPlayersToTheQueue(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	event := liveEvent(t, st, []string{"Court 1"}, entries(4, 1000)...)
	checkInAll(t, st, event.ID)
	court := courtIDs(t, st, event.ID)[0]

	match, _, err := st.AssignNext(ctx, event.ID, court)
	if err != nil {
		t.Fatalf("assign: %v", err)
	}

	done, changed, err := st.CompleteMatch(ctx, event.ID, match.ID, 1)
	if err != nil || !changed {
		t.Fatalf("complete: changed=%v err=%v", changed, err)
	}
	if done.State != store.MatchCompleted || done.WinningTeam == nil || *done.WinningTeam != 1 {
		t.Fatalf("completed match = %+v", done)
	}

	snap := snapshot(t, st, event.ID)
	if len(snap.Queue) != 4 {
		t.Errorf("queue after the match = %d players, want all 4 back", len(snap.Queue))
	}
	if n := countActiveMatches(t, st, event.ID); n != 0 {
		t.Errorf("active matches = %d, want 0", n)
	}

	var active int
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM match_players WHERE active`).Scan(&active); err != nil {
		t.Fatalf("count active memberships: %v", err)
	}
	if active != 0 {
		t.Errorf("active match memberships = %d, want 0 once the match is over", active)
	}
}

func TestCancelMatchReleasesEveryone(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	event := liveEvent(t, st, []string{"Court 1"}, entries(4, 1000)...)
	checkInAll(t, st, event.ID)
	court := courtIDs(t, st, event.ID)[0]

	match, _, err := st.AssignNext(ctx, event.ID, court)
	if err != nil {
		t.Fatalf("assign: %v", err)
	}

	if _, changed, err := st.CancelMatch(ctx, event.ID, match.ID); err != nil || !changed {
		t.Fatalf("cancel: changed=%v err=%v", changed, err)
	}
	if snap := snapshot(t, st, event.ID); len(snap.Queue) != 4 {
		t.Errorf("queue after cancelling = %d, want 4", len(snap.Queue))
	}

	t.Run("cancelling twice is harmless", func(t *testing.T) {
		_, changed, err := st.CancelMatch(ctx, event.ID, match.ID)
		if err != nil {
			t.Fatalf("second cancel: %v", err)
		}
		if changed {
			t.Error("the second cancel reported a change")
		}
	})

	t.Run("a cancelled match cannot be given a result", func(t *testing.T) {
		_, _, err := st.CompleteMatch(ctx, event.ID, match.ID, 1)
		mustKind(t, err, store.KindConflict)
	})

	t.Run("the court can be used again", func(t *testing.T) {
		if _, created, err := st.AssignNext(ctx, event.ID, court); err != nil || !created {
			t.Fatalf("reassign after cancel: created=%v err=%v", created, err)
		}
	})
}

func TestFinishingAnEventRequiresEmptyCourts(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	event := liveEvent(t, st, []string{"Court 1"}, entries(4, 1000)...)
	checkInAll(t, st, event.ID)

	match, _, err := st.AssignNext(ctx, event.ID, courtIDs(t, st, event.ID)[0])
	if err != nil {
		t.Fatalf("assign: %v", err)
	}

	_, err = st.SetEventState(ctx, event.ID, store.EventFinished)
	mustKind(t, err, store.KindPrecondition)

	if _, _, err := st.CompleteMatch(ctx, event.ID, match.ID, 2); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if _, err := st.SetEventState(ctx, event.ID, store.EventFinished); err != nil {
		t.Fatalf("finish after the court cleared: %v", err)
	}
}

func TestCourtFromAnotherEventIsRejected(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	a := liveEvent(t, st, []string{"Court 1"}, entries(4, 1000)...)
	b := liveEvent(t, st, []string{"Court 1"})
	checkInAll(t, st, a.ID)

	otherCourt := courtIDs(t, st, b.ID)[0]
	_, _, err := st.AssignNext(ctx, a.ID, otherCourt)
	mustKind(t, err, store.KindNotFound)
}
