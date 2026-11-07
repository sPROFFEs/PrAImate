import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { mobileNavigation } from './mobileNavigation.js'

test('mobile navigation preserves every desktop page, label and outline icon', async () => {
  const app=await readFile(new URL('../App.svelte',import.meta.url),'utf8')
  const rows=[...app.matchAll(/\{ id: '([^']+)', label: '([^']+)', icon: icons\.([a-zA-Z]+), load:/g)]
  assert.equal(rows.length,mobileNavigation.length)
  for(const [,id,label,icon] of rows){
    const mobile=mobileNavigation.find(page=>page.id===id)
    assert.ok(mobile,`Missing ${id}`)
    assert.equal(mobile.label,label)
    const desktopIcon=app.match(new RegExp(`\\b${icon}:\\s*'([^']+)'`))
    assert.equal(mobile.icon,desktopIcon[1])
  }
})
