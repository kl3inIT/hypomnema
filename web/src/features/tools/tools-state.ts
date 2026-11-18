export function toolEntries(entries: string[]) {
  return entries.map((entry) => entry.trim()).filter(Boolean)
}
