package store_test

import (
	"context"
	"reflect"

	"testing"

	"github.com/nicholashu/courtops/internal/roster"
	"github.com/nicholashu/courtops/internal/store"
)

func arrival(t *testing.T, ref string) roster.Entry {
	t.Helper()
	entry, err := roster.NewEntry("Late arrival", ref, "beginner", "7:15 PM", "yes")
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

func TestAddPlayerDuringLiveMatch(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	event := liveEvent(t, st, []string{"Court 1"}, entries(6, 1000)...)
	checkInAll(t, st, event.ID)
	match, _, err := st.AssignNext(ctx, event.ID, courtIDs(t, st, event.ID)[0])
	if err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, st, event.ID)
	player, err := st.AddPlayer(ctx, event.ID, arrival(t, "  LATE@example.com  "), true)
	if err != nil {
		t.Fatal(err)
	}
	after := snapshot(t, st, event.ID)
	if player.State != store.StateReady || player.ReadySince == nil || player.CheckedInAt == nil {
		t.Fatalf("new arrival not checked in: %+v", player)
	}
	if len(after.Queue) != 3 || after.Queue[2].PlayerID != player.PlayerID || !player.ReadySince.After(*before.Queue[1].ReadySince) {
		t.Fatalf("arrival did not join end of queue: %+v", after.Queue)
	}
	unchanged, err := st.GetMatch(ctx, event.ID, match.ID)
	if err != nil || !reflect.DeepEqual(unchanged, match) {
		t.Fatalf("adding arrival changed an ongoing match: %+v / %v", unchanged, err)
	}
	_, err = st.AddPlayer(ctx, event.ID, arrival(t, "late@EXAMPLE.com"), true)
	mustKind(t, err, store.KindConflict)
	if again := snapshot(t, st, event.ID); !reflect.DeepEqual(again.Queue, after.Queue) || len(again.Roster) != 7 {
		t.Fatalf("duplicate changed attendance or queue: %+v", again.Queue)
	}
	if n := countRows(t, st, "players"); n != 7 {
		t.Fatalf("duplicate created an identity: %d", n)
	}
}
