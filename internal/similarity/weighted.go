package similarity

import (
	"math"
	"sort"
)

// Result explains how two tag profiles compare.
type Result struct {
	// GenreScore is the plain Jaccard similarity of the two tag sets.
	GenreScore float64
	// WeightedScore is the weighted Jaccard similarity of the two profiles,
	// see WeightedJaccard.
	WeightedScore float64
	// Shared lists tags present in both profiles, strongest first.
	Shared []string
	// OnlyA and OnlyB list tags present in just one profile, strongest first.
	OnlyA []string
	OnlyB []string
}

// Compare scores two tag profiles (tag -> vote count) and reports which tags
// they share. Counts below 1 are treated as 1, so a tag nobody voted for still
// counts as present.
func Compare(a, b map[string]int) Result {
	shareA, shareB := shares(a), shares(b)

	res := Result{Shared: []string{}, OnlyA: []string{}, OnlyB: []string{}}
	for tag := range shareA {
		if _, ok := shareB[tag]; ok {
			res.Shared = append(res.Shared, tag)
		} else {
			res.OnlyA = append(res.OnlyA, tag)
		}
	}
	for tag := range shareB {
		if _, ok := shareA[tag]; !ok {
			res.OnlyB = append(res.OnlyB, tag)
		}
	}

	sortByWeight(res.Shared, func(tag string) float64 { return math.Min(shareA[tag], shareB[tag]) })
	sortByWeight(res.OnlyA, func(tag string) float64 { return shareA[tag] })
	sortByWeight(res.OnlyB, func(tag string) float64 { return shareB[tag] })

	if union := len(res.Shared) + len(res.OnlyA) + len(res.OnlyB); union > 0 {
		res.GenreScore = float64(len(res.Shared)) / float64(union)
	}
	res.WeightedScore = weightedJaccard(shareA, shareB)

	return res
}

// WeightedJaccard returns sum(min(a, b)) / sum(max(a, b)) over all tags, where
// a and b are each tag's share of its track's votes. Using shares rather than
// raw counts keeps a heavily tagged hit comparable with an obscure track: what
// matters is how the votes are distributed, not how many there are. The result
// is in [0, 1]; it is 0 when either profile is empty.
func WeightedJaccard(a, b map[string]int) float64 {
	return weightedJaccard(shares(a), shares(b))
}

func weightedJaccard(a, b map[string]float64) float64 {
	var minSum, maxSum float64
	for tag, wa := range a {
		wb := b[tag]
		minSum += math.Min(wa, wb)
		maxSum += math.Max(wa, wb)
	}
	for tag, wb := range b {
		if _, ok := a[tag]; !ok {
			maxSum += wb
		}
	}
	if maxSum == 0 {
		return 0
	}
	return minSum / maxSum
}

func shares(counts map[string]int) map[string]float64 {
	total := 0
	for _, c := range counts {
		total += max(c, 1)
	}
	out := make(map[string]float64, len(counts))
	for tag, c := range counts {
		out[tag] = float64(max(c, 1)) / float64(total)
	}
	return out
}

func sortByWeight(tags []string, weight func(string) float64) {
	sort.Slice(tags, func(i, j int) bool {
		wi, wj := weight(tags[i]), weight(tags[j])
		if wi != wj {
			return wi > wj
		}
		return tags[i] < tags[j]
	})
}
