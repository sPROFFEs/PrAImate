// Bulk choices share the core's 128-binding limit. Keep existing choices and
// load newly selected skills on demand rather than pinning the entire library.
export function selectVisibleSkills(versions, visible) {
  const selected = new Map()
  for (const version of versions) {
    if (version.selected && version.approved && !selected.has(version.ref) && selected.size < 128) {
      selected.set(version.ref, version.digest)
    }
  }
  for (const version of visible) {
    if (version.approved && !selected.has(version.ref) && selected.size < 128) {
      selected.set(version.ref, version.digest)
    }
  }
  return {
    limited: visible.some(version => version.approved && !selected.has(version.ref)),
    versions: versions.map(version => {
      const chosen = selected.has(version.ref) && selected.get(version.ref) === version.digest
      return { ...version, selected: chosen, activation: chosen && !version.selected ? 'auto' : version.activation }
    })
  }
}
