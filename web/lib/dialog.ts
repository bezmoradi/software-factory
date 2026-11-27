import { button, el, opener } from './dom.ts'
import { actions } from './form.ts'
import { docsLink, pageHead } from './page.ts'

export interface ModalOptions {
  kind: string
  title: string
  body?: Node[]
  lead?: Node[]
  actions?: Node[]
  consent?: boolean
  headingClass?: string
  focus?: 'heading' | HTMLElement
  single?: string
  docs: string | false
  searchOnly?: boolean
  expand?: boolean
}

export interface Modal {
  dialog: HTMLDialogElement
  close(): void
  onClose(fn: () => void): void
}

function sized(d: HTMLDialogElement, onClose: (fn: () => void) => void): HTMLButtonElement {
  const b = button('', 'dialog-expand')
  const mark = el('sf-icon', 'icon')
  b.setAttribute('aria-label', 'Expand')
  const paint = (on: boolean) => {
    d.classList.toggle('expanded', on)
    b.setAttribute('aria-pressed', String(on))
    b.title = on ? 'Restore' : 'Expand'
    mark.setAttribute('name', on ? 'collapse' : 'expand')
  }
  const fit = () => {
    if (d.classList.contains('expanded')) return
    const root = document.documentElement
    const rem = parseFloat(getComputedStyle(root).fontSize)
    b.hidden = d.getBoundingClientRect().width >= root.clientWidth - 3 * rem
  }
  b.append(mark)
  b.addEventListener('click', () => paint(!d.classList.contains('expanded')))
  paint(false)
  const watch = new ResizeObserver(fit)
  watch.observe(d)
  addEventListener('resize', fit)
  onClose(() => {
    watch.disconnect()
    removeEventListener('resize', fit)
  })
  return b
}

const singles = new Map<string, { modal: Modal; focus: () => void }>()
let titles = 0

export function reopen(single: string): boolean {
  const open = singles.get(single)
  if (!open) return false
  open.focus()
  return true
}

export function modal(o: ModalOptions): Modal {
  const existing = o.single ? singles.get(o.single) : undefined
  if (existing) {
    existing.focus()
    return existing.modal
  }
  const back = opener()
  const d = el('dialog', [o.consent === false ? '' : 'consent', o.kind, 'surface'].filter(Boolean).join(' '))
  const heading = el('h2', o.headingClass ?? 'label', o.title)
  heading.id = `dialog-title-${++titles}`
  d.setAttribute('aria-labelledby', heading.id)
  const cleanups: (() => void)[] = []
  let closed = false
  const finish = () => {
    if (closed) return
    closed = true
    if (o.single && singles.get(o.single)?.modal === handle) singles.delete(o.single)
    for (const fn of cleanups.splice(0)) {
      try {
        fn()
      } catch (e) {
        reportError(e)
      }
    }
    d.remove()
    back()
  }
  const handle: Modal = {
    dialog: d,
    close: () => {
      if (d.open) d.close()
      finish()
    },
    onClose: fn => {
      if (closed) fn()
      else cleanups.push(fn)
    },
  }
  const focus = () => {
    if (o.focus === 'heading') heading.focus()
    else if (o.focus instanceof HTMLElement) o.focus.focus()
  }
  if (o.focus === 'heading') heading.tabIndex = -1
  const docs = o.docs === false ? undefined : docsLink({ doc: o.docs, open: () => handle.close() })
  const tools = [docs, o.expand ? sized(d, handle.onClose) : undefined].filter(t => t !== undefined)
  const head = pageHead({ title: heading })[0]!
  head.append(...tools)
  d.append(head, ...(o.body ?? []))
  if (o.lead?.length || o.actions?.length) d.append(actions(o.actions ?? [], o.lead ?? []))
  d.addEventListener('cancel', e => {
    e.preventDefault()
    handle.close()
  })
  d.addEventListener('close', finish)
  let changed = false
  let pressedOutside = false
  const outside = (e: MouseEvent) => {
    if (e.target !== d) return false
    const r = d.getBoundingClientRect()
    return e.clientX < r.left || e.clientX >= r.right || e.clientY < r.top || e.clientY >= r.bottom
  }
  const touched = () => {
    if (!o.searchOnly) changed = true
  }
  d.addEventListener('input', touched)
  d.addEventListener('change', touched)
  d.addEventListener('pointerdown', e => {
    pressedOutside = outside(e)
  })
  d.addEventListener('click', e => {
    const wasOutside = pressedOutside
    pressedOutside = false
    if (wasOutside && !changed && outside(e)) handle.close()
  })
  if (o.single) singles.set(o.single, { modal: handle, focus })
  document.body.append(d)
  d.showModal()
  focus()
  return handle
}

export function confirmDialog(title: string, docs: string, parts: Node[], yes: string, tone: 'primary' | 'danger', no = 'Cancel', expand = false): Promise<boolean> {
  return new Promise(resolve => {
    let answer = false
    const m = modal({
      kind: '',
      title,
      docs,
      expand,
      body: parts,
      actions: [
        button(no, 'ghost small', () => m.close()),
        button(yes, `${tone} small`, () => {
          answer = true
          m.close()
        }),
      ],
    })
    m.onClose(() => resolve(answer))
  })
}
