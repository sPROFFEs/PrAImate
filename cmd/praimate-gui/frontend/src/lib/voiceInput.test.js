import test from 'node:test'
import assert from 'node:assert/strict'
import { encodeWAV, matchesShortcut, beginCapture, cancelCapture } from './voiceInput.js'

test('voice generates canonical bounded 16 kHz PCM WAV', () => {
  const wav = encodeWAV(new Float32Array(48000).fill(0.5), 48000)
  const view = new DataView(wav.buffer)
  assert.equal(new TextDecoder().decode(wav.subarray(0,4)), 'RIFF')
  assert.equal(view.getUint32(24,true), 16000)
  assert.equal(view.getUint16(22,true), 1)
  assert.equal(view.getUint16(34,true), 16)
  assert.equal(view.getUint32(40,true), 32000)
  assert.equal(view.getInt16(44,true), 16384)
  assert.equal(view.getUint32(4,true), wav.length - 8)
})

test('cancelled pending capture ends only its own lease', async () => {
  const previous = globalThis.window
  let resolve, ended = []
  globalThis.window = {go:{main:{App:{
    BeginVoiceCapture: () => new Promise(r => { resolve = r }),
    EndVoiceCapture: async id => { ended.push(id) }
  }}}}
  try {
    const pending = beginCapture()
    cancelCapture()
    assert.deepEqual(ended, [])
    resolve({id:'old-lease',native:true})
    await assert.rejects(pending,/cancelled/)
    assert.deepEqual(ended,['old-lease'])
  } finally { cancelCapture(); globalThis.window = previous }
})
test('native capture bypasses browser media APIs and returns encoded WAV', async () => {
  const previous = globalThis.window
  let ended = [], finished = []
  globalThis.window = {go:{main:{App:{
    BeginVoiceCapture: async () => ({id:'native-lease',native:true}),
    EndVoiceCapture: async id => { ended.push(id) },
    FinishNativeVoiceCapture: async id => { finished.push(id); return 'wav-fixture' }
  }}}}
  try {
    const capture = await beginCapture()
    assert.equal(await capture.finish(),'wav-fixture')
    assert.deepEqual(finished,['native-lease'])
    assert.deepEqual(ended,['native-lease'])
  } finally { cancelCapture(); globalThis.window = previous }
})
test('voice resampling clamps PCM and rejects missing audio', () => {
  const wav = encodeWAV(new Float32Array([-2,2]),16000)
  const view = new DataView(wav.buffer)
  assert.equal(view.getInt16(44,true),-32768)
  assert.equal(view.getInt16(46,true),32767)
  assert.throws(() => encodeWAV(new Float32Array(),48000),/No microphone/)
  assert.throws(() => encodeWAV(new Float32Array(100),8000),/sample rate/)
})
test('configurable shortcuts work across Ctrl and Cmd and exclude Alt', () => {
  assert.equal(matchesShortcut({ctrlKey:true,shiftKey:true,key:'m'},'Mod+Shift+M'),true)
  assert.equal(matchesShortcut({metaKey:true,shiftKey:true,key:' ',code:'Space'},'Mod+Shift+Space'),true)
  assert.equal(matchesShortcut({ctrlKey:true,shiftKey:false,key:'m'},'Mod+Shift+M'),false)
  assert.equal(matchesShortcut({ctrlKey:true,shiftKey:true,altKey:true,key:'m'},'Mod+Shift+M'),false)
})
