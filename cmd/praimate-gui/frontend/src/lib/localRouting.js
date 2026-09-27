export const LOCAL_ROUTABLE_CLIS = Object.freeze(['openclaude', 'opencode', 'praimate-code', 'praimate-cli'])

export function supportsLocalRouting(cli) {
  return LOCAL_ROUTABLE_CLIS.includes(cli)
}

export function localRoutingUnavailableMessage(cli) {
  if (cli === 'claude') {
    return 'Claude Code uses Anthropic routing. Choose OpenClaude to run Claude-style agents with a local OpenAI-compatible model.'
  }
  if (cli === 'codex') {
    return 'Codex keeps its own provider configuration and is not modified by PrAImate.'
  }
  return `${cli || 'This CLI'} cannot use a PrAImate-managed local route.`
}
