export type TokenKind = '' | 'keyword' | 'string' | 'comment' | 'number'

export interface Token {
  kind: TokenKind
  text: string
}

export interface Lang {
  name: string
  line?: string[]
  hashLine?: boolean
  block?: [string, string][]
  multi?: [string, string][]
  quotes?: string[]
  words?: Set<string>
  constants?: Set<string>
  caseless?: boolean
  tags?: boolean
  keys?: boolean
  headings?: boolean
  first?: Set<string>
}

export interface LexState {
  end: string
  kind: TokenKind
}

const set = (words: string) => new Set(words.split(' '))

const C_LIKE = set('if else for while do switch case default break continue return goto struct union enum typedef const static extern volatile register sizeof void char short int long float double signed unsigned bool class public private protected virtual override final abstract interface extends implements new delete this super throw throws try catch finally namespace using template typename import package export var let val fun func fn auto inline operator friend mutable explicit constexpr noexcept static_cast dynamic_cast reinterpret_cast instanceof synchronized transient native strictfp assert yield async await in is as out ref readonly sealed internal object when where init lazy lateinit companion suspend data open guard defer protocol extension self Self static_assert')
const C_CONSTANTS = set('true false null nullptr NULL nil None undefined')

const LANGS: Record<string, Lang> = {
  go: { name: 'Go', line: ['//'], block: [['/*', '*/']], multi: [['`', '`']], quotes: ['"', "'"], words: set('break case chan const continue default defer else fallthrough for func go goto if import interface map package range return select struct switch type var any error string bool byte rune int int8 int16 int32 int64 uint uint8 uint16 uint32 uint64 uintptr float32 float64 complex64 complex128 make new len cap append copy delete panic recover close'), constants: set('true false nil iota') },
  js: { name: 'JavaScript', line: ['//'], block: [['/*', '*/']], multi: [['`', '`']], quotes: ['"', "'"], words: set('async await break case catch class const continue debugger default delete do else export extends finally for from function get if import in instanceof let new of return set static super switch this throw try typeof var void while with yield as type interface enum implements private protected public readonly declare namespace abstract keyof infer satisfies unique is asserts override accessor'), constants: set('true false null undefined NaN Infinity') },
  json: { name: 'JSON', line: ['//'], block: [['/*', '*/']], quotes: ['"'], constants: set('true false null') },
  py: { name: 'Python', hashLine: true, multi: [['"""', '"""'], ["'''", "'''"]], quotes: ['"', "'"], words: set('and as assert async await break class continue def del elif else except finally for from global if import in is lambda match case nonlocal not or pass raise return try while with yield self print'), constants: set('True False None') },
  rs: { name: 'Rust', line: ['//'], block: [['/*', '*/']], quotes: ['"'], words: set('as async await break const continue crate dyn else enum extern fn for if impl in let loop match mod move mut pub ref return self Self static struct super trait type unsafe use where while macro_rules i8 i16 i32 i64 i128 isize u8 u16 u32 u64 u128 usize f32 f64 bool char str String Vec Option Result Box'), constants: set('true false None Some Ok Err') },
  c: { name: 'C-like', line: ['//'], block: [['/*', '*/']], quotes: ['"', "'"], words: C_LIKE, constants: C_CONSTANTS },
  rb: { name: 'Ruby', hashLine: true, quotes: ['"', "'"], words: set('alias and begin break case class def defined do else elsif end ensure for if in module next not or redo rescue retry return self super then undef unless until when while yield require attr_reader attr_writer attr_accessor puts'), constants: set('true false nil') },
  sh: { name: 'Shell', hashLine: true, quotes: ['"', "'"], words: set('if then else elif fi for while until do done case esac function in return local export readonly unset shift exit set echo printf cd test source exec trap eval'), constants: set('true false') },
  make: { name: 'Makefile', hashLine: true, quotes: ['"', "'"], words: set('ifeq ifneq ifdef ifndef else endif include define endef export unexport override vpath') },
  yaml: { name: 'YAML', hashLine: true, quotes: ['"', "'"], keys: true, constants: set('true false null yes no on off True False Null') },
  toml: { name: 'TOML', hashLine: true, line: [';'], quotes: ['"', "'"], multi: [['"""', '"""'], ["'''", "'''"]], keys: true, constants: set('true false') },
  css: { name: 'CSS', line: ['//'], block: [['/*', '*/']], quotes: ['"', "'"], words: set('important inherit initial unset revert none auto media import supports layer container keyframes font-face') },
  html: { name: 'Markup', block: [['<!--', '-->']], quotes: ['"', "'"], tags: true },
  md: { name: 'Markdown', block: [['<!--', '-->']], multi: [['```', '```']], quotes: ['`'], headings: true },
  sql: { name: 'SQL', line: ['--'], block: [['/*', '*/']], quotes: ["'", '"'], caseless: true, words: set('select from where and or not insert into values update set delete create table index view drop alter add column primary key foreign references join left right inner outer full on as group by order having limit offset union all distinct case when then else end is in like between exists returning with begin commit rollback transaction default unique check constraint integer int text varchar char boolean bool timestamp date serial bigint real numeric'), constants: set('true false null') },
  docker: { name: 'Dockerfile', hashLine: true, quotes: ['"', "'"], caseless: true, first: set('from run cmd label maintainer expose env add copy entrypoint volume user workdir arg onbuild stopsignal healthcheck shell as') },
  lua: { name: 'Lua', line: ['--'], quotes: ['"', "'"], words: set('and break do else elseif end for function goto if in local not or repeat return then until while'), constants: set('true false nil') },
}

const EXT: Record<string, string> = {
  go: 'go', js: 'js', mjs: 'js', cjs: 'js', jsx: 'js', ts: 'js', tsx: 'js', mts: 'js', cts: 'js',
  json: 'json', jsonc: 'json', json5: 'json',
  py: 'py', pyi: 'py', rs: 'rs',
  c: 'c', h: 'c', cc: 'c', cpp: 'c', cxx: 'c', hpp: 'c', hh: 'c', cs: 'c', java: 'c', kt: 'c', kts: 'c', swift: 'c', scala: 'c', dart: 'c', php: 'c', m: 'c', groovy: 'c', gradle: 'c',
  rb: 'rb', rake: 'rb', gemspec: 'rb',
  sh: 'sh', bash: 'sh', zsh: 'sh', ksh: 'sh', env: 'sh',
  mk: 'make', yaml: 'yaml', yml: 'yaml', toml: 'toml', ini: 'toml', cfg: 'toml', conf: 'toml', properties: 'toml',
  css: 'css', scss: 'css', less: 'css',
  html: 'html', htm: 'html', xhtml: 'html', xml: 'html', svg: 'html', vue: 'html', svelte: 'html', plist: 'html',
  md: 'md', markdown: 'md', sql: 'sql', lua: 'lua', dockerfile: 'docker',
}

const NAMES: Record<string, string> = { makefile: 'make', gnumakefile: 'make', dockerfile: 'docker', containerfile: 'docker', gemfile: 'rb', rakefile: 'rb', '.bashrc': 'sh', '.zshrc': 'sh', '.profile': 'sh', '.env': 'sh', 'go.mod': 'go', 'go.sum': 'go' }

export function langFor(path: string): Lang | null {
  const name = (path.split('/').pop() ?? '').toLowerCase()
  const byName = NAMES[name] ?? (name.startsWith('dockerfile.') || name.startsWith('containerfile.') ? 'docker' : name.startsWith('.env.') ? 'sh' : '')
  if (byName) return LANGS[byName] ?? null
  const dot = name.lastIndexOf('.')
  const key = dot > 0 ? EXT[name.slice(dot + 1)] : undefined
  return key ? LANGS[key] ?? null : null
}

const NUMBER = /0[xX][0-9a-fA-F_]+|0[bB][01_]+|0[oO][0-7_]+|\d[\d_]*(?:\.\d[\d_]*)?(?:[eE][+-]?\d+)?[a-zA-Z%]{0,4}|#[0-9a-fA-F]{3,8}\b/y
const WORD = /[A-Za-z_$][\w$-]*/y
const PLAIN_WORD = /[A-Za-z_$][\w$]*/y
const KEY = /^([ \t]*(?:-[ \t]+)?)(["']?[\w.\-/@]+(?: +[\w.\-/@]+)*["']?)([ \t]*[:=])(?=\s|$)/
const SECTION = /^\s*\[{1,2}[^\]]*\]{1,2}\s*$/
const TAG = /<\/?[A-Za-z][\w:.-]*|\/?>/y
const HEADING = /^\s{0,3}#{1,6}(\s|$)/

export const MAX_LINE = 2000

export function newState(): LexState {
  return { end: '', kind: '' }
}

export function tokens(text: string, lang: Lang, state: LexState): Token[] {
  const out: Token[] = []
  const push = (kind: TokenKind, part: string) => {
    if (!part) return
    const last = out[out.length - 1]
    if (last && last.kind === kind) last.text += part
    else out.push({ kind, text: part })
  }
  if (text.length > MAX_LINE) return [{ kind: '', text }]
  let i = 0
  const n = text.length
  if (state.end) {
    const at = text.indexOf(state.end)
    if (at < 0) {
      push(state.kind, text)
      return out
    }
    push(state.kind, text.slice(0, at + state.end.length))
    i = at + state.end.length
    state.end = ''
    state.kind = ''
  }
  if (lang.headings && i === 0 && HEADING.test(text)) {
    push('keyword', text)
    return out
  }
  if (lang.keys && i === 0) {
    if (SECTION.test(text)) {
      push('keyword', text)
      return out
    }
    const m = KEY.exec(text)
    if (m && !/^\s*#/.test(text)) {
      push('', m[1] ?? '')
      push('keyword', m[2] ?? '')
      push('', m[3] ?? '')
      i = m[0].length
    }
  }
  if (lang.first && i === 0) {
    const m = /^\s*([A-Za-z]+)/.exec(text)
    if (m && lang.first.has((m[1] ?? '').toLowerCase())) {
      push('', text.slice(0, m[0].length - (m[1] ?? '').length))
      push('keyword', m[1] ?? '')
      i = m[0].length
    }
  }
  let inTag = false
  while (i < n) {
    const ch = text[i]!
    if (lang.hashLine && ch === '#' && (i === 0 || /\s/.test(text[i - 1]!))) {
      push('comment', text.slice(i))
      return out
    }
    const line = lang.line?.find(m => text.startsWith(m, i))
    if (line) {
      push('comment', text.slice(i))
      return out
    }
    const pair = lang.block?.find(([open]) => text.startsWith(open, i)) ?? lang.multi?.find(([open]) => text.startsWith(open, i))
    if (pair) {
      const kind: TokenKind = lang.block?.includes(pair) ? 'comment' : 'string'
      const close = text.indexOf(pair[1], i + pair[0].length)
      if (close < 0) {
        push(kind, text.slice(i))
        state.end = pair[1]
        state.kind = kind
        return out
      }
      push(kind, text.slice(i, close + pair[1].length))
      i = close + pair[1].length
      continue
    }
    if (lang.tags && (ch === '<' || ch === '>' || ch === '/')) {
      TAG.lastIndex = i
      const m = TAG.exec(text)
      if (m) {
        push('keyword', m[0])
        inTag = !m[0].endsWith('>')
        i += m[0].length
        continue
      }
    }
    if (lang.quotes?.includes(ch) && (!lang.tags || inTag)) {
      let j = i + 1
      while (j < n && text[j] !== ch) j += text[j] === '\\' ? 2 : 1
      push('string', text.slice(i, Math.min(n, j + 1)))
      i = Math.min(n, j + 1)
      continue
    }
    const before = i > 0 ? text[i - 1]! : ''
    if (/[0-9#]/.test(ch) && !/[\w$]/.test(before) && !lang.tags) {
      NUMBER.lastIndex = i
      const m = NUMBER.exec(text)
      if (m && (ch !== '#' || lang.name === 'CSS')) {
        push('number', m[0])
        i += m[0].length
        continue
      }
    }
    if (/[A-Za-z_$]/.test(ch) && !/[\w$]/.test(before)) {
      const re = lang.name === 'CSS' ? WORD : PLAIN_WORD
      re.lastIndex = i
      const m = re.exec(text)
      if (m) {
        const word = m[0]
        const key = lang.caseless ? word.toLowerCase() : word
        push(lang.constants?.has(word) || lang.constants?.has(key) ? 'number' : lang.words?.has(key) ? 'keyword' : '', word)
        i += word.length
        continue
      }
    }
    push('', ch)
    i++
  }
  return out
}
