import type { EngineInterface } from 'claude-code'

import { engine, link, parsed } from './transport.ts'

export type Checked = { kind: string; waits: boolean; pushes: boolean; protected: string; held: string; cloud: string; irreversible: boolean; guarded: string; engine: boolean }

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
const LOOSE_PUSH: RegExp[][] = [[/\bgit\b/, /\b(push|send-pack)\b/], [/(^|[\s;&|(`$])(gh|glab)\s/]]
const FORGE_MCP = /^mcp__.*(github|gitlab)/i
const PUSH_TOOL = /^mcp__[^_][\w.-]*__factory_push$/
const CLOUD_REASON = "this runs on the vendor's servers, outside Software Factory"
const LEAD = String.raw`(?:(?:sudo|doas|env|command|exec|nohup)(?:\s+-\S+)*\s+)*`
const DOWNLOADER = String.raw`(?:curl|wget|fetch|aria2c|irm|iwr|invoke-webrequest|invoke-restmethod)`
const RUNNER = String.raw`(?:sh|bash|zsh|dash|ksh|ash|fish|python[\d.]*|node|perl|ruby|php|iex|invoke-expression)`
const LOOSE_CLOUD: RegExp[][] = [
  [/\bclaude\b/i, /--(?:remote|cloud|teleport|remote-control|rc)\b|\b(?:remote-control|rc)\b/i],
  [/\bclaude\b/i, /\bultrareview\b/i, /--post\b/i],
]
const LOOSE_UNDONE: RegExp[][] = [
  [/\bgit\b/i, /\breset\b/i, /--hard/i],
  [/\bgit\b/i, /\bclean\b/i, /\s-(?:-force|[a-z]*f)/i],
  [/\b(?:terraform|tofu|terragrunt)\b/i, /\bdestroy\b/i],
  [/\b(?:kubectl|oc)\b/i, /\bdelete\b/i],
  [/\b(?:drop|truncate|dropdb)\b/i],
  [/\bclaude\b/i],
  [new RegExp(String.raw`\b${DOWNLOADER}\b`, 'i'), new RegExp(String.raw`\|&?\s*${LEAD}${RUNNER}\b`, 'i')],
  [new RegExp(String.raw`(?:^|[\s;&|])\.\s+<\(\s*${DOWNLOADER}\b`, 'i')],
]
const LOOSE_UNDONE_STEP: RegExp[][] = [
  [/\brm\b/i, /\s-(?:-recursive|[a-z]*r)/i],
  [/\bfind\b/i, /\s-delete\b/i],
  [new RegExp(String.raw`\b(?:${RUNNER}|eval|source)\b`, 'i'), new RegExp(String.raw`(?:<\(|\$\(|\`)\s*${LEAD}${DOWNLOADER}\b`, 'i')],
]

const kept = new Map<string, { at: number; found: Checked }>()
let downUntil = 0

export const isPushTool = (tool: string) => PUSH_TOOL.test(tool)

function inOrder(text: string, steps: RegExp[]): boolean {
  let left = text
  for (const step of steps) {
    const m = step.exec(left)
    if (!m) return false
    left = left.slice(m.index + m[0].length)
  }
  return true
}

function looseUndone(command: string): boolean {
  if (LOOSE_UNDONE.some(steps => inOrder(command, steps))) return true
  return command.split(/[\n;&|]/).some(step => LOOSE_UNDONE_STEP.some(steps => inOrder(step, steps)))
}

export function fallback(tool: string, input: Record<string, unknown>): Checked {
  const command = SHELLS.has(tool) && typeof input.command === 'string' ? input.command : ''
  const remote = REMOTE.has(tool) || ((tool === 'Agent' || tool === 'Task') && input.isolation === 'remote') || (tool === 'Projects' && input.method === 'project_write')
  const mcp = tool.startsWith('mcp__') && !isPushTool(tool)
  const cloud = LOOSE_CLOUD.some(steps => inOrder(command, steps)) ? CLOUD_REASON : ''
  return { kind: 'unknown', waits: remote || mcp, pushes: LOOSE_PUSH.some(steps => inOrder(command, steps)) || FORGE_MCP.test(tool), protected: '', held: '', cloud, irreversible: command !== '' && looseUndone(command), guarded: '', engine: false }
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
  const cloud = v.cloud === undefined || v.cloud === null ? '' : v.cloud
  if (typeof cloud !== 'string' || cloud.length > 500) return null
  const irreversible = v.irreversible === undefined ? false : v.irreversible
  if (typeof irreversible !== 'boolean') return null
  const guarded = v.guarded === undefined || v.guarded === null ? '' : v.guarded
  if (typeof guarded !== 'string' || guarded.length > 500) return null
  return { kind: v.kind, waits: v.waits, pushes: v.pushes, protected: why, held, cloud, irreversible, guarded, engine: true }
}

type Asked = { found: Checked | null; down: boolean }

async function ask($: EngineInterface, body: unknown): Promise<Asked> {
  const down: Asked = { found: null, down: true }
  let give: (v: Asked) => void = () => undefined
  const late = new Promise<Asked>(resolve => (give = resolve))
  const timer = $.clock.after(ASK_MS, () => give(down))
  const asked = engine($, '/check', body)
    .then(r => ({ found: r.status === 200 ? checkedOf(parsed(r.text)) : null, down: false }))
    .catch(() => down)
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
  const { found, down } = await ask($, { tool, input: sent, top: place.top, paths: place.paths })
  if (!found) {
    if (down) downUntil = Date.now() + DOWN_MS
    return fallback(tool, input)
  }
  kept.set(key, { at: Date.now(), found })
  while (kept.size > 100) kept.delete(kept.keys().next().value as string)
  return found
}
