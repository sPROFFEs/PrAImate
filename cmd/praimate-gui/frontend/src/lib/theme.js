// Theme system — OpenGUI-style class-driven theming, adapted to a
// dark-native app: `:root` carries the dark tokens, the `.light` class
// flips to light, and "system" follows prefers-color-scheme. Mode and
// accent persist in localStorage so they survive restarts.
import { writable } from 'svelte/store'

const MODE_KEY = 'praimate:theme'
const ACCENT_KEY = 'praimate:accent'

/** Preset accent colors (OpenGUI's palette): 'default' = theme-neutral. */
export const ACCENT_PRESETS = [
  { id: 'default', color: null, label: 'Standard' },
  { id: 'blue', color: '#5482ff', label: 'Blue' },
  { id: 'green', color: '#22c55e', label: 'Green' },
  { id: 'purple', color: '#a855f7', label: 'Purple' },
  { id: 'orange', color: '#f97316', label: 'Orange' },
  { id: 'red', color: '#e11d48', label: 'Red' },
  { id: 'teal', color: '#14b8a6', label: 'Teal' },
]

function storedMode() {
  let v
  try { v = localStorage.getItem(MODE_KEY) } catch {}
  return v === 'light' || v === 'dark' || v === 'system' ? v : 'dark'
}

function storedAccent() {
  let v
  try { v = localStorage.getItem(ACCENT_KEY) } catch {}
  if (v === 'default') return 'default'
  if (v && /^#[0-9a-f]{6}$/i.test(v)) return v
  return 'default'
}

export const themeMode = writable(storedMode())
export const accentColor = writable(storedAccent())

function systemPrefersLight() {
  return window.matchMedia('(prefers-color-scheme: light)').matches
}

function resolve(mode) {
  if (mode === 'system') return systemPrefersLight() ? 'light' : 'dark'
  return mode
}

/** WCAG-ish readable foreground for an accent hex (white or near-black). */
function readableForeground(hex) {
  const v = Number.parseInt(hex.slice(1), 16)
  const toLin = (c) => {
    const s = c / 255
    return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4
  }
  const lum =
    0.2126 * toLin((v >> 16) & 255) + 0.7152 * toLin((v >> 8) & 255) + 0.0722 * toLin(v & 255)
  // Choose the greater contrast ratio, including medium-bright presets.
  return (lum + 0.05) / 0.05 > 1.05 / (lum + 0.05) ? '#000000' : '#ffffff'
}

function apply(mode, accent) {
  const root = document.documentElement
  root.classList.toggle('light', resolve(mode) === 'light')
  if (accent === 'default') {
    root.style.removeProperty('--accent')
    root.style.removeProperty('--accent-fg')
  } else {
    root.style.setProperty('--accent', accent)
    root.style.setProperty('--accent-fg', readableForeground(accent))
  }
}

let mode = storedMode()
let accent = storedAccent()

export function setThemeMode(next) {
  mode = next
  try { localStorage.setItem(MODE_KEY, next) } catch {}
  themeMode.set(next)
  apply(mode, accent)
}

export function setAccent(next) {
  accent = next
  try { localStorage.setItem(ACCENT_KEY, next) } catch {}
  accentColor.set(next)
  apply(mode, accent)
}

export function initTheme() {
  apply(mode, accent)
  // Track OS preference while in "system" mode.
  const mq = window.matchMedia('(prefers-color-scheme: light)')
  const changed = () => { if (mode === 'system') apply(mode, accent) }
  mq.addEventListener('change', changed)
  return () => mq.removeEventListener('change', changed)
}
