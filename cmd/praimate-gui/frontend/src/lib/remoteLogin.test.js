import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { compile, parse } from 'svelte/compiler'

async function renderedPairingInput() {
  const source = await readFile(new URL('./RemoteLogin.svelte', import.meta.url), 'utf8')
  const inputs = []
  function visit(node) {
    if (node?.type === 'Element' && node.name === 'input' && node.attributes.some(attribute => attribute.name === 'autocomplete' && attribute.value?.[0]?.data === 'one-time-code')) inputs.push(node)
    for (const child of node?.children || []) visit(child)
    if (node?.else) visit(node.else)
  }
  visit(parse(source).html)
  assert.equal(inputs.length, 1)
  const field = inputs[0]
  // Render the actual field through Svelte so interpolated attribute values
  // are tested as delivered to the browser, rather than inspecting source text.
  const { js } = compile(`<script>export let code = ''</script>${source.slice(field.start, field.end)}`, {generate:'ssr'})
  const module = await import(`data:text/javascript;base64,${Buffer.from(js.code.replaceAll('"svelte/internal"', JSON.stringify(import.meta.resolve('svelte/internal')))).toString('base64')}`)
  return module.default.render({code:'01234567'}).html
}

test('rendered login accepts all eight-digit pairing codes, including leading zeroes', async () => {
  const html = await renderedPairingInput()
  const pattern = html.match(/\bpattern="([^"]+)"/)[1]
  const validity = new RegExp(`^(?:${pattern})$`, 'v')
  for (const code of ['01234567','00000000','12345678','99999999']) assert.ok(validity.test(code), `Valid code rejected: ${code}`)
  assert.match(html, /value="01234567"/)
})

test('rendered login rejects incomplete, oversized and nonnumeric codes', async () => {
  const html = await renderedPairingInput()
  const pattern = html.match(/\bpattern="([^"]+)"/)[1]
  const validity = new RegExp(`^(?:${pattern})$`, 'v')
  for (const code of ['', '18', '1234567', '123456789', '1234abcd']) assert.equal(validity.test(code), false, `Invalid code accepted: ${code}`)
})
