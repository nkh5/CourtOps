package web

import (
	"errors"
	"net/http"
	"strings"

	"github.com/nicholashu/courtops/internal/roster"
	"github.com/nicholashu/courtops/internal/store"
)

// importView is what the preview page shows.
type importView struct {
	EventID int64
	Result  roster.Result
	// Raw is the uploaded file, carried in a hidden field so that confirming
	// the import re-parses the actual file rather than trusting a summary the
	// browser sends back.
	Raw     string
	Applied *store.ImportResult
}

// handleImport runs both halves of the roster upload.
//
// Step one takes the file, validates every row and shows a preview. Step two
// re-parses the same bytes and, only if the file is completely clean, writes it
// in a single transaction. A file with any bad row imports nothing: a
// half-imported roster is worse than a rejected one because nobody can tell
// which half is missing.
func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	eventID, ok := s.eventID(w, r)
	if !ok {
		return
	}

	raw, err := s.readUpload(r)
	if err != nil {
		s.importProblem(w, r, eventID, err)
		return
	}

	result, err := roster.Parse(strings.NewReader(raw))
	if err != nil {
		s.importProblem(w, r, eventID, err)
		return
	}

	confirmed := r.PostFormValue("confirm") == "1"
	if !confirmed || !result.OK() {
		s.renderImportPreview(w, r, eventID, importView{EventID: eventID, Result: result, Raw: raw}, result.OK())
		return
	}

	applied, err := s.store.ImportRoster(r.Context(), eventID, result.Entries)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	s.log.Info("roster imported",
		"event_id", eventID,
		"players_created", applied.PlayersCreated,
		"players_matched", applied.PlayersMatched,
		"added", applied.MembershipsAdded,
		"already_present", applied.MembershipsSkipped)

	if wantsJSON(r) {
		writeJSON(w, http.StatusOK, map[string]any{
			"players_created":   applied.PlayersCreated,
			"players_matched":   applied.PlayersMatched,
			"added_to_event":    applied.MembershipsAdded,
			"already_on_roster": applied.MembershipsSkipped,
			"duplicate_rows":    result.Duplicates,
		})
		return
	}

	view := importView{EventID: eventID, Result: result, Applied: &applied}
	data := s.newPage(w, r, "Roster imported")
	data.Import = &view
	snap, snapErr := s.store.Snapshot(r.Context(), eventID)
	if snapErr == nil {
		data.Snapshot = &snap
	}
	s.render(w, r, http.StatusOK, "import.html", data)
}

// readUpload returns the CSV text from either the file field or the hidden
// field on the preview page.
func (s *Server) readUpload(r *http.Request) (string, error) {
	// organizerOnly has already bounded and parsed the body before CSRF.
	if raw := r.PostFormValue("csv"); raw != "" {
		if len(raw) > roster.MaxBytes {
			return "", roster.ErrTooLarge
		}
		return raw, nil
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		return "", errors.New("no file was uploaded; choose the CSV exported from the response sheet")
	}
	defer file.Close()
	if header.Size > roster.MaxBytes {
		return "", roster.ErrTooLarge
	}

	buf := new(strings.Builder)
	if _, err := copyLimited(buf, file, roster.MaxBytes+1); err != nil {
		return "", err
	}
	if buf.Len() > roster.MaxBytes {
		return "", roster.ErrTooLarge
	}
	return buf.String(), nil
}

func (s *Server) renderImportPreview(w http.ResponseWriter, r *http.Request, eventID int64, view importView, ok bool) {
	status := http.StatusOK
	if !ok {
		status = http.StatusUnprocessableEntity
	}
	if wantsJSON(r) {
		rows := make([]map[string]any, 0, len(view.Result.Errors))
		for _, e := range view.Result.Errors {
			rows = append(rows, map[string]any{"row": e.Row, "problem": e.Message})
		}
		writeJSON(w, status, map[string]any{
			"valid_rows": len(view.Result.Entries),
			"errors":     rows,
			"importable": ok,
		})
		return
	}

	data := s.newPage(w, r, "Check the roster")
	data.Import = &view
	if snap, err := s.store.Snapshot(r.Context(), eventID); err == nil {
		data.Snapshot = &snap
	}
	s.render(w, r, status, "import.html", data)
}

func (s *Server) importProblem(w http.ResponseWriter, r *http.Request, eventID int64, err error) {
	msg := err.Error()
	if errors.Is(err, roster.ErrTooLarge) {
		msg = "That file is larger than 1 MB. This version expects a club roster, not a full export."
	}
	if errors.Is(err, roster.ErrTooManyRows) {
		msg = "That file has more than 100 rows, which is more than this version supports."
	}
	s.respondError(w, r, http.StatusUnprocessableEntity, msg)
}
