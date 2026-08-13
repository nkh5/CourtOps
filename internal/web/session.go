package web

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	sessionCookie = "courtops_session"
	csrfCookie    = "courtops_csrf"
	csrfField     = "csrf_token"
	csrfHeader    = "X-CSRF-Token"
	oauthCookie   = "courtops_oauth"
)

// Session is the signed payload in the organizer's cookie.
//
// It is deliberately small: identity plus an expiry. There is no server-side
// session table because nothing here needs to outlive a cookie, and adding one
// would be a table to back up for no gain.
type Session struct {
	Subject   string `json:"sub"`   // Google's stable account identifier
	Email     string `json:"email"` // used for the organizer allowlist
	Name      string `json:"name"`
	ExpiresAt int64  `json:"exp"`
}

// Expired reports whether the session is past its expiry.
func (s Session) Expired(now time.Time) bool { return now.Unix() >= s.ExpiresAt }

var errBadSession = errors.New("session cookie is missing or invalid")

// encodeSession returns payload.signature, both base64url without padding.
func encodeSession(secret []byte, s Session) (string, error) {
	payload, err := json.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("encode session: %w", err)
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	return body + "." + sign(secret, body), nil
}

// decodeSession verifies the signature before the payload is parsed, so an
// attacker-supplied cookie never reaches the JSON decoder with meaning.
func decodeSession(secret []byte, raw string) (Session, error) {
	body, sig, ok := strings.Cut(raw, ".")
	if !ok {
		return Session{}, errBadSession
	}
	if !hmac.Equal([]byte(sig), []byte(sign(secret, body))) {
		return Session{}, errBadSession
	}
	payload, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return Session{}, errBadSession
	}
	var s Session
	if err := json.Unmarshal(payload, &s); err != nil {
		return Session{}, errBadSession
	}
	if s.Subject == "" || s.Email == "" {
		return Session{}, errBadSession
	}
	return s, nil
}

func sign(secret []byte, body string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// setSessionCookie writes the signed session cookie.
//
// HttpOnly keeps it away from any script on the page, SameSite=Lax stops a
// third-party form post from arriving already authenticated, and Secure is set
// whenever the app is served over HTTPS.
func (s *Server) setSessionCookie(w http.ResponseWriter, sess Session) error {
	value, err := encodeSession(s.cfg.SessionSecret, sess)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.secureCookies(),
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Unix(sess.ExpiresAt, 0),
		MaxAge:   int(time.Until(time.Unix(sess.ExpiresAt, 0)).Seconds()),
	})
	return nil
}

func (s *Server) clearCookie(w http.ResponseWriter, name string) {
	path := "/"
	if name == oauthCookie {
		path = "/auth"
	}
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     path,
		HttpOnly: true,
		Secure:   s.secureCookies(),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

func (s *Server) secureCookies() bool {
	return strings.HasPrefix(s.cfg.BaseURL, "https://")
}

// sessionFrom reads and validates the cookie on a request.
func (s *Server) sessionFrom(r *http.Request) (Session, bool) {
	if len(s.cfg.SessionSecret) == 0 {
		return Session{}, false
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return Session{}, false
	}
	sess, err := decodeSession(s.cfg.SessionSecret, c.Value)
	if err != nil || sess.Expired(time.Now()) {
		return Session{}, false
	}
	// The allowlist is checked on every request, not just at sign-in, so
	// removing an organizer takes effect immediately.
	if !s.cfg.AllowsOrganizer(sess.Email) {
		return Session{}, false
	}
	return sess, true
}

// csrfToken returns the caller's CSRF token, creating one if needed.
//
// This is the double-submit pattern: the token lives in a cookie and must be
// echoed in the form body. A cross-site page can cause the cookie to be sent
// but cannot read it, so it cannot produce a matching field.
func (s *Server) csrfToken(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(csrfCookie); err == nil && len(c.Value) == 64 {
		return c.Value
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		// Failing closed here would take the whole page down; the mutation
		// check below rejects an empty token anyway.
		return ""
	}
	token := hex.EncodeToString(buf)
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: false, // the board's small script echoes it back on POSTs
		Secure:   s.secureCookies(),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int((12 * time.Hour).Seconds()),
	})
	return token
}

// checkCSRF validates a mutating request.
func (s *Server) checkCSRF(r *http.Request) error {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return fmt.Errorf("cross-site request rejected (Sec-Fetch-Site: %s)", site)
	}

	c, err := r.Cookie(csrfCookie)
	if err != nil || c.Value == "" {
		return errors.New("this page has no CSRF token; reload it and try again")
	}

	supplied := r.Header.Get(csrfHeader)
	if supplied == "" {
		supplied = r.PostFormValue(csrfField)
	}
	if supplied == "" {
		return errors.New("the form was submitted without a CSRF token")
	}
	if subtle.ConstantTimeCompare([]byte(supplied), []byte(c.Value)) != 1 {
		return errors.New("the CSRF token did not match; reload the page and try again")
	}
	return nil
}
