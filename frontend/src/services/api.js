const USER_SERVICE_URL = 'http://localhost:8002'
const MOOD_SERVICE_URL = 'http://localhost:8001'
const VIBE_SERVICE_URL = 'http://localhost:8080'
// Similar tracks and listening events go to the same Go API under /api/v1.
// The path is relative by default: in development Vite proxies /api to
// localhost:8080 (vite.config.js), so the browser needs no CORS. Set
// VITE_VIBE_API_URL to call another origin instead.
const VIBE_API_BASE = import.meta.env.VITE_VIBE_API_URL ?? ''

const TOKEN_KEY = 'jwtToken'

export function getToken() {
  return localStorage.getItem(TOKEN_KEY)
}

export function setToken(token) {
  localStorage.setItem(TOKEN_KEY, token)
}

export function clearToken() {
  localStorage.removeItem(TOKEN_KEY)
}

function authHeaders() {
  const token = getToken()
  return token ? { Authorization: `Bearer ${token}` } : {}
}

async function request(url, options = {}) {
  let response
  try {
    response = await fetch(url, options)
  } catch {
    throw new Error('Network error: service is unreachable')
  }

  const contentType = response.headers.get('content-type') || ''
  const body = contentType.includes('application/json') ? await response.json().catch(() => null) : null

  if (!response.ok) {
    const message = body?.error || body?.detail || `Request failed with status ${response.status}`
    throw new Error(message)
  }

  return body
}

function jsonHeaders(extra = {}) {
  return { 'Content-Type': 'application/json', ...extra }
}

// --- user-service (auth + history) ---

export function register(email, password) {
  return request(`${USER_SERVICE_URL}/register`, {
    method: 'POST',
    headers: jsonHeaders(),
    body: JSON.stringify({ email, password }),
  })
}

export function login(email, password) {
  return request(`${USER_SERVICE_URL}/login`, {
    method: 'POST',
    headers: jsonHeaders(),
    body: JSON.stringify({ email, password }),
  })
}

export function getMe() {
  return request(`${USER_SERVICE_URL}/me`, {
    method: 'GET',
    headers: jsonHeaders(authHeaders()),
  })
}

export function addHistory({ songA, songB, genreScore, moodScore }) {
  return request(`${USER_SERVICE_URL}/history`, {
    method: 'POST',
    headers: jsonHeaders(authHeaders()),
    body: JSON.stringify({
      song_a: songA,
      song_b: songB,
      genre_score: genreScore,
      mood_score: moodScore,
    }),
  })
}

export function getHistory() {
  return request(`${USER_SERVICE_URL}/history`, {
    method: 'GET',
    headers: jsonHeaders(authHeaders()),
  })
}

// --- mood-service ---

export function analyzeMood(artist, title) {
  return request(`${MOOD_SERVICE_URL}/analyze`, {
    method: 'POST',
    headers: jsonHeaders(),
    body: JSON.stringify({ artist, title }),
  })
}

// --- vibe-service: similar tracks and listening events ---

export function searchTracks(q, { limit = 8, signal } = {}) {
  const params = new URLSearchParams({ q, limit: String(limit) })
  return request(`${VIBE_API_BASE}/api/v1/tracks/search?${params}`, { signal })
}

// getSimilar returns { track, neighbors } and, when rerank is asked for,
// reranked (true/false) and rerank_skipped_reason when it was not applied.
export function getSimilar(trackId, { limit = 10, rerank = false, excludeSameArtist = false, signal } = {}) {
  const params = new URLSearchParams({ track_id: String(trackId), limit: String(limit) })
  if (rerank) params.set('rerank', 'true')
  if (excludeSameArtist) params.set('exclude_same_artist', 'true')
  return request(`${VIBE_API_BASE}/api/v1/similar?${params}`, { signal })
}

// getTrack returns { track_id, title, artist, genre, x, y } for one track.
// The API has no single-track endpoint, so it asks /api/v1/similar for one
// neighbour and keeps the source track from the answer.
export async function getTrack(trackId, { signal } = {}) {
  const body = await getSimilar(trackId, { limit: 1, signal })
  return body.track
}

// sendEvent posts one listening event and never throws: analytics must not
// break the page. keepalive lets the request finish while the tab closes.
export async function sendEvent(event, { keepalive = false } = {}) {
  if (!event) return false
  try {
    await request(`${VIBE_API_BASE}/api/v1/events`, {
      method: 'POST',
      headers: jsonHeaders(),
      body: JSON.stringify(event),
      keepalive,
    })
    return true
  } catch (err) {
    // 503 (events disabled), 429 (buffer full) and network errors end here.
    console.debug('listening event not sent:', err.message)
    return false
  }
}

// --- vibe-service (genre comparison) ---

export function compareGenres(songA, songB) {
  return request(`${VIBE_SERVICE_URL}/compare`, {
    method: 'POST',
    headers: jsonHeaders(),
    body: JSON.stringify({
      song_a: { artist: songA.artist, title: songA.title },
      song_b: { artist: songB.artist, title: songB.title },
    }),
  })
}
