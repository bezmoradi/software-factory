import { UNREAD, savedTone, type EngineAnswer } from '../api.ts'
import { actionButton, button, el, labelError, say, statusLine, visible, whileBusy } from '../lib/dom.ts'
import { modal } from '../lib/dialog.ts'
import { actions, field, fieldRow, form, showAnswer } from '../lib/form.ts'
import { dropdown, option, textbox } from '../lib/controls.ts'
import { emptyState } from '../lib/page.ts'
import { CHEAP_EVERY_MS, whileOpen } from '../lib/refresh.ts'
import { GIT_HOST_NOTE, openSavePreset, presetLine, presetsOf } from './credentials.ts'
import { ACCESS_ROUTE, accessCardOf, accessCardView, firstChoice, type AccessChoice } from './access-card.ts'
import { sessionActionsPart } from './actions.ts'
import { when } from './usage.ts'
import type { AccessCard } from '../types.ts'

type Post = (path: string, body: unknown) => Promise<EngineAnswer>

interface GrantRow {
  id: string
  host: string
  target: string
  credential: string
  delivery: string
  deliveryTag: string
  copy: string
  scopeLabel: string
  own: boolean
  until: string
  who: string
  reason: string
}

const text = (v: unknown): string => (typeof v === 'string' ? v : '')
const record = (v: unknown): Record<string, unknown> => (v !== null && typeof v === 'object' && !Array.isArray(v) ? (v as Record<string, unknown>) : {})

function grantsOf(b: Record<string, unknown>): GrantRow[] | null {
  if (!Array.isArray(b.grants)) return null
  return b.grants.map(record).map(g => ({ id: text(g.id), host: text(g.host), target: text(g.target), credential: text(g.credential), delivery: text(g.delivery), deliveryTag: text(g.deliveryTag), copy: text(g.copy), scopeLabel: text(g.scopeLabel), own: g.own === true, until: typeof g.expires === 'number' && g.expires > 0 ? `until ${when(g.expires * 1000)}` : text(g.until), who: text(g.who), reason: text(g.reason) })).filter(g => g.id && (g.host || g.credential))
}

let headings = 0

export function openAccess(post: Post, sid: string, openNetwork: () => void, openCredentials: () => void): void {
  const said = statusLine('small')
  const network = el('p', 'small')
  const presets = el('ul', 'list access-presets')
  const presetsPart = el('div')
  presetsPart.append(el('h3', 'label', 'Presets'), el('p', 'small muted', 'Apply a saved grant in one click.'), presets)
  presetsPart.hidden = true
  const whatPick = dropdown({ name: 'credential' })
  const list = el('ul', 'list access-grants')
  const allRow = el('div', 'row text-row')
  const hostInput = textbox({ name: 'host', spellcheck: false, autocomplete: 'off', placeholder: 'api.example.com' })
  hostInput.setAttribute('aria-required', 'true')
  const portInput = textbox({ name: 'port', inputMode: 'numeric', autocomplete: 'off', placeholder: '22' })
  const reasonInput = textbox({ name: 'reason', autocomplete: 'off', placeholder: 'Why this session needs it' })
  const cardBox = el('div', 'access-check')
  const formSaid = statusLine('small bad')
  let checked: { card: AccessCard; choice: AccessChoice; host: string; port: number; credential: string; reason: string } | null = null
  const heading = `access-dialog-card-${++headings}`

  const tell = (a: EngineAnswer) => say(said, a.message, a.ok ? savedTone(a) : 'bad')
  const sessionActions = sessionActionsPart(post, ACCESS_ROUTE, sid, a => {
    tell(a)
    if (a.ok) paint(a)
  })

  const paint = (a: EngineAnswer) => {
    const grants = grantsOf(a.body)
    if (!a.ok || !grants) {
      say(said, a.ok ? UNREAD : a.message, 'warn')
      return
    }
    network.replaceChildren(visible(text(a.body.network)), ' ', button('Open Network access', 'link', () => {
      m.close()
      openNetwork()
    }), ' · ', button('Open Credentials', 'link', () => {
      m.close()
      openCredentials()
    }))
    const creds = Array.isArray(a.body.credentials) ? a.body.credentials.map(record).filter(c => text(c.id)) : []
    const want = [{ value: '', label: 'A host or a port' }, ...creds.map(c => ({ value: text(c.id), label: `${text(c.name)} (${text(c.typeLabel)})` }))]
    if (whatPick.options.length !== want.length || want.some((c, i) => whatPick.options[i]?.value !== c.value)) {
      const keep = whatPick.value
      whatPick.replaceChildren(...want.map(c => option(visible(c.label), c.value)))
      if (want.some(c => c.value === keep)) whatPick.value = keep
    }
    hostRow.hidden = whatPick.value !== ''
    presets.replaceChildren()
    const rows = presetsOf(a.body.presets)
    presetsPart.hidden = !rows.length
    for (const p of rows) {
      const li = el('li', 'row')
      li.append(presetLine(p), el('span', 'spacer'), actionButton('Apply', 'ghost small', async () => {
        const r = await post(ACCESS_ROUTE, { op: 'apply', sid, preset: p.id })
        tell(r)
        if (r.ok) paint(r)
      }, 'button', `apply:${p.id}`))
      presets.append(li)
    }
    list.replaceChildren()
    if (!grants.length) list.append(emptyState('Nothing granted yet.', { tag: 'li' }))
    for (const g of grants) {
      const li = el('li', 'row access-grant')
      const what = el('div', 'access-grant-text')
      const first = el('p', 'small')
      if (g.credential) first.append(el('strong', '', visible(g.credential)), ' ', el('span', g.delivery === 'signing' ? 'tag' : 'tag warn', visible(g.deliveryTag)), ' ')
      else first.append(el('code', '', visible(g.target || g.host)), ' ')
      first.append(el('span', 'tag', visible(g.scopeLabel)), ' ', el('span', 'muted', visible(g.until)))
      const second = el('p', 'small muted', g.reason ? `${visible(g.who)}: “${visible(g.reason)}”` : visible(g.who))
      what.append(first, second)
      if (g.copy) what.append(el('p', 'small warn', visible(g.copy)))
      li.append(what, el('span', 'spacer'), actionButton('Take back', 'ghost small', async () => {
        const r = await post(ACCESS_ROUTE, { op: 'revoke', sid, grant: g.id })
        tell(r)
        if (r.ok) paint(r)
      }, 'button', `revoke:${g.id}`))
      list.append(li)
    }
    allRow.hidden = !grants.some(g => g.own) && !(Array.isArray(a.body.actionGrants) && a.body.actionGrants.length)
    sessionActions.paint(a.body)
  }

  const showCard = () => {
    if (!checked) return cardBox.replaceChildren()
    const c = checked
    const what = c.credential ? { credential: c.credential } : c.port ? { host: c.host, port: c.port } : { host: c.host }
    cardBox.replaceChildren(accessCardView({
      card: c.card, choice: c.choice, disabled: false, heading,
      onChoice: next => {
        c.choice = next
        showCard()
      },
      accept: async choice => {
        const a = await post(ACCESS_ROUTE, { op: 'allow', sid, ...what, ...(c.reason ? { reason: c.reason } : {}), scope: choice.scope, expires: choice.expires, confirmed: choice.confirmed })
        if (!a.ok && a.error === 'git host') {
          await check()
          say(formSaid, a.message, 'bad')
        }
        return a
      },
      savePreset: choice => openSavePreset(post, { sid, ...what, scope: choice.scope, expires: choice.expires, confirmed: choice.confirmed }),
      done: a => {
        checked = null
        hostInput.value = ''
        portInput.value = ''
        reasonInput.value = ''
        showCard()
        tell(a)
        paint(a)
        hostInput.focus()
      },
    }))
  }

  const check = async () => {
    checked = null
    showCard()
    const credential = whatPick.value
    const host = credential ? '' : hostInput.value
    const typed = credential ? '' : portInput.value.trim()
    const port = typed ? (Number.isInteger(Number(typed)) ? Number(typed) : 0) : 0
    const reason = reasonInput.value
    const a = await post(ACCESS_ROUTE, { op: 'check', sid, ...(credential ? { credential } : typed ? { host, port } : { host }), ...(reason ? { reason } : {}) })
    const card = a.ok ? accessCardOf(a.body.card) : null
    if (!showAnswer(giveForm, a, formSaid) || !card) {
      if (a.ok && !card) formSaid.textContent = UNREAD
      return
    }
    checked = { card, choice: firstChoice(card), host, port: typed ? card.port ?? port : 0, credential, reason }
    showCard()
    cardBox.querySelector<HTMLElement>('select')?.focus()
  }

  for (const input of [hostInput, portInput]) input.addEventListener('input', () => {
    labelError(input, '')
    checked = null
    showCard()
  })
  reasonInput.addEventListener('input', () => labelError(reasonInput, ''))
  whatPick.addEventListener('change', () => {
    labelError(whatPick, '')
    hostRow.hidden = whatPick.value !== ''
    checked = null
    showCard()
  })
  const hostRow = fieldRow([field('Host or address', hostInput), field('Port', portInput, { optional: true })], 'host-port')
  const checkButton = button('Check the risk', 'primary small', undefined, 'submit')
  const giveForm = form({
    cls: 'access-give',
    children: [field('What', whatPick), hostRow, field('Reason', reasonInput, { optional: true }), actions([checkButton]), formSaid],
    onSubmit: () => void whileBusy(checkButton, check),
  })
  allRow.append(el('p', 'small', "This session's own grants end when it stops or is deleted."), el('span', 'spacer'), actionButton('Take back all', 'ghost small', async () => {
    const r = await post(ACCESS_ROUTE, { op: 'revoke-all', sid })
    tell(r)
    if (r.ok) paint(r)
  }))
  allRow.hidden = true
  const m = modal({
    kind: 'access',
    title: 'Access',
    docs: 'access',
    single: `access:${sid}`,
    focus: 'heading',
    body: [
      network, said,
      list, allRow,
      presetsPart,
      el('h3', 'label', 'Give access'),
      el('p', 'small muted', GIT_HOST_NOTE),
      giveForm, cardBox,
      ...sessionActions.nodes,
    ],
    actions: [button('Close', 'ghost small', () => m.close())],
  })
  const refresh = whileOpen({ root: list, read: () => post(ACCESS_ROUTE, { op: 'read', sid }), show: paint, everyMs: CHEAP_EVERY_MS })
  m.onClose(() => refresh.stop())
  void refresh.now()
}
