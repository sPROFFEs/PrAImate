import test from 'node:test'
import assert from 'node:assert/strict'
import { visibleNavigation } from './editionNavigation.js'

const pages = [{id:'dashboard'}, {id:'server'}, {id:'settings'}]
test('Server administration is visible in the local Server GUI and to remote administrators', () => {
  assert.deepEqual(visibleNavigation(pages, 'server'), pages)
  assert.deepEqual(visibleNavigation(pages, 'server', 'admin'), pages)
})
test('Desktop and restricted remote identities never receive Server administration navigation', () => {
  for (const [edition, role] of [['desktop',undefined],['desktop','admin'],['server','control'],['server','viewer']]) {
    assert.deepEqual(visibleNavigation(pages, edition, role).map(page=>page.id), ['dashboard','settings'])
  }
})
