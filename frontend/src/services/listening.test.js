// Run with: npm test (node --test, no extra dependencies)
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { MAX_LISTEN_MS, SKIP_THRESHOLD_MS, createListeningSession, eventTypeFor } from './listening.js'
import { getSessionId } from './session.js'

function clock(start = 1_000_000) {
  let t = start
  return { now: () => t, advance: (ms) => (t += ms) }
}

test('the first selection sends nothing and has no prev_track_id', () => {
  const c = clock()
  const s = createListeningSession('sess-abcdef12', c.now)
  assert.equal(s.select(2), null)
  assert.equal(s.current.prevTrackId, null)
})

test('leaving a track quickly is a skip with the previous track as prev_track_id', () => {
  const c = clock()
  const s = createListeningSession('sess-abcdef12', c.now)
  s.select(2)
  c.advance(45_000)
  assert.deepEqual(s.select(5), {
    session_id: 'sess-abcdef12', track_id: 2, event_type: 'complete', listen_ms: 45_000,
  })
  c.advance(4_200)
  assert.deepEqual(s.select(10), {
    session_id: 'sess-abcdef12', track_id: 5, prev_track_id: 2, event_type: 'skip', listen_ms: 4_200,
  })
  assert.equal(s.current.prevTrackId, 5)
})

test('the threshold decides between skip and complete', () => {
  assert.equal(eventTypeFor(0), 'skip')
  assert.equal(eventTypeFor(SKIP_THRESHOLD_MS - 1), 'skip')
  assert.equal(eventTypeFor(SKIP_THRESHOLD_MS), 'complete')
})

test('re-selecting the same track sends nothing and keeps the timer', () => {
  const c = clock()
  const s = createListeningSession('sess-abcdef12', c.now)
  s.select(2)
  c.advance(10_000)
  assert.equal(s.select(2), null)
  c.advance(25_000)
  assert.equal(s.select(3).listen_ms, 35_000)
})

test('finish closes the track and the next selection still links to it', () => {
  const c = clock()
  const s = createListeningSession('sess-abcdef12', c.now)
  s.select(2)
  c.advance(1_000)
  assert.equal(s.finish().event_type, 'skip')
  assert.equal(s.finish(), null)
  assert.equal(s.current, null)
  // after the page comes back, a new track has no previous one (nothing was selected)
  assert.equal(s.select(7), null)
  assert.equal(s.current.prevTrackId, null)
})

test('like is sent once per selection with the current listen time', () => {
  const c = clock()
  const s = createListeningSession('sess-abcdef12', c.now)
  assert.equal(s.like(), null)
  s.select(2)
  s.select(5)
  c.advance(12_000)
  assert.deepEqual(s.like(), {
    session_id: 'sess-abcdef12', track_id: 5, prev_track_id: 2, event_type: 'like', listen_ms: 12_000,
  })
  assert.equal(s.like(), null)
  s.select(6)
  assert.equal(s.like().event_type, 'like')
})

test('listen_ms is clamped to what the API accepts', () => {
  const c = clock()
  const s = createListeningSession('sess-abcdef12', c.now)
  s.select(2)
  c.advance(MAX_LISTEN_MS * 3)
  assert.equal(s.select(3).listen_ms, MAX_LISTEN_MS)
  c.advance(-5_000) // a clock that went backwards
  assert.equal(s.select(4).listen_ms, 0)
})

test('the session id is created once and fits the API format', () => {
  const store = new Map()
  const storage = { getItem: (k) => store.get(k) ?? null, setItem: (k, v) => store.set(k, v) }
  const id = getSessionId(storage)
  assert.match(id, /^[A-Za-z0-9_-]{8,64}$/)
  assert.equal(getSessionId(storage), id)
})

test('a blocked sessionStorage falls back to one id per page', () => {
  const broken = { getItem: () => { throw new Error('blocked') }, setItem: () => { throw new Error('blocked') } }
  const id = getSessionId(broken)
  assert.match(id, /^[A-Za-z0-9_-]{8,64}$/)
  assert.equal(getSessionId(broken), id)
})
