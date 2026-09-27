import test from 'node:test'
import assert from 'node:assert/strict'
import { localChoice, localChoiceKey } from './localModelChoice.js'

test('same model on two hosts keeps its selected route', () => {
  const options = { allModels: [
    { hostId: 'default', endpoint: 'http://localhost:11434', model: 'qwen' },
    { hostId: 'gpu', endpoint: 'http://localhost:8000', model: 'qwen' },
  ] }
  assert.equal(localChoiceKey(options, 'http://localhost:8000', 'qwen'), 'gpu::qwen')
  assert.deepEqual(localChoice(options, 'gpu::qwen'), options.allModels[1])
})
