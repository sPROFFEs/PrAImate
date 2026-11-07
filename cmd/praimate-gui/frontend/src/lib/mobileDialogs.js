// Android Back cancels the top dialog; it must never approve an operation.
export function dismissMobileDialog(document) {
  const dialog=[...document.querySelectorAll('[role="dialog"],[role="alertdialog"]')].filter(node=>node.getClientRects().length).at(-1)
  if(!dialog)return false
  const escape=new KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true})
  dialog.dispatchEvent(escape)
  if(escape.defaultPrevented || !dialog.isConnected)return true
  const cancel=[...dialog.querySelectorAll('button')].find(button=>!button.disabled && /^(cancel|close|back|dismiss)$/i.test(button.getAttribute('aria-label') || button.textContent.trim()))
  if(!cancel)return false
  cancel.click()
  return true
}
