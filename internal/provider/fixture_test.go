package provider

import (
	"context"
	"net/http"
	"os"
	"reflect"
	"testing"
)

// TestSearchTrackOnRecordedResponse runs the client against a real MusicBrainz
// answer, recorded once and replayed from testdata through httptest. The unit
// tests elsewhere build their own JSON and so only prove the client agrees
// with itself; this one fails if the client stops understanding what
// MusicBrainz actually sends (field names, the score, the artist credit).
//
// The fixture is the response to
//
//	GET /ws/2/recording/?query=artist:"Justice" AND recording:"D.A.N.C.E." AND tag:*&fmt=json&limit=100
//
// recorded on 2026-10-12. It holds 22 recordings: 14 by Justice scoring 90 or
// more, 4 by Justice scoring less, 3 by other artists ("Jimmy Justice",
// "Justice Crew") and one collaboration below the score threshold.
// To refresh it, repeat the request and update the expectations below.
func TestSearchTrackOnRecordedResponse(t *testing.T) {
	fixture, err := os.ReadFile("testdata/recording_search_tagged.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	var requests []string
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.Path+"?"+r.URL.Query().Get("query"))
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(fixture)
	})

	track, err := client.SearchTrack(context.Background(), "justice", "D.A.N.C.E.")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if want := []string{`/recording/?artist:"justice" AND recording:"D.A.N.C.E." AND tag:*`}; !reflect.DeepEqual(requests, want) {
		t.Errorf("requests = %q, want one tagged search %q", requests, want)
	}

	// The best tagged recording by Justice (51 votes) names the track.
	if want := "d18a1284-d6a1-42d9-b0c1-b247e9f27b80"; track.ID != want {
		t.Errorf("ID = %q, want %q", track.ID, want)
	}
	if track.Title != "D.A.N.C.E." || track.Artist != "Justice" {
		t.Errorf("title/artist = %q / %q, want D.A.N.C.E. / Justice", track.Title, track.Artist)
	}
	if want := "860b2707-6153-4e3a-aa57-74d2b42c55b5"; track.ArtistID != want {
		t.Errorf("ArtistID = %q, want %q", track.ArtistID, want)
	}

	// Votes summed over the five best tagged recordings (51, 21, 16, 14 and 9
	// votes; the tie at 9 is broken by recording ID).
	wantTop := []Tag{
		{"electronic", 18}, {"house", 11}, {"electro", 10}, {"elektro", 8},
		{"dance", 7}, {"club/dance", 6}, {"club", 5}, {"electro house", 5},
	}
	if len(track.RecordingTags) != 39 {
		t.Errorf("%d merged tags, want 39", len(track.RecordingTags))
	}
	if len(track.RecordingTags) < len(wantTop) || !reflect.DeepEqual(track.RecordingTags[:len(wantTop)], wantTop) {
		t.Errorf("top tags = %v\nwant       %v", track.RecordingTags, wantTop)
	}
	if len(track.Tags) != len(track.RecordingTags) || track.Tags[0] != "electronic" {
		t.Errorf("Tags = %v, want the names of RecordingTags", track.Tags)
	}

	// Recordings by other artists must not leak in: "soul and r&b" is tagged
	// only on Jimmy Justice's "Save the Last Dance for Me".
	for _, tag := range track.RecordingTags {
		if tag.Name == "soul and r&b" {
			t.Error("tags of another artist's recording were merged in")
		}
	}
}

// TestRecordedResponseForAnotherArtist uses the same fixture to check that a
// request for an artist who merely shares a word with the results does not
// match them.
func TestRecordedResponseForAnotherArtist(t *testing.T) {
	fixture, err := os.ReadFile("testdata/recording_search_tagged.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	served := 0
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		served++
		if served == 1 {
			_, _ = w.Write(fixture)
			return
		}
		_, _ = w.Write([]byte(`{"recordings":[]}`))
	})

	// "Justice Crew" is in the fixture, but only with a score of 66.
	if _, err := client.SearchTrack(context.Background(), "Justice Crew", "Dance With Me"); err != ErrNotFound {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}
