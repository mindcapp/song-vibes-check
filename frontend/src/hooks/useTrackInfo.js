import { useEffect, useState } from 'react'
import { getTrack } from '../services/api'
import { createTrackCache } from '../map/trackCache'

// Shared by every component on the page: hovering back over a point, or a
// track already seen in search results or neighbours, needs no request.
const cache = createTrackCache(500)

export function rememberTrack(track) {
  if (track?.track_id) cache.set(track.track_id, track)
}

export function cachedTrack(trackId) {
  return cache.get(trackId)
}

const HOVER_DELAY_MS = 150

// useTrackInfo(trackId) returns { status: 'idle' | 'loading' | 'ok' | 'error',
// track } for the hovered track. A request starts only after the pointer has
// rested on the point for HOVER_DELAY_MS; moving on aborts it.
export function useTrackInfo(trackId) {
  const [state, setState] = useState({ status: 'idle', track: null })

  useEffect(() => {
    if (trackId == null) {
      setState({ status: 'idle', track: null })
      return undefined
    }
    const hit = cache.get(trackId)
    if (hit) {
      setState({ status: 'ok', track: hit })
      return undefined
    }
    setState({ status: 'loading', track: null })
    const controller = new AbortController()
    const timer = setTimeout(() => {
      getTrack(trackId, { signal: controller.signal })
        .then((track) => {
          cache.set(trackId, track)
          setState({ status: 'ok', track })
        })
        .catch(() => {
          if (!controller.signal.aborted) setState({ status: 'error', track: null })
        })
    }, HOVER_DELAY_MS)
    return () => {
      clearTimeout(timer)
      controller.abort()
    }
  }, [trackId])

  return state
}
