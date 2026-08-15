package web

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const googleIssuer = "https://accounts.google.com"

// googleAuth wraps the OIDC provider. Discovery is done lazily on the first
// sign-in attempt so that the app still starts on a machine with no network,
// and so a Google outage cannot stop an event that is already running.
type googleAuth struct {
	mu       sync.Mutex
	provider *oidc.Provider

	clientID     string
	clientSecret string
	redirectURL  string
}

func (g *googleAuth) config(ctx context.Context) (*oauth2.Config, *oidc.IDTokenVerifier, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.provider == nil {
		discoverCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		provider, err := oidc.NewProvider(discoverCtx, googleIssuer)
		if err != nil {
			return nil, nil, fmt.Errorf("contact Google's OpenID configuration: %w", err)
		}
		g.provider = provider
	}

	cfg := &oauth2.Config{
		ClientID:     g.clientID,
		ClientSecret: g.clientSecret,
		RedirectURL:  g.redirectURL,
		Endpoint:     g.provider.Endpoint(),
		Scopes:       []string{oidc.ScopeOpenID, "email", "profile"},
	}
	return cfg, g.provider.Verifier(&oidc.Config{ClientID: g.clientID}), nil
}

// oauthState is the short-lived signed cookie that ties a callback back to the
// browser that started the sign-in.
type oauthState struct {
	State     string `json:"state"`
	Nonce     string `json:"nonce"`
	Return    string `json:"return"`
	ExpiresAt int64  `json:"exp"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		s.renderError(w, r, http.StatusNotImplemented,
			"Google sign-in is not configured on this deployment.")
		return
	}

	cfg, _, err := s.auth.config(r.Context())
	if err != nil {
		s.log.Error("oidc discovery failed", "error", err)
		s.renderError(w, r, http.StatusBadGateway,
			"Could not reach Google to start sign-in. Try again in a moment.")
		return
	}

	state, err := randomToken()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	nonce, err := randomToken()
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	payload, err := json.Marshal(oauthState{
		State:     state,
		Nonce:     nonce,
		Return:    safeReturnPath(r.URL.Query().Get("return")),
		ExpiresAt: time.Now().Add(10 * time.Minute).Unix(),
	})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	http.SetCookie(w, &http.Cookie{
		Name:     oauthCookie,
		Value:    body + "." + sign(s.cfg.SessionSecret, body),
		Path:     "/auth",
		HttpOnly: true,
		Secure:   s.secureCookies(),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   600,
	})

	http.Redirect(w, r, cfg.AuthCodeURL(state, oidc.Nonce(nonce)), http.StatusFound)
}

func (s *Server) handleCallback(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		s.renderError(w, r, http.StatusNotImplemented, "Google sign-in is not configured on this deployment.")
		return
	}

	pending, err := s.readOAuthState(r)
	s.clearCookie(w, oauthCookie)
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest,
			"This sign-in link has expired or was not started here. Try signing in again.")
		return
	}
	// Compare the state from the redirect with the one this browser was given.
	if q := r.URL.Query().Get("state"); q == "" || q != pending.State {
		s.renderError(w, r, http.StatusBadRequest, "Sign-in state did not match. Try again.")
		return
	}
	if errParam := r.URL.Query().Get("error"); errParam != "" {
		s.renderError(w, r, http.StatusBadRequest, "Google reported: "+errParam)
		return
	}

	cfg, verifier, err := s.auth.config(r.Context())
	if err != nil {
		s.log.Error("oidc discovery failed", "error", err)
		s.renderError(w, r, http.StatusBadGateway, "Could not reach Google to finish sign-in.")
		return
	}

	token, err := cfg.Exchange(r.Context(), r.URL.Query().Get("code"))
	if err != nil {
		s.log.Warn("oauth exchange failed", "error", err)
		s.renderError(w, r, http.StatusBadRequest, "Could not complete sign-in with Google.")
		return
	}
	rawID, ok := token.Extra("id_token").(string)
	if !ok {
		s.renderError(w, r, http.StatusBadGateway, "Google did not return an identity token.")
		return
	}
	idToken, err := verifier.Verify(r.Context(), rawID)
	if err != nil {
		s.log.Warn("id token verification failed", "error", err)
		s.renderError(w, r, http.StatusBadRequest, "Google's identity token could not be verified.")
		return
	}
	if idToken.Nonce != pending.Nonce {
		s.renderError(w, r, http.StatusBadRequest, "Sign-in nonce did not match. Try again.")
		return
	}

	var claims struct {
		Email    string `json:"email"`
		Verified bool   `json:"email_verified"`
		Name     string `json:"name"`
	}
	if err := idToken.Claims(&claims); err != nil {
		s.serverError(w, r, err)
		return
	}
	email := strings.ToLower(strings.TrimSpace(claims.Email))
	if email == "" || !claims.Verified {
		s.renderError(w, r, http.StatusForbidden, "That Google account has no verified email address.")
		return
	}

	// Authentication succeeded; authorization is a separate question.
	if !s.cfg.AllowsOrganizer(email) {
		s.log.Info("sign-in refused", "email", email)
		s.rejectOrganizerSignIn(w, r)
		return
	}

	sess := Session{
		Subject:   idToken.Subject,
		Email:     email,
		Name:      claims.Name,
		ExpiresAt: time.Now().Add(s.cfg.SessionTTL).Unix(),
	}
	if err := s.setSessionCookie(w, sess); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.log.Info("organizer signed in", "email", email)
	http.Redirect(w, r, pending.Return, http.StatusFound)
}

// A verified spectator can keep browsing the public event list. Do not put
// their email in a redirect URL or retain a previous organizer session.
func (s *Server) rejectOrganizerSignIn(w http.ResponseWriter, r *http.Request) {
	s.clearCookie(w, sessionCookie)
	http.Redirect(w, r, "/?signin=not-organizer", http.StatusSeeOther)
}

func (s *Server) readOAuthState(r *http.Request) (oauthState, error) {
	c, err := r.Cookie(oauthCookie)
	if err != nil {
		return oauthState{}, errors.New("no pending sign-in")
	}
	body, sig, ok := strings.Cut(c.Value, ".")
	if !ok || !hmac.Equal([]byte(sig), []byte(sign(s.cfg.SessionSecret, body))) {
		return oauthState{}, errors.New("sign-in cookie is not valid")
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return oauthState{}, err
	}
	var st oauthState
	if err := json.Unmarshal(raw, &st); err != nil {
		return oauthState{}, err
	}
	if time.Now().Unix() >= st.ExpiresAt {
		return oauthState{}, errors.New("sign-in attempt expired")
	}
	st.Return = safeReturnPath(st.Return)
	return st, nil
}

// handleLogout clears the local session. Google's own session is untouched,
// which is the documented limit of "log out" here.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if err := s.checkCSRF(r); err != nil {
		s.renderError(w, r, http.StatusForbidden, err.Error())
		return
	}
	s.clearCookie(w, sessionCookie)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleDevLogin signs in without Google. It exists so the app can be built and
// rehearsed offline, and config.validate refuses to start a production process
// with it enabled.
func (s *Server) handleDevLogin(w http.ResponseWriter, r *http.Request) {
	if s.cfg.IsProduction() || s.cfg.DevOrganizer == "" {
		http.NotFound(w, r)
		return
	}
	sess := Session{
		Subject:   "dev|" + s.cfg.DevOrganizer,
		Email:     strings.ToLower(s.cfg.DevOrganizer),
		Name:      "Development organizer",
		ExpiresAt: time.Now().Add(s.cfg.SessionTTL).Unix(),
	}
	if err := s.setSessionCookie(w, sess); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.log.Warn("development sign-in used", "email", sess.Email)
	http.Redirect(w, r, safeReturnPath(r.URL.Query().Get("return")), http.StatusSeeOther)
}

// safeReturnPath keeps redirects on this site: an absolute or protocol-relative
// URL from a query parameter is an open redirect waiting to happen.
func safeReturnPath(raw string) string {
	if raw == "" || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") ||
		strings.Contains(raw, "\\") || strings.ContainsFunc(raw, unicode.IsControl) {
		return "/"
	}
	return raw
}

func randomToken() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
