import test from 'node:test'
import assert from 'node:assert/strict'
import { AndroidTransport } from './androidTransport.js'

test('native API responses stream bytes and acknowledge consumption', async()=>{
  const sent=[],port={postMessage:raw=>sent.push(JSON.parse(raw))}
  const transport=new AndroidTransport(port)
  globalThis.location={origin:'https://appassets.androidplatform.net'}
  const promise=transport.fetch('/v1/status',{headers:{Authorization:'Bearer android-keystore-session'}})
  const id=sent[0].id
  transport.receive({id,type:'headers',status:200,headers:{'Content-Type':'application/json'}})
  const response=await promise
  transport.receive({id,type:'data',data:btoa('{"protocol":2}')})
  const result=response.json()
  await Promise.resolve()
  assert.ok(sent.some(item=>item.action==='ack'))
  transport.receive({id,type:'done'})
  assert.deepEqual(await result,{protocol:2})
  assert.equal(transport.pending.size,0)
})

test('native transport rejects external URLs and cancels disconnected streams',async()=>{
  const sent=[],transport=new AndroidTransport({postMessage:raw=>sent.push(JSON.parse(raw))})
  globalThis.location={origin:'https://appassets.androidplatform.net'}
  await assert.rejects(transport.fetch('https://other.example/v1/status'),/Only PrAImate/)
  const controller=new AbortController(),pending=transport.fetch('/v1/events',{signal:controller.signal})
  controller.abort()
  await assert.rejects(pending,error=>error.name==='AbortError')
  assert.ok(sent.some(item=>item.action==='cancel'))
  assert.equal(transport.pending.size,0)
})

test('native action replies resolve their matching pending request',async()=>{
  const sent=[],transport=new AndroidTransport({postMessage:raw=>sent.push(JSON.parse(raw))})
  const pending=transport.request('status')
  transport.receive({id:sent[0].id,type:'result',result:{paired:true,serverURL:'https://server.example'}})
  assert.deepEqual(await pending,{paired:true,serverURL:'https://server.example'})
})

test('request bursts queue below the native limit and cancellation releases a slot',async()=>{
  globalThis.location={origin:'https://appassets.androidplatform.net'}
  const sent=[],transport=new AndroidTransport({postMessage:raw=>sent.push(JSON.parse(raw))},{maxConcurrent:2})
  const abort=new AbortController()
  const requests=Array.from({length:20},(_,index)=>transport.fetch('/v1/status',index===0?{signal:abort.signal}:{}))
  const results=Promise.all(requests.map(request=>request.catch(error=>error)))
  assert.equal(sent.filter(item=>item.action==='http').length,2)
  abort.abort()
  assert.equal(sent.filter(item=>item.action==='http').length,3)
  for(const id of [...transport.pending.keys()])transport.receive({id,type:'error',message:'fixture complete'})
  await results
  assert.equal(transport.pending.size,0)
  assert.equal(transport.active,0)
  assert.equal(sent.filter(item=>item.action==='http').length,20)
})

test('cancelling a queued request never sends it to the CLI/server',async()=>{
  const sent=[],transport=new AndroidTransport({postMessage:raw=>sent.push(JSON.parse(raw))},{maxConcurrent:1})
  const first=transport.fetch('/v1/events').catch(error=>error)
  const controller=new AbortController(),second=transport.fetch('/v1/call',{method:'POST',body:'command',signal:controller.signal})
  controller.abort()
  await assert.rejects(second,error=>error.name==='AbortError')
  transport.receive({id:sent[0].id,type:'error',message:'fixture complete'})
  await first
  assert.equal(sent.filter(item=>item.action==='http').length,1)
  assert.equal(transport.pending.size,0)
})

test('an unresponsive native bootstrap fails visibly rather than waiting forever',async()=>{
  const transport=new AndroidTransport({postMessage:()=>{}})
  await assert.rejects(transport.request('status',{}, {timeoutMs:10}),/Android did not respond/)
  assert.equal(transport.pending.size,0)
})

test('reloaded pages cannot consume replies belonging to an older page',async()=>{
  const sent=[],old=new AndroidTransport({postMessage:raw=>sent.push(JSON.parse(raw))}),fresh=new AndroidTransport({postMessage:raw=>sent.push(JSON.parse(raw))})
  const previous=old.request('status'),current=fresh.request('status')
  assert.notEqual(sent[0].id,sent[1].id)
  fresh.receive({id:sent[0].id,type:'result',result:'wrong page'})
  assert.equal(fresh.pending.size,1)
  old.receive({id:sent[0].id,type:'result',result:'old page'})
  fresh.receive({id:sent[1].id,type:'result',result:'current page'})
  assert.equal(await previous,'old page');assert.equal(await current,'current page')
})
