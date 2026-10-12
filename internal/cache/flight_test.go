package cache

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"song-similarity/internal/provider"
)

// gatedSearcher blocks every call until release is closed or the call's
// context ends, and reports both events on channels so tests can synchronise
// on them instead of sleeping.
type gatedSearcher struct {
	calls     atomic.Int32
	started   chan context.Context
	cancelled chan struct{}
	release   chan struct{}
	track     *provider.Track
	err       error
}

func newGatedSearcher(track *provider.Track, err error) *gatedSearcher {
	return &gatedSearcher{
		started:   make(chan context.Context, 64),
		cancelled: make(chan struct{}, 64),
		release:   make(chan struct{}),
		track:     track,
		err:       err,
	}
}

func (g *gatedSearcher) SearchTrack(ctx context.Context, _, _ string) (*provider.Track, error) {
	g.calls.Add(1)
	g.started <- ctx
	select {
	case <-g.release:
		return g.track, g.err
	case <-ctx.Done():
		g.cancelled <- struct{}{}
		return nil, ctx.Err()
	}
}

// joinSignal makes s report every caller that attaches to a flight. It must be
// installed before s is used concurrently.
func joinSignal(s *Searcher) <-chan bool {
	joined := make(chan bool, 256)
	count := s.flights.joined
	s.flights.joined = func(leader bool) {
		count(leader)
		joined <- leader
	}
	return joined
}

type lookupResult struct {
	track *provider.Track
	err   error
}

func lookupAsync(s *Searcher, ctx context.Context, title string) <-chan lookupResult {
	out := make(chan lookupResult, 1)
	go func() {
		track, err := s.SearchTrack(ctx, "Queen", title)
		out <- lookupResult{track, err}
	}()
	return out
}

func TestConcurrentMissesShareOneUpstreamCall(t *testing.T) {
	const callers = 25

	next := newGatedSearcher(taggedTrack(), nil)
	s := NewSearcher(next, newMemStore(), testConfig, quietLogger())
	joined := joinSignal(s)
	missesBefore, coalescedBefore := testutil.ToFloat64(missesTotal.WithLabelValues(kindTrack)), testutil.ToFloat64(coalescedTotal.WithLabelValues(kindTrack))

	var (
		wg      sync.WaitGroup
		barrier = make(chan struct{})
		results = make([]lookupResult, callers)
	)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-barrier
			track, err := s.SearchTrack(context.Background(), "Queen", "Bohemian Rhapsody")
			results[i] = lookupResult{track, err}
			if err != nil {
				return
			}
			// Every caller scribbles over its result. With a shared track this
			// is a data race and the checks below fail.
			track.Title = fmt.Sprintf("mine-%d", i)
			track.Tags[0] = fmt.Sprintf("tag-%d", i)
			track.RecordingTags[0].Count = i
			track.Tags = append(track.Tags, "extra")
		}(i)
	}

	close(barrier)
	for i := 0; i < callers; i++ {
		<-joined // all callers are attached to the flight before it finishes
	}
	close(next.release)
	wg.Wait()

	if calls := next.calls.Load(); calls != 1 {
		t.Errorf("upstream called %d times for %d concurrent misses, want 1", calls, callers)
	}
	for i, r := range results {
		if r.err != nil {
			t.Fatalf("caller %d: %v", i, r.err)
		}
		if r.track.Title != fmt.Sprintf("mine-%d", i) || r.track.Tags[0] != fmt.Sprintf("tag-%d", i) ||
			r.track.RecordingTags[0].Count != i || len(r.track.Tags) != 4 {
			t.Errorf("caller %d sees another caller's changes: %+v", i, r.track)
		}
	}

	if got := testutil.ToFloat64(missesTotal.WithLabelValues(kindTrack)) - missesBefore; got != 1 {
		t.Errorf("cache_misses_total grew by %v, want 1 (the leader only)", got)
	}
	if got := testutil.ToFloat64(coalescedTotal.WithLabelValues(kindTrack)) - coalescedBefore; got != callers-1 {
		t.Errorf("cache_coalesced_total grew by %v, want %d", got, callers-1)
	}

	// The cached copy was not touched by any of the callers either.
	cached, err := s.SearchTrack(context.Background(), "Queen", "Bohemian Rhapsody")
	want := taggedTrack()
	want.Cached = true
	if err != nil || cached.Title != want.Title || len(cached.Tags) != 3 || cached.Tags[0] != "rock" || cached.RecordingTags[0].Count != 7 {
		t.Errorf("cached track = %+v (err %v), want the original %+v", cached, err, want)
	}
}

func TestDifferentKeysDoNotShareAFlight(t *testing.T) {
	next := newGatedSearcher(taggedTrack(), nil)
	s := NewSearcher(next, newMemStore(), testConfig, quietLogger())

	var pending []<-chan lookupResult
	for _, title := range []string{"One", "Two", "Three"} {
		pending = append(pending, lookupAsync(s, context.Background(), title))
	}
	for range pending {
		<-next.started
	}
	close(next.release)
	for _, p := range pending {
		if r := <-p; r.err != nil {
			t.Fatalf("lookup failed: %v", r.err)
		}
	}

	if calls := next.calls.Load(); calls != 3 {
		t.Errorf("upstream called %d times for 3 different tracks, want 3", calls)
	}
}

func TestWaiterLeavesOnItsOwnContextWithoutWaitingForLeader(t *testing.T) {
	next := newGatedSearcher(taggedTrack(), nil)
	s := NewSearcher(next, newMemStore(), testConfig, quietLogger())
	joined := joinSignal(s)

	leader := lookupAsync(s, context.Background(), "Bohemian Rhapsody")
	<-joined

	waiterCtx, cancelWaiter := context.WithCancel(context.Background())
	waiter := lookupAsync(s, waiterCtx, "Bohemian Rhapsody")
	<-joined

	// The upstream call is still blocked: release has not been closed.
	cancelWaiter()
	if r := <-waiter; !errors.Is(r.err, context.Canceled) {
		t.Fatalf("waiter got %v, want context.Canceled", r.err)
	}

	select {
	case <-next.cancelled:
		t.Fatal("upstream lookup was cancelled although the leader still waits")
	default:
	}

	close(next.release)
	if r := <-leader; r.err != nil || r.track == nil {
		t.Fatalf("leader failed after a waiter left: track=%v err=%v", r.track, r.err)
	}
	if calls := next.calls.Load(); calls != 1 {
		t.Errorf("upstream called %d times, want 1", calls)
	}
}

func TestLeaderLeavingDoesNotFailOtherWaiters(t *testing.T) {
	next := newGatedSearcher(taggedTrack(), nil)
	store := newMemStore()
	s := NewSearcher(next, store, testConfig, quietLogger())
	joined := joinSignal(s)

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leader := lookupAsync(s, leaderCtx, "Bohemian Rhapsody")
	<-joined
	waiter := lookupAsync(s, context.Background(), "Bohemian Rhapsody")
	<-joined

	cancelLeader()
	if r := <-leader; !errors.Is(r.err, context.Canceled) {
		t.Errorf("leader got %v, want context.Canceled", r.err)
	}

	close(next.release)
	if r := <-waiter; r.err != nil || r.track == nil {
		t.Fatalf("waiter failed after the leader left: track=%v err=%v", r.track, r.err)
	}
	if calls := next.calls.Load(); calls != 1 {
		t.Errorf("upstream called %d times, want 1", calls)
	}
	if store.sets != 1 {
		t.Errorf("result stored %d times, want 1", store.sets)
	}
}

func TestLookupIsCancelledWhenLastWaiterLeaves(t *testing.T) {
	next := newGatedSearcher(taggedTrack(), nil)
	store := newMemStore()
	s := NewSearcher(next, store, testConfig, quietLogger())
	joined := joinSignal(s)

	ctx, cancel := context.WithCancel(context.Background())
	only := lookupAsync(s, ctx, "Bohemian Rhapsody")
	<-joined
	<-next.started

	cancel()
	if r := <-only; !errors.Is(r.err, context.Canceled) {
		t.Fatalf("caller got %v, want context.Canceled", r.err)
	}
	<-next.cancelled // the abandoned upstream lookup was told to stop

	// The key is free again: a new caller starts a fresh lookup.
	fresh := lookupAsync(s, context.Background(), "Bohemian Rhapsody")
	if leader := <-joined; !leader {
		t.Fatal("new caller joined the abandoned flight instead of starting one")
	}
	close(next.release)
	if r := <-fresh; r.err != nil || r.track == nil {
		t.Fatalf("fresh lookup failed: track=%v err=%v", r.track, r.err)
	}
	if calls := next.calls.Load(); calls != 2 {
		t.Errorf("upstream called %d times, want 2", calls)
	}
	if store.sets != 1 {
		t.Errorf("result stored %d times, want 1 (the abandoned lookup must not store)", store.sets)
	}
}

func TestAlreadyCancelledCallerStartsNothing(t *testing.T) {
	next := &countingSearcher{track: taggedTrack()}
	s := NewSearcher(next, &brokenStore{getErr: errStoreDown}, testConfig, quietLogger())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := s.SearchTrack(ctx, "Queen", "Bohemian Rhapsody"); !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
	if next.calls != 0 {
		t.Errorf("upstream called %d times for an already cancelled request", next.calls)
	}
}

// scriptedSearcher returns one scripted outcome per call, in order.
type scriptedSearcher struct {
	mu    sync.Mutex
	steps []func() (*provider.Track, error)
	calls int
}

func (s *scriptedSearcher) SearchTrack(context.Context, string, string) (*provider.Track, error) {
	s.mu.Lock()
	step := s.steps[s.calls]
	s.calls++
	s.mu.Unlock()
	return step()
}

func TestKeyIsFreedAfterError(t *testing.T) {
	next := &scriptedSearcher{steps: []func() (*provider.Track, error){
		func() (*provider.Track, error) { return nil, provider.ErrUpstream },
		func() (*provider.Track, error) { return taggedTrack(), nil },
	}}
	s := NewSearcher(next, newMemStore(), testConfig, quietLogger())

	if _, err := s.SearchTrack(context.Background(), "Queen", "Bohemian Rhapsody"); !errors.Is(err, provider.ErrUpstream) {
		t.Fatalf("first lookup: error = %v, want ErrUpstream", err)
	}
	if n := len(s.flights.calls); n != 0 {
		t.Fatalf("%d flights left in the map after an error", n)
	}

	track, err := s.SearchTrack(context.Background(), "Queen", "Bohemian Rhapsody")
	if err != nil || track == nil {
		t.Fatalf("second lookup: track=%v err=%v, want a fresh successful lookup", track, err)
	}
	if next.calls != 2 {
		t.Errorf("upstream called %d times, want 2", next.calls)
	}
}

func TestPanicIsLoggedWithStackAndKeyIsFreed(t *testing.T) {
	next := &scriptedSearcher{steps: []func() (*provider.Track, error){
		func() (*provider.Track, error) { panic("boom") },
		func() (*provider.Track, error) { return taggedTrack(), nil },
	}}
	var logs strings.Builder
	s := NewSearcher(next, newMemStore(), testConfig, slog.New(slog.NewJSONHandler(&logs, nil)))

	_, err := s.SearchTrack(context.Background(), "Queen", "Bohemian Rhapsody")
	if err == nil || !strings.Contains(err.Error(), "panicked") {
		t.Fatalf("error = %v, want a \"panicked\" error", err)
	}
	if n := len(s.flights.calls); n != 0 {
		t.Fatalf("%d flights left in the map after a panic", n)
	}

	out := logs.String()
	for _, want := range []string{`"level":"ERROR"`, `"panic":"boom"`, `"stack":"goroutine `, "flight_test.go"} {
		if !strings.Contains(out, want) {
			t.Errorf("panic log is missing %q:\n%s", want, out)
		}
	}

	track, err := s.SearchTrack(context.Background(), "Queen", "Bohemian Rhapsody")
	if err != nil || track == nil {
		t.Fatalf("lookup after a panic: track=%v err=%v", track, err)
	}
}

func TestSharedLookupCarriesNoDeadline(t *testing.T) {
	next := newGatedSearcher(taggedTrack(), nil)
	s := NewSearcher(next, newMemStore(), testConfig, quietLogger())

	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()

	pending := lookupAsync(s, ctx, "Bohemian Rhapsody")
	upstreamCtx := <-next.started
	if deadline, ok := upstreamCtx.Deadline(); ok {
		t.Errorf("upstream lookup inherited a deadline (%v); a rate-limit queue would be cut off by it", deadline)
	}
	close(next.release)
	<-pending
}

// queueingSearcher serves one call at a time, like the rate-limited client.
type queueingSearcher struct {
	mu    sync.Mutex
	calls int
}

func (q *queueingSearcher) SearchTrack(ctx context.Context, _, _ string) (*provider.Track, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	q.calls++
	return taggedTrack(), nil
}

func TestQueueOfDifferentKeysAllComplete(t *testing.T) {
	const keys = 60

	next := &queueingSearcher{}
	s := NewSearcher(next, newMemStore(), testConfig, quietLogger())

	// Hold the queue until every lookup is waiting in it.
	next.mu.Lock()
	joined := joinSignal(s)
	var pending []<-chan lookupResult
	for i := 0; i < keys; i++ {
		pending = append(pending, lookupAsync(s, context.Background(), fmt.Sprintf("Song %d", i)))
	}
	for i := 0; i < keys; i++ {
		<-joined
	}
	next.mu.Unlock()

	for i, p := range pending {
		if r := <-p; r.err != nil {
			t.Fatalf("lookup %d of %d failed while queued: %v", i, keys, r.err)
		}
	}
	if next.calls != keys {
		t.Errorf("upstream called %d times, want %d", next.calls, keys)
	}
}
