import type { EngineAnswer } from '../types.ts'
import { el, ensureId, labelError, say, type Tone } from './dom.ts'

export interface FormOptions {
  cls?: string
  inline?: boolean
  autocomplete?: 'off'
  onSubmit?: (e: SubmitEvent) => void
  children?: Node[]
}

export function form(o: FormOptions = {}): HTMLFormElement {
  const f = el('form', ['form', o.inline ? 'form-inline' : '', o.cls ?? ''].filter(Boolean).join(' '))
  f.noValidate = true
  if (o.autocomplete) f.autocomplete = o.autocomplete
  const submitted = o.onSubmit
  f.addEventListener('submit', e => {
    e.preventDefault()
    submitted?.(e)
  })
  if (o.children) f.append(...o.children)
  return f
}

export type FieldKind = 'stack' | 'inline' | 'check' | 'option' | 'hidden'

export interface FieldOptions {
  kind?: FieldKind
  help?: string
  cls?: string
  labelCls?: string
  optional?: boolean
}

const LABEL_CLASS: Record<FieldKind, string> = { stack: 'small', inline: 'small', check: 'small', option: '', hidden: 'sr-only' }

function labelTarget(control: HTMLElement): HTMLElement {
  if (control instanceof HTMLInputElement || control instanceof HTMLSelectElement || control instanceof HTMLTextAreaElement || control instanceof HTMLButtonElement) return control
  return control.querySelector<HTMLElement>('input, select, textarea') ?? control
}

export function field(label: string | Node, control: HTMLElement, o: FieldOptions = {}): HTMLDivElement {
  const kind = o.kind ?? 'stack'
  const target = labelTarget(control)
  const id = ensureId(target)
  const box = el('div', ['field', kind === 'stack' ? '' : `field-${kind}`, o.cls ?? ''].filter(Boolean).join(' '))
  const name = el('label', o.labelCls ?? LABEL_CLASS[kind])
  name.htmlFor = id
  name.append(label)
  if (o.optional) name.append(' ', el('span', 'muted', '(optional)'))
  if (kind === 'check' || kind === 'option') {
    name.prepend(control)
    box.append(name)
  } else box.append(name, control)
  if (o.help) {
    const note = el('p', 'small muted', o.help)
    note.id = `${id}-help`
    const described = (target.getAttribute('aria-describedby') ?? '').split(' ').filter(Boolean)
    if (!described.includes(note.id)) target.setAttribute('aria-describedby', [...described, note.id].join(' '))
    box.append(note)
  }
  return box
}

export function fieldGroup(children: Node[], cls = ''): HTMLDivElement {
  const group = el('div', ['form', cls].filter(Boolean).join(' '))
  group.append(...children)
  return group
}

export function fieldRow(children: Node[], cls = ''): HTMLDivElement {
  const row = el('div', ['form-row', cls].filter(Boolean).join(' '))
  row.append(...children)
  return row
}

export function actions(buttons: Node[], lead: Node[] = []): HTMLDivElement {
  const row = el('div', 'form-actions')
  const submits = buttons.filter(b => b instanceof HTMLButtonElement && b.type === 'submit')
  row.append(...lead, ...(lead.length ? [el('span', 'spacer')] : []), ...buttons.filter(b => !submits.includes(b)), ...submits)
  return row
}

function labelled(control: HTMLElement): boolean {
  const root = control.getRootNode()
  return !!control.id && (root instanceof Document || root instanceof DocumentFragment || root instanceof Element) && !!root.querySelector(`label[for="${CSS.escape(control.id)}"]`)
}

export function answerControl(container: HTMLElement, answer: EngineAnswer): HTMLElement | null {
  if (answer.ok || !answer.field) return null
  const control = container.querySelector<HTMLElement>(`[name="${CSS.escape(answer.field)}"]`)
  return control && labelled(control) ? control : null
}

export interface AnswerPlace {
  tone?: Tone
}

export function showAnswer(container: HTMLElement, answer: EngineAnswer, line?: HTMLElement | null, o: AnswerPlace = {}): boolean {
  for (const marked of container.querySelectorAll<HTMLElement>('[aria-invalid="true"]')) labelError(marked, '')
  const control = answerControl(container, answer)
  if (answer.ok || control) {
    if (line) line.textContent = ''
    if (control) {
      labelError(control, answer.message)
      control.focus()
    }
    return answer.ok
  }
  if (line) say(line, answer.message, o.tone ?? 'bad')
  return false
}
