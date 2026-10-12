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
	defaultBaseURL   = "https://musicbrainz.org/ws/2/recording/"
	defaultRateLimit = time.Second
	defaultTimeout   = 10 * time.Second
)

// ErrNotFound is returned when MusicBrainz has no recording matching the query.
var ErrNotFound = errors.New("musicbrainz: track not found")

// ErrUpstream is returned when MusicBrainz responds with a non-2xx status.
var ErrUpstream = errors.New("musicbrainz: upstream error")

// Tag is a folksonomy tag or genre together with the number of user votes
// MusicBrainz reports for it.
type Tag struct {
	Name  string
	Count int
}

// Track is the subset of MusicBrainz recording data the rest of the service needs.
type Track struct {
	ID     string
	Title  string
	Artist string
	// Tags holds lower-cased, de-duplicated tag and genre names.
	Tags []string
	// WeightedTags holds the same tags, in the same order, with vote counts.
	WeightedTags []Tag
}

// Client talks to the MusicBrainz API while respecting its 1 req/sec rate limit policy.
type Client struct {
	httpClient *http.Client
	userAgent  string
	baseURL    string
	limiter    *rateLimiter
}

// Option customises a Client.
type Option func(*Client)

// WithBaseURL overrides the MusicBrainz recording search endpoint.
func WithBaseURL(baseURL string) Option {
	return func(c *Client) { c.baseURL = baseURL }
}

// WithRateInterval overrides the minimum interval between outgoing requests.
func WithRateInterval(interval time.Duration) Option {
	return func(c *Client) { c.limiter.interval = interval }
}

// WithTimeout overrides the per-request HTTP timeout.
func WithTimeout(timeout time.Duration) Option {
	return func(c *Client) { c.httpClient.Timeout = timeout }
}

// NewClient builds a MusicBrainz client. userAgent must follow MusicBrainz's
// requirements (app name, version and contact info), e.g.
// "song-similarity/0.1.0 ( contact@example.com )".
func NewClient(userAgent string, opts ...Option) *Client {
	c := &Client{
		httpClient: &http.Client{Timeout: defaultTimeout},
		userAgent:  userAgent,
		baseURL:    defaultBaseURL,
		limiter:    &rateLimiter{interval: defaultRateLimit},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// SearchTrack looks up a recording by artist and title and returns its genre/folksonomy tags.
func (c *Client) SearchTrack(ctx context.Context, artist, title string) (*Track, error) {
	if err := c.limiter.wait(ctx); err != nil {
		return nil, err
	}

	reqURL, err := buildSearchURL(c.baseURL, artist, title)
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

	weighted := mergeTags(rec.Tags, rec.Genres)

	return &Track{
		ID:           rec.ID,
		Title:        rec.Title,
		Artist:       resolvedArtist,
		Tags:         tagNames(weighted),
		WeightedTags: weighted,
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

func buildSearchURL(baseURL, artist, title string) (string, error) {
	u, err := url.Parse(baseURL)
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

// mergeTags flattens the tag and genre lists into one. MusicBrainz repeats
// every genre in the tag list with the same vote count, so duplicates keep the
// highest count instead of being summed.
func mergeTags(groups ...[]tagEntry) []Tag {
	index := make(map[string]int)
	var tags []Tag
	for _, group := range groups {
		for _, t := range group {
			name := strings.ToLower(strings.TrimSpace(t.Name))
			if name == "" {
				continue
			}
			if i, ok := index[name]; ok {
				if t.Count > tags[i].Count {
					tags[i].Count = t.Count
				}
				continue
			}
			index[name] = len(tags)
			tags = append(tags, Tag{Name: name, Count: t.Count})
		}
	}
	return tags
}

func tagNames(tags []Tag) []string {
	if len(tags) == 0 {
		return nil
	}
	names := make([]string, len(tags))
	for i, t := range tags {
		names[i] = t.Name
	}
	return names
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
