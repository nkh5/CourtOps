package web

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/nicholashu/courtops/internal/config"
	"github.com/nicholashu/courtops/internal/roster"
)

func reviewServer(t *testing.T) *Server {
	t.Helper()
	s, err := New(config.Config{Env: config.EnvDevelopment, BaseURL: "http://localhost", SessionSecret: []byte(strings.Repeat("s", 32)), AllowedOrganizers: []string{"test@example.com"}}, nil, slog.New(slog.DiscardHandler), "test")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func signedForm(t *testing.T, s *Server, path, contentType string, body io.Reader) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, path, body)
	r.Header.Set("Content-Type", contentType)
	r.Header.Set("Accept", "application/json")
	session, err := encodeSession(s.cfg.SessionSecret, Session{Subject: "test", Email: "test@example.com", ExpiresAt: time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
	r.AddCookie(&http.Cookie{Name: csrfCookie, Value: strings.Repeat("c", 64)})
	return r
}

func TestOversizedMultipartIsRejectedBeforeCSRF(t *testing.T) {
	s := reviewServer(t)
	var b bytes.Buffer
	form := multipart.NewWriter(&b)
	file, err := form.CreateFormFile("file", "roster.csv")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.Copy(file, strings.NewReader(strings.Repeat("x", 2*roster.MaxBytes))); err != nil {
		t.Fatal(err)
	}
	if err = form.Close(); err != nil {
		t.Fatal(err)
	}
	r := signedForm(t, s, "/events/1/import", form.FormDataContentType(), &b)
	w := httptest.NewRecorder()
	s.organizerOnly(func(http.ResponseWriter, *http.Request) { t.Error("oversized upload reached handler") }).ServeHTTP(w, r)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
}

func TestMalformedAndOversizedActionFormsCannotMutate(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		status     int
	}{
		{"malformed escaping", "available=true&bad=%zz", http.StatusBadRequest},
		{"oversized", "available=true&padding=" + strings.Repeat("x", 70<<10), http.StatusRequestEntityTooLarge},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := reviewServer(t)
			r := signedForm(t, s, "/events/1/courts/1/availability", "application/x-www-form-urlencoded", strings.NewReader(tt.body))
			r.Header.Set(csrfHeader, strings.Repeat("c", 64))
			w := httptest.NewRecorder()
			s.organizerOnly(func(http.ResponseWriter, *http.Request) { t.Error("invalid form reached handler") }).ServeHTTP(w, r)
			if w.Code != tt.status {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
		})
	}
}

func TestImportConfirmationAllowsEncodingExpansionAndPreservesLines(t *testing.T) {
	s := reviewServer(t)
	raw := "\nName,Email,Skill,Notes\nAna,a@example.com,Beginner," + strings.Repeat("界", 200000) + "\n"
	form := url.Values{"csrf_token": {strings.Repeat("c", 64)}, "csv": {raw}, "confirm": {"1"}}
	encoded := form.Encode()
	if len(raw) >= roster.MaxBytes || len(encoded) <= roster.MaxBytes {
		t.Fatal("fixture does not exercise encoding expansion")
	}
	r := signedForm(t, s, "/events/1/import", "application/x-www-form-urlencoded", strings.NewReader(encoded))
	w := httptest.NewRecorder()
	called := false
	s.organizerOnly(func(w http.ResponseWriter, r *http.Request) {
		called = true
		got, err := s.readUpload(r)
		if err != nil {
			t.Fatal(err)
		}
		if got != raw {
			t.Fatal("upload changed the CSV bytes or line positions")
		}
		parsed, err := roster.Parse(strings.NewReader(got))
		if err != nil || !parsed.OK() || parsed.Entries[0].Row != 3 {
			t.Fatalf("parsed=%+v err=%v", parsed, err)
		}
	}).ServeHTTP(w, r)
	if !called || w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
}

func TestCourtAvailabilityRequiresExplicitBoolean(t *testing.T) {
	s := reviewServer(t)
	for _, value := range []string{"", "tru", "0"} {
		r := httptest.NewRequest(http.MethodPost, "/events/1/courts/1/availability", strings.NewReader(url.Values{"available": {value}}.Encode()))
		r.SetPathValue("eventID", "1")
		r.SetPathValue("courtID", "1")
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Accept", "application/json")
		w := httptest.NewRecorder()
		// No store is supplied: invalid input must be refused before a write.
		s.handleCourtAvailability(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("available=%q status=%d", value, w.Code)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestConcurrentDiscoveryRecoversAfterFailure(t *testing.T) {
	var calls atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != googleIssuer+"/.well-known/openid-configuration" {
			return nil, fmt.Errorf("unexpected URL %s", r.URL)
		}
		if calls.Add(1) == 1 {
			time.Sleep(10 * time.Millisecond)
			return nil, fmt.Errorf("temporary outage")
		}
		metadata := `{"issuer":"https://accounts.google.com","authorization_endpoint":"https://accounts.google.com/auth","token_endpoint":"https://accounts.google.com/token","jwks_uri":"https://accounts.google.com/keys","id_token_signing_alg_values_supported":["RS256"]}`
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(metadata))}, nil
	})}
	ctx := oidc.ClientContext(context.Background(), client)
	auth := &googleAuth{clientID: "client", clientSecret: "secret", redirectURL: "https://example.com/auth/google/callback"}
	var wg sync.WaitGroup
	errs := make([]error, 16)
	start := make(chan struct{})
	for i := range errs {
		wg.Add(1)
		go func(i int) { defer wg.Done(); <-start; _, _, errs[i] = auth.config(ctx) }(i)
	}
	close(start)
	wg.Wait()
	failures := 0
	for _, err := range errs {
		if err != nil {
			failures++
		}
	}
	if calls.Load() != 2 || failures != 1 {
		t.Fatalf("discovery calls=%d failures=%d, want 2 and 1", calls.Load(), failures)
	}
}

func TestOAuthCookieIsClearedOnItsOriginalPath(t *testing.T) {
	s := reviewServer(t)
	w := httptest.NewRecorder()
	s.clearCookie(w, oauthCookie)
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Path != "/auth" || cookies[0].MaxAge != -1 {
		t.Fatalf("cookie deletion=%+v", cookies)
	}
}

func TestReturnPathRejectsBrowserRedirectEscapes(t *testing.T) {
	for _, raw := range []string{"https://example.com", "//example.com", "/\\example.com", "/\n/example.com"} {
		if got := safeReturnPath(raw); got != "/" {
			t.Errorf("safeReturnPath(%q)=%q", raw, got)
		}
	}
	if got := safeReturnPath("/events/12?tab=queue"); got != "/events/12?tab=queue" {
		t.Errorf("valid return path lost: %s", got)
	}
}
