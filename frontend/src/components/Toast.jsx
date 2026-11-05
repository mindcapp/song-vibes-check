export default function Toast({ toast, onClose }) {
  if (!toast) return null

  const isError = toast.type === 'error'

  return (
    <div className="fixed bottom-4 right-4 z-50 w-full max-w-xs sm:max-w-sm">
      <div
        className={`flex items-start justify-between gap-3 rounded-lg border px-4 py-3 shadow-lg ${
          isError
            ? 'border-red-200 bg-red-50 text-red-800 dark:border-red-900 dark:bg-red-950 dark:text-red-200'
            : 'border-green-200 bg-green-50 text-green-800 dark:border-green-900 dark:bg-green-950 dark:text-green-200'
        }`}
        role="status"
      >
        <p className="text-sm font-medium">{toast.message}</p>
        <button
          onClick={onClose}
          className="text-sm font-semibold opacity-70 hover:opacity-100"
          aria-label="Dismiss notification"
        >
          ✕
        </button>
      </div>
    </div>
  )
}
