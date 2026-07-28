package store

import "time"

// Event states.
const (
	EventSetup    = "setup"
	EventLive     = "live"
	EventFinished = "finished"
)

// Player states within one event.
const (
	StateReady   = "ready"
	StateResting = "resting"
	StatePlaying = "playing"
	StateLeft    = "left"
)

// Match states.
const (
	MatchActive    = "active"
	MatchCompleted = "completed"
	MatchCancelled = "cancelled"
)

// Event is one club session on one date.
type Event struct {
	ID        int64
	Title     string
	Date      time.Time
	State     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (e Event) IsLive() bool { return e.State == EventLive }

// Player is a person who has ever been imported. Identity is reused across
// events so ratings can accumulate.
type Player struct {
	ID          int64
	DisplayName string
	PrivateRef  string // never rendered on a public page
	SkillSeed   int
	Rating      float64
	RatedGames  int
}

// EventPlayer is one player's membership and current state in one event.
//
// Note what is missing: PrivateRef. Public responses are built from this type,
// so a private identifier cannot leak into the board by accident.
type EventPlayer struct {
	PlayerID    int64
	DisplayName string
	State       string
	ReadySince  *time.Time
	CheckedInAt *time.Time
	Rating      float64
	RatedGames  int
}

// Registration contains organizer-only answers for one event. It deliberately
// does not appear in Snapshot or EventPlayer, which feed the public court board.
type Registration struct {
	EstimatedArrival string
	NeedsPaddle      *bool
}

// WaitingFor reports how long a ready player has been in the queue.
func (p EventPlayer) WaitingFor(now time.Time) time.Duration {
	if p.ReadySince == nil {
		return 0
	}
	return now.Sub(*p.ReadySince)
}

// Court belongs to exactly one event.
type Court struct {
	ID          int64
	EventID     int64
	Label       string
	Available   bool
	ActiveMatch *Match
}

// Match is one game on one court.
type Match struct {
	ID          int64
	EventID     int64
	CourtID     int64
	CourtLabel  string
	State       string
	WinningTeam *int
	StartedAt   time.Time
	EndedAt     *time.Time
	Players     []MatchPlayer
}

// Team returns the members of team 1 or team 2 in a stable order.
func (m Match) Team(n int) []MatchPlayer {
	out := make([]MatchPlayer, 0, 2)
	for _, p := range m.Players {
		if p.Team == n {
			out = append(out, p)
		}
	}
	return out
}

// MatchPlayer is one player's place in one match.
type MatchPlayer struct {
	PlayerID     int64
	DisplayName  string
	Team         int
	Active       bool
	RatingBefore *float64
	RatingAfter  *float64
}

// Delta is the rating change this match produced, if ratings were recorded.
func (p MatchPlayer) Delta() *float64 {
	if p.RatingBefore == nil || p.RatingAfter == nil {
		return nil
	}
	d := *p.RatingAfter - *p.RatingBefore
	return &d
}

// Snapshot is everything a page needs about an event in one read.
type Snapshot struct {
	Event     Event
	Courts    []Court
	Roster    []EventPlayer // every member, in display order
	Queue     []EventPlayer // ready players, longest wait first
	Recent    []Match       // most recently finished matches
	FetchedAt time.Time
}

// CountByState is used by the templates for the small status line.
func (s Snapshot) CountByState(state string) int {
	n := 0
	for _, p := range s.Roster {
		if p.State == state {
			n++
		}
	}
	return n
}

// ImportResult summarises what a roster upload actually did.
type ImportResult struct {
	PlayersCreated     int
	PlayersMatched     int
	MembershipsAdded   int
	MembershipsSkipped int // already on this event's roster
}
