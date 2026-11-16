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

const REQUEST_ID = /^[0-9a-f]{16}$/

export function requestOf(res: Response): string {
  const id = res.headers.get('X-SF-Request') ?? ''
  return REQUEST_ID.test(id) ? id : ''
}

let failed = { id: '', at: 0 }

export function lastFailedRequest(within = 10_000): string {
  return failed.id && Date.now() - failed.at <= within ? failed.id : ''
}

async function named(res: Response): Promise<Response> {
  const id = requestOf(res)
  if (res.ok || !id) return res
  failed = { id, at: Date.now() }
  if (!(res.headers.get('Content-Type') ?? '').startsWith('application/json')) return res
  const body: unknown = await res.clone().json().catch(() => null)
  if (!body || typeof body !== 'object' || Array.isArray(body)) return res
  const got = { ...(body as Record<string, unknown>) }
  const said = typeof got.message === 'string' ? got.message.trim() : ''
  if (said) got.message = `${said} Request id ${id}.`
  got.request = id
  return new Response(JSON.stringify(got), { status: res.status, statusText: res.statusText, headers: res.headers })
}

export function api(path: string, body: unknown): Promise<Response> {
  return fetch(path, { method: 'POST', headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' }, body: JSON.stringify(body), cache: 'no-store' }).then(named)
}

export function sendState(file: Blob): Promise<Response> {
  return fetch('/api/import', { method: 'POST', headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/gzip' }, body: file, cache: 'no-store' }).then(named)
}

export function upload(blob: Blob, sid: string, name = ''): Promise<Response> {
  const headers: Record<string, string> = { Authorization: `Bearer ${token}`, 'Content-Type': name ? 'application/octet-stream' : blob.type, 'X-SF-Session': sid }
  if (name) headers['X-SF-Name'] = encodeURIComponent(name)
  return fetch('/api/upload', { method: 'POST', headers, body: blob, cache: 'no-store' }).then(named)
}
