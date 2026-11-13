export type DiffLine = { kind: 'same' | 'added' | 'removed'; text: string }

const MAX_CELLS = 1_000_000
const CONTEXT = 2

export function lineDiff(before: string, after: string): DiffLine[] | null {
  const a = before.split('\n')
  const b = after.split('\n')
  let start = 0
  while (start < a.length && start < b.length && a[start] === b[start]) start++
  let endA = a.length
  let endB = b.length
  while (endA > start && endB > start && a[endA - 1] === b[endB - 1]) {
    endA--
    endB--
  }
  const n = endA - start
  const m = endB - start
  if ((n + 1) * (m + 1) > MAX_CELLS) return null
  const width = m + 1
  const lcs = new Uint32Array((n + 1) * width)
  const at = (i: number, j: number) => lcs[i * width + j] ?? 0
  const lineA = (i: number) => a[start + i] ?? ''
  const lineB = (j: number) => b[start + j] ?? ''
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      lcs[i * width + j] = lineA(i) === lineB(j) ? at(i + 1, j + 1) + 1 : Math.max(at(i + 1, j), at(i, j + 1))
    }
  }
  const out: DiffLine[] = a.slice(0, start).map(text => ({ kind: 'same', text }))
  let i = 0
  let j = 0
  while (i < n || j < m) {
    if (i < n && j < m && lineA(i) === lineB(j)) {
      out.push({ kind: 'same', text: lineA(i) })
      i++
      j++
    } else if (j < m && (i === n || at(i, j + 1) > at(i + 1, j))) {
      out.push({ kind: 'added', text: lineB(j) })
      j++
    } else {
      out.push({ kind: 'removed', text: lineA(i) })
      i++
    }
  }
  for (const text of a.slice(endA)) out.push({ kind: 'same', text })
  return out
}

export function diffText(lines: DiffLine[]): { text: string; added: number; removed: number } {
  const keep = lines.map((l, i) => l.kind !== 'same' || lines.slice(Math.max(0, i - CONTEXT), i + CONTEXT + 1).some(x => x.kind !== 'same'))
  const parts: string[] = []
  let skipped = 0
  lines.forEach((l, i) => {
    if (!keep[i]) {
      skipped++
      return
    }
    if (skipped) parts.push(`  … ${skipped} unchanged ${skipped === 1 ? 'line' : 'lines'}`)
    skipped = 0
    parts.push(`${l.kind === 'added' ? '+' : l.kind === 'removed' ? '-' : ' '} ${l.text}`)
  })
  if (skipped) parts.push(`  … ${skipped} unchanged ${skipped === 1 ? 'line' : 'lines'}`)
  return { text: parts.join('\n'), added: lines.filter(l => l.kind === 'added').length, removed: lines.filter(l => l.kind === 'removed').length }
}
