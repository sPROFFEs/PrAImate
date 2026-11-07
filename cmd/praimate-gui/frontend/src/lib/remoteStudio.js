// The web client owns its windows; the server prepares persistent project/chat
// state without requiring or launching a native editor on the host.
export function remoteStudio(client, openWindow, origin, version, workspaceChanged = () => {}) {
  async function open(folder, chatId = '', config = null) {
    const target = openWindow('about:blank', '_blank')
    if (!target) throw new Error('Allow pop-up windows to open the project editor')
    try {
      let workspace
      if (!chatId && config) {
        const result = await client.call('projects.prepare', [folder, config.agent || '', config.cli || 'claude', config.model || '', config.tools || 'edits', config.localEndpoint || '', config.localModel || ''])
        workspace = result.workspace
        chatId = result.chatId
      } else workspace = await client.call('projects.validate', [folder])
      target.location = `${origin}/#${new URLSearchParams({workspace, chat:chatId})}`
      return chatId || workspace
    } catch (error) { target.close(); throw error }
  }
  return {
    open,
    status: () => ({state:'installed',remote:true,binaryPath:'Browser editor',version,backendRunning:true}),
    openProject: (folder, cli, model, agent, tools) => open(folder, '', {cli,model,agent,tools}),
    openEditor: (folder, agent, cli, model, chatId, localEndpoint, ignoredKey, localModel) => open(folder, chatId, {agent,cli,model,localEndpoint,localModel}),
    async saveConfig(...args) {
      const workspace = args[8] ? await client.call('projects.validate', [args[8]]) : ''
      if (workspace) args[8] = workspace
      const result = await client.call('api.SaveStudioConfig', args)
      if (workspace) workspaceChanged(workspace)
      return result
    },
  }
}
