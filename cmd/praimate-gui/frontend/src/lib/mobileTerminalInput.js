export function terminalTextPayload(text, bracketedPaste = false) {
  const value = String(text).replace(/\r\n|\n/g, '\r')
  return (bracketedPaste ? `\x1b[200~${value}\x1b[201~` : value) + '\r'
}

// Keep text until delivery is acknowledged. Each terminal owns its draft, so a
// response from a previous session cannot clear text in the newly selected one.
export function createTerminalComposer({write, bracketedPaste = () => false, changed = () => {}}) {
  const drafts = new Map()
  let terminalID = ''
  let disposed = false
  const entry = id => {
    if (!drafts.has(id)) drafts.set(id, {draft:'',busy:false,error:'',code:''})
    return drafts.get(id)
  }
  const snapshot = () => ({terminalID, ...(terminalID ? entry(terminalID) : {draft:'',busy:false,error:'',code:''})})
  const notify = () => { if (!disposed) changed(snapshot()) }
  return {
    snapshot,
    select(id) { id=id || ''; if (terminalID===id) return; terminalID=id; notify() },
    edit(text) { if (!terminalID || entry(terminalID).busy) return; entry(terminalID).draft=text; notify() },
    warn(id, message, code = '') {
      if (!id) return
      Object.assign(entry(id), {error:message,code})
      if (id===terminalID) notify()
    },
    clearError() { if (terminalID) { Object.assign(entry(terminalID),{error:'',code:''});notify() } },
    async send() {
      const id=terminalID
      if (!id || entry(id).busy || !entry(id).draft) return false
      const state=entry(id), text=state.draft
      Object.assign(state,{busy:true,error:'',code:''});notify()
      try {
        const accepted=await write(id,terminalTextPayload(text,bracketedPaste(id)))
        if (accepted===false) throw new Error('Input delivery could not be confirmed. Check terminal output before sending again.')
        if (state.draft===text) state.draft=''
        return true
      } catch(error) {
        if (!state.error) { state.error=error.message || String(error);state.code=error.code || '' }
        return false
      } finally { state.busy=false;notify() }
    },
    dispose() { disposed=true },
  }
}
