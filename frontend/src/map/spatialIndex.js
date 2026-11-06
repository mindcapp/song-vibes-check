// A uniform grid over the points' bounding box. nearest() only looks at the
// cells within the search radius, so a hover test touches a handful of points
// instead of all 8000.

export function createGridIndex(xs, ys, bounds, targetPerCell = 4) {
  const n = xs.length
  const width = Math.max(bounds.maxX - bounds.minX, 1e-9)
  const height = Math.max(bounds.maxY - bounds.minY, 1e-9)
  // cell side so that, on average, targetPerCell points share a cell
  const cell = n > 0 ? Math.sqrt((width * height * targetPerCell) / n) : Math.max(width, height)
  const cols = Math.max(1, Math.ceil(width / cell))
  const rows = Math.max(1, Math.ceil(height / cell))

  // Counting sort into one flat array: starts[c] .. starts[c + 1] are the
  // point indices of cell c.
  const cellOf = new Int32Array(n)
  const starts = new Int32Array(cols * rows + 1)
  for (let i = 0; i < n; i++) {
    const c = cellIndex(xs[i], ys[i])
    cellOf[i] = c
    starts[c + 1]++
  }
  for (let c = 0; c < cols * rows; c++) starts[c + 1] += starts[c]
  const fill = starts.slice(0, -1)
  const items = new Int32Array(n)
  for (let i = 0; i < n; i++) items[fill[cellOf[i]]++] = i

  function col(x) {
    return Math.min(cols - 1, Math.max(0, Math.floor((x - bounds.minX) / cell)))
  }
  function row(y) {
    return Math.min(rows - 1, Math.max(0, Math.floor((y - bounds.minY) / cell)))
  }
  function cellIndex(x, y) {
    return row(y) * cols + col(x)
  }

  return {
    cell,
    cols,
    rows,
    // nearest returns the index of the closest point within maxDist of
    // (x, y) for which accept(i) is true (all points if omitted), or -1.
    // visited counts the points examined, for tests and tuning.
    nearest(x, y, maxDist, accept) {
      let best = -1
      let bestD2 = maxDist * maxDist
      let visited = 0
      const c0 = col(x - maxDist)
      const c1 = col(x + maxDist)
      const r0 = row(y - maxDist)
      const r1 = row(y + maxDist)
      for (let r = r0; r <= r1; r++) {
        for (let c = c0; c <= c1; c++) {
          const cellId = r * cols + c
          for (let k = starts[cellId]; k < starts[cellId + 1]; k++) {
            const i = items[k]
            visited++
            const dx = xs[i] - x
            const dy = ys[i] - y
            const d2 = dx * dx + dy * dy
            if (d2 <= bestD2 && (!accept || accept(i))) {
              // ties go to the lower index so the result is deterministic
              if (d2 < bestD2 || best === -1 || i < best) {
                best = i
                bestD2 = d2
              }
            }
          }
        }
      }
      this.lastVisited = visited
      return best
    },
    lastVisited: 0,
  }
}
