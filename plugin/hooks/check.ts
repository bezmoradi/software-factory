import type { EngineInterface } from 'claude-code'

import { engine, link, parsed } from './transport.ts'

export type Checked = { kind: string; waits: boolean; pushes: boolean; protected: string; held: string; engine: boolean }

const MAX_INPUT = 1024 * 1024
const ASK_MS = 5000
const DOWN_MS = 10000
const KEPT_MS = 10000
const CONTENT_KEYS = ['content', 'new_string', 'old_string', 'edits', 'new_source']
const REMOTE = new Set([
  'RemoteTrigger', 'PublishPlugin', 'SendToProject', 'SendToProjectContext', 'SendFile', 'SendUserFile', 'SendFeedback', 'DesignSync', 'SendMessage',
  'Artifact', 'ArtifactCheck', 'ArtifactComments', 'ArtifactData', 'AppifactRepl', 'ClaudeDesign', 'ShareOnboardingGuide', 'PushNotification',
  'memory_write', 'request_computer',
])
const SHELLS = new Set(['Bash', 'Monitor', 'PowerShell'])
const LOOSE_PUSH = /\bgit\b[\s\S]*\b(push|send-pack)\b|(^|[\s;&|(`$])(gh|glab)\s/
const FORGE_MCP = /^mcp__.*(github|gitlab)/i
const PUSH_TOOL = /^mcp__[^_][\w.-]*__factory_push$/

const kept = new Map<string, { at: number; found: Checked }>()
let downUntil = 0

export const isPushTool = (tool: string) => PUSH_TOOL.test(tool)

export function fallback(tool: string, input: Record<string, unknown>): Checked {
  const command = SHELLS.has(tool) && typeof input.command === 'string' ? input.command : ''
  const remote = REMOTE.has(tool) || ((tool === 'Agent' || tool === 'Task') && input.isolation === 'remote') || (tool === 'Projects' && input.method === 'project_write')
  const mcp = tool.startsWith('mcp__') && !isPushTool(tool)
  return { kind: 'unknown', waits: remote || mcp, pushes: LOOSE_PUSH.test(command) || FORGE_MCP.test(tool), protected: '', held: '', engine: false }
}

function sized(input: Record<string, unknown>): Record<string, unknown> | null {
  if (JSON.stringify(input).length <= MAX_INPUT) return input
  const slim = Object.fromEntries(Object.entries(input).filter(([k]) => !CONTENT_KEYS.includes(k)))
  return JSON.stringify(slim).length <= MAX_INPUT ? slim : null
}

function checkedOf(v: Record<string, unknown> | null): Checked | null {
  if (!v || typeof v.kind !== 'string' || v.kind.length > 40 || typeof v.waits !== 'boolean' || typeof v.pushes !== 'boolean') return null
  const why = v.protected === undefined || v.protected === null ? '' : v.protected
  if (typeof why !== 'string' || why.length > 500) return null
  const held = v.held === undefined || v.held === null ? '' : v.held
  if (typeof held !== 'string' || held.length > 2000) return null
  return { kind: v.kind, waits: v.waits, pushes: v.pushes, protected: why, held, engine: true }
}

async function ask($: EngineInterface, body: unknown): Promise<Checked | null> {
  let give: (v: null) => void = () => undefined
  const late = new Promise<null>(resolve => (give = resolve))
  const timer = $.clock.after(ASK_MS, () => give(null))
  const asked = engine($, '/check', body)
    .then(r => (r.status === 200 ? checkedOf(parsed(r.text)) : null))
    .catch(() => null)
  const answer = await Promise.race([asked, late])
  timer.cancel()
  return answer
}

export async function check($: EngineInterface, tool: string, input: Record<string, unknown>, place: { top: string; paths: string[] }): Promise<Checked> {
  const key = JSON.stringify([tool, input, place.top, place.paths])
  const now = Date.now()
  const known = kept.get(key)
  if (known && now - known.at < KEPT_MS) return known.found
  const sent = sized(input)
  if (!sent || !link.secret || now < downUntil) return fallback(tool, input)
  const found = await ask($, { tool, input: sent, top: place.top, paths: place.paths })
  if (!found) {
    downUntil = Date.now() + DOWN_MS
    return fallback(tool, input)
  }
  kept.set(key, { at: Date.now(), found })
  while (kept.size > 100) kept.delete(kept.keys().next().value as string)
  return found
}
