package store_test

import (
	"context"
	"testing"
	"time"

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
