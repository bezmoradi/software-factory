import { el } from './dom.ts'
import type { Catalog, CatalogEntry } from '../types.ts'

const SHOWN = 30

export interface FileList {
  files: string[]
  cut: boolean
}

interface Item {
  label: string
  detail: string
  insert: string
}

function rank(name: string, query: string): number {
  const n = name.toLowerCase()
  if (!query || n.startsWith(query)) return 0
  if (n.slice(n.indexOf(':') + 1).startsWith(query)) return 1
  if (n.includes(query)) return 2
  let i = 0
  for (const c of n) if (c === query[i]) i++
  return i === query.length ? 3 : -1
}

function pick(list: CatalogEntry[], query: string): CatalogEntry[] {
  const q = query.toLowerCase()
  return list
    .map((item, i): [number, number, CatalogEntry] => [rank(item.name, q), i, item])
    .filter(r => r[0] >= 0)
    .sort((a, b) => a[0] - b[0] || a[1] - b[1])
    .map(r => r[2])
}

export function complete(text: HTMLTextAreaElement, menu: HTMLElement, { catalog, files }: { catalog: () => Catalog | null; files: (query: string) => Promise<FileList> }): void {
  let items: Item[] = []
  let cut = false
  let active = 0
  let range: [number, number] | null = null
  let ask = 0
  let timer = 0
  let told = 0
  let toldActive = -1
  const said = document.createElement('span')
  said.className = 'sr-only'
  said.setAttribute('role', 'status')
  menu.after(said)
  const tell = (n: number) => {
    const line = !n ? '' : n !== told ? `${n} suggestion${n === 1 ? '' : 's'}: use the up and down arrows, then Tab or Enter` : active !== toldActive ? `${items[active]?.label ?? ''}, ${active + 1} of ${n}` : said.textContent
    told = n
    toldActive = n ? active : -1
    if (said.textContent !== line) said.textContent = line
  }

  const close = () => {
    items = []
    cut = false
    range = null
    menu.hidden = true
    menu.replaceChildren()
    text.removeAttribute('aria-activedescendant')
    tell(0)
  }

  const reveal = () => {
    const node = menu.children[active] as HTMLElement | undefined
    if (!node) return
    if (node.offsetTop < menu.scrollTop) menu.scrollTop = node.offsetTop
    else if (node.offsetTop + node.offsetHeight > menu.scrollTop + menu.clientHeight) menu.scrollTop = node.offsetTop + node.offsetHeight - menu.clientHeight
  }

  const paint = () => {
    if (!items.length && !cut) {
      menu.hidden = true
      menu.replaceChildren()
      text.removeAttribute('aria-activedescendant')
      tell(0)
      return
    }
    menu.replaceChildren(
      ...items.map((item, i) => {
        const li = el('li', i === active ? 'active' : '')
        li.id = `complete-${i}`
        li.setAttribute('role', 'option')
        li.setAttribute('aria-selected', String(i === active))
        li.append(el('span', 'name', item.label))
        if (item.detail) li.append(el('span', 'detail', item.detail))
        li.addEventListener('mousedown', e => {
          e.preventDefault()
          accept(i)
        })
        return li
      }),
    )
    if (cut) {
      const note = el('li', 'cut')
      note.setAttribute('role', 'option')
      note.setAttribute('aria-disabled', 'true')
      note.append(el('span', 'detail', 'Not every file is listed. Type the full path.'))
      note.addEventListener('mousedown', e => e.preventDefault())
      menu.append(note)
    }
    menu.hidden = false
    if (items.length) text.setAttribute('aria-activedescendant', `complete-${active}`)
    else text.removeAttribute('aria-activedescendant')
    tell(items.length)
    reveal()
  }

  const accept = (i: number) => {
    const item = items[i]
    if (!item || !range) return
    text.setRangeText(item.insert, range[0], range[1], 'end')
    close()
    text.focus()
    text.dispatchEvent(new Event('input', { bubbles: true }))
  }

  const update = () => {
    const ticket = ++ask
    clearTimeout(timer)
    const caret = text.selectionStart
    if (caret !== text.selectionEnd) return close()
    const before = text.value.slice(0, caret)
    const { commands = [], agents = [] }: Partial<Catalog> = catalog() || {}
    const slash = /^\/(\S*)$/.exec(before)
    cut = false
    if (slash) {
      range = [0, caret]
      items = pick(commands, slash[1]!)
        .slice(0, SHOWN)
        .map(c => ({ label: `/${c.name}`, detail: c.description, insert: `/${c.name} ` }))
      active = 0
      return paint()
    }
    const at = /(^|\s)@([^\s@]*)$/.exec(before)
    if (!at) return close()
    const query = at[2]!
    range = [caret - query.length - 1, caret]
    const agentItems = pick(agents, query.replace(/^agent-/, ''))
      .slice(0, SHOWN)
      .map(a => ({ label: `${a.name} (agent)`, detail: a.description, insert: `@agent-${a.name} ` }))
    items = agentItems
    active = 0
    paint()
    timer = setTimeout(async () => {
      const found = await files(query).catch((): FileList => ({ files: [], cut: false }))
      if (ticket !== ask || !range) return
      items = [...found.files.map(f => ({ label: f, detail: '', insert: `@${f} ` })), ...agentItems].slice(0, SHOWN)
      cut = found.cut
      active = 0
      paint()
    }, 120)
  }

  text.setAttribute('aria-autocomplete', 'list')
  text.setAttribute('aria-haspopup', 'listbox')
  text.setAttribute('aria-controls', menu.id)
  text.addEventListener('input', update)
  text.addEventListener('click', update)
  text.addEventListener('blur', () => {
    ask++
    clearTimeout(timer)
    close()
  })
  text.addEventListener('keydown', e => {
    if (menu.hidden || e.isComposing) return
    if (!items.length) {
      if (e.key !== 'Escape') return
      close()
      e.preventDefault()
      e.stopImmediatePropagation()
      return
    }
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      active = (active + (e.key === 'ArrowDown' ? 1 : items.length - 1)) % items.length
      paint()
    } else if (e.key === 'Tab' || (e.key === 'Enter' && !e.shiftKey)) {
      if (e.key === 'Enter' && text.value.trim() === items[active]!.insert.trim()) {
        close()
        return
      }
      accept(active)
    } else if (e.key === 'Escape') close()
    else return
    e.preventDefault()
    e.stopImmediatePropagation()
  })
}
