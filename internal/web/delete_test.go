package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestDeleteEventRequiresExactTypedConfirmation(t *testing.T) {
	s := reviewServer(t)
	for _, confirmation := range []string{"", "delete", "Delete", "DELETE ", " DELETE", "yes"} {
		t.Run(confirmation, func(t *testing.T) {
			r := signedForm(t, s, "/events/1/delete", "application/x-www-form-urlencoded", strings.NewReader(url.Values{
				"confirmation": {confirmation}, "csrf_token": {strings.Repeat("c", 64)},
			}.Encode()))
			r.SetPathValue("eventID", "1")
			w := httptest.NewRecorder()
			// The nil store proves invalid confirmation is refused before mutation.
			s.organizerOnly(s.handleDeleteEvent).ServeHTTP(w, r)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
		})
	}
}

func TestDeleteEventRouteRequiresOrganizerCSRFAndWritableDeployment(t *testing.T) {
	for _, tt := range []struct {
		name     string
		signedIn bool
		csrf     bool
		readOnly bool
		status   int
	}{
		{"signed out", false, true, false, http.StatusUnauthorized},
		{"missing csrf", true, false, false, http.StatusForbidden},
		{"read only", true, true, true, http.StatusServiceUnavailable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := reviewServer(t)
			s.cfg.ReadOnly = tt.readOnly
			form := url.Values{"confirmation": {"DELETE"}}
			if tt.csrf {
				form.Set("csrf_token", strings.Repeat("c", 64))
			}
			r := signedForm(t, s, "/events/1/delete", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
			if !tt.signedIn {
				r.Header.Del("Cookie")
			}
			w := httptest.NewRecorder()
			// Exercise actual route wiring. Passing these checks would reach the
			// nil store and return 500, so a missing guard cannot pass this test.
			s.ServeHTTP(w, r)
			if w.Code != tt.status {
				t.Fatalf("status=%d, want %d; body=%s", w.Code, tt.status, w.Body)
			}
		})
	}
}
