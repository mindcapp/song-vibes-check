import { useCallback, useEffect, useRef, useState } from 'react'
import { sendEvent } from '../services/api'
import { createListeningSession } from '../services/listening'
import { getSessionId } from '../services/session'

// useListeningEvents reports what the user listens to as POST /api/v1/events.
// select(trackId) is called whenever another track is picked; like() sends a
// "like" for the current one. Sending is fire-and-forget: failures are
// swallowed in sendEvent and never reach the UI.
export function useListeningEvents() {
  const sessionRef = useRef(null)
  if (sessionRef.current === null) {
    sessionRef.current = createListeningSession(getSessionId())
  }
  const [liked, setLiked] = useState(false)

  const select = useCallback((trackId) => {
    sendEvent(sessionRef.current.select(trackId))
    setLiked(Boolean(sessionRef.current.current?.liked))
  }, [])

  const like = useCallback(() => {
    const event = sessionRef.current.like()
    if (event) {
      setLiked(true)
      sendEvent(event)
    }
  }, [])

  useEffect(() => {
    // Closing the tab or navigating away ends the current track; keepalive
    // lets the request outlive the page.
    const onPageHide = () => sendEvent(sessionRef.current.finish(), { keepalive: true })
    window.addEventListener('pagehide', onPageHide)
    return () => {
      window.removeEventListener('pagehide', onPageHide)
      // Leaving the page inside the app (another route) ends it as well.
      sendEvent(sessionRef.current.finish())
    }
  }, [])

  return { select, like, liked }
}
