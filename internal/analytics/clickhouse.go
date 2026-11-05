package analytics

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultTimeout bounds one ClickHouse request: reranking is an
	// enhancement and must not make /api/v1/similar slow.
	DefaultTimeout = 500 * time.Millisecond
	// DefaultDatabase is the database lab-events/clickhouse-init creates.
	DefaultDatabase = "vibe"

	// maxTargets caps how many tracks one query may ask about.
	maxTargets = 1000
	// maxResponseBytes caps a response body: one short row per target.
	maxResponseBytes = 4 << 20
	// maxErrorDetail is how much of ClickHouse's error text is kept for logs.
	maxErrorDetail = 300
)

// statsQuery sums the transition matrix. ClickHouse's SummingMergeTree merges
// rows in the background, so the table must be read through sum() and
// GROUP BY. The aliases differ from the column names on purpose: an alias
// equal to a column makes ClickHouse nest aggregates (ILLEGAL_AGGREGATION).
// Track ids are Strings in ClickHouse; all values are bound as parameters.
const statsQuery = `SELECT to_track, sum(plays) AS total_plays, sum(skips) AS total_skips
FROM transitions
WHERE from_track = {from_track:String} AND to_track IN {to_tracks:Array(String)}
GROUP BY to_track
FORMAT JSONEachRow`

// ClickHouse is a TransitionStats backed by ClickHouse's HTTP interface.
type ClickHouse struct {
	baseURL    string
	database   string
	user       string
	password   string
	timeout    time.Duration
	httpClient *http.Client
}

// Option configures a ClickHouse store.
type Option func(*ClickHouse)

// WithCredentials sets the user and password, sent in the X-ClickHouse-User
// and X-ClickHouse-Key headers (never in the URL). An empty user means
// ClickHouse's "default" user.
func WithCredentials(user, password string) Option {
	return func(c *ClickHouse) { c.user, c.password = user, password }
}

// WithDatabase selects the database holding the transitions table.
func WithDatabase(name string) Option { return func(c *ClickHouse) { c.database = name } }

// WithTimeout bounds each request (default DefaultTimeout).
func WithTimeout(d time.Duration) Option { return func(c *ClickHouse) { c.timeout = d } }

// WithHTTPClient replaces the default HTTP client.
func WithHTTPClient(h *http.Client) Option { return func(c *ClickHouse) { c.httpClient = h } }

// NewClickHouse returns a store for the ClickHouse HTTP interface at
// baseURL (e.g. "http://127.0.0.1:8123"). It does not contact ClickHouse.
func NewClickHouse(baseURL string, opts ...Option) (*ClickHouse, error) {
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("analytics: invalid ClickHouse URL %q: want http(s)://host:port", baseURL)
	}
	c := &ClickHouse{
		baseURL:  strings.TrimRight(baseURL, "/"),
		database: DefaultDatabase,
		timeout:  DefaultTimeout,
	}
	for _, opt := range opts {
		opt(c)
	}
	if c.timeout <= 0 {
		return nil, fmt.Errorf("analytics: timeout must be positive, got %s", c.timeout)
	}
	if c.httpClient == nil {
		c.httpClient = &http.Client{}
	}
	return c, nil
}

// Stats implements TransitionStats.
func (c *ClickHouse) Stats(ctx context.Context, from int64, to []int64) (stats map[int64]Stat, err error) {
	if len(to) == 0 {
		return map[int64]Stat{}, nil
	}
	if len(to) > maxTargets {
		return nil, fmt.Errorf("analytics: %d target tracks, at most %d", len(to), maxTargets)
	}

	start := time.Now()
	defer func() {
		requestDuration.Observe(time.Since(start).Seconds())
		if err != nil {
			errorsTotal.WithLabelValues(errorReason(err)).Inc()
		}
	}()

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	// A GET makes ClickHouse run the query read-only. Ids are formatted from
	// int64 here and bound as typed parameters, so nothing is concatenated
	// into the SQL text.
	values := url.Values{
		"query":            {statsQuery},
		"database":         {c.database},
		"param_from_track": {strconv.FormatInt(from, 10)},
		"param_to_tracks":  {arrayLiteral(to)},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/?"+values.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("analytics: build request: %w", err)
	}
	if c.user != "" {
		req.Header.Set("X-ClickHouse-User", c.user)
	}
	if c.password != "" {
		req.Header.Set("X-ClickHouse-Key", c.password)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, &UpstreamError{Err: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorDetail))
		return nil, &UpstreamError{Status: resp.StatusCode, Detail: strings.TrimSpace(string(detail))}
	}
	return parseRows(resp.Body)
}

// arrayLiteral formats ids as a ClickHouse Array(String) parameter value:
// ['1','2'].
func arrayLiteral(ids []int64) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, id := range ids {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('\'')
		b.WriteString(strconv.FormatInt(id, 10))
		b.WriteByte('\'')
	}
	b.WriteByte(']')
	return b.String()
}

// counter decodes a UInt64 that ClickHouse writes as a quoted string by
// default (output_format_json_quote_64bit_integers) or as a bare number.
type counter uint64

func (n *counter) UnmarshalJSON(b []byte) error {
	v, err := strconv.ParseUint(strings.Trim(string(b), `"`), 10, 64)
	if err != nil {
		return err
	}
	*n = counter(v)
	return nil
}

type row struct {
	ToTrack    string  `json:"to_track"`
	TotalPlays counter `json:"total_plays"`
	TotalSkips counter `json:"total_skips"`
}

func parseRows(r io.Reader) (map[int64]Stat, error) {
	stats := make(map[int64]Stat)
	scanner := bufio.NewScanner(io.LimitReader(r, maxResponseBytes))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var row row
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return nil, &UpstreamError{Err: fmt.Errorf("%w: decode row: %v", errBadResponse, err)}
		}
		id, err := strconv.ParseInt(row.ToTrack, 10, 64)
		if err != nil {
			return nil, &UpstreamError{Err: fmt.Errorf("%w: track id %q is not an integer", errBadResponse, row.ToTrack)}
		}
		stats[id] = Stat{Plays: uint64(row.TotalPlays), Skips: uint64(row.TotalSkips)}
	}
	if err := scanner.Err(); err != nil {
		// Includes a timeout or reset while the body was being read.
		return nil, &UpstreamError{Err: fmt.Errorf("%w: %w", errBadResponse, err)}
	}
	return stats, nil
}
