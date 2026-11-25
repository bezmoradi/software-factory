import type { EngineAnswer } from '../api.ts'
import { actionButton, button, el, labelError, statusLine, visible, visibleCommand, whileBusy } from '../lib/dom.ts'
import { actions, field, fieldRow, form, reveal, showAnswer } from '../lib/form.ts'
import { checkbox, dropdown, option, textarea, textbox } from '../lib/controls.ts'
import { emptyState } from '../lib/page.ts'
import { codeBlock } from './code-block.ts'
import { askRow, focusIfLost, focusKey, itemTable, rowMenu, rowName, rowNote, tableRow } from './row-table.ts'
import { when } from './usage.ts'

type Post = (path: string, body: unknown) => Promise<EngineAnswer>

export interface ActionSlot {
  name: string
  pattern: string
  max: number
}

export interface ActionRow {
  id: string
  name: string
  command: string
  argv: string[]
  credential: string
  target: string
  slots: ActionSlot[]
  files: boolean
  minutes: number
  level: string
  label: string
  broken: string
}

interface RunRow {
  id: string
  name: string
  command: string
  target: string
  credential: string
  running: boolean
  state: string
  code: number
  output: string
  cut: boolean
  said: string
  started: number
  allowed: string
}

interface ActionGrantRow {
  id: string
  name: string
  command: string
  expires: number
}

const text = (v: unknown): string => (typeof v === 'string' ? v : '')
const record = (v: unknown): Record<string, unknown> => (v !== null && typeof v === 'object' && !Array.isArray(v) ? (v as Record<string, unknown>) : {})
const list = (v: unknown): Record<string, unknown>[] => (Array.isArray(v) ? v.map(record) : [])

export function actionsOf(v: unknown): ActionRow[] {
  return list(v).map(a => ({
    id: text(a.id), name: text(a.name), command: text(a.command), argv: Array.isArray(a.argv) ? a.argv.filter((x): x is string => typeof x === 'string') : [],
    credential: text(a.credential), target: text(a.target), files: a.files === true, minutes: Number(a.minutes) || 0, level: text(a.level), label: text(a.label), broken: text(a.broken),
    slots: list(a.slots).map(s => ({ name: text(s.name), pattern: text(s.pattern), max: Number(s.max) || 0 })),
  })).filter(a => a.id)
}

function runsOf(v: unknown): RunRow[] {
  return list(v).map(r => ({
    id: text(r.id), name: text(r.name), command: text(r.command), target: text(r.target), credential: text(r.credential), running: r.running === true, state: text(r.state),
    code: Number(r.code) || 0, output: text(r.output), cut: r.cut === true, said: text(r.said), started: Number(r.started) || 0, allowed: text(r.allowed),
  })).filter(r => r.id)
}

function grantsOf(v: unknown): ActionGrantRow[] {
  return list(v).map(g => ({ id: text(g.id), name: text(g.name), command: text(g.command), expires: Number(g.expires) || 0 })).filter(g => g.id)
}

const stateTone: Record<string, string> = { passed: 'tag ok', failed: 'tag bad', error: 'tag bad', 'timed-out': 'tag bad', stopped: 'tag warn' }

function actionHead(a: ActionRow): HTMLElement {
  const first = el('p', 'small')
  const level = el('span', 'tag access-level', visible(a.label))
  level.dataset.level = a.level
  first.append(el('strong', '', visible(a.name)), ' ', level, ' ', el('span', 'muted', `against ${visible(a.target)} with ${visible(a.credential || '?')}${a.files ? ', with the session’s files' : ''}, ${a.minutes} min`))
  return first
}

function commandBlock(name: string, command: string): HTMLElement {
  return codeBlock({ text: visibleCommand(command), label: `${visible(name)} command line`, look: 'terminal', wrap: true, size: 'small' })
}

const ADD_ACTION = 'Add an action'

export interface SavedActionsPart {
  nodes: Node[]
  paint: (body: Record<string, unknown>, credentials: { value: string; label: string }[]) => void
}

export function savedActionsPart(post: Post, route: string, shown: (a: EngineAnswer, place: HTMLFormElement, line: HTMLElement) => boolean, said: (a: EngineAnswer) => void): SavedActionsPart {
  const heading = el('h3', 'label', 'Actions')
  const rows = el('div', 'saved-actions')
  let editing = ''
  const name = textbox({ name: 'name', spellcheck: false, autocomplete: 'off', placeholder: 'deploy-pi' })
  const credential = dropdown({ name: 'credential' })
  const target = textbox({ name: 'target', spellcheck: false, autocomplete: 'off', placeholder: '192.168.1.20:22' })
  const command = textarea({ name: 'command', rows: 5, spellcheck: false, placeholder: 'rsync\n-a\n--delete\ndist/\npi@192.168.1.20:/srv/site/' })
  const slots = textarea({ name: 'slots', rows: 2, spellcheck: false, placeholder: 'tag 40 v[0-9]+\\.[0-9]+\\.[0-9]+' })
  const files = checkbox({ name: 'files' })
  const minutes = textbox({ name: 'minutes', inputMode: 'numeric', autocomplete: 'off', placeholder: '10' })
  const head = el('h4', '', ADD_ACTION)
  const line = statusLine('small bad')
  const save = button('Save action', 'primary small', undefined, 'submit')
  const cancel = button('Cancel', 'small', () => {
    reset()
    adding.close()
  })
  for (const c of [name, credential, target, command, slots, files, minutes]) {
    c.addEventListener('input', () => labelError(c, ''))
    c.addEventListener('change', () => labelError(c, ''))
  }
  name.setAttribute('aria-required', 'true')
  target.setAttribute('aria-required', 'true')
  command.setAttribute('aria-required', 'true')
  const reset = () => {
    editing = ''
    head.textContent = ADD_ACTION
    for (const c of [name, target, command, slots, minutes]) c.value = ''
    files.checked = false
    line.textContent = ''
    for (const marked of f.querySelectorAll<HTMLElement>('[aria-invalid="true"]')) labelError(marked, '')
  }
  const f = form({
    cls: 'action-form',
    children: [
      fieldRow([field('Name', name), field('Credential', credential)], 'halves'),
      fieldRow([field('Target', target), field('Time limit in minutes', minutes, { optional: true })], 'halves'),
      field('Command, one argument a line', command),
      field('Arguments the agent fills', slots, { optional: true }),
      field('Give it the session’s files, read-only', files, { kind: 'check' }),
      actions([cancel, save]), line,
    ],
    onSubmit: () => void whileBusy(save, async () => {
      const a = await post(route, { op: 'action-save', action: { ...(editing ? { id: editing } : {}), name: name.value, credential: credential.value, target: target.value, command: command.value, slots: slots.value, files: files.checked, ...(minutes.value.trim() ? { minutes: Number(minutes.value.trim()) } : {}) } })
      if (!shown(a, f, line)) return
      reset()
      adding.close()
    }),
  })
  const adding = reveal(ADD_ACTION, [head, f], 'add-action')
  const part = el('div', 'saved-actions-part')
  part.append(heading, rows, adding.node)
  let asking = ''
  let last: Parameters<SavedActionsPart['paint']> | null = null
  const ask = (key: string, to: string) => {
    asking = key
    if (last) draw(...last)
    focusKey(part, to)
  }
  const draw: SavedActionsPart['paint'] = (body, credentials) => {
    last = [body, credentials]
    part.hidden = body.actionsOn !== true
    const keep = credential.value
    credential.replaceChildren(...credentials.map(c => option(visible(c.label), c.value)))
    if (credentials.some(c => c.value === keep)) credential.value = keep
    const all = actionsOf(body.actions)
    rows.replaceChildren(all.length ? itemTable('action-table', 'Actions', ['Action', 'Command', 'Target'], all.map(a => {
      const level = el('span', 'tag access-level', visible(a.label))
      level.dataset.level = a.level
      const notes: HTMLElement[] = []
      if (a.files) notes.push(rowNote('With the session’s files, read-only.'))
      if (a.slots.length) notes.push(rowNote(`The agent fills ${a.slots.map(s => `{${visible(s.name)}}`).join(', ')}`))
      if (a.broken) notes.push(rowNote(visible(a.broken), 'warn'))
      const cmd = el('code', 'item-command', visibleCommand(a.command))
      const where = el('span', 'secondary item-hosts', `${visible(a.target)} with ${visible(a.credential || '?')}, ${a.minutes} min`)
      if (asking === a.id) return askRow(rowName(visible(a.name), level), `Delete “${visible(a.name)}”?`, `action:${a.id}`, () => ask('', `action-more:${a.id}`), async () => {
        asking = ''
        said(await post(route, { op: 'action-delete', id: a.id }))
        focusIfLost(part, 'add-action')
      })
      return tableRow([rowName(visible(a.name), level), cmd, where, ...notes], rowMenu(visible(a.name), `action-more:${a.id}`, [
        { text: 'Replace', glyph: 'edit', run: from => {
          for (const marked of f.querySelectorAll<HTMLElement>('[aria-invalid="true"]')) labelError(marked, '')
          editing = a.id
          head.textContent = `Replace ${visible(a.name)}`
          name.value = a.name
          target.value = a.target
          command.value = a.argv.join('\n')
          slots.value = a.slots.map(s => `${s.name} ${s.max} ${s.pattern}`).join('\n')
          files.checked = a.files
          minutes.value = String(a.minutes)
          const c = credentials.find(x => x.label.startsWith(`${a.credential} `))
          if (c) credential.value = c.value
          adding.open(from)
        } },
        { text: 'Delete', glyph: 'trash', run: () => ask(a.id, `yes:action:${a.id}`) },
      ]))
    })) : emptyState('No actions saved.'))
  }
  return { nodes: [part], paint: draw }
}

export interface SessionActionsPart {
  nodes: Node[]
  paint: (body: Record<string, unknown>) => void
}

export function sessionActionsPart(post: Post, route: string, sid: string, done: (a: EngineAnswer) => void): SessionActionsPart {
  const heading = el('h3', 'label', 'Actions')
  const intro = el('p', 'small muted', 'Run a saved action.')
  const saved = el('ul', 'list session-actions')
  const granted = el('ul', 'list action-grants')
  const runs = el('ul', 'list action-runs')
  const runsHead = el('h4', '', 'Runs')
  let savedKey = ''
  const part = el('div', 'session-actions-part')
  part.append(heading, intro, saved, granted, runsHead, runs)
  const runForm = (a: ActionRow): HTMLElement => {
    const inputs = a.slots.map(s => {
      const t = textbox({ name: s.name, spellcheck: false, autocomplete: 'off', placeholder: s.pattern })
      t.addEventListener('input', () => labelError(t, ''))
      return t
    })
    const line = statusLine('small bad')
    const run = button(`Run ${visible(a.name)}`, 'primary small', undefined, 'submit')
    const f = form({
      cls: 'action-run',
      children: [...inputs.map((t, i) => field(`{${visible(a.slots[i]!.name)}}`, t)), actions([run]), line],
      onSubmit: () => void whileBusy(run, async () => {
        const args: Record<string, string> = {}
        a.slots.forEach((s, i) => (args[s.name] = inputs[i]!.value))
        const r = await post(route, { op: 'action-run', sid, action: a.id, args })
        if (!showAnswer(f, r, line)) return
        for (const t of inputs) t.value = ''
        done(r)
      }),
    })
    return f
  }
  return {
    nodes: [part],
    paint: body => {
      const all = actionsOf(body.actions)
      const grants = grantsOf(body.actionGrants)
      const rows = runsOf(body.runs)
      part.hidden = !all.length && !grants.length && !rows.length
      intro.hidden = saved.hidden = !all.length
      const key = JSON.stringify(all)
      if (key !== savedKey) {
        savedKey = key
        saved.replaceChildren()
        for (const a of all) {
          const li = el('li', 'session-action')
          li.append(actionHead(a), commandBlock(a.name, a.command))
          if (a.broken) li.append(el('p', 'small warn', visible(a.broken)))
          else li.append(runForm(a))
          saved.append(li)
        }
      }
      granted.replaceChildren()
      for (const g of grants) {
        const li = el('li', 'row access-grant')
        const what = el('div', 'access-grant-text')
        const first = el('p', 'small')
        first.append(el('strong', '', visible(g.name)), ' ', el('span', 'tag warn', 'runs without a card'), ' ', el('span', 'muted', `until ${when(g.expires * 1000)}`))
        what.append(first, el('p', 'small muted', visible(g.command)))
        li.append(what, el('span', 'spacer'), actionButton('Take back', 'ghost small', async () => done(await post(route, { op: 'revoke', sid, grant: g.id })), 'button', `revoke:${g.id}`))
        granted.append(li)
      }
      runsHead.hidden = !rows.length
      runs.replaceChildren()
      for (const r of rows) {
        const li = el('li', 'action-run-row')
        const first = el('div', 'row text-row')
        const label = el('p', 'small')
        label.append(el('strong', '', visible(r.name)), ' ', el('span', stateTone[r.state] ?? 'tag', visible(r.state)), ' ', el('span', 'muted', `${when(r.started * 1000)}, allowed by ${visible(r.allowed)}`))
        first.append(label)
        if (r.running) first.append(el('span', 'spacer'), actionButton('Stop', 'ghost small', async () => done(await post(route, { op: 'action-stop', sid, run: r.id })), 'button', `action-stop:${r.id}`))
        li.append(first)
        if (r.said) li.append(el('p', 'small', visible(r.said)))
        if (r.output) li.append(codeBlock({ text: visibleCommand(r.output), label: `${visible(r.name)} output`, title: r.cut ? 'Output, the last 64 KiB' : 'Output', keep: 'end', wrap: true, size: 'small' }))
        runs.append(li)
      }
    },
  }
}
