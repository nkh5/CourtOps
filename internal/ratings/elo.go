// Package ratings holds the provisional local rating maths.
//
// These are project-specific numbers, not a claim about anybody's real skill.
// A doubles result says something about the pair, and this model spreads the
// change evenly over both partners because it cannot tell them apart.
package ratings

import "math"

// K is the update factor. 32 is the common club-level choice: fast enough that
// a mis-seeded player converges within an evening, slow enough that one upset
// does not throw the queue.
const K = 32

// Skill band seeds used when a roster is imported. They are starting points for
// matchmaking, not measurements.
const (
	SeedBeginner     = 800
	SeedIntermediate = 1000
	SeedAdvanced     = 1200
)

// Expected returns the probability that a team rated a beats a team rated b.
func Expected(a, b float64) float64 {
	return 1 / (1 + math.Pow(10, (b-a)/400))
}

// Update is the rating change for one completed match.
type Update struct {
	// DeltaA is added to each player on team A and subtracted from each player
	// on team B, so the four-player total is unchanged.
	DeltaA float64
	// ExpectedA is team A's win probability before the match was played.
	ExpectedA float64
}

// DeltaB is the change applied to each team B player.
func (u Update) DeltaB() float64 { return -u.DeltaA }

// TeamUpdate computes the rating change for a decisive doubles result.
//
// Teams are compared by mean rating, so a 1200+800 pair is treated the same as
// a 1000+1000 pair. That is a real limitation of the model and is documented
// rather than worked around.
func TeamUpdate(teamA, teamB [2]float64, teamAWon bool) Update {
	a := mean(teamA)
	b := mean(teamB)

	expectedA := Expected(a, b)
	actualA := 0.0
	if teamAWon {
		actualA = 1
	}

	return Update{
		DeltaA:    K * (actualA - expectedA),
		ExpectedA: expectedA,
	}
}

// Apply returns the four post-match ratings in (A1, A2, B1, B2) order.
func Apply(teamA, teamB [2]float64, u Update) [4]float64 {
	return [4]float64{
		teamA[0] + u.DeltaA,
		teamA[1] + u.DeltaA,
		teamB[0] + u.DeltaB(),
		teamB[1] + u.DeltaB(),
	}
}

func mean(t [2]float64) float64 { return (t[0] + t[1]) / 2 }
