import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import HistoryList from '../components/HistoryList'
import { getHistory } from '../services/api'
import { useAuth } from '../hooks/useAuth'
import { useToast } from '../hooks/useToast'

export default function Profile() {
  const { userEmail, logout } = useAuth()
  const { showToast } = useToast()
  const navigate = useNavigate()

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
        if (!cancelled) {
          setHistory((entries || []).slice(0, 10))
        }
      } catch (err) {
        if (!cancelled) {
          setError(err.message || 'Failed to load history')
        }
      } finally {
        if (!cancelled) setLoading(false)
      }
    }

    loadHistory()
    return () => {
      cancelled = true
    }
  }, [])

  const handleLogout = () => {
    logout()
    showToast('Logged out')
    navigate('/')
  }

  return (
    <div className="mx-auto max-w-3xl px-4 py-10 sm:px-6">
      <div className="mb-8 flex flex-col items-start justify-between gap-4 rounded-xl border border-slate-200 bg-white p-6 shadow-sm dark:border-slate-800 dark:bg-slate-900 sm:flex-row sm:items-center">
        <div>
          <p className="text-xs font-semibold uppercase tracking-wide text-slate-400">Signed in as</p>
          <p className="text-lg font-semibold text-slate-900 dark:text-white">{userEmail}</p>
        </div>
        <button
          onClick={handleLogout}
          className="rounded-md bg-slate-900 px-4 py-2 text-sm font-semibold text-white hover:bg-slate-700 dark:bg-white dark:text-slate-900 dark:hover:bg-slate-200"
        >
          Logout
        </button>
      </div>

      <h2 className="mb-4 text-lg font-semibold text-slate-900 dark:text-white">Recent Requests</h2>

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
