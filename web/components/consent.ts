import { button, el, unspoofed, visible } from '../lib/dom.ts'
import { scrolledText } from './cards.ts'
import { shortSha } from '../lib/format.ts'
import type { Needs, SignIn, StartupApproval, StartupFile } from '../types.ts'

function size(n: number): string {
  return n < 1024 ? `${n} bytes` : `${(n / 1024).toFixed(1)} KiB`
}

function fileItem(f: StartupFile): HTMLLIElement {
  const li = el('li', 'consent-file')
  const head = el('div', 'row')
  const path = el('code', '', visible(f.place === 'clone' ? `(clone folder) ${f.path}` : f.path))
  head.append(path, el('span', 'spacer'))
  if (f.uncommitted) head.append(el('span', 'tag warn', f.place === 'clone' ? 'outside the worktree' : 'uncommitted'))
  li.append(head)
  const notes: string[] = []
  if (f.place === 'clone') notes.push("In the clone's own folder, where Claude Code reads a worktree's local settings. It comes from no commit.")
  else if (f.uncommitted) notes.push('Differs from that commit: it was changed or added in the worktree.')
  if (f.link) {
    li.append(el('p', 'small warn', `A symbolic link to ${visible(f.target ?? '')}. Approving it approves what it points to now; a change there asks again.`))
  }
  if (f.link && f.size === 0) {
    notes.push('It points to a folder, whose files are listed under this path, to an empty file, or to nothing.')
  } else {
    notes.push(size(f.size))
    const [pre, more] = scrolledText(unspoofed(f.content), `Content of ${visible(f.path)}`, 'consent-content small')
    li.append(pre, more)
    if (f.clipped) notes.push('Only the start of this file is shown.')
  }
  li.append(el('p', 'small muted', notes.join(' ')))
  return li
}

function dialog(title: string, parts: Node[], yes: string, tone: 'primary' | 'danger'): Promise<boolean> {
  return new Promise(resolve => {
    const d = el('dialog', 'consent surface')
    d.setAttribute('aria-label', title)
    const done = (answer: boolean) => {
      d.close()
      d.remove()
      resolve(answer)
    }
    const actions = el('div', 'row consent-actions')
    const ok = button(yes, `${tone} small`, () => done(true))
    actions.append(el('span', 'spacer'), button('Cancel', 'ghost small', () => done(false)), ok)
    d.append(el('h2', 'label', title), ...parts, actions)
    d.addEventListener('cancel', e => {
      e.preventDefault()
      done(false)
    })
    document.body.append(d)
    d.showModal()
  })
}

export function approveStartup(a: StartupApproval, name: string): Promise<boolean> {
  const list = el('ul', 'list consent-files')
  for (const f of a.files) list.append(fileItem(f))
  return dialog(`Approve ${visible(name)}'s start-up files`, [
    el('p', 'small', `${visible(name)} loads these files from ${visible(a.repo)} when it starts. They can change what it may do without asking you: permissions, hooks, MCP servers, plugins, agents, commands and endpoints. The worktree is at commit ${shortSha(a.commit)}. Characters that change text direction or hide are shown as ⟨U+…⟩, other invisible and control characters as \\u{…}, and long runs of blank lines or spaces as ⏎ ×n and ␠ ×n.`),
    list,
    el('p', 'small muted', 'Approving lets this exact set start now and later. Any change to any of them asks again.'),
  ], 'Approve and start', 'primary')
}

export function confirmCost(s: SignIn, what: string): Promise<boolean> {
  return dialog('This runs on a sign-in that costs money', [
    el('p', 'small', `It is signed in with ${visible(s.words)}, so ${what} is billed per use. Claude Code and other agents report only an estimate of the cost, which may differ from the bill.`),
  ], 'Start anyway', 'danger')
}

export function askTrust(path: string): Promise<boolean> {
  return dialog('Claude Code asks whether you trust this folder', [
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
