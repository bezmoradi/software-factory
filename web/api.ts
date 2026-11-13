import type { SessionGrant } from './types.ts'

const TOKEN_KEY = 'sf-key'
let token = readToken()

function readToken(): string {
  try {
    return sessionStorage.getItem(TOKEN_KEY) ?? ''
  } catch {
    return ''
  }
}

function keep(value: string): void {
  token = value
  try {
    if (value) sessionStorage.setItem(TOKEN_KEY, value)
    else sessionStorage.removeItem(TOKEN_KEY)
  } catch {}
}

async function dropWorkers() {
  const registrations = await navigator.serviceWorker?.getRegistrations().catch(() => []) ?? []
  await Promise.all(registrations.map(r => r.unregister().catch(() => false)))
}

export function signedIn(): boolean {
  return token !== ''
}

export function forget(): void {
  keep('')
}

export async function exchange(): Promise<void> {
  await dropWorkers()
  if (!token) await trade()
}

export async function trade(): Promise<void> {
  const res = await fetch('/api/session', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}', cache: 'no-store' }).catch(() => null)
  if (!res || !res.ok) return
  const body = (await res.json()) as SessionGrant
  keep(typeof body.token === 'string' ? body.token : '')
}

export function events(): Promise<Response> {
  return fetch('/api/events', { headers: { Authorization: `Bearer ${token}` }, cache: 'no-store' })
}

export function api(path: string, body: unknown): Promise<Response> {
  return fetch(path, { method: 'POST', headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' }, body: JSON.stringify(body), cache: 'no-store' })
}

export function upload(blob: Blob, sid: string, name = ''): Promise<Response> {
  const headers: Record<string, string> = { Authorization: `Bearer ${token}`, 'Content-Type': name ? 'application/octet-stream' : blob.type, 'X-SF-Session': sid }
  if (name) headers['X-SF-Name'] = encodeURIComponent(name)
  return fetch('/api/upload', { method: 'POST', headers, body: blob, cache: 'no-store' })
}
