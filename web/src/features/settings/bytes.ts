// Sizes as the owner writes a BigQuery cap (issue #976), as `hypomnema integrations max-bytes` reads them.

const units: [string, number][] = [
  ["TiB", 2 ** 40],
  ["GiB", 2 ** 30],
  ["MiB", 2 ** 20],
  ["KiB", 2 ** 10],
  ["TB", 1e12],
  ["GB", 1e9],
  ["MB", 1e6],
  ["KB", 1e3],
  ["B", 1],
]

// parseSize reads a size as the owner writes it, 10GiB or 500MB or a number of bytes; undefined when it is none.
export function parseSize(text: string): number | undefined {
  const given = text.trim()
  const unit = units.find(([suffix]) => given.endsWith(suffix))
  const number = Number((unit ? given.slice(0, -unit[0].length) : given).trim())
  const bytes = Math.floor(number * (unit ? unit[1] : 1))

  return Number.isFinite(bytes) && bytes >= 1 ? bytes : undefined
}

// formatSize writes a number of bytes as the owner writes a cap: 10GiB.
export function formatSize(bytes: number): string {
  let value = bytes
  let i = 0
  const names = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"]
  while (value >= 1024 && i < names.length - 1) {
    value /= 1024
    i++
  }

  return `${Number.isInteger(value) ? value : value.toFixed(1)}${names[i]}`
}
