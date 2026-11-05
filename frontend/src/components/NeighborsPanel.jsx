const MODES = [
  { id: 'sound', label: 'By sound' },
  { id: 'listeners', label: 'By listener behaviour' },
]

function fmt(value, digits = 2) {
  return typeof value === 'number' ? value.toFixed(digits) : '—'
}

function ModeToggle({ mode, onChange }) {
  return (
    <div role="group" aria-label="Rank neighbours" className="inline-flex rounded-md border border-slate-300 p-0.5 dark:border-slate-700">
      {MODES.map((m) => (
        <button
          key={m.id}
          type="button"
          aria-pressed={mode === m.id}
          onClick={() => onChange(m.id)}
          className={`rounded px-3 py-1.5 text-sm font-medium ${
            mode === m.id
              ? 'bg-indigo-600 text-white'
              : 'text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800'
          }`}
        >
          {m.label}
        </button>
      ))}
    </div>
  )
}

// A reranked neighbour carries cosine, keep_rate and plays; a plain one (or a
// rerank that fell back to Qdrant's order) only has score = cosine.
function NeighborStats({ n }) {
  if (n.cosine === undefined) {
    return (
      <span className="text-sm tabular-nums text-slate-700 dark:text-slate-200" title="Cosine similarity of the audio features">
        {fmt(n.score)}
      </span>
    )
  }
  return (
    <div className="text-right">
      <span className="text-sm font-semibold tabular-nums text-slate-900 dark:text-white" title="(1 − w)·cosine + w·keep rate">
        {fmt(n.score)}
      </span>
      <div className="mt-0.5 flex gap-3 text-xs tabular-nums text-slate-500 dark:text-slate-400">
        <span title="Audio similarity">cosine {fmt(n.cosine)}</span>
        <span title={n.plays > 0 ? 'Share of plays not skipped, smoothed' : 'No plays yet: the average keep rate is used'}>
          keep {Math.round(n.keep_rate * 100)}%
        </span>
        <span title="Times listeners played it right after the selected track">plays {n.plays}</span>
      </div>
    </div>
  )
}

export default function NeighborsPanel({ track, result, mode, onModeChange, loading, error, onSelect, liked, onLike }) {
  const skipped = mode === 'listeners' && result?.reranked === false

  return (
    <section className="mt-6 rounded-lg border border-slate-200 bg-white p-5 shadow-sm dark:border-slate-800 dark:bg-slate-900">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div className="min-w-0">
          <p className="text-xs uppercase tracking-wide text-slate-500 dark:text-slate-400">Selected</p>
          <h2 className="truncate text-lg font-semibold text-slate-900 dark:text-white">{track.title}</h2>
          <p className="text-sm text-slate-500 dark:text-slate-400">
            {track.artist} · {track.genre}
          </p>
        </div>
        <button
          type="button"
          onClick={onLike}
          disabled={liked}
          aria-pressed={liked}
          className={`rounded-md px-3 py-1.5 text-sm font-medium ${
            liked
              ? 'bg-rose-100 text-rose-700 dark:bg-rose-950 dark:text-rose-300'
              : 'bg-slate-100 text-slate-700 hover:bg-slate-200 dark:bg-slate-800 dark:text-slate-200 dark:hover:bg-slate-700'
          }`}
        >
          {liked ? '♥ Liked' : '♡ Like'}
        </button>
      </div>

      <div className="mt-5 flex flex-wrap items-center justify-between gap-3">
        <h3 className="text-sm font-semibold text-slate-900 dark:text-white">Similar tracks</h3>
        <ModeToggle mode={mode} onChange={onModeChange} />
      </div>

      {skipped && (
        <div role="status" className="mt-3 rounded-md border border-amber-200 bg-amber-50 px-3 py-2 text-sm text-amber-800 dark:border-amber-900 dark:bg-amber-950 dark:text-amber-200">
          Listener ranking is not available right now, so tracks are ordered by sound.
          {result.rerank_skipped_reason && (
            <span className="block text-xs opacity-80">{result.rerank_skipped_reason}</span>
          )}
        </div>
      )}

      {error && (
        <div className="mt-3 rounded-md border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700 dark:border-red-900 dark:bg-red-950 dark:text-red-300">
          {error}
        </div>
      )}

      {loading && !result && <p className="mt-4 text-sm text-slate-500 dark:text-slate-400">Loading…</p>}

      {result && (
        <ol className={`mt-3 divide-y divide-slate-100 dark:divide-slate-800 ${loading ? 'opacity-50' : ''}`}>
          {result.neighbors.length === 0 && (
            <li className="py-3 text-sm text-slate-500 dark:text-slate-400">No similar tracks found</li>
          )}
          {result.neighbors.map((n, i) => (
            <li key={n.track_id}>
              <button
                type="button"
                onClick={() => onSelect(n)}
                className="flex w-full items-center justify-between gap-4 rounded px-2 py-2.5 text-left hover:bg-slate-50 dark:hover:bg-slate-800"
              >
                <span className="flex min-w-0 items-center gap-3">
                  <span className="w-5 shrink-0 text-right text-xs tabular-nums text-slate-400">{i + 1}</span>
                  <span className="min-w-0">
                    <span className="block truncate text-sm font-medium text-slate-900 dark:text-white">{n.title}</span>
                    <span className="block truncate text-xs text-slate-500 dark:text-slate-400">
                      {n.artist} · {n.genre}
                    </span>
                  </span>
                </span>
                <NeighborStats n={n} />
              </button>
            </li>
          ))}
        </ol>
      )}
    </section>
  )
}
