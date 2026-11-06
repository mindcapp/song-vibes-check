// Mapping between UMAP world coordinates and CSS pixels on the canvas.
//
// A view is { scale, tx, ty }: screenX = tx + x * scale, screenY = ty - y * scale.
// The y axis is flipped so that larger UMAP y is drawn higher up, as on a
// chart. Everything here is pure, so it is tested with node --test.

export function computeBounds(xs, ys) {
  let minX = Infinity
  let minY = Infinity
  let maxX = -Infinity
  let maxY = -Infinity
  for (let i = 0; i < xs.length; i++) {
    if (xs[i] < minX) minX = xs[i]
    if (xs[i] > maxX) maxX = xs[i]
    if (ys[i] < minY) minY = ys[i]
    if (ys[i] > maxY) maxY = ys[i]
  }
  if (xs.length === 0) {
    return { minX: 0, minY: 0, maxX: 1, maxY: 1 }
  }
  return { minX, minY, maxX, maxY }
}

export function worldToScreen(view, x, y) {
  return { x: view.tx + x * view.scale, y: view.ty - y * view.scale }
}

export function screenToWorld(view, sx, sy) {
  return { x: (sx - view.tx) / view.scale, y: (view.ty - sy) / view.scale }
}

// fitView shows the whole bounds inside width x height with padding (px) on
// every side, centred.
export function fitView(bounds, width, height, padding = 24) {
  const w = Math.max(bounds.maxX - bounds.minX, 1e-9)
  const h = Math.max(bounds.maxY - bounds.minY, 1e-9)
  const availW = Math.max(width - 2 * padding, 1)
  const availH = Math.max(height - 2 * padding, 1)
  const scale = Math.min(availW / w, availH / h)
  const cx = (bounds.minX + bounds.maxX) / 2
  const cy = (bounds.minY + bounds.maxY) / 2
  return { scale, tx: width / 2 - cx * scale, ty: height / 2 + cy * scale }
}

export function clampScale(scale, minScale, maxScale) {
  return Math.min(Math.max(scale, minScale), maxScale)
}

// zoomAt multiplies the scale by factor (clamped) and keeps the world point
// under the screen point (sx, sy) where it is, as wheel and pinch zoom expect.
export function zoomAt(view, factor, sx, sy, minScale = 0, maxScale = Infinity) {
  const scale = clampScale(view.scale * factor, minScale, maxScale)
  const anchor = screenToWorld(view, sx, sy)
  return { scale, tx: sx - anchor.x * scale, ty: sy + anchor.y * scale }
}

export function panBy(view, dx, dy) {
  return { scale: view.scale, tx: view.tx + dx, ty: view.ty + dy }
}

// centerOn puts world point (x, y) in the middle of width x height, zooming
// in to at least minScale so a single track is easy to see.
export function centerOn(view, x, y, width, height, minScale = 0) {
  const scale = Math.max(view.scale, minScale)
  return { scale, tx: width / 2 - x * scale, ty: height / 2 + y * scale }
}

// visibleWorldRect is the part of the world on screen, for culling.
export function visibleWorldRect(view, width, height) {
  const a = screenToWorld(view, 0, 0)
  const b = screenToWorld(view, width, height)
  return {
    minX: Math.min(a.x, b.x),
    maxX: Math.max(a.x, b.x),
    minY: Math.min(a.y, b.y),
    maxY: Math.max(a.y, b.y),
  }
}
