import '../../src/styles.css'
import WorkerGraph from '../../src/lib/WorkerGraph.svelte'
import WorkerMonitor from '../../src/lib/WorkerMonitor.svelte'
import Documents from '../../src/pages/Documents.svelte'
import Studio from '../../src/pages/Studio.svelte'
import Chats from '../../src/pages/Chats.svelte'
import Editor from '../../src/pages/Editor.svelte'
import { openChatId } from '../../src/lib/stores.js'

const calls=[]
const data={ListChats:[],ListAgents:[],ListCLIs:[{id:'codex',available:true}],LocalLLMModels:{configured:false},ListCLIModels:[],MCPServers:[{id:'docs-id',name:'Documentation endpoint',enabled:true},{id:'disabled',name:'Disabled endpoint',enabled:false}],PreflightExecution:{ok:true,issues:[]},PrepareStudioChat:'created-chat',PickFolder:'/project'}
Object.assign(data,{StudioGetStatus:{state:'installed',version:'1.3.6',binaryPath:'/editor/codium'},StudioListRecentProjects:['/project'],StudioGetEditorPreferences:{extensionsDirectory:'',managedDirectory:'/managed/extensions'}})
window.go={main:{App:new Proxy({},{get(_,method){return async(...args)=>{calls.push({method,args});if(method==='StudioSaveEditorPreferences'){data.StudioGetEditorPreferences.extensionsDirectory=args[0];return structuredClone(data.StudioGetEditorPreferences)}if(typeof data[method]==='function')return data[method](...args);return method in data?structuredClone(data[method]):[]}}})}}
const listeners = new Map()
window.runtime = {
  EventsOn(name, handler) {
    if (!listeners.has(name)) listeners.set(name, new Set())
    listeners.get(name).add(handler)
    return () => listeners.get(name)?.delete(handler)
  },
  EventsOff(name) { listeners.delete(name) },
}
const snapshot={id:'review-fixture',task:'Review a saved task graph',status:'draft',profiles:[{tier:'middle',runtime:'cli',cli:'codex'}],events:[],dag:{targetBranch:'main',baseCommit:'abc',maxParallel:2,tasks:[{id:'one',description:'Apply the requested change',worker:{profile:'middle'},dependencies:[],status:'pending'}]}}
let component
window.issueReview = {
  calls, data,
  selectChat: id => openChatId.set(id),
  emit: (name, data) => listeners.get(name)?.forEach(handler => handler(data)),
  show(mode, customSnapshot = snapshot) {
    component?.$destroy()
    const target = document.getElementById('app')
    target.replaceChildren()
    if (mode === 'chats' && !customSnapshot.keepSelection) openChatId.set(data.ListChats[0]?.ID || null)
    const Component = { chats: Chats, editor: Editor, graph: WorkerGraph, monitor: WorkerMonitor, documents: Documents, studio: Studio }[mode]
    const props = mode === 'editor' ? customSnapshot : ['graph', 'monitor'].includes(mode) ? { snapshot: customSnapshot, clis: data.ListCLIs } : {}
    component = new Component({ target, props })
  },
}
window.issueReview.show('graph')
