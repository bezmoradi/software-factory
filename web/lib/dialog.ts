import { button, el, opener } from './dom.ts'

export interface ModalOptions {
  kind: string
  title: string
  body?: Node[]
  lead?: Node[]
  actions?: Node[]
  consent?: boolean
  headingClass?: string
  closeButton?: boolean
  focus?: 'heading' | HTMLElement
  single?: string
  backdropCloses?: boolean
}

export interface Modal {
  dialog: HTMLDialogElement
  close(): void
  onClose(fn: () => void): void
}

const singles = new Map<string, { modal: Modal; focus: () => void }>()
let titles = 0

export function actionRow(lead: Node[], end: Node[]): HTMLDivElement {
  const row = el('div', 'row consent-actions')
  row.append(...lead, el('span', 'spacer'), ...end)
  return row
}

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
  const heading = el('h2', o.headingClass ?? (o.closeButton ? 'dialog-title' : 'label'), o.title)
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
  let head: Node = heading
  if (o.closeButton) {
    const cross = button('', 'ghost icon-button dialog-x', () => handle.close())
    cross.setAttribute('aria-label', 'Close')
    cross.title = 'Close'
    const mark = el('sf-icon', 'icon')
    mark.setAttribute('name', 'close')
    cross.append(mark)
    const wrap = el('div', 'dialog-head')
    wrap.append(heading, cross)
    head = wrap
  }
  d.append(head, ...(o.body ?? []))
  if (o.lead?.length || o.actions?.length) d.append(actionRow(o.lead ?? [], o.actions ?? []))
  d.addEventListener('cancel', e => {
    e.preventDefault()
    handle.close()
  })
  d.addEventListener('close', finish)
  if (o.backdropCloses) {
    d.addEventListener('click', e => {
      if (e.target === d) handle.close()
    })
  }
  if (o.single) singles.set(o.single, { modal: handle, focus })
  document.body.append(d)
  d.showModal()
  focus()
  return handle
}

export function confirmDialog(title: string, parts: Node[], yes: string, tone: 'primary' | 'danger', cleanup?: () => void, no = 'Cancel'): Promise<boolean> {
  return new Promise(resolve => {
    let answer = false
    const m = modal({
      kind: '',
      title,
      body: parts,
      actions: [
        button(no, 'ghost small', () => m.close()),
        button(yes, `${tone} small`, () => {
          answer = true
          m.close()
        }),
      ],
    })
    if (cleanup) m.onClose(cleanup)
    m.onClose(() => resolve(answer))
  })
}
