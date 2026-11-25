import { UNREAD, savedTone, type EngineAnswer } from '../api.ts'
import { button, el, labelError, say, statusLine, whileBusy } from '../lib/dom.ts'
import { modal } from '../lib/dialog.ts'
import { dialogDocs } from '../lib/dialog-docs.ts'
import { actions, field, form, showAnswer } from '../lib/form.ts'
import { textbox, type TextType } from '../lib/controls.ts'

type Post = (path: string, body: unknown) => Promise<EngineAnswer>

export interface CommitIdentity {
  name: string
  email: string
}

const ROUTE = '/api/commit-identity'

function identityOf(v: unknown): CommitIdentity | null {
  if (v === null || typeof v !== 'object') return null
  const r = v as Record<string, unknown>
  return typeof r.name === 'string' && typeof r.email === 'string' && r.name && r.email ? { name: r.name, email: r.email } : null
}

export async function identityState(post: Post): Promise<CommitIdentity | null | undefined> {
  const a = await post(ROUTE, { op: 'read' })
  return a.ok ? identityOf(a.body.identity) : undefined
}

function textField(name: string, words: string, type: TextType, value: string, auto: AutoFill): [HTMLDivElement, HTMLInputElement] {
  const input = textbox({ type, name, autocomplete: auto, spellcheck: false })
  input.setAttribute('aria-required', 'true')
  input.value = value
  return [field(words, input), input]
}

export function editIdentity(post: Post, current: CommitIdentity | null, saved: (id: CommitIdentity, said: string, tone: 'ok' | 'warn') => void): void {
  const [nameField, name] = textField('name', 'Name', 'text', current?.name ?? '', 'name')
  const [emailField, email] = textField('email', 'Email', 'email', current?.email ?? '', 'email')
  const why = statusLine('small warn status-slot')
  for (const input of [name, email]) input.addEventListener('input', () => labelError(input, ''))
  const save = button('Save', 'primary small', undefined, 'submit')
  const box = form({
    cls: 'identity-form',
    children: [nameField, emailField, why, actions([button('Cancel', 'ghost small', () => m.close()), save])],
    onSubmit: () => {
      void whileBusy(save, () => post(ROUTE, { op: 'set', name: name.value, email: email.value })).then(a => {
        if (!showAnswer(box, a, why, { tone: 'warn' })) return
        const id = identityOf(a.body.identity)
        if (!id || !a.message) return say(why, UNREAD, 'warn')
        m.close()
        saved(id, a.message, savedTone(a))
      })
    },
  })
  const m = modal({
    kind: 'identity',
    ...dialogDocs('identity'),
    body: [
      el('p', 'small', 'Every agent commits with these, and pushes make them public. For GitHub, use your noreply address: ID+USERNAME@users.noreply.github.com. New sessions use them at once; a stopped session when you wake it.'),
      box,
    ],
    focus: name,
  })
}
