// A remote renderer is not the owner of other views' permission decisions.
// Desktop-only windows retain their existing fail-closed behavior.
export function ownsApproval(request,{chatIDs,remote,role,resolve}) {
  if(remote && role==='viewer')return false
  const owned=!!request.chatId && chatIDs.filter(Boolean).includes(request.chatId)
  if(!owned && !remote){
    try{resolve(request.id,false,false)?.catch(()=>{})}catch{}
  }
  return owned
}
