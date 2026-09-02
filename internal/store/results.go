package store

import (
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
