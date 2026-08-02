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
