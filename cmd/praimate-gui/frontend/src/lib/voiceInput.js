import { writable } from 'svelte/store'
import { api } from './api.js'

export const assistantConfig = writable(null)
export const assistantTranscript = writable(null)
export async function refreshAssistantConfig() {
  const config = await api.assistantConfig()
  assistantConfig.set(config)
  return config
}
export function matchesShortcut(event, shortcut) {
  const key = shortcut?.split('+').at(-1)
  return !!key && (event.ctrlKey || event.metaKey) && event.shiftKey && !event.altKey &&
    (key === 'Space' ? event.code === 'Space' : event.key.toUpperCase() === key)
}

// PCM WAV is the only backend format. Average input samples before downsampling
// rather than selecting one sample, then encode bounded mono PCM16.
export function encodeWAV(samples, sampleRate) {
  if (!Number.isFinite(sampleRate) || sampleRate < 16000) throw new Error('Unsupported microphone sample rate')
  const count = Math.min(16000 * 120, Math.floor(samples.length * 16000 / sampleRate))
  if (!count) throw new Error('No microphone audio was captured')
  const bytes = new ArrayBuffer(44 + count * 2), view = new DataView(bytes)
  const str = (offset, value) => { for (let i = 0; i < value.length; i++) view.setUint8(offset + i, value.charCodeAt(i)) }
  str(0, 'RIFF'); view.setUint32(4, bytes.byteLength - 8, true); str(8, 'WAVE'); str(12, 'fmt ')
  view.setUint32(16, 16, true); view.setUint16(20, 1, true); view.setUint16(22, 1, true)
  view.setUint32(24, 16000, true); view.setUint32(28, 32000, true)
  view.setUint16(32, 2, true); view.setUint16(34, 16, true); str(36, 'data'); view.setUint32(40, count * 2, true)
  for (let i = 0; i < count; i++) {
    const from = Math.floor(i * sampleRate / 16000), to = Math.max(from + 1, Math.floor((i + 1) * sampleRate / 16000))
    let sum = 0; for (let j = from; j < to && j < samples.length; j++) sum += samples[j]
    const value = Math.max(-1, Math.min(1, sum / (to - from)))
    view.setInt16(44 + i * 2, Math.round(value * (value < 0 ? 32768 : 32767)), true)
  }
  return new Uint8Array(bytes)
}
export function audioBase64(bytes) {
  let encoded = ''; for (let i = 0; i < bytes.length; i += 32768) encoded += String.fromCharCode(...bytes.subarray(i, i + 32768))
  return btoa(encoded)
}

let activeCapture = null
export async function beginCapture() {
  if (activeCapture) throw new Error('A microphone recording is already active')
  const capture = { cancelled: false, stream: null, context: null, processor: null, source: null, chunks: [], timer: null }
  activeCapture = capture
  capture.cancel = () => {
    capture.cancelled = true; clearTimeout(capture.timer)
    if (capture.lease) { const lease = capture.lease; capture.lease = null; api.endVoiceCapture(lease).catch(() => {}) }
    if (capture.processor) capture.processor.onaudioprocess = null
    capture.processor?.disconnect(); capture.source?.disconnect()
    capture.stream?.getTracks().forEach(track => track.stop())
    capture.context?.close().catch(() => {})
    capture.chunks = []
    if (activeCapture === capture) activeCapture = null
  }
  try {
    const session = await api.beginVoiceCapture()
    capture.lease = session.id
    if (capture.cancelled) { capture.cancel(); throw new Error('Recording cancelled') }
    if (session.native) {
      capture.finish = async () => {
        try { return await api.finishNativeVoiceCapture(capture.lease) }
        finally { capture.cancel() }
      }
      capture.timer = setTimeout(() => capture.onLimit?.(), 120000)
      return capture
    }
    if (!navigator.mediaDevices?.getUserMedia) throw new Error('Microphone capture is unavailable in this desktop WebView')
    capture.stream = await navigator.mediaDevices.getUserMedia({ audio: { channelCount: 1, echoCancellation: true }, video: false })
    if (capture.cancelled) { capture.cancel(); throw new Error('Recording cancelled') }
    const Audio = window.AudioContext || window.webkitAudioContext
    if (!Audio) throw new Error('Microphone audio processing is unavailable')
    capture.context = new Audio(); await capture.context.resume()
    if (capture.cancelled) { capture.cancel(); throw new Error('Recording cancelled') }
    capture.source = capture.context.createMediaStreamSource(capture.stream)
    // ScriptProcessor is supported by the embedded desktop WebViews and exists
    // only while holding push-to-talk; audio never leaves the local backend.
    capture.processor = capture.context.createScriptProcessor(4096, 1, 1)
    let length = 0
    capture.processor.onaudioprocess = event => {
      if (capture.cancelled || length >= capture.context.sampleRate * 120) return
      const chunk = new Float32Array(event.inputBuffer.getChannelData(0)); capture.chunks.push(chunk); length += chunk.length
    }
    capture.source.connect(capture.processor); capture.processor.connect(capture.context.destination)
    capture.finish = async () => {
      const rate = capture.context.sampleRate
      const samples = new Float32Array(length); let offset = 0
      for (const chunk of capture.chunks) { samples.set(chunk, offset); offset += chunk.length }
      capture.cancel(); return audioBase64(encodeWAV(samples, rate))
    }
    capture.timer = setTimeout(() => capture.onLimit?.(), 120000)
    return capture
  } catch (error) { capture.cancel(); throw error }
}
export function cancelCapture() { activeCapture?.cancel() }
