package web

import (
	"bytes"
	"fmt"
	"net/http"
	"time"

	"github.com/nicholashu/courtops/internal/store"
)

// pageData is the root value for every HTML template.
type pageData struct {
	Title     string
	Session   *Session
	SignedIn  bool
	CSRF      string
	Notice    string
	Problem   string
	ReadOnly  bool
	CanSignIn bool
	DevLogin  bool
	Ratings   bool
	Version   string
	Now       time.Time

	Results       *store.EventResults
	History       *store.PlayerHistory
	HistoryPage   int
	Search        string
	FoundPlayers  []store.ResultPlayer
	Events        []store.Event
	Snapshot      *store.Snapshot
	Import        *importView
	Match         *store.Match
	Status        int
	Registrations map[int64]store.Registration
}

func (s *Server) newPage(w http.ResponseWriter, r *http.Request, title string) pageData {
	p := pageData{
		Title:     title,
		CSRF:      s.csrfToken(w, r),
		Notice:    r.URL.Query().Get("notice"),
		Problem:   r.URL.Query().Get("problem"),
		ReadOnly:  s.cfg.ReadOnly,
		CanSignIn: s.auth != nil,
		DevLogin:  !s.cfg.IsProduction() && s.cfg.DevOrganizer != "",
		Ratings:   s.cfg.RatingsEnabled,
		Version:   s.version,
		Now:       time.Now(),
	}
	if sess, ok := s.sessionFrom(r); ok {
		p.Session = &sess
		p.SignedIn = true
	}
	return p
}

// render writes a template to a buffer first, so a template error becomes a
// clean 500 instead of a half-written page.
func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, name string, data pageData) {
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		s.log.Error("template failed", "template", name, "error", err)
		http.Error(w, "The page could not be rendered.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

func (s *Server) renderError(w http.ResponseWriter, r *http.Request, status int, msg string) {
	data := s.newPage(w, r, "Problem")
	data.Problem = msg
	data.Status = status
	s.render(w, r, status, "error.html", data)
}

// handleIndex lists events and, for an organizer, offers to create one.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		s.renderError(w, r, http.StatusNotFound, "There is no page at "+r.URL.Path+".")
		return
	}

	events, err := s.store.ListEvents(r.Context(), 20)
	if err != nil {
		s.storeError(w, r, err)
		return
	}

	data := s.newPage(w, r, "CourtOps")
	data.Events = events
	if r.URL.Query().Get("signin") == "not-organizer" {
		data.Problem = "You are signed in to Google, but this account is not an organizer for this deployment. You can still view events and public court boards."
	}
	s.render(w, r, http.StatusOK, "index.html", data)
}

// handleOrganizer renders the controls for one event.
func (s *Server) handleOrganizer(w http.ResponseWriter, r *http.Request) {
	eventID, err := pathInt(r, "eventID")
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	snap, err := s.store.Snapshot(r.Context(), eventID)
	if err != nil {
		s.storeError(w, r, err)
		return
	}

	data := s.newPage(w, r, snap.Event.Title)
	data.Snapshot = &snap
	data.Registrations, err = s.store.RegistrationDetails(r.Context(), eventID)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "organizer.html", data)
}

func (s *Server) handleNewPlayer(w http.ResponseWriter, r *http.Request) {
	s.renderEventAction(w, r, false)
}

func (s *Server) handleDeleteConfirmation(w http.ResponseWriter, r *http.Request) {
	s.renderEventAction(w, r, true)
}

func (s *Server) renderEventAction(w http.ResponseWriter, r *http.Request, deleting bool) {
	eventID, err := pathInt(r, "eventID")
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	snap, err := s.store.Snapshot(r.Context(), eventID)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	if s.cfg.ReadOnly {
		s.renderError(w, r, http.StatusServiceUnavailable, "This deployment is read-only.")
		return
	}
	if deleting && snap.Event.State != store.EventFinished {
		s.renderError(w, r, http.StatusUnprocessableEntity, "Only finished events can be deleted.")
		return
	}
	if !deleting && snap.Event.State == store.EventFinished {
		s.renderError(w, r, http.StatusUnprocessableEntity, "This event is finished. Players can no longer be added.")
		return
	}
	title, tmpl := "Add player", "player_add.html"
	if deleting {
		title, tmpl = "Delete event", "delete.html"
	}
	data := s.newPage(w, r, title)
	data.Snapshot = &snap
	s.render(w, r, http.StatusOK, tmpl, data)
}

// handleBoard renders the public court board.
//
// With ?fragment=1 it returns just the board body, which is what the page's
// small polling script swaps in. One rendering path, in Go, for both.
func (s *Server) handleBoard(w http.ResponseWriter, r *http.Request) {
	eventID, err := pathInt(r, "eventID")
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	snap, err := s.store.Snapshot(r.Context(), eventID)
	if err != nil {
		s.storeError(w, r, err)
		return
	}


	data := s.newPage(w, r, snap.Event.Title+" — court board")
	data.Snapshot = &snap

	if r.URL.Query().Get("fragment") == "1" {
		s.render(w, r, http.StatusOK, "board_body.html", data)
		return
	}
	s.render(w, r, http.StatusOK, "board.html", data)
}

// handleBoardJSON is the public read model.
//
// It is built from store.EventPlayer, which has no private_ref field, so a
// private identifier cannot reach this response even by mistake.
func (s *Server) handleBoardJSON(w http.ResponseWriter, r *http.Request) {
	eventID, err := pathInt(r, "eventID")
	if err != nil {
		s.respondError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	snap, err := s.store.Snapshot(r.Context(), eventID)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, publicView(snap))
}

// handleOrganizerJSON is the same event with the organizer's extra columns.
func (s *Server) handleOrganizerJSON(w http.ResponseWriter, r *http.Request) {
	eventID, err := pathInt(r, "eventID")
	if err != nil {
		s.respondError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	snap, err := s.store.Snapshot(r.Context(), eventID)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, organizerView(snap, s.cfg.RatingsEnabled))
}

// handleConfirmResult shows who is on court and asks for the winner. Session 16
// calls for an explicit confirmation step because a result cannot be edited
// once ratings have been applied.
func (s *Server) handleConfirmResult(w http.ResponseWriter, r *http.Request) {
	eventID, err := pathInt(r, "eventID")
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	matchID, err := pathInt(r, "matchID")
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest, err.Error())
		return
	}

	match, err := s.store.GetMatch(r.Context(), eventID, matchID)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	snap, err := s.store.Snapshot(r.Context(), eventID)
	if err != nil {
		s.storeError(w, r, err)
		return
	}

	data := s.newPage(w, r, "Record a result")
	data.Snapshot = &snap
	data.Match = &match
	s.render(w, r, http.StatusOK, "confirm.html", data)
}

// redirectBack returns the organizer to the event page with one short message.
func (s *Server) redirectBack(w http.ResponseWriter, r *http.Request, eventID int64, notice string) {
	target := fmt.Sprintf("/events/%d", eventID)
	if notice != "" {
		target += "?notice=" + urlQueryEscape(notice)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}
