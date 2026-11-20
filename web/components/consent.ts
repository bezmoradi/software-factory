import { el, unspoofed, visible } from '../lib/dom.ts'
import { confirmDialog } from '../lib/dialog.ts'
import { scrolledText } from './cards.ts'
import { shortSha } from '../lib/format.ts'
import type { HomeApproval, Needs, SignIn, StartupApproval, StartupFile, StartupMark } from '../types.ts'

function size(n: number): string {
  return n < 1024 ? `${n} bytes` : `${(n / 1024).toFixed(1)} KiB`
}

function fileItem(f: StartupFile, watch: ResizeObserver[]): HTMLLIElement {
  const li = el('li', 'consent-file')
  const head = el('div', 'row')
  const path = el('code', '', visible(f.place === 'clone' ? `(clone folder) ${f.path}` : f.place === 'pack' ? `(pack) ${f.path}` : f.path))
  head.append(path, el('span', 'spacer'))
  if (f.hidden) head.append(el('span', 'tag warn', 'hidden characters'))
  if (f.marks?.length) head.append(el('span', 'tag warn', `${f.marks.length} risky ${f.marks.length === 1 ? 'line' : 'lines'}`))
  if (f.uncommitted) head.append(el('span', 'tag warn', f.place === 'worktree' ? 'uncommitted' : 'outside the worktree'))
  li.append(head)
  if (f.marks?.length) li.append(markList(f.marks))
  const notes: string[] = []
  if (f.place === 'clone') notes.push("In the clone's own folder, where Claude Code reads a worktree's local settings, and its skills when the worktree has none. It comes from no commit.")
  else if (f.place === 'pack') notes.push('From the pack this project uses, which the session loads as a plugin: it runs in this session’s container. A new version of the pack asks again.')
  else if (f.place === 'above') notes.push('In a folder above the worktree, inside this session’s own folder, which only this session’s container writes. Claude Code loads it at start. It comes from no commit.')
  else if (f.uncommitted) notes.push('Differs from that commit: it was changed or added in the worktree.')
  if (f.link) {
    li.append(el('p', 'small warn', `A symbolic link to ${visible(f.target ?? '')}. Approving it approves what it points to now; a change there asks again.`))
  }
  if (f.link && f.size === 0) {
    notes.push('It points to a folder, whose files are listed under this path, to an empty file, or to nothing.')
  } else {
    notes.push(size(f.size))
    const [pre, more, observer] = scrolledText(unspoofed(f.content), `Content of ${visible(f.path)}`, 'consent-content small')
    watch.push(observer)
    li.append(pre, more)
    if (f.clipped) notes.push('Only the start of this file is shown.')
  }
  li.append(el('p', 'small muted', notes.join(' ')))
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

function instructionSection(a: StartupApproval, watch: ResizeObserver[]): Node[] {
  const files = a.instructions ?? []
  const unchanged = a.unchanged ?? 0
  if (!files.length && !unchanged && !a.notes?.length) return []
  const d = el('details', 'consent-info')
  const marked = files.filter(f => f.hidden || f.marks?.length).length
  d.append(el('summary', 'small', `Instruction files: ${files.length} new or changed${marked ? `, ${marked} marked` : ''} (information, not approved)`))
  if (files.length) {
    const list = el('ul', 'list consent-files')
    for (const f of files) list.append(fileItem(f, watch))
    d.append(list)
  } else {
    d.append(el('p', 'small', 'None is new or changed since your last approval.'))
  }
  const notes: string[] = []
  if (unchanged) notes.push(`${unchanged} more ${unchanged === 1 ? 'is' : 'are'} unchanged since your last approval and not shown.`)
  if (a.notes?.length) notes.push(`Not all instruction files are shown: ${plain(a.notes)}.`)
  if (a.skipped?.length) notes.push(`The search skips these folders: ${plain(a.skipped)}.`)
  for (const n of notes) d.append(el('p', 'small muted', n))
  const why = el('p', 'small muted consent-why', 'Instruction files steer the agent but grant it no permission, so they are shown to read, not approved: a change to one alone doesn’t ask again.')
  return [why, d]
}

function unwatch(watch: ResizeObserver[]): () => void {
  return () => {
    for (const o of watch) o.disconnect()
  }
}

export function approveStartup(a: StartupApproval, name: string): Promise<boolean> {
  const watch: ResizeObserver[] = []
  const list = el('ul', 'list consent-files')
  for (const f of a.files) list.append(fileItem(f, watch))
  return confirmDialog(`Approve ${visible(name)}'s start-up files`, [
    el('p', 'small', `${visible(name)} loads these files from ${visible(a.repo)} when it starts. They can change what it may do without asking you: permissions, hooks, MCP servers, plugins, agents, commands and endpoints. The worktree is at commit ${shortSha(a.commit)}. Characters that change text direction or hide are shown as ⟨U+…⟩, other invisible and control characters as \\u{…}, and long runs of blank lines or spaces as ⏎ ×n and ␠ ×n. Risky lines are marked for you to read; marking blocks nothing.`),
    list,
    ...(a.walk?.length ? [el('p', 'small warn', `The search of the worktree's subfolders stopped early, so a skill folder or instruction file further in isn't shown, and approving doesn't cover it: ${plain(a.walk)}.${a.skipped?.length ? ` It also skips ${plain(a.skipped)}.` : ''}`)] : []),
    el('p', 'small muted', 'Approving lets this exact set start now and later. Any change to any of them asks again.'),
    ...instructionSection(a, watch),
  ], 'Approve and start', 'primary', unwatch(watch))
}

export function approveTrustFiles(a: StartupApproval, name: string): Promise<boolean> {
  const watch: ResizeObserver[] = []
  const list = el('ul', 'list consent-files')
  for (const f of a.files) list.append(fileItem(f, watch))
  return confirmDialog(`Approve the files ${visible(name)} loads from ${visible(a.repo)}'s clone folder`, [
    el('p', 'small', `Claude Code's trust question is asked in ${visible(a.repo)}'s clone folder, and once the folder is trusted ${visible(name)} loads these files from it. No commit put them there: they were written in the agents' container. They can change what it may do without asking you: permissions, hooks, MCP servers, plugins, agents, commands and endpoints. Characters that change text direction or hide are shown as ⟨U+…⟩, other invisible and control characters as \\u{…}, and long runs of blank lines or spaces as ⏎ ×n and ␠ ×n.`),
    list,
    el('p', 'small muted', 'Approving answers the trust question for this exact set. Cancel leaves the folder untrusted.'),
  ], 'Approve and trust', 'primary', unwatch(watch))
}

export function approveHomeSettings(h: HomeApproval, name: string): Promise<boolean> {
  const list = el('ul', 'list consent-files')
  for (const item of h.changed) {
    const li = el('li', 'consent-file')
    li.append(el('code', '', visible(unspoofed(item))))
    list.append(li)
  }
  const parts: Node[] = [
    el('p', 'small', `${visible(name)}'s user-level settings in this session's container changed since its last start, and waking loads them. They can change what it may do without asking you: permissions, hooks, instructions, agents, skills, commands and endpoints. They are in the agent's own home in this session's container, which the agent itself can write. The engine keeps only a hash of each item, not a copy, so it can't show what an item held before or restore it.`),
  ]
  if (h.problem) parts.push(el('p', 'small warn', visible(h.problem)))
  const total = h.changed.length + Math.max(h.more, 0)
  if (total) parts.push(el('p', 'small', `${total} ${total === 1 ? 'item' : 'items'} ${h.first ? 'with no earlier list to compare' : 'changed or can’t be compared'}, one line for each plugin with changes first, then other items that can run programs, then items that give the agent instructions, then the rest:`))
  if (h.changed.length) parts.push(list)
  if (h.more > 0) parts.push(el('p', 'small warn', `And ${h.more} more ${h.more === 1 ? 'item' : 'items'} not shown, which approving also approves.`))
  parts.push(
    el('p', 'small muted', "Not compared: user MCP servers (kept in ~/.claude.json, which also holds account details), files these settings name outside the listed folders (a hook's script, a CLAUDE.md import), and Claude Code's auto-memory, which the Agents section lists. A “don't ask again” saved in the agent's user settings changes ~/.claude/settings.json too. To read an item first, open it in the session's container with docker exec."),
    el('p', 'small muted', "Approving wakes the session with these settings now, and later wakes compare with them. Any further change asks again. Don't wake stops the container: the session stays stopped and can be deleted, and its next wake asks again."),
  )
  return confirmDialog('The agent’s user settings changed', parts, 'Approve and wake', 'primary', undefined, 'Don’t wake')
}

export function confirmCost(s: SignIn, what: string): Promise<boolean> {
  return confirmDialog('This runs on a sign-in that costs money', [
    el('p', 'small', `It is signed in with ${visible(s.words)}, so ${what} is billed per use. Claude Code and other agents report only an estimate of the cost, which may differ from the bill.`),
  ], 'Start anyway', 'danger')
}

export function askTrust(path: string): Promise<boolean> {
  return confirmDialog('Claude Code asks whether you trust this folder', [
    el('p', 'small', `Accessing workspace: ${visible(path)}`),
    el('p', 'small', 'Quick safety check: is this a project you created or one you trust, like your own code, a well-known open source project, or work from your team? If not, review what’s in this folder first. Claude Code will be able to read, edit and execute files here.'),
    el('p', 'small muted', 'Claude Code’s security notes: Claude can make mistakes, so review its actions, especially when it runs code. Because of prompt injection, use it only with code you trust. Your answer covers every later session of this project.'),
  ], 'Yes, I trust this folder', 'primary')
}

export async function consentFor(needs: Needs | undefined, name: string, what: string): Promise<{ approve?: string; costOk?: boolean } | null> {
  if (!needs) return null
  if (needs.approval) return (await approveStartup(needs.approval, name)) ? { approve: needs.approval.hash } : null
  if (needs.cost && needs.signIn) return (await confirmCost(needs.signIn, what)) ? { costOk: true } : null
  return null
}

export function money(usd: number | undefined): string {
  if (typeof usd !== 'number' || !Number.isFinite(usd) || usd <= 0) return ''
  return usd < 0.01 ? `$${usd.toFixed(4)}` : `$${usd.toFixed(2)}`
}
