package web

import (
	"net/http"
	"strconv"
	"strings"
)

func (s *Server) handleResults(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "eventID")
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	results, err := s.store.Results(r.Context(), id)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	data := s.newPage(w, r, results.Event.Title+" — Results")
	data.Results = &results
	data.Search = strings.TrimSpace(r.URL.Query().Get("q"))
	if len(data.Search) > 100 {
		s.renderError(w, r, http.StatusBadRequest, "Search by a display name of 100 characters or fewer.")
		return
	}
	data.FoundPlayers = results.Search(data.Search)
	s.render(w, r, http.StatusOK, "results.html", data)
}
func (s *Server) handlePlayerHistory(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "playerID")
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	page := 1
	if raw := r.URL.Query().Get("page"); raw != "" {
		page, err = strconv.Atoi(raw)
		if err != nil || page < 1 || page > 100000 {
			s.renderError(w, r, http.StatusBadRequest, "Invalid history page.")
			return
		}
	}
	history, err := s.store.PlayerHistory(r.Context(), id, page)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	data := s.newPage(w, r, history.Player.Name+" — Player History")
	data.History = &history
	data.HistoryPage = page
	s.render(w, r, http.StatusOK, "player_history.html", data)
}
