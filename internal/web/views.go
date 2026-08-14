package web

import (
	"net/url"
	"time"

	"github.com/nicholashu/courtops/internal/store"
)

// The view types below are the JSON contract. They exist separately from the
// store types so that adding a column to the database does not silently publish
// it, and so the public board and the organizer page can differ by construction
// rather than by a template remembering to omit a field.

type eventView struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	Date  string `json:"date"`
	State string `json:"state"`
}

type playerView struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	State       string  `json:"state"`
	WaitSeconds int     `json:"wait_seconds,omitempty"`
	Rating      float64 `json:"rating,omitempty"`
	RatedGames  int     `json:"rated_games,omitempty"`
}

type matchPlayerView struct {
	ID     int64    `json:"id"`
	Name   string   `json:"name"`
	Team   int      `json:"team"`
	Change *float64 `json:"rating_change,omitempty"`
}

type matchView struct {
	ID        int64             `json:"id"`
	CourtID   int64             `json:"court_id"`
	Court     string            `json:"court"`
	State     string            `json:"state"`
	Winner    *int              `json:"winning_team,omitempty"`
	StartedAt time.Time         `json:"started_at"`
	EndedAt   *time.Time        `json:"ended_at,omitempty"`
	Players   []matchPlayerView `json:"players"`
}

type courtView struct {
	ID        int64      `json:"id"`
	Label     string     `json:"label"`
	Available bool       `json:"available"`
	Match     *matchView `json:"match,omitempty"`
}

type snapshotView struct {
	Event     eventView      `json:"event"`
	Courts    []courtView    `json:"courts"`
	Queue     []playerView   `json:"queue"`
	Roster    []playerView   `json:"roster,omitempty"`
	Recent    []matchView    `json:"recent"`
	FetchedAt time.Time      `json:"fetched_at"`
	Stats     eventStatsView `json:"stats"`
}

type eventStatsView struct {
	CheckedIn       int `json:"checked_in"`
	Queue           int `json:"queue"`
	OnCourt         int `json:"on_court"`
	CourtsAvailable int `json:"courts_available"`
	CourtsTotal     int `json:"courts_total"`
}

func eventStats(s store.Snapshot) eventStatsView {
	v := eventStatsView{Queue: len(s.Queue), OnCourt: s.CountByState(store.StatePlaying), CourtsTotal: len(s.Courts)}
	for _, p := range s.Roster {
		if p.CheckedInAt != nil && p.State != store.StateLeft {
			v.CheckedIn++
		}
	}
	for _, c := range s.Courts {
		if c.Available && c.ActiveMatch == nil {
			v.CourtsAvailable++
		}
	}
	return v
}

// publicView is everything a spectator may see: display names, who is on which
// court, and who is waiting. No email addresses, no club IDs, no ratings.
func publicView(s store.Snapshot) snapshotView {
	v := snapshotView{
		Stats:     eventStats(s),
		Event:     eventView{ID: s.Event.ID, Title: s.Event.Title, Date: s.Event.Date.Format("2006-01-02"), State: s.Event.State},
		FetchedAt: s.FetchedAt,
		Queue:     make([]playerView, 0, len(s.Queue)),
		Courts:    make([]courtView, 0, len(s.Courts)),
		Recent:    make([]matchView, 0, len(s.Recent)),
	}
	for _, p := range s.Queue {
		v.Queue = append(v.Queue, playerView{
			ID: p.PlayerID, Name: p.DisplayName, State: p.State,
			WaitSeconds: int(p.WaitingFor(s.FetchedAt).Seconds()),
		})
	}
	for _, c := range s.Courts {
		cv := courtView{ID: c.ID, Label: c.Label, Available: c.Available}
		if c.ActiveMatch != nil {
			m := matchViewOf(*c.ActiveMatch, false)
			cv.Match = &m
		}
		v.Courts = append(v.Courts, cv)
	}
	for _, m := range s.Recent {
		v.Recent = append(v.Recent, matchViewOf(m, false))
	}
	return v
}

// organizerView adds the roster and the rating columns.
func organizerView(s store.Snapshot, withRatings bool) snapshotView {
	v := publicView(s)
	v.Roster = make([]playerView, 0, len(s.Roster))
	for _, p := range s.Roster {
		pv := playerView{
			ID: p.PlayerID, Name: p.DisplayName, State: p.State,
			WaitSeconds: int(p.WaitingFor(s.FetchedAt).Seconds()),
		}
		if withRatings {
			pv.Rating = p.Rating
			pv.RatedGames = p.RatedGames
		}
		v.Roster = append(v.Roster, pv)
	}
	if withRatings {
		for i, m := range s.Recent {
			v.Recent[i] = matchViewOf(m, true)
		}
	}
	return v
}

func matchViewOf(m store.Match, withRatings bool) matchView {
	mv := matchView{
		ID: m.ID, CourtID: m.CourtID, Court: m.CourtLabel, State: m.State,
		Winner: m.WinningTeam, StartedAt: m.StartedAt, EndedAt: m.EndedAt,
		Players: make([]matchPlayerView, 0, len(m.Players)),
	}
	for _, p := range m.Players {
		pv := matchPlayerView{ID: p.PlayerID, Name: p.DisplayName, Team: p.Team}
		if withRatings {
			pv.Change = p.Delta()
		}
		mv.Players = append(mv.Players, pv)
	}
	return mv
}

func urlQueryEscape(s string) string { return url.QueryEscape(s) }
