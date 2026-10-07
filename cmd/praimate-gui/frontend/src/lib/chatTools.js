const levels = [
  { id: '', label: 'Safe', hint: 'Read and answer with the CLI permission policy' },
  { id: 'ask', label: 'Ask', hint: 'Request approval where supported; other CLIs use their safe policy' },
  { id: 'edits', label: 'Edits', hint: 'Automatically accept file edits' },
  { id: 'full', label: 'Full', hint: 'Automatically approve tools' },
]

export function toolLevelsForCli(cli) {
  if (cli === 'opencode' || cli === 'praimate-code') return [
    { id: 'plan', label: 'Plan', hint: 'Plan without editing files' },
    { id: '', label: 'Build', hint: 'Use the CLI build agent and permission policy' },
    levels[3],
  ]
  if (cli === 'antigravity') return [
    { id: '', label: 'Safe', hint: 'Plan without automatically accepting edits' },
    { id: 'plan', label: 'Plan', hint: 'Plan without automatically accepting edits' },
    levels[2], levels[3],
  ]
  if (cli === 'copilot') return levels.filter(level => level.id !== 'ask')
  return levels
}

export function normalizeToolsForCli(cli, value) {
  return toolLevelsForCli(cli).some(level => level.id === value) ? value : ''
}
