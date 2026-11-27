import type { ModelUsage } from 'claude-code'

const HIDDEN = '\\u000b-\\u001f\\u007f-\\u009f\\u00ad\\u061c\\u180e\\u200b-\\u200f\\u2028-\\u202e\\u2060-\\u2069\\ufe00-\\ufe0f\\ufeff'
const INVISIBLE = new RegExp(`[\\u0000-\\u0008${HIDDEN}]|[\\u{e0000}-\\u{e007f}]`, 'gu')
const SHELLS = new Set(['Bash', 'Monitor', 'PowerShell'])

export type Update = { type: string; destination?: string; rules?: { toolName: string; ruleContent?: string }[]; mode?: string; directories?: string[]; behavior?: string }

export function visible(text: string): string {
  return text.replace(INVISIBLE, c => `\\u{${c.codePointAt(0)!.toString(16).padStart(4, '0')}}`)
}

export function clip(s: unknown, n: number): string {
  const t = typeof s === 'string' ? s : s === undefined || s === null ? '' : JSON.stringify(s)
  return t.length > n ? `${t.slice(0, n)}…` : t
}

export function summarize(tool: string, input: Record<string, unknown>): string {
  const pick = (...keys: string[]) => keys.map(k => input[k]).find(v => typeof v === 'string' && v) as string | undefined
  if (SHELLS.has(tool)) return clip(pick('command', 'description') ?? '', 300)
  if (tool === 'Agent' || tool === 'Task') return clip(`${pick('subagent_type') ?? 'agent'}: ${pick('description') ?? ''}`, 200)
  if (tool === 'Skill') return clip(pick('skill', 'command') ?? '', 200)
  return clip(pick('file_path', 'path', 'pattern', 'url', 'query', 'notebook_path') ?? '', 200)
}

export function spent(usage: ModelUsage | undefined): number {
  if (!usage) return 0
  const n = usage.input_tokens + usage.output_tokens + usage.cache_read_input_tokens + usage.cache_creation_input_tokens
  return Number.isSafeInteger(n) && n > 0 ? n : 0
}

export function agentTurn(e: { agentId?: string; reason: string; usage?: ModelUsage }): Record<string, unknown> & { type: string } {
  const tokens = spent(e.usage)
  return { type: 'agent_turn', agentId: clip(e.agentId, 200), reason: clip(e.reason, 40), ...(Number.isSafeInteger(tokens) && tokens > 0 ? { tokens } : {}) }
}

const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? '' : 's'}`

function size(bytes: number): string {
  return bytes < 1024 ? `${bytes} B` : bytes < 1048576 ? `${(bytes / 1024).toFixed(1)}KB` : `${(bytes / 1048576).toFixed(1)}MB`
}

function counted(hunks: unknown): { added: number; removed: number } {
  const lines = Array.isArray(hunks) ? hunks.flatMap(hunk => (Array.isArray((hunk as { lines?: unknown }).lines) ? ((hunk as { lines: unknown[] }).lines as string[]) : [])) : []
  return { added: lines.filter(l => typeof l === 'string' && l.startsWith('+')).length, removed: lines.filter(l => typeof l === 'string' && l.startsWith('-')).length }
}

function changes(hunks: unknown): string {
  const { added, removed } = counted(hunks)
  return `Added ${plural(added, 'line')}, removed ${plural(removed, 'line')}`
}

export function callFacts(tool: string, input: Record<string, unknown>): Record<string, unknown> {
  if (!SHELLS.has(tool)) return {}
  const facts: Record<string, unknown> = {}
  if (typeof input.command === 'string' && input.command) facts.command = clip(input.command, 4000)
  if (typeof input.description === 'string' && input.description) facts.description = clip(input.description, 300)
  if (input.dangerouslyDisableSandbox === true) facts.unsandboxed = true
  return facts
}

export function endFacts(tool: string, ran: { deny?: string; isError?: boolean; text?: string; result?: unknown }): Record<string, unknown> {
  try {
    if (ran.deny !== undefined) return { denied: true }
    if (SHELLS.has(tool) && ran.isError) {
      const lines = String(ran.text ?? '').split('\n')
      const code = /^Exit code (\d{1,3})\s*$/.exec(lines[0] ?? '')
      if (code) lines.shift()
      const tail = lines.filter(l => l.trim()).slice(-5).map(l => clip(l, 300))
      return { ...(code ? { exit: Number(code[1]) } : {}), ...(tail.length ? { tail } : {}) }
    }
    if (ran.isError) return {}
    const r = (ran.result ?? {}) as Record<string, any>
    if (tool === 'Write' && r.type === 'create') return { added: String(r.content ?? '').replace(/\n$/, '').split('\n').length, removed: 0 }
    if (tool === 'Edit' || tool === 'MultiEdit' || tool === 'Write') return Array.isArray(r.structuredPatch) ? counted(r.structuredPatch) : {}
  } catch {
    return {}
  }
  return {}
}

export function outcome(tool: string, ran: { deny?: string; isError?: boolean; text?: string; result?: unknown }): string {
  const first = (t: unknown) => clip(String(t ?? '').trim().split('\n')[0], 200)
  if (ran.deny !== undefined) return first(ran.deny) || 'Denied'
  if (ran.isError) return first(ran.text) || 'Failed'
  const r = (ran.result ?? {}) as Record<string, any>
  try {
    if (tool === 'Bash') {
      if (r.backgroundTaskId) return 'Running in the background'
      if (r.interrupted) return 'Interrupted'
      const lines = String(r.stdout || r.stderr || '').trimEnd().split('\n').filter((l, _i, a) => a.length > 1 || l)
      if (!lines.length) return 'No output'
      const shown = lines.slice(0, 3).map(l => clip(l, 200)).join('\n')
      return lines.length > 3 ? `${shown}\n… +${plural(lines.length - 3, 'line')}` : shown
    }
    if (tool === 'Read') return r.type === 'text' && typeof r.file?.numLines === 'number' ? `Read ${plural(r.file.numLines, 'line')}` : r.type === 'image' ? 'Read an image' : ''
    if (tool === 'Edit') return changes(r.structuredPatch)
    if (tool === 'Write') return r.type === 'create' ? `Wrote ${plural(String(r.content ?? '').split('\n').length, 'line')}` : changes(r.structuredPatch)
    if (tool === 'WebFetch' && typeof r.bytes === 'number') return `Received ${size(r.bytes)}${typeof r.code === 'number' ? ` (${r.code}${typeof r.codeText === 'string' && r.codeText ? ` ${r.codeText}` : ''})` : ''}`
  } catch {
    return ''
  }
  return ''
}

function bashNotes(input: Record<string, unknown>): string[] {
  const notes: string[] = []
  if (input.dangerouslyDisableSandbox === true) notes.push('⚠ Asks to run outside Claude Code\'s own sandbox (dangerouslyDisableSandbox)')
  if (input.run_in_background === true) notes.push('Runs in the background (run_in_background)')
  if (typeof input.timeout === 'number') notes.push(`Timeout: ${input.timeout} ms`)
  for (const [k, v] of Object.entries(input)) if (!['command', 'description', 'dangerouslyDisableSandbox', 'run_in_background', 'timeout'].includes(k) && v !== undefined && v !== false) notes.push(`${k}: ${JSON.stringify(v)}`)
  return notes
}

export function fullCall(tool: string, input: Record<string, unknown>): string {
  if (tool !== 'Bash' || typeof input.command !== 'string') return JSON.stringify(input, null, 2)
  const notes = bashNotes(input)
  return notes.length ? `${notes.join('\n')}\n\n${input.command}` : input.command
}

const WHERE: Record<string, string> = { session: 'during this session', localSettings: 'in this project', projectSettings: 'in this project, for everyone', userSettings: 'in every project', cliArg: 'during this session' }

const RULE_VERB: Record<string, [string, string]> = { allow: ["don't ask again for", 'allow'], deny: ['always deny', 'deny'], ask: ['always ask before', 'ask'] }

export function describe(u: Update): string {
  const where = WHERE[u.destination ?? ''] ?? ''
  const verb = RULE_VERB[u.behavior ?? '']
  const what = (u.rules ?? []).map(r => (r.toolName === 'Bash' && r.ruleContent ? `${r.ruleContent.replace(/:\*$|\s\*$/, '')} commands` : r.ruleContent ? `${r.toolName}(${r.ruleContent})` : r.toolName)).join(', ')
  const dirs = (u.directories ?? []).join(', ')
  let text = ''
  if (u.type === 'addRules' && verb && what) text = `${verb[0]} ${what}`
  else if (u.type === 'replaceRules' && verb && what) text = `replace the ${verb[1]} rules with ${what}`
  else if (u.type === 'removeRules' && verb && what) text = `remove the ${verb[1]} rules for ${what}`
  else if (u.type === 'setMode' && u.mode === 'acceptEdits') text = 'allow all edits'
  else if (u.type === 'setMode' && u.mode) text = `switch to ${u.mode} mode`
  else if (u.type === 'addDirectories' && dirs) text = `allow access to ${dirs}`
  else if (u.type === 'removeDirectories' && dirs) text = `remove access to ${dirs}`
  const full = text ? `${text} ${where}`.trim() : ''
  return full.length > 300 ? '' : full
}

export function autoModeOff(settings: unknown): boolean {
  const s = settings && typeof settings === 'object' && !Array.isArray(settings) ? (settings as Record<string, unknown>) : {}
  const permissions = s.permissions && typeof s.permissions === 'object' && !Array.isArray(s.permissions) ? (s.permissions as Record<string, unknown>) : {}
  return permissions.disableAutoMode === 'disable' || s.disableAutoMode === 'disable'
}
