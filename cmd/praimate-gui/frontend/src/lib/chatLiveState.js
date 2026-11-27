import { applyChatStreamEvent } from './chatActivity.js'

export function applyLiveChatEvent(view, event) {
  const turnId = event.turnId || 0
  const sequence = event.sequence || 0
  if (turnId && view?.turnId === turnId && sequence <= view.sequence) return view
  if (turnId && view?.turnId > turnId) return view
  const stream = view?.turnId === turnId && event.type !== 'started' ? view.stream : null
  return { turnId, sequence, stream: applyChatStreamEvent(stream, event) }
}

export function restoreLiveChat(snapshot, received) {
  let view = snapshot?.turnId ? { turnId: snapshot.turnId, sequence: 0, stream: null } : null
  for (const event of snapshot?.events || []) view = applyLiveChatEvent(view, event)
  for (const event of received) view = applyLiveChatEvent(view, event)
  return view
}
