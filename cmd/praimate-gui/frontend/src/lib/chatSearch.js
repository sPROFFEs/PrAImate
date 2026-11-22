// Pure helpers for the in-chat find bar (Ctrl/Cmd+F). Matching is
// case-insensitive and non-overlapping; highlighting uses Range objects and the
// CSS Custom Highlight API so the rendered message DOM is never mutated.

export function findMatches(text, query) {
  const q = String(query ?? '')
  if (!q || !text) return []
  const literal = q.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  const pattern = new RegExp(literal, 'giu')
  const out = []
  for (const match of String(text).matchAll(pattern)) {
    out.push([match.index, match.index + match[0].length])
    if (out.length >= 5000) break
  }
  return out
}

// Matches across several text chunks: [{ node: index, start, end }].
export function findAllMatches(texts, query) {
  const out = []
  texts.forEach((t, node) => {
    for (const [start, end] of findMatches(t, query)) out.push({ node, start, end })
  })
  return out
}

export function stepIndex(current, total, delta) {
  if (total <= 0) return -1
  if (current < 0) return delta < 0 ? total - 1 : 0
  return (((current + delta) % total) + total) % total
}

export function counterLabel(index, total) {
  return total > 0 ? `${index + 1}/${total}` : '0 results'
}

// Ctrl+F (Cmd+F on macOS) opens the bar. Other text fields keep native behavior
// unless they live inside `scope` (the chat view, which includes its composer).
export function isFindShortcut(e) {
  return (e.ctrlKey || e.metaKey) && !e.altKey && !e.shiftKey && String(e.key).toLowerCase() === 'f'
}

export function shouldOpenFind(e, scope) {
  if (!isFindShortcut(e)) return false
  const t = e.target
  const editable = t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT' || t.isContentEditable)
  if (!editable) return true
  return Boolean(scope && scope.contains && scope.contains(t))
}

// DOM helpers (browser only).
export function collectTextNodes(root) {
  const nodes = []
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT, {
    acceptNode: (n) => (n.nodeValue ? (n.parentElement?.closest('script,style') ? NodeFilter.FILTER_REJECT : NodeFilter.FILTER_ACCEPT) : NodeFilter.FILTER_REJECT)
  })
  while (walker.nextNode()) nodes.push(walker.currentNode)
  return nodes
}

export function buildRanges(root, query) {
  // Keep message boundaries, but match across inline markup such as bold/code.
  const messages = [...root.querySelectorAll('.msg')]
  const groups = messages.length ? messages : [root]
  const ranges = []
  for (const group of groups) {
    const nodes = collectTextNodes(group)
    const starts = []; let offset = 0
    for (const node of nodes) { starts.push(offset); offset += node.nodeValue.length }
    const text = nodes.map(n => n.nodeValue).join('')
    let first = 0, last = 0
    for (const [start, end] of findMatches(text, query)) {
      while (first + 1 < nodes.length && starts[first + 1] <= start) first++
      last = Math.max(first, last)
      while (last + 1 < nodes.length && starts[last + 1] < end) last++
      const range = document.createRange()
      range.setStart(nodes[first], start - starts[first])
      range.setEnd(nodes[last], end - starts[last])
      ranges.push(range)
      if (ranges.length >= 5000) return ranges
    }
  }
  return ranges
}

export function paintHighlights(ranges, current) {
  if (typeof CSS === 'undefined' || !CSS.highlights || typeof Highlight === 'undefined') return
  CSS.highlights.set('chat-find', new Highlight(...ranges))
  if (current >= 0 && ranges[current]) CSS.highlights.set('chat-find-current', new Highlight(ranges[current]))
  else CSS.highlights.delete('chat-find-current')
}

export function clearHighlights() {
  if (typeof CSS === 'undefined' || !CSS.highlights) return
  CSS.highlights.delete('chat-find')
  CSS.highlights.delete('chat-find-current')
}
