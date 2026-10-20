import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { compile } from 'svelte/compiler'
import { tick } from 'svelte'

const source = await readFile(new URL('./Editor.svelte', import.meta.url), 'utf8')
let script = source.match(/<script>([\s\S]*?)<\/script>/)[1]
  .replace(/^  import .* from '[^']+\.svelte'\n/gm, '')
for (const name of ['files','tabs','active','treeLoading','cfg','error','fileSyncError']) script = script.replace(`let ${name} =`, `export let ${name} =`)
for (const name of ['loadTree','open','onFsEvent','flush','saveConfig']) script = script.replace(new RegExp(`(async )?function ${name}\\(`), (_,async='') => `export ${async}function ${name}(`)
const { js } = compile(`<script>${script}</script>`, { generate:'dom', accessors:true })
const code = js.code.replace(/(["'])(svelte(?:\/[^"']+)?|\.\.\/lib\/[^"']+)\1/g, (_,quote,name) => JSON.stringify(name.startsWith('svelte') ? import.meta.resolve(name) : new URL(name,import.meta.url).href))
const { default: Editor } = await import('data:text/javascript;base64,'+Buffer.from(code).toString('base64'))

function fixture(t, methods = {}) {
  const previousWindow = globalThis.window, previousDocument = globalThis.document
  const calls = []
  let chat = { ID:'doc', WorkspacePath:'/old', CLIAgent:'praimate-cli', Settings:{} }
  const api = {
    EditorListFiles:async()=>['file.md'], EditorReadFile:async()=> 'agent text',
    EditorWriteFile:async(...args)=>calls.push(['write',...args]),
    SaveStudioConfig:async(...args)=>{calls.push(['save',...args]);chat={...chat,WorkspacePath:args.at(-1)}},
    ChatMessages:async()=>[], ListChats:async()=>[chat],
    ...methods,
  }
  globalThis.window = {addEventListener(){},removeEventListener(){},go:{main:{App:api}}}
  globalThis.document = {title:''}
  const editor = new Editor({target:{},props:{folder:'/old',chatId:'doc'}})
  t.after(()=>{editor.$destroy();globalThis.window=previousWindow;globalThis.document=previousDocument})
  return {editor,calls}
}

test('initial snapshot and watcher rescan load files without a manual refresh', async t=>{
  let count=0
  const {editor}=fixture(t,{EditorListFiles:async()=>count++ ? ['file.md'] : []})
  assert.equal(editor.treeLoading,true)
  await editor.loadTree()
  await editor.onFsEvent({path:'',op:'RESCAN',folder:'/old'})
  assert.deepEqual(editor.files,['file.md'])
  assert.equal(editor.treeLoading,false)
})

test('external edits refresh a clean open tab and preserve a user draft',async t=>{
  const {editor}=fixture(t)
  let external=''
  editor.$set({tabs:[{path:'file.md',content:'old',dirty:false,ref:{setExternal:value=>{external=value}}}]})
  await editor.onFsEvent({path:'file.md',folder:'/old'})
  assert.equal(editor.tabs[0].content,'agent text')
  assert.equal(external,'agent text')
  editor.tabs[0].content='unsaved draft';editor.tabs[0].dirty=true
  await editor.onFsEvent({path:'file.md',folder:'/old'})
  assert.equal(editor.tabs[0].content,'unsaved draft')
  assert.equal(editor.tabs[0].externalPending,true)
  assert.equal(editor.tabs[0].freshContent,'agent text')
})

test('changing folder saves drafts first, resets old tabs and uses the new root',async t=>{
  const {editor,calls}=fixture(t)
  editor.$set({tabs:[{path:'file.md',content:'draft',dirty:true}],cfg:{workspacePath:'/new',name:'Doc',cli:'praimate-cli',model:'',tools:'full',localEndpoint:'',localModel:'',mcps:[]}})
  await editor.saveConfig()
  assert.equal(calls[0][0],'write')
  assert.equal(calls[1][0],'save')
  assert.equal(editor.folder,'/new')
  assert.deepEqual(editor.tabs,[])
})

test('a failed draft write prevents workspace changes',async t=>{
  const {editor,calls}=fixture(t,{EditorWriteFile:async()=>{throw Error('disk full')}})
  editor.$set({tabs:[{path:'file.md',content:'draft',dirty:true}],cfg:{workspacePath:'/new'}})
  await editor.saveConfig()
  assert.equal(calls.length,0)
  assert.equal(editor.folder,'/old')
  assert.equal(editor.tabs[0].dirty,true)
})

test('saving a snapshot does not mark newer keystrokes as saved',async t=>{
  let complete
  const {editor}=fixture(t,{EditorWriteFile:()=>new Promise(resolve=>{complete=resolve})})
  const tab={path:'file.md',content:'first',dirty:true}
  const pending=editor.flush(tab)
  await tick()
  tab.content='newer'
  complete()
  await pending
  assert.equal(tab.dirty,true)
})

test('events from a previous workspace cannot overwrite the current tab',async t=>{
  let reads=0
  const {editor}=fixture(t,{EditorReadFile:async()=>{reads++;return 'wrong'}})
  editor.$set({folder:'/new',tabs:[{path:'file.md',content:'new workspace',dirty:false}]})
  await editor.onFsEvent({path:'file.md',folder:'/old'})
  assert.equal(reads,0)
  assert.equal(editor.tabs[0].content,'new workspace')
})
