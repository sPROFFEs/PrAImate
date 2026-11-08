import test from 'node:test'
import assert from 'node:assert/strict'
import { observeMobileViewport } from './mobileViewport.js'

function fixture() {
  const win=new EventTarget(),document=new EventTarget(),viewport=new EventTarget()
  Object.assign(viewport,{height:844,offsetTop:0})
  Object.assign(document,{activeElement:{tagName:'BODY'}})
  let next=0;const frames=new Map(),styles=new Map(),states=[]
  Object.assign(win,{document,visualViewport:viewport,innerHeight:900,requestAnimationFrame(callback){frames.set(++next,callback);return next},cancelAnimationFrame(id){frames.delete(id)}})
  const root={style:{setProperty:(key,value)=>styles.set(key,value),removeProperty:key=>styles.delete(key)}}
  const stop=observeMobileViewport(win,root,state=>states.push(state))
  return {win,document,viewport,styles,states,frames,stop,flush(){const pending=[...frames.values()];frames.clear();pending.forEach(callback=>callback())}}
}

test('mobile viewport follows the keyboard and pans without queuing duplicate measurements',()=>{
  const f=fixture()
  assert.equal(f.styles.get('--app-height'),'844px')
  f.document.activeElement={tagName:'TEXTAREA'}
  f.document.dispatchEvent(new Event('focusin'))
  Object.assign(f.viewport,{height:420,offsetTop:32})
  f.viewport.dispatchEvent(new Event('resize'));f.viewport.dispatchEvent(new Event('scroll'))
  assert.equal(f.frames.size,1)
  f.flush()
  assert.equal(f.styles.get('--app-height'),'420px');assert.equal(f.styles.get('--app-top'),'32px')
  assert.deepEqual(f.states.at(-1),{editing:true,keyboard:true,height:420})
  f.document.activeElement={tagName:'INPUT',type:'checkbox'}
  f.document.dispatchEvent(new Event('focusout'));f.flush()
  assert.deepEqual(f.states.at(-1),{editing:false,keyboard:false,height:420})
  f.stop();assert.equal(f.styles.size,0)
  f.viewport.dispatchEvent(new Event('resize'));assert.equal(f.frames.size,0)
})

test('orientation changes and WebViews without visualViewport use the current window height',()=>{
  const f=fixture();f.win.visualViewport=undefined;f.win.innerHeight=390
  f.document.activeElement={tagName:'DIV',isContentEditable:true}
  f.win.dispatchEvent(new Event('orientationchange'));f.flush()
  assert.equal(f.styles.get('--app-height'),'390px');assert.equal(f.styles.get('--app-top'),'0px')
  assert.deepEqual(f.states.at(-1),{editing:true,keyboard:false,height:390});f.stop()
})
