import { el } from './dom.ts'

export interface BoxOptions {
  name?: string
  placeholder?: string
  autocomplete?: AutoFill
  spellcheck?: boolean
  cls?: string
}

export type TextType = 'text' | 'url' | 'email' | 'search' | 'password' | 'number'

export interface TextboxOptions extends BoxOptions {
  type?: TextType
  value?: string
  inputMode?: string
}

export interface TextareaOptions extends BoxOptions {
  rows?: number
  value?: string
}

export interface DropdownOptions {
  name?: string
  cls?: string
  options?: Node[]
}

export interface OptionState {
  selected?: boolean
  disabled?: boolean
}

export interface CheckOptions {
  name?: string
  value?: string
  checked?: boolean
  cls?: string
}

export interface FileOptions {
  name?: string
  accept?: string
  multiple?: boolean
  hidden?: boolean
}

function boxClass(cls: string | undefined): string {
  return ['textbox', cls ?? ''].filter(Boolean).join(' ')
}

function boxText(node: HTMLInputElement | HTMLTextAreaElement, o: BoxOptions): void {
  if (o.name !== undefined) node.name = o.name
  if (o.placeholder !== undefined) node.placeholder = o.placeholder
  if (o.autocomplete !== undefined) node.autocomplete = o.autocomplete
  if (o.spellcheck !== undefined) node.spellcheck = o.spellcheck
}

export function textbox(o: TextboxOptions = {}): HTMLInputElement {
  const input = el('input', boxClass(o.cls))
  if (o.type) input.type = o.type
  if (o.inputMode !== undefined) input.inputMode = o.inputMode
  boxText(input, o)
  if (o.value !== undefined) input.value = o.value
  return input
}

export function textarea(o: TextareaOptions = {}): HTMLTextAreaElement {
  const area = el('textarea', boxClass(o.cls))
  if (o.rows !== undefined) area.rows = o.rows
  boxText(area, o)
  if (o.value !== undefined) area.value = o.value
  return area
}

const customSelect = typeof CSS !== 'undefined' && CSS.supports('appearance', 'base-select')

function shownValue(pick: HTMLSelectElement): void {
  const face = el('button')
  face.type = 'button'
  face.append(el('selectedcontent'))
  pick.prepend(face)
  new MutationObserver(() => {
    if (face.parentNode !== pick) pick.prepend(face)
  }).observe(pick, { childList: true })
}

export function dropdown(o: DropdownOptions = {}): HTMLSelectElement {
  const pick = el('select', boxClass(o.cls))
  if (o.name !== undefined) pick.name = o.name
  if (customSelect) shownValue(pick)
  if (o.options) pick.append(...o.options)
  return pick
}

export function option(text: string, value?: string, o: OptionState = {}): HTMLOptionElement {
  const item = el('option', '', text)
  if (value !== undefined) item.value = value
  if (o.disabled !== undefined) item.disabled = o.disabled
  if (o.selected !== undefined) item.selected = o.selected
  return item
}

function choice(type: 'checkbox' | 'radio', o: CheckOptions): HTMLInputElement {
  const input = el('input', ['choice', o.cls].filter(Boolean).join(' '))
  input.type = type
  if (o.name !== undefined) input.name = o.name
  if (o.value !== undefined) input.value = o.value
  if (o.checked !== undefined) input.checked = o.checked
  return input
}

export function checkbox(o: CheckOptions = {}): HTMLInputElement {
  return choice('checkbox', o)
}

export function radio(name: string, value: string, o: Omit<CheckOptions, 'name' | 'value'> = {}): HTMLInputElement {
  return choice('radio', { ...o, name, value })
}

export function filePicker(o: FileOptions = {}): HTMLInputElement {
  const input = el('input')
  input.type = 'file'
  if (o.name !== undefined) input.name = o.name
  if (o.accept !== undefined) input.accept = o.accept
  if (o.multiple !== undefined) input.multiple = o.multiple
  if (o.hidden !== undefined) input.hidden = o.hidden
  return input
}
