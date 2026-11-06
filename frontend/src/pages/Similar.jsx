import { useEffect, useState } from 'react'
import NeighborsPanel from '../components/NeighborsPanel'
import TrackSearch from '../components/TrackSearch'
import { useListeningEvents } from '../hooks/useListeningEvents'
import { getSimilar } from '../services/api'

const NEIGHBOURS = 10

export default function Similar() {
  const [track, setTrack] = useState(null)
  const [mode, setMode] = useState('sound') // 'sound' -> rerank=false, 'listeners' -> rerank=true
  const [result, setResult] = useState(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const { select, like, liked } = useListeningEvents()

  useEffect(() => {
    if (!track) return undefined
    const controller = new AbortController()
    setLoading(true)
    setError('')
    getSimilar(track.track_id, { limit: NEIGHBOURS, rerank: mode === 'listeners', signal: controller.signal })
      .then((body) => setResult(body))
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
  }, [track, mode])

  // Picking a track from the search or a neighbour from the list is a move
  // from the current track to the new one.
  const choose = (t) => {
    if (track?.track_id === t.track_id) return
    select(t.track_id)
    setTrack({ track_id: t.track_id, title: t.title, artist: t.artist, genre: t.genre })
  }

  return (
    <div className="mx-auto max-w-3xl px-4 py-10 sm:px-6">
      <div className="mb-8 text-center">
        <h1 className="text-2xl font-bold text-slate-900 dark:text-white sm:text-3xl">Similar Tracks</h1>
        <p className="mt-2 text-sm text-slate-500 dark:text-slate-400">
          Pick a track and explore what sounds like it, or what listeners keep playing after it.
        </p>
      </div>

      <TrackSearch onSelect={choose} />

      {track ? (
        <NeighborsPanel
          track={track}
          result={result}
          mode={mode}
          onModeChange={setMode}
          loading={loading}
          error={error}
          onSelect={choose}
          liked={liked}
          onLike={like}
        />
      ) : (
        <p className="mt-6 text-center text-sm text-slate-500 dark:text-slate-400">Start by searching for a track.</p>
      )}
    </div>
  )
}
