import { mobileNavigation } from './mobileNavigation.js'

const pages = new Set(mobileNavigation.map(item => item.id))

export function remoteRoute(page, {chat = '', worker = '', terminal = '', agent = ''} = {}) {
  const params = new URLSearchParams({page: pages.has(page) ? page : 'dashboard'})
  if (page === 'chats' && chat) params.set('chat', chat)
  if (page === 'workers' && worker) params.set('worker', worker)
  if (page === 'code' && terminal) params.set('terminal', terminal)
  if (page === 'agents' && agent) params.set('agent', agent)
  return `#${params}`
}

export async function restoreRemoteRoute(hash, stores, loadTerminals) {
  const params = new URLSearchParams(hash.replace(/^#/, ''))
  const page = params.get('page')
  if (params.has('workspace') || !pages.has(page)) return false
  if (page === 'chats' && params.get('chat')) stores.openChatId.set(params.get('chat'))
  if (page === 'workers' && params.get('worker')) stores.openWorkerId.set(params.get('worker'))
  if (page === 'agents' && params.get('agent')) stores.agentStudio.set({id:params.get('agent')})
  if (page === 'code' && params.get('terminal')) {
    const sessions = await loadTerminals()
    const session = sessions?.find(item => item.id === params.get('terminal'))
    if (session) stores.pendingTerm.set({termId:session.id,chatId:session.chatId || '',cli:session.name,cwd:session.cwd})
  }
  stores.activePage.set(page)
  return true
}
