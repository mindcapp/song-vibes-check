import { useState } from 'react'

const emptyForm = { artistA: '', songA: '', artistB: '', songB: '' }

export default function CompareForm({ onSubmit, loading }) {
  const [form, setForm] = useState(emptyForm)

  const handleChange = (field) => (e) => {
    setForm((prev) => ({ ...prev, [field]: e.target.value }))
  }

  const handleSubmit = (e) => {
    e.preventDefault()
    onSubmit(form)
  }

  const inputClass =
    'w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm text-slate-900 placeholder-slate-400 focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500 dark:border-slate-700 dark:bg-slate-800 dark:text-white'

  return (
    <form onSubmit={handleSubmit} className="rounded-xl border border-slate-200 bg-white p-6 shadow-sm dark:border-slate-800 dark:bg-slate-900">
      <div className="grid grid-cols-1 gap-6 sm:grid-cols-2">
        <fieldset className="space-y-3">
          <legend className="mb-1 text-sm font-semibold text-slate-700 dark:text-slate-200">Song A</legend>
          <div>
            <label className="mb-1 block text-xs font-medium text-slate-500 dark:text-slate-400">Artist</label>
            <input
              className={inputClass}
              value={form.artistA}
              onChange={handleChange('artistA')}
              placeholder="e.g. Daft Punk"
              required
            />
          </div>
          <div>
            <label className="mb-1 block text-xs font-medium text-slate-500 dark:text-slate-400">Song</label>
            <input
              className={inputClass}
              value={form.songA}
              onChange={handleChange('songA')}
              placeholder="e.g. One More Time"
              required
            />
          </div>
        </fieldset>

        <fieldset className="space-y-3">
          <legend className="mb-1 text-sm font-semibold text-slate-700 dark:text-slate-200">Song B</legend>
          <div>
            <label className="mb-1 block text-xs font-medium text-slate-500 dark:text-slate-400">Artist</label>
            <input
              className={inputClass}
              value={form.artistB}
              onChange={handleChange('artistB')}
              placeholder="e.g. Justice"
              required
            />
          </div>
          <div>
            <label className="mb-1 block text-xs font-medium text-slate-500 dark:text-slate-400">Song</label>
            <input
              className={inputClass}
              value={form.songB}
              onChange={handleChange('songB')}
              placeholder="e.g. D.A.N.C.E."
              required
            />
          </div>
        </fieldset>
      </div>

      <button
        type="submit"
        disabled={loading}
        className="mt-6 flex w-full items-center justify-center gap-2 rounded-md bg-indigo-600 px-4 py-2.5 text-sm font-semibold text-white transition hover:bg-indigo-500 disabled:cursor-not-allowed disabled:opacity-60 sm:w-auto"
      >
        {loading && (
          <span className="h-4 w-4 animate-spin rounded-full border-2 border-white border-t-transparent" aria-hidden="true" />
        )}
        {loading ? 'Comparing...' : 'Compare Songs'}
      </button>
    </form>
  )
}
