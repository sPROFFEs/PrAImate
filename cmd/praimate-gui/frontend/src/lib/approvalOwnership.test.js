import test from 'node:test'
import assert from 'node:assert/strict'
import { ownsApproval } from './approvalOwnership.js'

test('a remote Studio window cannot reject another chat or worker permission request',()=>{
  const decisions=[],context={chatIDs:['studio-chat'],remote:true,role:'admin',resolve:(...args)=>decisions.push(args)}
  for(const chatId of ['other-chat','workers-1','assistant-1'])assert.equal(ownsApproval({id:'request',chatId},context),false)
  assert.deepEqual(decisions,[])
  assert.equal(ownsApproval({id:'request',chatId:'studio-chat'},context),true)
  assert.deepEqual(decisions,[])
})
test('read-only clients never present or resolve approvals',()=>{
  const decisions=[],context={chatIDs:['own-chat'],remote:true,role:'viewer',resolve:(...args)=>decisions.push(args)}
  assert.equal(ownsApproval({id:'request',chatId:'own-chat'},context),false)
  assert.deepEqual(decisions,[])
})
test('desktop ownership and fail-closed decisions are preserved',()=>{
  const decisions=[],context={chatIDs:['own-chat','resumed-run'],remote:false,resolve:(...args)=>{decisions.push(args);return Promise.resolve()}}
  assert.equal(ownsApproval({id:'request',chatId:'own-chat'},context),true)
  assert.equal(ownsApproval({id:'request',chatId:'resumed-run'},context),true)
  assert.equal(ownsApproval({id:'other',chatId:'unowned-chat'},context),false)
  assert.deepEqual(decisions,[['other',false,false]])
})
