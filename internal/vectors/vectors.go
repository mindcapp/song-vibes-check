// Package vectors finds similar tracks in a vector database filled by the
// offline pipeline (pipeline/ in the repository).
package vectors

import (
	"context"
	"errors"
	"fmt"
)

// Track is the payload stored with every point: the track's metadata and its
// 2D map coordinates (UMAP). Genre is the FMA genre_top.
type Track struct {
	TrackID int64   `json:"track_id"`
	Title   string  `json:"title"`
	Artist  string  `json:"artist"`
	Genre   string  `json:"genre"`
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
}

// Neighbor is a track returned by a similarity search with its cosine
// similarity to the query track (1 means identical direction).
type Neighbor struct {
	Track
	Score float64 `json:"score"`
}

// VectorStore looks tracks up by their audio-feature vectors.
type VectorStore interface {
	// Similar returns the track with the given id and up to limit of its
	// nearest neighbours, most similar first. The track itself is never among
	// the neighbours. ErrNotFound means there is no track with that id.
	Similar(ctx context.Context, trackID int64, limit int) (*Track, []Neighbor, error)
	// Search returns up to limit tracks whose title or artist contains q,
	// case-insensitively.
	Search(ctx context.Context, q string, limit int) ([]Track, error)
}

// ErrNotFound is returned when there is no track with the requested id.
var ErrNotFound = errors.New("vectors: track not found")

// ErrUpstream is returned when the vector database cannot be reached or
// answers with an unexpected status. Errors of this kind are *UpstreamError.
var ErrUpstream = errors.New("vectors: upstream error")

// UpstreamError describes a failed call to the vector database. It matches
// ErrUpstream with errors.Is.
type UpstreamError struct {
	// Status is the HTTP status the database answered with, or 0 when there
	// was no usable answer (network error, timeout, broken body).
	Status int
	// Err is the underlying error, if any.
	Err error
}

func (e *UpstreamError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%v: %v", ErrUpstream, e.Err)
	}
	return fmt.Sprintf("%v: status %d", ErrUpstream, e.Status)
}

func (e *UpstreamError) Is(target error) bool { return target == ErrUpstream }

func (e *UpstreamError) Unwrap() error { return e.Err }
