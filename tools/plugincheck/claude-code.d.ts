export type Json = unknown

export interface FileStat {
  kind: 'file' | 'dir' | 'other'
  size: number
  mtimeMs: number
  isLink: boolean
  realPath?: string
}

export interface DirEntry {
  name: string
  kind: 'file' | 'dir' | 'other'
  size: number
  mtimeMs: number
  isLink: boolean
}

export interface HttpReply {
  status: number
  ok: boolean
  text: string
}

export interface HttpOptions {
  method?: string
  headers?: Record<string, string>
  body?: string
  socketPath?: string
}

export interface Timer {
  cancel(): void
}

export interface ContextCategory {
  name: string
  tokens: number
  color: string
  kind: string
}

export interface ContextSquare {
  categoryName: string
  squareFullness: number
}

export interface ContextBreakdown {
  model: string
  totalTokens: number
  rawMaxTokens: number
  percentage: number
  categories: ContextCategory[]
  gridRows: ContextSquare[][]
}

export interface SessionUsage {
  context: { tokens: number; breakdown?: ContextBreakdown }
}

export interface SessionMessage {
  role: 'user' | 'assistant'
  text: string
}

export interface AgentEntry {
  id: string
  type: string
  description: string
  status: string
}

export interface CommandEntry {
  name: string
  description: string
  source: string
}

export interface EngineInterface {
  agent: { list(): Promise<AgentEntry[]> }
  clock: {
    after(ms: number, fn: () => void): Timer
    every(ms: number, fn: () => void): Timer
    sleep(ms: number, options?: { signal?: AbortSignal }): Promise<void>
  }
  command: {
    list(): Promise<CommandEntry[]>
    register(command: { name: string; description: string }): Promise<void>
    run(call: { command: string; args: string }): Promise<{ text?: string }>
  }
  env: { get(name: string): Promise<string | undefined> }
  fs: {
    list(path: string): Promise<DirEntry[]>
    read(path: string): Promise<string>
    stat(path: string, options?: { resolve?: boolean }): Promise<FileStat>
  }
  http: { fetch(url: string, options?: HttpOptions): Promise<HttpReply> }
  plugin: { readonly name: string; readonly root: string }
  process: { run(argv: string[]): Promise<{ code: number; stdout: string; stderr: string }> }
  prompt: { submit(prompt: { text: string; asUser?: boolean }): Promise<void> }
  session: {
    id(): Promise<string>
    cwd(): Promise<string>
    messages(): Promise<SessionMessage[]>
    usage(options?: { breakdown?: 'full' | 'summary'; columns?: number }): Promise<SessionUsage>
  }
  settings: { read(): Promise<Json> }
  store: {
    get(key: string): Promise<Json>
    set(key: string, value: Json): Promise<void>
    keys(): Promise<string[]>
    delete(key: string): Promise<void>
  }
  tool: {
    register(tool: { name: string; description: string; inputSchema?: Record<string, unknown>; isDeferred?: boolean }): Promise<{ tool: string }>
  }
  turn: { abort(turn: { turnId: string }): Promise<void> }
  ui: { toast(text: string, options?: { timeoutMs?: number }): void }
}

export type PermissionMode = 'default' | 'acceptEdits' | 'plan' | 'bypassPermissions' | 'dontAsk' | 'auto'

export type PermissionUpdateDestination = 'userSettings' | 'projectSettings' | 'localSettings' | 'session' | 'cliArg'

export interface PermissionRuleValue {
  toolName: string
  ruleContent?: string
}

export type PermissionUpdate =
  | { type: 'addRules' | 'replaceRules' | 'removeRules'; rules: PermissionRuleValue[]; behavior: 'allow' | 'deny' | 'ask'; destination: PermissionUpdateDestination }
  | { type: 'setMode'; mode: PermissionMode; destination: PermissionUpdateDestination }
  | { type: 'addDirectories' | 'removeDirectories'; directories: string[]; destination: PermissionUpdateDestination }

export type PermissionDecision =
  | { behavior: 'allow'; updatedInput?: Record<string, unknown>; updatedPermissions?: PermissionUpdate[] }
  | { behavior: 'deny'; message?: string; interrupt?: true }

export interface ToolCallResult {
  deny?: string
  isError?: boolean
  text?: string
  result?: unknown
}

export interface TurnChunk {
  kind: string
  text?: string
}

export interface Events {
  'session.start': [{ source?: string }, unknown]
  'session.end': [{ reason?: string }, unknown]
  'session.measure': [{ context: { tokens: number } }, unknown]
  'session.compact': [
    { agentId?: string; trigger: string },
    { skip: string; tokensBefore?: undefined; tokensAfter?: undefined } | { skip?: undefined; tokensBefore?: number; tokensAfter?: number },
  ]
  'command.run': [{ command: string; args: string; origin?: unknown }, { text?: string }]
  'prompt.submit': [{ text: string; isMeta?: boolean; origin?: unknown; command?: unknown }, unknown]
  'turn.start': [{ turnId: string; agentId?: string }, unknown]
  'turn.complete': [
    { turnId: string; agentId?: string; reason: string; usage?: { input_tokens: number; output_tokens: number } },
    unknown,
  ]
  'agent.offer': [{ agent: string; description?: string }, unknown]
  'tool.check': [{ tool: string; input: unknown; agentId?: string }, { decision: 'allow' | 'deny' | 'ask'; reason?: string }]
  'tool.call': [{ tool: string; tool_use_id?: string; agentId?: string; [arg: string]: unknown }, ToolCallResult]
  'classic.SessionStart': [{ source?: string }, { additionalContext?: string[] }]
  'classic.UserPromptSubmit': [{ permission_mode: PermissionMode; prompt: string }, unknown]
  'classic.Stop': [{ permission_mode: PermissionMode }, unknown]
  'classic.StopFailure': [{ error: string; permission_mode?: PermissionMode }, unknown]
  'classic.PermissionRequest': [
    { tool_name: string; tool_input: unknown; permission_mode?: string; agent_id?: string; permission_suggestions?: PermissionUpdate[] },
    { decision?: PermissionDecision },
  ]
}

export interface Next<I, R> {
  (e: I): Promise<R>
  readonly called: boolean
  readonly signal: AbortSignal
}

export type Hook<K extends keyof Events> = (
  $: EngineInterface,
  e: Events[K][0],
  next: Next<Events[K][0], Events[K][1]>,
) => Events[K][1] | Promise<Events[K][1]>

export type StepHook = (
  $: EngineInterface,
  e: { turnId: string; agentId?: string },
  next: ((e: { turnId: string; agentId?: string }) => AsyncIterable<TurnChunk>) & { readonly called: boolean },
) => AsyncIterable<TurnChunk>

export interface Registration<H> {
  catch(hook: H): Registration<H>
}

export type On = {
  <K extends keyof Events>(pattern: K, hook: Hook<K>): Registration<Hook<K>>
  <K extends keyof Events>(pattern: K, matcher: Partial<Events[K][0]>, hook: Hook<K>): Registration<Hook<K>>
  (pattern: 'turn.step', hook: StepHook): Registration<StepHook>
}

export type PluginOptions = Record<string, unknown>

export type Register = (on: On, options: PluginOptions) => unknown
