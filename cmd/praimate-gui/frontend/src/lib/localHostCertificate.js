export async function probeWithCertificateException({ endpoint, probe, inspect, trust, confirm, current = () => true }) {
  try { return { models: await probe(), trusted: false } }
  catch (error) {
    if (!/x509|certificate signed by unknown authority|tls: failed to verify certificate|certificate is expired or not valid yet|trusted host certificate changed/i.test(String(error)) || !current()) throw error
    const certificate = await inspect(endpoint)
    if (!current()) throw error
    const accepted = await confirm({
      title: 'Trust this host certificate?',
      message: `The certificate for ${certificate.origin} is not trusted by this system. Trust this certificate and its presented issuing CA chain?\nSHA-256: ${certificate.fingerprint}\nExpires: ${new Date(certificate.expires).toLocaleString()}${certificate.authorities?.length ? '\nIssuing CA SHA-256:\n' + certificate.authorities.join('\n') : ''}\nPrAImate pins this host certificate. OpenCode also receives the approved public certificates for its CLI process. Hostname and expiry checks remain enabled. If the server omits its root CA, import the public CA certificate in Host Settings.`,
      confirmLabel: 'Trust certificate',
      cancelLabel: 'Cancel'
    })
    if (!accepted || !current()) throw error
    await trust(endpoint, certificate.trust || certificate.pem)
    if (!current()) throw error
    return { models: await probe(), trusted: true }
  }
}
