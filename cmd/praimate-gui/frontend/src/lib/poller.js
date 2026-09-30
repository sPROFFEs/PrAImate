// One request at a time, with the next timer scheduled after completion.
// Manual refreshes coalesce; a refresh requested during I/O runs once after it.
export function createPoller(task, { delay = () => 3000, visible = () => !document.hidden,
  schedule = setTimeout, cancel = clearTimeout } = {}) {
  let timer, flight, pending = false, stopped = false
  function request() {
    if (stopped) return Promise.resolve()
    cancel(timer)
    pending = true
    if (!flight) {
      flight = (async () => {
        // Let flight be assigned before calling task, even for a sync task.
        await Promise.resolve()
        try {
          while (pending && !stopped) {
            pending = false
            await task()
          }
        } finally {
          flight = null
          if (!stopped) timer = schedule(() => {
            if (visible()) request()
            else timer = schedule(checkVisible, delay())
          }, delay())
        }
      })()
    }
    return flight
  }
  function checkVisible() {
    if (stopped) return
    if (visible()) request()
    else timer = schedule(checkVisible, delay())
  }
  return { request, stop() { stopped = true; pending = false; cancel(timer) } }
}
