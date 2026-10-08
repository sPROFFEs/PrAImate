import assert from 'node:assert/strict'
import { test } from 'node:test'
import { mkdtempSync, readFileSync, writeFileSync, rmSync, mkdirSync, copyFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { resolve } from 'node:path'
import { execFileSync, spawnSync, spawn } from 'node:child_process'
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
    const patch = resolve(root, 'scripts/praimate-code-tls.mjs')
    const pristine = readFileSync(resolve(root, 'third_party/opencode/packages/opencode/src/provider/provider.ts'), 'utf8').replace(/\r\n/g, '\n')
    for (const newline of ['\n', '\r\n']) {
      writeFileSync(resolve(target, 'provider.ts'), pristine.replace(/\n/g, newline))
      execFileSync(process.execPath, [patch, dir])
      const first = readFileSync(resolve(target, 'provider.ts'), 'utf8')
      execFileSync(process.execPath, [patch, dir])
      assert.equal(readFileSync(resolve(target, 'provider.ts'), 'utf8'), first)
      assert.match(first, /trustedTLS \? \{ tls: trustedTLS, redirect: "manual" \}/)
      assert.ok(readFileSync(resolve(target, 'praimate-host-tls.ts'), 'utf8').includes('rejectUnauthorized: true'))
    }
    const invalid = mkdtempSync(resolve(tmpdir(), 'praimate-patch-invalid-'))
    try {
      mkdirSync(resolve(invalid, 'packages/opencode/src/provider'), { recursive: true })
      copyFileSync(resolve(target, 'praimate-host-tls.ts'), resolve(invalid, 'packages/opencode/src/provider/provider.ts'))
      assert.notEqual(spawnSync(process.execPath, [patch, invalid]).status, 0)
    } finally { rmSync(invalid, { recursive: true, force: true }) }
  } finally { rmSync(dir, { recursive: true, force: true }) }
})

test('private CA chains work with pinned PrAImate Code and standard extra-CA CLI startup', async () => {
  const ca = fixture(), leafDir = mkdtempSync(resolve(tmpdir(), 'praimate-leaf-test-'))
  const run = args => execFileSync('openssl', args, {stdio:'ignore'})
  run(['req','-newkey','rsa:2048','-nodes','-keyout',resolve(leafDir,'key.pem'),'-out',resolve(leafDir,'leaf.csr'),'-subj','/CN=localhost'])
  writeFileSync(resolve(leafDir,'ext.cnf'),'subjectAltName=DNS:localhost\nextendedKeyUsage=serverAuth\n')
  run(['x509','-req','-in',resolve(leafDir,'leaf.csr'),'-CA',resolve(ca.dir,'cert.pem'),'-CAkey',resolve(ca.dir,'key.pem'),'-CAcreateserial','-out',resolve(leafDir,'leaf.pem'),'-days','1','-extfile',resolve(leafDir,'ext.cnf')])
  const cert=readFileSync(resolve(leafDir,'leaf.pem'),'utf8')
  const server=createServer({key:readFileSync(resolve(leafDir,'key.pem')),cert:cert+ca.cert},(req,res)=>{res.setHeader('Connection','close');res.end('ca-ready')})
  await new Promise(resolve => server.listen(0,'127.0.0.1',resolve))
  const origin=`https://localhost:${server.address().port}`, endpoint=origin+'/v1'
  const get = tls => new Promise((resolve,reject)=>request(endpoint,{...tls,agent:false},res=>{let body='';res.on('data',part=>body+=part);res.on('end',()=>resolve(body))}).on('error',reject).end())
  const child = extra => new Promise((resolve,reject)=>{
    const proc=spawn(process.execPath,['-e',`require('node:https').get(${JSON.stringify(endpoint)},r=>{r.pipe(process.stdout)}).on('error',()=>process.exitCode=2)`],{env:{...process.env,NODE_EXTRA_CA_CERTS:extra}})
    let out='';proc.stdout.on('data',p=>out+=p);proc.on('error',reject);proc.on('exit',code=>resolve({code,out}))
  })
  try {
    await assert.rejects(get())
    for(const trust of [{certificate:cert,authorities:ca.cert},{authorities:ca.cert}]) {
      const tls=hostTLS(endpoint,{[origin]:JSON.stringify(trust)})
      assert.equal(await get(tls),'ca-ready')
      if(typeof Bun!=='undefined') assert.equal(await (await fetch(endpoint,{tls})).text(),'ca-ready')
    }
    assert.equal((await child('')).code,2)
    assert.deepEqual(await child(resolve(ca.dir,'cert.pem')),{code:0,out:'ca-ready'})
  } finally {await new Promise(resolve=>server.close(resolve));ca.close();rmSync(leafDir,{recursive:true,force:true})}
})
