import { request } from '../api.ts'
import { el, focusedKey, refocus, replaceKeeping, say, statusLine, visible } from '../lib/dom.ts'
import { field, form } from '../lib/form.ts'
import { dropdown, option } from '../lib/controls.ts'

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
    const a = await request('/api/review-models', { ...body, path: project })
    this.#busy = false
    if (project !== this.#target?.project) return
    if (!a.ok) {
      say(this.#status, a.message, 'warn')
      return this.#paint()
    }
    const v = readView(a.body)
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
      const model = dropdown({ options: [option('Model: the default', ''), ...v.models.map(m => option(visible(m.name), m.id))] })
      model.id = `review-model-${role}`
      model.dataset.key = `review-model:${role}`
      model.value = v.models.some(m => m.id === r.model) ? r.model : ''
      const effort = dropdown({ options: [option('Effort: the default', ''), ...v.efforts.map(e => option(`Effort: ${visible(e)}`, e))] })
      effort.dataset.key = `review-effort:${role}`
      effort.setAttribute('aria-label', `Effort of the ${visible(r.name)} role`)
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
      parts.push(form({ inline: true, cls: 'field-row', children: [field(`The ${visible(r.name)} role runs on`, model, { kind: 'inline', cls: 'field-row' }), effort] }))
    }
    parts.push(this.#status)
    replaceKeeping(this, parts)
    refocus(this, focused)
  }
}

customElements.define('sf-review-models', SfReviewModels)

declare global {
  interface HTMLElementTagNameMap {
    'sf-review-models': SfReviewModels
  }
}
