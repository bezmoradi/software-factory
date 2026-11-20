import { el } from './dom.ts'

export function saveFile(blob: Blob, name: string): void {
  const url = URL.createObjectURL(blob)
  const a = el('a')
  a.href = url
  a.download = name
  document.body.append(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 60_000)
}
