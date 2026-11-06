// Run with: npm test
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { GENRES, genreColor, genreIndex } from './genres.js'
import { parsePoints } from './points.js'
import { createGridIndex } from './spatialIndex.js'
import { createTrackCache } from './trackCache.js'
import {
  centerOn,
  computeBounds,
  fitView,
  panBy,
  screenToWorld,
  visibleWorldRect,
  worldToScreen,
  zoomAt,
} from './viewport.js'

const close = (a, b, eps = 1e-9) => assert.ok(Math.abs(a - b) < eps, `${a} != ${b}`)

test('world <-> screen round trip, with y pointing up', () => {
  const view = { scale: 50, tx: 100, ty: 400 }
  const s = worldToScreen(view, 2, 3)
  assert.deepEqual(s, { x: 200, y: 250 })
  const w = screenToWorld(view, s.x, s.y)
  close(w.x, 2)
  close(w.y, 3)
  // larger world y is higher up on screen
  assert.ok(worldToScreen(view, 0, 5).y < worldToScreen(view, 0, 1).y)
})

test('fitView centres the bounds and keeps the padding', () => {
  const bounds = { minX: 4, maxX: 12, minY: -4, maxY: 4 } // 8 x 8 world
  const view = fitView(bounds, 1000, 500, 25)
  close(view.scale, (500 - 50) / 8) // height is the tighter side
  const centre = worldToScreen(view, 8, 0)
  close(centre.x, 500)
  close(centre.y, 250)
  const topLeft = worldToScreen(view, 4, 4)
  close(topLeft.y, 25)
})

test('zoomAt keeps the point under the cursor fixed and clamps the scale', () => {
  const view = { scale: 10, tx: 0, ty: 0 }
  const before = screenToWorld(view, 300, 200)
  const zoomed = zoomAt(view, 2, 300, 200)
  close(zoomed.scale, 20)
  const after = screenToWorld(zoomed, 300, 200)
  close(after.x, before.x)
  close(after.y, before.y)

  assert.equal(zoomAt(view, 100, 0, 0, 1, 50).scale, 50)
  assert.equal(zoomAt(view, 0.001, 0, 0, 1, 50).scale, 1)
})

test('panBy and centerOn', () => {
  assert.deepEqual(panBy({ scale: 3, tx: 1, ty: 2 }, 10, -5), { scale: 3, tx: 11, ty: -3 })
  const view = centerOn({ scale: 5, tx: 0, ty: 0 }, 7, -2, 800, 600, 40)
  assert.equal(view.scale, 40)
  const s = worldToScreen(view, 7, -2)
  close(s.x, 400)
  close(s.y, 300)
  // centerOn never zooms out
  assert.equal(centerOn({ scale: 80, tx: 0, ty: 0 }, 0, 0, 10, 10, 40).scale, 80)
})

test('computeBounds and visibleWorldRect', () => {
  assert.deepEqual(computeBounds([1, 5, 3], [-2, 0, 4]), { minX: 1, maxX: 5, minY: -2, maxY: 4 })
  const view = { scale: 10, tx: 0, ty: 100 }
  assert.deepEqual(visibleWorldRect(view, 200, 100), { minX: 0, maxX: 20, minY: 0, maxY: 10 })
})

function randomPoints(n, seed = 1) {
  let s = seed
  const rnd = () => ((s = (s * 1103515245 + 12345) % 2147483648) / 2147483648)
  const xs = new Float64Array(n)
  const ys = new Float64Array(n)
  for (let i = 0; i < n; i++) {
    // clustered like UMAP output: a few blobs
    const blob = i % 8
    xs[i] = 5 + (blob % 4) * 2 + rnd() * 1.5
    ys[i] = -4 + Math.floor(blob / 4) * 4 + rnd() * 3
  }
  return { xs, ys }
}

function bruteNearest(xs, ys, x, y, maxDist, accept) {
  let best = -1
  let bestD2 = maxDist * maxDist
  for (let i = 0; i < xs.length; i++) {
    const d2 = (xs[i] - x) ** 2 + (ys[i] - y) ** 2
    if (d2 <= bestD2 && (!accept || accept(i)) && (d2 < bestD2 || best === -1 || i < best)) {
      best = i
      bestD2 = d2
    }
  }
  return best
}

test('grid nearest matches brute force on 8000 clustered points', () => {
  const { xs, ys } = randomPoints(8000)
  const index = createGridIndex(xs, ys, computeBounds(xs, ys))
  const { xs: qx, ys: qy } = randomPoints(500, 7)
  for (let q = 0; q < 500; q++) {
    for (const r of [0.01, 0.05, 0.3]) {
      assert.equal(index.nearest(qx[q], qy[q], r), bruteNearest(xs, ys, qx[q], qy[q], r), `query ${q} r ${r}`)
    }
  }
})

test('grid nearest looks at a few points, not all of them', () => {
  const { xs, ys } = randomPoints(8000)
  const index = createGridIndex(xs, ys, computeBounds(xs, ys))
  index.nearest(6, -2, 0.05)
  assert.ok(index.lastVisited < 200, `visited ${index.lastVisited} points`)
})

test('grid nearest respects the radius, the filter and points outside the grid', () => {
  const xs = Float64Array.from([0, 1, 1.05, 3])
  const ys = Float64Array.from([0, 0, 0, 3])
  const index = createGridIndex(xs, ys, computeBounds(xs, ys), 1)
  assert.equal(index.nearest(1.04, 0, 0.1), 2)
  assert.equal(index.nearest(1.04, 0, 0.1, (i) => i !== 2), 1)
  assert.equal(index.nearest(2, 2, 0.1), -1)
  // a query outside the bounding box still finds a close point inside it
  assert.equal(index.nearest(-0.05, -0.05, 0.1), 0)
  // empty data
  assert.equal(createGridIndex(new Float64Array(0), new Float64Array(0), computeBounds([], [])).nearest(0, 0, 1), -1)
})

test('parsePoints builds typed arrays and skips broken rows', () => {
  const p = parsePoints([
    { track_id: 2, x: 7.6, y: -3.3, genre: 'Hip-Hop' },
    { track_id: 5, x: 8.6, y: -3.5, genre: 'Folk' },
    { track_id: 'x', x: 1, y: 1, genre: 'Pop' },
    { track_id: 9, x: null, y: 1, genre: 'Pop' },
    { track_id: 11, x: 1, y: 2, genre: 'Polka' },
  ])
  assert.equal(p.count, 3)
  assert.equal(p.skipped, 2)
  assert.deepEqual([...p.ids], [2, 5, 11])
  assert.equal(p.byId.get(5), 1)
  assert.equal(GENRES[p.genres[0]].name, 'Hip-Hop')
  assert.equal(p.genres[2], GENRES.length) // unknown genre
  assert.throws(() => parsePoints({ points: [] }), /array/)
})

test('genres: eight FMA genres, distinct colours per mode, unknown is grey', () => {
  assert.equal(GENRES.length, 8)
  assert.equal(new Set(GENRES.map((g) => g.light)).size, 8)
  assert.equal(new Set(GENRES.map((g) => g.dark)).size, 8)
  assert.equal(genreIndex('Pop'), GENRES.findIndex((g) => g.name === 'Pop'))
  assert.equal(genreColor(genreIndex('nope'), false), '#64748b')
})

test('track cache keeps the most recently used entries', () => {
  const cache = createTrackCache(2)
  cache.set(1, 'a')
  cache.set(2, 'b')
  assert.equal(cache.get(1), 'a') // 1 is now the most recent
  cache.set(3, 'c') // evicts 2
  assert.equal(cache.get(2), undefined)
  assert.equal(cache.get(1), 'a')
  assert.equal(cache.get(3), 'c')
  assert.equal(cache.size, 2)
})
