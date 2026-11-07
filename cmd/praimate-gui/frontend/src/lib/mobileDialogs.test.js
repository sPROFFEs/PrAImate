import test from 'node:test'
import assert from 'node:assert/strict'
import { dismissMobileDialog } from './mobileDialogs.js'

globalThis.KeyboardEvent=class extends Event {constructor(type,options){super(type,options);this.key=options.key}}
const button=(text,disabled=false)=>({textContent:text,disabled,clicked:false,getAttribute:()=>null,click(){this.clicked=true}})
const dialog=(buttons,escape=false)=>({isConnected:true,getClientRects:()=>[{}],querySelectorAll:()=>buttons,dispatchEvent:event=>{if(escape)event.preventDefault()}})
const document=(...dialogs)=>({querySelectorAll:()=>dialogs})

test('Back closes only the top dialog and never chooses approval buttons',()=>{
  const lower=button('Cancel'),approve=button('Allow'),cancel=button('Cancel')
  assert.equal(dismissMobileDialog(document(dialog([lower]),dialog([approve,cancel]))),true)
  assert.equal(lower.clicked,false);assert.equal(approve.clicked,false);assert.equal(cancel.clicked,true)
})
test('an Escape handler is not followed by a second cancellation',()=>{
  const cancel=button('Close')
  assert.equal(dismissMobileDialog(document(dialog([cancel],true))),true)
  assert.equal(cancel.clicked,false)
})
test('Back never accepts a mandatory notice or clicks a disabled button',()=>{
  const accept=button('I understand — continue'),cancel=button('Cancel',true)
  assert.equal(dismissMobileDialog(document(dialog([accept,cancel]))),false)
  assert.equal(accept.clicked,false);assert.equal(cancel.clicked,false)
  assert.equal(dismissMobileDialog(document()),false)
})
