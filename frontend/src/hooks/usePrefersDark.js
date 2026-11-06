import { useEffect, useState } from 'react'

const QUERY = '(prefers-color-scheme: dark)'

// usePrefersDark follows the OS colour scheme, like Tailwind's darkMode: 'media'.
export function usePrefersDark() {
  const [dark, setDark] = useState(() => window.matchMedia?.(QUERY).matches ?? false)
  useEffect(() => {
    const mq = window.matchMedia?.(QUERY)
    if (!mq) return undefined
    const onChange = (e) => setDark(e.matches)
    mq.addEventListener('change', onChange)
    return () => mq.removeEventListener('change', onChange)
  }, [])
  return dark
}
