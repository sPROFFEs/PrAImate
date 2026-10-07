import test from 'node:test'
import assert from 'node:assert/strict'
import { selectVisibleSkills } from './skillSelection.js'

test('bulk selection keeps existing modes, selects one approved version per skill and loads additions on demand', () => {
  const versions = [
    { ref: 'existing', digest: 'kept', approved: true, selected: true, activation: 'pinned' },
    { ref: 'existing', digest: 'alternative', approved: true, selected: false },
    { ref: 'new', digest: 'one', approved: true, selected: false, activation: 'pinned' },
    { ref: 'new', digest: 'two', approved: true, selected: false },
    { ref: 'pending', digest: 'unapproved', approved: false, selected: false }
  ]
  const result = selectVisibleSkills(versions, versions)
  assert.equal(result.limited, false)
  assert.deepEqual(result.versions.filter(v => v.selected).map(v => [v.ref, v.digest, v.activation]), [
    ['existing', 'kept', 'pinned'], ['new', 'one', 'auto']
  ])
  assert.equal(versions[2].selected, false)
})

test('bulk selection respects the search and the 128-binding limit', () => {
  const versions = Array.from({ length: 150 }, (_, i) => ({ ref: `skill-${i}`, digest: `${i}`, approved: true, selected: i === 149, activation: 'manual' }))
  const filtered = selectVisibleSkills(versions, versions.slice(5, 8))
  assert.deepEqual(filtered.versions.filter(v => v.selected).map(v => v.ref), ['skill-5', 'skill-6', 'skill-7', 'skill-149'])
  const all = selectVisibleSkills(versions, versions)
  assert.equal(all.versions.filter(v => v.selected).length, 128)
  assert.equal(all.limited, true)
  assert.equal(all.versions[149].activation, 'manual')
  assert.equal(all.versions.filter(v => v.selected && v.activation === 'pinned').length, 0)
})
