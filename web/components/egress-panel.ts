import { actionButton, button, el, field, focusedKey, labelError, refocus, statusLine, visible, whileBusy } from '../lib/dom.ts'
import { modal } from '../lib/dialog.ts'
import { secret } from './secret.ts'

type Post = (path: string, body: unknown) => Promise<Response>

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
  agents: boolean
  reviewers: boolean
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

type Answer = { ok: boolean; body: Record<string, unknown> }
type Refill = 'gateway' | null

async function call(post: Post, body: unknown, route = ROUTE): Promise<Answer> {
  const res = await post(route, body).catch(() => null)
  if (!res) return { ok: false, body: { message: 'The engine didn’t answer.' } }
  const parsed: unknown = await res.json().catch(() => ({}))
  return { ok: res.ok, body: parsed !== null && typeof parsed === 'object' ? parsed as Record<string, unknown> : {} }
}

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
    gateway: text(g.url) ? { url: text(g.url), host: text(g.host), agents: g.agents === true, reviewers: g.reviewers === true } : null,
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
  const intro = el('p', 'small', 'Agents and reviewers reach the internet only through egress, which allows HTTPS to the hosts below and refuses every git host: the engine pushes for them. A change here takes effect at once, without a restart.')
  const startNote = el('p', 'small muted')
  const pending = el('p', 'small bad')
  const vendorNote = el('p', 'small muted', 'Once you save a change here, Claude Code’s own hosts are on both lists, except on a side a gateway is on for.')
  const addedList = el('ul', 'list egress-added')
  const hostInput = el('input', 'textbox')
  hostInput.name = 'host'
  hostInput.spellcheck = false
  hostInput.autocomplete = 'off'
  hostInput.placeholder = 'registry.npmjs.org'
  hostInput.dataset.key = 'host'
  const reasonInput = el('input', 'textbox')
  reasonInput.name = 'reason'
  reasonInput.autocomplete = 'off'
  reasonInput.placeholder = 'Why the agents need it (optional)'
  const expiry = el('select', 'textbox')
  const hint = el('p', 'small muted')
  const uploadWarn = el('div', 'notice warn small')
  uploadWarn.hidden = true
  const refusedBox = el('div', 'egress-refused')
  const gatewayURL = el('input', 'textbox')
  gatewayURL.name = 'gateway'
  gatewayURL.type = 'url'
  gatewayURL.spellcheck = false
  gatewayURL.autocomplete = 'off'
  gatewayURL.placeholder = 'https://llm-gateway.example.com'
  const gwAgents = el('input')
  gwAgents.type = 'checkbox'
  const gwReviewers = el('input')
  gwReviewers.type = 'checkbox'
  const gwInputs: Record<string, HTMLInputElement> = { url: gatewayURL, agents: gwAgents }
  const gwKey = el('input', 'textbox')
  gwKey.type = 'password'
  gwKey.name = 'gateway-key'
  gwKey.spellcheck = false
  gwKey.autocomplete = 'off'
  const gwNote = el('p', 'small muted')
  const gwSay = statusLine()
  const listsNote = el('p', 'small muted')
  let view: EgressView | null = null
  let gwFilled: { url: string; agents: boolean; reviewers: boolean } | null = null
  const gwEdited = () => !!gwFilled && (gatewayURL.value !== gwFilled.url || gwAgents.checked !== gwFilled.agents || gwReviewers.checked !== gwFilled.reviewers)

  const failed = (a: Answer, fallback: string) => {
    say.textContent = text(a.body.message) ? visible(text(a.body.message)) : fallback
  }

  const paint = (v: EgressView, refill: Refill = null) => {
    const key = focusedKey(m.dialog)
    const before = view?.added.map(h => h.host) ?? []
    view = v
    if (expiry.options.length !== v.expiries.length || v.expiries.some((c, i) => expiry.options[i]?.value !== c.value)) {
      const keep = expiry.value
      expiry.replaceChildren(...v.expiries.map(c => {
        const o = el('option', '', visible(c.label))
        o.value = c.value
        return o
      }))
      if (v.expiries.some(c => c.value === keep)) expiry.value = keep
    }
    startNote.textContent = v.saved ? '' : 'Until you save a change here, the lists are the starting values from SF_ALLOW and SF_EXTRA_ALLOW.'
    pending.textContent = v.pending ? 'egress hasn’t taken the latest list yet, so the sandbox check fails and sessions are Held until it does.' : ''
    addedList.replaceChildren()
    if (!v.added.length) addedList.append(el('li', 'muted', 'No hosts added.'))
    for (const h of v.added) {
      const li = el('li', 'row')
      li.append(el('code', '', visible(h.host)), el('span', 'small muted', until(h.expires, v.now)), el('span', 'small', typeof h.reason === 'string' && h.reason ? `“${visible(h.reason)}”` : 'no reason given'))
      if (h.uploads) li.append(el('span', 'tag warn', 'accepts uploads'))
      li.append(el('span', 'spacer'), actionButton('Remove', 'ghost small', () => remove(h.host), 'button', `remove:${h.host}`))
      addedList.append(li)
    }
    if (refill === 'gateway' || !gwEdited()) {
      gatewayURL.value = v.gateway?.url ?? ''
      gwAgents.checked = v.gateway ? v.gateway.agents : true
      gwReviewers.checked = v.gateway ? v.gateway.reviewers : true
      gwFilled = { url: gatewayURL.value, agents: gwAgents.checked, reviewers: gwReviewers.checked }
    }
    gwNote.textContent = v.gatewayKeyNote
    listsNote.textContent = `Agents reach: ${v.lists.agents.map(visible).join(', ') || 'nothing'}. Reviewers reach: ${v.lists.reviewers.map(visible).join(', ') || 'nothing'}.`
    refocus(m.dialog, key, k => {
      if (!k.startsWith('remove:')) return []
      const at = before.indexOf(k.slice('remove:'.length))
      return [...before.slice(at + 1), ...before.slice(0, Math.max(at, 0)).reverse()].map(h => `remove:${h}`).concat('host')
    })
  }

  const answer = (a: Answer, fallback: string, refill: Refill = null): boolean => {
    const v = viewOf(a.body)
    if (!a.ok || !v) {
      failed(a, fallback)
      void reload()
      return false
    }
    say.textContent = ''
    paint(v, refill)
    return true
  }

  const reload = async () => {
    const a = await call(post, { op: 'read' })
    const v = viewOf(a.body)
    if (v) paint(v)
    else failed(a, 'The allowlist couldn’t be read.')
  }

  const remove = async (host: string) => {
    answer(await call(post, { op: 'remove', host }), 'The host wasn’t removed.')
  }

  const add = async (uploadsOk: boolean) => {
    const host = hostInput.value
    const reason = reasonInput.value
    for (const input of [hostInput, expiry, reasonInput]) labelError(input, '')
    const a = await call(post, { op: 'add', host, expires: expiry.value, ...(uploadsOk ? { uploadsOk: true } : {}), ...(reason ? { reason } : {}) })
    if (!a.ok && a.body.error === 'uploads') {
      uploadWarn.replaceChildren(el('p', '', visible(text(a.body.message))), button('Cancel', 'ghost small', () => {
        uploadWarn.hidden = true
        hostInput.focus()
      }), actionButton('Add anyway', 'ghost small', () => add(true)))
      uploadWarn.hidden = false
      return
    }
    const fromWarn = uploadWarn.contains(document.activeElement)
    uploadWarn.hidden = true
    const own = a.ok ? null : a.body.error === 'host' ? hostInput : a.body.error === 'reason' ? reasonInput : a.body.error === 'expires' ? expiry : null
    if (own && text(a.body.message)) {
      say.textContent = ''
      labelError(own, visible(text(a.body.message)))
      return own.focus()
    }
    const added = answer(a, 'The host wasn’t added.')
    if (!added && !fromWarn) return hostInput.focus()
    if (added) {
      hostInput.value = ''
      reasonInput.value = ''
      hint.textContent = visible(text(a.body.note))
      void loadRefusals()
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

  const loadRefusals = async () => {
    const a = await call(post, { op: 'refusals' })
    if (!a.ok) {
      refusedBox.replaceChildren(el('p', 'small warn', text(a.body.message) ? visible(text(a.body.message)) : 'egress didn’t say what it refused.'))
      return
    }
    const sides = refusalsOf(a.body)
    refusedBox.replaceChildren()
    for (const [side, title] of [['agents', 'Agents'], ['reviewers', 'Reviewers']] as const) {
      const list = sides[side] ?? []
      refusedBox.append(el('h4', 'label', title))
      if (!list.length) {
        refusedBox.append(el('p', 'small muted', 'Nothing refused since egress started.'))
        continue
      }
      const ul = el('ul', 'list')
      for (const r of list) {
        const li = el('li', 'row')
        li.append(el('code', '', visible(r.host)), el('span', 'small muted', `${visible(r.reasonLabel || r.reason)} · ${r.count}× · last ${when(r.last)}`))
        if (r.gitHost) li.append(el('span', 'tag', 'git host'))
        if (r.probe) li.append(el('span', 'tag', 'the sandbox check tries this host'))
        li.append(el('span', 'spacer'))
        if (r.addable) li.append(button('Add this host', 'ghost small', () => prefill(r)))
        ul.append(li)
      }
      refusedBox.append(ul)
    }
  }

  const gwSides = (): ('agents' | 'reviewers')[] => {
    const g = view?.gateway
    if (!g) return []
    return [...(g.agents ? ['agents' as const] : []), ...(g.reviewers ? ['reviewers' as const] : [])]
  }

  const gwTell = (lines: string[], bad: boolean) => {
    gwSay.textContent = lines.join(' ')
    gwSay.classList.toggle('bad', bad)
  }

  const keySteps = async (body: (target: 'reviewers') => Record<string, string>, done: string, fallback: string) => {
    const sides = gwSides()
    const sessions = sides.includes('agents') ? ['For Claude Code sessions, paste the gateway’s key in each session’s sign-in panel, since each session runs in its own container.'] : []
    if (!sides.includes('reviewers')) return gwTell(sessions, false)
    const a = await call(post, body('reviewers'), LOGIN_ROUTE)
    if (!a.ok) return gwTell([`Reviewers: ${text(a.body.message) ? visible(text(a.body.message)) : fallback}`, ...sessions], true)
    gwTell([`${done} for the reviewers.`, ...sessions], false)
  }

  const saveGateway = async (clear: boolean) => {
    const key = clear ? '' : gwKey.value.trim()
    gwKey.value = ''
    gwKey.type = 'password'
    gwTell([], false)
    for (const input of Object.values(gwInputs)) labelError(input, '')
    const a = await call(post, { op: 'gateway', url: clear ? '' : gatewayURL.value.trim(), agents: gwAgents.checked, reviewers: gwReviewers.checked })
    const field = text(a.body.field)
    const own = !a.ok && text(a.body.message) && Object.hasOwn(gwInputs, field) ? gwInputs[field] : undefined
    if (own) {
      say.textContent = ''
      labelError(own, visible(text(a.body.message)))
      return own.focus()
    }
    if (!answer(a, 'The gateway wasn’t saved.', 'gateway') || !key || !gwSides().length) return
    gwTell(['Saving the key…'], false)
    await keySteps(target => ({ step: 'key', target, key }), 'Key saved', 'The key wasn’t saved. Try again.')
  }

  const removeKey = async () => {
    if (!gwSides().length) return gwTell(['No gateway is set.'], true)
    await keySteps(target => ({ step: 'clear-key', target }), 'Key removed', 'The key wasn’t removed.')
  }

  reasonInput.addEventListener('input', () => labelError(reasonInput, ''))
  gatewayURL.addEventListener('input', () => labelError(gatewayURL, ''))
  for (const box of [gwAgents, gwReviewers]) box.addEventListener('change', () => labelError(gwAgents, ''))
  expiry.addEventListener('change', () => labelError(expiry, ''))
  hostInput.addEventListener('input', () => {
    labelError(hostInput, '')
    hint.textContent = ''
  })
  const addForm = el('form', 'row field-row egress-add')
  addForm.noValidate = true
  hostInput.setAttribute('aria-required', 'true')
  const addButton = button('Add', 'primary small', undefined, 'submit')
  addForm.append(field('Host', hostInput), field('Allowed', expiry), field('Reason', reasonInput), addButton)
  addForm.addEventListener('submit', e => {
    e.preventDefault()
    void whileBusy(addButton, () => add(false))
  })
  const gwForm = el('form', 'egress-gateway')
  gwForm.noValidate = true
  const urlField = field('Base URL', gatewayURL)
  const aLabel = el('label', 'small row')
  gwAgents.id = `${gatewayURL.id}-agents`
  aLabel.htmlFor = gwAgents.id
  aLabel.append(gwAgents, el('span', '', 'Claude Code sessions'))
  const rLabel = el('label', 'small row')
  rLabel.append(gwReviewers, el('span', '', 'Reviewers'))
  const keyField = field('Key or token (leave empty to keep the saved one)', secret(gwKey, 'key'), undefined, 'egress-key')
  const gwActions = el('div', 'row')
  const saveGatewayButton = button('Save gateway', 'ghost small', undefined, 'submit')
  gwActions.append(saveGatewayButton, actionButton('Remove key', 'ghost small', removeKey), actionButton('Remove gateway', 'ghost small', () => saveGateway(true)))
  gwForm.append(urlField, aLabel, rLabel, keyField, gwActions, gwSay)
  gwForm.addEventListener('submit', e => {
    e.preventDefault()
    void whileBusy(saveGatewayButton, () => saveGateway(false))
  })
  const refreshRow = el('div', 'row')
  refreshRow.append(el('h3', 'label', 'Refused requests'), el('span', 'spacer'), actionButton('Refresh', 'ghost small', loadRefusals))
  const m = modal({
    kind: 'egress',
    title: 'Network access',
    body: [
      intro, startNote, pending, say,
      vendorNote,
      el('h3', 'label', 'Hosts you added'),
      el('p', 'small muted', 'For agents only; reviewers reach only Claude Code’s hosts and the gateway. A host you add gives every session a way out, so add only what a task needs, and give it an expiry when you can.'),
      addedList, addForm, hint, uploadWarn,
      refreshRow,
      el('p', 'small muted', 'What egress refused since it started: host, count and last time, never the path. It keeps one count per host and reason across all sessions, so it doesn’t say which session asked.'),
      refusedBox,
      el('h3', 'label', 'Company LLM gateway'),
      el('p', 'small', 'Sets Claude Code’s base URL (ANTHROPIC_BASE_URL) for the sessions and reviewers you choose, and adds the gateway’s host to their allowlist. While the gateway is on for a side, that side’s list leaves out Anthropic’s own hosts and Claude Code’s telemetry hosts (anthropic.com, claude.ai, claude.com, Statsig, Sentry, Datadog), unless the gateway is one of them, so a key pasted for the gateway can’t reach them; removing the gateway brings them back.'),
      el('p', 'small warn', `${GATEWAY_WARNING}.`),
      gwNote, gwForm,
      listsNote,
    ],
    actions: [button('Close', 'ghost small', () => m.close())],
  })
  void reload()
  void loadRefusals()
}
