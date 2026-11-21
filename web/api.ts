import { visible } from './types.ts'
import type { EngineAnswer, SessionGrant } from './types.ts'

export type { EngineAnswer }

export const NO_ANSWER = 'The engine didn’t answer. Check that it is running.'
export const UNREAD = 'The engine’s answer couldn’t be read. Check that it is running.'

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
  const grant = (await answerOf(res)).body as Partial<SessionGrant>
  keep(typeof grant.token === 'string' ? grant.token : '')
}

export function events(): Promise<Response> {
  return fetch('/api/events', { headers: { Authorization: `Bearer ${token}` }, cache: 'no-store' })
}

function api(path: string, body: unknown): Promise<Response> {
  return fetch(path, { method: 'POST', headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' }, body: JSON.stringify(body), cache: 'no-store' })
}

function text(v: unknown): string {
  return typeof v === 'string' ? v : ''
}

export async function answerOf(res: Response | null): Promise<EngineAnswer> {
  if (!res) return { ok: false, status: 0, error: '', field: '', message: NO_ANSWER, body: {} }
  const parsed: unknown = await res.json().catch(() => null)
  const body = parsed !== null && typeof parsed === 'object' && !Array.isArray(parsed) ? (parsed as Record<string, unknown>) : {}
  const message = visible(text(body.message))
  return { ok: res.ok, status: res.status, error: text(body.error), field: text(body.field), message: res.ok || message ? message : UNREAD, body }
}

export function savedTone(a: EngineAnswer): 'ok' | 'warn' {
  return a.body.tone === 'warn' ? 'warn' : 'ok'
}

export async function request(path: string, body: unknown = {}): Promise<EngineAnswer> {
  return answerOf(await api(path, body).catch(() => null))
}

export interface Download {
  answer: EngineAnswer
  file: Blob | null
  type: string
  disposition: string
}

export async function download(path: string, body: unknown = {}): Promise<Download> {
  const res = await api(path, body).catch(() => null)
  if (!res?.ok) return { answer: await answerOf(res), file: null, type: '', disposition: '' }
  const file = await res.blob().catch(() => null)
  return { answer: { ok: true, status: res.status, error: '', field: '', message: '', body: {} }, file, type: res.headers.get('Content-Type') ?? '', disposition: res.headers.get('Content-Disposition') ?? '' }
}

export async function sendState(file: Blob): Promise<EngineAnswer> {
  return answerOf(await fetch('/api/import', { method: 'POST', headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/gzip' }, body: file, cache: 'no-store' }).catch(() => null))
}

export async function upload(blob: Blob, sid: string, name = ''): Promise<EngineAnswer> {
  const headers: Record<string, string> = { Authorization: `Bearer ${token}`, 'Content-Type': name ? 'application/octet-stream' : blob.type, 'X-SF-Session': sid }
  if (name) headers['X-SF-Name'] = encodeURIComponent(name)
  return answerOf(await fetch('/api/upload', { method: 'POST', headers, body: blob, cache: 'no-store' }).catch(() => null))
}
