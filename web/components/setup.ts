import { button, el, visible } from '../lib/dom.ts'
import { icon } from './icon.ts'
import './accordion.ts'
import type { AgentLogin, SignInState } from './login.ts'

export interface SetupState {
  signIn: SignInState | null
  projects: number | null
  sessions: number
}

interface Step {
  title: string
  done: boolean
  why: string
  action?: string
  start?: string
  rows?: HTMLElement
}

const VENDORS: Record<string, string> = { claude: 'Anthropic', codex: 'OpenAI' }

function stateWords(a: AgentLogin): string {
  if (a.signedIn === true) return 'Signed in'
  if (a.signedIn === false) return 'Not signed in'
  return 'Couldn’t be checked'
}

function agentPanel(a: AgentLogin): HTMLElement {
  const panel = el('div', 'setup-agent-panel')
  if (a.signedIn) {
    panel.append(el('p', 'small muted', `${visible(a.name)} is signed in.`))
    return panel
  }
  if (a.id === 'claude') {
    panel.append(el('p', 'small muted', 'Sign in with a Claude subscription or an API key from the Claude Console.'))
    const go = button('Sign in', 'primary small')
    go.dataset.start = 'login'
    panel.append(go)
    return panel
  }
  if (a.step.startsWith('docker compose ')) {
    panel.append(el('p', 'small muted', 'Run this in a terminal, in the Software Factory folder, then open the link it prints and enter the code it shows.'))
    const block = el('div', 'setup-code')
    const copy = button('', 'setup-copy', () => {
      void navigator.clipboard.writeText(a.step).then(() => {
        copy.replaceChildren(icon('check'))
        copy.setAttribute('aria-label', 'Copied')
        copy.title = 'Copied'
        setTimeout(() => {
          copy.replaceChildren(icon('copy'))
          copy.setAttribute('aria-label', 'Copy the command')
          copy.title = 'Copy'
        }, 1500)
      }, () => { copy.title = 'Couldn’t copy: select the command and copy it' })
    })
    copy.append(icon('copy'))
    copy.setAttribute('aria-label', 'Copy the command')
    copy.title = 'Copy'
    block.append(el('code', '', visible(a.step)), copy)
    panel.append(block)
    return panel
  }
  if (a.step) panel.append(el('p', 'small muted', visible(a.step)))
  return panel
}

class SfSetup extends HTMLElement {
  #key = ''
  readonly #agents = el('sf-accordion', 'setup-agents')

  #agentList(agents: AgentLogin[]): HTMLElement {
    this.#agents.items = agents.map(a => ({
      id: a.id,
      title: visible(a.name),
      note: VENDORS[a.id] ?? '',
      state: stateWords(a),
      good: a.signedIn === true,
      panel: agentPanel(a),
    }))
    return this.#agents
  }

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
    const agents = v.signIn.agents
    const steps: Step[] = [
      {
        title: 'Sign an agent in',
        done: agents.some(a => a.signedIn === true),
        why: agents.length
          ? 'Sessions run on your own account with each agent’s vendor. One agent is enough to start; open one to sign it in.'
          : 'No agent is switched on in this engine.',
        rows: agents.length ? this.#agentList(agents) : undefined,
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
