import { el, unspoofed, visible, visibleCommandLines } from '../lib/dom.ts'
import { confirmDialog } from '../lib/dialog.ts'
import { codeBlock } from './code-block.ts'
import { plural, shortSha } from '../lib/format.ts'
import { langFor } from '../lib/highlight.ts'
import type { HomeApproval, Needs, SignIn, StartupApproval, StartupFile, StartupMark } from '../types.ts'

function size(n: number): string {
  return n < 1024 ? `${n} bytes` : `${(n / 1024).toFixed(1)} KiB`
}

function fileItem(f: StartupFile): HTMLLIElement {
  const li = el('li', 'consent-file')
  const tags: string[] = []
  if (f.hidden) tags.push('hidden characters')
  if (f.marks?.length) tags.push(`${f.marks.length} risky ${f.marks.length === 1 ? 'line' : 'lines'}`)
  if (f.uncommitted) tags.push(f.place === 'worktree' ? 'uncommitted' : 'outside the worktree')
  const title = visible(f.place === 'clone' ? `(clone folder) ${f.path}` : f.place === 'pack' ? `(pack) ${f.path}` : f.path)
  const label = `Content of ${visible(f.path)}`
  const notes: string[] = []
  if (f.place === 'clone') notes.push("In the clone's own folder, where Claude Code reads a worktree's local settings, and its skills when the worktree has none. It comes from no commit.")
  else if (f.place === 'pack') notes.push('From the pack this project uses, which the session loads as a plugin: it runs in this session’s container. A new version of the pack asks again.')
  else if (f.place === 'above') notes.push('In a folder above the worktree, inside this session’s own folder, which only this session’s container writes. Claude Code loads it at start. It comes from no commit.')
  else if (f.uncommitted) notes.push('Differs from that commit: it was changed or added in the worktree.')
  if (f.link && f.size === 0) {
    li.append(codeBlock({ title, tags, text: '', label }))
    notes.push('It points to a folder, whose files are listed under this path, to an empty file, or to nothing.')
  } else {
    const rows = visibleCommandLines(unspoofed(f.content))
    if (rows.length > 1 && rows[rows.length - 1]!.text === '') rows.pop()
    const count = rows[rows.length - 1]?.line ?? 0
    const marked = new Set((f.marks ?? []).filter(m => m.line > 0).map(m => m.line))
    li.append(codeBlock({ title, tags, details: `${count} ${plural(count, 'line')} · ${size(f.size)}`, text: rows, label, lang: langFor(f.path), numbers: true, marked, wrap: true, wrapToggle: true }))
    if (f.clipped) notes.push('Only the start of this file is shown.')
  }
  if (f.link) li.append(el('p', 'small warn', `A symbolic link to ${visible(f.target ?? '')}. Approving it approves what it points to now; a change there asks again.`))
  if (f.marks?.length) li.append(markList(f.marks))
  if (notes.length) li.append(el('p', 'small muted', notes.join(' ')))
  return li
}

function markList(marks: StartupMark[]): HTMLUListElement {
  const list = el('ul', 'consent-marks small')
  for (const m of marks) {
    const li = el('li', 'warn', m.line > 0 ? `Line ${m.line}: ${visible(m.what)}` : visible(m.what))
    if (m.text) li.append(' ', el('code', '', visible(unspoofed(m.text))))
    list.append(li)
  }
  return list
}

function plain(items: string[] | undefined): string {
  return (items ?? []).map(visible).join('; ')
}

function instructionSection(a: StartupApproval): Node[] {
  const files = a.instructions ?? []
  const unchanged = a.unchanged ?? 0
  if (!files.length && !unchanged && !a.notes?.length) return []
  const d = el('details', 'consent-info')
  const marked = files.filter(f => f.hidden || f.marks?.length).length
  d.append(el('summary', 'small', `Instruction files: ${files.length} new or changed${marked ? `, ${marked} marked` : ''}, to read`))
  d.append(el('p', 'small muted', 'Instruction files steer the agent but grant it no permission, so they are shown to read, not approved: a change to one alone doesn’t ask again.'))
  if (files.length) {
    const list = el('ul', 'list consent-files')
    for (const f of files) list.append(fileItem(f))
    d.append(list)
  } else {
    d.append(el('p', 'small', 'None is new or changed since your last approval.'))
  }
  const notes: string[] = []
  if (unchanged) notes.push(`${unchanged} more ${unchanged === 1 ? 'is' : 'are'} unchanged since your last approval and not shown.`)
  if (a.notes?.length) notes.push(`Not all instruction files are shown: ${plain(a.notes)}.`)
  if (a.skipped?.length) notes.push(`The search skips these folders: ${plain(a.skipped)}.`)
  for (const n of notes) d.append(el('p', 'small muted', n))
  return [d]
}

function details(...lines: string[]): HTMLDetailsElement {
  const d = el('details', 'consent-info')
  d.append(el('summary', 'small', 'Details'), ...lines.map(t => el('p', 'small muted', t)))
  return d
}

const LEGEND = 'Characters that change text direction or hide are shown as ⟨U+…⟩, other invisible and control characters as \\u{…}, and long runs of blank lines or spaces as ⏎ ×n and ␠ ×n. Risky lines are marked for you to read; marking blocks nothing.'

export function approveStartup(a: StartupApproval, name: string): Promise<boolean> {
  const list = el('ul', 'list consent-files')
  for (const f of a.files) list.append(fileItem(f))
  return confirmDialog(`Approve ${visible(name)}'s start-up files`, 'projects', [
    el('p', 'small', `${visible(name)} runs these files from ${visible(a.repo)} when it starts. They can let it act without asking you, so read them before you approve.`),
    list,
    ...(a.walk?.length ? [el('p', 'small warn', `The search of the worktree's subfolders stopped early, so a skill folder or instruction file further in isn't shown, and approving doesn't cover it: ${plain(a.walk)}.${a.skipped?.length ? ` It also skips ${plain(a.skipped)}.` : ''}`)] : []),
    ...instructionSection(a),
    details(`They can change its permissions, hooks, MCP servers, plugins, agents, commands and endpoints. The worktree is at commit ${shortSha(a.commit)}.`, LEGEND, 'Approving lets this exact set start now and later. Any change to any of them asks again.'),
  ], 'Approve and start', 'primary', 'Cancel', true)
}

export function approveTrustFiles(a: StartupApproval, name: string): Promise<boolean> {
  const list = el('ul', 'list consent-files')
  for (const f of a.files) list.append(fileItem(f))
  return confirmDialog(`Approve the files ${visible(name)} loads from ${visible(a.repo)}'s clone folder`, 'projects', [
    el('p', 'small', `Once ${visible(a.repo)}'s clone folder is trusted, ${visible(name)} loads these files from it. No commit put them there: they were written in the agents' container, so read them before you approve.`),
    list,
    details("Claude Code's trust question is asked in the clone folder. These files can change what it may do without asking you: permissions, hooks, MCP servers, plugins, agents, commands and endpoints.", LEGEND, 'Approving answers the trust question for this exact set. Cancel leaves the folder untrusted.'),
  ], 'Approve and trust', 'primary')
}

export function approveHomeSettings(h: HomeApproval, name: string): Promise<boolean> {
  const list = el('ul', 'list consent-files')
  for (const item of h.changed) {
    const li = el('li', 'consent-file')
    li.append(el('code', '', visible(unspoofed(item))))
    list.append(li)
  }
  const parts: Node[] = [
    el('p', 'small', `${visible(name)}'s user settings in this session's container changed since it last ran. The agent can write them, and waking loads them, so they can change what it may do without asking you, such as its permissions, hooks and instructions.`),
  ]
  if (h.problem) parts.push(el('p', 'small warn', visible(h.problem)))
  const total = h.changed.length + Math.max(h.more, 0)
  if (total) parts.push(el('p', 'small', `${total} ${total === 1 ? 'item' : 'items'} ${h.first ? 'with no earlier list to compare' : 'changed or can’t be compared'}:`))
  if (h.changed.length) parts.push(list)
  if (h.more > 0) parts.push(el('p', 'small warn', `And ${h.more} more ${h.more === 1 ? 'item' : 'items'} not shown, which approving also approves.`))
  const details = el('details', 'consent-info')
  details.append(
    el('summary', 'small', 'Details'),
    el('p', 'small muted', 'The list shows one line for each plugin with changes first, then other items that can run programs, then items that give the agent instructions, then the rest. The engine keeps only a hash of each item, not a copy, so it can’t show what an item held before or restore it.'),
    el('p', 'small muted', "Not compared: user MCP servers (kept in ~/.claude.json, which also holds account details), files these settings name outside the listed folders (a hook's script, a CLAUDE.md import), and Claude Code's auto-memory, which the Agents section lists. A “don't ask again” saved in the agent's user settings changes ~/.claude/settings.json too. To read an item first, open it in the session's container with docker exec."),
    el('p', 'small muted', "Approving wakes the session with these settings now, and later wakes compare with them. Any further change asks again. Don't wake stops the container: the session stays stopped and can be deleted, and its next wake asks again."),
  )
  parts.push(details)
  return confirmDialog('The agent’s user settings changed', 'projects', parts, 'Approve and wake', 'primary', 'Don’t wake')
}

export function confirmCost(s: SignIn, what: string): Promise<boolean> {
  return confirmDialog('This runs on a sign-in that costs money', 'cost-and-models', [
    el('p', 'small', `It is signed in with ${visible(s.words)}, so ${what} is billed per use. Claude Code and other agents report only an estimate of the cost, which may differ from the bill.`),
  ], 'Start anyway', 'danger')
}

export function askTrust(): Promise<boolean> {
  return confirmDialog('Claude Code asks whether you trust this folder', 'trusting-a-repository', [
    el('p', 'small', 'Quick safety check: is this a project you created or one you trust, like your own code, a well-known open source project, or work from your team? If not, review what’s in this folder first. Claude Code will be able to read, edit and execute files here.'),
  ], 'Yes, I trust this folder', 'primary')
}

export async function consentFor(needs: Needs | undefined, name: string, what: string): Promise<{ approve?: string; costOk?: boolean } | null> {
  if (needs?.next === 'approve' && needs.approval) return (await approveStartup(needs.approval, name)) ? { approve: needs.approval.hash } : null
  if (needs?.next === 'cost' && needs.signIn) return (await confirmCost(needs.signIn, what)) ? { costOk: true } : null
  return null
}

export function money(usd: number | undefined): string {
  if (typeof usd !== 'number' || !Number.isFinite(usd) || usd <= 0) return ''
  return usd < 0.01 ? `$${usd.toFixed(4)}` : `$${usd.toFixed(2)}`
}
