// Coalesces redraw requests into at most one animation frame.
//
// cancel() must forget the pending frame, not only cancel it: in development
// React.StrictMode mounts, unmounts and remounts every component, so the
// unmount cleanup cancels the first frame and the remount asks for a new one.
// Keeping the cancelled id around made request() believe a frame was still
// coming, and the map was never drawn.
export function createFrameScheduler(
  callback,
  raf = (fn) => globalThis.requestAnimationFrame(fn),
  caf = (id) => globalThis.cancelAnimationFrame(id),
) {
  let id = null
  return {
    request() {
      if (id !== null) return
      id = raf(() => {
        id = null
        callback()
      })
    },
    cancel() {
      if (id === null) return
      caf(id)
      id = null
    },
    get pending() {
      return id !== null
    },
  }
}
