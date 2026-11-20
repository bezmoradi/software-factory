import { el } from './dom.ts'

const MAX_DATA = 34 << 20

export function pngBlob(data: string): Blob | null {
  if (data.length > MAX_DATA || !/^[A-Za-z0-9+/]*={0,2}$/.test(data)) return null
  let raw: string
  try {
    raw = atob(data)
  } catch {
    return null
  }
  const bytes = new Uint8Array(raw.length)
  for (let i = 0; i < raw.length; i++) bytes[i] = raw.charCodeAt(i)
  if (bytes.length < 8 || bytes[0] !== 0x89 || bytes[1] !== 0x50 || bytes[2] !== 0x4e || bytes[3] !== 0x47) return null
  return new Blob([bytes], { type: 'image/png' })
}

export function saveBlob(blob: Blob, name: string): void {
  const url = URL.createObjectURL(blob)
  const a = el('a')
  a.href = url
  a.download = name
  document.body.append(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 60_000)
}
