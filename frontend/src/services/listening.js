// Turns "which track is selected and since when" into listening events for
// POST /api/v1/events. Pure logic, no React and no network, so it can be
// tested with `node --test`.
//
// A track counts as listened to for as long as it stays selected. When the
// user moves on to another track, the event for the track being left is
// built: "skip" if it was selected for less than SKIP_THRESHOLD_MS, otherwise
// "complete", with listen_ms = how long it was selected. prev_track_id is the
// track selected before it, so ClickHouse records the transition prev -> track.

// Same cut-off the lab generator uses: skips last 3-30 s, full listens minutes.
export const SKIP_THRESHOLD_MS = 30_000
// The API rejects listen_ms above one day.
export const MAX_LISTEN_MS = 24 * 60 * 60 * 1000

export function eventTypeFor(listenMs) {
  return listenMs < SKIP_THRESHOLD_MS ? 'skip' : 'complete'
}

function clampListenMs(ms) {
  return Math.min(Math.max(Math.round(ms), 0), MAX_LISTEN_MS)
}

function buildEvent(sessionId, current, eventType, listenMs) {
  const event = {
    session_id: sessionId,
    track_id: current.trackId,
    event_type: eventType,
    listen_ms: listenMs,
  }
  if (current.prevTrackId != null) {
    event.prev_track_id = current.prevTrackId
  }
  return event
}

export function createListeningSession(sessionId, now = () => Date.now()) {
  // { trackId, prevTrackId, since, liked } of the selected track, or null.
  let current = null

  return {
    // select records that trackId is now selected and returns the event for
    // the track that was selected before (or null). Re-selecting the current
    // track changes nothing.
    select(trackId) {
      if (current && current.trackId === trackId) {
        return null
      }
      const finished = this.finish()
      current = {
        trackId,
        prevTrackId: finished ? finished.track_id : null,
        since: now(),
        liked: false,
      }
      return finished
    },

    // finish closes the selected track (when the page is left) and returns
    // its event. Nothing is selected afterwards, so the next selection starts
    // without a prev_track_id, like the first track of a session.
    finish() {
      if (!current) {
        return null
      }
      const listenMs = clampListenMs(now() - current.since)
      const event = buildEvent(sessionId, current, eventTypeFor(listenMs), listenMs)
      current = null
      return event
    },

    // like returns a "like" event for the selected track, at most once per
    // selection, or null.
    like() {
      if (!current || current.liked) {
        return null
      }
      current.liked = true
      return buildEvent(sessionId, current, 'like', clampListenMs(now() - current.since))
    },

    get current() {
      return current ? { ...current } : null
    },
  }
}
