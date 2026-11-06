import { Link, useNavigate } from 'react-router-dom'
import { useAuth } from '../hooks/useAuth'

export default function Header() {
  const { isLoggedIn, userEmail, logout } = useAuth()
  const navigate = useNavigate()

  const handleLogout = () => {
    logout()
    navigate('/')
  }

  return (
    <header className="border-b border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
      <div className="mx-auto flex max-w-5xl items-center justify-between px-4 py-4 sm:px-6">
        <Link to="/" className="text-lg font-semibold text-slate-900 dark:text-white">
          🎧 Music Intelligence
        </Link>

        <nav className="flex items-center gap-4 text-sm">
          <Link to="/" className="text-slate-600 hover:text-slate-900 dark:text-slate-300 dark:hover:text-white">
            Compare
          </Link>
          <Link to="/similar" className="text-slate-600 hover:text-slate-900 dark:text-slate-300 dark:hover:text-white">
            Similar
          </Link>

          {isLoggedIn ? (
            <>
              <Link
                to="/history"
                className="text-slate-600 hover:text-slate-900 dark:text-slate-300 dark:hover:text-white"
              >
                History
              </Link>
              <Link
                to="/profile"
                className="text-slate-600 hover:text-slate-900 dark:text-slate-300 dark:hover:text-white"
              >
                {userEmail || 'Profile'}
              </Link>
              <button
                onClick={handleLogout}
                className="rounded-md bg-slate-100 px-3 py-1.5 font-medium text-slate-700 hover:bg-slate-200 dark:bg-slate-800 dark:text-slate-200 dark:hover:bg-slate-700"
              >
                Logout
              </button>
            </>
          ) : (
            <Link
              to="/login"
              className="rounded-md bg-indigo-600 px-3 py-1.5 font-medium text-white hover:bg-indigo-500"
            >
              Login
            </Link>
          )}
        </nav>
      </div>
    </header>
  )
}
