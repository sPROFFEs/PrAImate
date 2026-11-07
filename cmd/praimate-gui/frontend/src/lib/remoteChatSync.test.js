import test from 'node:test'
import assert from 'node:assert/strict'
import { createRemoteChatSync } from './remoteChatSync.js'

function fixture(extra={}){
  const snapshots=[],errors=[]
  const options={api:{remoteActivity:async()=>({chats:['chat'],approvals:[{id:'own',chatId:'chat'},{id:'other',chatId:'other'}]}),chatMessages:async()=>[{Role:'user',Content:'work'}]},chatID:()=> 'chat',ownsTurn:()=>false,role:()=> 'admin',apply:value=>snapshots.push(value),onError:error=>errors.push(error),...extra}
  return {sync:createRemoteChatSync(options),options,snapshots,errors}
}
test('a reopened Studio sees an active turn and only its own approval requests',async()=>{
  const {sync,snapshots}=fixture();await sync.refresh()
  assert.deepEqual(snapshots,[{sending:true,approvals:[{id:'own',chatId:'chat'}],messages:[{Role:'user',Content:'work'}]}])
})
test('reconnection never replaces the optimistic transcript or busy state of an owned turn',async()=>{
  const {sync,snapshots}=fixture({ownsTurn:()=>true});await sync.refresh()
  assert.deepEqual(snapshots,[{approvals:[{id:'own',chatId:'chat'}]}])
})
test('finished turns reload the saved reply and read-only viewers receive no approval controls',async()=>{
  const {sync,snapshots}=fixture({role:()=> 'viewer',api:{remoteActivity:async()=>({chats:[],approvals:[{chatId:'chat'}]}),chatMessages:async()=>[{Role:'assistant',Content:'done'}]}})
  await sync.refresh();assert.deepEqual(snapshots,[{sending:false,approvals:[],messages:[{Role:'assistant',Content:'done'}]}])
})
test('a history failure preserves independent active-state evidence and reports the error',async()=>{
  const {sync,options,snapshots,errors}=fixture();options.api.chatMessages=async()=>{throw Error('History unavailable')}
  await sync.refresh();assert.equal(snapshots[0].sending,true);assert.equal(snapshots[0].messages,undefined);assert.equal(errors[0].message,'History unavailable')
})
test('late snapshots cannot overwrite a different chat or a disposed view',async()=>{
  let finish,id='chat';const {sync,snapshots}=fixture({chatID:()=>id,api:{remoteActivity:()=>new Promise(resolve=>finish=resolve),chatMessages:async()=>[]}})
  const first=sync.refresh();id='other';finish({chats:[],approvals:[]});await first;assert.equal(snapshots.length,0)
  id='chat';const second=sync.refresh();sync.dispose();finish({chats:[],approvals:[]});await second;assert.equal(snapshots.length,0)
})
