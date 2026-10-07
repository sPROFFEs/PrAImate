import assert from 'node:assert/strict'
import test from 'node:test'
import { versionLabel } from './versionLabel.js'

test('release tags and bare versions have exactly one prefix', () => {
  for (const version of ['1.2.16', 'v1.2.16', ' vv1.2.16 ']) assert.equal(versionLabel(version), 'v1.2.16')
  assert.equal(versionLabel('v1.3.0-rc.1'), 'v1.3.0-rc.1')
  assert.equal(versionLabel(null), '')
})
