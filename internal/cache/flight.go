package cache

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync"

	"song-similarity/internal/provider"
)

// flightGroup makes concurrent lookups of the same key share one execution,
// so a burst of identical cache misses costs a single upstream request.
type flightGroup struct {
	mu    sync.Mutex
	calls map[string]*flight

	// joined, if set, is called (with the group locked) each time a caller
	// attaches to a flight: leader is true for the caller that started it.
	joined func(leader bool)
	// onPanic, if set, is called when the shared function panics.
	onPanic func(key string, recovered any, stack []byte)
}

type flight struct {
	done    chan struct{}
	cancel  context.CancelFunc
	waiters int

	// track and err are written once, before done is closed, and never
	// modified afterwards. track is shared by all waiters and must not be
	// handed out directly: do returns a copy to each caller.
	track *provider.Track
	err   error
}

// do runs fn once per key at a time; callers that arrive while a run is in
// progress wait for its result instead of starting another.
//
// fn runs in its own goroutine under a context that is detached from any one
// caller: it carries no deadline and is cancelled only when every caller
// waiting on it has gone. A caller whose own ctx ends returns ctx.Err()
// straight away, without waiting for the run to finish.
//
// Each caller gets its own copy of the resulting track.
func (g *flightGroup) do(ctx context.Context, key string, fn func(context.Context) (*provider.Track, error)) (*provider.Track, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	g.mu.Lock()
	if g.calls == nil {
		g.calls = make(map[string]*flight)
	}
	f, running := g.calls[key]
	if !running {
		flightCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		f = &flight{done: make(chan struct{}), cancel: cancel}
		g.calls[key] = f
		go g.run(flightCtx, key, f, fn)
	}
	f.waiters++
	if g.joined != nil {
		g.joined(!running)
	}
	g.mu.Unlock()

	select {
	case <-f.done:
		if f.err != nil {
			return nil, f.err
		}
		return f.track.Clone(), nil
	case <-ctx.Done():
		g.leave(key, f)
		return nil, ctx.Err()
	}
}

// leave detaches one waiter. When the last one goes, the run is cancelled and
// the key is freed at once, so a later caller starts a fresh run rather than
// joining one that is being torn down.
func (g *flightGroup) leave(key string, f *flight) {
	g.mu.Lock()
	defer g.mu.Unlock()

	f.waiters--
	if f.waiters > 0 {
		return
	}
	if g.calls[key] == f {
		delete(g.calls, key)
	}
	f.cancel()
}

func (g *flightGroup) run(ctx context.Context, key string, f *flight, fn func(context.Context) (*provider.Track, error)) {
	defer func() {
		if r := recover(); r != nil {
			f.track, f.err = nil, fmt.Errorf("cache: lookup panicked: %v", r)
			if g.onPanic != nil {
				g.onPanic(key, r, debug.Stack())
			}
		}

		// Runs on success, error and panic alike, so a key never stays stuck.
		g.mu.Lock()
		if g.calls[key] == f {
			delete(g.calls, key)
		}
		g.mu.Unlock()

		f.cancel()
		close(f.done)
	}()

	f.track, f.err = fn(ctx)
}
