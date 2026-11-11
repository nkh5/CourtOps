package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPublicPracticeRequiresNoSessionOrDatabase(t *testing.T) {
	s := reviewServer(t) // deliberately has no store
	s.cfg.ReadOnly = true
	for _, tc := range []struct {
		method string
		code   int
	}{{"GET", 200}, {"POST", 405}} {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest(tc.method, "/demo", nil))
		if w.Code != tc.code || len(w.Result().Cookies()) != 0 {
			t.Fatalf("%s /demo = %d, cookies %v", tc.method, w.Code, w.Result().Cookies())
		}
		if tc.method == http.MethodGet && !strings.Contains(w.Body.String(), "/static/demo.js") {
			t.Fatal("the practice controls are missing")
		}
	}
}
