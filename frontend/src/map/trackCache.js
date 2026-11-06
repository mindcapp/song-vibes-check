// A small LRU cache of track metadata (title, artist, ...) by track_id, so
// hovering back and forth over the same points does not refetch them.
export function createTrackCache(limit = 500) {
  const map = new Map()
  return {
    get(id) {
      if (!map.has(id)) return undefined
      const value = map.get(id)
      map.delete(id) // move to the most recently used end
      map.set(id, value)
      return value
    },
    set(id, value) {
      map.delete(id)
      map.set(id, value)
      if (map.size > limit) {
        map.delete(map.keys().next().value)
      }
    },
    get size() {
      return map.size
    },
  }
}
