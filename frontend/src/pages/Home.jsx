import { useState } from 'react'
import CompareForm from '../components/CompareForm'
import ResultCard from '../components/ResultCard'
import { addHistory, analyzeMood, compareGenres } from '../services/api'
import { useAuth } from '../hooks/useAuth'
import { useToast } from '../hooks/useToast'

export default function Home() {
  const { isLoggedIn } = useAuth()
  const { showToast } = useToast()

  const [loading, setLoading] = useState(false)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const [result, setResult] = useState(null)

  const handleCompare = async (form) => {
    setError('')
    setResult(null)
    setLoading(true)

    const songA = { artist: form.artistA.trim(), title: form.songA.trim() }
    const songB = { artist: form.artistB.trim(), title: form.songB.trim() }

    try {
      const [genre, moodA, moodB] = await Promise.all([
        compareGenres(songA, songB),
        analyzeMood(songA.artist, songA.title),
        analyzeMood(songB.artist, songB.title),
      ])

      const moodScore = (moodA.energy + moodB.energy) / 2

      setResult({
        songA,
        songB,
        genre,
        mood: { songA: moodA, songB: moodB, moodScore },
      })
    } catch (err) {
      setError(err.message || 'Something went wrong while comparing songs')
    } finally {
      setLoading(false)
    }
  }

  const handleSave = async () => {
    if (!result) return
    setSaving(true)
    try {
      await addHistory({
        songA: `${result.songA.artist} - ${result.songA.title}`,
        songB: `${result.songB.artist} - ${result.songB.title}`,
        genreScore: result.genre.genre_score,
        moodScore: result.mood.moodScore,
      })
      showToast('Saved to history')
    } catch (err) {
      showToast(err.message || 'Failed to save to history', 'error')
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="mx-auto max-w-3xl px-4 py-10 sm:px-6">
      <div className="mb-8 text-center">
        <h1 className="text-2xl font-bold text-slate-900 dark:text-white sm:text-3xl">Compare Two Songs</h1>
        <p className="mt-2 text-sm text-slate-500 dark:text-slate-400">
          See how similar two tracks are by genre and mood.
        </p>
      </div>

      <CompareForm onSubmit={handleCompare} loading={loading} />

      {error && (
        <div className="mt-4 rounded-md border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-900 dark:bg-red-950 dark:text-red-300">
          {error}
        </div>
      )}

      {result && <ResultCard result={result} onSave={handleSave} saving={saving} canSave={isLoggedIn} />}
    </div>
  )
}
