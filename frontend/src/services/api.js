const USER_SERVICE_URL = 'http://localhost:8002'
const MOOD_SERVICE_URL = 'http://localhost:8001'
const VIBE_SERVICE_URL = 'http://localhost:8080'

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
