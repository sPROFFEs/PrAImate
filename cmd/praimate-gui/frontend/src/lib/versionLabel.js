export function versionLabel(value) {
  const version = String(value || '').trim().replace(/^v+/i, '')
  return version ? `v${version}` : ''
}
