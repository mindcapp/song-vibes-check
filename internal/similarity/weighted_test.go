package similarity

import (
	"math"
	"reflect"
	"testing"
)

func almostEqual(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestCompare(t *testing.T) {
	tests := []struct {
		name         string
		a, b         map[string]int
		wantGenre    float64
		wantWeighted float64
		wantShared   []string
		wantOnlyA    []string
		wantOnlyB    []string
	}{
		{
			name:       "both empty",
			wantShared: []string{}, wantOnlyA: []string{}, wantOnlyB: []string{},
		},
		{
			name:       "one empty",
			a:          map[string]int{"rock": 3},
			wantShared: []string{}, wantOnlyA: []string{"rock"}, wantOnlyB: []string{},
		},
		{
			name:      "identical profiles",
			a:         map[string]int{"rock": 4, "pop": 1},
			b:         map[string]int{"rock": 4, "pop": 1},
			wantGenre: 1, wantWeighted: 1,
			wantShared: []string{"rock", "pop"}, wantOnlyA: []string{}, wantOnlyB: []string{},
		},
		{
			name:      "same distribution, different vote totals",
			a:         map[string]int{"rock": 40, "pop": 10},
			b:         map[string]int{"rock": 4, "pop": 1},
			wantGenre: 1, wantWeighted: 1,
			wantShared: []string{"rock", "pop"}, wantOnlyA: []string{}, wantOnlyB: []string{},
		},
		{
			name:       "no overlap",
			a:          map[string]int{"rock": 2},
			b:          map[string]int{"jazz": 5},
			wantShared: []string{}, wantOnlyA: []string{"rock"}, wantOnlyB: []string{"jazz"},
		},
		{
			// shares: a = rock .75, pop .25; b = rock .25, jazz .75
			// min = .25; max = .75 + .25 + .75 = 1.75
			name:      "shared tag is dominant in one track only",
			a:         map[string]int{"rock": 3, "pop": 1},
			b:         map[string]int{"rock": 1, "jazz": 3},
			wantGenre: 1.0 / 3.0, wantWeighted: 0.25 / 1.75,
			wantShared: []string{"rock"}, wantOnlyA: []string{"pop"}, wantOnlyB: []string{"jazz"},
		},
		{
			// shares: a = rock .9, pop .1; b = rock .9, jazz .1
			// min = .9; max = .9 + .1 + .1 = 1.1
			name:      "shared tag is dominant in both tracks",
			a:         map[string]int{"rock": 9, "pop": 1},
			b:         map[string]int{"rock": 9, "jazz": 1},
			wantGenre: 1.0 / 3.0, wantWeighted: 0.9 / 1.1,
			wantShared: []string{"rock"}, wantOnlyA: []string{"pop"}, wantOnlyB: []string{"jazz"},
		},
		{
			name:      "zero and negative counts count as one vote",
			a:         map[string]int{"rock": 0, "pop": -2},
			b:         map[string]int{"rock": 1, "pop": 1},
			wantGenre: 1, wantWeighted: 1,
			wantShared: []string{"pop", "rock"}, wantOnlyA: []string{}, wantOnlyB: []string{},
		},
		{
			name:      "lists are ordered by weight, then name",
			a:         map[string]int{"rock": 1, "blues": 5, "folk": 2, "ambient": 2},
			b:         map[string]int{"rock": 1, "blues": 5, "techno": 1, "house": 3},
			wantGenre: 2.0 / 6.0, wantWeighted: 0.6 / 1.4,
			wantShared: []string{"blues", "rock"},
			wantOnlyA:  []string{"ambient", "folk"},
			wantOnlyB:  []string{"house", "techno"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Compare(tt.a, tt.b)

			if !almostEqual(got.GenreScore, tt.wantGenre) {
				t.Errorf("GenreScore = %v, want %v", got.GenreScore, tt.wantGenre)
			}
			if !almostEqual(got.WeightedScore, tt.wantWeighted) {
				t.Errorf("WeightedScore = %v, want %v", got.WeightedScore, tt.wantWeighted)
			}
			if !reflect.DeepEqual(got.Shared, tt.wantShared) {
				t.Errorf("Shared = %v, want %v", got.Shared, tt.wantShared)
			}
			if !reflect.DeepEqual(got.OnlyA, tt.wantOnlyA) {
				t.Errorf("OnlyA = %v, want %v", got.OnlyA, tt.wantOnlyA)
			}
			if !reflect.DeepEqual(got.OnlyB, tt.wantOnlyB) {
				t.Errorf("OnlyB = %v, want %v", got.OnlyB, tt.wantOnlyB)
			}
		})
	}
}

func TestCompareGenreScoreMatchesCompareGenres(t *testing.T) {
	a := map[string]int{"rock": 3, "pop": 1, "glam rock": 2}
	b := map[string]int{"rock": 1, "jazz": 4}

	keys := func(m map[string]int) []string {
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		return out
	}

	if got, want := Compare(a, b).GenreScore, CompareGenres(keys(a), keys(b)); !almostEqual(got, want) {
		t.Errorf("Compare().GenreScore = %v, CompareGenres = %v", got, want)
	}
}

func TestWeightedJaccardIsSymmetricAndBounded(t *testing.T) {
	a := map[string]int{"rock": 7, "pop": 2, "glam rock": 1}
	b := map[string]int{"rock": 2, "jazz": 9}

	ab, ba := WeightedJaccard(a, b), WeightedJaccard(b, a)
	if !almostEqual(ab, ba) {
		t.Errorf("not symmetric: %v vs %v", ab, ba)
	}
	if ab <= 0 || ab >= 1 {
		t.Errorf("score = %v, want strictly between 0 and 1", ab)
	}
}
