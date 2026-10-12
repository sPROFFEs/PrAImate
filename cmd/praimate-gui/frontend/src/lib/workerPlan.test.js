import test from 'node:test'
import assert from 'node:assert/strict'
import { shouldRevealWorkerPlan } from './workerActivity.js'

test('a plan becomes visible when the same run transitions from planning to draft', () => {
  const run={id:'run-one',status:'planning',dag:{tasks:[]}}
  assert.equal(shouldRevealWorkerPlan(run,''),false)
  run.status='draft';run.dag.tasks=[{id:'task-a'}]
  assert.equal(shouldRevealWorkerPlan(run,''),true)
  assert.equal(shouldRevealWorkerPlan(run,'run-one'),false)
  run.status='running'
  assert.equal(shouldRevealWorkerPlan(run,''),false)
})
