package tags

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func mustLoad(t *testing.T) *Normalizer {
	t.Helper()
	n, err := Load("")
	if err != nil {
		t.Fatalf("load embedded defaults: %v", err)
	}
	return n
}

func TestNormalize(t *testing.T) {
	n := mustLoad(t)

	tests := []struct {
		raw    string
		want   string
		wantOK bool
	}{
		{raw: "Rock", want: "rock", wantOK: true},
		{raw: "  Glam   Rock ", want: "glam rock", wantOK: true},
		{raw: "hip hop", want: "hip hop", wantOK: true},
		{raw: "Hip-Hop", want: "hip hop", wantOK: true},
		{raw: "hiphop", want: "hip hop", wantOK: true},
		{raw: "hip_hop", want: "hip hop", wantOK: true},
		{raw: "Hip–Hop", want: "hip hop", wantOK: true},
		{raw: "RnB", want: "r&b", wantOK: true},
		{raw: "Rhythm and Blues", want: "r&b", wantOK: true},
		{raw: "rock & roll", want: "rock and roll", wantOK: true},
		{raw: "Rock'n'Roll", want: "rock and roll", wantOK: true},
		{raw: "rock ’n’ roll", want: "rock and roll", wantOK: true},
		{raw: "synth-pop", want: "synth pop", wantOK: true},
		{raw: "synthpop", want: "synth pop", wantOK: true},
		{raw: "post-punk", want: "post punk", wantOK: true},
		{raw: "british rock", want: "british rock", wantOK: true},
		{raw: "classic rock", want: "classic rock", wantOK: true},

		{raw: "", wantOK: false},
		{raw: "   ", wantOK: false},
		{raw: "seen live", wantOK: false},
		{raw: "Seen-Live", wantOK: false},
		{raw: "British", wantOK: false},
		{raw: "USA", wantOK: false},
		{raw: "1975", wantOK: false},
		{raw: "80s", wantOK: false},
		{raw: "1990s", wantOK: false},
		{raw: "'90s", wantOK: false},
		{raw: "00s", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got, ok := n.Normalize(tt.raw)
			if ok != tt.wantOK || got != tt.want {
				t.Errorf("Normalize(%q) = (%q, %v), want (%q, %v)", tt.raw, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestNormalizeAll(t *testing.T) {
	n := mustLoad(t)

	got := n.NormalizeAll([]string{"Hip-Hop", "seen live", "rock", "hiphop", "1994", "Rock", "american", "rap"})
	want := []string{"hip hop", "rock", "rap"}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("NormalizeAll = %v, want %v", got, want)
	}

	if got := n.NormalizeAll(nil); len(got) != 0 {
		t.Errorf("NormalizeAll(nil) = %v, want empty", got)
	}
}

func TestStoplistAppliesToSynonyms(t *testing.T) {
	n, err := New(strings.NewReader("hiphop\n"), strings.NewReader("hip hop: hiphop\n"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for _, raw := range []string{"hip hop", "hip-hop", "hiphop"} {
		if tag, ok := n.Normalize(raw); ok {
			t.Errorf("Normalize(%q) = %q, want it stop-listed", raw, tag)
		}
	}
}

func TestLoadCustomStoplistReplacesDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stoplist.txt")
	if err := os.WriteFile(path, []byte("# my list\n\nRock\n"), 0o600); err != nil {
		t.Fatalf("write stop list: %v", err)
	}

	n, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if _, ok := n.Normalize("rock"); ok {
		t.Error("custom stop list entry \"rock\" was not applied")
	}
	if _, ok := n.Normalize("seen live"); !ok {
		t.Error("default stop list is still active, want it replaced")
	}
	if tag, _ := n.Normalize("hip-hop"); tag != "hip hop" {
		t.Errorf("synonyms lost with custom stop list: got %q", tag)
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "missing.txt")); err == nil {
		t.Error("expected an error for a missing stop list file")
	}
}

func TestNewRejectsMalformedSynonyms(t *testing.T) {
	for _, bad := range []string{"no separator here\n", ": orphan\n"} {
		if _, err := New(strings.NewReader(""), strings.NewReader(bad)); err == nil {
			t.Errorf("New accepted malformed synonyms %q", bad)
		}
	}
}
