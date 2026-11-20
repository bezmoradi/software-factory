export const DOUBLED_NOTE = "Read-only: another session on this engine holds this session's branch. Its changes, gate, recap and audit still open here; delete it to offer its unpushed commits, or keep working in the other session."

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

const MODE_LABELS: Record<string, string> = { default: 'Manual', manual: 'Manual', acceptEdits: 'Accept edits', plan: 'Plan', auto: 'Auto', dontAsk: "Don't ask", bypassPermissions: 'Bypass permissions' }

export function modeLabel(mode: string): string {
  return MODE_LABELS[mode] ?? mode
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

const COMPOSE = /^docker compose( -p ([a-z0-9][a-z0-9_-]*))?$/
const BOX = /^[0-9a-f]{16}$/
let compose = 'docker compose'
let stack = 'software-factory'

export function setCompose(value: unknown): void {
  const m = typeof value === 'string' ? COMPOSE.exec(value) : null
  compose = m ? m[0] : 'docker compose'
  stack = m?.[2] ?? 'software-factory'
}

export function composeCommand(rest: string): string {
  return `${compose} ${rest}`
}

export function boxContainer(box: string): string {
  return BOX.test(box) ? `${stack}-box-${box}` : ''
}

export function attachCommand(id: string, box: string): string {
  const name = boxContainer(box)
  return name ? `docker exec -it ${name} claude attach ${id}` : ''
}

export function logsCommand(id: string, box: string): string {
  const name = boxContainer(box)
  return name ? `docker exec ${name} claude logs ${id}` : ''
}

export function signInStep(box: string | undefined): string {
  const name = boxContainer(box ?? '')
  if (!name) return "the session's sign-in panel, which Wake opens"
  return `docker exec -it ${name} claude, then /login`
}
