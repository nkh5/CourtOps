package web

import (
	"net/http"
	"time"

	"github.com/nicholashu/courtops/internal/matchmaking"
	"github.com/nicholashu/courtops/internal/ratings"
)

// The demo has no write routes, session, or store. Only public game rules
// reach the browser; the visitor's event never comes back to the server.
type demoRules struct {
	Beginner, Intermediate, Advanced, K int
	WaitMS                              int64
}

func (s *Server) handleDemo(w http.ResponseWriter, r *http.Request) {
	rules := demoRules{ratings.SeedBeginner, ratings.SeedIntermediate, ratings.SeedAdvanced, ratings.K, int64(matchmaking.WaitWindow / time.Millisecond)}
	s.render(w, r, http.StatusOK, "demo.html", pageData{Title: "Play Demo", Demo: &rules})
}
