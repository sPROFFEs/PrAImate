export function canConfigureServer(edition, remoteRole) {
  return edition === 'server' && (remoteRole == null || remoteRole === 'admin')
}

export function visibleNavigation(pages, edition, remoteRole) {
  return pages.filter(page => page.id !== 'server' || canConfigureServer(edition, remoteRole))
}
