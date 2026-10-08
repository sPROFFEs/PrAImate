import { X509Certificate } from "node:crypto"
import { checkServerIdentity, rootCertificates } from "node:tls"

// The core supplies only certificates explicitly accepted for an HTTPS origin.
// Keep normal validation; legacy and inspected certificates retain a leaf pin.
// Explicit CA imports use standard chain validation without a leaf pin.
export function hostTLS(input: string | URL | Request, trusted: Record<string, string>) {
  const url = new URL(input instanceof Request ? input.url : String(input))
  if (url.protocol !== "https:" || url.username || url.password) return
  const origin = `https://${url.hostname.toLowerCase()}:${url.port || "443"}`
  const pem = trusted[origin]
  if (!pem) return
  const trust = pem.trimStart().startsWith("{") ? JSON.parse(pem) : { certificate: pem }
  const certificate = trust.certificate ? new X509Certificate(trust.certificate) : undefined
  const authorities: string[] = (trust.authorities || "").match(/-----BEGIN CERTIFICATE-----[\s\S]*?-----END CERTIFICATE-----/g) || []
  if (!certificate && !authorities.length) throw new Error("Empty PrAImate certificate trust")
  return {
    ca: [...rootCertificates, ...(certificate ? [trust.certificate] : []), ...authorities],
    allowPartialTrustChain: true,
    rejectUnauthorized: true,
    checkServerIdentity(hostname: string, peer: Parameters<typeof checkServerIdentity>[1]) {
      const error = checkServerIdentity(hostname, peer)
      if (error) return error
      if (certificate && peer.fingerprint256 !== certificate.fingerprint256)
        return new Error("Trusted host certificate changed; review the new certificate in Local LLM settings")
    },
  }
}

export function hostCertificates(raw: string | undefined): Record<string, string> {
  if (!raw) return {}
  const value: unknown = JSON.parse(raw)
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error("Invalid PrAImate host certificates")
  if (Object.values(value).some(pem => typeof pem !== "string")) throw new Error("Invalid PrAImate host certificate")
  return value as Record<string, string>
}
