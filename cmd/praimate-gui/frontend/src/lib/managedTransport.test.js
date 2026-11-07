import test from 'node:test'
import assert from 'node:assert/strict'
import { managedFetch } from './managedTransport.js'

function fixture() {
  const listeners=new Map(),writes=[],begins=[],cancelled=[]
  let ack=0
  const native={BeginManagedRequest(...args){begins.push(args)},WriteManagedRequest(...args){writes.push(args)},AckManagedRequest(){ack++},CancelManagedRequest(id){cancelled.push(id)}}
  const runtime={EventsOn(name,fn){listeners.set(name,fn);return()=>listeners.delete(name)}}
  const emit=event=>listeners.values().next().value?.(event)
  const fetch=managedFetch(native,runtime,'http://127.0.0.1:8844',()=> 'a'.repeat(32))
  return {native,runtime,fetch,emit,listeners,writes,begins,cancelled,acks:()=>ack}
}
const tick=()=>new Promise(resolve=>setImmediate(resolve))
test('local transport streams response bytes with backpressure and no owner credential in JS',async()=>{
  const f=fixture(),pending=f.fetch('http://127.0.0.1:8844/v1/status')
  f.emit({status:200,headers:{'Content-Type':'application/json'}})
  f.emit({data:btoa('{"ok":true}')})
  const response=await pending
  assert.equal(f.acks(),0)
  const result=response.json();await tick()
  assert.equal(f.acks(),1);f.emit({done:true})
  assert.deepEqual(await result,{ok:true});assert.equal(f.listeners.size,0)
  assert.equal(f.begins[0][1],'/v1/status')
})
test('uploads are chunked in order and never replayed after a failure',async()=>{
  const f=fixture(),pending=f.fetch('http://127.0.0.1:8844/v1/upload?name=test',{method:'PUT',body:new Blob(['ñ'.repeat(70000)])})
  await tick();await tick()
  const data=f.writes.filter(row=>!row[2]).map(row=>Uint8Array.from(atob(row[1]),ch=>ch.charCodeAt(0)))
  assert.ok(data.length>1);assert.ok(data.every(chunk=>chunk.length<=48*1024))
  assert.equal(new TextDecoder().decode(Buffer.concat(data)),'ñ'.repeat(70000))
  assert.equal(f.writes.at(-1)[2],true)
  f.emit({error:'Connection interrupted'})
  await assert.rejects(pending,/interrupted/)
  assert.equal(f.begins.length,1);assert.equal(f.cancelled.length,1)
})
test('aborting a local SSE request closes its native stream and removes callbacks',async()=>{
  const f=fixture(),abort=new AbortController(),pending=f.fetch('http://127.0.0.1:8844/v1/events',{signal:abort.signal})
  f.emit({status:200,headers:{'Content-Type':'text/event-stream'}})
  const response=await pending,reader=response.body.getReader(),waiting=reader.read()
  abort.abort();await assert.rejects(waiting,{name:'AbortError'})
  assert.equal(f.listeners.size,0);assert.ok(f.cancelled.length>=1)
})
test('local transport rejects external and non-API requests before invoking Wails',async()=>{
  const f=fixture()
  for(const url of ['https://example.com/v1/status','http://127.0.0.1:8844/elsewhere'])await assert.rejects(f.fetch(url),/only accepts/)
  assert.equal(f.begins.length,0)
})

test('an early HTTP rejection keeps the server diagnostic when its upload pipe closes',async()=>{
  const f=fixture()
  f.native.WriteManagedRequest=async()=>{throw new Error('Upload pipe closed')}
  const pending=f.fetch('http://127.0.0.1:8844/v1/upload',{method:'PUT',body:'fixture'})
  f.emit({status:403,headers:{'Content-Type':'application/json'}})
  f.emit({data:btoa('{"error":{"message":"Access denied by server"}}')})
  const response=await pending,result=response.json();await tick();f.emit({done:true})
  assert.equal(response.status,403)
  assert.equal((await result).error.message,'Access denied by server')
})
