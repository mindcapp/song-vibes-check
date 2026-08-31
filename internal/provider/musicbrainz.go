// Package provider implements clients for external music metadata sources.
package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	searchURL        = "https://musicbrainz.org/ws/2/recording/"
	defaultRateLimit = time.Second
	defaultTimeout   = 10 * time.Second
)

// ErrNotFound is returned when MusicBrainz has no recording matching the query.
var ErrNotFound = errors.New("musicbrainz: track not found")

// ErrUpstream is returned when MusicBrainz responds with a non-2xx status.
var ErrUpstream = errors.New("musicbrainz: upstream error")

// Track is the subset of MusicBrainz recording data the rest of the service needs.
type Track struct {
	ID     string
	Title  string
	Artist string
	Tags   []string
}

// Client talks to the MusicBrainz API while respecting its 1 req/sec rate limit policy.
type Client struct {
	httpClient *http.Client
	userAgent  string
	limiter    *rateLimiter
}

// NewClient builds a MusicBrainz client. userAgent must follow MusicBrainz's
// requirements (app name, version and contact info), e.g.
// "song-similarity/0.1.0 ( contact@example.com )".
func NewClient(userAgent string) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: defaultTimeout},
		userAgent:  userAgent,
		limiter:    &rateLimiter{interval: defaultRateLimit},
	}
}

// SearchTrack looks up a recording by artist and title and returns its genre/folksonomy tags.
func (c *Client) SearchTrack(ctx context.Context, artist, title string) (*Track, error) {
	if err := c.limiter.wait(ctx); err != nil {
		return nil, err
	}

	reqURL, err := buildSearchURL(artist, title)
	if err != nil {
		return nil, fmt.Errorf("musicbrainz: build request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("musicbrainz: build request: %w", err)
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("musicbrainz: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d", ErrUpstream, resp.StatusCode)
	}

	var parsed searchResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("musicbrainz: decode response: %w", err)
	}

	if len(parsed.Recordings) == 0 {
		return nil, ErrNotFound
	}

	rec := bestRecording(parsed.Recordings)
	resolvedArtist := artist
	if len(rec.ArtistCredit) > 0 && rec.ArtistCredit[0].Name != "" {
		resolvedArtist = rec.ArtistCredit[0].Name
	}

	return &Track{
		ID:     rec.ID,
		Title:  rec.Title,
		Artist: resolvedArtist,
		Tags:   mergeTagNames(rec.Tags, rec.Genres),
	}, nil
}

// bestRecording picks the first result that carries tags/genres, since among
// same-titled recordings (studio, live, remaster, ...) MusicBrainz often
// scores several equally and the very first one frequently has no folksonomy
// data even when others further down do. Falls back to the top match.
func bestRecording(recordings []recordingResult) recordingResult {
	for _, rec := range recordings {
		if len(rec.Tags) > 0 || len(rec.Genres) > 0 {
			return rec
		}
	}
	return recordings[0]
}

func buildSearchURL(artist, title string) (string, error) {
	u, err := url.Parse(searchURL)
	if err != nil {
		return "", err
	}

	query := fmt.Sprintf(`artist:"%s" AND recording:"%s"`, escapeLucene(artist), escapeLucene(title))

	q := u.Query()
	q.Set("query", query)
	q.Set("fmt", "json")
	q.Set("inc", "tags genres")
	q.Set("limit", "25")
	u.RawQuery = q.Encode()

	return u.String(), nil
}

// escapeLucene escapes the characters that would otherwise break out of the
// quoted Lucene term MusicBrainz search expects.
func escapeLucene(s string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return replacer.Replace(s)
}

func mergeTagNames(groups ...[]tagEntry) []string {
	seen := make(map[string]struct{})
	var tags []string
	for _, group := range groups {
		for _, t := range group {
			name := strings.ToLower(strings.TrimSpace(t.Name))
			if name == "" {
				continue
			}
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			tags = append(tags, name)
		}
	}
	return tags
}

type searchResponse struct {
	Recordings []recordingResult `json:"recordings"`
}

type recordingResult struct {
	ID           string              `json:"id"`
	Title        string              `json:"title"`
	ArtistCredit []artistCreditEntry `json:"artist-credit"`
	Tags         []tagEntry          `json:"tags"`
	Genres       []tagEntry          `json:"genres"`
}

type artistCreditEntry struct {
	Name string `json:"name"`
}

type tagEntry struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// rateLimiter enforces MusicBrainz's "no more than one request per second" policy
// across all callers sharing the same Client.
type rateLimiter struct {
	mu       sync.Mutex
	last     time.Time
	interval time.Duration
}

func (rl *rateLimiter) wait(ctx context.Context) error {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	if wait := rl.interval - time.Since(rl.last); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	rl.last = time.Now()
	return nil
}
