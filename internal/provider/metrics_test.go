package provider

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// histogramCount returns how many observations the endpoint's histogram holds.
func histogramCount(t *testing.T, endpoint string) uint64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	for _, family := range families {
		if family.GetName() != "musicbrainz_request_duration_seconds" {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() == "endpoint" && label.GetValue() == endpoint {
					return metric.GetHistogram().GetSampleCount()
				}
			}
		}
	}
	return 0
}

func TestRequestMetrics(t *testing.T) {
	errs := func(endpoint, reason string) float64 {
		return testutil.ToFloat64(errorsTotal.WithLabelValues(endpoint, reason))
	}
	retries := func(endpoint string) float64 { return testutil.ToFloat64(retriesTotal.WithLabelValues(endpoint)) }

	searchesBefore, lookupsBefore := histogramCount(t, endpointSearch), histogramCount(t, endpointArtist)
	err503Before, retriesBefore := errs(endpointSearch, "status_503"), retries(endpointSearch)
	artistErrBefore := errs(endpointArtist, "status_5xx")

	// One 503 that is retried successfully: two timed requests, one error, one retry.
	mb := &fakeMB{
		tagged:  searchBody(rec("rec-1", 100, "Queen", "rock:1")),
		artists: map[string]string{"queen-id": `{"tags":[{"name":"rock","count":1}]}`},
		fail:    []int{503},
	}
	client := newTestClient(t, mb.handler())
	if _, err := client.SearchTrack(context.Background(), "Queen", "Song"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// A lookup that works, one for an unknown artist (404 is an answer, not an
	// error) and one that fails with a status that is not retried.
	for _, id := range []string{"queen-id", "unknown-id"} {
		if _, err := client.ArtistTags(context.Background(), id); err != nil {
			t.Fatalf("ArtistTags(%q): %v", id, err)
		}
	}
	mb.mu.Lock()
	mb.fail = []int{500}
	mb.mu.Unlock()
	if _, err := client.ArtistTags(context.Background(), "queen-id"); err == nil {
		t.Fatal("expected an error for a 500")
	}

	if got := histogramCount(t, endpointSearch) - searchesBefore; got != 2 {
		t.Errorf("search duration observed %d times, want 2", got)
	}
	if got := histogramCount(t, endpointArtist) - lookupsBefore; got != 3 {
		t.Errorf("artist lookup duration observed %d times, want 3", got)
	}
	if got := errs(endpointSearch, "status_503") - err503Before; got != 1 {
		t.Errorf("musicbrainz_errors_total{status_503} grew by %v, want 1", got)
	}
	if got := retries(endpointSearch) - retriesBefore; got != 1 {
		t.Errorf("musicbrainz_retries_total grew by %v, want 1", got)
	}
	if got := errs(endpointArtist, "status_5xx") - artistErrBefore; got != 1 {
		t.Errorf("musicbrainz_errors_total{artist, status_5xx} grew by %v, want 1 (the 404 must not count)", got)
	}
}

func TestErrorReason(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{&UpstreamError{Status: 503}, "status_503"},
		{&UpstreamError{Status: 500}, "status_5xx"},
		{&UpstreamError{Status: 502}, "status_5xx"},
		{&UpstreamError{Status: 429}, "status_4xx"},
		{&UpstreamError{Status: 404}, "status_4xx"},
		{&UpstreamError{Err: errors.New("connection refused")}, "network"},
		{&UpstreamError{Err: context.DeadlineExceeded}, "timeout"},
		{fmt.Errorf("wrapped: %w", context.Canceled), "canceled"},
		{errors.New("musicbrainz: decode response: unexpected EOF"), "bad_response"},
	}
	for _, tt := range tests {
		if got := errorReason(tt.err); got != tt.want {
			t.Errorf("errorReason(%v) = %q, want %q", tt.err, got, tt.want)
		}
	}
}
