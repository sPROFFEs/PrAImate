import { test } from 'node:test'
import assert from 'node:assert/strict'

test('all accent presets choose a foreground with readable text contrast', async () => {
  const styles = new Map()
  globalThis.localStorage = { getItem: () => null, setItem() {} }
  globalThis.document = { documentElement: { classList: { toggle() {} }, style: {
    setProperty: (k, v) => styles.set(k, v), removeProperty: (k) => styles.delete(k),
  } } }
  let subscribed = 0
  globalThis.window = { matchMedia: () => ({ matches: false,
    addEventListener: () => subscribed++, removeEventListener: () => subscribed--,
  }) }
  const { setAccent, ACCENT_PRESETS, initTheme, setThemeMode } = await import('./theme.js')
  const luminance = (hex) => {
    const rgb = hex.slice(1).match(/../g).map(v => parseInt(v, 16) / 255)
      .map(v => v <= .04045 ? v / 12.92 : ((v + .055) / 1.055) ** 2.4)
    return rgb[0] * .2126 + rgb[1] * .7152 + rgb[2] * .0722
  }
  for (const { color } of ACCENT_PRESETS.filter(p => p.color)) {
    setAccent(color)
    const a = luminance(color), b = luminance(styles.get('--accent-fg'))
    assert.ok((Math.max(a,b) + .05) / (Math.min(a,b) + .05) >= 4.5, color)
  }
  const dispose = initTheme()
  assert.equal(subscribed, 1)
  dispose()
  assert.equal(subscribed, 0)
  localStorage.setItem = () => { throw new Error('storage unavailable') }
  assert.doesNotThrow(() => setThemeMode('light'))
  assert.doesNotThrow(() => setAccent('default'))
  assert.equal(styles.has('--accent'), false)
})
