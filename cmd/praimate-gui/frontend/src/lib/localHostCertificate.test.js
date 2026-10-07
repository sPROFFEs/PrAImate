import test from 'node:test'
import assert from 'node:assert/strict'
import { probeWithCertificateException } from './localHostCertificate.js'

const tlsError = new Error('tls: failed to verify certificate: x509: certificate signed by unknown authority')
const certificate = { origin:'https://local.example:443',pem:'fixture certificate',fingerprint:'fixture fingerprint',expires:'2030-01-01' }

test('only accepts and retries a certificate after explicit confirmation', async () => {
  const calls=[];let attempts=0
  const result=await probeWithCertificateException({endpoint:'https://local.example',
    probe:async()=>{calls.push('probe');if(++attempts===1)throw tlsError;return ['model']},
    inspect:async()=>{calls.push('inspect');return certificate},
    confirm:async options=>{assert.match(options.message,/fixture fingerprint/);calls.push('confirm');return true},
    trust:async(endpoint,pem)=>{assert.equal(endpoint,'https://local.example');assert.equal(pem,certificate.pem);calls.push('trust')}
  })
  assert.deepEqual(calls,['probe','inspect','confirm','trust','probe']);assert.deepEqual(result,{models:['model'],trusted:true})
})

test('cancelling never stores trust or retries', async () => {
  let probes=0
  await assert.rejects(probeWithCertificateException({endpoint:'https://local.example',probe:async()=>{probes++;throw tlsError},inspect:async()=>certificate,confirm:async()=>false,trust:()=>assert.fail('trust stored without consent')}),/unknown authority/)
  assert.equal(probes,1)
})

test('ordinary provider errors do not request TLS exceptions', async () => {
  await assert.rejects(probeWithCertificateException({endpoint:'https://local.example',probe:async()=>{throw Error('HTTP 503')},inspect:()=>assert.fail('inspected a provider failure')}),/503/)
})

test('changing the selected endpoint while confirming cannot persist the exception', async () => {
  let current=true
  await assert.rejects(probeWithCertificateException({endpoint:'https://local.example',probe:async()=>{throw tlsError},inspect:async()=>certificate,current:()=>current,confirm:async()=>{current=false;return true},trust:()=>assert.fail('trust persisted after endpoint changed')}),/unknown authority/)
})

test('a changed pinned certificate requires a new confirmation', async () => {
  let confirmed=false
  await assert.rejects(probeWithCertificateException({endpoint:'https://local.example',probe:async()=>{throw Error('trusted host certificate changed; review the new certificate')},inspect:async()=>certificate,confirm:async()=>{confirmed=true;return false},trust:()=>assert.fail('changed certificate automatically trusted')}),/certificate changed/)
  assert.equal(confirmed,true)
})
