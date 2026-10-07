import assert from 'node:assert/strict'
import { test } from 'node:test'
import { mkdtempSync, readFileSync, rmSync, mkdirSync, copyFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { resolve } from 'node:path'
import { execFileSync, spawnSync } from 'node:child_process'
import { X509Certificate } from 'node:crypto'
import { createServer, request } from 'node:https'
import { hostCertificates, hostTLS } from './host-tls.ts'

function fixture() {
  const dir = mkdtempSync(resolve(tmpdir(), 'praimate-tls-test-'))
  execFileSync('openssl', ['req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-keyout', resolve(dir, 'key.pem'), '-out', resolve(dir, 'cert.pem'), '-days', '1', '-subj', '/CN=localhost', '-addext', 'subjectAltName=DNS:localhost'], { stdio: 'ignore' })
  return { dir, key: readFileSync(resolve(dir, 'key.pem')), cert: readFileSync(resolve(dir, 'cert.pem'), 'utf8'), close() { rmSync(dir, { recursive: true, force: true }) } }
}

test('TLS exceptions are scoped, validate the hostname, and pin the exact leaf', () => {
  const first = fixture(), second = fixture()
  try {
    const trusted = hostCertificates(JSON.stringify({ 'https://localhost:443': first.cert }))
    const tls = hostTLS(new Request('https://localhost/v1'), trusted)
    assert.equal(tls.rejectUnauthorized, true)
    assert.equal(tls.checkServerIdentity('localhost', new X509Certificate(first.cert).toLegacyObject()), undefined)
    assert.match(tls.checkServerIdentity('localhost', new X509Certificate(second.cert).toLegacyObject()).message, /certificate changed/)
    assert.ok(tls.checkServerIdentity('other.example', new X509Certificate(first.cert).toLegacyObject()) instanceof Error)
    assert.equal(hostTLS('https://localhost:444/v1', trusted), undefined)
    assert.equal(hostTLS('https://other.example/v1', trusted), undefined)
    assert.equal(hostTLS('http://localhost/v1', trusted), undefined)
    assert.throws(() => hostCertificates('[]'), /Invalid/)
    assert.throws(() => hostCertificates('{"https://localhost:443":true}'), /Invalid/)
  } finally { first.close(); second.close() }
})

test('a self-signed HTTPS server is usable only after scoped consent', async () => {
  const f = fixture()
  const server = createServer({ key: f.key, cert: f.cert }, (req, res) => { res.setHeader('Connection', 'close'); res.end('fixture-ready') })
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve))
  const endpoint = `https://localhost:${server.address().port}/v1`
  const fetchFixture = typeof Bun !== 'undefined' ? fetch : (url, options = {}) => new Promise((resolve, reject) => {
    request(url, { ...options.tls, agent: false }, res => {
      let text = ''; res.on('data', data => { text += data }); res.on('end', () => resolve(new Response(text)))
    }).on('error', reject).end()
  })
  try {
    await assert.rejects(fetchFixture(endpoint))
    const trusted = { [`https://localhost:${server.address().port}`]: f.cert }
    const response = await fetchFixture(endpoint, { tls: hostTLS(endpoint, trusted) })
    assert.equal(await response.text(), 'fixture-ready')
    await assert.rejects(fetchFixture(endpoint, { tls: hostTLS(endpoint, {}) }))
  } finally { await new Promise(resolve => server.close(resolve)); f.close() }
})

test('build patch applies to the pinned provider, is idempotent and fails on drift', () => {
  const dir = mkdtempSync(resolve(tmpdir(), 'praimate-patch-test-'))
  const root = resolve(import.meta.dirname, '../..')
  const target = resolve(dir, 'packages/opencode/src/provider')
  mkdirSync(target, { recursive: true })
  try {
    copyFileSync(resolve(root, 'third_party/opencode/packages/opencode/src/provider/provider.ts'), resolve(target, 'provider.ts'))
    const patch = resolve(root, 'scripts/praimate-code-tls.mjs')
    execFileSync(process.execPath, [patch, dir])
    const first = readFileSync(resolve(target, 'provider.ts'), 'utf8')
    execFileSync(process.execPath, [patch, dir])
    assert.equal(readFileSync(resolve(target, 'provider.ts'), 'utf8'), first)
    assert.match(first, /trustedTLS \? \{ tls: trustedTLS, redirect: "manual" \}/)
    assert.ok(readFileSync(resolve(target, 'praimate-host-tls.ts'), 'utf8').includes('rejectUnauthorized: true'))
    const invalid = mkdtempSync(resolve(tmpdir(), 'praimate-patch-invalid-'))
    try {
      mkdirSync(resolve(invalid, 'packages/opencode/src/provider'), { recursive: true })
      copyFileSync(resolve(target, 'praimate-host-tls.ts'), resolve(invalid, 'packages/opencode/src/provider/provider.ts'))
      assert.notEqual(spawnSync(process.execPath, [patch, invalid]).status, 0)
    } finally { rmSync(invalid, { recursive: true, force: true }) }
  } finally { rmSync(dir, { recursive: true, force: true }) }
})
