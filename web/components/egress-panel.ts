import { button, el, visible } from '../lib/dom.ts'

type Post = (path: string, body: unknown) => Promise<Response>

const ROUTE = '/api/egress'
const LOGIN_ROUTE = '/api/claude-login'

export const GATEWAY_WARNING = "The gateway sees all code and decides every tool call; use only your own company's gateway or the vendors' hosts"

interface AgentUse {
  id: string
  name: string
  hosts: string[]
  inUse: boolean
}

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
  agents: AgentUse[]
  added: AddedHost[]
  gateway: GatewayView | null
  lists: { agents: string[]; reviewers: string[] }
  now: number
  gatewayKeyNote: string
  pypiNote: string
}

interface Refusal {
  host: string
  reason: string
  count: number
  last: number
  addable: boolean
  gitHost: boolean
  uploads: boolean
  probe: boolean
  suggest: string[]
}

type Answer = { ok: boolean; body: Record<string, unknown> }

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
  if (!Array.isArray(b.agents) || !Array.isArray(b.added)) return null
  const lists = record(b.lists)
  const g = record(b.gateway)
  return {
    saved: b.saved === true,
    pending: b.pending === true,
    agents: b.agents.map(record).map(a => ({ id: text(a.id), name: text(a.name), hosts: strings(a.hosts), inUse: a.inUse === true })).filter(a => a.id),
    added: b.added.map(record).map(h => ({ host: text(h.host), expires: Number(h.expires) || 0, uploads: h.uploads === true })).filter(h => h.host),
    gateway: text(g.url) ? { url: text(g.url), host: text(g.host), agents: g.agents === true, reviewers: g.reviewers === true } : null,
    lists: { agents: strings(record(lists.agents).allow), reviewers: strings(record(lists.reviewers).allow) },
    now: Number(b.now) || Math.floor(Date.now() / 1000),
    gatewayKeyNote: text(b.gatewayKeyNote),
    pypiNote: text(b.pypiNote),
  }
}

function refusalsOf(b: Record<string, unknown>): Record<string, Refusal[]> {
  const sides = record(b.refusals)
  const out: Record<string, Refusal[]> = {}
  for (const side of ['agents', 'reviewers']) {
    const list = Array.isArray(sides[side]) ? sides[side] as unknown[] : []
    out[side] = list.map(record).map(r => ({
      host: text(r.host), reason: text(r.reason), count: Number(r.count) || 0, last: Number(r.last) || 0,
      addable: r.addable === true, gitHost: r.gitHost === true, uploads: r.uploads === true, probe: r.probe === true, suggest: strings(r.suggest),
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

const REASONS: Record<string, string> = {
  not_allowlisted: 'not on the allowlist', git_host: 'a git host, always refused', port: 'a port other than 443', method: 'plain HTTP, not HTTPS',
  ip_literal: 'an IP address, not a name', sni_mismatch: 'a TLS name sent through another host', private_address: 'a private address',
  resolve_failed: 'the name didn’t resolve', dial_failed: 'the host didn’t answer', no_sni: 'no TLS name', ech: 'encrypted TLS hello', not_tls: 'not TLS', invalid_host: 'not a host name',
}

export function openEgress(post: Post): void {
  const d = el('dialog', 'consent egress surface')
  d.setAttribute('aria-label', 'Network access')
  const close = () => {
    d.close()
    d.remove()
  }
  const say = el('p', 'small warn')
  say.setAttribute('role', 'status')
  const intro = el('p', 'small', 'Agents and reviewers reach the internet only through egress, which allows HTTPS to the hosts below and refuses every git host: the engine pushes for them. A change here takes effect at once, without a restart.')
  const startNote = el('p', 'small muted')
  const pending = el('p', 'small bad')
  const agentsBox = el('fieldset', 'egress-agents')
  const addedList = el('ul', 'list egress-added')
  const hostInput = el('input', 'textbox')
  hostInput.name = 'host'
  hostInput.maxLength = 253
  hostInput.spellcheck = false
  hostInput.autocomplete = 'off'
  hostInput.placeholder = 'registry.npmjs.org'
  const reasonInput = el('input', 'textbox')
  reasonInput.name = 'reason'
  reasonInput.maxLength = 200
  reasonInput.autocomplete = 'off'
  reasonInput.placeholder = 'Why the agents need it (optional)'
  const expiry = el('select', 'textbox')
  for (const [value, words] of [['never', 'No expiry'], ['hour', 'For an hour'], ['day', 'For a day'], ['week', 'For a week']] as const) {
    const o = el('option', '', words)
    o.value = value
    expiry.append(o)
  }
  const hint = el('p', 'small muted')
  const uploadWarn = el('div', 'notice warn small')
  uploadWarn.hidden = true
  const refusedBox = el('div', 'egress-refused')
  const gatewayURL = el('input', 'textbox')
  gatewayURL.name = 'gateway'
  gatewayURL.type = 'url'
  gatewayURL.maxLength = 512
  gatewayURL.spellcheck = false
  gatewayURL.autocomplete = 'off'
  gatewayURL.placeholder = 'https://llm-gateway.example.com'
  const gwAgents = el('input')
  gwAgents.type = 'checkbox'
  const gwReviewers = el('input')
  gwReviewers.type = 'checkbox'
  const gwKey = el('input', 'textbox')
  gwKey.type = 'password'
  gwKey.name = 'gateway-key'
  gwKey.maxLength = 512
  gwKey.spellcheck = false
  gwKey.autocomplete = 'off'
  const gwNote = el('p', 'small muted')
  const gwSay = el('p', 'small')
  gwSay.setAttribute('role', 'status')
  const listsNote = el('p', 'small muted')
  let view: EgressView | null = null

  const failed = (a: Answer, fallback: string) => {
    say.textContent = text(a.body.message) ? visible(text(a.body.message)) : fallback
  }

  const paint = (v: EgressView) => {
    view = v
    startNote.textContent = v.saved ? '' : 'Until you save a change here, the lists are the starting values from SF_ALLOW and SF_EXTRA_ALLOW.'
    pending.textContent = v.pending ? 'egress hasn’t taken the latest list yet, so the sandbox check fails and sessions are Held until it does.' : ''
    agentsBox.replaceChildren(el('legend', 'label', 'Agents you use'))
    for (const a of v.agents) {
      const label = el('label', 'small row')
      const box = el('input')
      box.type = 'checkbox'
      box.value = a.id
      box.checked = a.inUse
      label.append(box, el('span', '', `${visible(a.name)}: ${a.hosts.map(visible).join(', ')}`))
      agentsBox.append(label)
    }
    agentsBox.append(button('Save agents', 'ghost small', () => void saveAgents()))
    addedList.replaceChildren()
    if (!v.added.length) addedList.append(el('li', 'muted', 'No hosts added.'))
    for (const h of v.added) {
      const li = el('li', 'row')
      li.append(el('code', '', visible(h.host)), el('span', 'small muted', until(h.expires, v.now)), el('span', 'small', typeof h.reason === 'string' && h.reason ? `“${visible(h.reason)}”` : 'no reason given'))
      if (h.uploads) li.append(el('span', 'tag warn', 'accepts uploads'))
      li.append(el('span', 'spacer'), button('Remove', 'ghost small', () => void remove(h.host)))
      addedList.append(li)
    }
    gatewayURL.value = v.gateway?.url ?? ''
    gwAgents.checked = v.gateway ? v.gateway.agents : true
    gwReviewers.checked = v.gateway ? v.gateway.reviewers : true
    gwNote.textContent = v.gatewayKeyNote
    listsNote.textContent = `Agents reach: ${v.lists.agents.map(visible).join(', ') || 'nothing'}. Reviewers reach: ${v.lists.reviewers.map(visible).join(', ') || 'nothing'}.`
  }

  const answer = (a: Answer, fallback: string): boolean => {
    const v = viewOf(a.body)
    if (!a.ok || !v) {
      failed(a, fallback)
      void reload()
      return false
    }
    say.textContent = ''
    paint(v)
    return true
  }

  const reload = async () => {
    const a = await call(post, { op: 'read' })
    const v = viewOf(a.body)
    if (v) paint(v)
    else failed(a, 'The allowlist couldn’t be read.')
  }

  const saveAgents = async () => {
    const ids = [...agentsBox.querySelectorAll<HTMLInputElement>('input[type=checkbox]')].filter(b => b.checked).map(b => b.value)
    answer(await call(post, { op: 'agents', agents: ids }), 'The agents weren’t saved.')
  }

  const remove = async (host: string) => {
    answer(await call(post, { op: 'remove', host }), 'The host wasn’t removed.')
  }

  const add = async (uploadsOk: boolean) => {
    const host = hostInput.value.trim()
    if (!host) return hostInput.focus()
    const reason = reasonInput.value.trim()
    const a = await call(post, { op: 'add', host, expires: expiry.value, ...(uploadsOk ? { uploadsOk: true } : {}), ...(reason ? { reason } : {}) })
    if (!a.ok && a.body.error === 'uploads') {
      uploadWarn.replaceChildren(el('p', '', visible(text(a.body.message))), button('Add anyway', 'ghost small', () => void add(true)), button('Cancel', 'ghost small', () => { uploadWarn.hidden = true }))
      uploadWarn.hidden = false
      return
    }
    uploadWarn.hidden = true
    if (answer(a, 'The host wasn’t added.')) {
      hostInput.value = ''
      reasonInput.value = ''
      hint.textContent = ''
      void loadRefusals()
    }
  }

  const prefill = (r: Refusal) => {
    hostInput.value = r.suggest[0] ?? r.host
    hint.textContent = r.suggest.length ? view?.pypiNote ?? '' : r.uploads ? `${visible(r.host)} also accepts uploads.` : ''
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
        li.append(el('code', '', visible(r.host)), el('span', 'small muted', `${REASONS[r.reason] ?? visible(r.reason)} · ${r.count}× · last ${when(r.last)}`))
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
    gwTell([], false)
    const a = await call(post, { op: 'gateway', url: clear ? '' : gatewayURL.value.trim(), agents: gwAgents.checked, reviewers: gwReviewers.checked })
    if (!answer(a, 'The gateway wasn’t saved.') || !key || !gwSides().length) return
    gwTell(['Saving the key…'], false)
    await keySteps(target => ({ step: 'key', target, key }), 'Key saved', 'The key wasn’t saved. Try again.')
  }

  const removeKey = async () => {
    if (!gwSides().length) return gwTell(['No gateway is set.'], true)
    await keySteps(target => ({ step: 'clear-key', target }), 'Key removed', 'The key wasn’t removed.')
  }

  hostInput.addEventListener('input', () => {
    const h = hostInput.value.trim().toLowerCase()
    hint.textContent = /(^|\.)(pypi\.org|pythonhosted\.org)$/.test(h) ? view?.pypiNote ?? '' : ''
  })
  const addForm = el('form', 'row egress-add')
  const hostLabel = el('label', 'small', 'Host')
  hostLabel.append(hostInput)
  const expiryLabel = el('label', 'small', 'Allowed')
  expiryLabel.append(expiry)
  const reasonLabel = el('label', 'small', 'Reason')
  reasonLabel.append(reasonInput)
  addForm.append(hostLabel, expiryLabel, reasonLabel, button('Add', 'primary small', undefined, 'submit'))
  addForm.addEventListener('submit', e => {
    e.preventDefault()
    void add(false)
  })
  const gwForm = el('form', 'egress-gateway')
  const urlLabel = el('label', 'small', 'Base URL')
  urlLabel.append(gatewayURL)
  const aLabel = el('label', 'small row')
  aLabel.append(gwAgents, el('span', '', 'Claude Code sessions'))
  const rLabel = el('label', 'small row')
  rLabel.append(gwReviewers, el('span', '', 'Reviewers'))
  const keyLabel = el('label', 'small', 'Key or token (leave empty to keep the saved one)')
  keyLabel.append(gwKey)
  const gwActions = el('div', 'row')
  gwActions.append(button('Save gateway', 'ghost small', undefined, 'submit'), button('Remove key', 'ghost small', () => void removeKey()), button('Remove gateway', 'ghost small', () => void saveGateway(true)))
  gwForm.append(urlLabel, aLabel, rLabel, keyLabel, gwActions, gwSay)
  gwForm.addEventListener('submit', e => {
    e.preventDefault()
    void saveGateway(false)
  })
  const refreshRow = el('div', 'row')
  refreshRow.append(el('h3', 'label', 'Refused requests'), el('span', 'spacer'), button('Refresh', 'ghost small', () => void loadRefusals()))
  const actions = el('div', 'row consent-actions')
  actions.append(el('span', 'spacer'), button('Close', 'ghost small', close))
  d.append(
    el('h2', 'label', 'Network access'),
    intro, startNote, pending, say,
    agentsBox,
    el('h3', 'label', 'Hosts you added'),
    el('p', 'small muted', 'For agents only; reviewers reach only Claude Code’s hosts and the gateway. A host you add gives every session a way out, so add only what a task needs, and give it an expiry when you can.'),
    addedList, addForm, hint, uploadWarn,
    refreshRow,
    el('p', 'small muted', 'What egress refused since it started: host, count and last time, never the path. Every session shares one container, so egress can’t tell sessions apart.'),
    refusedBox,
    el('h3', 'label', 'Company LLM gateway'),
    el('p', 'small', 'Sets Claude Code’s base URL (ANTHROPIC_BASE_URL) for the sessions and reviewers you choose, and adds the gateway’s host to their allowlist. While the gateway is on for a side, that side’s list leaves out Anthropic’s own hosts and Claude Code’s telemetry hosts (anthropic.com, claude.ai, claude.com, Statsig, Sentry, Datadog), unless the gateway is one of them, so a key pasted for the gateway can’t reach them; removing the gateway brings them back.'),
    el('p', 'small warn', `${GATEWAY_WARNING}.`),
    gwNote, gwForm,
    listsNote,
    actions,
  )
  d.addEventListener('cancel', e => {
    e.preventDefault()
    close()
  })
  document.body.append(d)
  d.showModal()
  void reload()
  void loadRefusals()
}
