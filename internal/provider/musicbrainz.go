// Package provider implements clients for external music metadata sources.
package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	defaultBaseURL   = "https://musicbrainz.org/ws/2"
	defaultRateLimit = time.Second
	defaultTimeout   = 10 * time.Second

	// minScore is the lowest MusicBrainz search score (0-100) a recording
	// may have to be treated as the requested song.
	minScore = 90
	// maxMerged is how many matching recordings contribute tags.
	maxMerged = 5
	// searchLimit is how many search results are requested. A popular song
	// has hundreds of recordings with the same top score, of which only a few
	// carry tags, so a short page often contains none of them.
	searchLimit = 100
)

// Where a track's tags came from.
const (
	TagSourceRecording = "recording"
	TagSourceArtist    = "artist"
)

// ErrNotFound is returned when MusicBrainz has no recording matching the query.
var ErrNotFound = errors.New("musicbrainz: track not found")

// ErrUpstream is returned when MusicBrainz responds with a non-2xx status.
var ErrUpstream = errors.New("musicbrainz: upstream error")

// errNoSuchEntity is returned internally for a 404 on a direct lookup.
var errNoSuchEntity = errors.New("musicbrainz: no such entity")

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
	// TagSource says where the tags came from: TagSourceRecording when they
	// were merged from matching recordings, TagSourceArtist when no matching
	// recording had any and the artist's tags were used instead, and empty
	// when there are no tags at all.
	TagSource string
	// Cached is true when the track was served from the cache rather than
	// fetched from MusicBrainz.
	Cached bool
}

// Clone returns a deep copy of the track, safe to modify independently of the
// original. It returns nil for a nil track.
func (t *Track) Clone() *Track {
	if t == nil {
		return nil
	}
	clone := *t
	clone.Tags = append([]string(nil), t.Tags...)
	clone.WeightedTags = append([]Tag(nil), t.WeightedTags...)
	return &clone
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

// WithBaseURL overrides the MusicBrainz web service root
// (https://musicbrainz.org/ws/2 by default).
func WithBaseURL(baseURL string) Option {
	return func(c *Client) { c.baseURL = strings.TrimRight(baseURL, "/") }
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

// SearchTrack looks up a song by artist and title and returns its tags.
//
// A popular song exists in MusicBrainz as many recordings (studio, live,
// remaster, ...) that score equally in search and are returned in no stable
// order, while only a few of them carry tags. Picking a single recording
// therefore gives different tags from one call to the next. Instead, the tags
// of up to maxMerged recordings are merged, summing vote counts. A recording
// takes part only if its search score is at least minScore and it is credited
// to the requested artist (compared after normalization, see normalizeArtist).
//
// If none of the matching recordings has tags, the artist's own tags are used.
// ErrNotFound means no recording matched at all.
func (c *Client) SearchTrack(ctx context.Context, artist, title string) (*Track, error) {
	var parsed searchResponse
	if err := c.getJSON(ctx, c.searchURL(artist, title), &parsed); err != nil {
		return nil, err
	}

	matches := matchingRecordings(parsed.Recordings, artist)
	if len(matches) == 0 {
		return nil, ErrNotFound
	}

	tagged := bestTagged(matches)
	best := matches[0]
	if len(tagged) > 0 {
		best = tagged[0]
	}

	track := &Track{ID: best.rec.ID, Title: best.rec.Title, Artist: best.artist.Name}
	if track.Artist == "" {
		track.Artist = artist
	}

	weighted := sumTags(tagged)
	source := TagSourceRecording
	if len(weighted) == 0 && best.artist.ID != "" {
		artistTags, err := c.artistTags(ctx, best.artist.ID)
		if err != nil {
			return nil, err
		}
		weighted, source = artistTags, TagSourceArtist
	}
	if len(weighted) > 0 {
		track.Tags, track.WeightedTags, track.TagSource = tagNames(weighted), weighted, source
	}
	return track, nil
}

// artistTags fetches the tags of an artist by MusicBrainz ID. An unknown ID
// yields no tags rather than an error.
func (c *Client) artistTags(ctx context.Context, artistID string) ([]Tag, error) {
	u := c.baseURL + "/artist/" + url.PathEscape(artistID) + "?" + url.Values{
		"fmt": {"json"},
		"inc": {"tags genres"},
	}.Encode()

	var parsed artistResponse
	if err := c.getJSON(ctx, u, &parsed); err != nil {
		if errors.Is(err, errNoSuchEntity) {
			return nil, nil
		}
		return nil, err
	}
	return sortTags(mergeTags(parsed.Tags, parsed.Genres)), nil
}

// getJSON performs one rate-limited GET and decodes the JSON response.
func (c *Client) getJSON(ctx context.Context, reqURL string, out any) error {
	if err := c.limiter.wait(ctx); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return fmt.Errorf("musicbrainz: build request: %w", err)
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("musicbrainz: request failed: %w", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return errNoSuchEntity
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("%w: status %d", ErrUpstream, resp.StatusCode)
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("musicbrainz: decode response: %w", err)
	}
	return nil
}

func (c *Client) searchURL(artist, title string) string {
	query := fmt.Sprintf(`artist:"%s" AND recording:"%s"`, escapeLucene(artist), escapeLucene(title))

	return c.baseURL + "/recording/?" + url.Values{
		"query": {query},
		"fmt":   {"json"},
		"limit": {strconv.Itoa(searchLimit)},
	}.Encode()
}

// match is a search result accepted as the requested song, with the credited
// artist that matched the request.
type match struct {
	rec    recordingResult
	artist artistRef
}

// matchingRecordings keeps the results that score at least minScore and are
// credited to the requested artist.
func matchingRecordings(recordings []recordingResult, artist string) []match {
	want := normalizeArtist(artist)

	var out []match
	for _, rec := range recordings {
		if rec.Score < minScore {
			continue
		}
		if ref, ok := creditedArtist(rec.ArtistCredit, want); ok {
			out = append(out, match{rec: rec, artist: ref})
		}
	}
	return out
}

// creditedArtist reports whether the normalized artist name want appears in
// the credit: as one credited artist (under the credited or the canonical
// name) or as the whole credit ("Queen & David Bowie").
func creditedArtist(credit []artistCreditEntry, want string) (artistRef, bool) {
	if want == "" {
		return artistRef{}, false
	}

	var full strings.Builder
	for _, entry := range credit {
		if normalizeArtist(entry.Name) == want || normalizeArtist(entry.Artist.Name) == want {
			return artistRef{ID: entry.Artist.ID, Name: entry.Name}, true
		}
		full.WriteString(entry.Name)
		full.WriteString(entry.JoinPhrase)
	}

	if len(credit) > 1 && normalizeArtist(full.String()) == want {
		return artistRef{ID: credit[0].Artist.ID, Name: full.String()}, true
	}
	return artistRef{}, false
}

// bestTagged returns the matches that carry tags, at most maxMerged of them.
// Search order among equally scored recordings is not stable, so the choice
// is made on the data itself: most votes first, then recording ID.
func bestTagged(matches []match) []match {
	var tagged []match
	for _, m := range matches {
		if len(m.rec.Tags) > 0 || len(m.rec.Genres) > 0 {
			tagged = append(tagged, m)
		}
	}

	sort.SliceStable(tagged, func(i, j int) bool {
		vi, vj := votes(tagged[i].rec), votes(tagged[j].rec)
		if vi != vj {
			return vi > vj
		}
		return tagged[i].rec.ID < tagged[j].rec.ID
	})

	if len(tagged) > maxMerged {
		tagged = tagged[:maxMerged]
	}
	return tagged
}

func votes(rec recordingResult) int {
	total := 0
	for _, t := range mergeTags(rec.Tags, rec.Genres) {
		total += t.Count
	}
	return total
}

// sumTags adds up the vote counts of every tag across the given recordings.
func sumTags(matches []match) []Tag {
	index := make(map[string]int)
	var tags []Tag
	for _, m := range matches {
		for _, t := range mergeTags(m.rec.Tags, m.rec.Genres) {
			if i, ok := index[t.Name]; ok {
				tags[i].Count += t.Count
				continue
			}
			index[t.Name] = len(tags)
			tags = append(tags, t)
		}
	}
	return sortTags(tags)
}

// sortTags orders tags by votes, then name, so equal data always produces the
// same slice whatever order MusicBrainz returned it in.
func sortTags(tags []Tag) []Tag {
	sort.SliceStable(tags, func(i, j int) bool {
		if tags[i].Count != tags[j].Count {
			return tags[i].Count > tags[j].Count
		}
		return tags[i].Name < tags[j].Name
	})
	return tags
}

var (
	artistFold = strings.NewReplacer(
		"&", " and ",
		"á", "a", "à", "a", "â", "a", "ä", "a", "ã", "a", "å", "a",
		"é", "e", "è", "e", "ê", "e", "ë", "e",
		"í", "i", "ì", "i", "î", "i", "ï", "i",
		"ó", "o", "ò", "o", "ô", "o", "ö", "o", "õ", "o", "ø", "o",
		"ú", "u", "ù", "u", "û", "u", "ü", "u",
		"ñ", "n", "ç", "c", "ß", "ss",
	)
)

// normalizeArtist reduces an artist name to a form in which spelling
// variants compare equal: lower-case, common Latin diacritics folded, "&"
// read as "and", punctuation dropped, whitespace collapsed and a leading
// "the" removed. "Guns N' Roses", "guns n roses" and "Guns N’ Roses" all
// become "guns n roses".
func normalizeArtist(name string) string {
	folded := artistFold.Replace(strings.ToLower(name))

	var b strings.Builder
	for _, r := range folded {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		case unicode.IsSpace(r) || r == '-' || r == '/' || r == '_':
			b.WriteRune(' ')
		}
	}

	normalized := strings.Join(strings.Fields(b.String()), " ")
	// "The Beatles" -> "beatles", but "The The" must not collapse to "the".
	if rest, ok := strings.CutPrefix(normalized, "the "); ok && rest != "the" {
		return rest
	}
	return normalized
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
	Score        int                 `json:"score"`
	Title        string              `json:"title"`
	ArtistCredit []artistCreditEntry `json:"artist-credit"`
	Tags         []tagEntry          `json:"tags"`
	Genres       []tagEntry          `json:"genres"`
}

type artistCreditEntry struct {
	Name       string    `json:"name"`
	JoinPhrase string    `json:"joinphrase"`
	Artist     artistRef `json:"artist"`
}

type artistRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type artistResponse struct {
	Tags   []tagEntry `json:"tags"`
	Genres []tagEntry `json:"genres"`
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
