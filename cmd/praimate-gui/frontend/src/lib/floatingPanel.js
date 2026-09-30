// Move overlays out of scrolling/overflow ancestors without losing Svelte state.
export function floatingPanel(node, { anchor, close }) {
  document.body.appendChild(node)
  function place() {
    const rect = anchor.getBoundingClientRect()
    const edge = anchor.closest('.sidebar')?.getBoundingClientRect().right ?? rect.right
    const width = node.offsetWidth, height = node.offsetHeight, gap = 12
    node.style.left = `${Math.max(gap, Math.min(edge + gap, window.innerWidth - width - gap))}px`
    node.style.top = `${Math.max(gap, Math.min(rect.bottom - height, window.innerHeight - height - gap))}px`
  }
  function outside(event) {
    if (!node.contains(event.target) && !anchor.contains(event.target) && !document.querySelector('[aria-modal="true"]')) close()
  }
  function key(event) {
    if (event.key === 'Escape' && !document.querySelector('[aria-modal="true"]')) { event.preventDefault(); close() }
  }
  const observer = new ResizeObserver(place)
  observer.observe(node)
  window.addEventListener('resize', place)
  document.addEventListener('scroll', place, true)
  document.addEventListener('pointerdown', outside)
  document.addEventListener('keydown', key)
  place()
  node.querySelector('button')?.focus()
  return { destroy() {
    observer.disconnect()
    window.removeEventListener('resize', place)
    document.removeEventListener('scroll', place, true)
    document.removeEventListener('pointerdown', outside)
    document.removeEventListener('keydown', key)
    const restore = node.contains(document.activeElement)
    node.remove()
    if (restore) anchor.focus()
  } }
}
