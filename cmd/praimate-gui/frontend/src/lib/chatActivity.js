export function emptyChatStream() { return { text: '', activity: [] } }

export function applyChatStreamEvent(stream, event) {
  if (event.type === 'text') return { ...(stream || emptyChatStream()), text: (stream?.text || '') + (event.text || '') }
  const next = { ...(stream || emptyChatStream()), activity: [...(stream?.activity || [])] }
  if (event.type === 'reasoning') {
    const text = event.text || event.detail || ''
    const last = next.activity.at(-1)
    if (last?.type === 'reasoning') next.activity[next.activity.length - 1] = { ...last, text: (last.text || '') + text }
    else if (text) next.activity.push({ type: 'reasoning', text, ok: true })
  } else if (event.type === 'tool_start') next.activity.push({ type: 'tool', id: event.id || '', tool: event.tool, detail: event.detail, done: false, ok: true })
  else if (event.type === 'tool_end') {
    const index = next.activity.findIndex(item => item.type === 'tool' && !item.done && (event.id ? item.id === event.id : true))
    if (index >= 0) next.activity[index] = { ...next.activity[index], done: true, ok: event.ok !== false }
  } else if (['step_start', 'step_finish', 'context_compacted', 'context_recovery', 'error', 'retry', 'status'].includes(event.type)) {
    next.activity.push({ type: event.type, detail: event.detail, ok: event.type !== 'error' && event.ok !== false })
  }
  return next
}

export function activityName(item) {
  return ({ reasoning: 'thought', step_start: 'step', step_finish: 'step done', retry: 'retry', status: 'status', context_compacted: 'context compacted', context_recovery: 'context recovery', error: 'error' })[item.type] || item.tool || item.type || 'tool'
}

export function activityStatus(item) {
  if (item.ok === false || item.type === 'error') return '✗'
  if (item.type === 'reasoning') return '💭'
  if (['step_start', 'retry', 'status'].includes(item.type) || item.done === false) return '◌'
  return '✓'
}
