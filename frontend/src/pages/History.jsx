import { useEffect, useState } from 'react'
import HistoryList from '../components/HistoryList'
import { getHistory } from '../services/api'

export default function History() {
  const [history, setHistory] = useState([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')

  useEffect(() => {
    let cancelled = false

    async function loadHistory() {
      setLoading(true)
      setError('')
      try {
        const entries = await getHistory()
        if (!cancelled) setHistory(entries || [])
      } catch (err) {
        if (!cancelled) setError(err.message || 'Failed to load history')
      } finally {
        if (!cancelled) setLoading(false)
      }
    }

    loadHistory()
    return () => {
      cancelled = true
    }
  }, [])

  return (
    <div className="mx-auto max-w-4xl px-4 py-10 sm:px-6">
      <h1 className="mb-6 text-2xl font-bold text-slate-900 dark:text-white">Full History</h1>

      {loading && (
        <div className="flex justify-center py-8">
          <span className="h-8 w-8 animate-spin rounded-full border-2 border-indigo-500 border-t-transparent" aria-hidden="true" />
        </div>
      )}

      {error && !loading && (
        <div className="rounded-md border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-900 dark:bg-red-950 dark:text-red-300">
          {error}
        </div>
      )}

      {!loading && !error && <HistoryList entries={history} />}
    </div>
  )
}
