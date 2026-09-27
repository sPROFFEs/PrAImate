// Code's terminal attachment consumes flat session fields, not a nested chat.
// Keep the persisted chat ID when replacing an exited PTY so reopening never
// creates another Code chat row.
export function codeReopenPayload(chat, termId = '') {
  const local = chat.Settings?.local
  return {
    termId,
    chatId: chat.ID,
    cli: chat.CLIAgent,
    cwd: chat.WorkspacePath,
    agentId: chat.AgentID || '',
    model: chat.Settings?.model || '',
    localEndpoint: local?.endpoint || '',
    localModel: local?.model || '',
    label: chat.Title || chat.CLIAgent,
  }
}
