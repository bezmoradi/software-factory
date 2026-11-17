import { button, el, field, say, statusLine, visible, whileBusy } from '../lib/dom.ts'
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

function textField(name: string, words: string, type: string, max: number, value: string, auto: AutoFill): [HTMLDivElement, HTMLInputElement] {
  const input = el('input', 'textbox')
  input.type = type
  input.name = name
  input.autocomplete = auto
  input.spellcheck = false
  input.required = true
  input.maxLength = max
  input.value = value
  return [field(words, input), input]
}

export function editIdentity(post: Post, current: CommitIdentity | null, saved: (id: CommitIdentity) => void): void {
  const form = el('form', 'identity-form')
  const [nameField, name] = textField('name', 'Name', 'text', 100, current?.name ?? '', 'name')
  const [emailField, email] = textField('email', 'Email', 'email', 254, current?.email ?? '', 'email')
  const why = statusLine('small warn status-slot')
  const save = button('Save', 'primary small', undefined, 'submit')
  form.append(nameField, emailField, why, actionRow([], [button('Cancel', 'ghost small', () => m.close()), save]))
  form.addEventListener('submit', e => {
    e.preventDefault()
    if (!form.reportValidity()) return
    void whileBusy(save, () => call(post, { op: 'set', name: name.value, email: email.value })).then(a => {
      const id = identityOf(a.body.identity)
      if (!a.ok || !id) {
        say(why, typeof a.body.message === 'string' ? visible(a.body.message) : 'The name and email weren’t saved. Check that the engine is running.', 'warn')
        return
      }
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
