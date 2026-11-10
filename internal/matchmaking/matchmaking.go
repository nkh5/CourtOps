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

// WaitWindow is measured from the oldest remaining ready player at each pick.
const WaitWindow = 7 * time.Minute

// History counts completed games in this event for pairs of players.
// Keys contain the lower ID first. Cancelled matches do not count.
type Meetings struct{ Shared, Partners int }
type History map[[2]int64]Meetings

func (h History) between(a, b int64) Meetings {
	if a > b {
		a, b = b, a
	}
	return h[[2]int64{a, b}]
}

// SelectNext guarantees the oldest player a place. For the remaining places,
// similar waits favor players with fewer shared games with those already picked.
func SelectNext(candidates []Candidate, history History) (Assignment, error) {
	if len(candidates) < PlayersPerMatch {
		return Assignment{}, ErrNotEnoughPlayers
	}

	queue := make([]Candidate, len(candidates))
	copy(queue, candidates)
	sort.Slice(queue, func(i, j int) bool {
		return lessByWait(queue[i], queue[j])
	})

	var four [4]Candidate
	for slot := range four {
		// The first pick has no prior players to compare, so FIFO wins.
		cutoff := queue[0].ReadySince.Add(WaitWindow)
		bestIndex, bestShared := 0, math.MaxInt
		for i, candidate := range queue {
			if candidate.ReadySince.After(cutoff) {
				break
			}
			shared := 0
			for _, picked := range four[:slot] {
				shared += history.between(candidate.PlayerID, picked.PlayerID).Shared
			}
			if shared < bestShared {
				bestIndex, bestShared = i, shared
				if shared == 0 {
					break
				}
			}
		}
		four[slot] = queue[bestIndex]
		queue = append(queue[:bestIndex], queue[bestIndex+1:]...)
	}

	best := Assignment{}
	bestGap, bestPartners := math.Inf(1), math.MaxInt
	for _, split := range splits {
		a := [2]Candidate{four[split[0]], four[split[1]]}
		b := [2]Candidate{four[split[2]], four[split[3]]}
		gap := math.Abs(average(a) - average(b))
		partners := history.between(a[0].PlayerID, a[1].PlayerID).Partners +
			history.between(b[0].PlayerID, b[1].PlayerID).Partners
		if gap < bestGap || (gap == bestGap && partners < bestPartners) {
			bestGap, bestPartners = gap, partners
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
