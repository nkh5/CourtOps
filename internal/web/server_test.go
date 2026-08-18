package web_test

import (
	"encoding/json"

	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/nicholashu/courtops/internal/config"

	"github.com/nicholashu/courtops/internal/store"
	"github.com/nicholashu/courtops/internal/testdb"
	"github.com/nicholashu/courtops/internal/web"
)

const devOrganizer = "organizer@example.com"

// client drives the app the way a browser does: one cookie jar, real form
// posts, and no shortcuts past the middleware.
type client struct {
	t    *testing.T
	http *http.Client
	base string
	st   *store.Store
}

func newClient(t *testing.T, adjust func(*config.Config)) *client {
	t.Helper()

	st := testdb.New(t)
	st.RatingsEnabled = true

	cfg := config.Config{
		Env:               config.EnvDevelopment,
		Addr:              ":0",
		BaseURL:           "http://localhost",
		SessionSecret:     []byte("test-secret-that-is-long-enough-32"),
		SessionTTL:        time.Hour,
		AllowedOrganizers: []string{devOrganizer},
		DevOrganizer:      devOrganizer,
		RatingsEnabled:    true,
		RequestTimeout:    5 * time.Second,
	}
	if adjust != nil {
		adjust(&cfg)
	}

	srv, err := web.New(cfg, st, slog.New(slog.DiscardHandler), "test")
	if err != nil {
		t.Fatalf("build server: %v", err)
	}

	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}

	return &client{
		t:    t,
		base: ts.URL,
		st:   st,
		http: &http.Client{
			Jar:     jar,
			Timeout: 10 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse // assert on the redirect itself
			},
		},
	}
}

func (c *client) get(path string) *http.Response {
	c.t.Helper()
	resp, err := c.http.Get(c.base + path)
	if err != nil {
		c.t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

// post submits a form with the CSRF token the browser would have.
func (c *client) post(path string, form url.Values) *http.Response {
	c.t.Helper()
	if form == nil {
		form = url.Values{}
	}
	if token := c.csrf(); token != "" && form.Get("csrf_token") == "" {
		form.Set("csrf_token", token)
	}
	resp, err := c.http.PostForm(c.base+path, form)
	if err != nil {
		c.t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

// postRaw skips the CSRF token, for the tests that check it is enforced.
func (c *client) postRaw(path string, form url.Values) *http.Response {
	c.t.Helper()
	resp, err := c.http.PostForm(c.base+path, form)
	if err != nil {
		c.t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

func (c *client) csrf() string {
	u, _ := url.Parse(c.base)
	for _, cookie := range c.http.Jar.Cookies(u) {
		if cookie.Name == "courtops_csrf" {
			return cookie.Value
		}
	}
	return ""
}

func (c *client) signIn() {
	c.t.Helper()
	resp := c.get("/auth/dev/login")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		c.t.Fatalf("dev sign-in status = %d, want 303", resp.StatusCode)
	}
	// Load a page so the CSRF cookie is issued, as a browser would.
	page := c.get("/")
	page.Body.Close()
}

func body(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}

func decode(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	return out
}

func wantStatus(t *testing.T, resp *http.Response, want int) {
	t.Helper()
	if resp.StatusCode != want {
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want %d\n%s", resp.StatusCode, want, truncate(string(b), 400))
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// TestEveryPageRenders walks the pages an organizer actually opens during an
// event. A template that references a field the type does not have fails only
// when it is executed, so each page needs to be rendered at least once.
func TestEveryPageRenders(t *testing.T) {
	c := newClient(t, nil)
	c.signIn()

	c.post("/events", url.Values{"title": {"Render check"}, "date": {"2026-09-24"}, "courts": {"Court 1"}}).Body.Close()
	csv := "Name,Email,Skill\nAna,ana@example.com,Advanced\nBen,ben@example.com,Intermediate\n" +
		"Chi,chi@example.com,Intermediate\nDee,dee@example.com,Beginner\n"
	c.post("/events/1/import", url.Values{"csv": {csv}, "confirm": {"1"}}).Body.Close()
	c.post("/events/1/state", url.Values{"state": {"live"}}).Body.Close()
	c.post("/events/1/checkin-all", nil).Body.Close()

	pages := []struct {
		name, path, wantText string
	}{
		{"index", "/", "Events"},
		{"organizer before a match", "/events/1", "Queue"},
		{"board before a match", "/board/1", "Queue"},
	}
	for _, p := range pages {
		t.Run(p.name, func(t *testing.T) {
			resp := c.get(p.path)
			wantStatus(t, resp, http.StatusOK)
			if html := body(t, resp); !strings.Contains(html, p.wantText) {
				t.Errorf("%s did not contain %q", p.path, p.wantText)
			}
		})
	}

	c.post("/events/1/courts/1/next", nil).Body.Close()
	matchID := itoa(activeMatchID(t, c, 1))

	// The same pages again with a live match, plus the confirmation screen:
	// this is where the team partial is rendered.
	withMatch := []struct {
		name, path, wantText string
	}{
		{"organizer with a match", "/events/1", "Record result"},
		{"board with a match", "/board/1", "Team 1"},
		{"confirm a result", "/events/1/matches/" + matchID + "/confirm", "Which team won?"},
	}
	for _, p := range withMatch {
		t.Run(p.name, func(t *testing.T) {
			resp := c.get(p.path)
			wantStatus(t, resp, http.StatusOK)
			if html := body(t, resp); !strings.Contains(html, p.wantText) {
				t.Errorf("%s did not contain %q", p.path, p.wantText)
			}
		})
	}

	// And once more after a completed match, so the results list and the rating
	// deltas are rendered too.
	c.post("/events/1/matches/"+matchID+"/complete", url.Values{"winning_team": {"1"}}).Body.Close()
	for _, path := range []string{"/events/1", "/board/1"} {
		resp := c.get(path)
		wantStatus(t, resp, http.StatusOK)
		if html := body(t, resp); !strings.Contains(html, "won") {
			t.Errorf("%s did not show the finished match", path)
		}
	}

	t.Run("the error page renders", func(t *testing.T) {
		resp := c.get("/events/999")
		wantStatus(t, resp, http.StatusNotFound)
		if html := body(t, resp); !strings.Contains(html, "does not exist") {
			t.Errorf("the 404 page did not explain the problem")
		}
	})
}

func TestHealthz(t *testing.T) {
	c := newClient(t, nil)
	resp := c.get("/healthz")
	wantStatus(t, resp, http.StatusOK)

	got := decode(t, resp)
	if got["status"] != "ok" || got["database"] != "ok" {
		t.Errorf("health = %v, want status and database ok", got)
	}
}

func TestOrganizerRoutesRequireSignIn(t *testing.T) {
	c := newClient(t, nil)

	t.Run("a page redirects to sign in", func(t *testing.T) {
		resp := c.get("/events/1")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("status = %d, want a redirect to the sign-in page", resp.StatusCode)
		}
	})

	t.Run("a mutation is refused", func(t *testing.T) {
		resp := c.postRaw("/events", url.Values{"title": {"Sneaky"}, "date": {"2026-09-20"}})
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", resp.StatusCode)
		}
		if n := countEvents(t, c); n != 0 {
			t.Errorf("events = %d, want the write to have been refused", n)
		}
	})
}

func TestCSRFIsEnforced(t *testing.T) {
	c := newClient(t, nil)
	c.signIn()

	resp := c.postRaw("/events", url.Values{"title": {"No token"}, "date": {"2026-09-20"}})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 without a CSRF token", resp.StatusCode)
	}
	if n := countEvents(t, c); n != 0 {
		t.Errorf("events = %d, want 0", n)
	}
}

func TestReadOnlyDeploymentRefusesWrites(t *testing.T) {
	c := newClient(t, func(cfg *config.Config) { cfg.ReadOnly = true })
	c.signIn()

	resp := c.post("/events", url.Values{"title": {"Demo"}, "date": {"2026-09-20"}})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 on a read-only deployment", resp.StatusCode)
	}
}

// TestOrganizerWorkflow walks the whole evening through HTTP: create, import,
// check in, assign, record a result.
func TestOrganizerWorkflow(t *testing.T) {
	c := newClient(t, nil)
	c.signIn()

	resp := c.post("/events", url.Values{
		"title":  {"Thursday doubles"},
		"date":   {"2026-09-24"},
		"courts": {"Court 1, Court 2"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create event status = %d, want 303", resp.StatusCode)
	}
	eventID := int64(1)

	t.Run("import the roster", func(t *testing.T) {
		csv := "Name,Email,Skill\n" +
			"Ana,ana@example.com,Advanced\n" +
			"Ben,ben@example.com,Intermediate\n" +
			"Chi,chi@example.com,Intermediate\n" +
			"Dee,dee@example.com,Beginner\n"

		preview := c.post("/events/1/import", url.Values{"csv": {csv}})
		wantStatus(t, preview, http.StatusOK)
		if html := body(t, preview); !strings.Contains(html, "4 valid player") {
			t.Errorf("preview did not list the four players:\n%s", truncate(html, 500))
		}

		confirm := c.post("/events/1/import", url.Values{"csv": {csv}, "confirm": {"1"}})
		wantStatus(t, confirm, http.StatusOK)
		if html := body(t, confirm); !strings.Contains(html, "Imported") {
			t.Errorf("confirmation page did not report the import:\n%s", truncate(html, 500))
		}
	})

	t.Run("start and fill a court", func(t *testing.T) {
		start := c.post("/events/1/state", url.Values{"state": {"live"}})
		start.Body.Close()

		checkIn := c.post("/events/1/checkin-all", nil)
		checkIn.Body.Close()

		snap := snapshotOf(t, c, eventID)
		if len(snap.Queue) != 4 {
			t.Fatalf("queue = %d, want 4 ready players", len(snap.Queue))
		}

		assign := c.post("/events/1/courts/1/next", nil)
		assign.Body.Close()

		if n := activeMatches(t, c, eventID); n != 1 {
			t.Fatalf("active matches = %d, want 1", n)
		}
	})

	t.Run("a repeated assignment does not take a second court", func(t *testing.T) {
		again := c.post("/events/1/courts/1/next", nil)
		again.Body.Close()
		if n := activeMatches(t, c, eventID); n != 1 {
			t.Errorf("active matches = %d after a retry, want 1", n)
		}
	})

	t.Run("record the result", func(t *testing.T) {
		matchID := activeMatchID(t, c, eventID)

		done := c.post("/events/1/matches/"+itoa(matchID)+"/complete", url.Values{"winning_team": {"1"}})
		done.Body.Close()

		repeat := c.post("/events/1/matches/"+itoa(matchID)+"/complete", url.Values{"winning_team": {"1"}})
		repeat.Body.Close()

		conflict := c.post("/events/1/matches/"+itoa(matchID)+"/complete", url.Values{"winning_team": {"2"}})
		defer conflict.Body.Close()
		if conflict.StatusCode != http.StatusConflict {
			t.Errorf("conflicting result status = %d, want 409", conflict.StatusCode)
		}
	})
}

// Helpers that read through the app's own JSON, so the tests exercise the
// contract the board relies on.

type snapView struct {
	Courts []struct {
		ID    int64 `json:"id"`
		Match *struct {
			ID    int64  `json:"id"`
			State string `json:"state"`
		} `json:"match"`
	} `json:"courts"`
	Queue []struct {
		ID int64 `json:"id"`
	} `json:"queue"`
}

func snapshotOf(t *testing.T, c *client, eventID int64) snapView {
	t.Helper()
	resp := c.get("/board/" + itoa(eventID) + "/state.json")
	defer resp.Body.Close()
	var out snapView
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode board json: %v", err)
	}
	return out
}

func activeMatches(t *testing.T, c *client, eventID int64) int {
	t.Helper()
	n := 0
	for _, court := range snapshotOf(t, c, eventID).Courts {
		if court.Match != nil && court.Match.State == "active" {
			n++
		}
	}
	return n
}

func activeMatchID(t *testing.T, c *client, eventID int64) int64 {
	t.Helper()
	for _, court := range snapshotOf(t, c, eventID).Courts {
		if court.Match != nil {
			return court.Match.ID
		}
	}
	t.Fatal("no active match")
	return 0
}

func countEvents(t *testing.T, c *client) int {
	t.Helper()
	events, err := c.st.ListEvents(t.Context(), 50)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	return len(events)
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	return string(b)
}
