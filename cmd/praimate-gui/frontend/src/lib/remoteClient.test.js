import test from 'node:test'
import assert from 'node:assert/strict'
import { RemoteClient } from './remoteClient.js'

test('browser fetch keeps the global receiver when used by the remote client', async t => {
  const mock=t.mock.method(globalThis,'fetch',async function () {
    assert.equal(this,globalThis,'native Window.fetch rejects a RemoteClient receiver')
    return new Response(JSON.stringify({connected:true}),{headers:{'Content-Type':'application/json'}})
  })
  const client=new RemoteClient({baseURL:'https://server.example',token:'fixture-token'})
  assert.deepEqual(await client.request('/v1/status'),{connected:true})
  await client.fetch('https://server.example/v1/download')
  assert.equal(mock.mock.callCount(),2)
})

test('an Access redirect never follows or repeats a mutation', async()=>{
  let requests=0, cancelled=false
  const client=new RemoteClient({baseURL:'https://server.example',token:'fixture-token',fetchImpl:async(url,options)=>{
    requests++;assert.equal(options.redirect,'manual')
    return {type:'opaqueredirect',status:0,body:{cancel:async()=>{cancelled=true}}}
  }})
  client.methods.set('api.Create',{mutation:true})
  await assert.rejects(client.call('api.Create',[]),error=>error.code==='access_required'&&/sign in/i.test(error.message))
  assert.equal(requests,1);assert.equal(cancelled,true)
})

test('a Cloudflare HTML challenge is reported separately from PrAImate pairing', async()=>{
  const client=new RemoteClient({baseURL:'https://server.example',token:'fixture-token',fetchImpl:async()=>new Response('<html>Access</html>',{status:403,headers:{'Content-Type':'text/html','CF-Ray':'fixture'}})})
  await assert.rejects(client.request('/v1/status'),error=>error.code==='access_required')
})

test('a lost mutation response is recovered without another POST', async () => {
  let posts = 0, operationID = ''
  const client = new RemoteClient({ baseURL:'https://server.example',token:'fixture-token',fetchImpl: async (url, options) => {
    if (options.method === 'POST') { posts++; operationID=JSON.parse(options.body).requestId; throw new TypeError('connection lost after acceptance') }
    assert.equal(url, `https://server.example/v1/operations/${operationID}`)
    return {ok:true,json:async()=>({id:operationID,status:'completed',result:'created'})}
  } })
  client.methods.set('api.Create', {mutation:true})
  assert.equal(await client.call('api.Create', ['example']), 'created')
  assert.equal(posts,1)
  assert.match(operationID,/^[0-9]{13}_[A-Za-z0-9_-]{8,64}$/)
})

test('an explicit permission error never retries a mutation', async () => {
  let count=0
  const client=new RemoteClient({baseURL:'https://server.example',token:'fixture-token',fetchImpl:async()=>{count++;return {ok:false,status:403,json:async()=>({error:{code:'forbidden',message:'Device lacks permission'}})}}})
  client.methods.set('api.Create',{mutation:true})
  await assert.rejects(client.call('api.Create',[]),/permission/)
  assert.equal(count,1)
})

test('a non-JSON authentication failure retains its HTTP status', async () => {
  let count = 0
  const client = new RemoteClient({baseURL:'https://server.example', token:'fixture-token', fetchImpl:async(url, options) => {
    count++
    assert.match(options.headers['X-Praimate-Client'], /^[A-Za-z0-9_-]{1,80}$/)
    return {ok:false,status:401,json:async()=>{throw new SyntaxError('plain text response')}}
  }})
  client.methods.set('api.Create',{mutation:true})
  await assert.rejects(client.call('api.Create', []), error => error.status === 401)
  assert.equal(count, 1)
})

test('split SSE frames retain cursors and do not place credentials in URLs', async () => {
  const encoder=new TextEncoder(),events=[]
  let client,index=0
  const chunks=['data: {"id":"epoch:1","name":"term:', 'data:term-1","payload":{"data":"YQ=="}}\n', '\n']
  client=new RemoteClient({baseURL:'https://server.example',token:'fixture-token',fetchImpl:async(url,options)=>{
    assert.equal(url,'https://server.example/v1/events')
    assert.equal(options.headers.Authorization,'Bearer fixture-token')
    return {ok:true,body:{getReader:()=>({read:async()=>index<chunks.length?{value:encoder.encode(chunks[index++]),done:false}:{done:true}})}}
  },onEvent:event=>{events.push(event);client.stop()}})
  await client.events()
  assert.equal(events.length,1)
  assert.equal(client.cursor,'epoch:1')
  assert.equal(events[0].payload.data,'YQ==')
})
