import { UNREAD, savedTone, type EngineAnswer } from '../api.ts'
import { actionButton, button, el, focusedKey, labelError, refocus, say as sayLine, statusLine, visible, whileBusy } from '../lib/dom.ts'
import { modal } from '../lib/dialog.ts'
import { dialogDocs, GATEWAY_HEADING } from '../lib/dialog-docs.ts'
import { actions, answerControl, field, fieldRow, form, showAnswer } from '../lib/form.ts'
import { secret } from './secret.ts'
import { docsId, emptyState, pageHead } from '../lib/page.ts'
import { textbox, dropdown, option } from '../lib/controls.ts'
import { CHEAP_EVERY_MS, whileOpen, type WhileOpen } from '../lib/refresh.ts'

type Post = (path: string, body: unknown) => Promise<EngineAnswer>

const ROUTE = '/api/egress'
const LOGIN_ROUTE = '/api/claude-login'

export const GATEWAY_WARNING = "The gateway sees all code and decides every tool call; use only your own company's gateway or the vendors' hosts"

interface AddedHost {
  host: string
  expires: number
  uploads: boolean
  reason?: string
}

interface GatewayView {
  url: string
  host: string
}

interface EgressView {
  saved: boolean
  pending: boolean
  added: AddedHost[]
  gateway: GatewayView | null
  lists: { agents: string[]; reviewers: string[] }
  now: number
  gatewayKeyNote: string
  expiries: { value: string; label: string }[]
}

interface Refusal {
  host: string
  reason: string
  reasonLabel: string
  count: number
  last: number
  addable: boolean
  gitHost: boolean
  uploads: boolean
  probe: boolean
  suggest: string[]
  note: string
}

type Answer = EngineAnswer
type Refill = 'gateway' | null

const text = (v: unknown): string => typeof v === 'string' ? v : ''
const strings = (v: unknown): string[] => Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : []
const record = (v: unknown): Record<string, unknown> => v !== null && typeof v === 'object' && !Array.isArray(v) ? v as Record<string, unknown> : {}

function viewOf(b: Record<string, unknown>): EgressView | null {
  if (!Array.isArray(b.added)) return null
  const lists = record(b.lists)
  const g = record(b.gateway)
  return {
    saved: b.saved === true,
    pending: b.pending === true,
    added: b.added.map(record).map(h => ({ host: text(h.host), expires: Number(h.expires) || 0, uploads: h.uploads === true })).filter(h => h.host),
    gateway: text(g.url) ? { url: text(g.url), host: text(g.host) } : null,
    lists: { agents: strings(record(lists.agents).allow), reviewers: strings(record(lists.reviewers).allow) },
    now: Number(b.now) || Math.floor(Date.now() / 1000),
    gatewayKeyNote: text(b.gatewayKeyNote),
    expiries: Array.isArray(b.expiries) ? b.expiries.map(record).map(c => ({ value: text(c.value), label: text(c.label) })).filter(c => c.value) : [],
  }
}

function refusalsOf(b: Record<string, unknown>): Record<string, Refusal[]> {
  const sides = record(b.refusals)
  const out: Record<string, Refusal[]> = {}
  for (const side of ['agents', 'reviewers']) {
    const list = Array.isArray(sides[side]) ? sides[side] as unknown[] : []
    out[side] = list.map(record).map(r => ({
      host: text(r.host), reason: text(r.reason), reasonLabel: text(r.reasonLabel), count: Number(r.count) || 0, last: Number(r.last) || 0,
      addable: r.addable === true, gitHost: r.gitHost === true, uploads: r.uploads === true, probe: r.probe === true, suggest: strings(r.suggest), note: text(r.note),
    })).filter(r => r.host)
  }
  return out
}

function until(expires: number, now: number): string {
  if (!expires) return 'no expiry'
  const left = expires - now
  if (left <= 0) return 'expired; egress refuses it'
  if (left < 3600) return `expires in ${Math.max(1, Math.round(left / 60))} min`
  if (left < 86400) return `expires in ${Math.round(left / 3600)} h`
  return `expires in ${Math.round(left / 86400)} d`
}

function when(at: number): string {
  return at ? new Date(at * 1000).toLocaleString() : ''
}


export function openEgress(post: Post): void {
  const say = statusLine('small warn')
  const intro = el('p', 'small')
  intro.append('Agents and reviewers reach the internet only through egress, which allows HTTPS to the hosts below. Changes take effect at once.')
  const pending = el('p', 'small bad')
  const addedList = el('ul', 'list egress-added')
  const hostInput = textbox({ name: 'host', spellcheck: false, autocomplete: 'off', placeholder: 'registry.npmjs.org' })
  hostInput.dataset.key = 'host'
  const reasonInput = textbox({ name: 'reason', autocomplete: 'off', placeholder: 'Why the agents need it' })
  const expiry = dropdown({ name: 'expires' })
  const hint = el('p', 'small muted')
  const uploadWarn = el('div', 'notice warn small')
  uploadWarn.hidden = true
  const refusedBox = el('div', 'egress-refused')
  const gatewayURL = textbox({ type: 'url', name: 'url', spellcheck: false, autocomplete: 'off', placeholder: 'https://llm-gateway.example.com' })
  const gwKey = textbox({ type: 'password', name: 'gateway-key', spellcheck: false, autocomplete: 'off', placeholder: 'Leave empty to keep the saved one' })
  const gwNote = el('span')
  const gwIntro = el('p', 'small')
  gwIntro.append('Sets Claude Code’s base URL for every session and the reviewers. ', gwNote)
  const gwSay = statusLine()
  let view: EgressView | null = null
  let gwFilled: { url: string } | null = null
  const gwEdited = () => !!gwFilled && gatewayURL.value !== gwFilled.url

  const paint = (v: EgressView, refill: Refill = null) => {
    const key = focusedKey(m.dialog)
    const before = view?.added.map(h => h.host) ?? []
    view = v
    if (expiry.options.length !== v.expiries.length || v.expiries.some((c, i) => expiry.options[i]?.value !== c.value)) {
      const keep = expiry.value
      expiry.replaceChildren(...v.expiries.map(c => option(visible(c.label), c.value)))
      if (v.expiries.some(c => c.value === keep)) expiry.value = keep
    }
    pending.textContent = v.pending ? 'egress hasn’t taken the latest list yet, so the sandbox check fails and sessions are Held until it does.' : ''
    addedList.replaceChildren()
    if (!v.added.length) addedList.append(emptyState('No hosts added.', { tag: 'li' }))
    for (const h of v.added) {
      const li = el('li', 'row')
      li.append(el('code', '', visible(h.host)), el('span', 'small muted', until(h.expires, v.now)), el('span', 'small', typeof h.reason === 'string' && h.reason ? `“${visible(h.reason)}”` : 'no reason given'))
      if (h.uploads) li.append(el('span', 'tag warn', 'accepts uploads'))
      li.append(el('span', 'spacer'), actionButton('Remove', 'ghost small', () => remove(h.host), 'button', `remove:${h.host}`))
      addedList.append(li)
    }
    if (refill === 'gateway' || !gwEdited()) {
      gatewayURL.value = v.gateway?.url ?? ''
      removeGatewayButton.hidden = !v.gateway
      gwFilled = { url: gatewayURL.value }
    }
    gwNote.textContent = v.gatewayKeyNote
    refocus(m.dialog, key, k => {
      if (!k.startsWith('remove:')) return []
      const at = before.indexOf(k.slice('remove:'.length))
      return [...before.slice(at + 1), ...before.slice(0, Math.max(at, 0)).reverse()].map(h => `remove:${h}`).concat('host')
    })
  }

  const answer = (a: Answer, place: HTMLElement, refill: Refill = null): boolean => {
    const v = viewOf(a.body)
    if (!a.ok || !v) {
      if (showAnswer(place, a, say, { tone: 'warn' })) say.textContent = UNREAD
      void reload()
      return false
    }
    showAnswer(place, a, say)
    paint(v, refill)
    return true
  }

  const reload = async () => {
    const a = await post(ROUTE, { op: 'read' })
    const v = viewOf(a.body)
    if (v) paint(v)
    else say.textContent = a.ok ? UNREAD : a.message
  }

  const remove = async (host: string) => {
    answer(await post(ROUTE, { op: 'remove', host }), addedList)
  }

  const add = async (uploadsOk: boolean) => {
    const host = hostInput.value
    const reason = reasonInput.value
    const a = await post(ROUTE, { op: 'add', host, expires: expiry.value, ...(uploadsOk ? { uploadsOk: true } : {}), ...(reason ? { reason } : {}) })
    if (!a.ok && a.error === 'uploads') {
      uploadWarn.replaceChildren(el('p', '', a.message), button('Cancel', 'ghost small', () => {
        uploadWarn.hidden = true
        hostInput.focus()
      }), actionButton('Add anyway', 'ghost small', () => add(true)))
      uploadWarn.hidden = false
      return
    }
    const fromWarn = uploadWarn.contains(document.activeElement)
    uploadWarn.hidden = true
    const added = answer(a, addForm)
    if (!added && answerControl(addForm, a)) return
    if (!added && !fromWarn) return hostInput.focus()
    if (added) {
      hostInput.value = ''
      reasonInput.value = ''
      hint.textContent = visible(text(a.body.note))
      void refusals?.now()
    }
    if (!fromWarn) return
    const shown = added ? view?.added.find(h => h.host.toLowerCase() === host.toLowerCase())?.host : undefined
    const remover = shown ? m.dialog.querySelector<HTMLButtonElement>(`[data-key="${CSS.escape(`remove:${shown}`)}"]`) : null
    ;(remover ?? hostInput).focus()
  }

  const prefill = (r: Refusal) => {
    hostInput.value = r.suggest[0] ?? r.host
    hint.textContent = visible(r.note)
    hostInput.focus()
  }

  let refusals: WhileOpen | undefined
  const showRefusals = (a: EngineAnswer) => {
    if (!a.ok) {
      refusedBox.replaceChildren(el('p', 'small warn', a.message))
      return
    }
    const sides = refusalsOf(a.body)
    refusedBox.replaceChildren()
    if (!sides.agents?.length && !sides.reviewers?.length) {
      refusedBox.append(emptyState('Nothing refused since egress started.'))
      return
    }
    for (const [side, title] of [['agents', 'Agents'], ['reviewers', 'Reviewers']] as const) {
      const list = sides[side] ?? []
      refusedBox.append(el('h4', 'label', title))
      if (!list.length) {
        refusedBox.append(emptyState('Nothing refused since egress started.'))
        continue
      }
      const ul = el('ul', 'list')
      for (const r of list) {
        const li = el('li', 'row')
        li.append(el('code', '', visible(r.host)), el('span', 'small muted', `${visible(r.reasonLabel || r.reason)} · ${r.count}× · last ${when(r.last)}`))
        if (r.gitHost) li.append(el('span', 'tag', 'git host'))
        if (r.probe) li.append(el('span', 'tag', 'the sandbox check tries this host'))
        li.append(el('span', 'spacer'))
        if (r.addable) li.append(button('Add this host', 'ghost small', () => prefill(r), 'button', `add:${side}:${r.host}`))
        ul.append(li)
      }
      refusedBox.append(ul)
    }
  }

  const gwTell = (lines: string[], bad: boolean) => {
    sayLine(gwSay, lines.join(' '), bad ? 'bad' : undefined)
  }

  const keySteps = async (body: (target: 'reviewers') => Record<string, string>, done: string) => {
    const sessions = ['For Claude Code sessions, paste the gateway’s key in each session’s sign-in panel, since each session runs in its own container.']
    const a = await post(LOGIN_ROUTE, body('reviewers'))
    if (!a.ok) return gwTell([`Reviewers: ${a.message}`, ...sessions], true)
    gwTell([`${done} for the reviewers.`, ...sessions], false)
  }

  const saveGateway = async (clear: boolean) => {
    const key = clear ? '' : gwKey.value.trim()
    gwKey.value = ''
    gwKey.type = 'password'
    gwTell([], false)
    const a = await post(ROUTE, { op: 'gateway', url: clear ? '' : gatewayURL.value.trim() })
    if (!answer(a, gwForm, 'gateway')) return
    if (!key || !view?.gateway) return sayLine(gwSay, a.message, savedTone(a))
    gwTell(['Saving the key…'], false)
    await keySteps(target => ({ step: 'key', target, key }), 'Key saved')
  }

  reasonInput.addEventListener('input', () => labelError(reasonInput, ''))
  gatewayURL.addEventListener('input', () => labelError(gatewayURL, ''))
  expiry.addEventListener('change', () => labelError(expiry, ''))
  hostInput.addEventListener('input', () => {
    labelError(hostInput, '')
    hint.textContent = ''
  })
  hostInput.setAttribute('aria-required', 'true')
  const addButton = button('Add', 'primary small', undefined, 'submit')
  const addForm = form({
    inline: true,
    cls: 'field-row egress-add',
    children: [field('Host', hostInput), field('Allowed', expiry), field('Reason', reasonInput, { optional: true }), actions([addButton])],
    onSubmit: () => void whileBusy(addButton, () => add(false)),
  })
  const urlField = field('Base URL', gatewayURL)
  const keyField = field('Key or token', secret(gwKey, 'key'), { cls: 'egress-key' })
  const saveGatewayButton = button('Save', 'primary small', undefined, 'submit')
  const removeGatewayButton = actionButton('Remove gateway', 'ghost small', () => saveGateway(true))
  removeGatewayButton.hidden = true
  const gwHead = pageHead({ title: GATEWAY_HEADING, level: 'h3', headingClass: 'label', docs: { doc: docsId(GATEWAY_HEADING), open: () => m.close() }, action: removeGatewayButton })[0]!
  const gwForm = form({
    cls: 'egress-gateway',
    children: [fieldRow([urlField, keyField, actions([saveGatewayButton])], 'halves'), gwSay],
    onSubmit: () => void whileBusy(saveGatewayButton, () => saveGateway(false)),
  })
  const m = modal({
    kind: 'egress',
    ...dialogDocs('egress'),
    body: [
      intro, pending, say,
      gwHead,
      gwIntro, gwForm,
      el('h3', 'label', 'Hosts you added'),
      el('p', 'small muted', 'For agents only. Add only what a task needs: a name like registry.npmjs.org, or .example.com for every name under it.'),
      addedList, addForm, hint, uploadWarn,
      el('h3', 'label', 'Refused requests'),
      el('p', 'small muted', 'What egress refused since it started, across all sessions.'),
      refusedBox,
    ],
    actions: [button('Close', 'ghost small', () => m.close())],
  })
  refusals = whileOpen({ root: refusedBox, read: () => post(ROUTE, { op: 'refusals' }), show: showRefusals, everyMs: CHEAP_EVERY_MS })
  m.onClose(() => refusals?.stop())
  void reload()
}
