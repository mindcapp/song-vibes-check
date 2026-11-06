import { createContext, useCallback, useContext, useRef, useState } from 'react'
import Toast from '../components/Toast'

const ToastContext = createContext(null)

export function ToastProvider({ children }) {
  const [toast, setToast] = useState(null)
  const timeoutRef = useRef(null)

  const showToast = useCallback((message, type = 'success') => {
    clearTimeout(timeoutRef.current)
    setToast({ message, type })
    timeoutRef.current = setTimeout(() => setToast(null), 4000)
  }, [])

  const closeToast = useCallback(() => {
    clearTimeout(timeoutRef.current)
    setToast(null)
  }, [])

  return (
    <ToastContext.Provider value={{ showToast }}>
      {children}
      <Toast toast={toast} onClose={closeToast} />
    </ToastContext.Provider>
  )
}

export function useToast() {
  const ctx = useContext(ToastContext)
  if (!ctx) {
    throw new Error('useToast must be used within a ToastProvider')
  }
  return ctx
}
