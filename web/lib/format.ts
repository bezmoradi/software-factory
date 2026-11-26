
export function age(startedAt: number | undefined, now = Date.now()): string {
  if (!startedAt || startedAt > now) return ''
  const m = Math.floor((now - startedAt) / 60000)
  if (m < 60) return `${Math.max(m, 1)}m`
  if (m < 48 * 60) return `${Math.floor(m / 60)}h`
  return `${Math.floor(m / 1440)}d`
}

export function project(cwd: string | undefined): string {
  return String(cwd || '').replace(/\/+$/, '').split('/').pop() || 'session'
}

export function label(info: { name?: string; cwd?: string }): string {
  return info.name || project(info.cwd)
}

export function baseName(path: string): string {
  return path.split('/').filter(Boolean).pop() || path
}

export function shortSha(sha: string): string {
  return sha.slice(0, 7)
}

export function plural(n: number, word: string): string {
  return n === 1 ? word : `${word}s`
}

export function duration(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000))
  const pad = (n: number) => String(n).padStart(2, '0')
  if (s < 60) return `${s}s`
  if (s < 3600) return `${Math.floor(s / 60)}m ${pad(s % 60)}s`
  return `${Math.floor(s / 3600)}h ${pad(Math.floor(s / 60) % 60)}m`
}

const AGENT_WORDS: Record<string, string> = { claude: 'Claude' }
const agentNames = new Map<string, string>()

export function setAgentNames(list: { id: string; name: string }[]): void {
  agentNames.clear()
  for (const a of list) agentNames.set(a.id, a.name)
}

export function isClaude(info: { agent?: string }): boolean {
  return !info.agent || info.agent === 'claude'
}

export function agentWord(agent?: string): string {
  if (!agent) return 'Claude'
  return AGENT_WORDS[agent] ?? agentNames.get(agent) ?? 'The agent'
}

