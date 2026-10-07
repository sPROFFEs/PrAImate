import { readFileSync, writeFileSync, copyFileSync } from 'node:fs'
import { resolve, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'

const source = resolve(process.argv[2], 'packages/opencode/src/provider')
const providerPath = resolve(source, 'provider.ts')
const anchor = '          const res = await fetchFn(input, {\n            ...opts,'
let provider = readFileSync(providerPath, 'utf8')
if (!provider.includes('praimate:host-tls')) {
  if (!provider.includes(anchor)) throw new Error('PrAImate host TLS patch anchor missing; review the upstream provider transport')
  provider = 'import { hostTLS, hostCertificates } from "./praimate-host-tls"\n' + provider.replace(anchor,
    '          // praimate:host-tls — consent is scoped to this request origin.\n' +
    '          const trustedTLS = hostTLS(input, hostCertificates(process.env.PRAIMATE_HOST_TLS))\n' +
    '          const res = await fetchFn(input, {\n            ...opts,\n' +
    '            ...(trustedTLS ? { tls: trustedTLS, redirect: "manual" } : {}),')
  writeFileSync(providerPath, provider)
}
copyFileSync(resolve(dirname(fileURLToPath(import.meta.url)), 'praimate-code/host-tls.ts'), resolve(source, 'praimate-host-tls.ts'))
