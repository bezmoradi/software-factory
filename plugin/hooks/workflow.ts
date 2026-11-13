import type { EngineInterface } from 'claude-code'

import { clip } from './show.ts'

export type Ran = { name: string; via: 'command' | 'skill'; at: number }
export type Kept = { written: string[]; ran: Ran[] }

const TEXT_MAX = 100000
const NAME = /^[A-Za-z0-9][\w.:@-]{0,119}$/
const fileOf = (base: string) => new RegExp(`(?:^|/)${base}(-[\\w.-]+)?\\.md$`)
export const SESSION_FILE = fileOf('(?:PLAN|RESEARCH|REQUIREMENTS)')

export function keptOf(raw: unknown): Kept {
  const v = raw && typeof raw === 'object' ? (raw as { written?: unknown; ran?: unknown }) : {}
  const written = Array.isArray(v.written) ? v.written.filter((f): f is string => typeof f === 'string' && f.length <= 500 && f.startsWith('/') && !f.includes('/../') && SESSION_FILE.test(f)).slice(-20) : []
  const ran = Array.isArray(v.ran)
    ? v.ran.flatMap((r): Ran[] => {
        const x = r as { name?: unknown; via?: unknown; at?: unknown } | null
        return x && typeof x.name === 'string' && NAME.test(x.name) && (x.via === 'command' || x.via === 'skill') && typeof x.at === 'number' && Number.isFinite(x.at) ? [{ name: x.name, via: x.via, at: x.at }] : []
      }).slice(-30)
    : []
  return { written, ran }
}

export function ranName(raw: unknown): string {
  const name = typeof raw === 'string' ? raw.trim().replace(/^\//, '').split(/\s/)[0] ?? '' : ''
  return NAME.test(name) ? name : ''
}

async function stateHome($: EngineInterface): Promise<string> {
  const xdg = await $.env.get('XDG_STATE_HOME')
  return xdg?.startsWith('/') ? xdg : `${await $.env.get('HOME')}/.local/state`
}

function plainFile($: EngineInterface, p: string): Promise<boolean> {
  return $.fs.stat(p).then(s => s.kind === 'file' && !s.isLink).catch(() => false)
}

function sectionText(body: string, name: string): string {
  return new RegExp(`^## ${name}\\n([\\s\\S]*?)(?=^## |$(?![\\s\\S]))`, 'm').exec(body)?.[1] ?? ''
}

function sectionOf(body: string, name: string): string[] {
  return sectionText(body, name).split('\n').filter(l => l.startsWith('- ')).map(l => l.slice(2, 600)).slice(0, 200)
}

async function currentIntent($: EngineInterface, sid: string): Promise<{ body: string; path: string } | null> {
  const root = `${await stateHome($)}/knackpack/intent`
  const found: { path: string; mtime: number }[] = []
  for (const repo of await $.fs.list(root).catch(() => [])) {
    if (repo.kind !== 'dir' || repo.isLink) continue
    for (const f of await $.fs.list(`${root}/${repo.name}`).catch(() => [])) {
      const rest = f.name.startsWith(sid) ? f.name.slice(sid.length) : null
      if (f.kind === 'file' && !f.isLink && (rest === '.md' || (rest && /^\.\d+\.md$/.test(rest)))) found.push({ path: `${root}/${repo.name}/${f.name}`, mtime: f.mtimeMs })
    }
  }
  found.sort((a, b) => b.mtime - a.mtime)
  for (const { path } of found) {
    const body = (await $.fs.read(path).catch(() => '')).slice(0, 200000)
    const status = /^- Status: ([a-z]+)/m.exec(body)?.[1] ?? ''
    if (status === 'done' && found.length > 1) continue
    return { body, path }
  }
  return null
}

async function textOf($: EngineInterface, path: string): Promise<{ path: string; text: string } | null> {
  if (!path) return null
  const text = await $.fs.read(path).catch(() => null)
  return text === null ? null : { path, text: clip(text, TEXT_MAX) }
}

async function jobScratch($: EngineInterface, sid: string): Promise<string> {
  if (!/^[0-9a-f]{8}-[0-9a-f-]{27}$/.test(sid)) return ''
  const config = (await $.env.get('CLAUDE_CONFIG_DIR')) || `${await $.env.get('HOME')}/.claude`
  if (!config.startsWith('/')) return ''
  const dir = `${config.replace(/\/+$/, '')}/jobs/${sid.slice(0, 8)}`
  const state = await $.fs.read(`${dir}/state.json`).catch(() => '')
  try {
    return (JSON.parse(state.slice(0, 200000)) as { sessionId?: unknown }).sessionId === sid ? `${dir}/tmp` : ''
  } catch {
    return ''
  }
}

async function filesIn($: EngineInterface, dir: string): Promise<string[]> {
  if (!dir) return []
  return $.fs.list(dir).then(l => l.filter(f => f.kind === 'file' && !f.isLink).map(f => `${dir}/${f.name}`)).catch(() => [] as string[])
}

export async function workflowReport($: EngineInterface, sid: string, kept: Kept, knackpack: boolean): Promise<Record<string, unknown>> {
  const tmp = ((await $.env.get('TMPDIR')) || '/tmp').replace(/\/+$/, '')
  const scratch = `${tmp}/claude-${sid}`
  const here = [...await filesIn($, await jobScratch($, sid).catch(() => '')), ...await filesIn($, scratch)]
  const intent = await currentIntent($, sid).catch(() => null)
  const body = intent?.body ?? ''
  const listed = (/^- Files: (.*)$/m.exec(body)?.[1] ?? '').split(/,\s+/).filter(p => p.startsWith('/') && !p.includes('/../'))
  const pick = async (base: string): Promise<string> => {
    const re = fileOf(base)
    for (const p of [...here, ...[...kept.written].reverse(), ...listed]) if (re.test(p) && (await plainFile($, p))) return p
    return ''
  }
  const [plan, research, requirements] = await Promise.all([pick('PLAN'), pick('RESEARCH'), pick('REQUIREMENTS')])
  return {
    type: 'workflow',
    knackpack,
    plan: await textOf($, plan),
    research: research ? { path: research } : null,
    requirements: await textOf($, requirements),
    intent: intent
      ? {
          path: intent.path,
          title: clip(/^# Intent record: (.*)$/m.exec(body)?.[1] ?? '', 300),
          status: /^- Status: ([a-z]+)/m.exec(body)?.[1] ?? '',
          agreed: sectionOf(body, 'Agreed'),
          assumed: sectionOf(body, 'Assumed \\(not confirmed\\)'),
          open: sectionOf(body, 'Open questions'),
          built: sectionOf(body, 'Built'),
          files: listed.slice(0, 50).map(p => clip(p, 500)),
        }
      : null,
    ran: kept.ran,
  }
}
