# Music Intelligence Platform — Frontend

React 18 + Vite + Tailwind CSS frontend for comparing two songs by genre and mood.

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

## Build for production

```bash
npm run build
npm run preview
```

## Docker

```bash
docker build -t music-intelligence-frontend .
docker run -p 8080:80 music-intelligence-frontend
```

## Pages

- `/` — compare two songs (artist + title for each), see genre/mood results and an overall similarity score. Logged-in users can save the comparison to their history.
- `/login` — login or register (toggle), stores the JWT in `localStorage`.
- `/profile` — shows the current user's email and their 10 most recent history entries, with logout.
- `/history` — full history list (requires login).

## API contracts used

- `POST /register`, `POST /login` on `user-service` → `{ token }`
- `GET /me` on `user-service` (Bearer token) → `{ id, email, created_at }`
- `POST /history`, `GET /history` on `user-service` (Bearer token)
- `POST /analyze` on `mood-service` → `{ mood, confidence, energy }`
- `POST /compare` on `vibe-service` → `{ genre_score, tags_a, tags_b }`

All service base URLs are set in [src/services/api.js](src/services/api.js).
