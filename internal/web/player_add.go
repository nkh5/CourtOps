package web

import (
	"fmt"
	"net/http"

	"github.com/nicholashu/courtops/internal/roster"
)

// handleAddPlayer is protected by organizerOnly, including its CSRF and
// read-only checks. The store commits identity, registration and check-in as
// one action so an arriving player never appears in a half-created state.
func (s *Server) handleAddPlayer(w http.ResponseWriter, r *http.Request) {
	eventID, ok := s.eventID(w, r)
	if !ok {
		return
	}
	entry, err := roster.NewEntry(r.PostFormValue("display_name"), r.PostFormValue("private_ref"),
		r.PostFormValue("skill_band"), r.PostFormValue("estimated_arrival"), r.PostFormValue("needs_paddle"))
	if err != nil {
		s.respondError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	checkInRaw := r.PostFormValue("check_in")
	if checkInRaw != "" && checkInRaw != "1" {
		s.respondError(w, r, http.StatusBadRequest, "Check in immediately must be selected or left unchecked.")
		return
	}
	player, err := s.store.AddPlayer(r.Context(), eventID, entry, checkInRaw == "1")
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	s.log.Info("player added to event", "event_id", eventID, "player_id", player.PlayerID, "checked_in", checkInRaw == "1")
	if wantsJSON(r) {
		writeJSON(w, http.StatusCreated, playerView{ID: player.PlayerID, Name: player.DisplayName, State: player.State})
		return
	}
	notice := fmt.Sprintf("Added %s to the event roster.", player.DisplayName)
	if checkInRaw == "1" {
		notice = fmt.Sprintf("Added %s and checked them into the queue.", player.DisplayName)
	}
	s.redirectBack(w, r, eventID, notice)
}
