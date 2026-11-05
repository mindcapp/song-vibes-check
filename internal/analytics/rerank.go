package analytics

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/mindcapp/song-vibes-check/internal/vectors"
)

const (
	// DefaultWeight is how much of the final score comes from listener
	// behaviour (the rest is the cosine similarity of the audio features).
	DefaultWeight = 0.3
	// DefaultSmoothing is k: how many "average" plays are mixed into every
	// candidate's keep rate, so a pair seen twice does not outrank one seen
	// two hundred times.
	DefaultSmoothing = 5.0
	// maxSmoothing is a sanity bound for k.
	maxSmoothing = 1e6
	// NoDataKeepRate is the average keep rate when no candidate has any plays.
	NoDataKeepRate = 0.5
)

// RerankConfig holds the two tunable parameters of the formula.
type RerankConfig struct {
	// Weight is w in score = (1-w)*cosine + w*keep, between 0 and 1.
	Weight float64
	// Smoothing is k in keep = (keeps + k*avg) / (plays + k), at least 0.
	Smoothing float64
}

// DefaultRerankConfig returns w = 0.3 and k = 5.
func DefaultRerankConfig() RerankConfig {
	return RerankConfig{Weight: DefaultWeight, Smoothing: DefaultSmoothing}
}

// Validate reports whether the parameters are usable.
func (c RerankConfig) Validate() error {
	if math.IsNaN(c.Weight) || c.Weight < 0 || c.Weight > 1 {
		return fmt.Errorf("rerank weight must be between 0 and 1, got %v", c.Weight)
	}
	if math.IsNaN(c.Smoothing) || c.Smoothing < 0 || c.Smoothing > maxSmoothing {
		return fmt.Errorf("rerank smoothing must be between 0 and %v, got %v", maxSmoothing, c.Smoothing)
	}
	return nil
}

// ParseWeight reads RERANK_WEIGHT; an empty value means DefaultWeight.
func ParseWeight(raw string) (float64, error) {
	if strings.TrimSpace(raw) == "" {
		return DefaultWeight, nil
	}
	w, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return 0, fmt.Errorf("RERANK_WEIGHT %q is not a number", raw)
	}
	if err := (RerankConfig{Weight: w, Smoothing: DefaultSmoothing}).Validate(); err != nil {
		return 0, fmt.Errorf("RERANK_WEIGHT: %w", err)
	}
	return w, nil
}

// ParseSmoothing reads RERANK_SMOOTHING; an empty value means
// DefaultSmoothing.
func ParseSmoothing(raw string) (float64, error) {
	if strings.TrimSpace(raw) == "" {
		return DefaultSmoothing, nil
	}
	k, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return 0, fmt.Errorf("RERANK_SMOOTHING %q is not a number", raw)
	}
	if err := (RerankConfig{Weight: DefaultWeight, Smoothing: k}).Validate(); err != nil {
		return 0, fmt.Errorf("RERANK_SMOOTHING: %w", err)
	}
	return k, nil
}

// Ranked is a candidate with the numbers it was ranked by.
type Ranked struct {
	vectors.Track
	// Cosine is the similarity Qdrant reported.
	Cosine float64
	// KeepRate is the smoothed share of plays that were not skipped.
	KeepRate float64
	// Plays is how often listeners played this track right after the source.
	Plays uint64
	// Score is (1-w)*Cosine + w*KeepRate; the list is sorted by it.
	Score float64
}

// Rerank orders candidates by
//
//	score = (1-w)*cosine + w*keep_smoothed
//	keep_smoothed = (keeps + k*avg) / (plays + k),  keeps = plays - skips
//
// where avg is the mean raw keep rate (1 - skips/plays) over the candidates
// that have plays, or NoDataKeepRate if none has. A candidate without plays
// gets keep_smoothed = avg. Ties are broken by cosine, then by track id.
// cfg must be valid.
func Rerank(candidates []vectors.Neighbor, stats map[int64]Stat, cfg RerankConfig) []Ranked {
	sum, withData := 0.0, 0
	for _, c := range candidates {
		if s, ok := stats[c.TrackID]; ok && s.Plays > 0 {
			sum += rawKeepRate(s)
			withData++
		}
	}
	avg := NoDataKeepRate
	if withData > 0 {
		avg = sum / float64(withData)
	}

	ranked := make([]Ranked, len(candidates))
	for i, c := range candidates {
		keep, plays := avg, uint64(0)
		if s, ok := stats[c.TrackID]; ok && s.Plays > 0 {
			skips := min(s.Skips, s.Plays) // skips are a subset of plays
			keeps := float64(s.Plays - skips)
			keep = (keeps + cfg.Smoothing*avg) / (float64(s.Plays) + cfg.Smoothing)
			plays = s.Plays
		}
		ranked[i] = Ranked{
			Track:    c.Track,
			Cosine:   c.Score,
			KeepRate: keep,
			Plays:    plays,
			Score:    (1-cfg.Weight)*c.Score + cfg.Weight*keep,
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		a, b := ranked[i], ranked[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.Cosine != b.Cosine {
			return a.Cosine > b.Cosine
		}
		return a.TrackID < b.TrackID
	})
	return ranked
}

func rawKeepRate(s Stat) float64 {
	skips := min(s.Skips, s.Plays)
	return 1 - float64(skips)/float64(s.Plays)
}
