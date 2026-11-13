import type { EngineInterface } from 'claude-code'

export type Reply = { status: number; ok: boolean; text: string }

export const DEFAULT_SOCKET = '/run/sf/plugin.sock'

export const link = { sock: '', secret: '', push: '' }

export async function socketPath($: EngineInterface): Promise<string> {
  const given = (await $.env.get('SF_PLUGIN_SOCKET').catch(() => undefined)) ?? ''
  return given.startsWith('/') && given.length <= 103 && !given.includes('\0') ? given : DEFAULT_SOCKET
}

export function engine($: EngineInterface, path: string, body?: unknown, secret = link.secret): Promise<Reply> {
  if (!link.sock) return Promise.reject(new Error('software-factory: no engine socket'))
  const headers: Record<string, string> = { 'content-type': 'application/json' }
  if (secret) headers['x-sf-conn'] = secret
  return $.http.fetch(`http://engine${path}`, {
    method: body === undefined ? 'GET' : 'POST',
    socketPath: link.sock,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  })
}

export function parsed(text: string): Record<string, unknown> | null {
  try {
    const v: unknown = JSON.parse(text)
    return v && typeof v === 'object' && !Array.isArray(v) ? (v as Record<string, unknown>) : null
  } catch {
    return null
  }
}
