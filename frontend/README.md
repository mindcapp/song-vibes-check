# Music Intelligence Platform — Frontend

React 18 + Vite + Tailwind CSS frontend: a map of 8 000 tracks placed by sound,
similar-track search, and comparing two songs by genre and mood.

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

The map and the similar-tracks page call the Go API through relative `/api/...`
paths. In development Vite forwards `/api/*` to `http://localhost:8080` (see
`vite.config.js`), so the browser needs no CORS.

### Running with the API, Qdrant and ClickHouse

From the repository root, in separate terminals:

```bash
# 1. Qdrant with the tracks collection (load it once: see pipeline/README.md)
cd pipeline && docker compose up -d && cd ..

# 2. optional: Redpanda + ClickHouse for events and the listener ranking
cd lab-events && docker compose up -d && cd ..

# 3. the Go API on the host
QDRANT_URL=http://127.0.0.1:6333 \
CLICKHOUSE_URL=http://127.0.0.1:8123 CLICKHOUSE_USER=vibe CLICKHOUSE_PASSWORD=vibe \
REDPANDA_BROKERS=localhost:9092 \
go run ./cmd/server

# 4. the frontend
cd frontend && npm install && npm run dev     # http://localhost:5173
```

What each part enables:

| Running | Without it |
| --- | --- |
| `public/points.json` (in the repo) | the map shows an error with a retry button |
| Qdrant + `QDRANT_URL` | search, neighbours and tooltip details fail (the API answers 503); dots, colours and the legend still work |
| ClickHouse + `CLICKHOUSE_URL` | "By listener behaviour" falls back to the sound order with a notice |
| Redpanda + `REDPANDA_BROKERS` | listening events are dropped silently |

`points.json` is the `pipeline/output/points.json` of the same pipeline run that filled
Qdrant. If the collection is rebuilt, copy the new file to `public/`, or ids and
positions will no longer match.

## Tests

```bash
npm test       # node --test, no extra dependencies
npm run lint   # ESLint over .js and .jsx
```

The tests cover the pure logic: the listening session (`src/services/listening.js`), and
for the map the world/screen transforms and zoom, the grid index (checked against brute
force on 8 000 points), `points.json` parsing, the palette and the track cache
(`src/map/*.js`). Components are not tested; there is no DOM test setup.

`react/prop-types` is turned off in `.eslintrc.cjs`: the project is plain JavaScript
without the PropTypes runtime, and components describe their props in comments.

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

- `/` — the map (below).
- `/compare` — compare two songs (artist + title for each), see genre/mood results and an overall similarity score. Logged-in users can save the comparison to their history.
- `/login` — login or register (toggle), stores the JWT in `localStorage`.
- `/profile` — shows the current user's email and their 10 most recent history entries, with logout.
- `/history` — full history list (requires login).
- `/similar` — find a track (title or artist), see its nearest neighbours and click one to
  move to it. A toggle switches the order between **By sound** (`rerank=false`, audio
  similarity from Qdrant) and **By listener behaviour** (`rerank=true`): each neighbour then
  shows the combined score, `cosine`, keep rate and `plays`. If the API answers
  `reranked: false`, a soft notice shows `rerank_skipped_reason` and the list stays in sound
  order. **Like** marks the selected track.

### Map (`/`)

Every dot is one of the 8 000 tracks, placed by the 2D UMAP projection of its audio
features (`public/points.json`) and coloured by genre.

- **Move and zoom**: drag; mouse wheel or pinch to zoom around the pointer; the
  `+` / `−` / `⤢` buttons; with the map focused, arrow keys, `+`, `-` and `0` (reset).
- **Hover** a dot (a 24 px target around it) to see title, artist and genre. Details
  are fetched only after the pointer rests for 150 ms, cached (500 tracks) and the
  previous request is aborted when the pointer moves on.
- **Click** a dot, or pick a search result, to select it: lines go to its 5 nearest
  neighbours, and the side panel (under the map on narrow screens) lists them with the
  same **By sound / By listener behaviour** toggle as `/similar`, an **Other artists
  only** option (`exclude_same_artist`) and **Like**. Search results and neighbours
  clicked in the panel also centre the map on the track.
- **Legend**: click a genre to hide or show it, **only** to isolate it, hover to dim the
  others.

Selecting tracks on the map sends the same listening events as `/similar` (below),
through the same `useListeningEvents` hook.

Implementation: one `<canvas>`, sized for `devicePixelRatio`, redrawn on
`requestAnimationFrame` only when something changes; points are batched into one path
per genre and culled outside the view. Hover and click use a uniform grid over the
points (`src/map/spatialIndex.js`), so a test looks at a few dozen points instead of
all 8 000.

### Listening events (`/similar` and the map)

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

## Known limitations

- **Map distances are approximate.** UMAP squeezes 64 dimensions into 2, preserving
  local neighbourhoods better than global distances. The neighbours come from Qdrant in
  the full 64-dimensional space, so a neighbour can sit visibly far away on the map, and
  two clusters far apart on the map are not necessarily very different.
- **Eight genre colours are not all distinguishable for everyone.** The palette is the
  validated reference palette for light and dark backgrounds, and adjacent legend
  entries pass the colour-vision checks, but with eight colours in a scatter some pairs
  (orange/red, green/orange for protanopia) are close. Identity never relies on colour
  alone: isolate a genre in the legend, and the tooltip and panel name the genre.
- **Tooltip details cost a neighbour search.** The API has no single-track endpoint, so
  a tooltip asks `/api/v1/similar?track_id=…&limit=1` (two Qdrant calls). The delay and
  the cache keep this to one request per new track hovered.
- **The canvas is not readable by screen readers.** Keyboard and screen-reader users
  reach tracks through the search field and the side panel; the canvas itself only
  supports keyboard panning and zooming.
- **Dark mode follows the OS** (`prefers-color-scheme`), like the rest of the app.
- **No CORS on the API**: outside `npm run dev`, `/api` must be routed to the Go API by
  a reverse proxy (see Docker above).
- `/` used to be the compare page; login and logout still redirect to `/`, which is
  now the map.

## API contracts used

- `POST /register`, `POST /login` on `user-service` → `{ token }`
- `GET /me` on `user-service` (Bearer token) → `{ id, email, created_at }`
- `POST /history`, `GET /history` on `user-service` (Bearer token)
- `POST /analyze` on `mood-service` → `{ mood, confidence, energy }`
- `POST /compare` on `vibe-service` → `{ genre_score, tags_a, tags_b }`
- `GET /api/v1/tracks/search?q=&limit=` on the Go API → `{ tracks: [...] }`
- `GET /api/v1/similar?track_id=&limit=&rerank=` on the Go API → `{ track, neighbors, reranked?, rerank_skipped_reason? }`
- `POST /api/v1/events` on the Go API → `202 { event_id, status }`
- `GET /points.json` (static, `public/`) → `[{ track_id, x, y, genre }]`

All service base URLs are set in [src/services/api.js](src/services/api.js).
