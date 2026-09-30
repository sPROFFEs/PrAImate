// Keep keyboard navigation inside a modal, then restore its trigger on close.
export function focusDialog(node, options = {}) {
  const previous = document.activeElement
  node.tabIndex = -1
  const focusable = () => [...node.querySelectorAll('button, input, select, textarea, a[href], [tabindex]')]
    .filter((el) => !el.disabled && el.tabIndex >= 0 && el.getClientRects().length)
  const frame = requestAnimationFrame(() => (focusable()[0] || node).focus())
  function keydown(event) {
    if (event.key === 'Escape' && options.onClose) {
      event.preventDefault()
      event.stopPropagation()
      options.onClose()
    } else if (event.key === 'Tab') {
      const items = focusable()
      const first = items[0], last = items.at(-1)
      if (!first) { event.preventDefault(); node.focus(); return }
      if (event.shiftKey && (document.activeElement === first || document.activeElement === node)) {
        event.preventDefault(); last.focus()
      } else if (!event.shiftKey && (document.activeElement === last || document.activeElement === node)) {
        event.preventDefault(); first.focus()
      }
    }
  }
  node.addEventListener('keydown', keydown)
  return {
    update(next) { options = next },
    destroy() {
      cancelAnimationFrame(frame)
      node.removeEventListener('keydown', keydown)
      if (previous?.isConnected) previous.focus()
    },
  }
}
