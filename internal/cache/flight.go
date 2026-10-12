package cache

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync"
)

// flightGroup makes concurrent lookups of the same key share one execution,
// so a burst of identical cache misses costs a single upstream request.
type flightGroup[T any] struct {
	mu    sync.Mutex
	calls map[string]*flight[T]

	// clone copies a result for one caller. The value produced by a run is
	// shared by all of its waiters and is never handed out directly.
	clone func(T) T
	// joined, if set, is called (with the group locked) each time a caller
	// attaches to a flight: leader is true for the caller that started it.
	joined func(leader bool)
	// onPanic, if set, is called when the shared function panics.
	onPanic func(key string, recovered any, stack []byte)
}

type flight[T any] struct {
	done    chan struct{}
	cancel  context.CancelFunc
	waiters int

	// value and err are written once, before done is closed, and never
	// modified afterwards.
	value T
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
// Each caller gets its own copy of the result.
func (g *flightGroup[T]) do(ctx context.Context, key string, fn func(context.Context) (T, error)) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}

	g.mu.Lock()
	if g.calls == nil {
		g.calls = make(map[string]*flight[T])
	}
	f, running := g.calls[key]
	if !running {
		flightCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		f = &flight[T]{done: make(chan struct{}), cancel: cancel}
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
			return zero, f.err
		}
		return g.clone(f.value), nil
	case <-ctx.Done():
		g.leave(key, f)
		return zero, ctx.Err()
	}
}

// leave detaches one waiter. When the last one goes, the run is cancelled and
// the key is freed at once, so a later caller starts a fresh run rather than
// joining one that is being torn down.
func (g *flightGroup[T]) leave(key string, f *flight[T]) {
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

func (g *flightGroup[T]) run(ctx context.Context, key string, f *flight[T], fn func(context.Context) (T, error)) {
	defer func() {
		if r := recover(); r != nil {
			var zero T
			f.value, f.err = zero, fmt.Errorf("cache: lookup panicked: %v", r)
			if g.onPanic != nil {
				g.onPanic(key, fmt.Sprint(r), debug.Stack())
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

	f.value, f.err = fn(ctx)
}
