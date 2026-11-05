package analytics

import (
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/mindcapp/song-vibes-check/internal/vectors"
)

func cand(id int64, cosine float64) vectors.Neighbor {
	return vectors.Neighbor{Track: vectors.Track{TrackID: id}, Score: cosine}
}

func near(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("%s = %.12f, want %.12f", name, got, want)
	}
}

func ids(r []Ranked) []int64 {
	out := make([]int64, len(r))
	for i, x := range r {
		out[i] = x.TrackID
	}
	return out
}

// Hand calculation with w = 0.3, k = 5:
//
//	A: cosine 0.90, 10 plays, 2 skips -> raw keep 0.8
//	B: cosine 0.85, 20 plays, 18 skips -> raw keep 0.1
//	C: cosine 0.80, no data
//	avg = (0.8 + 0.1) / 2 = 0.45
//	A: keeps 8,  (8 + 5*0.45) / (10+5) = 10.25/15 = 0.683333...
//	   score = 0.7*0.90 + 0.3*0.683333 = 0.63 + 0.205  = 0.835
//	B: keeps 2,  (2 + 2.25) / (20+5)   = 4.25/25    = 0.17
//	   score = 0.7*0.85 + 0.3*0.17     = 0.595 + 0.051 = 0.646
//	C: keep = avg = 0.45
//	   score = 0.7*0.80 + 0.3*0.45     = 0.56 + 0.135 = 0.695
//
// Order: A, C, B. By cosine alone it would be A, B, C.
func TestRerankHandCalculation(t *testing.T) {
	stats := map[int64]Stat{1: {Plays: 10, Skips: 2}, 2: {Plays: 20, Skips: 18}}
	got := Rerank([]vectors.Neighbor{cand(1, 0.90), cand(2, 0.85), cand(3, 0.80)}, stats, DefaultRerankConfig())

	if want := []int64{1, 3, 2}; !reflect.DeepEqual(ids(got), want) {
		t.Fatalf("order = %v, want %v", ids(got), want)
	}
	near(t, "A keep", got[0].KeepRate, 10.25/15)
	near(t, "A score", got[0].Score, 0.835)
	near(t, "A cosine", got[0].Cosine, 0.90)
	if got[0].Plays != 10 {
		t.Errorf("A plays = %d, want 10", got[0].Plays)
	}
	near(t, "C keep", got[1].KeepRate, 0.45)
	near(t, "C score", got[1].Score, 0.695)
	if got[1].Plays != 0 {
		t.Errorf("C plays = %d, want 0", got[1].Plays)
	}
	near(t, "B keep", got[2].KeepRate, 0.17)
	near(t, "B score", got[2].Score, 0.646)
}

func TestRerankNoDataKeepsCosineOrder(t *testing.T) {
	for name, stats := range map[string]map[int64]Stat{
		"nil":        nil,
		"empty":      {},
		"zero plays": {1: {}},
	} {
		got := Rerank([]vectors.Neighbor{cand(1, 0.9), cand(2, 0.8), cand(3, 0.7)}, stats, DefaultRerankConfig())
		if want := []int64{1, 2, 3}; !reflect.DeepEqual(ids(got), want) {
			t.Errorf("%s: order = %v, want %v", name, ids(got), want)
		}
		for _, r := range got {
			near(t, name+" keep", r.KeepRate, NoDataKeepRate)
			near(t, name+" score", r.Score, 0.7*r.Cosine+0.3*0.5)
		}
	}
	if got := Rerank(nil, nil, DefaultRerankConfig()); len(got) != 0 {
		t.Errorf("no candidates = %v, want none", got)
	}
}

func TestRerankWeightExtremes(t *testing.T) {
	stats := map[int64]Stat{1: {Plays: 100, Skips: 90}, 2: {Plays: 100, Skips: 5}}
	cands := []vectors.Neighbor{cand(1, 0.9), cand(2, 0.8)}

	if got := Rerank(cands, stats, RerankConfig{Weight: 0, Smoothing: 5}); !reflect.DeepEqual(ids(got), []int64{1, 2}) {
		t.Errorf("w=0 order = %v, want cosine order [1 2]", ids(got))
	}
	got := Rerank(cands, stats, RerankConfig{Weight: 1, Smoothing: 5})
	if !reflect.DeepEqual(ids(got), []int64{2, 1}) {
		t.Errorf("w=1 order = %v, want behaviour order [2 1]", ids(got))
	}
	near(t, "w=1 score is keep", got[0].Score, got[0].KeepRate)
}

// A has 2 plays and no skips (raw keep 1.0), B has 200 plays and 20 skips
// (raw 0.9), D drags the average down: D has 100 plays, 90 skips (raw 0.1).
// avg = (1.0 + 0.9 + 0.1) / 3 = 0.666667.
//
//	k = 5: A = (2 + 5*0.666667) / 7    = 0.761905
//	       B = (180 + 5*0.666667) / 205 = 0.894309   -> B ahead of A
//	k = 0: A = 1.0, B = 0.9                           -> A ahead of B
//
// A tiny sample must not beat a well-measured one just because it has no
// skips yet.
func TestRerankSmoothingPullsSmallSamplesToAverage(t *testing.T) {
	stats := map[int64]Stat{1: {Plays: 2, Skips: 0}, 2: {Plays: 200, Skips: 20}, 3: {Plays: 100, Skips: 90}}
	cands := []vectors.Neighbor{cand(1, 0.8), cand(2, 0.8), cand(3, 0.5)}

	got := Rerank(cands, stats, RerankConfig{Weight: 0.3, Smoothing: 5})
	if want := []int64{2, 1, 3}; !reflect.DeepEqual(ids(got), want) {
		t.Fatalf("k=5 order = %v, want %v", ids(got), want)
	}
	near(t, "B keep", got[0].KeepRate, (180+5*(2.0/3))/205)
	near(t, "A keep", got[1].KeepRate, (2+5*(2.0/3))/7)

	got = Rerank(cands, stats, RerankConfig{Weight: 0.3, Smoothing: 0})
	if want := []int64{1, 2, 3}; !reflect.DeepEqual(ids(got), want) {
		t.Fatalf("k=0 order = %v, want %v", ids(got), want)
	}
	near(t, "k=0 A keep", got[0].KeepRate, 1.0)
}

func TestRerankTiesAndSkipsAbovePlays(t *testing.T) {
	// equal scores: higher cosine first, then lower id
	got := Rerank([]vectors.Neighbor{cand(9, 0.5), cand(4, 0.5), cand(7, 0.5)}, nil, DefaultRerankConfig())
	if want := []int64{4, 7, 9}; !reflect.DeepEqual(ids(got), want) {
		t.Errorf("tie order = %v, want %v", ids(got), want)
	}
	// inconsistent data (skips > plays) is clamped instead of going negative
	got = Rerank([]vectors.Neighbor{cand(1, 0.5)}, map[int64]Stat{1: {Plays: 3, Skips: 9}}, DefaultRerankConfig())
	if got[0].KeepRate < 0 || got[0].KeepRate > 1 {
		t.Errorf("keep = %v, want within [0, 1]", got[0].KeepRate)
	}
}

func TestRerankDoesNotModifyInput(t *testing.T) {
	cands := []vectors.Neighbor{cand(1, 0.9), cand(2, 0.8)}
	Rerank(cands, map[int64]Stat{2: {Plays: 50, Skips: 0}}, RerankConfig{Weight: 1, Smoothing: 0})
	if cands[0].TrackID != 1 || cands[1].TrackID != 2 {
		t.Errorf("input was reordered: %v", cands)
	}
}

func TestParseWeight(t *testing.T) {
	for raw, want := range map[string]float64{"": 0.3, "0": 0, "1": 1, "0.55": 0.55, " 0.1 ": 0.1} {
		got, err := ParseWeight(raw)
		if err != nil || got != want {
			t.Errorf("ParseWeight(%q) = %v, %v; want %v", raw, got, err, want)
		}
	}
	for _, raw := range []string{"-0.1", "1.01", "2", "abc", "NaN", "Inf", "-Inf", "0,5"} {
		if _, err := ParseWeight(raw); err == nil || !strings.Contains(err.Error(), "RERANK_WEIGHT") {
			t.Errorf("ParseWeight(%q) = %v, want an error naming RERANK_WEIGHT", raw, err)
		}
	}
}

func TestParseSmoothing(t *testing.T) {
	for raw, want := range map[string]float64{"": 5, "0": 0, "2.5": 2.5, "100": 100} {
		got, err := ParseSmoothing(raw)
		if err != nil || got != want {
			t.Errorf("ParseSmoothing(%q) = %v, %v; want %v", raw, got, err, want)
		}
	}
	for _, raw := range []string{"-1", "abc", "NaN", "Inf", "1e9"} {
		if _, err := ParseSmoothing(raw); err == nil || !strings.Contains(err.Error(), "RERANK_SMOOTHING") {
			t.Errorf("ParseSmoothing(%q) = %v, want an error naming RERANK_SMOOTHING", raw, err)
		}
	}
}

func TestRerankConfigValidate(t *testing.T) {
	if err := DefaultRerankConfig().Validate(); err != nil {
		t.Errorf("default config is invalid: %v", err)
	}
	for _, bad := range []RerankConfig{{Weight: -0.01, Smoothing: 5}, {Weight: 1.01, Smoothing: 5}, {Weight: math.NaN(), Smoothing: 5}, {Weight: 0.3, Smoothing: -1}} {
		if err := bad.Validate(); err == nil {
			t.Errorf("%+v passed validation", bad)
		}
	}
}
