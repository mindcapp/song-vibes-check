package similarity

import (
	"math"
	"testing"
)

func TestCompareGenres(t *testing.T) {
	tests := []struct {
		name  string
		tagsA []string
		tagsB []string
		want  float64
	}{
		{name: "both nil", tagsA: nil, tagsB: nil, want: 0},
		{name: "both empty", tagsA: []string{}, tagsB: []string{}, want: 0},
		{name: "first empty", tagsA: nil, tagsB: []string{"rock"}, want: 0},
		{name: "second empty", tagsA: []string{"rock"}, tagsB: nil, want: 0},
		{name: "only blank tags", tagsA: []string{"", "  "}, tagsB: []string{"\t"}, want: 0},
		{name: "full match", tagsA: []string{"rock", "pop"}, tagsB: []string{"pop", "rock"}, want: 1},
		{name: "no overlap", tagsA: []string{"rock", "pop"}, tagsB: []string{"jazz", "blues"}, want: 0},
		{name: "partial overlap", tagsA: []string{"rock", "pop"}, tagsB: []string{"rock", "jazz"}, want: 1.0 / 3.0},
		{name: "subset", tagsA: []string{"rock"}, tagsB: []string{"rock", "pop", "jazz", "blues"}, want: 0.25},
		{name: "case insensitive", tagsA: []string{"Rock", "POP"}, tagsB: []string{"rock", "pop"}, want: 1},
		{name: "surrounding whitespace", tagsA: []string{" rock "}, tagsB: []string{"rock"}, want: 1},
		{name: "duplicates counted once", tagsA: []string{"rock", "Rock", "rock"}, tagsB: []string{"rock", "pop"}, want: 0.5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CompareGenres(tt.tagsA, tt.tagsB)
			if math.Abs(got-tt.want) > 1e-9 {
				t.Errorf("CompareGenres(%v, %v) = %v, want %v", tt.tagsA, tt.tagsB, got, tt.want)
			}
		})
	}
}

func TestCompareGenresIsSymmetric(t *testing.T) {
	a := []string{"rock", "pop", "glam rock"}
	b := []string{"Pop", "jazz"}

	if ab, ba := CompareGenres(a, b), CompareGenres(b, a); ab != ba {
		t.Errorf("CompareGenres is not symmetric: %v vs %v", ab, ba)
	}
}
