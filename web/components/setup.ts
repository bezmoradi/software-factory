import { button, el } from '../lib/dom.ts'
import { icon } from './icon.ts'
import type { SignInState } from './login.ts'

export interface SetupState {
  signIn: SignInState | null
  projects: number | null
  sessions: number
  identity: boolean | null
}

interface Step {
  title: string
  done: boolean
  why: string
  action?: string
  start?: string
  rows?: HTMLElement
}

class SfSetup extends HTMLElement {
  #key = ''

  set state(v: SetupState) {
    const key = JSON.stringify(v)
    if (key === this.#key) return
    this.#key = key
    this.#render(v)
  }

  #render(v: SetupState) {
    if (v.signIn === null || v.projects === null) {
      this.hidden = true
      return
    }
    const steps: Step[] = [
      {
        title: 'You’ll sign in to each session when you start it',
        done: true,
        why: 'Each session runs in its own container, on your own account with its agent’s vendor.',
      },
      {
        title: 'Sign the reviewers in',
        done: v.signIn.reviewers === true,
        why: 'Every push waits for reviews, which run on Claude Code in the reviewers’ own container, signed in separately from the agents.',
        action: 'Sign Claude Code in',
        start: 'login',
      },
      {
        title: 'Add a project',
        done: v.projects > 0,
        why: 'The Git repository the agents clone and work in.',
        action: 'Add project',
        start: 'add',
      },
      ...(v.identity === null ? [] : [{
        title: 'Set the name and email agents commit with',
        done: v.identity,
        why: 'No session starts until they are set: an agent with none may commit with the email of its sign-in, which a push makes public. For GitHub, use your noreply address.',
        action: 'Set name and email',
        start: 'identity',
      }]),
      {
        title: 'Start a session',
        done: v.sessions > 0,
        why: 'Pick a project and an agent. Each session works on its own branch.',
        action: 'New session',
        start: 'session',
      },
    ]
    const done = steps.filter(s => s.done).length
    if (done === steps.length) {
      this.hidden = true
      return
    }
    const current = steps.findIndex(s => !s.done)
    const list = el('ol', 'setup-steps')
    steps.forEach((s, i) => {
      const li = el('li', s.done ? 'done' : i === current ? 'current' : 'later')
      const mark = el('span', 'setup-mark')
      mark.setAttribute('aria-hidden', 'true')
      if (s.done) mark.append(icon('check'))
      else mark.textContent = String(i + 1)
      const body = el('div', 'setup-body')
      body.append(el('strong', '', s.title))
      if (s.done) body.append(el('span', 'sr-only', ', done'))
      li.append(mark, body)
      if (i === current) {
        body.append(el('p', 'small muted', s.why))
        if (s.rows) body.append(s.rows)
        if (s.action && s.start) {
          const go = button(s.action, 'primary small')
          go.dataset.start = s.start
          li.append(go)
        }
      }
      list.append(li)
    })
    const head = el('p', 'setup-head')
    head.append(el('strong', '', 'Finish setting up'), el('span', 'muted', ` · ${done} of ${steps.length} done`))
    this.replaceChildren(head, list)
    this.hidden = false
  }
}

customElements.define('sf-setup', SfSetup)

declare global {
  interface HTMLElementTagNameMap {
    'sf-setup': SfSetup
  }
}
