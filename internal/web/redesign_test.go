package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nicholashu/courtops/internal/store"
)

func TestDeniedOrganizerSignInReturnsToPublicHome(t *testing.T) {
	s := reviewServer(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/auth/google/callback", nil)
	s.rejectOrganizerSignIn(w, r)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/?signin=not-organizer" {
		t.Fatalf("status=%d location=%s", w.Code, w.Header().Get("Location"))
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie && c.MaxAge == -1 {
			return
		}
	}
	t.Fatal("a denied account must clear any previous organizer session")
}

func TestEventStatsExcludeUnarrivedAndDepartedPlayersAndBusyCourts(t *testing.T) {
	now := time.Now()
	snap := store.Snapshot{
		Roster: []store.EventPlayer{
			{State: store.StateReady, CheckedInAt: &now},
			{State: store.StateResting, CheckedInAt: &now},
			{State: store.StatePlaying, CheckedInAt: &now},
			{State: store.StateLeft, CheckedInAt: &now},
			{State: store.StateResting},
		},
		Queue:  []store.EventPlayer{{State: store.StateReady}},
		Courts: []store.Court{{Available: true}, {Available: false}, {Available: true, ActiveMatch: &store.Match{}}},
	}
	want := eventStatsView{CheckedIn: 3, Queue: 1, OnCourt: 1, CourtsAvailable: 1, CourtsTotal: 3}
	if got := eventStats(snap); got != want {
		t.Fatalf("stats = %+v, want %+v", got, want)
	}
	if got := publicView(snap).Stats; got != want {
		t.Fatalf("public stats = %+v, want %+v", got, want)
	}
}
