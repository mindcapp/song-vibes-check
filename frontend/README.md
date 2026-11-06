# Music Intelligence Platform — Frontend

React 18 + Vite + Tailwind CSS frontend for comparing two songs by genre and mood
and for exploring similar tracks.

## Requirements

- Node.js 20+ and npm
- The three backend services running locally:
  - `vibe-service` (Go, genre comparison) on `http://localhost:8080`
  - `mood-service` (Python/FastAPI, mood analysis) on `http://localhost:8001`
  - `user-service` (Go, auth + history) on `http://localhost:8002`

## Getting started

```bash
cd frontend
npm install
npm run dev
```

The app runs at `http://localhost:5173`.

The similar-tracks page calls the Go API through relative `/api/...` paths. In
development Vite forwards `/api/*` to `http://localhost:8080` (see `vite.config.js`),
so the browser needs no CORS. Start the API on the host with the features you want:

```bash
QDRANT_URL=http://127.0.0.1:6333 \
CLICKHOUSE_URL=http://127.0.0.1:8123 CLICKHOUSE_USER=vibe CLICKHOUSE_PASSWORD=vibe \
REDPANDA_BROKERS=localhost:9092 \
go run ./cmd/server
```

Without `CLICKHOUSE_URL` the "By listener behaviour" toggle falls back to the sound
order and says so; without `REDPANDA_BROKERS` events are silently dropped.

## Tests

```bash
npm test     # node --test, no extra dependencies: listening-session logic
```

## Build for production

```bash
npm run build
npm run preview
```

## Docker

The image serves the static build with nginx and does not proxy `/api`. Either put it
behind a reverse proxy that routes `/api/` to the Go API, or build with
`VITE_VIBE_API_URL=https://api.example.com` (the Go API then needs CORS headers, which it
does not send today).

```bash
docker build -t music-intelligence-frontend .
docker run -p 8080:80 music-intelligence-frontend
```

## Pages

- `/` — compare two songs (artist + title for each), see genre/mood results and an overall similarity score. Logged-in users can save the comparison to their history.
- `/login` — login or register (toggle), stores the JWT in `localStorage`.
- `/profile` — shows the current user's email and their 10 most recent history entries, with logout.
- `/history` — full history list (requires login).
- `/similar` — find a track (title or artist), see its nearest neighbours and click one to
  move to it. A toggle switches the order between **By sound** (`rerank=false`, audio
  similarity from Qdrant) and **By listener behaviour** (`rerank=true`): each neighbour then
  shows the combined score, `cosine`, keep rate and `plays`. If the API answers
  `reranked: false`, a soft notice shows `rerank_skipped_reason` and the list stays in sound
  order. **Like** marks the selected track.

### Listening events (`/similar`)

The page reports what the user listens to with `POST /api/v1/events`, fire-and-forget:
sending errors (events disabled, buffer full, network) are swallowed and never reach the UI.

- `session_id` is a random UUID created once per browser tab and kept in `sessionStorage`.
- A track counts as listened to while it is selected. When the user picks another track
  (from the search or the neighbour list), the event for the track being left is sent:
  `skip` if it was selected for less than 30 s, otherwise `complete`; `listen_ms` is the
  time it was selected; `prev_track_id` is the track selected before it (absent for the
  first track of the session).
- Closing the tab or leaving the page ends the current track the same way
  (`keepalive` request on `pagehide`).
- **Like** sends `event_type: like` for the selected track, once per selection.

The logic lives in `src/services/listening.js` (pure, tested by `npm test`);
`src/hooks/useListeningEvents.js` wires it into React.

## API contracts used

- `POST /register`, `POST /login` on `user-service` → `{ token }`
- `GET /me` on `user-service` (Bearer token) → `{ id, email, created_at }`
- `POST /history`, `GET /history` on `user-service` (Bearer token)
- `POST /analyze` on `mood-service` → `{ mood, confidence, energy }`
- `POST /compare` on `vibe-service` → `{ genre_score, tags_a, tags_b }`
- `GET /api/v1/tracks/search?q=&limit=` on the Go API → `{ tracks: [...] }`
- `GET /api/v1/similar?track_id=&limit=&rerank=` on the Go API → `{ track, neighbors, reranked?, rerank_skipped_reason? }`
- `POST /api/v1/events` on the Go API → `202 { event_id, status }`

All service base URLs are set in [src/services/api.js](src/services/api.js).
