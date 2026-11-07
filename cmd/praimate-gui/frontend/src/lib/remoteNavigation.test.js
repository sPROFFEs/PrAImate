import test from 'node:test'
import assert from 'node:assert/strict'
import { mobileNavigation } from './mobileNavigation.js'
import { remoteRoute, restoreRemoteRoute } from './remoteNavigation.js'

const stores = () => Object.fromEntries(['activePage','openChatId','openWorkerId','agentStudio','pendingTerm'].map(key => [key,{value:null,set(value){this.value=value}}]))

test('every desktop section opens in a new remote/mobile window',async()=>{
  for (const {id} of mobileNavigation) {
    const state=stores()
    assert.equal(await restoreRemoteRoute(remoteRoute(id),state,()=>{throw Error('Unexpected terminal lookup')}),true)
    assert.equal(state.activePage.value,id)
  }
})

test('session links restore the requested chat, worker, agent and live terminal',async()=>{
  const state=stores()
  await restoreRemoteRoute(remoteRoute('chats',{chat:'chat & 1'}),state)
  assert.equal(state.openChatId.value,'chat & 1')
  await restoreRemoteRoute(remoteRoute('workers',{worker:'workers-1'}),state)
  assert.equal(state.openWorkerId.value,'workers-1')
  await restoreRemoteRoute(remoteRoute('agents',{agent:'agent-1'}),state)
  assert.deepEqual(state.agentStudio.value,{id:'agent-1'})
  await restoreRemoteRoute(remoteRoute('code',{terminal:'term-1'}),state,async()=>[{id:'term-1',chatId:'code-1',name:'codex',cwd:'/project'}])
  assert.deepEqual(state.pendingTerm.value,{termId:'term-1',chatId:'code-1',cli:'codex',cwd:'/project'})
  assert.equal(state.activePage.value,'code')
})

test('closed terminals open Code without resurrecting or launching a process',async()=>{
  const state=stores()
  await restoreRemoteRoute('#page=code&terminal=closed',state,async()=>[])
  assert.equal(state.pendingTerm.value,null)
  assert.equal(state.activePage.value,'code')
})

test('workspace and invalid routes never replace the document editor or open a session',async()=>{
  for (const hash of ['#workspace=%2Fproject&page=chats&chat=one','#page=invalid&chat=one']) {
    const state=stores()
    assert.equal(await restoreRemoteRoute(hash,state),false)
    assert.equal(state.activePage.value,null)
    assert.equal(state.openChatId.value,null)
  }
  assert.equal(remoteRoute('settings',{chat:'private-chat',terminal:'term-1'}),'#page=settings')
})
