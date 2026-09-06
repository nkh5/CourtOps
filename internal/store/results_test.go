package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/nicholashu/courtops/internal/store"
)

func TestResultsAttendanceRankingsAndFullHistory(t *testing.T) {
	st := newTestStore(t)
	st.RatingsEnabled = true
	ctx := context.Background()
	e, err := st.CreateEvent(ctx, "Results night", time.Now(), []string{"Court 1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.ImportRoster(ctx, e.ID, entries(6, 1000)); err != nil {
		t.Fatal(err)
	}
	if _, err = st.Results(ctx, e.ID); err == nil {
		t.Fatal("setup event exposed final results")
	}
	roster := checkInAll(t, st, e.ID)
	// One registrant never arrives; four stay throughout; one leaves and returns.
	if _, err = st.Pool().Exec(ctx, `UPDATE event_players SET state='resting', checked_in_at=NULL,ready_since=NULL WHERE event_id=$1 AND player_id=$2`, e.ID, roster[5].PlayerID); err != nil {
		t.Fatal(err)
	}
	if _, err = st.SetEventState(ctx, e.ID, store.EventLive); err != nil {
		t.Fatal(err)
	}
	if _, err = st.SetPlayerState(ctx, e.ID, roster[4].PlayerID, store.StateLeft, ""); err != nil {
		t.Fatal(err)
	}
	if _, err = st.SetPlayerState(ctx, e.ID, roster[4].PlayerID, store.StateReady, ""); err != nil {
		t.Fatal(err)
	}
	if _, err = st.SetPlayerState(ctx, e.ID, roster[4].PlayerID, store.StateResting, ""); err != nil {
		t.Fatal(err)
	}
	court := courtIDs(t, st, e.ID)[0]
	for i := 0; i < 27; i++ {
		m, _, err := st.AssignNext(ctx, e.ID, court)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = st.Pool().Exec(ctx, `UPDATE matches SET started_at=statement_timestamp()-interval '10 minutes' WHERE id=$1`, m.ID); err != nil {
			t.Fatal(err)
		}
		if _, _, err = st.CompleteMatch(ctx, e.ID, m.ID, 1); err != nil {
			t.Fatal(err)
		}
	}
	m, _, err := st.AssignNext(ctx, e.ID, court)
	if err != nil {
		t.Fatal(err)
	}
	// A long cancelled game must not become the longest completed game.
	if _, err = st.Pool().Exec(ctx, `UPDATE matches SET started_at=statement_timestamp()-interval '2 hours' WHERE id=$1`, m.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = st.CancelMatch(ctx, e.ID, m.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = st.SetEventState(ctx, e.ID, store.EventFinished); err != nil {
		t.Fatal(err)
	}
	r, err := st.Results(ctx, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Registered != 6 || r.Arrived != 5 || r.AtEnd != 5 || r.WholeEvent != 4 || !r.AttendanceRecorded {
		t.Fatalf("bad attendance: %+v", r)
	}
	if r.Completed != 27 || r.Cancelled != 1 || len(r.Matches) != 28 || r.Longest < 10*time.Minute || r.Longest > 11*time.Minute {
		t.Fatalf("bad totals: %+v", r)
	}
	if len(r.Leaders) != 4 {
		t.Fatalf("standings included non-playing members: %+v", r.Leaders)
	}
	totalWins := 0
	for i, p := range r.Leaders {
		totalWins += p.Wins
		if p.Games != 27 {
			t.Fatalf("games=%d", p.Games)
		}
		if i > 0 && p.Wins > r.Leaders[i-1].Wins {
			t.Fatal("wins not sorted")
		}
	}
	if totalWins != 54 {
		t.Fatalf("wins=%d", totalWins)
	}
	if len(r.Search("PLAYER 01")) != 1 || len(r.Search("@example.com")) != 0 {
		t.Fatal("search must use public names only")
	}
	h, err := st.PlayerHistory(ctx, roster[0].PlayerID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !h.HasNext || len(h.Matches) != 25 || h.Player.RatedGames != 27 {
		t.Fatalf("bad history: %+v", h)
	}
	h2, err := st.PlayerHistory(ctx, roster[0].PlayerID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if h2.HasNext || len(h2.Matches) != 3 || h.Matches[24].ID == h2.Matches[0].ID {
		t.Fatal("bad history pagination")
	}
	// Legacy events must not invent whole-event attendance.
	if _, err = st.Pool().Exec(ctx, `UPDATE events SET started_at=NULL WHERE id=$1`, e.ID); err != nil {
		t.Fatal(err)
	}
	r, err = st.Results(ctx, e.ID)
	if err != nil || r.AttendanceRecorded || r.LengthRecorded {
		t.Fatalf("legacy attendance: %v %+v", err, r)
	}
}

