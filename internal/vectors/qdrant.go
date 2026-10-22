package vectors

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultTimeout = 5 * time.Second
	// defaultCatalogTTL is how long the in-memory catalog used by Search is
	// kept before it is read from Qdrant again, so a pipeline re-run shows up.
	defaultCatalogTTL = 10 * time.Minute
	// catalogRetryDelay is how long a stale catalog keeps being served after a
	// failed refresh before the next attempt.
	catalogRetryDelay = 30 * time.Second
	scrollPageSize    = 1000
	// maxCatalogSize stops Search's catalog from growing without bound if the
	// collection turns out to be much larger than expected.
	maxCatalogSize = 200_000
	// maxResponseBytes caps a response body; a scroll page is about 150 KB.
	maxResponseBytes = 16 << 20
)

// payloadFields are the payload keys read from Qdrant; vectors are never
// fetched.
var payloadFields = []string{"track_id", "title", "artist", "genre", "x", "y"}

// Qdrant is a VectorStore backed by a Qdrant collection, spoken to over its
// REST API. The collection must use cosine distance, so that point scores are
// cosine similarities.
type Qdrant struct {
	baseURL    string
	collection string
	apiKey     string
	httpClient *http.Client
	catalogTTL time.Duration
	now        func() time.Time

	mu        sync.Mutex
	catalog   []catalogEntry
	refreshAt time.Time
}

// Option configures a Qdrant store.
type Option func(*Qdrant)

// WithHTTPClient replaces the default HTTP client (5 s timeout).
func WithHTTPClient(c *http.Client) Option { return func(q *Qdrant) { q.httpClient = c } }

// WithAPIKey sends key in the api-key header, for a Qdrant with auth enabled.
func WithAPIKey(key string) Option { return func(q *Qdrant) { q.apiKey = key } }

// WithCatalogTTL sets how long Search's in-memory catalog is reused.
func WithCatalogTTL(ttl time.Duration) Option { return func(q *Qdrant) { q.catalogTTL = ttl } }

// NewQdrant returns a store for collection on the Qdrant at baseURL
// (e.g. "http://127.0.0.1:6333"). It does not contact Qdrant.
func NewQdrant(baseURL, collection string, opts ...Option) (*Qdrant, error) {
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("vectors: invalid Qdrant URL %q: want http(s)://host:port", baseURL)
	}
	if collection == "" {
		return nil, fmt.Errorf("vectors: empty collection name")
	}
	q := &Qdrant{
		baseURL:    strings.TrimRight(baseURL, "/"),
		collection: collection,
		httpClient: &http.Client{Timeout: defaultTimeout},
		catalogTTL: defaultCatalogTTL,
		now:        time.Now,
	}
	for _, opt := range opts {
		opt(q)
	}
	return q, nil
}

type point struct {
	ID      json.RawMessage `json:"id"`
	Payload Track           `json:"payload"`
	Score   float64         `json:"score"`
}

// Similar implements VectorStore.
func (q *Qdrant) Similar(ctx context.Context, trackID int64, limit int) (*Track, []Neighbor, error) {
	var retrieved struct {
		Result []point `json:"result"`
	}
	err := q.post(ctx, opRetrieve, "/points", map[string]any{
		"ids":          []int64{trackID},
		"with_payload": payloadFields,
		"with_vector":  false,
	}, &retrieved)
	if err != nil {
		return nil, nil, err
	}
	if len(retrieved.Result) == 0 {
		return nil, nil, ErrNotFound
	}
	source := retrieved.Result[0].Payload

	// Querying by id uses the stored vector of that point. The filter keeps
	// the point itself out of the answer explicitly.
	var queried struct {
		Result struct {
			Points []point `json:"points"`
		} `json:"result"`
	}
	err = q.post(ctx, opQuery, "/points/query", map[string]any{
		"query":        trackID,
		"limit":        limit,
		"with_payload": payloadFields,
		"filter": map[string]any{
			"must_not": []any{map[string]any{"has_id": []int64{trackID}}},
		},
	}, &queried)
	if err != nil {
		return nil, nil, err
	}

	neighbors := make([]Neighbor, 0, len(queried.Result.Points))
	for _, p := range queried.Result.Points {
		neighbors = append(neighbors, Neighbor{Track: p.Payload, Score: p.Score})
	}
	return &source, neighbors, nil
}

type catalogEntry struct {
	track  Track
	title  string // lower-cased
	artist string // lower-cased
}

// Search implements VectorStore. It matches against an in-memory copy of all
// payloads: Qdrant's own text match is case-sensitive without a full-text
// index, and a few thousand titles are cheap to scan.
func (q *Qdrant) Search(ctx context.Context, query string, limit int) ([]Track, error) {
	catalog, err := q.loadCatalog(ctx)
	if err != nil {
		return nil, err
	}
	needle := strings.ToLower(query)

	type hit struct {
		rank  int
		entry *catalogEntry
	}
	var hits []hit
	for i := range catalog {
		e := &catalog[i]
		switch {
		case strings.HasPrefix(e.title, needle):
			hits = append(hits, hit{0, e})
		case strings.HasPrefix(e.artist, needle):
			hits = append(hits, hit{1, e})
		case strings.Contains(e.title, needle):
			hits = append(hits, hit{2, e})
		case strings.Contains(e.artist, needle):
			hits = append(hits, hit{3, e})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		a, b := hits[i], hits[j]
		if a.rank != b.rank {
			return a.rank < b.rank
		}
		if a.entry.title != b.entry.title {
			return a.entry.title < b.entry.title
		}
		return a.entry.track.TrackID < b.entry.track.TrackID
	})

	tracks := make([]Track, 0, min(limit, len(hits)))
	for _, h := range hits[:min(limit, len(hits))] {
		tracks = append(tracks, h.entry.track)
	}
	return tracks, nil
}

// loadCatalog returns the cached catalog, reading it from Qdrant when it is
// missing or older than catalogTTL. If a refresh fails while an older copy
// exists, the older copy is served and the refresh is retried later.
func (q *Qdrant) loadCatalog(ctx context.Context) ([]catalogEntry, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.catalog != nil && q.now().Before(q.refreshAt) {
		return q.catalog, nil
	}
	catalog, err := q.scrollAll(ctx)
	if err != nil {
		if q.catalog != nil {
			q.refreshAt = q.now().Add(catalogRetryDelay)
			return q.catalog, nil
		}
		return nil, err
	}
	q.catalog, q.refreshAt = catalog, q.now().Add(q.catalogTTL)
	return catalog, nil
}

func (q *Qdrant) scrollAll(ctx context.Context) ([]catalogEntry, error) {
	var catalog []catalogEntry
	var offset json.RawMessage
	for {
		body := map[string]any{
			"limit":        scrollPageSize,
			"with_payload": payloadFields,
			"with_vector":  false,
		}
		if offset != nil {
			body["offset"] = offset
		}
		var page struct {
			Result struct {
				Points     []point         `json:"points"`
				NextOffset json.RawMessage `json:"next_page_offset"`
			} `json:"result"`
		}
		if err := q.post(ctx, opScroll, "/points/scroll", body, &page); err != nil {
			return nil, err
		}
		for _, p := range page.Result.Points {
			catalog = append(catalog, catalogEntry{
				track:  p.Payload,
				title:  strings.ToLower(p.Payload.Title),
				artist: strings.ToLower(p.Payload.Artist),
			})
		}
		next := page.Result.NextOffset
		if len(next) == 0 || string(next) == "null" || len(catalog) >= maxCatalogSize {
			return catalog, nil
		}
		offset = next
	}
}

// post sends a JSON request to the collection's endpoint path and decodes
// the JSON answer into out. Every attempt is timed; failures are counted by
// reason.
func (q *Qdrant) post(ctx context.Context, op, path string, body, out any) (err error) {
	start := time.Now()
	defer func() {
		requestDuration.WithLabelValues(op).Observe(time.Since(start).Seconds())
		if err != nil {
			errorsTotal.WithLabelValues(op, errorReason(err)).Inc()
		}
	}()

	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("vectors: encode %s request: %w", op, err)
	}
	endpoint := q.baseURL + "/collections/" + url.PathEscape(q.collection) + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("vectors: build %s request: %w", op, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if q.apiKey != "" {
		req.Header.Set("api-key", q.apiKey)
	}

	resp, err := q.httpClient.Do(req)
	if err != nil {
		return &UpstreamError{Err: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
		return &UpstreamError{Status: resp.StatusCode}
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(out); err != nil {
		return fmt.Errorf("%w: decode %s response: %v", ErrUpstream, op, err)
	}
	return nil
}
