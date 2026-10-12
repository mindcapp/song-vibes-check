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
	"time"
	"unicode"
)

const (
	defaultBaseURL       = "https://musicbrainz.org/ws/2"
	defaultRateLimit     = time.Second
	defaultTimeout       = 10 * time.Second
	defaultMaxRetryAfter = 3 * time.Second

	// minScore is the lowest MusicBrainz search score (0-100) a recording
	// may have to be treated as the requested song.
	minScore = 90
	// maxMerged is how many matching recordings contribute tags.
	maxMerged = 5
	// taggedLimit is the page size for the search restricted to recordings
	// that have tags. It is the API maximum on purpose: the result is only
	// stable when the whole set fits in one page (see SearchTrack).
	taggedLimit = 100
	// plainLimit is the page size for the unrestricted search, which is only
	// used to confirm the song exists and to learn its artist's MBID.
	plainLimit = 25
)

// ErrNotFound is returned when MusicBrainz has no recording matching the query.
var ErrNotFound = errors.New("musicbrainz: track not found")

// ErrUpstream is returned when MusicBrainz cannot be reached or responds with
// an unexpected status. Errors of this kind are *UpstreamError values.
var ErrUpstream = errors.New("musicbrainz: upstream error")

// errNoSuchEntity is returned internally for a 404 on a direct lookup.
var errNoSuchEntity = errors.New("musicbrainz: no such entity")

// UpstreamError describes a failed call to MusicBrainz. It matches ErrUpstream
// with errors.Is.
type UpstreamError struct {
	// Status is the HTTP status MusicBrainz answered with, or 0 when there
	// was no answer (network error, timeout).
	Status int
	// RetryAfter is how long MusicBrainz asked the caller to wait, if it did.
	RetryAfter time.Duration
	// Err is the underlying transport error, if any.
	Err error
}

func (e *UpstreamError) Error() string {
	switch {
	case e.Err != nil:
		return fmt.Sprintf("%v: %v", ErrUpstream, e.Err)
	case e.RetryAfter > 0:
		return fmt.Sprintf("%v: status %d (retry after %s)", ErrUpstream, e.Status, e.RetryAfter)
	default:
		return fmt.Sprintf("%v: status %d", ErrUpstream, e.Status)
	}
}

func (e *UpstreamError) Is(target error) bool { return target == ErrUpstream }

func (e *UpstreamError) Unwrap() error { return e.Err }

// Tag is a folksonomy tag or genre together with the number of user votes
// MusicBrainz reports for it.
type Tag struct {
	Name  string
	Count int
}

// Where a track's tags came from.
const (
	TagSourceRecording = "recording"
	TagSourceArtist    = "artist"
	TagSourceMixed     = "mixed"
)

// Track is the subset of MusicBrainz data the rest of the service needs.
type Track struct {
	ID     string
	Title  string
	Artist string
	// ArtistID is the MBID of the credited artist that matched the request.
	ArtistID string
	// Tags holds the names of every tag in use for this track, as MusicBrainz
	// spells them (lower-cased): recording tags first, then artist tags.
	Tags []string
	// RecordingTags are the tags of the song's own recordings, with votes.
	RecordingTags []Tag
	// ArtistTags are the artist's tags. They are only filled in when the
	// recordings are too thinly tagged to compare on their own (see Resolver).
	ArtistTags []Tag
	// TagSource is TagSourceRecording, TagSourceArtist or TagSourceMixed
	// depending on which of the two lists above is in use; empty when the
	// track has no tags at all. It is set by Resolver.
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
	clone.RecordingTags = append([]Tag(nil), t.RecordingTags...)
	clone.ArtistTags = append([]Tag(nil), t.ArtistTags...)
	return &clone
}

// Client talks to the MusicBrainz API while respecting its rate limit policy
// of one request per second on average.
type Client struct {
	httpClient    *http.Client
	userAgent     string
	baseURL       string
	limiter       *rateLimiter
	maxRetryAfter time.Duration
}

// Option customises a Client.
type Option func(*Client)

// WithBaseURL overrides the MusicBrainz web service root
// (https://musicbrainz.org/ws/2 by default).
func WithBaseURL(baseURL string) Option {
	return func(c *Client) { c.baseURL = strings.TrimRight(baseURL, "/") }
}

// WithRateInterval overrides the minimum pause between one response and the
// next request.
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
		httpClient:    &http.Client{Timeout: defaultTimeout},
		userAgent:     userAgent,
		baseURL:       defaultBaseURL,
		limiter:       newRateLimiter(defaultRateLimit),
		maxRetryAfter: defaultMaxRetryAfter,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// SearchTrack looks up a song by artist and title and returns the tags of its
// recordings, together with the MBID of its artist.
//
// A popular song exists in MusicBrainz as hundreds of recordings (studio,
// live, remaster, ...) that score equally in search. Only a handful of them
// carry tags, and which page of the hundreds comes back differs from call to
// call, so reading tags off an ordinary search gives a different answer every
// time. The search is therefore restricted to recordings that have tags
// ("tag:*"): that set is small enough to arrive whole, which makes the result
// repeatable.
//
// Of those, a recording takes part only if its search score is at least
// minScore and it is credited to the requested artist (compared after
// normalization, see normalizeArtist). The tags of the maxMerged best tagged
// ones are merged, summing votes.
//
// When no tagged recording matches, an ordinary search confirms that the song
// exists and finds its artist; the track is then returned without recording
// tags. ErrNotFound means no recording matched at all.
func (c *Client) SearchTrack(ctx context.Context, artist, title string) (*Track, error) {
	tagged, err := c.search(ctx, artist, title, true)
	if err != nil {
		return nil, err
	}

	if best := bestTagged(tagged); len(best) > 0 {
		weighted := sumTags(best)
		return &Track{
			ID:            best[0].rec.ID,
			Title:         best[0].rec.Title,
			Artist:        best[0].artist.Name,
			ArtistID:      best[0].artist.ID,
			Tags:          tagNames(weighted),
			RecordingTags: weighted,
		}, nil
	}

	plain, err := c.search(ctx, artist, title, false)
	if err != nil {
		return nil, err
	}
	if len(plain) == 0 {
		return nil, ErrNotFound
	}

	// Search order is not stable; pick by ID so the same data always names
	// the same recording.
	sort.Slice(plain, func(i, j int) bool { return plain[i].rec.ID < plain[j].rec.ID })
	first := plain[0]
	return &Track{ID: first.rec.ID, Title: first.rec.Title, Artist: first.artist.Name, ArtistID: first.artist.ID}, nil
}

// ArtistTags returns the tags of an artist by MBID, strongest first. This is
// a direct lookup, not a search, so the answer does not vary between calls.
// An unknown MBID yields no tags rather than an error.
func (c *Client) ArtistTags(ctx context.Context, artistID string) ([]Tag, error) {
	// Genres are the subset of tags on MusicBrainz's genre list and repeat
	// their vote counts; both are requested so neither spelling is missed.
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

// search runs one recording search and returns the results that match the
// requested artist with a high enough score.
func (c *Client) search(ctx context.Context, artist, title string, taggedOnly bool) ([]match, error) {
	query := fmt.Sprintf(`artist:"%s" AND recording:"%s"`, escapeLucene(artist), escapeLucene(title))
	limit := plainLimit
	if taggedOnly {
		query += " AND tag:*"
		limit = taggedLimit
	}

	u := c.baseURL + "/recording/?" + url.Values{
		"query": {query},
		"fmt":   {"json"},
		"limit": {strconv.Itoa(limit)},
	}.Encode()

	var parsed searchResponse
	if err := c.getJSON(ctx, u, &parsed); err != nil {
		if errors.Is(err, errNoSuchEntity) {
			return nil, &UpstreamError{Status: http.StatusNotFound}
		}
		return nil, err
	}
	return matchingRecordings(parsed.Recordings, artist), nil
}

// getJSON performs a GET and decodes the JSON response. A 503 or a network
// error is retried once; nothing else is.
func (c *Client) getJSON(ctx context.Context, reqURL string, out any) error {
	err := c.attempt(ctx, reqURL, out)
	if retryable(ctx, err) {
		err = c.attempt(ctx, reqURL, out)
	}
	return err
}

func retryable(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	var upstream *UpstreamError
	if !errors.As(err, &upstream) {
		return false
	}
	return upstream.Status == http.StatusServiceUnavailable || upstream.Status == 0
}

// attempt makes one rate-limited request. The limiter slot is held until the
// response has been read, so the pause before the next request is measured
// from the end of this one.
func (c *Client) attempt(ctx context.Context, reqURL string, out any) error {
	if err := c.limiter.acquire(ctx); err != nil {
		return err
	}
	var backoff time.Duration
	defer func() { c.limiter.release(backoff) }()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return fmt.Errorf("musicbrainz: build request: %w", err)
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return &UpstreamError{Err: err}
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return errNoSuchEntity
	case resp.StatusCode != http.StatusOK:
		retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		// Honour the server's request for every caller sharing this client,
		// but never stall the queue for longer than maxRetryAfter.
		backoff = min(retryAfter, c.maxRetryAfter)
		return &UpstreamError{Status: resp.StatusCode, RetryAfter: retryAfter}
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("musicbrainz: decode response: %w", err)
	}
	return nil
}

// parseRetryAfter reads a Retry-After header, which is either a number of
// seconds or an HTTP date. It returns 0 when the header is absent or invalid.
func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds < 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil && at.After(now) {
		return at.Sub(now)
	}
	return 0
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
		if int(rec.Score) < minScore {
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

var artistFold = strings.NewReplacer(
	"&", " and ",
	"á", "a", "à", "a", "â", "a", "ä", "a", "ã", "a", "å", "a",
	"é", "e", "è", "e", "ê", "e", "ë", "e",
	"í", "i", "ì", "i", "î", "i", "ï", "i",
	"ó", "o", "ò", "o", "ô", "o", "ö", "o", "õ", "o", "ø", "o",
	"ú", "u", "ù", "u", "û", "u", "ü", "u",
	"ñ", "n", "ç", "c", "ß", "ss",
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
	Score        flexInt             `json:"score"`
	Title        string              `json:"title"`
	ArtistCredit []artistCreditEntry `json:"artist-credit"`
	Tags         []tagEntry          `json:"tags"`
	Genres       []tagEntry          `json:"genres"`
}

// flexInt decodes a JSON number or a numeric string. The live API sends the
// search score as a number while its documentation shows a string.
type flexInt int

func (f *flexInt) UnmarshalJSON(data []byte) error {
	n, err := strconv.Atoi(strings.Trim(string(data), `"`))
	if err != nil {
		return fmt.Errorf("musicbrainz: score %s is not an integer", data)
	}
	*f = flexInt(n)
	return nil
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

// rateLimiter lets one request through at a time and keeps a pause between
// the end of one response and the start of the next request, which is how
// MusicBrainz's "one request per second" policy is honoured. Waiting for a
// turn stops as soon as the caller's context ends.
type rateLimiter struct {
	interval time.Duration
	// slot holds the time before which the next request must not start. It
	// has capacity 1: taking the value takes the turn, putting one back ends it.
	slot chan time.Time
}

func newRateLimiter(interval time.Duration) *rateLimiter {
	rl := &rateLimiter{interval: interval, slot: make(chan time.Time, 1)}
	rl.slot <- time.Time{}
	return rl
}

// acquire waits for the caller's turn and for the pause after the previous
// response. On success the caller must call release.
func (rl *rateLimiter) acquire(ctx context.Context) error {
	var notBefore time.Time
	select {
	case notBefore = <-rl.slot:
	case <-ctx.Done():
		return ctx.Err()
	}

	if wait := time.Until(notBefore); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			rl.slot <- notBefore // the turn was not used; the pause still stands
			return ctx.Err()
		}
	}
	return nil
}

// release ends the caller's turn. The next request may start after the
// regular interval, or after backoff if the server asked for a longer pause.
func (rl *rateLimiter) release(backoff time.Duration) {
	rl.slot <- time.Now().Add(max(rl.interval, backoff))
}
