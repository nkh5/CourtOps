package web

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Every handler in this file follows the same shape: read and check the inputs,
// call exactly one store method, then either redirect an HTML form back to the
// event page or answer a JSON caller with the resulting object.
//
// The idempotency rules live in the store, where the transaction is. What the
// handlers add is honest reporting of what happened: "assigned" and "already on
// court" are different messages for the same 200.

func (s *Server) handleCreateEvent(w http.ResponseWriter, r *http.Request) {
	title := strings.TrimSpace(r.PostFormValue("title"))
	dateRaw := strings.TrimSpace(r.PostFormValue("date"))
	courts := splitCourts(r.PostFormValue("courts"))

	date, err := time.Parse("2006-01-02", dateRaw)
	if err != nil {
		s.respondError(w, r, http.StatusBadRequest, "The date must look like 2026-09-20.")
		return
	}

	event, err := s.store.CreateEvent(r.Context(), title, date, courts)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	s.log.Info("event created", "event_id", event.ID, "courts", len(courts))

	if wantsJSON(r) {
		writeJSON(w, http.StatusCreated, eventView{ID: event.ID, Title: event.Title,
			Date: event.Date.Format("2006-01-02"), State: event.State})
		return
	}
	s.redirectBack(w, r, event.ID, "Event created. Import the roster next.")
}

func splitCourts(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return []string{"Court 1"}
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (s *Server) handleSetEventState(w http.ResponseWriter, r *http.Request) {
	eventID, ok := s.eventID(w, r)
	if !ok {
		return
	}

	want := r.PostFormValue("state")
	event, err := s.store.SetEventState(r.Context(), eventID, want)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	s.log.Info("event state changed", "event_id", eventID, "state", event.State)

	if wantsJSON(r) {
		writeJSON(w, http.StatusOK, eventView{ID: event.ID, Title: event.Title,
			Date: event.Date.Format("2006-01-02"), State: event.State})
		return
	}
	s.redirectBack(w, r, eventID, "Event is now "+event.State+".")
}

func (s *Server) handleAddCourt(w http.ResponseWriter, r *http.Request) {
	eventID, ok := s.eventID(w, r)
	if !ok {
		return
	}
	court, err := s.store.AddCourt(r.Context(), eventID, r.PostFormValue("label"))
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	if wantsJSON(r) {
		writeJSON(w, http.StatusCreated, courtView{ID: court.ID, Label: court.Label, Available: court.Available})
		return
	}
	s.redirectBack(w, r, eventID, "Added "+court.Label+".")
}

func (s *Server) handleCourtAvailability(w http.ResponseWriter, r *http.Request) {
	eventID, ok := s.eventID(w, r)
	if !ok {
		return
	}
	courtID, err := pathInt(r, "courtID")
	if err != nil {
		s.respondError(w, r, http.StatusBadRequest, err.Error())
		return
	}

	raw := r.PostFormValue("available")
	if raw != "true" && raw != "false" {
		s.respondError(w, r, http.StatusBadRequest, "Court availability must be true or false.")
		return
	}
	available := raw == "true"
	court, err := s.store.SetCourtAvailable(r.Context(), eventID, courtID, available)
	if err != nil {
		s.storeError(w, r, err)
		return
	}

	state := "open"
	if !court.Available {
		state = "closed"
	}
	if wantsJSON(r) {
		writeJSON(w, http.StatusOK, courtView{ID: court.ID, Label: court.Label, Available: court.Available})
		return
	}
	s.redirectBack(w, r, eventID, fmt.Sprintf("%s is %s.", court.Label, state))
}

// handleSetPlayerState applies check-in, rest and left.
//
// The optional expected_state field carries what the organizer's page believed
// the player's state was. If it no longer matches, the store refuses the write
// rather than overwriting a change another organizer made a second earlier.
func (s *Server) handleSetPlayerState(w http.ResponseWriter, r *http.Request) {
	eventID, ok := s.eventID(w, r)
	if !ok {
		return
	}
	playerID, err := pathInt(r, "playerID")
	if err != nil {
		s.respondError(w, r, http.StatusBadRequest, err.Error())
		return
	}

	want := r.PostFormValue("state")
	expected := r.PostFormValue("expected_state")

	player, err := s.store.SetPlayerState(r.Context(), eventID, playerID, want, expected)
	if err != nil {
		s.storeError(w, r, err)
		return
	}

	if wantsJSON(r) {
		writeJSON(w, http.StatusOK, playerView{ID: player.PlayerID, Name: player.DisplayName, State: player.State})
		return
	}
	s.redirectBack(w, r, eventID, fmt.Sprintf("%s is %s.", player.DisplayName, player.State))
}

func (s *Server) handleCheckInAll(w http.ResponseWriter, r *http.Request) {
	eventID, ok := s.eventID(w, r)
	if !ok {
		return
	}
	n, err := s.store.CheckInAll(r.Context(), eventID)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	if wantsJSON(r) {
		writeJSON(w, http.StatusOK, map[string]int{"checked_in": n})
		return
	}
	s.redirectBack(w, r, eventID, fmt.Sprintf("Checked in %d player(s).", n))
}

// handleAssignNext is the "Next match" button.
//
// Pressing it twice, or a phone retrying a request whose answer was lost, must
// not take a second court or a second set of players. The store returns the
// existing match with created=false, and this handler says so instead of
// pretending a new match was made.
func (s *Server) handleAssignNext(w http.ResponseWriter, r *http.Request) {
	eventID, ok := s.eventID(w, r)
	if !ok {
		return
	}
	courtID, err := pathInt(r, "courtID")
	if err != nil {
		s.respondError(w, r, http.StatusBadRequest, err.Error())
		return
	}

	match, created, err := s.store.AssignNext(r.Context(), eventID, courtID)
	if err != nil {
		s.storeError(w, r, err)
		return
	}

	notice := fmt.Sprintf("Court %s already had that match; nothing new was assigned.", match.CourtLabel)
	status := http.StatusOK
	if created {
		notice = fmt.Sprintf("Match started on %s.", match.CourtLabel)
		status = http.StatusCreated
		s.log.Info("match assigned", "event_id", eventID, "match_id", match.ID, "court", match.CourtLabel)
	}

	if wantsJSON(r) {
		writeJSON(w, status, map[string]any{"created": created, "match": matchViewOf(match, s.cfg.RatingsEnabled)})
		return
	}
	s.redirectBack(w, r, eventID, notice)
}

// handleCompleteMatch records a winner.
//
// Repeating the same result is a no-op that returns the stored match. A
// different winner for a match that already has one is a 409: this version does
// not edit a confirmed result, and saying so is better than silently keeping
// the first answer.
func (s *Server) handleCompleteMatch(w http.ResponseWriter, r *http.Request) {
	eventID, ok := s.eventID(w, r)
	if !ok {
		return
	}
	matchID, err := pathInt(r, "matchID")
	if err != nil {
		s.respondError(w, r, http.StatusBadRequest, err.Error())
		return
	}

	team, err := teamNumber(r.PostFormValue("winning_team"))
	if err != nil {
		s.respondError(w, r, http.StatusBadRequest, err.Error())
		return
	}

	match, changed, err := s.store.CompleteMatch(r.Context(), eventID, matchID, team)
	if err != nil {
		s.storeError(w, r, err)
		return
	}

	notice := "That result was already saved; nothing changed."
	if changed {
		notice = fmt.Sprintf("Recorded team %d as the winner on %s.", team, match.CourtLabel)
		s.log.Info("match completed", "event_id", eventID, "match_id", match.ID,
			"winning_team", team, "ratings", s.cfg.RatingsEnabled)
	}

	if wantsJSON(r) {
		writeJSON(w, http.StatusOK, map[string]any{"changed": changed, "match": matchViewOf(match, s.cfg.RatingsEnabled)})
		return
	}
	s.redirectBack(w, r, eventID, notice)
}

// handleCancelMatch releases a court without recording a result. Ratings are
// untouched, because nothing was played.
func (s *Server) handleCancelMatch(w http.ResponseWriter, r *http.Request) {
	eventID, ok := s.eventID(w, r)
	if !ok {
		return
	}
	matchID, err := pathInt(r, "matchID")
	if err != nil {
		s.respondError(w, r, http.StatusBadRequest, err.Error())
		return
	}

	match, changed, err := s.store.CancelMatch(r.Context(), eventID, matchID)
	if err != nil {
		s.storeError(w, r, err)
		return
	}

	notice := "That match was already cancelled."
	if changed {
		notice = fmt.Sprintf("Cancelled the match on %s.", match.CourtLabel)
		s.log.Info("match cancelled", "event_id", eventID, "match_id", match.ID)
	}

	if wantsJSON(r) {
		writeJSON(w, http.StatusOK, map[string]any{"changed": changed, "match": matchViewOf(match, s.cfg.RatingsEnabled)})
		return
	}
	s.redirectBack(w, r, eventID, notice)
}

func (s *Server) eventID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := pathInt(r, "eventID")
	if err != nil {
		s.respondError(w, r, http.StatusBadRequest, err.Error())
		return 0, false
	}
	return id, true
}

func teamNumber(raw string) (int, error) {
	switch strings.TrimSpace(raw) {
	case "1":
		return 1, nil
	case "2":
		return 2, nil
	default:
		return 0, fmt.Errorf("choose team 1 or team 2 as the winner")
	}
}
