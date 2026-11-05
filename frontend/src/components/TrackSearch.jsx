import { useEffect, useState } from 'react'
import { searchTracks } from '../services/api'

const MIN_QUERY = 2
const DEBOUNCE_MS = 250

export default function TrackSearch({ onSelect }) {
  const [query, setQuery] = useState('')
  const [results, setResults] = useState([])
  const [open, setOpen] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    const q = query.trim()
    if (q.length < MIN_QUERY) {
      setResults([])
      setError('')
      return undefined
    }
    const controller = new AbortController()
    const timer = setTimeout(async () => {
      try {
        const body = await searchTracks(q, { signal: controller.signal })
        setResults(body.tracks)
        setError('')
        setOpen(true)
      } catch (err) {
        if (!controller.signal.aborted) {
          setResults([])
          setError(err.message)
        }
      }
    }, DEBOUNCE_MS)
    return () => {
      clearTimeout(timer)
      controller.abort()
    }
  }, [query])

  const choose = (track) => {
    setOpen(false)
    setQuery('')
    onSelect(track)
  }

  return (
    <div className="relative">
      <label htmlFor="track-search" className="mb-1 block text-sm font-medium text-slate-700 dark:text-slate-300">
        Find a track
      </label>
      <input
        id="track-search"
        type="search"
        autoComplete="off"
        value={query}
        onChange={(e) => setQuery(e.target.value)}
        onFocus={() => results.length > 0 && setOpen(true)}
        onKeyDown={(e) => {
          if (e.key === 'Escape') setOpen(false)
          if (e.key === 'Enter' && results.length > 0) choose(results[0])
        }}
        placeholder="Title or artist, at least 2 characters"
        className="w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm text-slate-900 shadow-sm focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
      />
      {error && <p className="mt-1 text-xs text-red-600 dark:text-red-400">{error}</p>}

      {open && query.trim().length >= MIN_QUERY && (
        <ul className="absolute z-10 mt-1 max-h-72 w-full overflow-auto rounded-md border border-slate-200 bg-white shadow-lg dark:border-slate-700 dark:bg-slate-800">
          {results.length === 0 ? (
            <li className="px-3 py-2 text-sm text-slate-500 dark:text-slate-400">No tracks found</li>
          ) : (
            results.map((t) => (
              <li key={t.track_id}>
                <button
                  type="button"
                  onClick={() => choose(t)}
                  className="flex w-full items-center justify-between gap-3 px-3 py-2 text-left text-sm hover:bg-slate-50 dark:hover:bg-slate-700"
                >
                  <span className="truncate text-slate-900 dark:text-white">
                    {t.title} <span className="text-slate-500 dark:text-slate-400">— {t.artist}</span>
                  </span>
                  <span className="shrink-0 text-xs text-slate-500 dark:text-slate-400">{t.genre}</span>
                </button>
              </li>
            ))
          )}
        </ul>
      )}
    </div>
  )
}
