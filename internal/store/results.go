package store

import (
	"context"
	"github.com/jackc/pgx/v5"
	"sort"
	"time"
)

// ResultPlayer deliberately excludes private identifiers and registration answers.
type ResultPlayer struct {
	ID                int64
	Name              string
	Wins, Games, Rank int
	Rating            float64
	RatedGames        int
	CheckedInAt       *time.Time
}

func (p ResultPlayer) WinRate() int {
	if p.Games == 0 {
		return 0
	}
	return p.Wins * 100 / p.Games
}

type EventResults struct {
	Event                                                        Event
	Registered, Arrived, AtEnd, WholeEvent, Completed, Cancelled int
	AttendanceRecorded                                           bool
	Longest                                                      time.Duration
	Length                                                       time.Duration
	LengthRecorded                                               bool
	Players, Leaders                                             []ResultPlayer
	Matches                                                      []Match
}

// Results reads all matches, not the eight-game preview used on the live board.
func (s *Store) Results(ctx context.Context, eventID int64) (EventResults, error) {
	var out EventResults
	err := s.inTxOptions(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out.Event, err = getEvent(ctx, tx, eventID)
		if err != nil {
			return err
		}
		if out.Event.State != EventFinished {
			return precondition("results will be available when this event finishes")
		}
		var started, finished *time.Time
		if err := tx.QueryRow(ctx, `SELECT started_at, finished_at FROM events WHERE id=$1`, eventID).Scan(&started, &finished); err != nil {
			return err
		}
		out.AttendanceRecorded = started != nil
		if started != nil && finished != nil && !finished.Before(*started) {
			out.Length = finished.Sub(*started)
			out.LengthRecorded = true
		}
		if err := tx.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE checked_in_at IS NOT NULL),
   count(*) FILTER (WHERE checked_in_at IS NOT NULL AND state <> 'left'),
   count(*) FILTER (WHERE stayed_whole_event AND checked_in_at IS NOT NULL AND state <> 'left')
   FROM event_players WHERE event_id=$1`, eventID).Scan(&out.Registered, &out.Arrived, &out.AtEnd, &out.WholeEvent); err != nil {
			return err
		}
		roster, err := rosterInTx(ctx, tx, eventID)
		if err != nil {
			return err
		}
		out.Matches, err = matchesWhere(ctx, tx, `m.event_id=$1 AND m.state <> 'active' ORDER BY m.ended_at DESC, m.id DESC`, eventID)
		if err != nil {
			return err
		}
		byID := make(map[int64]*ResultPlayer)
		for _, p := range roster {
			byID[p.PlayerID] = &ResultPlayer{ID: p.PlayerID, Name: p.DisplayName, Rating: p.Rating, RatedGames: p.RatedGames, CheckedInAt: p.CheckedInAt}
		}
		for _, m := range out.Matches {
			if m.State == MatchCancelled {
				out.Cancelled++
				continue
			}
			out.Completed++
			if m.EndedAt != nil && m.EndedAt.Sub(m.StartedAt) > out.Longest {
				out.Longest = m.EndedAt.Sub(m.StartedAt)
			}
			for _, mp := range m.Players {
				if p := byID[mp.PlayerID]; p != nil {
					p.Games++
					if m.WinningTeam != nil && mp.Team == *m.WinningTeam {
						p.Wins++
					}
				}
			}
		}
		for _, p := range byID {
			out.Players = append(out.Players, *p)
			if p.Games > 0 {
				out.Leaders = append(out.Leaders, *p)
			}
		}
		sort.Slice(out.Players, func(i, j int) bool {
			a, b := out.Players[i], out.Players[j]
			if a.Name == b.Name {
				return a.ID < b.ID
			}
			return a.Name < b.Name
		})
		sort.Slice(out.Leaders, func(i, j int) bool {
			a, b := out.Leaders[i], out.Leaders[j]
			if a.Wins != b.Wins {
				return a.Wins > b.Wins
			}
			if a.Wins*b.Games != b.Wins*a.Games {
				return a.Wins*b.Games > b.Wins*a.Games
			}
			if a.Rating != b.Rating {
				return a.Rating > b.Rating
			}
			// Missing historical attendance follows known check-ins. Bulk
			// check-ins can share a timestamp; player ID is the stable fallback.
			if a.CheckedInAt == nil || b.CheckedInAt == nil {
				if a.CheckedInAt != b.CheckedInAt {
					return a.CheckedInAt != nil
				}
			} else if !a.CheckedInAt.Equal(*b.CheckedInAt) {
				return a.CheckedInAt.Before(*b.CheckedInAt)
			}
			return a.ID < b.ID
		})
		for i := range out.Leaders {
			out.Leaders[i].Rank = i + 1
		}
		if len(out.Leaders) > 5 {
			out.Leaders = out.Leaders[:5]
		}

		return nil
	})
	return out, err
}
