import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import NeighborsPanel from '../components/NeighborsPanel'
import TrackSearch from '../components/TrackSearch'
import GenreLegend from '../components/map/GenreLegend'
import MapCanvas from '../components/map/MapCanvas'
import { useListeningEvents } from '../hooks/useListeningEvents'
import { usePrefersDark } from '../hooks/usePrefersDark'
import { cachedTrack, rememberTrack, useTrackInfo } from '../hooks/useTrackInfo'
import { GENRES } from '../map/genres'
import { parsePoints } from '../map/points'
import { createGridIndex } from '../map/spatialIndex'
import { computeBounds } from '../map/viewport'
import { getSimilar } from '../services/api'

const NEIGHBOURS = 5
const POINTS_URL = `${import.meta.env.BASE_URL}points.json`

function usePoints() {
  const [state, setState] = useState({ status: 'loading', points: null, error: '' })
  const [attempt, setAttempt] = useState(0)

  useEffect(() => {
    const controller = new AbortController()
    setState({ status: 'loading', points: null, error: '' })
    fetch(POINTS_URL, { signal: controller.signal })
      .then((res) => {
        if (!res.ok) throw new Error(`points.json: HTTP ${res.status}`)
        return res.json()
      })
      .then((data) => {
        const points = parsePoints(data)
        if (points.count === 0) throw new Error('points.json has no usable points')
        setState({ status: 'ready', points, error: '' })
      })
      .catch((err) => {
        if (!controller.signal.aborted) setState({ status: 'error', points: null, error: err.message })
      })
    return () => controller.abort()
  }, [attempt])

  return { ...state, retry: () => setAttempt((n) => n + 1) }
}

function ZoomButton({ label, onClick, children }) {
  return (
    <button
      type="button"
      aria-label={label}
      title={label}
      onClick={onClick}
      className="flex h-8 w-8 items-center justify-center rounded-md border border-slate-200 bg-white text-base font-semibold text-slate-700 shadow-sm hover:bg-slate-50 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-200 dark:hover:bg-slate-700"
    >
      {children}
    </button>
  )
}

export default function MapPage() {
  const { status, points, error: pointsError, retry } = usePoints()
  const dark = usePrefersDark()
  const mapRef = useRef(null)

  const bounds = useMemo(() => (points ? computeBounds(points.xs, points.ys) : null), [points])
  const index = useMemo(() => (points ? createGridIndex(points.xs, points.ys, bounds) : null), [points, bounds])
  const counts = useMemo(() => {
    const c = new Array(GENRES.length + 1).fill(0)
    if (points) for (let i = 0; i < points.count; i++) c[points.genres[i]]++
    return c
  }, [points])

  const [hidden, setHidden] = useState(() => new Set())
  const [focusGenre, setFocusGenre] = useState(null)
  const [hoverIdx, setHoverIdx] = useState(-1)
  const [highlightId, setHighlightId] = useState(null)

  const [selectedId, setSelectedId] = useState(null)
  const [mode, setMode] = useState('sound') // 'sound' -> rerank=false, 'listeners' -> rerank=true
  const [excludeSameArtist, setExcludeSameArtist] = useState(false)
  const [result, setResult] = useState(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const { select, like, liked } = useListeningEvents()

  const hoverId = points && hoverIdx >= 0 ? points.ids[hoverIdx] : null
  const hoverInfo = useTrackInfo(hoverId)

  useEffect(() => {
    if (selectedId == null) return undefined
    const controller = new AbortController()
    setLoading(true)
    setError('')
    getSimilar(selectedId, {
      limit: NEIGHBOURS,
      rerank: mode === 'listeners',
      excludeSameArtist,
      signal: controller.signal,
    })
      .then((body) => {
        rememberTrack(body.track)
        body.neighbors.forEach((n) => rememberTrack({ track_id: n.track_id, title: n.title, artist: n.artist, genre: n.genre }))
        setResult(body)
      })
      .catch((err) => {
        if (!controller.signal.aborted) {
          setResult(null)
          setError(err.message || 'Could not load similar tracks')
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false)
      })
    return () => controller.abort()
  }, [selectedId, mode, excludeSameArtist])

  // choose moves to another track: the events module reports the one being
  // left (skip/complete), and the map optionally centres on the new one.
  const choose = useCallback(
    (trackId, { center = false, track = null } = {}) => {
      if (!points) return
      if (track?.title) rememberTrack(track)
      const i = points.byId.get(trackId)
      if (i !== undefined && hidden.has(points.genres[i])) {
        setHidden((prev) => {
          const next = new Set(prev)
          next.delete(points.genres[i])
          return next
        })
      }
      if (center && i !== undefined) mapRef.current?.centerOn(i)
      if (trackId === selectedId) return
      select(trackId)
      setResult(null)
      setSelectedId(trackId)
    },
    [points, hidden, selectedId, select],
  )

  const selectedIdx = points && selectedId != null ? points.byId.get(selectedId) ?? -1 : -1
  const neighborIdx = useMemo(() => {
    if (!points || !result || result.track?.track_id !== selectedId) return []
    return result.neighbors.map((n) => points.byId.get(n.track_id)).filter((i) => i !== undefined)
  }, [points, result, selectedId])
  const highlightIdx = points && highlightId != null ? points.byId.get(highlightId) ?? -1 : -1

  const panelTrack = useMemo(() => {
    if (selectedId == null) return null
    if (result?.track?.track_id === selectedId) return result.track
    const known = cachedTrack(selectedId)
    if (known) return known
    const genre = selectedIdx >= 0 ? GENRES[points.genres[selectedIdx]]?.name ?? 'Other' : ''
    return { track_id: selectedId, title: 'Loading…', artist: `Track ${selectedId}`, genre }
  }, [selectedId, result, selectedIdx, points])

  const renderTooltip = (i) => {
    const genre = GENRES[points.genres[i]]?.name ?? 'Other'
    const sameTrack = hoverInfo.track && hoverInfo.track.track_id === points.ids[i]
    return (
      <>
        {sameTrack ? (
          <>
            <p className="font-medium text-slate-900 dark:text-white">{hoverInfo.track.title}</p>
            <p className="text-slate-600 dark:text-slate-300">{hoverInfo.track.artist}</p>
          </>
        ) : (
          <p className="text-slate-500 dark:text-slate-400">
            {hoverInfo.status === 'error' ? 'Details unavailable' : 'Loading…'}
          </p>
        )}
        <p className="mt-0.5 text-xs text-slate-500 dark:text-slate-400">{genre}</p>
      </>
    )
  }

  const toggleGenre = (g) =>
    setHidden((prev) => {
      const next = new Set(prev)
      if (next.has(g)) next.delete(g)
      else next.add(g)
      return next
    })
  const onlyGenre = (g) => setHidden(new Set(GENRES.map((_, i) => i).filter((i) => i !== g)))

  return (
    <div className="flex flex-col lg:h-[calc(100vh-4.0625rem)] lg:flex-row">
      <section aria-label="Track map" className="flex min-w-0 flex-1 flex-col">
        <div className="space-y-3 border-b border-slate-200 bg-white px-4 py-3 dark:border-slate-800 dark:bg-slate-900">
          <div className="max-w-md">
            <TrackSearch
              label="Find a track on the map"
              onSelect={(t) => choose(t.track_id, { center: true, track: t })}
            />
          </div>
          {points && (
            <GenreLegend
              counts={counts}
              hidden={hidden}
              dark={dark}
              onToggle={toggleGenre}
              onOnly={onlyGenre}
              onShowAll={() => setHidden(new Set())}
              onFocusGenre={setFocusGenre}
            />
          )}
        </div>

        <div className="relative h-[60vh] flex-1 bg-slate-50 dark:bg-slate-900 lg:h-auto">
          {status === 'loading' && (
            <p role="status" className="absolute inset-0 flex items-center justify-center text-sm text-slate-500 dark:text-slate-400">
              Loading the map…
            </p>
          )}
          {status === 'error' && (
            <div role="alert" className="absolute inset-0 flex flex-col items-center justify-center gap-3 p-6 text-center">
              <p className="text-sm text-red-700 dark:text-red-300">The map data could not be loaded ({pointsError}).</p>
              <button
                type="button"
                onClick={retry}
                className="rounded-md bg-indigo-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-indigo-500"
              >
                Try again
              </button>
            </div>
          )}
          {status === 'ready' && (
            <>
              <MapCanvas
                ref={mapRef}
                points={points}
                index={index}
                bounds={bounds}
                hidden={hidden}
                focusGenre={focusGenre}
                selected={selectedIdx}
                neighbors={neighborIdx}
                highlight={highlightIdx}
                dark={dark}
                onPick={(i) => choose(points.ids[i])}
                onHover={setHoverIdx}
                renderTooltip={renderTooltip}
              />
              <div className="absolute bottom-3 right-3 flex flex-col gap-1.5">
                <ZoomButton label="Zoom in" onClick={() => mapRef.current?.zoomBy(1.5)}>
                  +
                </ZoomButton>
                <ZoomButton label="Zoom out" onClick={() => mapRef.current?.zoomBy(1 / 1.5)}>
                  −
                </ZoomButton>
                <ZoomButton label="Show the whole map" onClick={() => mapRef.current?.reset()}>
                  ⤢
                </ZoomButton>
              </div>
            </>
          )}
        </div>
      </section>

      <aside
        aria-label="Selected track"
        className="border-t border-slate-200 p-4 dark:border-slate-800 lg:w-[26rem] lg:overflow-y-auto lg:border-l lg:border-t-0"
      >
        <p className="sr-only" aria-live="polite">
          {panelTrack && panelTrack.title !== 'Loading…' ? `Selected ${panelTrack.title} by ${panelTrack.artist}` : ''}
        </p>
        {panelTrack ? (
          <NeighborsPanel
            className=""
            track={panelTrack}
            result={result}
            mode={mode}
            onModeChange={setMode}
            loading={loading}
            error={error}
            onSelect={(n) => choose(n.track_id, { center: true, track: n })}
            liked={liked}
            onLike={like}
            excludeSameArtist={excludeSameArtist}
            onExcludeSameArtistChange={setExcludeSameArtist}
            onHoverNeighbor={setHighlightId}
          />
        ) : (
          <div className="rounded-lg border border-dashed border-slate-300 p-5 text-sm text-slate-500 dark:border-slate-700 dark:text-slate-400">
            <p className="font-medium text-slate-700 dark:text-slate-200">Explore the map</p>
            <p className="mt-1">
              Every dot is a track; nearby dots sound alike. Hover a dot to see what it is, click it (or search above) to
              see its {NEIGHBOURS} nearest neighbours.
            </p>
          </div>
        )}
      </aside>
    </div>
  )
}
