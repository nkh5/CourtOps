// Package matchmaking chooses the next doubles match for an event.
//
// Everything here is a pure function over a slice of candidates: no HTTP, no
// database, no clock. That keeps the selection rules cheap to test and easy to
// explain, which is the whole point of pulling it out of the store layer.
package matchmaking

import (
	"errors"
	"math"
	"sort"
	"time"
)

// PlayersPerMatch is fixed: this build only runs doubles.
const PlayersPerMatch = 4

// ErrNotEnoughPlayers is returned when fewer than four players are eligible.
var ErrNotEnoughPlayers = errors.New("matchmaking: fewer than four eligible players")

// Candidate is one player who is ready to be put on a court.
//
// The caller is responsible for filtering: only players whose event state is
// "ready" should ever reach this package.
type Candidate struct {
	PlayerID   int64
	Name       string
	Rating     float64
	ReadySince time.Time
}

// Assignment is the chosen four players, already split into two teams.
type Assignment struct {
	TeamA [2]Candidate
	TeamB [2]Candidate
	// Gap is the absolute difference between the two team average ratings.
	Gap float64
}

// Players returns the four chosen players in team order (A1, A2, B1, B2).
func (a Assignment) Players() [4]Candidate {
	return [4]Candidate{a.TeamA[0], a.TeamA[1], a.TeamB[0], a.TeamB[1]}
}

// splits are the three ways to divide four players into two doubles teams.
// Each entry is {teamA[0], teamA[1], teamB[0], teamB[1]} as indexes into the
// waiting-order slice. Trying all three is three constant-size comparisons,
// not a search.
var splits = [3][4]int{
	{0, 1, 2, 3},
	{0, 2, 1, 3},
	{0, 3, 1, 2},
}

// SelectNext picks the four longest-waiting candidates and returns the most
// balanced of the three possible team splits.
//
// Waiting order wins over balance on purpose: a player is never skipped to make
// the teams closer. Balance only decides how the four are paired up.
//
// Cost is the O(n log n) sort plus three fixed comparisons.
func SelectNext(candidates []Candidate) (Assignment, error) {
	if len(candidates) < PlayersPerMatch {
		return Assignment{}, ErrNotEnoughPlayers
	}

	queue := make([]Candidate, len(candidates))
	copy(queue, candidates)
	sort.Slice(queue, func(i, j int) bool {
		return lessByWait(queue[i], queue[j])
	})

	var four [4]Candidate
	copy(four[:], queue[:PlayersPerMatch])

	best := Assignment{}
	bestGap := math.Inf(1)
	for _, split := range splits {
		a := [2]Candidate{four[split[0]], four[split[1]]}
		b := [2]Candidate{four[split[2]], four[split[3]]}
		gap := math.Abs(average(a) - average(b))
		// Strictly less: on a tie the earlier split in the fixed order wins,
		// so the same four players always produce the same teams.
		if gap < bestGap {
			bestGap = gap
			best = Assignment{TeamA: a, TeamB: b, Gap: gap}
		}
	}
	return best, nil
}

// lessByWait orders by longest wait first, then by player ID so that equal
// timestamps still produce one stable answer.
func lessByWait(a, b Candidate) bool {
	if !a.ReadySince.Equal(b.ReadySince) {
		return a.ReadySince.Before(b.ReadySince)
	}
	return a.PlayerID < b.PlayerID
}

func average(t [2]Candidate) float64 {
	return (t[0].Rating + t[1].Rating) / 2
}
