import { UNREAD, savedTone, type EngineAnswer } from '../api.ts'
import { button, el, labelError, say, statusLine, visible, whileBusy } from '../lib/dom.ts'
import { modal } from '../lib/dialog.ts'
import { dialogDocs } from '../lib/dialog-docs.ts'
import { actions, field, fieldRow, form, reveal, showAnswer } from '../lib/form.ts'
import { checkbox, dropdown, option, textarea, textbox } from '../lib/controls.ts'
import { emptyState } from '../lib/page.ts'
import { CHEAP_EVERY_MS, whileOpen } from '../lib/refresh.ts'
import { savedActionsPart } from './actions.ts'
import { askRow, focusIfLost, focusKey, itemTable, rowMenu, rowName, rowNote, tableRow } from './row-table.ts'
import { secret } from './secret.ts'
import { toast } from './toasts.ts'
import { when } from './usage.ts'

type Post = (path: string, body: unknown) => Promise<EngineAnswer>

export const CREDENTIALS_ROUTE = '/api/credentials'

interface Choice {
  value: string
  label: string
}

interface CredentialGrant {
  id: string
  sid: string
  project: string
  scopeLabel: string
  until: string
  expires: number
  who: string
  lead: string
  copy: string
}

interface CredentialRow {
  id: string
  name: string
  type: string
  typeLabel: string
  bound: string[]
  tags: string[]
  saved: number
  grants: CredentialGrant[]
}

export interface PresetRow {
  id: string
  name: string
  kind: string
  what: string
  scopeLabel: string
  expiresLabel: string
  project: string
  auto: boolean
  level: string
  label: string
  off: string
}

interface CredentialsView {
  credentials: CredentialRow[]
  presets: PresetRow[]
  projects: string[]
  note: string
  types: Choice[]
  tags: Choice[]
  modes: Choice[]
  scopes: Choice[]
  expiries: Choice[]
  raw: Record<string, unknown>
}

const text = (v: unknown): string => (typeof v === 'string' ? v : '')
const strings = (v: unknown): string[] => (Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : [])
const record = (v: unknown): Record<string, unknown> => (v !== null && typeof v === 'object' && !Array.isArray(v) ? (v as Record<string, unknown>) : {})
const choices = (v: unknown): Choice[] => (Array.isArray(v) ? v.map(record).map(c => ({ value: text(c.value), label: text(c.label) })).filter(c => c.value) : [])

export function presetsOf(v: unknown): PresetRow[] {
  return Array.isArray(v)
    ? v.map(record).map(p => ({
        id: text(p.id), name: text(p.name), kind: text(p.kind), what: text(p.what), scopeLabel: text(p.scopeLabel), expiresLabel: text(p.expiresLabel),
        project: text(p.project), auto: p.auto === true, level: text(p.level), label: text(p.label), off: text(p.off),
      })).filter(p => p.id)
    : []
}

function viewOf(b: Record<string, unknown>): CredentialsView | null {
  if (!Array.isArray(b.credentials)) return null
  return {
    credentials: b.credentials.map(record).map(c => ({
      id: text(c.id), name: text(c.name), type: text(c.type), typeLabel: text(c.typeLabel), bound: strings(c.bound), tags: strings(c.tags), saved: Number(c.saved) || 0,
      grants: Array.isArray(c.grants) ? c.grants.map(record).map(g => ({ id: text(g.id), sid: text(g.sid), project: text(g.project), scopeLabel: text(g.scopeLabel), until: text(g.until), expires: Number(g.expires) || 0, who: text(g.who), lead: text(g.lead), copy: text(g.copy) })) : [],
    })).filter(c => c.id),
    presets: presetsOf(b.presets), projects: strings(b.projects), note: text(b.note),
    types: choices(b.types), tags: choices(b.tags), modes: choices(b.modes), scopes: choices(b.scopes), expiries: choices(b.expiries), raw: b,
  }
}

function fill(pick: HTMLSelectElement, list: Choice[]) {
  if (pick.options.length === list.length && list.every((c, i) => pick.options[i]?.value === c.value)) return
  const keep = pick.value
  pick.replaceChildren(...list.map(c => option(visible(c.label), c.value)))
  if (list.some(c => c.value === keep)) pick.value = keep
}

export function presetLine(p: PresetRow): HTMLElement {
  const first = el('p', 'small')
  first.append(el('strong', '', visible(p.name)), ' ', el('code', '', visible(p.what)), ' ', el('span', 'tag', visible(p.scopeLabel)), ' ', el('span', 'muted', visible(p.expiresLabel)))
  if (p.auto) first.append(' ', el('span', 'tag', 'applies at each start'))
  const tag = el('span', 'tag access-level', visible(p.label))
  tag.dataset.level = p.level
  first.append(' ', tag)
  return first
}

const ADD_CREDENTIAL = 'Add a credential'
export const GIT_HOST_NOTE = 'Software Factory pushes, opens issues and pull requests and reads CI for you, so it refuses every grant and credential that would open a git host. A git host’s token goes in Projects, under Git host tokens.'
const RETYPE = 'Type it again: the page never shows it.'

export function openCredentials(post: Post, labelOfSid: (sid: string) => string): void {
  const said = statusLine('small')
  const note = el('p', 'small')
  const list = el('div', 'credential-list')
  const presetList = el('div', 'credential-presets')
  let view: CredentialsView | null = null
  let editing = ''
  let asking = ''

  const nameInput = textbox({ name: 'name', spellcheck: false, autocomplete: 'off', placeholder: 'npm-ci' })
  const typePick = dropdown({ name: 'type' })
  const boundInput = textbox({ name: 'bound', spellcheck: false, autocomplete: 'off', placeholder: 'registry.npmjs.org, 192.168.1.20:22' })
  const tagWrite = checkbox({ name: 'tags', value: 'write' })
  const tagProd = checkbox({ name: 'tags', value: 'production' })
  const variable = textbox({ name: 'variable', spellcheck: false, autocomplete: 'off', placeholder: 'NPM_TOKEN' })
  const value = textbox({ type: 'password', name: 'value', spellcheck: false, autocomplete: 'off' })
  const keyId = textbox({ name: 'keyId', spellcheck: false, autocomplete: 'off', placeholder: 'AKIA…' })
  const secretKey = textbox({ type: 'password', name: 'secretKey', spellcheck: false, autocomplete: 'off' })
  const role = textbox({ name: 'role', spellcheck: false, autocomplete: 'off', placeholder: 'arn:aws:iam::123456789012:role/name' })
  const region = textbox({ name: 'region', spellcheck: false, autocomplete: 'off', placeholder: 'eu-west-1' })
  const user = textbox({ name: 'user', spellcheck: false, autocomplete: 'off', placeholder: 'pi' })
  const privateKey = textarea({ name: 'privateKey', rows: 4, spellcheck: false, placeholder: '-----BEGIN OPENSSH PRIVATE KEY-----' })
  const password = textbox({ type: 'password', name: 'password', spellcheck: false, autocomplete: 'off' })
  const hostKey = textbox({ name: 'hostKey', spellcheck: false, autocomplete: 'off', placeholder: 'ssh-ed25519 AAAA…' })
  const path = textbox({ name: 'path', spellcheck: false, autocomplete: 'off', placeholder: '.kube/config' })
  const mode = dropdown({ name: 'mode' })
  const contents = textarea({ name: 'contents', rows: 5, spellcheck: false })
  const vars = textarea({ name: 'vars', rows: 3, spellcheck: false, placeholder: 'NAME=value, one a line' })
  const secretFields = [variable, value, keyId, secretKey, role, region, user, privateKey, password, hostKey, path, contents, vars]

  const groups: Record<string, HTMLElement> = {
    token: fieldRow([field('Variable', variable), field('Value', secret(value, 'value'), { help: RETYPE })], 'halves'),
    aws: el('div'),
    ssh: el('div'),
    file: el('div'),
    vars: field('Variables', vars, { help: RETYPE }),
  }
  groups.aws!.append(fieldRow([field('Access key id', keyId), field('Secret access key', secret(secretKey, 'secret'), { help: RETYPE })], 'halves'), fieldRow([field('Region', region), field('Role ARN', role, { optional: true })], 'halves'))
  groups.ssh!.append(fieldRow([field('User', user), field('Host key', hostKey, { optional: true })], 'halves'), field('Private key', privateKey, { optional: true, help: RETYPE }), field('Or a password', secret(password, 'password'), { optional: true, help: RETYPE }))
  groups.file!.append(fieldRow([field('Path under the agent’s home', path), field('Mode', mode)], 'halves'), field('Contents', contents, { help: RETYPE }))
  const typed = el('div', 'credential-typed')
  const showType = () => {
    for (const [k, g] of Object.entries(groups)) g.hidden = k !== typePick.value
  }
  typed.append(...Object.values(groups))
  const retype = [value, secretKey, privateKey, password, contents, vars].map(f => ({ f, hint: typed.querySelector<HTMLElement>(`#${CSS.escape(f.id)}-help`)! }))
  const replacing = (on: boolean) => {
    for (const { f, hint } of retype) {
      hint.hidden = !on
      const described = (f.getAttribute('aria-describedby') ?? '').split(' ').filter(d => d && d !== hint.id)
      if (on) described.push(hint.id)
      if (described.length) f.setAttribute('aria-describedby', described.join(' '))
      else f.removeAttribute('aria-describedby')
    }
  }

  const formSaid = statusLine('small bad')
  const saveButton = button('Save', 'primary small', undefined, 'submit')
  const cancelEdit = button('Cancel', 'small', () => {
    resetForm()
    credReveal.close()
  })
  const formHead = el('h4', '', ADD_CREDENTIAL)

  const resetForm = () => {
    editing = ''
    formHead.textContent = ADD_CREDENTIAL
    replacing(false)
    formSaid.textContent = ''
    nameInput.value = ''
    boundInput.value = ''
    tagWrite.checked = tagProd.checked = false
    for (const f of secretFields) f.value = ''
    for (const marked of credForm.querySelectorAll<HTMLElement>('[aria-invalid="true"]')) labelError(marked, '')
  }

  const tell = (a: EngineAnswer) => say(said, a.message, a.ok ? savedTone(a) : 'bad')
  const savedActions = savedActionsPart(post, CREDENTIALS_ROUTE, (a, place, line) => answer(a, place, line), a => {
    tell(a)
    const next = a.ok ? viewOf(a.body) : null
    if (next) paint(next)
  })

  const paint = (v: CredentialsView) => {
    view = v
    note.textContent = visible(v.note)
    fill(typePick, v.types)
    fill(mode, v.modes)
    fill(presetWhat, [...v.credentials.map(c => ({ value: c.id, label: `${c.name} (${c.typeLabel})` })), { value: '', label: 'A host or a port' }])
    fill(presetScope, v.scopes)
    fill(presetExpires, v.expiries)
    fill(presetProject, [{ value: '', label: 'Every project' }, ...v.projects.map(p => ({ value: p, label: p }))])
    presetHostRow.hidden = presetWhat.value !== ''
    savedActions.paint(v.raw, v.credentials.map(c => ({ value: c.id, label: `${c.name} (${c.typeLabel})` })))
    showType()
    const ask = (key: string, to: string) => {
      asking = key
      if (view) paint(view)
      focusKey(m.dialog, to)
    }
    const deleted = async (op: string, id: string, back: string) => {
      const a = await post(CREDENTIALS_ROUTE, { op, id })
      asking = ''
      tell(a)
      const next = a.ok ? viewOf(a.body) : null
      if (next) paint(next)
      focusIfLost(m.dialog, back)
    }
    list.replaceChildren(v.credentials.length ? itemTable('credential-table', 'Credentials', ['Credential', 'Bound to', 'Saved'], v.credentials.map(c => {
      const name = rowName(visible(c.name), el('span', 'tag', visible(c.typeLabel)), ...c.tags.map(t => el('span', 'tag warn', visible(t))))
      if (asking === `cred:${c.id}`) return askRow(name, `Delete “${visible(c.name)}”?`, `cred:${c.id}`, () => ask('', `more:${c.id}`), () => deleted('delete', c.id, 'add-credential'))
      const hosts = el('span', 'secondary item-hosts')
      hosts.append(...c.bound.flatMap((h, i) => [i ? ', ' : '', el('code', '', visible(h))]))
      const notes = c.grants.flatMap(g => {
        const scope = g.scopeLabel === 'This project' ? `every session of ${visible(g.project)}` : visible(labelOfSid(g.sid))
        const lines = [rowNote(`${visible(g.lead)} ${scope}, ${g.expires ? `until ${when(g.expires * 1000)}` : visible(g.until)}. ${visible(g.who)}.`, '')]
        if (g.copy) lines.push(rowNote(visible(g.copy), 'warn'))
        return lines
      })
      return tableRow([name, hosts, el('span', 'secondary when', when(c.saved * 1000)), ...notes], rowMenu(visible(c.name), `more:${c.id}`, [
        { text: 'Replace', glyph: 'edit', run: from => {
          for (const marked of credForm.querySelectorAll<HTMLElement>('[aria-invalid="true"]')) labelError(marked, '')
          editing = c.id
          formHead.textContent = `Replace ${visible(c.name)}`
          replacing(true)
          nameInput.value = c.name
          typePick.value = c.type
          boundInput.value = c.bound.join(', ')
          tagWrite.checked = c.tags.includes('write')
          tagProd.checked = c.tags.includes('production')
          for (const f of secretFields) f.value = ''
          showType()
          credReveal.open(from)
        } },
        { text: 'Delete', glyph: 'trash', run: () => ask(`cred:${c.id}`, `yes:cred:${c.id}`) },
      ]))
    })) : emptyState('No credentials saved.'))
    presetList.replaceChildren(v.presets.length ? itemTable('preset-table', 'Presets', ['Preset', 'Grants', 'Project'], v.presets.map(p => {
      const level = el('span', 'tag access-level', visible(p.label))
      level.dataset.level = p.level
      const name = rowName(visible(p.name), level, ...(p.auto ? [el('span', 'tag', 'applies at each start')] : []))
      if (asking === `preset:${p.id}`) return askRow(name, `Delete “${visible(p.name)}”?`, `preset:${p.id}`, () => ask('', `preset-more:${p.id}`), () => deleted('preset-delete', p.id, 'add-preset'))
      const grants = el('span', 'secondary item-hosts')
      grants.append(el('code', '', visible(p.what)), ` ${visible(p.scopeLabel)}, ${visible(p.expiresLabel)}`)
      return tableRow([name, grants, el('span', 'secondary', p.project ? visible(p.project) : 'Every project')], rowMenu(visible(p.name), `preset-more:${p.id}`, [
        { text: 'Delete', glyph: 'trash', run: () => ask(`preset:${p.id}`, `yes:preset:${p.id}`) },
      ]))
    })) : emptyState('No presets saved.'))
  }

  const answer = (a: EngineAnswer, place: HTMLFormElement, line: HTMLElement): boolean => {
    if (!showAnswer(place, a, line)) return false
    const v = viewOf(a.body)
    if (!v) {
      say(said, UNREAD, 'warn')
      return false
    }
    tell(a)
    paint(v)
    return true
  }

  const save = async () => {
    const tags = [tagWrite, tagProd].filter(t => t.checked).map(t => t.value)
    const body: Record<string, unknown> = { name: nameInput.value, type: typePick.value, bound: boundInput.value, tags }
    if (editing) body.id = editing
    const kinds: Record<string, (HTMLInputElement | HTMLTextAreaElement)[]> = {
      token: [variable, value], aws: [keyId, secretKey, role, region], ssh: [user, password, hostKey], file: [path, contents], vars: [vars],
    }
    for (const f of kinds[typePick.value] ?? []) if (f.value) body[f.name] = f.value
    if (typePick.value === 'ssh' && privateKey.value) body.privateKey = privateKey.value
    if (typePick.value === 'file') body.mode = mode.value
    const a = await post(CREDENTIALS_ROUTE, { op: 'save', credential: body })
    if (!answer(a, credForm, formSaid)) return
    resetForm()
    credReveal.close()
  }

  for (const f of [nameInput, boundInput, ...secretFields]) f.addEventListener('input', () => labelError(f, ''))
  typePick.addEventListener('change', showType)
  nameInput.setAttribute('aria-required', 'true')
  boundInput.setAttribute('aria-required', 'true')
  const tagRow = el('div', 'row')
  tagRow.append(field('write: it can change things', tagWrite, { kind: 'check' }), field('production', tagProd, { kind: 'check' }))
  replacing(false)
  const credForm = form({
    cls: 'credential-form',
    children: [fieldRow([field('Name', nameInput), field('Type', typePick)], 'halves'), field('Hosts it may be sent to', boundInput, { help: GIT_HOST_NOTE }), tagRow, typed, actions([cancelEdit, saveButton]), formSaid],
    onSubmit: () => void whileBusy(saveButton, save),
  })

  const presetName = textbox({ name: 'name', autocomplete: 'off', placeholder: 'npm registry for this project, 1 hour' })
  const presetWhat = dropdown({ name: 'credential' })
  const presetHost = textbox({ name: 'host', spellcheck: false, autocomplete: 'off', placeholder: 'api.example.com' })
  const presetPort = textbox({ name: 'port', inputMode: 'numeric', autocomplete: 'off', placeholder: '22' })
  const presetHostRow = fieldRow([field('Host or address', presetHost), field('Port', presetPort, { optional: true })], 'host-port')
  const presetScope = dropdown({ name: 'scope' })
  const presetExpires = dropdown({ name: 'expires' })
  const presetProject = dropdown({ name: 'project' })
  const presetAuto = checkbox({ name: 'auto' })
  const presetConfirm = checkbox({ name: 'confirmed' })
  const presetRisk = el('div', 'notice small access-risk')
  const presetAccept = field('', presetConfirm, { kind: 'check' })
  presetRisk.hidden = true
  presetAccept.hidden = true
  const presetSaid = statusLine('small bad')
  const presetButton = button('Save preset', 'primary small', undefined, 'submit')
  const presetCancel = button('Cancel', 'small', () => {
    presetName.value = ''
    presetConfirm.checked = false
    for (const marked of presetForm.querySelectorAll<HTMLElement>('[aria-invalid="true"]')) labelError(marked, '')
    presetSaid.textContent = ''
    presetReveal.close()
  })
  const typedPort = () => {
    const v = presetPort.value.trim()
    return v ? (Number.isInteger(Number(v)) ? Number(v) : 0) : undefined
  }
  const draft = () => ({
    name: presetName.value, ...(presetWhat.value ? { credential: presetWhat.value } : typedPort() === undefined ? { host: presetHost.value } : { host: presetHost.value, port: typedPort() }), scope: presetScope.value, expires: presetExpires.value,
    project: presetProject.value, auto: presetAuto.checked,
  })
  let checking = 0
  const checkRisk = async () => {
    const at = ++checking
    presetConfirm.checked = false
    const a = await post(CREDENTIALS_ROUTE, { op: 'preset-check', preset: draft() })
    if (at !== checking) return
    const r = record(a.body.risk)
    presetRisk.hidden = !a.ok
    presetAccept.hidden = !a.ok || !text(r.accept)
    if (!a.ok) return
    presetRisk.dataset.level = text(r.level)
    const says = el('ul', 'access-says')
    for (const line of strings(r.says)) says.append(el('li', '', visible(line)))
    const label = el('p')
    label.append(el('strong', '', visible(text(r.label))))
    presetRisk.replaceChildren(label, says)
    presetAccept.querySelector('label')!.replaceChildren(presetConfirm, visible(text(r.accept)))
  }
  for (const pick of [presetWhat, presetScope, presetExpires, presetProject]) pick.addEventListener('change', () => {
    presetHostRow.hidden = presetWhat.value !== ''
    labelError(pick, '')
    void checkRisk()
  })
  for (const input of [presetHost, presetPort]) {
    input.addEventListener('change', () => void checkRisk())
    input.addEventListener('input', () => labelError(input, ''))
  }
  presetName.addEventListener('input', () => labelError(presetName, ''))
  const presetForm = form({
    cls: 'preset-form',
    children: [
      fieldRow([field('Name', presetName), field('What', presetWhat)], 'halves'), presetHostRow,
      fieldRow([field('Who can reach it', presetScope), field('For how long', presetExpires)], 'halves'),
      fieldRow([field('Project', presetProject), field('Apply when a session of the project starts', presetAuto, { kind: 'check' })], 'halves'),
      presetRisk, presetAccept, actions([presetCancel, presetButton]), presetSaid,
    ],
    onSubmit: () => void whileBusy(presetButton, async () => {
      const a = await post(CREDENTIALS_ROUTE, { op: 'preset-save', preset: { ...draft(), confirmed: presetConfirm.checked } })
      if (!answer(a, presetForm, presetSaid)) return
      presetName.value = ''
      presetConfirm.checked = false
      presetReveal.close()
    }),
  })

  const credReveal = reveal(ADD_CREDENTIAL, [formHead, credForm], 'add-credential')
  const presetReveal = reveal('Add a preset', [el('h4', '', 'Add a preset'), presetForm], 'add-preset')
  const m = modal({
    kind: 'credentials',
    ...dialogDocs('credentials'),
    body: [
      note, said, list, credReveal.node,
      el('h3', 'label', 'Presets'),
      presetList, presetReveal.node,
      ...savedActions.nodes,
    ],
    actions: [button('Close', 'ghost small', () => m.close())],
  })
  const refresh = whileOpen({
    root: list,
    read: () => post(CREDENTIALS_ROUTE, { op: 'read' }),
    show: a => {
      const v = a.ok ? viewOf(a.body) : null
      if (v) paint(v)
      else say(said, a.ok ? UNREAD : a.message, 'warn')
    },
    everyMs: CHEAP_EVERY_MS,
  })
  m.onClose(() => refresh.stop())
  void refresh.now().then(() => {
    if (view) void checkRisk()
  })
}

export interface PresetFromCard {
  sid: string
  host?: string
  port?: number
  credential?: string
  scope: string
  expires: string
  confirmed: boolean
}

export function openSavePreset(post: Post, from: PresetFromCard): void {
  const name = textbox({ name: 'name', autocomplete: 'off' })
  name.setAttribute('aria-required', 'true')
  const auto = checkbox({ name: 'auto' })
  const said = statusLine('small bad')
  const save = button('Save preset', 'primary small', undefined, 'submit')
  name.addEventListener('input', () => labelError(name, ''))
  const f = form({
    children: [field('Name', name), field('Apply when a session of this project starts', auto, { kind: 'check' }), actions([save]), said],
    onSubmit: () => void whileBusy(save, async () => {
      const a = await post(CREDENTIALS_ROUTE, { op: 'preset-save', preset: { name: name.value, ...from, auto: auto.checked } })
      if (!showAnswer(f, a, said)) return
      toast({ id: `preset:${from.sid}`, title: a.message, tone: savedTone(a) })
      m.close()
    }),
  })
  const m = modal({
    kind: 'preset',
    title: 'Save as preset',
    docs: 'presets',
    body: [el('p', 'small', `Keeps ${visible(from.credential ?? (from.host && from.port ? `${from.host}:${from.port}` : from.host) ?? '')} with this card’s choices, for this session’s project.`), f],
    actions: [button('Cancel', 'ghost small', () => m.close())],
  })
  name.focus()
}
