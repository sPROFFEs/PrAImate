import test from 'node:test'
import assert from 'node:assert/strict'
import { shouldSendMessage } from './composerKeyboard.js'

test('a phone return key adds a line; explicit hardware shortcuts can send',()=>{
  assert.equal(!!shouldSendMessage({key:'Enter'},true),false)
  assert.equal(!!shouldSendMessage({key:'Enter',ctrlKey:true},true),true)
  assert.equal(!!shouldSendMessage({key:'Enter',metaKey:true},true),true)
})
test('desktop Enter behavior, multiline input and IME composition are preserved',()=>{
  assert.equal(!!shouldSendMessage({key:'Enter'}),true)
  for(const event of [{key:'Enter',shiftKey:true},{key:'Enter',isComposing:true},{key:'Enter',keyCode:229},{key:'a'}]) {
    assert.equal(!!shouldSendMessage(event),false)
    assert.equal(!!shouldSendMessage({...event,ctrlKey:true},true),false)
  }
})
