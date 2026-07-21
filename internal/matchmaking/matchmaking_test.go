package matchmaking

import (
	"errors"
	"math"
	"testing"
	"time"
)

var base = time.Date(2026, 9, 13, 18, 0, 0, 0, time.UTC)

// at returns a timestamp n minutes after the fixed base time. Smaller n means
// the player has been waiting longer.
func at(n int) time.Time { return base.Add(time.Duration(n) * time.Minute) }

func cand(id int64, rating float64, waitMinutes int) Candidate {
	return Candidate{PlayerID: id, Name: "p", Rating: rating, ReadySince: at(waitMinutes)}
}

func ids(a Assignment) [4]int64 {
	p := a.Players()
	return [4]int64{p[0].PlayerID, p[1].PlayerID, p[2].PlayerID, p[3].PlayerID}
}

func TestSelectNext(t *testing.T) {
	tests := []struct {
		name       string
		candidates []Candidate
		wantErr    error
		// wantSet is the set of player IDs expected on court, order-independent.
		wantSet [4]int64
		// wantTeams, when non-zero, is the exact (A1,A2,B1,B2) pairing.
		wantTeams [4]int64
		wantGap   float64
	}{
		{
			name:       "no players",
			candidates: nil,
			wantErr:    ErrNotEnoughPlayers,
		},
		{
			name:       "three players is not enough",
			candidates: []Candidate{cand(1, 1000, 0), cand(2, 1000, 1), cand(3, 1000, 2)},
			wantErr:    ErrNotEnoughPlayers,
		},
		{
			name: "exactly four players all play",
			candidates: []Candidate{
				cand(1, 1200, 3), cand(2, 800, 1), cand(3, 1000, 2), cand(4, 1000, 0),
			},
			wantSet: [4]int64{1, 2, 3, 4},
			// Waiting order is 4,2,3,1 (1000,800,1000,1200).
			// Splits: (4,2)v(3,1) gap 200 | (4,3)v(2,1) gap 0 | (4,1)v(2,3) gap 200.
			wantTeams: [4]int64{4, 3, 2, 1},
			wantGap:   0,
		},
		{
			name: "longest waiting four are chosen, not the best balanced four",
			candidates: []Candidate{
				cand(1, 1200, 0), cand(2, 1200, 1), cand(3, 1200, 2), cand(4, 1200, 3),
				cand(5, 1200, 9), // balances nothing; has waited the least
				cand(6, 800, 10),
			},
			wantSet:   [4]int64{1, 2, 3, 4},
			wantTeams: [4]int64{1, 2, 3, 4},
			wantGap:   0,
		},
		{
			name: "tied ready_since falls back to player ID",
			candidates: []Candidate{
				cand(9, 1000, 0), cand(4, 1000, 0), cand(7, 1000, 0), cand(2, 1000, 0),
				cand(1, 1000, 5),
			},
			wantSet:   [4]int64{2, 4, 7, 9},
			wantTeams: [4]int64{2, 4, 7, 9}, // all ratings equal, first split wins
			wantGap:   0,
		},
		{
			name: "worked example: 1200/1000/1000/800 pairs strong with weak",
			candidates: []Candidate{
				cand(10, 1200, 0), cand(11, 1000, 1), cand(12, 1000, 2), cand(13, 800, 3),
			},
			wantSet: [4]int64{10, 11, 12, 13},
			// (10,11)v(12,13) = 1100 v 900 -> 200
			// (10,12)v(11,13) = 1100 v 900 -> 200
			// (10,13)v(11,12) = 1000 v 1000 -> 0  <- chosen
			wantTeams: [4]int64{10, 13, 11, 12},
			wantGap:   0,
		},
		{
			name: "unequal ratings still produce the smallest achievable gap",
			candidates: []Candidate{
				cand(20, 1200, 0), cand(21, 1200, 1), cand(22, 800, 2), cand(23, 1000, 3),
			},
			wantSet: [4]int64{20, 21, 22, 23},
			// (20,21)v(22,23) = 1200 v 900 -> 300
			// (20,22)v(21,23) = 1000 v 1100 -> 100 <- chosen
			// (20,23)v(21,22) = 1100 v 1000 -> 100 (tie, later split loses)
			wantTeams: [4]int64{20, 22, 21, 23},
			wantGap:   100,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SelectNext(tt.candidates)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("SelectNext() error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("SelectNext() unexpected error: %v", err)
			}
			if got := ids(got); got != tt.wantTeams {
				t.Errorf("teams = %v, want %v", got, tt.wantTeams)
			}
			if math.Abs(got.Gap-tt.wantGap) > 1e-9 {
				t.Errorf("gap = %v, want %v", got.Gap, tt.wantGap)
			}
			assertSameSet(t, ids(got), tt.wantSet)
		})
	}
}

func assertSameSet(t *testing.T, got, want [4]int64) {
	t.Helper()
	seen := map[int64]bool{}
	for _, id := range got {
		if seen[id] {
			t.Fatalf("player %d appears twice in one match: %v", id, got)
		}
		seen[id] = true
	}
	for _, id := range want {
		if !seen[id] {
			t.Fatalf("player %d missing from selection %v", id, got)
		}
	}
}

func benchmarkSelectNext(b *testing.B, n int) {
	in := make([]Candidate, n)
	for i := range in {
		// Spread ratings and wait times without using a random source so the
		// benchmark measures the same work on every run.
		in[i] = cand(int64(i+1), 800+float64((i*37)%400), (i*13)%n)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := SelectNext(in); err != nil {
			b.Fatal(err)
		}
	}
}
