import { RemoteClient } from './remoteClient.js'
import { remoteTerminal } from './remoteTerminal.js'
import { remoteStudio } from './remoteStudio.js'
import { restoreRemoteRoute } from './remoteNavigation.js'

function bytesToBase64(bytes) { let text = ''; for (let i = 0; i < bytes.length; i += 0x8000) text += String.fromCharCode(...bytes.subarray(i, i + 0x8000)); return btoa(text) }
function base64Bytes(text) { return Uint8Array.from(atob(text || ''), char => char.charCodeAt(0)) }

export async function installRemoteBridge(token) {
  const listeners = new Map(), hashes = new Map()
  const notify = (name, payload) => { for (const callback of [...(listeners.get(name) || [])]) callback(payload) }
  const editor = new URLSearchParams(location.hash.replace(/^#/, ''))
  let workspace = editor.get('workspace') || ''
  const client = new RemoteClient({ baseURL: location.origin, token,
    onConnection(connected, error) { notify('praimate:remote-connection', { connected, error }) },
    async onEvent(event) {
      if (event.name === 'resync') {
		if (workspace) client.call('files.watch',[workspace]).catch(()=>{})
        for (const name of listeners.keys()) if (name.startsWith('term:data:')) {
          const id = name.slice(10)
          try { notify(name, await client.call('api.GetTerminalSnapshot', [id])) } catch { /* Exited terminals are refreshed from the session list. */ }
        }
        notify('praimate:detached-resync', {}); notify('praimate:remote-resync', {})
      } else { if (event.name !== 'praimate:editor-fs' || !event.payload.workspace || event.payload.workspace === workspace) notify(event.name, event.payload); if (event.name === 'praimate:worker-update') notify('praimate:detached-connected', {}) }
    },
  })
  const status = await client.connect()
  const terminal = remoteTerminal(client, warning => notify('praimate:remote-warning', warning))
  const studio = remoteStudio(client, (...args) => window.open(...args), location.origin, status.version, next => {
    if (!workspace || workspace === next) return
    workspace = next
    hashes.clear()
    editor.set('workspace', next)
    history.replaceState(null, '', `#${editor}`)
    client.call('files.watch', [workspace]).catch(error => notify('praimate:remote-warning', {message:error.message}))
  })
  if (workspace) client.call('files.watch',[workspace]).catch(()=>{})
  const pickServerFolder = async () => {
    if(window.praimateMobile){
      const {default:Picker}=await import('./ServerFolderPicker.svelte')
      const about=await client.call('api.About',[])
      return new Promise(resolve=>{
        const host=document.createElement('div');document.body.appendChild(host)
        const picker=new Picker({target:host,props:{client,initial:sessionStorage.getItem('praimate.remote.folder')||(about.os==='windows'?'C:/':'/'),done:path=>{if(path)sessionStorage.setItem('praimate.remote.folder',path);picker.$destroy();host.remove();resolve(path)}}})
      })
    }
    const path = window.prompt('Server folder', sessionStorage.getItem('praimate.remote.folder') || '/')
    if (!path) return ''
    const valid = await client.call('projects.validate', [path]); sessionStorage.setItem('praimate.remote.folder', valid); return valid
  }
  async function upload(multiple = false, accept = '') {
    if (window.praimateMobile) return window.praimateMobile.upload(multiple, accept)
    const files = await new Promise(resolve => {
      const input = document.createElement('input'); input.type = 'file'; input.multiple = multiple; input.accept = accept
      input.onchange = () => resolve(Array.from(input.files || [])); input.oncancel = () => resolve([]); input.click()
    })
    const results = []
    for (const file of files) {
      if (file.size > 512 * 1024 * 1024) throw new Error('File exceeds the 512 MiB upload limit')
      const response = await client.fetch(`${client.baseURL}/v1/upload?name=${encodeURIComponent(file.name)}`, {method:'PUT',headers:{Authorization:`Bearer ${client.token}`},body:file})
      const result = await response.json(); if (!response.ok) throw new Error(result.error?.message || 'Upload failed'); results.push(result)
    }
    return results
  }
  async function downloadExport(result) {
    if (window.praimateMobile) return window.praimateMobile.download(result)
    const response = await client.fetch(`${client.baseURL}/v1/download?${new URLSearchParams({workspace:result.workspace,path:result.path})}`,{headers:{Authorization:`Bearer ${client.token}`}})
    if (!response.ok) throw new Error('Download failed')
    const url=URL.createObjectURL(await response.blob()),link=document.createElement('a');link.href=url;link.download=result.name;link.click();setTimeout(()=>URL.revokeObjectURL(url),10000);return result.name
  }
  const openEditor = studio.open
  const file = (method, params) => client.call(method, [{ workspace, ...params }])
  const local = {
    DetachedMode: () => ({ active: false }), DetachedWindows: () => [], DetachedSessionActive: () => false, DetachedRendererReady: () => true,
    DetachedChatConfig: () => null,
    DetachSession: async (kind, id) => {
      const route = kind === 'workers' ? {page:'workers',worker:id} : kind === 'terminal' ? {page:'code',terminal:id} : {page:'chats',chat:id}
      if (kind === 'studio') { const chats=await client.call('api.ListChats',[]); const chat=chats.find(item=>item.ID===id); if (!chat) throw new Error('Conversation not found'); return openEditor(chat.WorkspacePath,id) }
      const target=window.open(`${location.origin}/#${new URLSearchParams(route)}`,'_blank');if(!target)throw new Error('Allow pop-up windows to open this session');return id
    },
    EditorMode: () => ({ active: !!workspace, folder: workspace, chatId: editor.get('chat') || '' }),
    PickFolder: pickServerFolder, PickProjectFolder: pickServerFolder,
    PickSkillSourceV2: async kind => kind === 'directory' ? pickServerFolder() : (await upload(false, '.zip'))[0]?.path || '',
    PickSkillZipFile: async () => (await upload(false, '.zip'))[0]?.path || '',
    PickChatAttachments: () => upload(true),
    ReviewAgentImportDialog: async () => { const item = (await upload(false, '.md,.yaml,.yml,.zip,.praimate-agent'))[0]; if (!item) return null; if (/\.(zip|praimate-agent)$/i.test(item.name)) return client.call('agents.inspect', [item.path]); return { agent: await client.call('agents.import', [item.path]) } },
    ImportAgentDialog: async () => { const item = (await upload(false))[0]; return item ? client.call('agents.import', [item.path]) : null },
    ExportAgentDialog: async id => downloadExport(await client.call('agents.download', [id, false, 'yaml'])),
    ExportAgentPackDialog: async id => downloadExport(await client.call('agents.download', [id, true, 'pack'])),
    ExportSkillPackageV2: async (ref, digest) => downloadExport(await client.call('skills.export', [ref, digest])),
    PickAgentKnowledgeFiles: async id => client.call('agents.knowledge_add', [id, (await upload(true)).map(item => item.path)]),
    PickAgentKnowledgeFolder: async id => { const path = await pickServerFolder(); return path ? client.call('agents.knowledge_add', [id, [path]]) : [] },
    PickAgentRequirementsScript: async (id, os, instructions) => { const item = (await upload(false))[0]; return item ? client.call('agents.requirements_script', [id, item.path, os, instructions]) : null },
    EditorListFiles: () => file('files.tree', { path: '.' }),
    EditorReadFile: async path => { const result = await file('files.read', { path }); hashes.set(path, result.hash); return new TextDecoder('utf-8', { fatal: true }).decode(base64Bytes(result.data)) },
    EditorWriteFile: async (path, content) => { if (!hashes.has(path)) throw new Error('Reload this file before saving'); const result = await file('files.write', { path, data: bytesToBase64(new TextEncoder().encode(content)), expectedHash: hashes.get(path) }); hashes.set(path, result.hash) },
    EditorCreateFile: async path => { const result = await file('files.write', { path, data: '', create: true }); hashes.set(path, result.hash); return path },
    EditorDeleteFile: path => file('files.remove', { path }), EditorRenameFile: async (path, target) => { await file('files.rename', { path, target }); const hash=hashes.get(path); hashes.delete(path); if(hash)hashes.set(target,hash); return target },
    OpenEditorWindow: studio.openEditor,
    OpenEditorFolder: () => workspace || pickServerFolder(),
    OpenAgentKnowledgeFolder: async id => openEditor(await client.call('agents.knowledge_folder', [id])),
    OpenSkillInStudio: async (ref, digest) => openEditor(await client.call('skills.workspace', [ref, digest])),
    CreateSkillInStudio: async name => openEditor(await client.call('skills.create', [name])),
    StudioOpenProject: studio.openProject,
    StudioGetStatus: studio.status,
    SaveStudioConfig: studio.saveConfig,
    BeginVoiceCapture: () => ({native:false,id:'client-capture'}), EndVoiceCapture: () => true,
    FinishNativeVoiceCapture: () => { throw new Error('Capture microphone audio on this device before sending it for transcription') },
    InitializeDatabasePassword: () => { throw new Error('Initialize the server database from its console; use Change database password to rotate it remotely') },
    UnlockDatabase: () => ({unlocked:true}),
    SkillLibraryV2: body => client.call(['list','read','inspect','legacy-inspect','draft-read','draft-preview'].includes(JSON.parse(body).action) ? 'skills.query' : 'api.SkillLibraryV2', [body]),
    WriteTerminal: terminal.write,
    ResizeTerminal: terminal.resize,
    AcceptPrivacyNotice: () => client.identity.role === 'viewer' ? true : client.call('api.AcceptPrivacyNotice', []),
  }
  window.go = { main: { App: new Proxy({}, { get(_, name) { if (name === 'then' || typeof name !== 'string') return undefined; return (...args) => Promise.resolve(local[name] ? local[name](...args) : client.call(`api.${name}`, args)) } }) } }
  window.runtime = {
    EventsOn(name, callback) { if (!listeners.has(name)) listeners.set(name, new Set()); listeners.get(name).add(callback); return () => listeners.get(name)?.delete(callback) },
    EventsOff(name) { listeners.delete(name) },
    ClipboardSetText: text => window.praimateMobile ? window.praimateMobile.copy(text) : navigator.clipboard.writeText(text), Quit: () => window.praimateMobile ? window.praimateMobile.close() : window.close(),
  }
  window.praimateRemote = { client, status, async logout() { client.stop(); if(window.praimateMobile)await window.praimateMobile.credentials.clear();else sessionStorage.removeItem('praimate.remote.token'); location.reload() } }
  window.addEventListener('praimate:mobile-resume',()=>{client.connect().then(()=>notify('praimate:remote-resync',{})).catch(()=>{})})
  window.addEventListener('praimate:app-ready',async()=>{
    try {
      const stores=await import('./stores.js')
      await restoreRemoteRoute(`#${editor}`,stores,()=>client.call('api.ListTerminalSessions',[]))
    } catch(error) { notify('praimate:remote-warning',{message:error.message}) }
    finally { window.dispatchEvent(new Event('praimate:route-ready')) }
  },{once:true})
  client.events()
  return client
}
