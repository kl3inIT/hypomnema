export function firstLine(text: string, max = 120) {
  const line = text.trim().split("\n")[0] ?? ""

  return line.length > max ? `${line.slice(0, max - 1)}…` : line
}
