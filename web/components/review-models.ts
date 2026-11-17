import { api } from '../api.ts'
import { el, focusedKey, refocus, say, statusLine, visible } from '../lib/dom.ts'

interface RoleModel {
  name: string
  model: string
  effort: string
}

interface ReviewModelsView {
  roles: Record<string, RoleModel>
  models: { id: string; name: string }[]
  efforts: string[]
}

export interface ReviewModelsTarget {
  project: string
  disabled: boolean
}

function readView(v: unknown): ReviewModelsView | null {
  if (!v || typeof v !== 'object') return null
  const b = v as Record<string, unknown>
  if (!b.roles || typeof b.roles !== 'object' || !Array.isArray(b.models) || !Array.isArray(b.efforts)) return null
  const roles: Record<string, RoleModel> = {}
  for (const [k, r] of Object.entries(b.roles as Record<string, unknown>)) {
    const x = r as Record<string, unknown> | null
    if (x && typeof x.name === 'string' && typeof x.model === 'string' && typeof x.effort === 'string') roles[k] = { name: x.name, model: x.model, effort: x.effort }
  }
  const models = (b.models as unknown[]).filter((m): m is { id: string; name: string } => !!m && typeof (m as { id?: unknown }).id === 'string' && typeof (m as { name?: unknown }).name === 'string')
  return { roles, models, efforts: (b.efforts as unknown[]).filter((e): e is string => typeof e === 'string') }
}

async function problemOf(res: Response | null): Promise<string> {
  if (!res) return 'The engine didn\'t answer. Try again.'
  const body = await res.json().catch(() => null) as { message?: unknown } | null
  return typeof body?.message === 'string' && body.message ? body.message : 'The engine refused the change. Try again.'
}

class SfReviewModels extends HTMLElement {
  #target: ReviewModelsTarget | null = null
  #view: ReviewModelsView | null = null
  #busy = false
  #status = statusLine()
  #key = ''

  set view(value: ReviewModelsTarget | null) {
    const key = JSON.stringify(value)
    if (key === this.#key) return
    this.#key = key
    const changed = value?.project !== this.#target?.project
    this.#target = value
    this.hidden = !value?.project
    if (!value?.project) {
      this.#view = null
      this.replaceChildren()
      return
    }
    if (changed) {
      this.#view = null
      this.#status.textContent = ''
      void this.#call({ op: 'read' }, '')
    } else this.#paint()
  }

  async #call(body: Record<string, unknown>, ok: string) {
    const project = this.#target?.project
    if (!project) return
    this.#busy = true
    this.#paint()
    const res = await api('/api/review-models', { ...body, path: project }).catch(() => null)
    this.#busy = false
    if (project !== this.#target?.project) return
    if (!res || !res.ok) {
      say(this.#status, await problemOf(res), 'warn')
      return this.#paint()
    }
    const v = readView(await res.json().catch(() => null))
    if (v) this.#view = v
    if (ok) {
      say(this.#status, ok, 'ok')
    }
    this.#paint()
  }

  #paint() {
    const v = this.#view
    if (!this.#target?.project || !v) return this.replaceChildren()
    const focused = focusedKey(this)
    const off = this.#target.disabled
    const parts: HTMLElement[] = [el('p', 'small', 'Model and effort for each review role'), el('p', 'small muted', 'The default passes no --model or --effort, so the role\'s agent file decides. Each review shows the model it ran with.')]
    for (const [role, r] of Object.entries(v.roles)) {
      const model = el('select', 'textbox')
      model.id = `review-model-${role}`
      model.dataset.key = `review-model:${role}`
      const blank = el('option', '', 'Model: the default')
      blank.value = ''
      model.append(blank, ...v.models.map(m => {
        const o = el('option', '', visible(m.name))
        o.value = m.id
        return o
      }))
      model.value = v.models.some(m => m.id === r.model) ? r.model : ''
      const effort = el('select', 'textbox')
      effort.dataset.key = `review-effort:${role}`
      effort.setAttribute('aria-label', `Effort of the ${visible(r.name)} role`)
      const none = el('option', '', 'Effort: the default')
      none.value = ''
      effort.append(none, ...v.efforts.map(e => {
        const o = el('option', '', `Effort: ${visible(e)}`)
        o.value = e
        return o
      }))
      effort.value = v.efforts.includes(r.effort) ? r.effort : ''
      const shownModel = model.value
      const shownEffort = effort.value
      const save = () => {
        if (this.#busy) {
          model.value = shownModel
          effort.value = shownEffort
          return
        }
        void this.#call({ op: 'set', role, model: model.value, effort: effort.value }, `The ${visible(r.name)} role runs with ${model.value || 'the default model'}${effort.value ? ` at ${effort.value} effort` : ''} from the next review on.`)
      }
      model.addEventListener('change', save)
      effort.addEventListener('change', save)
      model.disabled = effort.disabled = off
      if (this.#busy) for (const s of [model, effort]) s.setAttribute('aria-disabled', 'true')
      const label = el('label', 'small', `The ${visible(r.name)} role runs on`)
      label.htmlFor = model.id
      const row = el('div', 'row field-row')
      row.append(label, model, effort)
      parts.push(row)
    }
    parts.push(this.#status)
    this.replaceChildren(...parts)
    refocus(this, focused)
  }
}

customElements.define('sf-review-models', SfReviewModels)

declare global {
  interface HTMLElementTagNameMap {
    'sf-review-models': SfReviewModels
  }
}
