export const workerTiers = [{ id: 'primary', label: 'Reasoner' }, { id: 'middle', label: 'Middle' }, { id: 'fast', label: 'Fast' }]
export const activeWorkerRun = run => ['running', 'planning', 'merging'].includes(run?.status)
export const isWorkerRun = id => /^workers?-/.test(id || '')
export const workerLabel = tier => workerTiers.find(t => t.id === tier)?.label || tier || 'Worker'
const terminalKinds = new Set(['completed', 'failed', 'cancelled'])

export function workerTaskRoute(task, run) {
  const worker = task?.worker || {}
  const profile = run?.profiles?.find(p => p.tier === (worker.profile || 'middle')) || {}
  const runtime = worker.runtime || (worker.cli ? 'cli' : worker.endpoint ? 'native' : profile.runtime)
  return { runtime, cli: runtime === 'native' ? '' : worker.cli || profile.cli || '', model: worker.model || profile.model || '' }
}

// Invocation routes come from recorded events, so changing a profile cannot
// relabel past work as if it had run on the new CLI/model.
export function workerExecutions(run) {
  const groups = new Map()
  for (const event of run?.events || []) {
    const key = event.workerID || `${event.taskID || 'legacy'}:${event.tier}`
    if (!groups.has(key)) {
      const task = run.dag?.tasks?.find(t => t.id === event.taskID)
      const profile = run.profiles?.find(p => p.tier === event.tier) || {}
      const route = task ? workerTaskRoute(task, run) : profile
      const runtime = event.runtime || route.runtime || ''
      groups.set(key, { id: key, taskID: event.taskID || '', parentID: event.parentID || '', tier: event.tier,
        cli: runtime === 'native' ? '' : event.cli || route.cli || '', model: event.model || route.model || '',
        runtime, workspace: event.workspace || task?.worktree?.path || run.workspace,
        reasoningEffort: event.workerID ? event.reasoningEffort || '' : profile.reasoningEffort || '',
        timeoutSeconds: event.workerID ? event.timeoutSeconds || 0 : profile.timeoutSeconds, startedAt: event.timestamp, updatedAt: event.timestamp,
        assignment: task?.description || (['started', 'input', 'request'].includes(event.kind) ? event.text : ''),
        status: activeWorkerRun(run) ? 'running' : run.status, phase: event.phase || '', step: 0, events: [] })
    }
    const group = groups.get(key)
    group.events.push(event)
    group.updatedAt = event.timestamp
    group.step = event.step || group.step
    if (event.phase) group.phase = event.phase
    if (terminalKinds.has(event.kind)) group.status = event.kind
    else if (event.kind === 'delegation') group.status = 'waiting'
    else if (['request', 'input', 'delegated_result', 'delegated_error'].includes(event.kind)) group.status = 'running'
    if (event.kind === 'request' || event.kind === 'input') group.callStartedAt = event.timestamp
    if (['stream', 'reasoning', 'tool_start', 'tool_end', 'backend_status', 'error'].includes(event.kind)) group.lastProgressAt = event.timestamp
  }
  for (const group of groups.values()) {
    // Cancellation/interruption can precede an invocation's final event.
    if (!activeWorkerRun(run) && ['running', 'waiting'].includes(group.status)) group.status = run.status === 'failed' ? 'interrupted' : run.status
  }
  return [...groups.values()]
}

export function workerEventLabel(event) {
  return ({ started: 'Assignment started', request: 'Model input', input: 'Planning input', stream: 'Model output', reasoning: 'Reported reasoning',
    response: 'Model response', output: 'Plan response', backend_status: 'Backend activity', tool_start: 'Tool started', tool_end: 'Tool finished', tool: 'Tool result',
    delegation: `Delegated to ${workerLabel(event.target)}`, delegated_result: `Returned from ${workerLabel(event.target)}`,
    delegated_error: 'Delegation failed', error: 'Operation error', failed: 'Worker failed', completed: 'Assignment completed',
    cancelled: 'Worker stopped', edited: 'File changed', inspect: 'File inspected', list: 'Directory listed' })[event.kind] || event.kind
}

export function workerUsage(run) {
  let input = 0, output = 0, calls = 0
  for (const event of run?.events || []) {
    if (['response', 'output'].includes(event.kind) && (event.usage?.source || event.usage?.Source) === 'provider') {
      input += event.usage.inputTokens ?? event.usage.InputTokens ?? 0; output += event.usage.outputTokens ?? event.usage.OutputTokens ?? 0; calls++
    }
  }
  return { input, output, calls }
}
