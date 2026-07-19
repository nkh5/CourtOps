package ratings

import (
	"math"
	"testing"
)

const tolerance = 1e-9

func TestEqualTeamsMoveBySixteen(t *testing.T) {
	a := [2]float64{1000, 1000}
	b := [2]float64{1000, 1000}

	u := TeamUpdate(a, b, true)
	if math.Abs(u.ExpectedA-0.5) > tolerance {
		t.Errorf("expectedA = %v, want 0.5", u.ExpectedA)
	}
	if math.Abs(u.DeltaA-16) > tolerance {
		t.Errorf("deltaA = %v, want +16", u.DeltaA)
	}
	if math.Abs(u.DeltaB()+16) > tolerance {
		t.Errorf("deltaB = %v, want -16", u.DeltaB())
	}

	got := Apply(a, b, u)
	want := [4]float64{1016, 1016, 984, 984}
	for i := range want {
		if math.Abs(got[i]-want[i]) > tolerance {
			t.Fatalf("Apply() = %v, want %v", got, want)
		}
	}
}

func TestFavoredTeamGainsLessThanUnderdog(t *testing.T) {
	strong := [2]float64{1200, 1200}
	weak := [2]float64{800, 800}

	favoredWins := TeamUpdate(strong, weak, true).DeltaA
	underdogWins := TeamUpdate(weak, strong, true).DeltaA

	if favoredWins <= 0 || underdogWins <= 0 {
		t.Fatalf("a winner should always gain: favored %v, underdog %v", favoredWins, underdogWins)
	}
	if favoredWins >= underdogWins {
		t.Errorf("favored gain %v should be smaller than underdog gain %v", favoredWins, underdogWins)
	}
}

func TestRatingTotalIsConserved(t *testing.T) {
	cases := []struct {
		name string
		a, b [2]float64
		aWon bool
	}{
		{"equal", [2]float64{1000, 1000}, [2]float64{1000, 1000}, true},
		{"mismatched", [2]float64{1247.5, 803.25}, [2]float64{998, 1101.75}, false},
		{"wide gap", [2]float64{1600, 1550}, [2]float64{700, 640}, true},
		{"identical pairs, b wins", [2]float64{900, 1100}, [2]float64{1100, 900}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := tc.a[0] + tc.a[1] + tc.b[0] + tc.b[1]
			after := sum(Apply(tc.a, tc.b, TeamUpdate(tc.a, tc.b, tc.aWon)))
			if math.Abs(after-before) > 1e-9 {
				t.Errorf("total moved from %v to %v", before, after)
			}
		})
	}
}

func sum(v [4]float64) float64 { return v[0] + v[1] + v[2] + v[3] }
