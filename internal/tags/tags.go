// Package tags cleans up the free-form folksonomy tags that come back from
// MusicBrainz so that spelling variants compare equal and non-genre noise
// (years, countries, "seen live", ...) does not skew similarity scores.
package tags

import (
	"bufio"
	_ "embed"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

//go:embed stoplist.txt
var defaultStoplist string

//go:embed synonyms.txt
var defaultSynonyms string

// yearPattern matches years and decades: "1975", "80s", "1990s", "'90s".
var yearPattern = regexp.MustCompile(`^'?\d{2}(\d{2})?'?s?$`)

var separators = strings.NewReplacer(
	"-", " ", "_", " ",
	"‐", " ", "‑", " ", "‒", " ", "–", " ", "—", " ",
	"’", "'",
)

// Normalizer maps raw tags to a canonical form and drops stop-listed ones.
// It is immutable after construction and safe for concurrent use.
type Normalizer struct {
	synonyms map[string]string
	stop     map[string]struct{}
}

// Load builds a Normalizer from the embedded synonym table and a stop list.
// An empty stoplistPath selects the embedded default stop list; otherwise the
// file at that path replaces it entirely.
func Load(stoplistPath string) (*Normalizer, error) {
	if stoplistPath == "" {
		return New(strings.NewReader(defaultStoplist), strings.NewReader(defaultSynonyms))
	}

	f, err := os.Open(stoplistPath)
	if err != nil {
		return nil, fmt.Errorf("tags: open stop list: %w", err)
	}
	defer f.Close()

	return New(f, strings.NewReader(defaultSynonyms))
}

// New builds a Normalizer from a stop list (one tag per line) and a synonym
// table ("canonical: variant, variant" per line). Blank lines and lines
// starting with "#" are ignored in both.
func New(stoplist, synonyms io.Reader) (*Normalizer, error) {
	n := &Normalizer{
		synonyms: make(map[string]string),
		stop:     make(map[string]struct{}),
	}

	err := scanLines(synonyms, func(lineNo int, line string) error {
		canonical, variants, ok := strings.Cut(line, ":")
		if !ok {
			return fmt.Errorf("tags: synonyms line %d: expected \"canonical: variant, ...\"", lineNo)
		}
		canonical = clean(canonical)
		if canonical == "" {
			return fmt.Errorf("tags: synonyms line %d: empty canonical tag", lineNo)
		}
		for _, v := range strings.Split(variants, ",") {
			if v = clean(v); v != "" && v != canonical {
				n.synonyms[v] = canonical
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	err = scanLines(stoplist, func(_ int, line string) error {
		n.stop[n.canonical(line)] = struct{}{}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return n, nil
}

// Normalize returns the canonical form of a raw tag. ok is false when the tag
// is empty or should be ignored (stop-listed, a year or a decade).
func (n *Normalizer) Normalize(raw string) (tag string, ok bool) {
	tag = n.canonical(raw)
	if tag == "" || yearPattern.MatchString(tag) {
		return "", false
	}
	if _, stopped := n.stop[tag]; stopped {
		return "", false
	}
	return tag, true
}

// NormalizeAll normalizes every tag, dropping ignored ones and duplicates.
// The order of first appearance is preserved.
func (n *Normalizer) NormalizeAll(raw []string) []string {
	seen := make(map[string]struct{}, len(raw))
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		tag, ok := n.Normalize(r)
		if !ok {
			continue
		}
		if _, dup := seen[tag]; dup {
			continue
		}
		seen[tag] = struct{}{}
		out = append(out, tag)
	}
	return out
}

func (n *Normalizer) canonical(raw string) string {
	tag := clean(raw)
	if canonical, ok := n.synonyms[tag]; ok {
		return canonical
	}
	return tag
}

// clean lower-cases a tag, turns hyphens and underscores into spaces and
// collapses runs of whitespace.
func clean(raw string) string {
	return strings.Join(strings.Fields(separators.Replace(strings.ToLower(raw))), " ")
}

func scanLines(r io.Reader, fn func(lineNo int, line string) error) error {
	scanner := bufio.NewScanner(r)
	for lineNo := 1; scanner.Scan(); lineNo++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if err := fn(lineNo, line); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("tags: read config: %w", err)
	}
	return nil
}
