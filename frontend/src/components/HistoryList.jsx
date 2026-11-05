function formatDate(value) {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return date.toLocaleString()
}

export default function HistoryList({ entries }) {
  if (!entries || entries.length === 0) {
    return <p className="text-sm text-slate-500 dark:text-slate-400">No history yet. Compare some songs first!</p>
  }

  return (
    <div className="overflow-x-auto rounded-xl border border-slate-200 dark:border-slate-800">
      <table className="min-w-full divide-y divide-slate-200 text-sm dark:divide-slate-800">
        <thead className="bg-slate-50 dark:bg-slate-800/60">
          <tr>
            <th className="px-4 py-3 text-left font-semibold text-slate-600 dark:text-slate-300">Song A</th>
            <th className="px-4 py-3 text-left font-semibold text-slate-600 dark:text-slate-300">Song B</th>
            <th className="px-4 py-3 text-left font-semibold text-slate-600 dark:text-slate-300">Genre Score</th>
            <th className="px-4 py-3 text-left font-semibold text-slate-600 dark:text-slate-300">Mood Score</th>
            <th className="px-4 py-3 text-left font-semibold text-slate-600 dark:text-slate-300">Date</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
          {entries.map((entry) => (
            <tr key={entry.id} className="bg-white dark:bg-slate-900">
              <td className="px-4 py-3 text-slate-800 dark:text-slate-200">{entry.song_a}</td>
              <td className="px-4 py-3 text-slate-800 dark:text-slate-200">{entry.song_b}</td>
              <td className="px-4 py-3 text-slate-800 dark:text-slate-200">{Math.round(entry.genre_score * 100)}%</td>
              <td className="px-4 py-3 text-slate-800 dark:text-slate-200">{Math.round(entry.mood_score * 100)}%</td>
              <td className="px-4 py-3 text-slate-500 dark:text-slate-400">{formatDate(entry.created_at)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
