function ScoreBar({ label, value }) {
  const percent = Math.round(value * 100)
  return (
    <div>
      <div className="mb-1 flex items-center justify-between text-sm">
        <span className="font-medium text-slate-600 dark:text-slate-300">{label}</span>
        <span className="font-semibold text-slate-900 dark:text-white">{percent}%</span>
      </div>
      <div className="h-2 w-full overflow-hidden rounded-full bg-slate-100 dark:bg-slate-800">
        <div className="h-full rounded-full bg-indigo-500" style={{ width: `${percent}%` }} />
      </div>
    </div>
  )
}

function SongSummary({ label, song, mood, tags }) {
  return (
    <div className="rounded-lg border border-slate-200 p-4 dark:border-slate-800">
      <p className="text-xs font-semibold uppercase tracking-wide text-slate-400">{label}</p>
      <p className="mt-1 font-semibold text-slate-900 dark:text-white">
        {song.artist} — {song.title}
      </p>

      <div className="mt-3 space-y-2 text-sm">
        <div>
          <span className="text-slate-500 dark:text-slate-400">Genres: </span>
          {tags && tags.length > 0 ? (
            <span className="inline-flex flex-wrap gap-1 align-middle">
              {tags.map((tag) => (
                <span
                  key={tag}
                  className="rounded-full bg-indigo-50 px-2 py-0.5 text-xs font-medium text-indigo-700 dark:bg-indigo-950 dark:text-indigo-300"
                >
                  {tag}
                </span>
              ))}
            </span>
          ) : (
            <span className="text-slate-400">unknown</span>
          )}
        </div>
        <div>
          <span className="text-slate-500 dark:text-slate-400">Mood: </span>
          <span className="font-medium capitalize text-slate-800 dark:text-slate-200">{mood?.mood ?? 'unknown'}</span>
          {mood && (
            <span className="text-slate-400"> (energy {Math.round(mood.energy * 100)}%, confidence {Math.round(mood.confidence * 100)}%)</span>
          )}
        </div>
      </div>
    </div>
  )
}

export default function ResultCard({ result, onSave, saving, canSave }) {
  const { songA, songB, genre, mood } = result
  const overallScore = (genre.genre_score + mood.moodScore) / 2

  return (
    <div className="mt-8 rounded-xl border border-slate-200 bg-white p-6 shadow-sm dark:border-slate-800 dark:bg-slate-900">
      <h2 className="text-lg font-semibold text-slate-900 dark:text-white">Comparison Result</h2>

      <div className="mt-4 grid grid-cols-1 gap-4 sm:grid-cols-2">
        <SongSummary label="Song A" song={songA} mood={mood.songA} tags={genre.tags_a} />
        <SongSummary label="Song B" song={songB} mood={mood.songB} tags={genre.tags_b} />
      </div>

      <div className="mt-6 space-y-4">
        <ScoreBar label="Genre Score" value={genre.genre_score} />
        <ScoreBar label="Mood Score" value={mood.moodScore} />
        <div className="rounded-lg bg-indigo-600 px-4 py-3 text-white">
          <div className="flex items-center justify-between">
            <span className="text-sm font-medium">Overall Similarity</span>
            <span className="text-xl font-bold">{Math.round(overallScore * 100)}%</span>
          </div>
        </div>
      </div>

      {canSave && (
        <button
          onClick={onSave}
          disabled={saving}
          className="mt-6 rounded-md bg-slate-900 px-4 py-2 text-sm font-semibold text-white hover:bg-slate-700 disabled:cursor-not-allowed disabled:opacity-60 dark:bg-white dark:text-slate-900 dark:hover:bg-slate-200"
        >
          {saving ? 'Saving...' : 'Save to History'}
        </button>
      )}
    </div>
  )
}
