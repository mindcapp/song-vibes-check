import { useEffect, useId, useState } from 'react'
import { searchTracks } from '../services/api'

const MIN_QUERY = 2
const DEBOUNCE_MS = 250

// TrackSearch is a combobox over GET /api/v1/tracks/search. Arrow keys move
// through the results, Enter picks the highlighted one (or the first),
// Escape closes the list. Props: onSelect(track), label (optional).
export default function TrackSearch({ onSelect, label = 'Find a track' }) {
  const id = useId()
  const inputId = `${id}-input`
  const listId = `${id}-list`
  const optionId = (i) => `${id}-opt-${i}`

  const [query, setQuery] = useState('')
  const [results, setResults] = useState([])
  const [open, setOpen] = useState(false)
  const [active, setActive] = useState(-1)
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
        setActive(-1)
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
    setActive(-1)
    setQuery('')
    onSelect(track)
  }

  const showList = open && query.trim().length >= MIN_QUERY

  const onKeyDown = (e) => {
    switch (e.key) {
      case 'ArrowDown':
        e.preventDefault()
        if (!showList && results.length > 0) setOpen(true)
        setActive((i) => (results.length === 0 ? -1 : (i + 1) % results.length))
        break
      case 'ArrowUp':
        e.preventDefault()
        setActive((i) => (results.length === 0 ? -1 : (i <= 0 ? results.length : i) - 1))
        break
      case 'Enter':
        if (showList && results.length > 0) {
          e.preventDefault()
          choose(results[active >= 0 ? active : 0])
        }
        break
      case 'Escape':
        setOpen(false)
        setActive(-1)
        break
      default:
    }
  }

  return (
    <div className="relative">
      <label htmlFor={inputId} className="mb-1 block text-sm font-medium text-slate-700 dark:text-slate-300">
        {label}
      </label>
      <input
        id={inputId}
        type="search"
        role="combobox"
        aria-expanded={showList}
        aria-controls={listId}
        aria-autocomplete="list"
        aria-activedescendant={showList && active >= 0 ? optionId(active) : undefined}
        autoComplete="off"
        value={query}
        onChange={(e) => setQuery(e.target.value)}
        onFocus={() => results.length > 0 && setOpen(true)}
        onBlur={() => setOpen(false)}
        onKeyDown={onKeyDown}
        placeholder="Title or artist, at least 2 characters"
        className="w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm text-slate-900 shadow-sm focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
      />
      {error && <p className="mt-1 text-xs text-red-600 dark:text-red-400">{error}</p>}
      <p className="sr-only" aria-live="polite">
        {showList ? `${results.length} ${results.length === 1 ? 'track' : 'tracks'} found` : ''}
      </p>

      {showList && (
        <ul
          id={listId}
          role="listbox"
          aria-label="Search results"
          className="absolute z-20 mt-1 max-h-72 w-full overflow-auto rounded-md border border-slate-200 bg-white shadow-lg dark:border-slate-700 dark:bg-slate-800"
        >
          {results.length === 0 ? (
            <li className="px-3 py-2 text-sm text-slate-500 dark:text-slate-400" role="presentation">
              No tracks found
            </li>
          ) : (
            results.map((t, i) => (
              <li
                key={t.track_id}
                id={optionId(i)}
                role="option"
                aria-selected={i === active}
                // mousedown, not click: it fires before the input's blur closes the list
                onMouseDown={(e) => {
                  e.preventDefault()
                  choose(t)
                }}
                onMouseEnter={() => setActive(i)}
                className={`flex cursor-pointer items-center justify-between gap-3 px-3 py-2 text-sm ${
                  i === active ? 'bg-indigo-50 dark:bg-slate-700' : ''
                }`}
              >
                <span className="truncate text-slate-900 dark:text-white">
                  {t.title} <span className="text-slate-500 dark:text-slate-400">— {t.artist}</span>
                </span>
                <span className="shrink-0 text-xs text-slate-500 dark:text-slate-400">{t.genre}</span>
              </li>
            ))
          )}
        </ul>
      )}
    </div>
  )
}
