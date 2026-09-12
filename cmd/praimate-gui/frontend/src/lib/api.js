// Thin wrapper over the Wails-injected window.go.main.App bindings.
// Every call returns a promise; rejections carry the Go error string.
//
// In a plain browser (vite dev without Wails) window.go is absent —
// calls reject with a clear message so pages render their error state
// instead of crashing.

function app() {
  if (typeof window !== 'undefined' && window.go?.main?.App) {
    return window.go.main.App
  }
  return null
}

function call(method, ...args) {
  const a = app()
  if (!a) return Promise.reject(new Error('PrAImate backend not available (running outside Wails?)'))
  return a[method](...args)
}

export const api = {
  skillLibraryV2: (request) => call('SkillLibraryV2', JSON.stringify(request)),
  pickSkillSourceV2: (kind) => call('PickSkillSourceV2', kind),
  exportSkillPackageV2: (ref, digest) => call('ExportSkillPackageV2', ref, digest),
  previewAgentSkillsV2: (body) => call('PreviewAgentSkillsV2', body),
  skillsV2RolloutState: () => call('SkillsV2RolloutState'),
  setSkillsV2RolloutState: (enabled) => call('SetSkillsV2RolloutState', !!enabled),
  chatSkillsV2: (id) => call('ChatSkillsV2', id),
  previewChatSkillsV2: (id, body) => call('PreviewChatSkillsV2', id, body),
  setChatSkillsV2: (id, body) => call('SetChatSkillsV2', id, body),
  installedSkillVersionsV2: () => call('InstalledSkillVersionsV2'),
  buildInstalledSkillSelectionV2: (choices) => call('BuildInstalledSkillSelectionV2', JSON.stringify(choices)),
  saveChatSkillChoicesV2: (id, choices) => call('SaveChatSkillChoicesV2', id, JSON.stringify(choices || [])),
  databaseLockStatus: () => call('DatabaseLockStatus'),
  detachedMode: () => call('DetachedMode'),
  detachSession: (kind, sessionID, title) => call('DetachSession', kind, sessionID, title || ''),
  detachedWindows: () => call('DetachedWindows'),
  detachedSessionActive: () => call('DetachedSessionActive'),
  detachedRendererReady: () => call('DetachedRendererReady'),
  initializeDatabasePassword: (password, confirmation, remember) =>
    call('InitializeDatabasePassword', password, confirmation, !!remember),
  unlockDatabase: (password, remember) =>
    call('UnlockDatabase', password, !!remember),
  forgetDatabasePassword: () => call('ForgetDatabasePassword'),
  health: () => call('Health'),
  about: () => call('About'),
  privacyNotice: () => call('PrivacyNotice'),
  acceptPrivacyNotice: () => call('AcceptPrivacyNotice'),
  firstRun: () => call('FirstRun'),
  completeFirstRun: (root, samples, agents, cloneURL) =>
    call('CompleteFirstRun', root, samples, agents, cloneURL || ''),

  listChats: () => call('ListChats'),
  chatMessages: (id) => call('ChatMessages', id),
  deleteChat: (id) => call('DeleteChat', id),
  startChat: (agentID, cli, cwd) => call('StartChat', agentID, cli, cwd),
  startCleanChat: (cli, model, cwd) => call('StartCleanChat', cli, model, cwd),
  sendChat: (chatID, message) => call('SendChat', chatID, message),
  sendChatWithAttachments: (chatID, message, paths) =>
    call('SendChatWithAttachments', chatID, message, paths),
  sendChatStream: (chatID, message, paths) =>
    call('SendChatStream', chatID, message, paths || []),
  cancelChatTurn: (chatID) => call('CancelChatTurn', chatID),
  resolveApproval: (id, allow, always) => call('ResolveApproval', id, allow, always),
  runChatCommand: (chatID, command) => call('RunChatCommand', chatID, command),
  setChatTools: (chatID, tools) => call('SetChatTools', chatID, tools),
  pickChatAttachments: (chatID) => call('PickChatAttachments', chatID),
  attachmentDataURL: (path) => call('AttachmentDataURL', path),

  listCLIs: () => call('ListCLIs'),
  listCLIModels: (cli) => call('ListCLIModels', cli),
  executionCapabilities: (cli) => call('ExecutionCapabilities', cli),
  preflightExecution: (agentID, surface, cli, model, tools, cwd, localEndpoint, localModel) =>
    call('PreflightExecution', agentID || '', surface, cli, model || '', tools || '', cwd || '', localEndpoint || '', localModel || ''),
  listWorkspaceChats: () => call('ListWorkspaceChats'),
  openWorkspaceChat: (id) => call('OpenWorkspaceChat', id),

  listAgents: () => call('ListAgents'),
  importAgentDialog: () => call('ImportAgentDialog'),
  reviewAgentImportDialog: () => call('ReviewAgentImportDialog'),
  importReviewedAgentPack: (path, reviewDigest) => call('ImportReviewedAgentPack', path, reviewDigest),
  exportAgentDialog: (id) => call('ExportAgentDialog', id),
  deleteAgent: (id) => call('DeleteAgent', id),
  agentYAML: (id) => call('AgentYAML', id),
  saveAgentYAML: (yaml) => call('SaveAgentYAML', yaml),
  newAgentTemplateYAML: () => call('NewAgentTemplateYAML'),
  previewGuidedAgent: (request) => call('PreviewGuidedAgent', request),
  createGuidedAgent: (request) => call('CreateGuidedAgent', request),
  agentRuntimeJSON: (id) => call('AgentRuntimeJSON', id),
  agentRuntimeConfig: (id) => call('AgentRuntimeConfig', id),
  enableAgentRuntime: (id) => call('EnableAgentRuntime', id),
  saveAgentRuntimeJSON: (id, body) => call('SaveAgentRuntimeJSON', id, body),
  listManagedRuns: (agentID) => call('ListManagedRuns', agentID || ''),
  managedRunDetails: (runID) => call('ManagedRunDetails', runID),
  managedArtifactText: (runID, name) => call('ManagedArtifactText', runID, name),
  resumeManagedRun: (runID) => call('ResumeManagedRun', runID),
  cancelManagedRun: (runID) => call('CancelManagedRun', runID),
  getAgentKnowledge: (id) => call('GetAgentKnowledge', id),
  setAgentKnowledgeMode: (id, mode) => call('SetAgentKnowledgeMode', id, mode),
  enableAgentKnowledge: (id) => call('EnableAgentKnowledge', id),
  pickAgentKnowledgeFiles: (id) => call('PickAgentKnowledgeFiles', id),
  pickAgentKnowledgeFolder: (id) => call('PickAgentKnowledgeFolder', id),
  deleteAgentKnowledgeFile: (id, rel) => call('DeleteAgentKnowledgeFile', id, rel),
  pickAgentRequirementsScript: (id, os, instructions) => call('PickAgentRequirementsScript', id, os, instructions || ''),
  runAgentRequirements: (id) => call('RunAgentRequirements', id),
  cancelAgentRequirements: (id) => call('CancelAgentRequirements', id),
  buildAgentRAG: (id, backend, apiKey, model) => call('BuildAgentRAG', id, backend || '', apiKey || '', model || ''),
  cancelAgentRAG: (id) => call('CancelAgentRAG', id),
  installBundledGraphify: () => call('InstallBundledGraphify'),
  exportAgentPackDialog: (id) => call('ExportAgentPackDialog', id),

  updateChatConfig: (chatID, cli, model, tools, localEndpoint, localApiKey, localModel) =>
    call('UpdateChatConfig', chatID, cli, model, tools, localEndpoint || '', localApiKey || '', localModel || ''),
  renameChat: (chatID, newTitle) => call('RenameChat', chatID, newTitle),
  searchChats: (q) => call('SearchChats', q),

  listCLIBackends: () => call('ListCLIBackends'),
  listInstallMethods: (cli) => call('ListInstallMethods', cli),
  installCLI: (cli, methodID) => call('InstallCLI', cli, methodID),
  listManagedTools: () => call('ListManagedTools'),
  listToolInstallMethods: (tool) => call('ListToolInstallMethods', tool),
  installManagedTool: (tool, methodID) => call('InstallManagedTool', tool, methodID),
  buildRequirements: (tool) => call('BuildRequirements', tool),
  buildToolFromSource: (tool) => call('BuildToolFromSource', tool),
  checkUpdate: () => call('CheckUpdate'),

  getLocalLLM: () => call('GetLocalLLM'),
  setLocalLLM: (d) => call('SetLocalLLM', d),
  testLocalLLM: (endpoint, apiKey) => call('TestLocalLLM', endpoint, apiKey),
  testLocalHost: (hostId, endpoint, apiKey) => call('TestLocalHost', hostId, endpoint, apiKey),
  listLocalHosts: () => call('ListLocalHosts'),
  saveLocalHost: (h) => call('SaveLocalHost', h),
  deleteLocalHost: (id) => call('DeleteLocalHost', id),
  setDefaultLocalHost: (id) => call('SetDefaultLocalHost', id),
  localLLMHostsModels: () => call('LocalLLMHostsModels'),
  applyModelsToCLI: (cli, hostId, models) => call('ApplyModelsToCLI', cli, hostId || '', models || []),
  removeModelFromCLI: (cli, hostId, model) => call('RemoveModelFromCLI', cli, hostId || '', model || ''),
  listAppliedCLIModels: () => call('ListAppliedCLIModels'),

  editorMode: () => call('EditorMode'),
  editorListFiles: () => call('EditorListFiles'),
  editorReadFile: (rel) => call('EditorReadFile', rel),
  editorWriteFile: (rel, content) => call('EditorWriteFile', rel, content),
  editorCreateFile: (rel) => call('EditorCreateFile', rel),
  editorRenameFile: (src, dst) => call('EditorRenameFile', src, dst),
  editorDeleteFile: (rel) => call('EditorDeleteFile', rel),
  agentRenameKnowledgeFile: (id, src, dst) => call('AgentRenameKnowledgeFile', id, src, dst),
  refreshPATH: () => call('RefreshPATH'),
  skillsList: () => call('SkillsList'),
  skillsForCLI: (cli) => call('SkillsForCLI', cli),
  skillsUserList: () => call('SkillsUserList'),
  addUserSkill: (input) => call('AddUserSkill', input),
  deleteUserSkill: (id) => call('DeleteUserSkill', id),
  importSkillFromURL: (url) => call('ImportSkillFromURL', url),
  importSkillFromZipFile: (path) => call('ImportSkillFromZipFile', path),
  pickSkillZipFile: () => call('PickSkillZipFile'),
  skillsDefaults: () => call('SkillsDefaults'),
  setSkillsDefaults: (ids) => call('SetSkillsDefaults', ids || []),
  chatSkills: (chatID) => call('ChatSkills', chatID),
  setChatSkills: (chatID, ids) => call('SetChatSkills', chatID, ids || []),
  setChatMCPServers: (chatID, ids) => call('SetChatMCPServers', chatID, ids || []),
  testMCPServer: (id) => call('TestMCPServer', id),
  importMCPServersJSON: (jsonText) => call('ImportMCPServersJSON', jsonText),
  activeChatIDs: () => call('ActiveChatIDs'),
  openEditorFolder: () => call('OpenEditorFolder'),
  openAgentKnowledgeFolder: (id) => call('OpenAgentKnowledgeFolder', id),
  openEditorWindow: (folder, agentID, cli, model, chatID, localEndpoint, localApiKey, localModel) =>
    call('OpenEditorWindow', folder, agentID, cli, model || '', chatID, localEndpoint || '', localApiKey || '', localModel || ''),
  prepareStudioChat: (folder, agentID, cli, model, localEndpoint, localModel, choices) =>
    call('PrepareStudioChat', folder, agentID || '', cli, model || '', localEndpoint || '', localModel || '', choices === null ? '' : JSON.stringify(choices)),
  startTerminal: (agentID, cli, model, cwd, localEndpoint, localApiKey, localModel, resume = false, skills = []) =>
    call('StartTerminal', agentID, cli, model || '', cwd, localEndpoint || '', localApiKey || '', localModel || '', !!resume, skills || []),
  localLLMModels: () => call('LocalLLMModels'),
  recordCodeSession: (agentID, cli, model, cwd, localEndpoint, localApiKey, localModel) =>
    call('RecordCodeSession', agentID || '', cli, model || '', cwd, localEndpoint || '', localApiKey || '', localModel || ''),
  startCodeSessionWithSkills: (agentID, cli, model, cwd, localEndpoint, localModel, choices) =>
    call('StartCodeSessionWithSkills', agentID || '', cli, model || '', cwd, localEndpoint || '', localModel || '', choices === null ? '' : JSON.stringify(choices)),
  startTerminalForChat: (chatID, resume = false) => call('StartTerminalForChat', chatID, !!resume),
  bindChatToTerminal: (termID, chatID) => call('BindChatToTerminal', termID, chatID),
  listTerminalSessions: () => call('ListTerminalSessions'),
  startAgentHelperChat: (cli, model, cwd, agentID) => call('StartAgentHelperChat', cli, model || '', cwd || '', agentID || ''),
  syncAgentYAMLToDisk: (id, cwd) => call('SyncAgentYAMLToDisk', id, cwd || ''),
  readAgentYAMLFromDisk: (id, cwd) => call('ReadAgentYAMLFromDisk', id, cwd || ''),
  writeAgentYAMLDraftToDisk: (id, cwd, body) => call('WriteAgentYAMLDraftToDisk', id, cwd || '', body),
  createAgentFromName: (name) => call('CreateAgentFromName', name),
  agentKnowledgeTree: (id) => call('AgentKnowledgeTree', id),
  agentReadKnowledgeFile: (id, rel) => call('AgentReadKnowledgeFile', id, rel),
  agentWriteKnowledgeFile: (id, rel, content) => call('AgentWriteKnowledgeFile', id, rel, content),
  agentCreateKnowledgeFile: (id, rel) => call('AgentCreateKnowledgeFile', id, rel),
  localCLIStatus: () => call('LocalCLIStatusNow'),
  applyLocalToCLI: (cli, model) => call('ApplyLocalToCLI', cli, model || ''),
  disableLocalForCLI: (cli) => call('DisableLocalForCLI', cli),

  pickFolder: () => call('PickFolder'),
  runWorkflow: (agentID, workflow, cli, model, cwd, inputs, localEndpoint, localApiKey, localModel) =>
    call('RunWorkflow', agentID, workflow, cli, model || '', cwd, inputs || {}, localEndpoint || '', localApiKey || '', localModel || ''),
  runAllWorkflows: (agentID, cli, model, cwd, inputsByWorkflow, localEndpoint, localApiKey, localModel) =>
    call('RunAllWorkflows', agentID, cli, model || '', cwd, inputsByWorkflow || {}, localEndpoint || '', localApiKey || '', localModel || ''),
  privacyPreview: (text) => call('PrivacyPreview', text),
  storedDataInfo: () => call('StoredDataInfo'),
  deleteAllStoredData: (projectsRoot, phrase) =>
    call('DeleteAllStoredData', projectsRoot || '', phrase || ''),

  mcpCatalogue: () => call('MCPCatalogue'),
  mcpServers: () => call('MCPServers'),
  addCustomMCP: (name, transport, command, url, envText) =>
    call('AddCustomMCP', name, transport, command, url, envText),
  updateMCPServer: (id, name, transport, command, url, envText) =>
    call('UpdateMCPServer', id, name, transport, command, url, envText),
  setMCPEnabled: (id, on) => call('SetMCPEnabled', id, on),
  deleteMCPServer: (id) => call('DeleteMCPServer', id),

  listWatchers: () => call('ListWatchers'),
  addWatcher: (agentID, path, workflow, patterns) =>
    call('AddWatcher', agentID, path, workflow, patterns),
  setWatcherEnabled: (id, on) => call('SetWatcherEnabled', id, on),
  deleteWatcher: (id) => call('DeleteWatcher', id),

  listSchedules: () => call('ListSchedules'),
  addCronSchedule: (agentID, cron, workflow) => call('AddCronSchedule', agentID, cron, workflow),
  setScheduleEnabled: (id, on) => call('SetScheduleEnabled', id, on),
  deleteSchedule: (id) => call('DeleteSchedule', id),

  listPrivacyPatterns: () => call('ListPrivacyPatterns'),
  addPrivacyPattern: (p) => call('AddPrivacyPattern', p),
  deletePrivacyPattern: (i) => call('DeletePrivacyPattern', i),

  getGUISetting: (k) => call('GetGUISetting', k),
  setGUISetting: (k, v) => call('SetGUISetting', k, v),

  praimateCodeInstalled: () => call('PraimateCodeInstalled'),
  installPraimateCode: () => call('InstallPraimateCode'),

  backupStatus: () => call('BackupStatus'),
  setBackupEnabled: (on) => call('SetBackupEnabled', on),
  configureBackup: (mode, remoteURL) => call('ConfigureBackup', mode, remoteURL || ''),
  setBackupRemote: (url) => call('SetBackupRemote', url),
  testBackupRemote: (url) => call('TestBackupRemote', url),
  backupSyncNow: () => call('BackupSyncNow'),
  resolveBackupDivergence: (strategy) => call('ResolveBackupDivergence', strategy),
  backupForcePush: () => call('BackupForcePush'),
  backupResetFromRemote: () => call('BackupResetFromRemote'),
  backupDisconnect: () => call('BackupDisconnect'),
  setBackupAutoSync: (on) => call('SetBackupAutoSync', on),
  setBackupForceLocal: (on) => call('SetBackupForceLocal', on),
}

// onTurn subscribes to streamed workflow turns. Returns an unsubscribe
// function. No-op outside Wails.
export function onTurn(handler) {
  if (typeof window !== 'undefined' && window.runtime?.EventsOn) {
    window.runtime.EventsOn('praimate:turn', handler)
    return () => window.runtime.EventsOff('praimate:turn')
  }
  return () => {}
}

// onWorkflowStream subscribes to live workflow events (state, text deltas
// and tool activity). Returns an unsubscribe function. No-op outside Wails.
export function onWorkflowStream(handler) {
  if (typeof window !== 'undefined' && window.runtime?.EventsOn) {
    window.runtime.EventsOn('praimate:workflow-stream', handler)
    return () => window.runtime.EventsOff('praimate:workflow-stream')
  }
  return () => {}
}

// onChatStream subscribes to live chat turn events (text deltas, tool
// activity) emitted while SendChatStream runs. Returns an unsubscribe
// function. No-op outside Wails.
export function onChatStream(handler) {
  if (typeof window !== 'undefined' && window.runtime?.EventsOn) {
    window.runtime.EventsOn('praimate:chat-stream', handler)
    return () => window.runtime.EventsOff('praimate:chat-stream')
  }
  return () => {}
}

// onApproval subscribes to mid-turn permission requests ("ask" Tools
// level). Answer with api.resolveApproval(id, allow, always). Returns
// an unsubscribe function. No-op outside Wails.
export function onApproval(handler) {
  if (typeof window !== 'undefined' && window.runtime?.EventsOn) {
    window.runtime.EventsOn('praimate:approval', handler)
    return () => window.runtime.EventsOff('praimate:approval')
  }
  return () => {}
}

// onRequirementsProgress subscribes to lifecycle and live-output events from
// an explicitly started agent requirements script.
export function onRequirementsProgress(handler) {
  if (typeof window !== 'undefined' && window.runtime?.EventsOn) {
    window.runtime.EventsOn('praimate:requirements', handler)
    return () => window.runtime.EventsOff('praimate:requirements')
  }
  return () => {}
}
