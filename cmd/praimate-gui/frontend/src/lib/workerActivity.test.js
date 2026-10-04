import test from 'node:test'
import assert from 'node:assert/strict'
import { workerExecutions, workerUsage, workerEventLabel, workerTaskRoute, isWorkerRun } from './workerActivity.js'

test('concurrent invocations retain their own routes, parents and stream events', () => {
  const run = { status: 'running', workspace: '/project', profiles: [{tier:'middle',cli:'new-cli',model:'new-model'}], events: [
    {workerID:'a',tier:'middle',cli:'codex',model:'astra',parentID:'parent',kind:'started',text:'review',timestamp:'2026-10-04T10:00:00Z'},
    {workerID:'b',tier:'middle',cli:'claude',model:'sonnet',parentID:'parent',kind:'started',text:'implement',timestamp:'2026-10-04T10:00:01Z'},
    {workerID:'a',tier:'middle',kind:'delegation',target:'fast',text:'find',timestamp:'2026-10-04T10:00:02Z'},
    {workerID:'b',tier:'middle',kind:'reasoning',text:'reported progress',timestamp:'2026-10-04T10:00:03Z'},
    {workerID:'a',tier:'middle',kind:'completed',timestamp:'2026-10-04T10:00:04Z'},
  ] }
  const [a,b] = workerExecutions(run)
  assert.equal(a.cli,'codex'); assert.equal(a.model,'astra'); assert.equal(a.parentID,'parent'); assert.equal(a.status,'completed')
  assert.equal(b.cli,'claude'); assert.equal(b.model,'sonnet'); assert.equal(b.status,'running'); assert.equal(b.events.length,2)
  assert.equal(workerEventLabel(a.events[1]),'Delegated to Fast')
})

test('old snapshots remain readable and interrupted invocations are not shown as active', () => {
  const groups = workerExecutions({ status:'cancelled', workspace:'/project', profiles:[{tier:'primary',cli:'codex',model:'model'}], events:[
    {tier:'primary',kind:'input',text:'objective',timestamp:'2026-10-04T10:00:00Z'},
  ] })
  assert.equal(groups[0].assignment,'objective'); assert.equal(groups[0].cli,'codex'); assert.equal(groups[0].status,'cancelled')
})

test('usage counts completed provider receipts once and excludes stream previews', () => {
  assert.deepEqual(workerUsage({ events:[
    {kind:'response',usage:{source:'provider',inputTokens:20,outputTokens:10}},
    {kind:'stream',usage:{source:'provider',inputTokens:20,outputTokens:10}},
    {kind:'output',usage:{source:'provider',inputTokens:30,outputTokens:15}},
    {kind:'response',usage:{source:'unavailable'}},
  ] }),{input:50,output:25,calls:2})
})

test('saved usage in the previous Go field casing stays visible', () => {
  assert.deepEqual(workerUsage({events:[{kind:'response',usage:{Source:'provider',InputTokens:5,OutputTokens:3}}]}),{input:5,output:3,calls:1})
})

test('native task overrides and event routes never inherit the default CLI name', () => {
  const task={id:'local-task',worker:{profile:'middle',runtime:'native',model:'local-model'}}
  const run={status:'running',profiles:[{tier:'middle',runtime:'cli',cli:'codex',model:'astra'}],dag:{tasks:[task]},events:[{kind:'started',workerID:'one',tier:'middle',taskID:'local-task',runtime:'native',cli:'',model:'actual-local'}]}
  assert.equal(workerTaskRoute(task,run).cli,''); assert.equal(workerExecutions(run)[0].cli,''); assert.equal(workerExecutions(run)[0].model,'actual-local')
})

test('approval ownership recognizes both hierarchical and parallel run identifiers', () => {
  assert.equal(isWorkerRun('worker-one'),true); assert.equal(isWorkerRun('workers-two'),true)
  assert.equal(isWorkerRun('chat-one'),false); assert.equal(isWorkerRun(undefined),false)
})
