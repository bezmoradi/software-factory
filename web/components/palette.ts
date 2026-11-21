import { button, el, settleFocus, statusLine, visible } from '../lib/dom.ts'
import { modal, reopen } from '../lib/dialog.ts'
import { field, form } from '../lib/form.ts'

export interface PaletteItem {
  id: string
  group: string
  label: string
  detail?: string
  off?: string
  keys?: string[]
  run: () => void
}

const MAX_SHOWN = 200

export const isMac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)

export function paletteKey(e: KeyboardEvent): boolean {
  if (e.altKey || e.shiftKey || e.key.toLowerCase() !== 'k') return false
  return isMac ? e.metaKey && !e.ctrlKey : e.ctrlKey && !e.metaKey
}

export function typing(target: EventTarget | null): boolean {
  return target instanceof HTMLElement && !!target.closest('input, textarea, select, [contenteditable]:not([contenteditable="false"])')
}

function score(item: PaletteItem, words: string[]): number {
  if (!words.length) return 1
  const name = item.label.toLowerCase()
  const hay = `${name} ${(item.detail ?? '').toLowerCase()} ${item.group.toLowerCase()}`
  if (!words.every(w => hay.includes(w))) return 0
  return (name.startsWith(words[0]!) ? 4 : 0) + (words.every(w => name.includes(w)) ? 2 : 0) + 1
}

function highlighted(text: string, words: string[]): (Node | string)[] {
  const lower = text.toLowerCase()
  if (lower.length !== text.length) return [text]
  const hits: [number, number][] = []
  for (const w of words) {
    const at = lower.indexOf(w)
    if (at >= 0) hits.push([at, at + w.length])
  }
  hits.sort((a, b) => a[0] - b[0])
  const out: (Node | string)[] = []
  let pos = 0
  for (const [from, to] of hits) {
    const start = Math.max(from, pos)
    if (to <= start) continue
    if (start > pos) out.push(text.slice(pos, start))
    out.push(el('span', 'palette-hit', text.slice(start, to)))
    pos = to
  }
  if (pos < text.length) out.push(text.slice(pos))
  return out
}

function keyCaps(keys: string[]): HTMLElement {
  const box = el('span', 'palette-keys')
  keys.forEach((chord, i) => {
    if (i) box.append(el('span', 'palette-chord', ' '))
    const parts = chord.length > 1 && !chord.endsWith('+') ? chord.split('+') : [chord]
    parts.forEach((part, j) => {
      if (j && !isMac) box.append(el('span', 'palette-key-plus', '+'))
      box.append(el('kbd', 'palette-key', part))
    })
  })
  return box
}

export function openPalette(list: () => PaletteItem[]): void {
  if (reopen('palette')) return
  const items = list()
  const input = el('input', 'textbox palette-input')
  input.type = 'text'
  input.id = 'palette-input'
  input.autocomplete = 'off'
  input.spellcheck = false
  input.placeholder = 'Search sessions, projects and commands'
  input.setAttribute('role', 'combobox')
  input.setAttribute('aria-expanded', 'true')
  input.setAttribute('aria-controls', 'palette-list')
  input.setAttribute('aria-autocomplete', 'list')
  const head = field('Command', input, { kind: 'hidden', cls: 'palette-head' })
  const listbox = el('div', 'palette-list')
  listbox.id = 'palette-list'
  listbox.setAttribute('role', 'listbox')
  listbox.setAttribute('aria-label', 'Commands')
  const none = el('p', 'palette-none', 'No matching results')
  none.hidden = true
  const said = statusLine('sr-only')
  let shown: PaletteItem[] = []
  let at = -1

  const options = () => [...listbox.querySelectorAll<HTMLElement>('[role="option"]')]
  const mark = (i: number, scroll = true) => {
    const all = options()
    at = i
    all.forEach((o, j) => o.setAttribute('aria-selected', String(j === i)))
    const node = all[i]
    if (node) {
      input.setAttribute('aria-activedescendant', node.id)
      if (scroll) node.scrollIntoView({ block: 'nearest' })
    } else input.removeAttribute('aria-activedescendant')
  }
  const move = (step: number) => {
    if (!shown.length) return
    let i = at
    for (let n = 0; n < shown.length; n++) {
      i = (i + step + shown.length) % shown.length
      if (!shown[i]!.off) return mark(i)
    }
  }
  const page = (step: number) => {
    const row = listbox.querySelector<HTMLElement>('[role="option"]')?.offsetHeight || 1
    const rows = Math.max(1, Math.floor(listbox.clientHeight / row) - 1)
    const target = Math.min(shown.length - 1, Math.max(0, (at < 0 ? 0 : at) + step * rows))
    for (let i = target; i >= 0 && i < shown.length; i += step > 0 ? -1 : 1) {
      if (!shown[i]!.off) return mark(i)
    }
  }
  const run = (i: number) => {
    const item = shown[i]
    if (!item) return
    if (item.off) {
      said.textContent = `${visible(item.label)} isn't available: ${visible(item.off)}`
      return
    }
    m.close()
    item.run()
    settleFocus()
  }
  const paint = () => {
    const words = input.value.toLowerCase().split(/\s+/).filter(Boolean)
    const ranked = items.map(item => ({ item, s: score(item, words) })).filter(x => x.s > 0)
    if (words.length) ranked.sort((a, b) => b.s - a.s)
    shown = ranked.slice(0, MAX_SHOWN).map(x => x.item)
    const groups = new Map<string, PaletteItem[]>()
    for (const item of shown) groups.set(item.group, [...(groups.get(item.group) ?? []), item])
    shown = [...groups.values()].flat()
    const nodes: HTMLElement[] = []
    let n = 0
    for (const [title, group] of groups) {
      const box = el('div', 'palette-group')
      box.setAttribute('role', 'group')
      box.setAttribute('aria-label', visible(title))
      group.forEach((item, k) => {
        const i = n++
        const o = el('div', `palette-option${item.off ? ' off' : ''}`)
        o.id = `palette-o-${i}`
        o.setAttribute('role', 'option')
        o.setAttribute('aria-selected', 'false')
        if (item.off) o.setAttribute('aria-disabled', 'true')
        const label = el('span', 'palette-label')
        label.append(...highlighted(visible(item.label), words))
        o.append(label)
        const detail = item.off ? `Not now: ${item.off}` : item.detail ?? ''
        if (detail) o.append(el('span', 'palette-detail', visible(detail)))
        if (item.keys?.length) o.append(keyCaps(item.keys))
        if (k === 0) {
          const tag = el('span', 'palette-group-name', visible(title))
          tag.setAttribute('aria-hidden', 'true')
          o.append(tag)
        }
        o.addEventListener('click', () => run(i))
        o.addEventListener('pointermove', () => {
          if (at !== i && !item.off) mark(i, false)
        })
        box.append(o)
      })
      nodes.push(box)
    }
    listbox.replaceChildren(...nodes)
    listbox.hidden = !shown.length
    none.hidden = !!shown.length
    at = -1
    move(1)
    said.textContent = shown.length ? `${shown.length} ${shown.length === 1 ? 'result' : 'results'}` : 'No results'
  }
  input.addEventListener('input', paint)
  input.addEventListener('keydown', e => {
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault()
      move(e.key === 'ArrowDown' ? 1 : -1)
    } else if (e.key === 'PageDown' || e.key === 'PageUp') {
      e.preventDefault()
      page(e.key === 'PageDown' ? 1 : -1)
    } else if (e.key === 'Enter' && !e.isComposing) {
      e.preventDefault()
      run(at)
    }
  })
  const m = modal({
    kind: 'palette',
    consent: false,
    title: 'Command palette',
    docs: 'working-in-a-session',
    headingClass: 'sr-only',
    body: [head, listbox, none, said],
    focus: input,
    single: 'palette',
    searchOnly: true,
  })
  paint()
}

export interface Shortcut {
  keys: string[]
  what: string
  where: string
}

export function shortcutList(): Shortcut[] {
  const mod = isMac ? '⌘' : 'Ctrl'
  return [
    { keys: [`${mod}+K`], what: 'Open the command palette', where: 'Anywhere' },
    { keys: ['?'], what: 'Show this list (you can turn this key off below)', where: 'Anywhere but a text field' },
    { keys: ['Escape'], what: 'Close the palette, a dialog or a menu; leave the full execution path', where: 'Anywhere' },
    { keys: ['Home', 'End'], what: 'Jump to the start or the latest of the conversation', where: 'A session, outside a text field' },
    { keys: ['Enter'], what: 'Send the message', where: 'The message box' },
    { keys: ['Shift+Enter'], what: 'Start a new line', where: 'The message box' },
    { keys: [`${mod}+Enter`], what: 'Add the comment or save the prompt', where: 'A line comment or a saved prompt' },
    { keys: ['↑', '↓', 'Enter'], what: 'Choose a line of the diff to comment on', where: 'The Changes view’s diff' },
    { keys: ['←', '→', 'Home', 'End'], what: 'Move between tabs', where: 'The tab bar' },
    { keys: ['Delete'], what: 'Close the focused tab', where: 'The tab bar' },
    { keys: ['Ctrl+Shift+PageUp', 'Ctrl+Shift+PageDown'], what: 'Move the focused tab left or right', where: 'The tab bar' },
    { keys: ['↑', '↓', 'Home', 'End'], what: 'Move between a menu’s items', where: 'An open menu' },
    { keys: ['Alt+Z'], what: 'Turn line wrapping on or off', where: 'The file viewer' },
  ]
}

export function openShortcuts(questionOn: boolean, setQuestion: (on: boolean) => void): void {
  const table = el('table', 'shortcut-table small')
  const caption = el('caption', 'sr-only', 'Keys, what they do and where')
  const headRow = el('tr')
  for (const h of ['Keys', 'What it does', 'Where']) {
    const th = el('th', '', h)
    th.scope = 'col'
    headRow.append(th)
  }
  const thead = el('thead')
  thead.append(headRow)
  const tbody = el('tbody')
  for (const s of shortcutList()) {
    const tr = el('tr')
    const keys = el('td', 'shortcut-keys')
    s.keys.forEach((k, i) => {
      if (i) keys.append(' ')
      keys.append(el('kbd', '', k))
    })
    tr.append(keys, el('td', '', s.what), el('td', 'muted', s.where))
    tbody.append(tr)
  }
  table.append(caption, thead, tbody)
  const box = el('input')
  box.type = 'checkbox'
  box.checked = questionOn
  box.addEventListener('change', () => setQuestion(box.checked))
  const toggle = form({ children: [field('Open this list with the ? key', box, { kind: 'check' })] })
  const note = el('p', 'small muted', 'None of these replaces a key the browser or a screen reader uses: the palette key is the browser’s search shortcut only while this page has focus, and ? works only outside text fields. A screen reader in browse mode keeps every key it uses for itself.')
  const closeButton = button('Close', 'ghost small', () => m.close())
  const wrap = el('div', 'shortcut-wrap')
  wrap.tabIndex = 0
  wrap.setAttribute('role', 'region')
  wrap.setAttribute('aria-label', 'Keyboard shortcuts')
  wrap.append(table)
  const m = modal({
    kind: 'shortcuts',
    title: 'Keyboard shortcuts',
    docs: 'working-in-a-session',
    body: [wrap, toggle, note],
    actions: [closeButton],
    focus: closeButton,
  })
}
