export function localChoiceKey(options, endpoint, model) {
  const item = (options?.allModels || []).find((entry) => entry.endpoint === endpoint && entry.model === model)
  return item ? `${item.hostId}::${item.model}` : ''
}

export function localChoice(options, value) {
  return (options?.allModels || []).find((entry) => `${entry.hostId}::${entry.model}` === value)
}
