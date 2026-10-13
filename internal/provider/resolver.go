package provider

import (
	"context"
	"errors"
)

const (
	// MinRecordingTags is how many usable tags a song's recordings must have
	// to be compared on those alone. Below that the artist's tags are added.
	MinRecordingTags = 3
	// ArtistWeight is the weight of the artist's tags relative to the
	// recordings' when both are in use. Recording tags describe the song
	// itself and stay the main signal; artist tags describe a whole career.
	ArtistWeight = 0.5
)

// TrackSearcher finds a song and the tags of its recordings.
type TrackSearcher interface {
	SearchTrack(ctx context.Context, artist, title string) (*Track, error)
}

// ArtistTagger returns the tags of an artist by MBID.
type ArtistTagger interface {
	ArtistTags(ctx context.Context, artistID string) ([]Tag, error)
}

// Resolver finds a song and decides which tags describe it. Recording tags
// are used on their own when there are at least MinRecordingTags usable ones.
// Otherwise the artist's tags are fetched by MBID and either complement the
// few recording tags (TagSourceMixed) or stand in for them (TagSourceArtist).
type Resolver struct {
	tracks  TrackSearcher
	artists ArtistTagger
	usable  func(names []string) int
}

// NewResolver builds a Resolver. usable reports how many of the given tag
// names are of any use for comparison, i.e. survive normalization; it decides
// whether a song's recording tags are enough on their own.
func NewResolver(tracks TrackSearcher, artists ArtistTagger, usable func(names []string) int) *Resolver {
	return &Resolver{tracks: tracks, artists: artists, usable: usable}
}

// SearchTrack implements TrackSearcher. The returned track has TagSource set
// and Tags listing every tag in use.
func (r *Resolver) SearchTrack(ctx context.Context, artist, title string) (*Track, error) {
	track, err := r.tracks.SearchTrack(ctx, artist, title)
	if err != nil {
		return nil, err
	}
	track = track.Clone()
	track.ArtistTags, track.TagSource = nil, ""
	track.Tags = tagNames(track.RecordingTags)

	recordingTags := r.usable(track.Tags)
	if recordingTags >= MinRecordingTags {
		track.TagSource = TagSourceRecording
		return track, nil
	}
	if recordingTags > 0 {
		track.TagSource = TagSourceRecording
	}
	if track.ArtistID == "" {
		return track, nil
	}

	artistTags, err := r.artists.ArtistTags(ctx, track.ArtistID)
	if err != nil {
		if recordingTags > 0 && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			// A thin answer beats none: keep the recording tags we have.
			return track, nil
		}
		return nil, err
	}
	if r.usable(tagNames(artistTags)) == 0 {
		return track, nil
	}

	track.ArtistTags = artistTags
	track.Tags = unionNames(track.Tags, tagNames(artistTags))
	if recordingTags > 0 {
		track.TagSource = TagSourceMixed
	} else {
		track.TagSource = TagSourceArtist
	}
	return track, nil
}

func unionNames(first, second []string) []string {
	seen := make(map[string]struct{}, len(first)+len(second))
	out := make([]string, 0, len(first)+len(second))
	for _, list := range [][]string{first, second} {
		for _, name := range list {
			if _, dup := seen[name]; dup {
				continue
			}
			seen[name] = struct{}{}
			out = append(out, name)
		}
	}
	return out
}
