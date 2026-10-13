package provider

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type stubTracks struct {
	track *Track
	err   error
}

func (s stubTracks) SearchTrack(context.Context, string, string) (*Track, error) {
	return s.track, s.err
}

type stubArtists struct {
	tags  []Tag
	err   error
	calls []string
}

func (s *stubArtists) ArtistTags(_ context.Context, artistID string) ([]Tag, error) {
	s.calls = append(s.calls, artistID)
	return s.tags, s.err
}

// usableExceptJunk counts every tag but "junk", standing in for the normalizer.
func usableExceptJunk(names []string) int {
	n := 0
	for _, name := range names {
		if name != "junk" {
			n++
		}
	}
	return n
}

func recordingTrack(tags ...Tag) *Track {
	return &Track{ID: "rec-1", Title: "Song", Artist: "Queen", ArtistID: "queen-id", Tags: tagNames(tags), RecordingTags: tags}
}

func TestResolver(t *testing.T) {
	artistTags := []Tag{{"rock", 9}, {"glam rock", 4}}

	tests := []struct {
		name            string
		track           *Track
		artistTags      []Tag
		artistErr       error
		wantErr         error
		wantSource      string
		wantTags        []string
		wantArtistTags  []Tag
		wantArtistCalls []string
	}{
		{
			name:       "three recording tags are enough on their own",
			track:      recordingTrack(Tag{"rock", 5}, Tag{"hard rock", 2}, Tag{"classic rock", 1}),
			artistTags: artistTags,
			wantSource: TagSourceRecording,
			wantTags:   []string{"rock", "hard rock", "classic rock"},
		},
		{
			name:            "two recording tags are complemented by artist tags",
			track:           recordingTrack(Tag{"rock", 5}, Tag{"hard rock", 2}),
			artistTags:      artistTags,
			wantSource:      TagSourceMixed,
			wantTags:        []string{"rock", "hard rock", "glam rock"},
			wantArtistTags:  artistTags,
			wantArtistCalls: []string{"queen-id"},
		},
		{
			name:            "junk does not count towards the three",
			track:           recordingTrack(Tag{"rock", 5}, Tag{"hard rock", 2}, Tag{"junk", 9}),
			artistTags:      artistTags,
			wantSource:      TagSourceMixed,
			wantTags:        []string{"rock", "hard rock", "junk", "glam rock"},
			wantArtistTags:  artistTags,
			wantArtistCalls: []string{"queen-id"},
		},
		{
			name:            "no recording tags: artist tags stand in",
			track:           recordingTrack(),
			artistTags:      artistTags,
			wantSource:      TagSourceArtist,
			wantTags:        []string{"rock", "glam rock"},
			wantArtistTags:  artistTags,
			wantArtistCalls: []string{"queen-id"},
		},
		{
			name:            "artist has nothing usable: thin recording tags stay as they are",
			track:           recordingTrack(Tag{"rock", 5}),
			artistTags:      []Tag{{"junk", 3}},
			wantSource:      TagSourceRecording,
			wantTags:        []string{"rock"},
			wantArtistCalls: []string{"queen-id"},
		},
		{
			name:            "no tags anywhere",
			track:           recordingTrack(),
			wantSource:      "",
			wantTags:        nil,
			wantArtistCalls: []string{"queen-id"},
		},
		{
			name:       "no artist MBID: nothing to look up",
			track:      &Track{ID: "rec-1", RecordingTags: []Tag{{"rock", 1}}},
			artistTags: artistTags,
			wantSource: TagSourceRecording,
			wantTags:   []string{"rock"},
		},
		{
			name:            "artist lookup fails but there are recording tags: keep them",
			track:           recordingTrack(Tag{"rock", 5}),
			artistErr:       &UpstreamError{Status: 503},
			wantSource:      TagSourceRecording,
			wantTags:        []string{"rock"},
			wantArtistCalls: []string{"queen-id"},
		},
		{
			name:      "artist lookup fails and there is nothing else: error",
			track:     recordingTrack(),
			artistErr: &UpstreamError{Status: 503},
			wantErr:   ErrUpstream,
		},
		{
			name:      "an abandoned request is not papered over",
			track:     recordingTrack(Tag{"rock", 5}),
			artistErr: context.Canceled,
			wantErr:   context.Canceled,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			artists := &stubArtists{tags: tt.artistTags, err: tt.artistErr}
			resolver := NewResolver(stubTracks{track: tt.track}, artists, usableExceptJunk)

			got, err := resolver.SearchTrack(context.Background(), "Queen", "Song")
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if got.TagSource != tt.wantSource {
				t.Errorf("TagSource = %q, want %q", got.TagSource, tt.wantSource)
			}
			if !reflect.DeepEqual(got.Tags, tt.wantTags) {
				t.Errorf("Tags = %v, want %v", got.Tags, tt.wantTags)
			}
			if !reflect.DeepEqual(got.ArtistTags, tt.wantArtistTags) {
				t.Errorf("ArtistTags = %v, want %v", got.ArtistTags, tt.wantArtistTags)
			}
			if !reflect.DeepEqual(artists.calls, tt.wantArtistCalls) {
				t.Errorf("artist lookups = %v, want %v", artists.calls, tt.wantArtistCalls)
			}
		})
	}
}

func TestResolverPassesSearchErrorsThrough(t *testing.T) {
	for _, want := range []error{ErrNotFound, ErrUpstream} {
		artists := &stubArtists{}
		resolver := NewResolver(stubTracks{err: want}, artists, usableExceptJunk)

		if _, err := resolver.SearchTrack(context.Background(), "Queen", "Song"); !errors.Is(err, want) {
			t.Errorf("error = %v, want %v", err, want)
		}
		if len(artists.calls) != 0 {
			t.Errorf("artist looked up although the search failed with %v", want)
		}
	}
}

func TestResolverDoesNotModifyTheTrackItWasGiven(t *testing.T) {
	original := recordingTrack(Tag{"rock", 5})
	resolver := NewResolver(stubTracks{track: original}, &stubArtists{tags: []Tag{{"glam rock", 4}}}, usableExceptJunk)

	got, err := resolver.SearchTrack(context.Background(), "Queen", "Song")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == original || len(original.ArtistTags) != 0 || original.TagSource != "" || len(original.Tags) != 1 {
		t.Errorf("the searcher's track was modified: %+v", original)
	}
}
