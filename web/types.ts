export interface SessionInfo {
  sid: string
  cwd: string
  project?: string
  agent?: string
  live: boolean
  startedAt?: number
  name?: string
  id?: string
  kind?: string
  state?: string
  status?: string
  waitingFor?: string
  queued?: number
  verified?: boolean
  running?: boolean
  disputed?: boolean
  outdated?: boolean
  controlled?: boolean
  mode?: string
  busy?: boolean
  needs?: number
  epoch?: string
  lastSeq?: number
  checkout?: string
  login?: boolean
  claims?: string[]
  level?: string
  levelReason?: string
  levelDropped?: boolean
  review?: SessionReview
  push?: SessionPush
  forge?: SessionForge[]
  signIn?: SignIn
}

export type SignInMethod = 'subscription' | 'api-key' | 'gateway' | 'bedrock' | 'vertex' | 'not-signed-in' | 'unknown'
export type Expect = 'any' | 'subscription' | 'api-key'

export interface SignIn {
  method: SignInMethod
  words: string
  endpoint?: string
  costs: boolean
  checkedAt?: number
}

export interface StartupFile {
  path: string
  place: 'worktree' | 'clone'
  link: boolean
  target?: string
  size: number
  hash: string
  content: string
  clipped: boolean
  uncommitted: boolean
}

export interface StartupApproval {
  agent: string
  repo: string
  commit: string
  hash: string
  files: StartupFile[]
}

export interface Needs {
  approval?: StartupApproval
  signIn?: SignIn
  expected?: Expect
  step?: string
  cost?: boolean
  trust?: string
}

export interface Consent {
  approve?: string
  costOk?: boolean
}

export interface ReviewerView {
  signIn?: SignIn
  expect: Expect
}

export type PushResult = '' | 'pushed' | 'up-to-date' | 'held' | 'denied' | 'refused' | 'unclear'

export interface PushAudit {
  time: number
  by: string
  result: PushResult
  reason: string
  commit: string
  branch: string
  remote: string
  missing: string[]
  personReason: string
}

export interface SessionPush {
  running: boolean
  result: PushResult
  reason: string
  card: string
  commit: string
  branch: string
  remote: string
  missing: string[]
  needsReason: boolean
  expires: number
  at: number
  audit: PushAudit | null
  handoff?: boolean
  passed?: number
  overridden?: number
  findings?: boolean
  findingsSent?: number
}

export interface SessionForge {
  card: string
  kind: 'issue' | 'pull-request' | 'comment'
  state: 'waiting' | 'sending' | 'opened' | 'exists' | 'denied' | 'failed' | 'unclear' | 'posting' | 'commented'
  title: string
  body: string
  head: string
  base: string
  host: string
  repo: string
  number: number
  url: string
  reason: string
  draft?: boolean
  asked: number
  at: number
  matches?: ForgeMatch[]
}

export interface ForgeMatch {
  number: number
  url: string
  title: string
  created: number
  exact: boolean
}

export interface ForgePanelView {
  forge: SessionForge[]
  disabled: string
  answering: boolean
}

export interface FlaggedFile {
  path: string
  why: string
  allowed: boolean
  kind?: string
  size?: number
  full?: boolean
}

export interface PushPanelView {
  push: SessionPush | null
  disabled: string
  answering: boolean
  flagged?: FlaggedFile[]
  flagsRead?: boolean
}

export type ReviewRoleState = 'running' | 'pass' | 'block' | 'unknown' | 'failed'

export interface ReviewRole {
  role: string
  name: string
  agent: string
  state: ReviewRoleState
  verdict: string
  message: string
  reason: string
  costUsd?: number
  why?: string
  round?: number
  limit?: number
  since?: string
  incremental?: boolean
  previous?: boolean
  override?: string
  pluginCommit?: string
  claudeCode?: string
  models?: string[]
}

export interface SessionReview {
  commit: string
  base: string
  branch: string
  running: boolean
  note: string
  roles: ReviewRole[]
  signIn?: SignIn
  costUsd?: number
  sessionCostUsd?: number
  testList?: TestList
}

export interface TestList {
  commit: string
  branch: string
  state: 'running' | 'done' | 'failed' | 'withheld'
  text: string
  reason: string
  costUsd?: number
  at: number
}

export interface ReviewsView {
  review: SessionReview | null
  disabled: string
  asking: boolean
  reviewer: ReviewerView | null
  pushing: boolean
  pushed?: boolean
  pushSaid?: { text: string; tone: string } | null
  planned?: { name: string; agent: string; why: string }[]
  gate?: Reviewer[]
  head?: string
  overriding?: string
  testList?: TestList | null
  testAsking?: boolean
}

export interface Changes {
  branch: string
  head: string
  worktree: 'main' | 'linked'
  base: string
  baseRef: string
  noBase: boolean
  commits: { sha: string; parents: string[]; time: number; subject: string }[]
  files: { status: 'added' | 'changed' | 'deleted'; path: string }[]
  more: boolean
  note: string
  diff?: string
  clipped?: boolean
}

export type OverviewSession = SessionInfo & { waiting: CardEvent[] }

export interface Project {
  path: string
  name: string
  url?: string
  branch?: string
  addedAt?: number
  agents: string[]
  rounds?: number
  type?: string
  instructions?: string[]
  autoFindings?: boolean
  prComment?: boolean
  hold?: HoldSetting
}

export interface HoldSetting {
  afterBlock: boolean
  maxFiles: number
  maxLines: number
}

export interface Starter {
  id: string
  name: string
  method: string
  enabled: boolean
  off?: string
  expect?: Expect
}

export interface Cloning {
  url: string
  name: string
  startedAt: number
}

export interface TokenHost {
  host: string
  savedAt: number
}

export interface ProjectsView {
  projects: Project[]
  cloning: Cloning[]
  hosts: TokenHost[]
  repos?: boolean
  starters?: Starter[]
  reviewer?: ReviewerView | null
}

export interface Dispatch extends Consent {
  path: string
  agent: string
  name: string
  model: string
  mode: string
  type?: string
}

export interface HelloEvent {
  type: 'hello'
  stream: string
  sessions: SessionInfo[]
  supervisor?: string
}

export interface SessionsEvent {
  type: 'sessions'
  sessions: SessionInfo[]
  supervisor?: string
}

export interface ResetEvent {
  type: 'reset'
  sid: string
  epoch: string
}

export interface DroppedEvent {
  type: 'dropped'
  sid: string
  why?: 'timeout' | 'read-only'
  count?: number
}

export interface SupervisorEvent {
  type: 'supervisor'
  state: string
}

interface Stamped {
  sid: string
  epoch: string
  seq: number
  ts: number
}

export interface PromptEvent extends Stamped {
  type: 'prompt'
  text: string
  from: 'page' | 'history' | 'terminal' | 'task'
  images?: number
  lost?: number
  files?: string[]
}

export interface ContextView {
  model: string
  total: number
  max: number
  percent: number
  categories: { name: string; tokens: number; color: string; kind: string }[]
  grid: [number, number][][]
}

export interface CommandEvent extends Stamped {
  type: 'command'
  name: string
  text: string
  failed?: boolean
  approval?: 'requirements' | 'plan'
  context?: ContextView
}

export interface TextEvent extends Stamped {
  type: 'text'
  turnId: string
  text: string
}

export interface ToolEvent extends Stamped {
  type: 'tool'
  id: string
  tool: string
  summary: string
  agentId?: string
}

export interface ToolDoneEvent extends Stamped {
  type: 'tool_done'
  id: string
  ok: boolean
  outcome: string
}

export interface BusyEvent extends Stamped {
  type: 'busy'
  turnId: string
}

export interface IdleEvent extends Stamped {
  type: 'idle'
  turnId: string
  reason?: string
  tokens?: number
}

export interface Option {
  label: string
  description: string
}

export interface Question {
  question: string
  header: string
  kind?: 'choice' | 'text' | 'number'
  description?: string
  multiSelect: boolean
  options: Option[]
  placeholder?: string
  min?: number
  max?: number
  step?: number
  unit?: string
  defaultValue?: number
}

interface CardBase extends Stamped {
  id: string
  agentId?: string
  readOnly?: boolean
  who?: string
  attach?: string
}

export interface AskEvent extends CardBase {
  type: 'ask'
  questions: Question[]
}

export interface PermissionEvent extends CardBase {
  type: 'permission'
  tool: string
  summary: string
  detail: string
  complete: boolean
  always: string
  auto?: boolean
}

export interface PlanEvent extends CardBase {
  type: 'plan'
  plan: string
}

export type CardEvent = AskEvent | PermissionEvent | PlanEvent

export interface ResolvedEvent extends Stamped {
  type: 'resolved'
  id: string
}

export interface EndedEvent extends Stamped {
  type: 'ended'
}

export type ReviewState = 'none' | 'running' | 'stale' | 'pass' | 'block' | 'overridden' | 'unknown' | 'failed' | 'unclean' | 'untrusted'

export interface Reviewer {
  type: string
  role?: string
  name: string
  why?: string
  state: ReviewState
  verdict: string
  round?: number
  limit?: number
  capped?: boolean
  override?: string
  since?: string
  incremental?: boolean
}

export interface GateEvent {
  head: string
  branch: string
  adapter: string
  define: boolean
  plan: string
  planHash?: string
  planDoc?: WorkflowDoc | null
  research: string
  reviewers: Reviewer[]
  allowed: boolean
  missing: string[]
  size?: Size
  chosen?: boolean
  effective?: Size
  sizeNote?: string
  approved?: boolean
  uncommitted?: { complete: boolean; count: number; note: string; files: { status: string; path: string; flag?: string; allowed?: boolean }[] } | null
  requirements?: WorkflowDoc | null
  implemented?: boolean
  waiting?: string
  holds?: { define: string; plan: string; tasks?: string; reviewer?: string; approveRequirements?: string; approvePlan?: string; ignore?: string }
  flagged?: FlaggedFile[]
  flagsRead?: boolean
  lfs?: string[]
  handoff?: boolean
  workType?: string
  types?: { id: string; name: string; steps: string[]; builtin: boolean }[]
  team?: { approved: boolean; note: string }
  commands?: { define?: string; plan?: string; tasks?: string; implement?: string }
  tasks?: boolean
  tasksWritten?: boolean
  rounds?: number
  committed?: boolean
}

export interface WorkflowDoc {
  path: string
  text: string
  hash: string
  approved?: boolean
  changed?: boolean
  clipped?: boolean
  previous?: string
}

export interface WorkflowEvent extends Stamped {
  type: 'workflow'
  knackpack: boolean
  plan: WorkflowDoc | null
  research: { path: string } | null
  requirements: WorkflowDoc | null
  intent: { path: string; title: string; status: string; agreed: string[]; assumed: string[]; open: string[]; built: string[]; files: string[] } | null
}

export interface TeamSetup {
  adapter: string
  types: { id: string; name: string; steps: string[]; edits: string; push: boolean; roles: string[]; builtin: boolean }[]
  defaultType: string
  roles: Record<string, string>
  agents: string[]
  fileRules: { pattern: string; agent: string }[]
  rounds: number
}

export interface TeamView {
  path: string
  head: string
  text: string | null
  sha: string
  problems: string[]
  approved: { sha: string; at: number; same: boolean } | null
  note: string
  setup: TeamSetup | null
  setupProblems: string[]
  remote: { branch: string; present: boolean; same: boolean }[]
}

export type Size = 'feature' | 'fix' | 'docs' | 'explore' | 'review'

export interface PlanFileEvent extends Stamped {
  type: 'planfile'
  path: string
  hash: string
  text: string
}

export interface PlanApproval {
  kind?: 'plan' | 'requirements'
  path: string
  text: string
  hash: string
  approved: boolean
  changed: boolean
  draft?: boolean
  disabled: boolean
  hold?: string
  refused?: string
  note?: string
  previous?: string
}

export interface ProjectAgents {
  project: string
  disabled: boolean
  rounds: number
  prComment: boolean
  type: string
  types: [string, string][]
  autoFindings: boolean
  hold?: HoldSetting
}

export interface AgentInfo {
  id: string
  type: string
  description: string
  status: string
}

export interface AgentsEvent extends Stamped {
  type: 'agents'
  agents: AgentInfo[]
}

export interface IntentRecord {
  found: boolean
  title?: string
  status?: string
  agreed?: string[]
  assumed?: string[]
  open?: string[]
  built?: string[]
}

export interface IntentEvent extends Stamped {
  type: 'intent'
  record: IntentRecord
}

export interface CatalogEntry {
  name: string
  description: string
}

export interface Catalog {
  commands: CatalogEntry[]
  agents: CatalogEntry[]
}

export interface CatalogEvent extends Stamped {
  type: 'catalog'
  commands: CatalogEntry[]
  agents: CatalogEntry[]
}

export interface ControlEvent extends Stamped {
  type: 'control'
  on: boolean
}

export interface ModeEvent extends Stamped {
  type: 'mode'
  mode: string
}

export interface ContextEvent extends Stamped {
  type: 'context'
  tokens: number
  level: '' | 'warn' | 'stop'
}

export interface CompactEvent extends Stamped {
  type: 'compact'
  state: 'start' | 'done' | 'skipped' | 'failed'
  trigger: string
  before?: number
  after?: number
  reason?: string
}

export interface UsageEvent extends Stamped {
  type: 'usage'
  total?: number
  costUsd?: number
  estimate?: boolean
}

export interface AuditEvent extends Stamped {
  type: 'audit' | 'refusal'
  kind: string
}

export interface LoginEvent extends Stamped {
  type: 'login'
  failed: boolean
}

export type SessionEvent =
  | ContextEvent
  | PromptEvent
  | CommandEvent
  | TextEvent
  | ToolEvent
  | ToolDoneEvent
  | BusyEvent
  | IdleEvent
  | CardEvent
  | ResolvedEvent
  | EndedEvent
  | WorkflowEvent
  | PlanFileEvent
  | AgentsEvent
  | IntentEvent
  | CatalogEvent
  | ControlEvent
  | ModeEvent
  | LoginEvent
  | UsageEvent
  | CompactEvent
  | AuditEvent

export interface RepoEvent {
  type: 'repo'
  state: 'cloning' | 'added' | 'failed'
  url: string
  name: string
  message?: string
}

export interface ReviewerEvent extends SignIn {
  type: 'reviewer'
}

export type StreamEvent = HelloEvent | SessionsEvent | ResetEvent | DroppedEvent | SupervisorEvent | RepoEvent | ReviewerEvent | SessionEvent

export interface SessionGrant {
  token: string
}

export type AskAnswer = Record<string, string>
export type PermissionAnswer = { choice: 'yes' | 'always' | 'auto' } | { choice: 'no'; note?: string }
export type PlanAnswer = { choice: 'revise'; note: string } | { choice: 'terminal' }
export type Answer = AskAnswer | PermissionAnswer | PlanAnswer

export interface CardHost extends HTMLElement {
  source: CardEvent
  card: CardEvent
  error: string
}

export interface Check {
  type: string
  name: string
  why?: string
}

export interface CheckStatus {
  running?: boolean
  ran?: boolean
  disabled?: boolean
  reason?: string
  armed?: boolean
  gate?: Reviewer | null
  error?: string
  asked?: number
  sid?: string
  outdated?: boolean
}

export interface Tab {
  id: string
  label: string
  title?: string
  closable: boolean
  need: boolean
}

export interface TabsState {
  tabs: Tab[]
  active: string
}

export interface FileView {
  path: string
  text?: string
  note?: string
}

export interface Prefs {
  theme?: string
  scale?: number
  bar?: string
}

declare global {
  interface HTMLElementEventMap {
    'sf-open': CustomEvent<{ id: string }>
    'sf-close': CustomEvent<{ id: string }>
    'sf-move': CustomEvent<{ id: string; index: number }>
    'sf-approve': CustomEvent<{ hash: string }>
    'sf-revise': CustomEvent<{ note: string }>
    'sf-project-setting': CustomEvent<{ path: string; op: 'rounds'; rounds: number } | { path: string; op: 'type'; type: string } | { path: string; op: 'findings'; findings: boolean } | { path: string; op: 'prComment'; on: boolean } | { path: string; op: 'hold'; hold: HoldSetting }>
    'sf-answer': CustomEvent<{ id: string; answer: Answer }>
    'sf-counts': CustomEvent<{ waiting: number; working: number; stopped: number }>
    'sf-paint': Event
    'sf-repo': CustomEvent<{ op: 'add'; url: string } | { op: 'remove'; name: string }>
    'sf-token': CustomEvent<{ op: 'save'; host: string; token: string } | { op: 'remove'; host: string }>
    'sf-dispatch': CustomEvent<Dispatch>
    'sf-session-action': CustomEvent<{ sid: string; action: 'stop' | 'remove' }>
    'sf-file': CustomEvent<{ path: string }>
    'sf-reload': Event
    'sf-settled': Event
    'sf-review': Event
    'sf-expect': CustomEvent<{ agent: string; expect: Expect }>
    'sf-push-request': Event
    'sf-push-answer': CustomEvent<{ card: string; commit: string; allow: boolean; reason: string }>
    'sf-handoff': CustomEvent<{ card: string; commit: string }>
    'sf-send-findings': CustomEvent<{ card: string; commit: string }>
    'sf-forge-answer': CustomEvent<{ card: string; approve: boolean; draft?: boolean }>
    'sf-test-list': Event
    'sf-allow-file': CustomEvent<{ path: string; reason: string }>
    'sf-remove-file': CustomEvent<{ path: string }>
    'sf-override': CustomEvent<{ agent: string; commit: string; reason: string }>
  }
}
