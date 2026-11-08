export function shouldSendMessage(event, mobile = false) {
  return event.key === 'Enter' && !event.shiftKey && !event.isComposing && event.keyCode !== 229 &&
    (!mobile || event.ctrlKey || event.metaKey)
}
