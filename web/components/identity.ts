import { button, el, opener, visible, whileBusy } from '../lib/dom.ts'

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

function field(name: string, words: string, type: string, max: number, value: string, auto: AutoFill): [HTMLLabelElement, HTMLInputElement] {
  const label = el('label', 'small', words)
  const input = el('input', 'textbox')
  input.type = type
  input.name = name
  input.autocomplete = auto
  input.spellcheck = false
  input.required = true
  input.maxLength = max
  input.value = value
  label.append(input)
  return [label, input]
}

export function editIdentity(post: Post, current: CommitIdentity | null, saved: (id: CommitIdentity) => void): void {
  const back = opener()
  const d = el('dialog', 'consent identity surface')
  d.setAttribute('aria-label', 'The name and email agents commit with')
  const close = () => {
    d.close()
    back()
    d.remove()
  }
  const form = el('form', 'identity-form')
  const [nameLabel, name] = field('name', 'Name', 'text', 100, current?.name ?? '', 'name')
  const [emailLabel, email] = field('email', 'Email', 'email', 254, current?.email ?? '', 'email')
  const why = el('p', 'small warn')
  why.setAttribute('role', 'status')
  const save = button('Save', 'primary small', undefined, 'submit')
  const actions = el('div', 'row consent-actions')
  actions.append(el('span', 'spacer'), button('Cancel', 'ghost small', close), save)
  form.append(nameLabel, emailLabel, why, actions)
  form.addEventListener('submit', e => {
    e.preventDefault()
    if (!form.reportValidity()) return
    void whileBusy(save, () => call(post, { op: 'set', name: name.value, email: email.value })).then(a => {
      const id = identityOf(a.body.identity)
      if (!a.ok || !id) {
        why.textContent = typeof a.body.message === 'string' ? visible(a.body.message) : 'The name and email weren’t saved. Check that the engine is running.'
        return
      }
      close()
      saved(id)
    })
  })
  d.append(
    el('h2', 'label', 'The name and email agents commit with'),
    el('p', 'small', 'Every agent commits with this name and email, in each session’s worktree, so it never falls back to the email of its sign-in. They become public when the engine pushes. For GitHub, use your noreply address, which keeps your own email private: ID+USERNAME@users.noreply.github.com, shown under Settings, Emails, “Keep my email addresses private”.'),
    el('p', 'small muted', 'A session started after you save uses it at once; a stopped session picks it up when you wake it. A running session keeps the name and email it started with.'),
    form,
  )
  d.addEventListener('cancel', e => {
    e.preventDefault()
    close()
  })
  document.body.append(d)
  d.showModal()
  name.focus()
}
