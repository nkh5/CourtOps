package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/nicholashu/courtops/internal/roster"
	"github.com/nicholashu/courtops/internal/store"
	"github.com/nicholashu/courtops/internal/testdb"
)

// These tests run against a real PostgreSQL because the behaviour under test is
// PostgreSQL's: row locks, partial unique indexes and transaction rollback. A
// fake would only prove that the fake agrees with itself.
//
// Set TEST_DATABASE_URL to a server the tests may create databases on:
//
//	TEST_DATABASE_URL=postgres://courtops:courtops@localhost:5433/courtops?sslmode=disable go test ./...
func newTestStore(t *testing.T) *store.Store { return testdb.New(t) }

// liveEvent creates an event with the given courts, imports players and starts
// it. Returns the event and the roster in import order.
func liveEvent(t *testing.T, st *store.Store, courts []string, players ...roster.Entry) store.Event {
	t.Helper()
	ctx := context.Background()

	event, err := st.CreateEvent(ctx, "Test event", time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC), courts)
	if err != nil {
		t.Fatalf("create event: %v", err)
	}
	if len(players) > 0 {
		if _, err := st.ImportRoster(ctx, event.ID, players); err != nil {
			t.Fatalf("import roster: %v", err)
		}
	}
	if _, err := st.SetEventState(ctx, event.ID, store.EventLive); err != nil {
		t.Fatalf("start event: %v", err)
	}
	return event
}

// entries builds n roster rows with predictable names and seeds.
func entries(n int, seed int) []roster.Entry {
	out := make([]roster.Entry, n)
	for i := range out {
		out[i] = roster.Entry{
			Row:         i + 2,
			DisplayName: fmt.Sprintf("Player %02d", i+1),
			PrivateRef:  fmt.Sprintf("player%02d@example.com", i+1),
			Band:        "intermediate",
			Seed:        seed,
		}
	}
	return out
}

// checkInAll marks the whole roster ready and returns it in queue order.
func checkInAll(t *testing.T, st *store.Store, eventID int64) []store.EventPlayer {
	t.Helper()
	ctx := context.Background()
	if _, err := st.CheckInAll(ctx, eventID); err != nil {
		t.Fatalf("check in all: %v", err)
	}
	snap, err := st.Snapshot(ctx, eventID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	return snap.Queue
}

func snapshot(t *testing.T, st *store.Store, eventID int64) store.Snapshot {
	t.Helper()
	snap, err := st.Snapshot(context.Background(), eventID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	return snap
}

func courtIDs(t *testing.T, st *store.Store, eventID int64) []int64 {
	t.Helper()
	snap := snapshot(t, st, eventID)
	out := make([]int64, 0, len(snap.Courts))
	for _, c := range snap.Courts {
		out = append(out, c.ID)
	}
	return out
}

// countActiveMatches reads straight from the database so the assertion does not
// depend on the code under test.
func countActiveMatches(t *testing.T, st *store.Store, eventID int64) int {
	t.Helper()
	var n int
	err := st.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM matches WHERE event_id = $1 AND state = 'active'`, eventID).Scan(&n)
	if err != nil {
		t.Fatalf("count active matches: %v", err)
	}
	return n
}

func countRows(t *testing.T, st *store.Store, table string) int {
	t.Helper()
	var n int
	if err := st.Pool().QueryRow(context.Background(), `SELECT count(*) FROM `+table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func ratingOf(t *testing.T, st *store.Store, privateRef string) (float64, int) {
	t.Helper()
	var rating float64
	var games int
	err := st.Pool().QueryRow(context.Background(),
		`SELECT rating, rated_games FROM players WHERE private_ref = $1`, privateRef).Scan(&rating, &games)
	if err != nil {
		t.Fatalf("read rating for %s: %v", privateRef, err)
	}
	return rating, games
}

func mustKind(t *testing.T, err error, want store.Kind) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected a %s error, got nil", want)
	}
	if got := store.KindOf(err); got != want {
		t.Fatalf("error kind = %s (%v), want %s", got, err, want)
	}
}
