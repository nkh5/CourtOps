package store_test

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

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

func TestAddPlayerPreservesSharedIdentityAndEventSpecificDetails(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	a := liveEvent(t, st, []string{"Court 1"})
	b := liveEvent(t, st, []string{"Court 1"})
	entry := arrival(t, "returning@example.com")
	first, err := st.AddPlayer(ctx, a.ID, entry, false)
	if err != nil {
		t.Fatal(err)
	}
	if first.State != store.StateResting || first.ReadySince != nil || first.CheckedInAt != nil {
		t.Fatalf("unchecked arrival should only be registered: %+v", first)
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE players SET rating = 1375, rated_games = 12 WHERE id = $1`, first.PlayerID); err != nil {
		t.Fatal(err)
	}
	entry.DisplayName = "An accidental rename"
	entry.Band = "advanced"
	entry.EstimatedArrival = "8 PM"
	no := false
	entry.NeedsPaddle = &no
	second, err := st.AddPlayer(ctx, b.ID, entry, true)
	if err != nil {
		t.Fatal(err)
	}
	if second.PlayerID != first.PlayerID || second.DisplayName != first.DisplayName || second.Rating != 1375 || second.RatedGames != 12 {
		t.Fatalf("returning identity or earned rating changed: %+v", second)
	}
	for _, check := range []struct {
		id      int64
		arrival string
		paddle  bool
	}{{a.ID, "7:15 PM", true}, {b.ID, "8 PM", false}} {
		registrations, err := st.RegistrationDetails(ctx, check.id)
		if err != nil {
			t.Fatal(err)
		}
		registration := registrations[first.PlayerID]
		if registration.EstimatedArrival != check.arrival || registration.NeedsPaddle == nil || *registration.NeedsPaddle != check.paddle {
			t.Errorf("wrong event registration for %d: %+v", check.id, registration)
		}
	}
}

func TestAddPlayerRejectsFinishedAndInvalidWithoutCreatingIdentity(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	event := liveEvent(t, st, []string{"Court 1"})
	bad := arrival(t, "bad@example.com")
	bad.Band = "unknown"
	_, err := st.AddPlayer(ctx, event.ID, bad, true)
	mustKind(t, err, store.KindInvalid)
	if _, err := st.SetEventState(ctx, event.ID, store.EventFinished); err != nil {
		t.Fatal(err)
	}
	_, err = st.AddPlayer(ctx, event.ID, arrival(t, "late@example.com"), true)
	mustKind(t, err, store.KindPrecondition)
	if n := countRows(t, st, "players"); n != 0 {
		t.Fatalf("refused registration left identities: %d", n)
	}
}

func TestConcurrentAddPlayerCreatesOneMembership(t *testing.T) {
	st := newTestStore(t)
	event := liveEvent(t, st, []string{"Court 1"})
	entry := arrival(t, "late@example.com")
	var wg sync.WaitGroup
	errors := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := st.AddPlayer(context.Background(), event.ID, entry, true)
			errors <- err
		}()
	}
	wg.Wait()
	close(errors)
	created := 0
	for err := range errors {
		if err == nil {
			created++
		} else {
			mustKind(t, err, store.KindConflict)
		}
	}
	if created != 1 || len(snapshot(t, st, event.ID).Roster) != 1 || countRows(t, st, "players") != 1 {
		t.Fatalf("concurrent duplicate additions: %d successful", created)
	}
}

func TestAddPlayerQueueTimeStartsAfterWaitingForEventLock(t *testing.T) {
	st := newTestStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	event := liveEvent(t, st, []string{"Court 1"}, entries(1, 1000)...)
	waiter := snapshot(t, st, event.ID).Roster[0]
	tx, err := st.Pool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `SELECT id FROM events WHERE id = $1 FOR UPDATE`, event.ID); err != nil {
		t.Fatal(err)
	}
	entry := arrival(t, "late@example.com")
	result := make(chan error, 1)
	go func() {
		_, err := st.AddPlayer(ctx, event.ID, entry, true)
		result <- err
	}()
	// Wait until the arriving player's transaction is actually waiting on our
	// event lock, then check the existing player in before releasing it.
	for {
		var blocked int
		if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock' AND query LIKE '%FOR UPDATE%'`).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked > 0 {
			break
		}
		select {
		case err := <-result:
			t.Fatalf("addition finished while event was locked: %v", err)
		case <-ctx.Done():
			t.Fatal("arrival did not reach the event lock")
		case <-time.After(time.Millisecond):
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE event_players SET state = 'ready', ready_since = clock_timestamp()
		WHERE event_id = $1 AND player_id = $2`, event.ID, waiter.PlayerID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	queue := snapshot(t, st, event.ID).Queue
	if len(queue) != 2 || queue[0].PlayerID != waiter.PlayerID || !queue[1].ReadySince.After(*queue[0].ReadySince) {
		t.Fatalf("arrival used a timestamp from before waiting for the lock: %+v", queue)
	}
}

func TestImportRegistrationAnswers(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	event, err := st.CreateEvent(ctx, "Form registrations", time.Now(), []string{"Court 1"})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := roster.Parse(strings.NewReader("Display name,Email or club ID,Skill band,Estimated arrival time,Need a paddle?\nAna,a,Beginner,6 PM,Yes\nBen,b,Advanced,,\n"))
	if err != nil || !parsed.OK() {
		t.Fatalf("parse: %v / %+v", err, parsed)
	}
	if _, err = st.ImportRoster(ctx, event.ID, parsed.Entries); err != nil {
		t.Fatal(err)
	}
	players := snapshot(t, st, event.ID).Roster
	details, err := st.RegistrationDetails(ctx, event.ID)
	if err != nil {
		t.Fatal(err)
	}
	if a := details[players[0].PlayerID]; a.EstimatedArrival != "6 PM" || a.NeedsPaddle == nil || !*a.NeedsPaddle {
		t.Errorf("missing imported answers: %+v", a)
	}
	if b := details[players[1].PlayerID]; b.EstimatedArrival != "" || b.NeedsPaddle != nil {
		t.Errorf("unanswered questions changed: %+v", b)
	}
	parsed.Entries[0].EstimatedArrival = "7 PM"
	if _, err = st.ImportRoster(ctx, event.ID, parsed.Entries); err != nil {
		t.Fatal(err)
	}
	details, err = st.RegistrationDetails(ctx, event.ID)
	if err != nil || details[players[0].PlayerID].EstimatedArrival != "7 PM" {
		t.Fatalf("reimport did not refresh event registration: %+v / %v", details, err)
	}
}
