import { createContext, useCallback, useContext, useEffect, useState } from 'react'
import { clearToken, getMe, getToken, login as apiLogin, register as apiRegister, setToken } from '../services/api'

const AuthContext = createContext(null)

export function AuthProvider({ children }) {
  const [jwtToken, setJwtToken] = useState(getToken())
  const [userEmail, setUserEmail] = useState(null)
  const [isLoggedIn, setIsLoggedIn] = useState(Boolean(getToken()))
  const [authLoading, setAuthLoading] = useState(true)

  const loadProfile = useCallback(async () => {
    if (!getToken()) {
      setIsLoggedIn(false)
      setUserEmail(null)
      setAuthLoading(false)
      return
    }
    try {
      const me = await getMe()
      setUserEmail(me.email)
      setIsLoggedIn(true)
    } catch {
      clearToken()
      setJwtToken(null)
      setIsLoggedIn(false)
      setUserEmail(null)
    } finally {
      setAuthLoading(false)
    }
  }, [])

  useEffect(() => {
    loadProfile()
  }, [loadProfile])

  const login = useCallback(async (email, password) => {
    const { token } = await apiLogin(email, password)
    setToken(token)
    setJwtToken(token)
    setUserEmail(email)
    setIsLoggedIn(true)
  }, [])

  const register = useCallback(async (email, password) => {
    const { token } = await apiRegister(email, password)
    setToken(token)
    setJwtToken(token)
    setUserEmail(email)
    setIsLoggedIn(true)
  }, [])

  const logout = useCallback(() => {
    clearToken()
    setJwtToken(null)
    setUserEmail(null)
    setIsLoggedIn(false)
  }, [])

  const value = {
    jwtToken,
    userEmail,
    isLoggedIn,
    authLoading,
    login,
    register,
    logout,
  }

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}

export function useAuth() {
  const ctx = useContext(AuthContext)
  if (!ctx) {
    throw new Error('useAuth must be used within an AuthProvider')
  }
  return ctx
}
