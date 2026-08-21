package web_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestDeniedSignInHomeStillShowsEvents(t *testing.T) {
	c := newClient(t, nil)
	c.signIn()
	c.post("/events", url.Values{"title": {"Public club evening"}, "date": {"2026-10-14"}, "courts": {"Court 1"}}).Body.Close()
	c.post("/auth/logout", nil).Body.Close()
	r := c.get("/?signin=not-organizer")
	wantStatus(t, r, http.StatusOK)
	html := body(t, r)
	for _, want := range []string{"Public club evening", "banner-problem", "not an organizer", "Court board", "Events"} {
		if !strings.Contains(html, want) {
			t.Errorf("home missing %q", want)
		}
	}
	for _, hidden := range []string{"403 ·", "Create event", "Rendered ", "CourtOps test"} {
		if strings.Contains(html, hidden) {
			t.Errorf("home unexpectedly contains %q", hidden)
		}
	}
}

func TestNewEventActionPagesRenderAndRespectFinishedState(t *testing.T) {
	c := newClient(t, nil)
	c.signIn()
	c.post("/events", url.Values{"title": {"Evening"}, "date": {"2026-10-14"}, "courts": {"Court 1"}}).Body.Close()
	r := c.get("/events/1/players/new")
	wantStatus(t, r, http.StatusOK)
	html := body(t, r)
	for _, name := range []string{"display_name", "private_ref", "skill_band", "needs_paddle", "check_in"} {
		if !strings.Contains(html, `name="`+name+`"`) {
			t.Errorf("missing form field %s", name)
		}
	}
	r = c.get("/events/1/delete")
	wantStatus(t, r, http.StatusUnprocessableEntity)
	r.Body.Close()
	c.post("/events/1/state", url.Values{"state": {"finished"}}).Body.Close()
	r = c.get("/events/1/delete")
	wantStatus(t, r, http.StatusOK)
	if html = body(t, r); !strings.Contains(html, `name="confirmation"`) || !strings.Contains(html, "DELETE") {
		t.Fatal("missing delete confirmation")
	}
	r = c.get("/events/1/players/new")
	wantStatus(t, r, http.StatusUnprocessableEntity)
	r.Body.Close()
}
