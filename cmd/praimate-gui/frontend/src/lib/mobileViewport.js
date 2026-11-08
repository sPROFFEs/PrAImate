// The visual viewport shrinks independently of CSS vh when the IME opens.
// Keep fixed chrome and flexible workspaces inside the actual visible area.
export function observeMobileViewport(win, root, onChange = () => {}) {
  let frame = 0
  let normalHeight = 0, previousWidth = 0
  const measure = () => {
    frame = 0
    const viewport = win.visualViewport
    const height = viewport?.height || win.innerHeight
    const width = viewport?.width || win.innerWidth
    if (width !== previousWidth) { normalHeight = 0; previousWidth = width }
    normalHeight = Math.max(normalHeight, height)
    root.style.setProperty('--app-height', `${height}px`)
    root.style.setProperty('--app-top', `${viewport?.offsetTop || 0}px`)
    const element = win.document.activeElement
    const editing = !!element && (element.tagName === 'TEXTAREA' || element.isContentEditable ||
      element.tagName === 'INPUT' && !['checkbox','radio','button','submit','reset','file','range','color','hidden'].includes(element.type))
    onChange({ editing, keyboard: editing && normalHeight - height > 120, height })
  }
  const schedule = event => { if (event?.type === 'orientationchange') normalHeight = 0; if (!frame) frame = win.requestAnimationFrame(measure) }
  const events = [[win,'resize'],[win,'orientationchange'],[win.visualViewport,'resize'],[win.visualViewport,'scroll'],[win.document,'focusin'],[win.document,'focusout']]
  for (const [target,event] of events) target?.addEventListener(event,schedule)
  measure()
  return () => {
    for (const [target,event] of events) target?.removeEventListener(event,schedule)
    win.cancelAnimationFrame(frame)
    root.style.removeProperty('--app-height')
    root.style.removeProperty('--app-top')
  }
}
