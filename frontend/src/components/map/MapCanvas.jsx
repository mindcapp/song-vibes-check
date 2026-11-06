import { forwardRef, useCallback, useEffect, useImperativeHandle, useMemo, useRef, useState } from 'react'
import { GENRES, genreColor } from '../../map/genres'
import { createFrameScheduler } from '../../map/frameScheduler'
import { centerOn, fitView, isOnScreen, panBy, worldToScreen, screenToWorld, zoomAt } from '../../map/viewport'

const HIT_RADIUS_PX = 12 // hover/click target: 24 px across, however small the dot
const DRAG_THRESHOLD_PX = 4 // less movement than this between down and up is a click
const MIN_ZOOM = 0.5 // relative to the fitted view
const MAX_ZOOM = 60
const SEARCH_ZOOM = 6 // centring on a track zooms in at least this far
const KEY_PAN_PX = 60
const KEY_ZOOM = 1.25
const DIMMED_ALPHA = 0.12

const THEME = {
  light: { surface: '#f8fafc', ink: '#0f172a', line: 'rgba(15, 23, 42, 0.55)' },
  dark: { surface: '#0f172a', ink: '#f8fafc', line: 'rgba(248, 250, 252, 0.6)' },
}

function pointRadius(scale, fitScale) {
  const zoom = scale / fitScale
  return Math.min(5, Math.max(2, 2 + 0.75 * Math.log2(Math.max(zoom, 1))))
}

// MapCanvas draws the points on one <canvas> and handles pan, wheel/pinch
// zoom, hover and click. Heavy per-frame state lives in refs; React only
// re-renders when the hovered point changes.
//
// Props:
//   points       parsed points (map/points.js)
//   index        grid index over points (map/spatialIndex.js)
//   bounds       world bounds of the points
//   hidden       Set of genre indices that are switched off
//   focusGenre   genre index to emphasise (others dimmed), or null
//   selected     point index of the selected track, or -1
//   neighbors    point indices of its neighbours
//   highlight    point index to ring (neighbour hovered in the panel), or -1
//   dark         use the dark palette
//   onPick(i)    a point was clicked or tapped
//   onHover(i)   the hovered point changed (-1 for none)
//   renderTooltip(i)  tooltip content for point i
//   ref          { centerOn(i), zoomBy(f), reset() }
const MapCanvas = forwardRef(function MapCanvas(props, ref) {
  // onPick and onHover are read through propsRef so handlers never go stale
  const { points, index, bounds, hidden, focusGenre, selected, neighbors, highlight, dark, renderTooltip } = props

  const containerRef = useRef(null)
  const canvasRef = useRef(null)
  const sizeRef = useRef({ w: 0, h: 0 })
  const viewRef = useRef(null)
  const fitScaleRef = useRef(1)
  const drawRef = useRef(null)
  const schedulerRef = useRef(null)
  if (schedulerRef.current === null) {
    // stable for the component's life; always calls the latest draw
    schedulerRef.current = createFrameScheduler(() => drawRef.current?.())
  }
  const pointersRef = useRef(new Map())
  const gestureRef = useRef(null)
  const hoverRef = useRef(-1) // hovered point index, drawn as a ring
  const propsRef = useRef(props)
  propsRef.current = props

  const [hover, setHover] = useState(null) // { i, x, y } in CSS px, or null

  // point indices grouped by genre, so each genre is one fill
  const byGenre = useMemo(() => {
    const groups = Array.from({ length: GENRES.length + 1 }, () => [])
    for (let i = 0; i < points.count; i++) groups[points.genres[i]].push(i)
    return groups.map((g) => Int32Array.from(g))
  }, [points])

  const draw = useCallback(() => {
    const canvas = canvasRef.current
    const view = viewRef.current
    const { w, h } = sizeRef.current
    if (!canvas || !view || w === 0 || h === 0) return

    const p = propsRef.current
    const dpr = window.devicePixelRatio || 1
    const bw = Math.round(w * dpr)
    const bh = Math.round(h * dpr)
    if (canvas.width !== bw || canvas.height !== bh) {
      canvas.width = bw
      canvas.height = bh
    }
    const ctx = canvas.getContext('2d')
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
    ctx.clearRect(0, 0, w, h)

    const theme = p.dark ? THEME.dark : THEME.light
    const r = pointRadius(view.scale, fitScaleRef.current)
    const { xs, ys } = p.points
    const TAU = Math.PI * 2

    for (let g = 0; g < byGenre.length; g++) {
      if (p.hidden.has(g)) continue
      const members = byGenre[g]
      ctx.globalAlpha = p.focusGenre != null && p.focusGenre !== g ? DIMMED_ALPHA : 0.9
      ctx.fillStyle = genreColor(g, p.dark)
      ctx.beginPath()
      for (let k = 0; k < members.length; k++) {
        const i = members[k]
        const sx = view.tx + xs[i] * view.scale
        const sy = view.ty - ys[i] * view.scale
        if (!isOnScreen(sx, sy, r, w, h)) continue
        ctx.moveTo(sx + r, sy)
        ctx.arc(sx, sy, r, 0, TAU)
      }
      ctx.fill()
    }
    ctx.globalAlpha = 1

    const at = (i) => worldToScreen(view, xs[i], ys[i])
    const dot = (i, radius, ring) => {
      const s = at(i)
      ctx.beginPath()
      ctx.arc(s.x, s.y, radius, 0, TAU)
      ctx.fillStyle = genreColor(p.points.genres[i], p.dark)
      ctx.fill()
      ctx.lineWidth = 2
      ctx.strokeStyle = ring
      ctx.stroke()
    }
    const ring = (i, radius) => {
      const s = at(i)
      ctx.beginPath()
      ctx.arc(s.x, s.y, radius, 0, TAU)
      ctx.lineWidth = 2
      ctx.strokeStyle = theme.ink
      ctx.stroke()
    }

    if (p.selected >= 0) {
      const from = at(p.selected)
      ctx.lineWidth = 1.5
      ctx.strokeStyle = theme.line
      ctx.beginPath()
      for (const n of p.neighbors) {
        const to = at(n)
        ctx.moveTo(from.x, from.y)
        ctx.lineTo(to.x, to.y)
      }
      ctx.stroke()
      for (const n of p.neighbors) dot(n, r + 2.5, theme.surface)
      dot(p.selected, r + 4.5, theme.ink)
    }
    if (p.highlight >= 0) ring(p.highlight, r + 6)
    const hovered = hoverRef.current
    if (hovered >= 0) ring(hovered, r + 4)
  }, [byGenre])

  drawRef.current = draw

  const requestDraw = useCallback(() => schedulerRef.current.request(), [])

  const setView = useCallback(
    (next) => {
      viewRef.current = next
      requestDraw()
    },
    [requestDraw],
  )

  const fit = useCallback(() => {
    const { w, h } = sizeRef.current
    const view = fitView(bounds, w, h)
    fitScaleRef.current = view.scale
    setView(view)
  }, [bounds, setView])

  const limits = () => [fitScaleRef.current * MIN_ZOOM, fitScaleRef.current * MAX_ZOOM]

  const updateHover = useCallback(
    (i) => {
      const view = viewRef.current
      if (i >= 0 && view) {
        const s = worldToScreen(view, points.xs[i], points.ys[i])
        setHover((prev) => (prev && prev.i === i && prev.x === s.x && prev.y === s.y ? prev : { i, x: s.x, y: s.y }))
      } else {
        setHover(null)
      }
      if (hoverRef.current !== i) {
        hoverRef.current = i
        propsRef.current.onHover?.(i)
        requestDraw()
      }
    },
    [points, requestDraw],
  )

  // nearest visible point within the hit radius of a screen position
  const hitTest = useCallback(
    (sx, sy) => {
      const view = viewRef.current
      if (!view) return -1
      const w = screenToWorld(view, sx, sy)
      const hiddenNow = propsRef.current.hidden
      return index.nearest(w.x, w.y, HIT_RADIUS_PX / view.scale, (i) => !hiddenNow.has(points.genres[i]))
    },
    [index, points],
  )

  useImperativeHandle(
    ref,
    () => ({
      centerOn(i) {
        const { w, h } = sizeRef.current
        if (!viewRef.current || i < 0) return
        setView(centerOn(viewRef.current, points.xs[i], points.ys[i], w, h, fitScaleRef.current * SEARCH_ZOOM))
        updateHover(-1)
      },
      zoomBy(factor) {
        const { w, h } = sizeRef.current
        if (!viewRef.current) return
        setView(zoomAt(viewRef.current, factor, w / 2, h / 2, ...limits()))
        updateHover(-1)
      },
      reset() {
        fit()
        updateHover(-1)
      },
    }),
    [points, setView, fit, updateHover],
  )

  // size: fit on first layout, keep the centre on later resizes
  useEffect(() => {
    const el = containerRef.current
    const observer = new ResizeObserver(([entry]) => {
      const { width, height } = entry.contentRect
      const prev = sizeRef.current
      sizeRef.current = { w: width, h: height }
      if (!viewRef.current || prev.w === 0) {
        fit()
      } else {
        setView(panBy(viewRef.current, (width - prev.w) / 2, (height - prev.h) / 2))
      }
    })
    observer.observe(el)
    return () => observer.disconnect()
  }, [fit, setView])

  // redraw when anything drawn changes
  useEffect(requestDraw, [requestDraw, hidden, focusGenre, selected, neighbors, highlight, dark])

  // a hidden genre's point must not stay hovered
  useEffect(() => {
    if (hoverRef.current >= 0 && hidden.has(points.genres[hoverRef.current])) updateHover(-1)
  }, [hidden, points, updateHover])

  useEffect(() => () => schedulerRef.current.cancel(), [])

  // Wheel zoom needs a non-passive listener to stop the page from scrolling.
  useEffect(() => {
    const canvas = canvasRef.current
    const onWheel = (e) => {
      e.preventDefault()
      const rect = canvas.getBoundingClientRect()
      const sx = e.clientX - rect.left
      const sy = e.clientY - rect.top
      const delta = e.deltaMode === 1 ? e.deltaY * 16 : e.deltaY
      viewRef.current = zoomAt(viewRef.current, Math.exp(-delta * 0.0015), sx, sy, ...limits())
      requestDraw()
      updateHover(hitTest(sx, sy))
    }
    canvas.addEventListener('wheel', onWheel, { passive: false })
    return () => canvas.removeEventListener('wheel', onWheel)
  }, [hitTest, requestDraw, updateHover])

  const local = (e) => {
    const rect = canvasRef.current.getBoundingClientRect()
    return { x: e.clientX - rect.left, y: e.clientY - rect.top }
  }

  const startPinch = () => {
    const [a, b] = [...pointersRef.current.values()]
    gestureRef.current = {
      kind: 'pinch',
      dist: Math.hypot(a.x - b.x, a.y - b.y) || 1,
      mid: { x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 },
      moved: true,
    }
  }

  const onPointerDown = (e) => {
    if (e.pointerType === 'mouse' && e.button !== 0) return
    canvasRef.current.setPointerCapture(e.pointerId)
    const p = local(e)
    pointersRef.current.set(e.pointerId, p)
    if (pointersRef.current.size === 1) {
      gestureRef.current = { kind: 'drag', start: p, last: p, moved: false }
    } else if (pointersRef.current.size === 2) {
      startPinch()
      updateHover(-1)
    }
  }

  const onPointerMove = (e) => {
    const p = local(e)
    const gesture = gestureRef.current
    if (!pointersRef.current.has(e.pointerId) || !gesture) {
      if (e.pointerType === 'mouse') updateHover(hitTest(p.x, p.y))
      return
    }
    pointersRef.current.set(e.pointerId, p)

    if (gesture.kind === 'pinch' && pointersRef.current.size >= 2) {
      const [a, b] = [...pointersRef.current.values()]
      const dist = Math.hypot(a.x - b.x, a.y - b.y) || 1
      const mid = { x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 }
      let view = panBy(viewRef.current, mid.x - gesture.mid.x, mid.y - gesture.mid.y)
      view = zoomAt(view, dist / gesture.dist, mid.x, mid.y, ...limits())
      gesture.dist = dist
      gesture.mid = mid
      setView(view)
      return
    }
    if (gesture.kind === 'drag') {
      if (!gesture.moved && Math.hypot(p.x - gesture.start.x, p.y - gesture.start.y) > DRAG_THRESHOLD_PX) {
        gesture.moved = true
        updateHover(-1)
      }
      if (gesture.moved) {
        setView(panBy(viewRef.current, p.x - gesture.last.x, p.y - gesture.last.y))
      }
      gesture.last = p
    }
  }

  const onPointerUp = (e) => {
    const gesture = gestureRef.current
    const wasDown = pointersRef.current.delete(e.pointerId)
    if (!wasDown) return
    if (pointersRef.current.size === 1) {
      // a pinch became a one-finger drag; never a click
      const [rest] = [...pointersRef.current.values()]
      gestureRef.current = { kind: 'drag', start: rest, last: rest, moved: true }
      return
    }
    gestureRef.current = null
    if (e.type === 'pointerup' && gesture?.kind === 'drag' && !gesture.moved) {
      const p = local(e)
      const i = hitTest(p.x, p.y)
      if (i >= 0) propsRef.current.onPick(i)
    }
  }

  const onKeyDown = (e) => {
    const { w, h } = sizeRef.current
    const actions = {
      ArrowLeft: () => panBy(viewRef.current, KEY_PAN_PX, 0),
      ArrowRight: () => panBy(viewRef.current, -KEY_PAN_PX, 0),
      ArrowUp: () => panBy(viewRef.current, 0, KEY_PAN_PX),
      ArrowDown: () => panBy(viewRef.current, 0, -KEY_PAN_PX),
      '+': () => zoomAt(viewRef.current, KEY_ZOOM, w / 2, h / 2, ...limits()),
      '=': () => zoomAt(viewRef.current, KEY_ZOOM, w / 2, h / 2, ...limits()),
      '-': () => zoomAt(viewRef.current, 1 / KEY_ZOOM, w / 2, h / 2, ...limits()),
      0: () => fitView(bounds, w, h),
    }
    const action = actions[e.key]
    if (!action || !viewRef.current) return
    e.preventDefault()
    setView(action())
    updateHover(-1)
  }

  const tooltip = hover && renderTooltip ? renderTooltip(hover.i) : null
  const { w: cw } = sizeRef.current
  const flip = hover && hover.x > cw - 240

  return (
    <div ref={containerRef} className="relative h-full w-full overflow-hidden">
      <canvas
        ref={canvasRef}
        tabIndex={0}
        role="application"
        aria-roledescription="map"
        aria-label={`Map of ${points.count} tracks placed by sound similarity, coloured by genre`}
        aria-describedby="map-instructions"
        className="block h-full w-full cursor-grab touch-none focus:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-indigo-500 active:cursor-grabbing"
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={onPointerUp}
        onPointerCancel={onPointerUp}
        onPointerLeave={(e) => e.pointerType === 'mouse' && !pointersRef.current.size && updateHover(-1)}
        onKeyDown={onKeyDown}
      />
      <p id="map-instructions" className="sr-only">
        Drag or use the arrow keys to move, the mouse wheel, a pinch or the plus and minus keys to zoom, 0 to
        reset. Use the search field to select a track.
      </p>
      {tooltip && (
        <div
          role="tooltip"
          className="pointer-events-none absolute z-10 max-w-[14rem] rounded-md border border-slate-200 bg-white px-3 py-2 text-sm shadow-lg dark:border-slate-700 dark:bg-slate-800"
          style={{
            left: flip ? undefined : hover.x + 14,
            right: flip ? cw - hover.x + 14 : undefined,
            top: Math.max(4, hover.y - 12),
          }}
        >
          {tooltip}
        </div>
      )}
    </div>
  )
})

export default MapCanvas
