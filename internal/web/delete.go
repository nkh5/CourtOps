package web

import "net/http"

// handleDeleteEvent is registered only behind organizerOnly, which validates
// the organizer session, read-only mode and CSRF token before any deletion.
func (s *Server) handleDeleteEvent(w http.ResponseWriter, r *http.Request) {
	eventID, ok := s.eventID(w, r)
	if !ok {
		return
	}
	if r.PostFormValue("confirmation") != "DELETE" {
		s.respondError(w, r, http.StatusBadRequest, "Type DELETE exactly to permanently delete this finished event.")
		return
	}
	if err := s.store.DeleteEvent(r.Context(), eventID); err != nil {
		s.storeError(w, r, err)
		return
	}
	s.log.Info("event deleted", "event_id", eventID)
	if wantsJSON(r) {
		writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "event_id": eventID})
		return
	}
	http.Redirect(w, r, "/?notice="+urlQueryEscape("Event deleted."), http.StatusSeeOther)
}
