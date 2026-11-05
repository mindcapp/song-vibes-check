// Package analytics reads listener behaviour (skips, plays) from ClickHouse,
// where lab-events/ aggregates the event stream into a track-to-track
// transition matrix, and uses it to rerank similar tracks.
package analytics

import (
	"context"
	"errors"
	"fmt"
)

// Stat is how often listeners played one track right after another and how
// often they skipped it. Plays counts complete and skip events; likes are
// not plays.
type Stat struct {
	Plays uint64
	Skips uint64
}

// TransitionStats answers how listeners behaved after a given track.
type TransitionStats interface {
	// Stats returns, for every track in to that was ever played right after
	// from, the summed plays and skips. Tracks without any transition are
	// absent from the result.
	Stats(ctx context.Context, from int64, to []int64) (map[int64]Stat, error)
}

// ErrUpstream is returned when ClickHouse cannot be reached, is too slow, or
// answers with an error or an unreadable body. Errors of this kind are
// *UpstreamError values.
var ErrUpstream = errors.New("analytics: upstream error")

// errBadResponse marks an answer that arrived but could not be understood.
var errBadResponse = errors.New("unreadable response")

// UpstreamError describes a failed call to ClickHouse. It matches ErrUpstream
// with errors.Is.
type UpstreamError struct {
	// Status is the HTTP status ClickHouse answered with, or 0 when there was
	// no usable answer (network error, timeout, broken body).
	Status int
	// Detail is the start of ClickHouse's error text, for logs only.
	Detail string
	// Err is the underlying error, if any.
	Err error
}

func (e *UpstreamError) Error() string {
	switch {
	case e.Err != nil:
		return fmt.Sprintf("%v: %v", ErrUpstream, e.Err)
	case e.Detail != "":
		return fmt.Sprintf("%v: status %d: %s", ErrUpstream, e.Status, e.Detail)
	default:
		return fmt.Sprintf("%v: status %d", ErrUpstream, e.Status)
	}
}

func (e *UpstreamError) Is(target error) bool { return target == ErrUpstream }

func (e *UpstreamError) Unwrap() error { return e.Err }
