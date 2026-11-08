import fs from 'node:fs/promises'
import assert from 'node:assert/strict'
// Start Vite on 5187 and a fresh Firefox --headless --no-remote profile with
// --remote-debugging-port=9234 before running this optional browser suite.
const ws=new WebSocket(process.env.BIDI_URL || 'ws://127.0.0.1:9234/session')
await new Promise((r,j)=>{ws.onopen=r;ws.onerror=j})
let sequence=0;const pending=new Map(),errors=[]
ws.onmessage=e=>{const m=JSON.parse(e.data);if(m.id){const p=pending.get(m.id);pending.delete(m.id);m.type==='error'?p.reject(Error(JSON.stringify(m))):p.resolve(m.result)}else if(m.method==='log.entryAdded'&&m.params.level==='error')errors.push(m.params.text)}
const call=(method,params={})=>new Promise((resolve,reject)=>{const id=++sequence;pending.set(id,{resolve,reject});ws.send(JSON.stringify({id,method,params}))})
try{await call('session.end')}catch{}
await call('session.new',{capabilities:{alwaysMatch:{}}})
const {context}=await call('browsingContext.create',{type:'tab'})
const evaluate=async expression=>{const r=await call('script.evaluate',{expression:`(async()=>JSON.stringify(await (${expression})))()`,target:{context},awaitPromise:true});if(r.type==='exception')throw Error(r.exceptionDetails.text);return r.result.type==='undefined'?undefined:JSON.parse(r.result.value)}
const wait=ms=>new Promise(r=>setTimeout(r,ms));const until=async expression=>{for(let i=0;i<200;i++){if(await evaluate(expression))return;await wait(100)}throw Error('State missing: '+expression)}
await call('session.subscribe',{events:['log.entryAdded']})

const base=process.env.MOBILE_TEST_URL || 'http://127.0.0.1:5187/tests/fixtures/mobile.html'
const evidence=process.env.MOBILE_TEST_OUTPUT || '/tmp/praimate-mobile-layout-evidence'
await fs.mkdir(evidence,{recursive:true});await fs.rm(`${evidence}/failure.png`,{force:true})
const settle=()=>evaluate('new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))')
const screenshot=async name=>{const result=await call('browsingContext.captureScreenshot',{context,origin:'viewport'});await fs.writeFile(`${evidence}/${name}.png`,Buffer.from(result.data,'base64'))}
const layout=()=>evaluate(`(()=>{const m=document.querySelector('.main');return {width:innerWidth,height:innerHeight,main:m?{width:m.clientWidth,scroll:m.scrollWidth}:null,document:document.documentElement.scrollWidth}})()`)
const navigate=async page=>{
 await evaluate(`window.mobileFixture.navigate(${JSON.stringify(page)})`)
 await until(`document.querySelector('.main')?.dataset.page===${JSON.stringify(page)} && !document.querySelector('.main')?.innerText.startsWith('Opening ')`)
 await settle()
 const size=await layout()
 assert.ok(size.document<=size.width+1,`${page}: document overflow ${JSON.stringify(size)}`)
 assert.ok(size.main.scroll<=size.main.width+1,`${page}: page overflow ${JSON.stringify(size)}`)
}
const inside=async (selector,vertical=true)=>{
 const bounds=await evaluate(`(()=>{const r=document.querySelector(${JSON.stringify(selector)})?.getBoundingClientRect();return r?{top:r.top,bottom:r.bottom,left:r.left,right:r.right,width:innerWidth,height:innerHeight}:null})()`)
 assert.ok(bounds,`Missing ${selector}`)
 assert.ok(bounds.left>=-1&&bounds.right<=bounds.width+1&&(!vertical || bounds.top>=-1&&bounds.bottom<=bounds.height+1),`${selector} outside viewport: ${JSON.stringify(bounds)}`)
 return bounds
}
let checks=0
try{
 await call('browsingContext.activate',{context})
 await call('browsingContext.setViewport',{context,viewport:{width:390,height:844},devicePixelRatio:1})
 await call('browsingContext.navigate',{context,url:base,wait:'complete'})
 await until('!!window.mobileFixture && !!document.querySelector(".main")')
 for(const [width,height] of (process.env.MOBILE_TEST_SCOPE==='workspaces'?[]:[[320,740],[390,844],[844,390],[1024,768]])){
  await call('browsingContext.setViewport',{context,viewport:{width,height},devicePixelRatio:1})
  for(const theme of ['dark','light']){
   await evaluate(`document.documentElement.classList.toggle('light',${theme==='light'})`)
   for(const page of ['dashboard','chats','code','workers','agents','skills','mcp','clis','localllm','settings','server','studio','documents','about']){await navigate(page);checks++}
   await navigate('workers');await until("!!document.querySelector('.board')")
   await inside('.board',false)
   assert.equal(await evaluate("[...document.querySelectorAll('.board .column')].filter(e=>e.getClientRects().length).length"),1)
   await evaluate("(()=>{const s=document.querySelector('[aria-label=\"Task group\"]');s.value='done';s.dispatchEvent(new Event('change',{bubbles:true}))})()")
   await settle()
   assert.equal(await evaluate("document.querySelector('.board .column.done').getClientRects().length>0"),true)
   await evaluate("[...document.querySelectorAll('button')].find(b=>b.textContent.trim()==='New worker chat').click()")
   await until("!!document.querySelector('.create-dialog')")
   await inside('.create-dialog')
   assert.equal(await evaluate('window.praimateMobile.onBack()'),true)
   await until("!document.querySelector('.create-dialog')")
   await evaluate('window.mobileFixture.openChat()')
   await until("!!document.querySelector('.chat-view .thread .msg')")
   await settle()
   const composer=await inside('.chat-view>.composer')
   const textarea=await evaluate("document.querySelector('.chat-view textarea').getBoundingClientRect().width")
   assert.ok(textarea>=composer.right-composer.left-2,`Composer too narrow: ${textarea}/${width}`)
   await screenshot(`chat-${width}-${theme}`)
   await evaluate("document.querySelector('.chat-view textarea').focus()")
   await call('browsingContext.setViewport',{context,viewport:{width,height:Math.min(420,height-160)},devicePixelRatio:1})
   await until("!document.querySelector('.mobile-bottom-nav')");await settle();await inside('.chat-view>.composer')
   assert.equal(await evaluate("document.querySelector('.chat-view textarea').dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true,cancelable:true}))"),true)
   await evaluate("document.querySelector('.chat-view textarea').blur()")
   await call('browsingContext.setViewport',{context,viewport:{width,height},devicePixelRatio:1})
   await until("!!document.querySelector('.mobile-toolbar')")
   assert.equal(await evaluate("!!document.querySelector('.mobile-bottom-nav')"),height>=500)
   await evaluate("document.querySelector('[aria-label=\"Open navigation\"]').click()")
   await until("!!document.querySelector('.mobile-navigation')")
   await evaluate("document.querySelector('.mobile-navigation').getAnimations().forEach(animation=>animation.finish())");await settle();await inside('.mobile-navigation')
   assert.equal(await evaluate("document.querySelectorAll('.mobile-nav-item').length"),14)
   assert.equal(await evaluate('window.praimateMobile.onBack()'),true)
   await until("!document.querySelector('.mobile-navigation')")
   await evaluate("document.querySelector('.remote-connection .connection').click()")
   await until("!!document.querySelector('.remote-connection [role=dialog]')")
   await inside('.remote-connection [role=dialog]')
   assert.equal(await evaluate('window.praimateMobile.onBack()'),true)
   await until("!document.querySelector('.remote-connection [role=dialog]')")
   await evaluate("document.querySelector('.mobile-assistant').click()")
   await until("!!document.querySelector('.assistant-panel')");await settle();await inside('.assistant-panel')
   assert.equal(await evaluate('window.praimateMobile.onBack()'),true)
   await until("!document.querySelector('.assistant-panel')")
   await evaluate("document.querySelector('.mobile-assistant').click()")
   await until("!!document.querySelector('.assistant-panel')")
   await evaluate("document.querySelector('.remote-connection .connection').click()")
   await until("!!document.querySelector('.remote-connection [role=dialog]') && !document.querySelector('.assistant-panel')")
   assert.equal(await evaluate('window.praimateMobile.onBack()'),true)
   await until("!document.querySelector('.remote-connection [role=dialog]')")
   assert.deepEqual(errors,[])
  }
 }
 // Use real CodeMirror and xterm renderers with synthetic backend responses.
 for(const [width,height] of [[320,740],[844,390],[1024,768]]){
  await call('browsingContext.setViewport',{context,viewport:{width,height},devicePixelRatio:1})
  await call('browsingContext.navigate',{context,url:base,wait:'complete'})
  await until('!!window.mobileFixture && !!document.querySelector(".main")')
  await evaluate("(()=>{window.mobileFixture.stores.pendingTerm.set({termId:'terminal-fixture',cli:'codex',cwd:'/projects/mobile-review'});window.mobileFixture.navigate('code')})()")
  await until("!!document.querySelector('.xterm-screen') && !!document.querySelector('.terminal-dock')")
  await settle()
  const terminal=await inside('.termhost'),dock=await inside('.terminal-dock')
  assert.ok(terminal.bottom<=dock.top+1,`Terminal covered by controls: ${JSON.stringify({terminal,dock})}`)
  assert.equal(await evaluate("!!document.querySelector('.mobile-bottom-nav')"),false)
  await evaluate("(()=>{const input=document.querySelector('.terminal-composer textarea');input.value='echo móvil';input.dispatchEvent(new Event('input',{bubbles:true}))})()")
  await settle();await evaluate("document.querySelector('[aria-label=\"Send terminal input\"]').click()")
  await until("window.mobileFixture.calls.some(c=>c.name==='WriteTerminal')")
  assert.equal(await evaluate("decodeURIComponent(escape(atob(window.mobileFixture.calls.find(c=>c.name==='WriteTerminal').args[1])))"),'echo móvil\r')
  await screenshot(`terminal-${width}`)
  await evaluate("document.querySelector('.terminal-composer textarea').focus()")
  await call('browsingContext.setViewport',{context,viewport:{width,height:Math.min(420,height-160)},devicePixelRatio:1})
  await settle();const keyboardDock=await inside('.terminal-dock'),keyboardTerminal=await inside('.termhost');await inside('.terminal-composer textarea');assert.ok(keyboardTerminal.bottom<=keyboardDock.top+1,`Keyboard covers terminal output: ${JSON.stringify({keyboardTerminal,keyboardDock})}`)
  await evaluate("document.querySelector('.terminal-composer textarea').blur()")
  await call('browsingContext.setViewport',{context,viewport:{width,height},devicePixelRatio:1})
  await navigate('dashboard')
  assert.equal(await evaluate("!!document.querySelector('.terminal-dock')"),false)
  await evaluate('window.mobileFixture.agent()')
  await until("!!document.querySelector('.mobile-agent .cm-editor')")
  for(const pane of ['Files','Editor','Assistant']){
   await evaluate(`[...document.querySelectorAll('.mobile-agent-tabs button')].find(b=>b.textContent===${JSON.stringify(pane)}).click()`)
   await settle();await inside('.mobile-agent');await inside('.mobile-agent-tabs');if(pane==='Assistant'){
    await inside('.mobile-agent .composer')
    await evaluate("document.querySelector('.mobile-agent .composer textarea').focus()")
    await call('browsingContext.setViewport',{context,viewport:{width,height:Math.min(420,height-160)},devicePixelRatio:1})
    await settle();await inside('.mobile-agent .composer');assert.ok((await evaluate("document.querySelector('.mobile-agent .thread').clientHeight"))>20,'Agent transcript hidden by keyboard');await screenshot(`agent-keyboard-${width}`)
    await evaluate("document.querySelector('.mobile-agent .composer textarea').blur()")
    await call('browsingContext.setViewport',{context,viewport:{width,height},devicePixelRatio:1})
   }
   if(pane==='Editor'){await inside('.mobile-agent .center .cm-editor');await screenshot(`agent-editor-${width}`)}
   assert.ok((await evaluate('document.documentElement.scrollWidth'))<=width+1,`Agent ${pane} overflow`)
  }
  await screenshot(`agent-${width}`)
  await evaluate("document.querySelector('.mobile-assistant').click()")
  await until("!!document.querySelector('.assistant-panel')")
  await evaluate("document.querySelector('[title=\"Assistant settings\"]').click()")
  await until("!!document.querySelector('.main[data-page=settings]') && !document.querySelector('.mobile-agent') && !document.querySelector('.assistant-panel')")
  await call('browsingContext.navigate',{context,url:base+'?view=editor#workspace=fixture',wait:'complete'})
  await until("!!document.querySelector('.mobile-studio .tree-item[title^=\"README.md\"]')")
  await evaluate("(()=>{[...document.querySelectorAll('.mobile-studio-tabs button')].find(b=>b.textContent==='Files').click();document.querySelector('.mobile-studio .tree-item[title^=\"README.md\"]').click()})()")
  await until("!!document.querySelector('.mobile-studio .cm-editor')")
  for(const pane of ['Files','Editor','Chat']){
   await evaluate(`[...document.querySelectorAll('.mobile-studio-tabs button')].find(b=>b.textContent===${JSON.stringify(pane)}).click()`)
   await settle();await inside('.mobile-studio');await inside('.mobile-studio-tabs');if(pane==='Chat'){
    await inside('.mobile-studio .composer')
    await evaluate("document.querySelector('.mobile-studio .composer textarea').focus()")
    await call('browsingContext.setViewport',{context,viewport:{width,height:Math.min(420,height-160)},devicePixelRatio:1})
    await settle();await inside('.mobile-studio .composer');assert.ok((await evaluate("document.querySelector('.mobile-studio .thread').clientHeight"))>20,'Studio transcript hidden by keyboard');await screenshot(`studio-keyboard-${width}`)
    await evaluate("document.querySelector('.mobile-studio .composer textarea').blur()")
    await call('browsingContext.setViewport',{context,viewport:{width,height},devicePixelRatio:1})
   }
   if(pane==='Editor'){await inside('.mobile-studio .editor-col .cm-editor');await screenshot(`studio-editor-${width}`)}
   assert.ok((await evaluate('document.documentElement.scrollWidth'))<=width+1,`Studio ${pane} overflow`)
  }
  await screenshot(`studio-${width}`)
  assert.deepEqual(errors,[])
 }
 if(checks)console.log(`PASS: ${checks} page/viewport/theme cases; full-width conversation inputs; keyboard-sized viewports; worker task filters and settings; navigation and connection Back dismissal; live terminal input and Studio/agent panels at 3 sizes.`)
 console.log(`PASS: live terminal UTF-8 input and Studio/agent panels at 3 sizes. Screenshots: ${evidence}`)
}catch(error){await screenshot('failure');console.error('Browser errors:',errors);console.error('Current state:',await evaluate(`({text:document.body.innerText.slice(-800),calls:window.mobileFixture?.calls.slice(-4),overflow:[...document.querySelectorAll('.main *')].filter(e=>e.getClientRects().length&&e.getBoundingClientRect().right>innerWidth).map(e=>({tag:e.tagName,cls:String(e.className),text:e.textContent.slice(0,45),parent:e.parentElement.className,parentWidth:e.parentElement.getBoundingClientRect().width,parentOverflow:getComputedStyle(e.parentElement).overflowX,rootApp:!!e.closest('#app'),right:e.getBoundingClientRect().right,width:e.getBoundingClientRect().width})).sort((a,b)=>b.right-a.right).slice(0,8)})`));throw error}
finally{try{await call('browsingContext.close',{context})}finally{await call('session.end');ws.close()}}
