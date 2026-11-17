import { busy, button, el, unspoofed, visible } from '../lib/dom.ts'
import { shortSha } from '../lib/format.ts'
import { money } from './consent.ts'
import './markdown.ts'
import type { AsideRun } from '../types.ts'

export interface AsideBox {
  id: string
  title: string
  start: string
  busy: string
  hint: string
  intro: string
  label: string
  run: AsideRun | null
  asking: boolean
  disabled: boolean
  stale: string
  press: () => void
}

function text(value: string): string {
  return visible(unspoofed(value))
}

function sentence(value: string): string {
  const t = value.trim()
  return /[.!?)]$/.test(t) ? t : `${t}.`
}

export function asideBox(o: AsideBox): HTMLElement {
  const box = el('div', 'aside-run')
  const t = o.run
  const running = t?.state === 'running' || o.asking
  const off = running || o.disabled
  const b = button(running ? o.busy : o.start, 'small', () => {
    if (b.getAttribute('aria-disabled') === 'true') return
    o.press()
  })
  b.id = o.id
  b.dataset.key = o.id
  b.setAttribute('aria-disabled', String(off))
  if (running) busy(b, true)
  b.title = o.hint
  const head = el('div', 'row aside-run-head')
  head.append(el('span', 'small', o.title), el('span', 'spacer'), b)
  box.append(head)
  if (!t) {
    box.append(el('p', 'small muted', o.intro))
    return box
  }
  const cost = money(t.costUsd)
  box.append(el('p', 'small muted', `For commit ${shortSha(t.commit)} on ${text(t.branch)}${o.stale}${cost ? `; ≈ ${cost}, Claude Code's own estimate` : ''}.`))
  if (t.state === 'running') box.append(el('p', 'small muted', 'Running in a reviewer container…'))
  if (t.reason) box.append(el('p', `small ${t.state === 'withheld' ? 'warn' : 'bad'}`, sentence(`${t.state === 'withheld' ? 'Not shown' : 'It failed'}: ${text(t.reason)}`)))
  if (t.state === 'done' && t.text) {
    const body = el('div', 'reply aside-run-text')
    body.setAttribute('role', 'region')
    body.setAttribute('aria-label', o.label)
    body.tabIndex = 0
    const md = el('sf-markdown', 'body')
    md.text = unspoofed(t.text)
    body.append(md)
    box.append(body, el('p', 'small muted', 'From a reviewer run with read-only tools. It isn\'t a verdict: the push gate, your approvals and the agent never see it.'))
  }
  return box
}
