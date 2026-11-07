import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { compile, parse } from 'svelte/compiler'

async function deviceList(path) {
  const source = await readFile(new URL(path, import.meta.url), 'utf8')
  let list
  function visit(node) {
    if (node?.type === 'Element' && node.attributes.some(attribute => attribute.name === 'class' && attribute.value?.[0]?.data === 'devices')) list = node
    for (const child of node?.children || []) visit(child)
    if (node?.else) visit(node.else)
  }
  visit(parse(source).html)
  assert.ok(list, 'Device list must be rendered by the settings view')
  const { js } = compile(`<script>export let devices=[]; let busy=false; let now=Date.now(); function revoke(){}</script>${source.slice(list.start, list.end)}`, {generate:'ssr'})
  const code = js.code.replace(/^import (.+?) from (['"])([^'"]+)\2;?$/gm, (_, bindings, quote, specifier) => `import ${bindings} from ${JSON.stringify(import.meta.resolve(specifier))};`)
  const module = await import(`data:text/javascript;base64,${Buffer.from(code).toString('base64')}`)
  return devices => module.default.render({devices}).html
}

for (const view of ['./RemoteConnection.svelte', '../pages/Server.svelte']) {
  test(`${view} removes revoked devices from the visible access list`, async () => {
    const render = await deviceList(view)
    const device = {id:'one', name:'Test phone', role:'control', expiresAt:'2099-01-01T00:00:00Z', revoked:false}
    const revoked = {...device, id:'two', name:'Revoked tablet', revoked:true}
    const before = render([device, revoked])
    assert.match(before, /Test phone/)
    assert.doesNotMatch(before, /Revoked tablet/)
    const after = render([{...device, revoked:true}, revoked])
    assert.doesNotMatch(after, /Test phone|Revoked tablet|>Revoke/)
    assert.match(after, /No active paired devices/)
  })
}
