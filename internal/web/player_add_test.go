package web_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/nicholashu/courtops/internal/config"
)

func newArrivalForm() url.Values {
	return url.Values{
		"display_name": {"Late arrival"}, "private_ref": {"private-arrival@example.com"},
		"skill_band": {"intermediate"}, "estimated_arrival": {"private arrival note at 7 PM"},
		"needs_paddle": {"yes"}, "check_in": {"1"},
	}
}

func TestAddPlayerHTTPWorkflowAndPrivacy(t *testing.T) {
	c := newClient(t, nil)
	c.signIn()
	response := c.post("/events", url.Values{"title": {"Live arrivals"}, "date": {"2026-10-13"}})
	wantStatus(t, response, http.StatusSeeOther)
	response.Body.Close()
	response = c.post("/events/1/state", url.Values{"state": {"live"}})
	wantStatus(t, response, http.StatusSeeOther)
	response.Body.Close()

	response = c.post("/events/1/players", newArrivalForm())
	wantStatus(t, response, http.StatusSeeOther)
	response.Body.Close()
	if got := snapshotOf(t, c, 1); len(got.Queue) != 1 {
		t.Fatalf("arrival is absent from queue: %+v", got)
	}
	response = c.post("/events/1/players", newArrivalForm())
	wantStatus(t, response, http.StatusConflict)
	response.Body.Close()

	for _, path := range []string{"/board/1", "/board/1?fragment=1", "/board/1/state.json"} {
		response = c.get(path)
		wantStatus(t, response, http.StatusOK)
		html := body(t, response)
		for _, private := range []string{"private-arrival@example.com", "private arrival note", "needs_paddle", "estimated_arrival"} {
			if strings.Contains(html, private) {
				t.Errorf("public response %s leaks %q", path, private)
			}
		}
	}
	response = c.post("/events/1/state", url.Values{"state": {"finished"}})
	wantStatus(t, response, http.StatusSeeOther)
	response.Body.Close()
	form := newArrivalForm()
	form.Set("private_ref", "too-late@example.com")
	response = c.post("/events/1/players", form)
	wantStatus(t, response, http.StatusUnprocessableEntity)
	response.Body.Close()
}

func TestAddPlayerHTTPProtections(t *testing.T) {
	t.Run("authentication", func(t *testing.T) {
		c := newClient(t, nil)
		response := c.postRaw("/events/1/players", newArrivalForm())
		defer response.Body.Close()
		wantStatus(t, response, http.StatusUnauthorized)
	})
	t.Run("csrf", func(t *testing.T) {
		c := newClient(t, nil)
		c.signIn()
		response := c.postRaw("/events/1/players", newArrivalForm())
		defer response.Body.Close()
		wantStatus(t, response, http.StatusForbidden)
	})
	t.Run("read only", func(t *testing.T) {
		c := newClient(t, func(cfg *config.Config) { cfg.ReadOnly = true })
		c.signIn()
		response := c.post("/events/1/players", newArrivalForm())
		defer response.Body.Close()
		wantStatus(t, response, http.StatusServiceUnavailable)
	})
	t.Run("input validation", func(t *testing.T) {
		c := newClient(t, nil)
		c.signIn()
		for _, invalid := range []struct{ field, value string }{
			{"skill_band", "unknown"}, {"needs_paddle", "maybe"}, {"private_ref", " "}, {"check_in", "false"},
		} {
			form := newArrivalForm()
			form.Set(invalid.field, invalid.value)
			response := c.post("/events/1/players", form)
			wantStatus(t, response, http.StatusBadRequest)
			response.Body.Close()
		}
	})
}
