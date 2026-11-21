import { button, el, field, labelError, say, statusLine, visible, whileBusy } from '../lib/dom.ts'
import { actionRow, modal } from '../lib/dialog.ts'

type Post = (path: string, body: unknown) => Promise<Response>

export interface CommitIdentity {
  name: string
  email: string
}

const ROUTE = '/api/commit-identity'

async function call(post: Post, body: unknown): Promise<{ ok: boolean; body: Record<string, unknown> }> {
  const res = await post(ROUTE, body).catch(() => null)
  if (!res) return { ok: false, body: { message: 'The engine didn’t answer.' } }
  const parsed: unknown = await res.json().catch(() => ({}))
  return { ok: res.ok, body: parsed !== null && typeof parsed === 'object' ? parsed as Record<string, unknown> : {} }
}

function identityOf(v: unknown): CommitIdentity | null {
  if (v === null || typeof v !== 'object') return null
  const r = v as Record<string, unknown>
  return typeof r.name === 'string' && typeof r.email === 'string' && r.name && r.email ? { name: r.name, email: r.email } : null
}

export async function identityState(post: Post): Promise<CommitIdentity | null | undefined> {
  const a = await call(post, { op: 'read' })
  return a.ok ? identityOf(a.body.identity) : undefined
}

function textField(name: string, words: string, type: string, value: string, auto: AutoFill): [HTMLDivElement, HTMLInputElement] {
  const input = el('input', 'textbox')
  input.type = type
  input.name = name
  input.autocomplete = auto
  input.spellcheck = false
  input.setAttribute('aria-required', 'true')
  input.value = value
  return [field(words, input), input]
}

export function editIdentity(post: Post, current: CommitIdentity | null, saved: (id: CommitIdentity) => void): void {
  const form = el('form', 'identity-form')
  form.noValidate = true
  const [nameField, name] = textField('name', 'Name', 'text', current?.name ?? '', 'name')
  const [emailField, email] = textField('email', 'Email', 'email', current?.email ?? '', 'email')
  const why = statusLine('small warn status-slot')
  const mark = (bad: HTMLInputElement | null, message = '') => {
    for (const input of [name, email]) labelError(input, input === bad ? message : '')
  }
  for (const input of [name, email]) input.addEventListener('input', () => labelError(input, ''))
  const save = button('Save', 'primary small', undefined, 'submit')
  form.append(nameField, emailField, why, actionRow([], [button('Cancel', 'ghost small', () => m.close()), save]))
  form.addEventListener('submit', e => {
    e.preventDefault()
    void whileBusy(save, () => call(post, { op: 'set', name: name.value, email: email.value })).then(a => {
      const id = identityOf(a.body.identity)
      if (!a.ok || !id) {
        const bad = a.body.error === 'name' ? name : a.body.error === 'email' ? email : null
        const message = typeof a.body.message === 'string' ? visible(a.body.message) : 'The name and email weren’t saved. Check that the engine is running.'
        mark(bad, message)
        say(why, bad ? '' : message, 'warn')
        bad?.focus()
        return
      }
      mark(null)
      say(why, '')
      m.close()
      saved(id)
    })
  })
  const m = modal({
    kind: 'identity',
    title: 'The name and email agents commit with',
    body: [
      el('p', 'small', 'Every agent commits with this name and email, in each session’s worktree, so it never falls back to the email of its sign-in. They become public when the engine pushes. For GitHub, use your noreply address, which keeps your own email private: ID+USERNAME@users.noreply.github.com, shown under Settings, Emails, “Keep my email addresses private”.'),
      el('p', 'small muted', 'A session started after you save uses it at once; a stopped session picks it up when you wake it. A running session keeps the name and email it started with.'),
      form,
    ],
    focus: name,
  })
}
