import { actionButton, button, el, rovingKeys } from '../lib/dom.ts'
import { placeMenu } from '../lib/menu.ts'
import { icon } from './icon.ts'
import { titled } from './session-list.ts'

export interface RowMenuItem {
  text: string
  glyph: string
  run: (from: HTMLElement) => void
}

export function rowMenu(name: string, key: string, items: RowMenuItem[]): HTMLElement[] {
  const more = button('', 'ghost icon-button', undefined, 'button', key)
  more.setAttribute('aria-label', `More actions for ${name}`)
  more.setAttribute('aria-haspopup', 'menu')
  more.append(icon('more'))
  const menu = el('div', 'menu row-menu surface')
  menu.popover = 'auto'
  menu.setAttribute('role', 'menu')
  menu.setAttribute('aria-label', `Actions for ${name}`)
  more.popoverTargetElement = menu
  for (const it of items) {
    const b = button('', '', () => {
      menu.hidePopover()
      more.focus()
      it.run(more)
    })
    b.setAttribute('role', 'menuitem')
    const mark = el('span', 'mark')
    mark.append(icon(it.glyph))
    b.append(mark, el('span', '', it.text))
    menu.append(b)
  }
  menu.addEventListener('toggle', e => {
    if ((e as ToggleEvent).newState !== 'open') return
    placeMenu(more, menu)
    menu.querySelector<HTMLElement>('button')?.focus()
  })
  menu.addEventListener('keydown', e => {
    const all = [...menu.querySelectorAll<HTMLElement>('button')]
    rovingKeys(e, all, all.indexOf(document.activeElement as HTMLElement), 'vertical')
  })
  return [more, menu]
}

export function rowName(name: string, ...tags: HTMLElement[]): HTMLElement {
  const cell = titled(name)
  cell.append(...tags)
  return cell
}

export function rowNote(text: string, tone = 'muted'): HTMLElement {
  return el('p', tone ? `small row-note ${tone}` : 'small row-note', text)
}

export function tableRow(cells: Node[], menu: HTMLElement[]): HTMLLIElement {
  const li = el('li', 'with-menu')
  const row = el('div', 'boxlist-row session-cells')
  row.append(...cells)
  li.append(row, ...menu)
  return li
}

export function itemTable(cls: string, label: string, heads: string[], rows: HTMLLIElement[]): HTMLDivElement {
  const head = el('div', 'sessions-head secondary')
  head.setAttribute('aria-hidden', 'true')
  head.append(...heads.map(h => el('span', '', h)))
  const ul = el('ul', 'sessions')
  ul.setAttribute('aria-label', label)
  ul.append(...rows)
  const table = el('div', `session-table item-table ${cls} with-menu`)
  table.append(head, ul)
  return table
}

export function focusIfLost(root: HTMLElement, key: string): void {
  const at = document.activeElement
  if (at && at !== document.body && at.isConnected) return
  focusKey(root, key)
}

export function askRow(name: HTMLElement, question: string, key: string, no: () => void, yes: () => Promise<unknown>): HTMLLIElement {
  const li = el('li', 'with-menu asking')
  const row = el('div', 'boxlist-row session-cells')
  const ask = el('span', 'acts session-ask')
  ask.append(el('span', 'small muted', question), button('Cancel', 'ghost small', no, 'button', `no:${key}`), actionButton('Delete', 'ghost small danger', yes, 'button', `yes:${key}`))
  row.append(name, ask)
  li.append(row)
  return li
}

export function focusKey(root: HTMLElement, key: string): void {
  root.querySelector<HTMLElement>(`[data-key="${CSS.escape(key)}"]`)?.focus()
}
