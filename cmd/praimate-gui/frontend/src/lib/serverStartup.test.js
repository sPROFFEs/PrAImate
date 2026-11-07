import test from 'node:test'
import assert from 'node:assert/strict'
import {readFile} from 'node:fs/promises'
import {compile} from 'svelte/compiler'
import {confirmModal} from './stores.js'

async function controller(props,backend){
  const source=await readFile(new URL('./ServerStartup.svelte',import.meta.url),'utf8')
  const script=source.match(/<script>([\s\S]*?)<\/script>/)[1].replace('let status=null','export let status=null').replace('async function install()','export async function install()').replace('async function control(action)','export async function control(action)')
  const {js}=compile(`<script>${script}</script>`,{generate:'dom',accessors:true})
  const code=js.code.replace(/^import (.+?) from (['"])([^'"]+)\2;?$/gm,(_,bindings,quote,name)=>`import ${bindings} from ${JSON.stringify(name.startsWith('.')?new URL(name,import.meta.url).href:import.meta.resolve(name))};`).replace(/^import (['"])([^'"]+)\1;?$/gm,(_,quote,name)=>`import ${JSON.stringify(import.meta.resolve(name))};`)
  const previous=globalThis.window
  globalThis.window={go:{main:{App:backend}}}
  const {default:Component}=await import(`data:text/javascript;base64,${Buffer.from(code).toString('base64')}`)
  const component=new Component({target:{},props})
  return {component,dispose(){component.$destroy();confirmModal.set(null);if(previous===undefined)delete globalThis.window;else globalThis.window=previous}}
}
test('startup setup requires consent and clears entered credentials before invoking the backend',async()=>{
  const calls=[]
  const f=await controller({password:'fixture db password',windowsPassword:'fixture account password',consent:false,tunnel:false,status:{platform:'windows'}},{InstallServerStartup:async(...args)=>calls.push(args),ServerStartupStatus:async()=>({installed:true,enabled:true,bootReady:true})})
  try{
    await f.component.install();assert.equal(calls.length,0)
    f.component.consent=true
    const pending=f.component.install()
    assert.equal(f.component.password,'');assert.equal(f.component.windowsPassword,'')
    await pending
    assert.deepEqual(calls,[['fixture db password','fixture account password',false]])
    await f.component.install();assert.equal(calls.length,1)
  }finally{f.dispose()}
})
test('cancelled foreground handoff never starts or stops an OS service',async()=>{
  const calls=[]
  const f=await controller({}, {ServerStartupControl:async action=>calls.push(action)})
  const off=confirmModal.subscribe(dialog=>dialog?.resolve(false))
  try{await f.component.control('start');assert.equal(calls.length,0)}finally{off();f.dispose()}
})
