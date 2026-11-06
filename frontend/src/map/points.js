import { genreIndex } from './genres.js'

// parsePoints turns points.json ([{ track_id, x, y, genre }]) into flat typed
// arrays for drawing and indexing. Rows with a bad id or coordinate are
// skipped and counted, so one broken row does not blank the map.
export function parsePoints(data) {
  if (!Array.isArray(data)) {
    throw new Error('points.json must be a JSON array')
  }
  const ids = []
  const xs = []
  const ys = []
  const genres = []
  let skipped = 0
  for (const row of data) {
    const ok =
      row &&
      Number.isInteger(row.track_id) &&
      row.track_id > 0 &&
      Number.isFinite(row.x) &&
      Number.isFinite(row.y)
    if (!ok) {
      skipped++
      continue
    }
    ids.push(row.track_id)
    xs.push(row.x)
    ys.push(row.y)
    genres.push(genreIndex(row.genre))
  }
  const byId = new Map()
  ids.forEach((id, i) => byId.set(id, i))
  return {
    count: ids.length,
    ids: Int32Array.from(ids),
    xs: Float64Array.from(xs),
    ys: Float64Array.from(ys),
    genres: Uint8Array.from(genres),
    byId,
    skipped,
  }
}
