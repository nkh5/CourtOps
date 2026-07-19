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

func sum(v [4]float64) float64 { return v[0] + v[1] + v[2] + v[3] }
