function quoteCommandPart(value) {
  if (/^[A-Za-z0-9_./:@%+=,-]+$/.test(value)) return value
  return `'${value.replaceAll("'", "'\\''")}'`
}

export function commandForMCPForm(server) {
  return [server.command || '', ...(server.args || [])]
    .filter(Boolean)
    .map(quoteCommandPart)
    .join(' ')
}

export function envForMCPForm(server) {
  return Object.entries(server.env || {})
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([key, value]) => `${key}=${value}`)
    .join('\n')
}

export function headersForMCPForm(server) {
  const headers = Object.create(null)
  const { header = 'Authorization', token = '' } = server.auth || {}
  if (token) headers[header] = header.toLowerCase() === 'authorization' && !/^bearer /i.test(token) ? `Bearer ${token}` : token
  try { Object.assign(headers, JSON.parse(server.auth?.headers || '{}')) } catch {}
  return Object.entries(headers).sort(([a],[b]) => a.localeCompare(b)).map(([name,value]) => `${name}: ${value}`).join('\n')
}
