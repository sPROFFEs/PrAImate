// Restore busy state and permission requests without starting another model turn.
export function createRemoteChatSync({api,chatID,ownsTurn,role,apply,onError,approvalIDs=()=>[chatID()]}) {
  let revision=0,disposed=false
  return {
    async refresh(){
      const id=chatID(),current=++revision
      if(!id||disposed)return
      const [activity,history]=await Promise.allSettled([api.remoteActivity(),api.chatMessages(id)])
      if(disposed||current!==revision||id!==chatID())return
      const next={}
      if(activity.status==='fulfilled'){
        const allowed=approvalIDs().filter(Boolean)
        next.approvals=role()==='viewer'?[]:(activity.value.approvals||[]).filter(request=>allowed.includes(request.chatId))
        if(!ownsTurn())next.sending=(activity.value.chats||[]).includes(id)
      }
      if(history.status==='fulfilled'&&!ownsTurn())next.messages=history.value||[]
      apply(next)
      for(const result of [activity,history])if(result.status==='rejected')onError(result.reason)
    },
    dispose(){disposed=true;revision++},
  }
}
