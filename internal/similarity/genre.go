// Package similarity compares metadata between two tracks.
package similarity

import "strings"

// CompareGenres returns the Jaccard similarity (|A ∩ B| / |A ∪ B|) between two
// sets of genre/tag strings, in the range [0, 1]. Comparison is case-insensitive.
func CompareGenres(tagsA, tagsB []string) float64 {
	setA := toSet(tagsA)
	setB := toSet(tagsB)

	if len(setA) == 0 && len(setB) == 0 {
		return 0
	}

	intersection := 0
	for tag := range setA {
		if _, ok := setB[tag]; ok {
			intersection++
		}
	}

	union := len(setA) + len(setB) - intersection
	if union == 0 {
		return 0
	}

	return float64(intersection) / float64(union)
}

func toSet(tags []string) map[string]struct{} {
	set := make(map[string]struct{}, len(tags))
	for _, t := range tags {
		normalized := strings.ToLower(strings.TrimSpace(t))
		if normalized == "" {
			continue
		}
		set[normalized] = struct{}{}
	}
	return set
}
