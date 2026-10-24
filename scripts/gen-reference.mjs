#!/usr/bin/env node
// Generate the reference pages in docs/reference/ from the contract's schemas and from the exported
// constants of the built harness package.
//
//   node scripts/gen-reference.mjs           write the pages
//   node scripts/gen-reference.mjs --check   generate to a temporary directory and fail with a diff
//                                            where a page in docs/reference/ differs
//
// Options (used by the drift test): --schemas DIR (default contract/schemas), --out DIR (default
// docs/reference). The package is read from packages/harness/dist, so `npm run build` comes first.
//
// Nothing here is written by hand except the headings, the column names and the few sentences that
// say how a page is read. Lists come from the schemas (members, enums, descriptions, defaults,
// closed or open) and from the package's constants; a constant a page uses that the package does not
// export, or a list that a schema and the package state differently, stops the run with the name.
// Header and marker constants are listed by name, never by value, and nothing here is read from a run.
import { spawnSync } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const repo = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const harness = join(repo, 'packages/harness')

// ---------------------------------------------------------------------------------------------
// Arguments

function parseArguments(argv) {
  const options = { check: false, schemas: join(repo, 'contract/schemas'), out: join(repo, 'docs/reference') }
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i]
    if (a === '--check') options.check = true
    else if (a === '--schemas' || a === '--out') {
      if (i + 1 >= argv.length) throw new Error(`${a} needs a directory`)
      options[a.slice(2)] = resolve(argv[++i])
    } else throw new Error(`unknown argument ${a}\nusage: node scripts/gen-reference.mjs [--check] [--schemas DIR] [--out DIR]`)
  }
  return options
}

// ---------------------------------------------------------------------------------------------
// Markdown helpers

const cell = (text) => String(text ?? '').replace(/\s*\n\s*/g, ' ').replace(/\|/g, '\\|').trim()
const code = (text) => {
  const s = String(text)
  return s.includes('`') ? `\`\` ${s} \`\`` : `\`${s}\``
}
const json = (value) => JSON.stringify(value)
const literal = (v) => code(typeof v === 'string' ? v : json(v))
const list = (values, render = literal) => values.map(render).join(', ')

function table(header, rows) {
  if (rows.length === 0) return ''
  const lines = [`| ${header.join(' | ')} |`, `| ${header.map(() => '---').join(' | ')} |`]
  for (const row of rows) lines.push(`| ${row.map(cell).join(' | ')} |`)
  return lines.join('\n')
}

// ---------------------------------------------------------------------------------------------
// Schemas

class Schemas {
  constructor(dir) {
    this.dir = dir
    this.files = new Map()
  }
  names() {
    return readdirSync(this.dir).filter((f) => f.endsWith('.json')).sort()
  }
  read(file) {
    if (!this.files.has(file)) {
      const path = join(this.dir, file)
      if (!existsSync(path)) throw new Error(`schema ${file} is not in the schemas directory`)
      this.files.set(file, JSON.parse(readFileSync(path, 'utf8')))
    }
    return this.files.get(file)
  }
  def(file, name) {
    const found = this.read(file).$defs?.[name]
    if (found === undefined) throw new Error(`${file} has no $defs/${name}`)
    return found
  }
}

/** A reference as a reader sees it: `$defs/name` in the same file, `file.json $defs/name` across files. */
function refText(ref, file) {
  const [target, pointer] = ref.split('#')
  const where = target === '' || target === file ? '' : `${target} `
  const what = pointer ? pointer.replace(/^\//, '') : ''
  return code(`${where}${what}`.trim() || file)
}

const CONSTRAINTS = [
  ['minimum', 'min'], ['maximum', 'max'], ['minLength', 'min length'], ['maxLength', 'max length'],
  ['minItems', 'min items'], ['maxItems', 'max items'], ['maxProperties', 'max members'], ['format', 'format']
]

function typeText(s, file) {
  if (s === true || s === undefined || (typeof s === 'object' && Object.keys(s).length === 0)) return 'any'
  if (s.$ref) return refText(s.$ref, file)
  if ('const' in s) {
    const v = json(s.const)
    return `const ${v.length > 60 ? '(see schema)' : literal(s.const)}`
  }
  if (s.enum) return `enum ${list(s.enum)}`
  for (const key of ['oneOf', 'anyOf']) {
    if (s[key]) return `${key === 'oneOf' ? 'one of' : 'any of'}: ${s[key].map((x) => typeText(x, file)).join(' / ')}`
  }
  if (s.allOf) return `all of: ${s.allOf.map((x) => typeText(x, file)).join(' / ')}`
  let base = Array.isArray(s.type) ? s.type.join(' or ') : (s.type ?? 'any')
  if (s.type === 'array') base = `array of ${typeText(s.items, file)}`
  const extras = CONSTRAINTS.filter(([key]) => s[key] !== undefined).map(([key, label]) => `${label} ${s[key]}`)
  if (s.pattern !== undefined) extras.push(`pattern ${code(s.pattern)}`)
  if (s.type === 'object' && s.properties === undefined && s.additionalProperties && typeof s.additionalProperties === 'object') {
    base = `object of ${typeText(s.additionalProperties, file)}`
  }
  return extras.length ? `${base} (${extras.join(', ')})` : base
}

function describe(s) {
  const parts = []
  if (s.description) parts.push(s.description)
  if ('default' in s) parts.push(`Default: ${code(json(s.default))}.`)
  return parts.join(' ')
}

/** The members of an object schema as table rows, an inline object or array of objects flattened. */
function memberRows(s, file, prefix = '', depth = 0, defaults = {}) {
  const rows = []
  const required = new Set(s.required ?? [])
  for (const [name, p] of Object.entries(s.properties ?? {})) {
    const key = `${prefix}${name}`
    const extra = key in defaults ? `${describe(p)} Default: ${code(json(defaults[key]))}.`.trim() : describe(p)
    rows.push([code(key), required.has(name) ? 'yes' : '', typeText(p, file), extra])
    if (depth < 2 && p && typeof p === 'object' && !p.$ref) {
      if (p.type === 'object' && p.properties) rows.push(...memberRows(p, file, `${key}.`, depth + 1))
      else if (p.type === 'array' && p.items && typeof p.items === 'object' && !p.items.$ref && p.items.properties) {
        rows.push(...memberRows(p.items, file, `${key}[].`, depth + 1))
      }
    }
  }
  return rows
}

const closedness = (s) => (s.additionalProperties === false ? 'Closed: a member the schema does not name is refused.' : 'Open: a member the schema does not name is accepted.')

/** One schema node as a section: its description, closed or open, its members, its conditions. */
function nodeSection(heading, s, file, defaults = {}) {
  const out = [heading, '']
  if (s.description) out.push(s.description, '')
  if (s.type === 'object' || s.properties) {
    out.push(closedness(s), '')
    const rows = memberRows(s, file, '', 0, defaults)
    if (rows.length) out.push(table(['Member', 'Required', 'Type', 'Description'], rows), '')
    else out.push(`Type: ${typeText(s, file)}`, '')
  } else {
    out.push(`Type: ${typeText(s, file)}`, '')
  }
  if (s.allOf || s.if || s.dependentRequired || s.anyOf) {
    const named = ['allOf', 'if', 'dependentRequired', 'anyOf'].filter((k) => s[k] !== undefined)
    if (s.type === 'object' || s.properties) out.push(`Conditions beyond the members (${named.join(', ')}) are in the schema file.`, '')
  }
  return out.join('\n')
}

// ---------------------------------------------------------------------------------------------
// The package

class Package {
  constructor(server) {
    this.server = server
    this.cache = new Map()
  }
  async module(name) {
    if (!this.cache.has(name)) {
      const path = join(harness, 'dist', `${name}.js`)
      if (!existsSync(path)) throw new Error(`packages/harness/dist/${name}.js is missing: run npm run build first`)
      this.cache.set(name, await this.server.ssrLoadModule(path))
    }
    return this.cache.get(name)
  }
}

/** The comment above `export const NAME` in the source, its first paragraph on one line. */
function documentation(moduleName, name) {
  const path = join(harness, `${moduleName}.ts`)
  if (!existsSync(path)) return ''
  const lines = readFileSync(path, 'utf8').split('\n')
  const at = lines.findIndex((l) => l.startsWith(`export const ${name}`))
  if (at < 1) return ''
  let end = at - 1
  if (!lines[end].trimEnd().endsWith('*/')) return ''
  let start = end
  while (start >= 0 && !lines[start].includes('/**')) start--
  if (start < 0) return ''
  const text = lines.slice(start, end + 1).join('\n').replace(/^\s*\/\*\*/, '').replace(/\*\/\s*$/, '')
  const body = text.split('\n').map((l) => l.replace(/^\s*\* ?/, ''))
  const paragraph = []
  for (const l of body) {
    if (l.trim() === '') {
      if (paragraph.length) break
      continue
    }
    paragraph.push(l.trim())
  }
  return paragraph.join(' ').replace(/\*\*/g, '').replace(/\s+/g, ' ')
}

// ---------------------------------------------------------------------------------------------
// One page

class Page {
  constructor(file, title, schemas, pkg) {
    this.file = file
    this.title = title
    this.schemas = schemas
    this.pkg = pkg
    this.schemaUsed = []
    this.dataUsed = []
    this.symbols = new Map()
    this.body = []
  }
  /** A schema file, noted as a source. */
  schema(file) {
    if (!this.schemaUsed.includes(file)) this.schemaUsed.push(file)
    return this.schemas.read(file)
  }
  def(file, name) {
    this.schema(file)
    return this.schemas.def(file, name)
  }
  /** A data file of the package, read as JSON and noted as a source. */
  data(path) {
    if (!this.dataUsed.includes(path)) this.dataUsed.push(path)
    return JSON.parse(readFileSync(join(harness, path), 'utf8'))
  }
  note(moduleName, name) {
    const symbols = this.symbols.get(moduleName) ?? []
    if (!symbols.includes(name)) symbols.push(name)
    this.symbols.set(moduleName, symbols)
  }
  /** An exported constant of the package; a missing one stops the run. */
  async use(moduleName, name) {
    const mod = await this.pkg.module(moduleName)
    if (mod[name] === undefined) throw new Error(`${this.file}: packages/harness/${moduleName}.ts does not export ${name}`)
    this.note(moduleName, name)
    return mod[name]
  }
  /** A header or marker constant: listed by name only, its value never read into the page. */
  async nameOnly(moduleName, name) {
    await this.use(moduleName, name)
    return { module: moduleName, name }
  }
  /** The name of the exported constant that holds a value, in the module that exports it. */
  async nameOf(moduleName, value) {
    const mod = await this.pkg.module(moduleName)
    const found = Object.keys(mod).filter((k) => mod[k] === value)
    if (found.length !== 1) throw new Error(`${this.file}: no single export of packages/harness/${moduleName}.ts holds the value looked up`)
    this.note(moduleName, found[0])
    return found[0]
  }
  doc(moduleName, name) {
    return documentation(moduleName, name)
  }
  add(...blocks) {
    this.body.push(...blocks)
  }
  nodeOf(file, name, heading) {
    return nodeSection(heading ?? `### ${code(`${file} $defs/${name}`)}`, this.def(file, name), file)
  }
  rootOf(file, heading, defaults) {
    return nodeSection(heading, this.schema(file), file, defaults)
  }
  render() {
    const out = [`# ${this.title}`, '', '## Sources', '']
    if (this.schemaUsed.length) {
      out.push('Schemas:', '', ...this.schemaUsed.map((f) => `- ${code(`contract/schemas/${f}`)}`), '')
    }
    if (this.symbols.size || this.dataUsed.length) {
      out.push('Package files (`packages/harness`), with the symbols read:', '')
      for (const [moduleName, names] of this.symbols) out.push(`- ${code(`${moduleName}.ts`)}: ${names.map((n) => code(n)).join(', ')}`)
      for (const path of this.dataUsed) out.push(`- ${code(path)}`)
      out.push('')
    }
    out.push('> generated; edit the sources, then run `node scripts/gen-reference.mjs`. `--check` fails where this page differs from them.', '')
    out.push(...this.body.map((b) => b.replace(/\n+$/, '')).flatMap((b) => [b, '']))
    return `${out.join('\n').replace(/\n{3,}/g, '\n\n').replace(/\n+$/, '')}\n`
  }
}

function agree(page, label, fromSchema, fromCode) {
  const a = [...fromSchema].sort()
  const b = [...fromCode].sort()
  if (a.length !== b.length || a.some((v, i) => v !== b[i])) {
    throw new Error(`${page.file}: ${label} differ between the schema (${a.join(', ')}) and the package (${b.join(', ')})`)
  }
}

function contained(page, label, inner, outer) {
  const missing = [...inner].filter((v) => !outer.has(v))
  if (missing.length) throw new Error(`${page.file}: ${label}: ${missing.join(', ')} not held by the package`)
}

const seeAlso = (...pages) => `See also: ${pages.map((p) => `[${p}](${p})`).join(', ')}.`

// ---------------------------------------------------------------------------------------------
// The pages

async function toolsPage(page) {
  const ASSISTANT_TOOLS = await page.use('host', 'ASSISTANT_TOOLS')
  const REFUSED = await page.use('toolGate', 'REFUSED_RUNTIME_TOOLS')
  const TEST_TOOL = await page.use('toolGate', 'TEST_TOOL')
  const REHEARSAL_TOOL = await page.use('toolGate', 'REHEARSAL_TOOL')
  const REHEARSAL_MEMBER = await page.use('toolGate', 'REHEARSAL_MEMBER')
  const TEST_ARGUMENTS = await page.use('toolGate', 'TEST_ARGUMENTS')
  const HELD_NAMES = await page.use('toolSources', 'HELD_NAMES')
  const LOAD_TOOL = await page.use('toolSources', 'LOAD_TOOL')
  const DECLARED_SOURCE_KINDS = await page.use('toolSources', 'DECLARED_SOURCE_KINDS')
  const TRUST_CLASSES = await page.use('toolSources', 'TRUST_CLASSES')
  const EFFECT_CLASSES = await page.use('toolSources', 'EFFECT_CLASSES')
  const APPROVAL_DEFAULT = await page.use('toolSources', 'APPROVAL_DEFAULT')
  const SOURCE_ID = await page.use('toolSources', 'SOURCE_ID')
  const SNAPSHOT_BYTES = await page.use('descriptorSnapshot', 'DESCRIPTOR_SNAPSHOT_BYTES')
  const NAME_BYTES = await page.use('toolSources', 'NAME_BYTES')
  const PROPOSE_PACK_TOOL = await page.use('engines/contract', 'PROPOSE_PACK_TOOL')
  const DELEGATE_TOOL = await page.use('delegationContract', 'DELEGATE_TOOL')
  const TASK_PROFILES = await page.use('delegationContract', 'TASK_PROFILES')
  const READ_TOOL = await page.use('context/reader', 'READ_TOOL')
  const SEARCH_TOOL = await page.use('context/search', 'SEARCH_TOOL')
  const LOAD_SKILL = await page.use('skills', 'LOAD_SKILL')
  const UNLOAD_SKILL = await page.use('skills', 'UNLOAD_SKILL')
  const CRITIC_TOOLS = await page.use('refutation', 'CRITIC_TOOLS')
  const CRITIC_READERS = await page.use('refutation', 'CRITIC_READERS')
  const PLAN_RUNTIME_READS = await page.use('planning/policy', 'PLAN_RUNTIME_READS')

  const ceiling = page.def('tool-offer.json', 'runtimeCeiling')
  const reserved = page.def('tool-offer.json', 'reservedName')
  const criticTools = page.def('tool-offer.json', 'criticTools')
  const effectClass = page.def('tool-offer.json', 'effectClass')
  const approvalPolicy = page.def('tool-offer.json', 'approvalPolicy')
  const sourceRecord = page.def('tool-offer.json', 'sourceRecord')
  agree(page, 'the runtime ceiling', ceiling.enum, ASSISTANT_TOOLS)
  agree(page, 'the critic’s tools', criticTools.enum, [...CRITIC_TOOLS, ...CRITIC_READERS])
  contained(page, 'the reserved context names', reserved.enum, HELD_NAMES)
  agree(page, 'the effect classes', effectClass.enum, EFFECT_CLASSES)
  agree(page, 'the trust classes', sourceRecord.properties.trust.enum, TRUST_CLASSES)
  agree(page, 'the source kinds', sourceRecord.properties.kind.enum, ['runtime', 'exchange', ...DECLARED_SOURCE_KINDS])

  page.add(
    'The Runtime tools a run may be offered, per profile; the names the harness reserves; a host tool’s declaration; effect classes, approval and tool sources.',
    seeAlso('skills.md', 'delegation.md', 'critic.md', 'budget.md', 'reasons.md')
  )

  page.add('## Runtime tools', '', nodeSection('### The ceiling', ceiling, 'tool-offer.json').replace(/\nType: .*\n/, '\n'))
  page.add(
    table(['Runtime tool', 'Gate rule named in the package'], ASSISTANT_TOOLS.map((name) => [code(name), name === REHEARSAL_TOOL ? `held to the rehearsal (${code(REHEARSAL_MEMBER)} member)` : name === TEST_TOOL ? `arguments limited to ${TEST_ARGUMENTS.map((a) => code(a)).join(', ')}` : ''])),
    'The Runtime tools that stay off the ceiling, with the reason the gate’s source states for each:',
    table(['Runtime tool', 'Why it stays off the ceiling'], Object.entries(REFUSED).map(([name, reason]) => [code(name), reason]))
  )

  const profiles = [
    ['session', 'a subset of the ceiling, the host’s allow-list', ASSISTANT_TOOLS.map((n) => code(n)).join(', ')],
    ['plan mode', `the ceiling without ${ASSISTANT_TOOLS.filter((n) => !PLAN_RUNTIME_READS.includes(n)).map((n) => code(n)).join(', ')}`, PLAN_RUNTIME_READS.map((n) => code(n)).join(', ')],
    ['critic', 'its own allow-list', CRITIC_TOOLS.map((n) => code(n)).join(', ')],
    ...TASK_PROFILES.map((p) => [`delegated child: ${p.id}`, 'the profile’s runtimeTools', p.runtimeTools?.length ? p.runtimeTools.map((n) => code(n)).join(', ') : 'none'])
  ]
  page.add('### Runtime tools per profile', '', table(['Profile', 'Rule', 'Runtime tools offered'], profiles.map(([a, b, c]) => [a, b, c])))

  page.add(...['propose_edit','propose_delete','propose_insert','undo_edit','draftId'].map(name=>page.nodeOf('tool-offer.json',name)),page.nodeOf('provenance.json','edit'),page.nodeOf('provenance.json','edits'))

  const harnessTools = [
    [PROPOSE_PACK_TOOL, 'PROPOSE_PACK_TOOL', 'engines/contract', 'tool-offer.json $defs/proposePack'],
    [LOAD_TOOL, 'LOAD_TOOL', 'toolSources', 'tool-offer.json $defs/loadTool'],
    [LOAD_SKILL, 'LOAD_SKILL', 'skills', 'tool-offer.json $defs/loadSkill'],
    [UNLOAD_SKILL, 'UNLOAD_SKILL', 'skills', 'tool-offer.json $defs/unloadSkill'],
    [DELEGATE_TOOL, 'DELEGATE_TOOL', 'delegationContract', 'delegated-task.json $defs/request'],
    [READ_TOOL, 'READ_TOOL', 'context/reader', 'tool-result.json $defs/readArguments'],
    [SEARCH_TOOL, 'SEARCH_TOOL', 'context/search', 'tool-result.json $defs/searchArguments']
  ]
  page.add(
    '## Reserved names and harness tools',
    '',
    nodeSection('### Context tool names', reserved, 'tool-offer.json').replace(/\nType: .*\n/, '\n'),
    `The package also holds a wider set, \`HELD_NAMES\`, that a tool with a source may not bear: the ceiling, the Runtime tools that stay off it, the context names above and ${code(PROPOSE_PACK_TOOL)}.`,
    table(['Held name', 'Origin'], [...HELD_NAMES].map((name) => [
      code(name),
      ASSISTANT_TOOLS.includes(name) ? 'Runtime tool on the ceiling' : Object.hasOwn(REFUSED, name) ? 'Runtime tool off the ceiling' : reserved.enum.includes(name) ? 'context tool name' : name === PROPOSE_PACK_TOOL ? 'harness host tool' : ''
    ])),
    'The tools the harness itself serves:',
    table(['Tool', 'Constant', 'File', 'Schema'], harnessTools.map(([value, name, file, where]) => [code(value), code(name), code(`${file}.ts`), code(where)]))
  )

  page.add(
    '## Host tools',
    '',
    nodeSection('### A host tool’s declaration', page.def('tool-offer.json', 'hostTool'), 'tool-offer.json')
  )

  page.add(
    '## Effect classes and approval',
    '',
    nodeSection('### Effect class of a tool with a source', effectClass, 'tool-offer.json').replace(/\nType: .*\n/, '\n'),
    table(['Effect class', 'Default approval'], EFFECT_CLASSES.map((c) => [code(c), code(APPROVAL_DEFAULT[c])])),
    nodeSection('### The approval policy’s answers', approvalPolicy, 'tool-offer.json').replace(/\nType: .*\n/, '\n')
  )

  page.add(
    '## Tool sources',
    '',
    `Kinds a host declares: ${DECLARED_SOURCE_KINDS.map((k) => code(k)).join(', ')}; the record also carries ${code('runtime')} (the Runtime's own row) and ${code('exchange')} (a skill from a Judgment Exchange entry, see [skills.md](skills.md)). Trust classes: ${TRUST_CLASSES.map((k) => code(k)).join(', ')}. A source's id matches ${code(SOURCE_ID.source)}; a wire name is at most ${NAME_BYTES} characters.`,
    nodeSection('### The source record', sourceRecord, 'tool-offer.json'),
    '### Gateway descriptor snapshot pins',
    '',
    `New MCP registrations supply approved raw UTF-8 descriptorSnapshot bytes (Go DescriptorSnapshot), their complete sha256 pin as toolListDigest, and required currentDescriptorSnapshot (Go CurrentDescriptorSnapshot). readDescriptorSnapshot and descriptorSnapshotDigest are public helpers. A snapshot is at most ${SNAPSHOT_BYTES} bytes and must match Gateway SPEC 1.1 canonical bytes: Unicode code-point key order, strict UTF-8, no lone surrogate, duplicate member, noninteger numeric spelling or trailing text. Agent record canonicalization is unchanged. inputSchemaText stays opaque original text in the pin.`,
    '',
    'Only host-declared captured tool names are admitted; an uncaptured name or wrong digest withholds that source whole. Offered descriptors derive from captured candidates; absent schema projects open while the original host schema is independently checked in the package-supported subset. The host executor retains every other validation and authorization rule. Snapshot platform/server metadata does not replace the local source id or execution identity.',
    '',
    'The observer reports current server descriptor content under the approved metadata before ordinary calls, approval decisions and approved continuations. Binding, endpoint, capturedAt, policy and server identity are pinned: a fresh capture timestamp requires re-approval. Agent does not independently verify the host observer. Observers may overlap and run outside observation locks; they must not recursively invoke source tools. A changed, malformed or failed observation latches refusal for the source within the run; an older overlapping observation cannot clear it.',
    '',
    'Absent descriptorSnapshot explicitly selects legacy list-pin behavior with toolListDigest/ToolListDigest and currentDigest/CurrentDigest. Snapshot mode cannot also supply the legacy observer; malformed supplied snapshots never fall back. Migrate through a newly person-approved snapshot declaration, and re-supply its observer on resume. Historical checkpoint and event bytes stay unchanged; this change adds no persisted record member. See ADR-0006 and contract/vectors/tool-offer/descriptor-snapshot.json.'
  )
}

async function skillsPage(page) {
  const SKILL_SOURCE_KINDS = await page.use('skills', 'SKILL_SOURCE_KINDS')
  const LOAD_SKILL = await page.use('skills', 'LOAD_SKILL')
  const UNLOAD_SKILL = await page.use('skills', 'UNLOAD_SKILL')
  const SKILL_NAME = await page.use('skills', 'SKILL_NAME')
  const SKILL_LINE_BYTES = await page.use('skills', 'SKILL_LINE_BYTES')
  const SKILL_DEFAULTS = await page.use('context/policy', 'SKILL_DEFAULTS')
  const markers = []
  for (const name of ['SKILLS_HEADER', 'SKILLS_END', 'SKILL_INSTRUCTIONS_HEADER', 'SKILL_INSTRUCTIONS_END', 'SKILL_OPENING', 'SKILL_CLOSING']) {
    markers.push(await page.nameOnly('skills', name))
  }
  page.schema('skill.json')
  const sourceKinds = page.def('tool-offer.json', 'sourceRecord').properties.kind.enum
  agree(page, 'the skill source kinds', sourceKinds.filter((k) => k !== 'runtime'), SKILL_SOURCE_KINDS)

  page.add(
    'The skill record, how a host declares one and how the run offers it, the skill bounds and the names of the headers and markers.',
    seeAlso('tools.md', 'budget.md', 'reasons.md', 'events.md', 'checkpoint.md', 'critic.md'),
    page.rootOf('skill.json', '## A skill'),
    ...['name', 'resource', 'digest', 'bytes'].map((n) => page.nodeOf('skill.json', n)),
    '## The offer',
    page.nodeOf('tool-offer.json', 'skillDeclaration', '### A skill as a host declares it'),
    page.nodeOf('tool-offer.json', 'skillCatalogueEntry', '### A catalogue entry'),
    page.nodeOf('tool-offer.json', 'loadSkill', `### The tool ${code(LOAD_SKILL)}`),
    page.nodeOf('tool-offer.json', 'unloadSkill', `### The tool ${code(UNLOAD_SKILL)}`),
    page.nodeOf('tool-offer.json', 'loadedSkill', '### A loaded skill as a load names it'),
    page.nodeOf('provenance.json', 'skillLoad', '### A skill in the run report'),
    '## Constants',
    '',
    table(['Constant', 'Value'], [
      [code('SKILL_SOURCE_KINDS'), SKILL_SOURCE_KINDS.map((k) => code(k)).join(', ')],
      [code('SKILL_NAME'), code(SKILL_NAME.source)],
      [code('SKILL_LINE_BYTES'), SKILL_LINE_BYTES]
    ]),
    '### Skill bounds and their defaults',
    '',
    `These are members of the context budget; see [budget.md](budget.md).`,
    table(['Member', 'Default'], Object.entries(SKILL_DEFAULTS).map(([k, v]) => [code(k), v])),
    '### Headers and markers',
    '',
    'Named here, never their text. Each is a constant of `skills.ts`.',
    table(['Constant'], markers.map((m) => [code(m.name)]))
  )
}

async function delegationPage(page) {
  const TASK_PROFILES = await page.use('delegationContract', 'TASK_PROFILES')
  const DEFAULT_TASK_BUDGET = await page.use('delegationContract', 'DEFAULT_TASK_BUDGET')
  const TASK_BUDGET_CEILING = await page.use('delegationContract', 'TASK_BUDGET_CEILING')
  const REPORT_KINDS = await page.use('delegationContract', 'REPORT_KINDS')
  const REPORT_BOUNDS = await page.use('delegationContract', 'REPORT_BOUNDS')
  const limits = {}
  for (const name of ['MAX_TOOLS', 'MAX_BATCHES', 'MAX_AGENT_RUNS', 'MAX_REFERENCES', 'CHILD_OPEN_QUESTIONS']) {
    limits[name] = await page.use('delegationContract', name)
  }
  const RESULT_LIMIT = await page.use('delegation', 'RESULT_LIMIT')
  const DEFAULT_CHILD_CONCURRENCY = await page.use('delegation', 'DEFAULT_CHILD_CONCURRENCY')
  const DELEGATE_TOOL = await page.use('delegationContract', 'DELEGATE_TOOL')
  const TOKEN_MEMBERS = await page.use('delegationContract', 'TOKEN_MEMBERS')

  const reportKinds = ['analyze', 'review', 'research', 'author-test']
  agree(page, 'the report kinds', reportKinds, REPORT_KINDS)
  page.schema('delegated-task.json')

  page.add(
    `The built-in task profiles and their offers, the limits on ${code(DELEGATE_TOOL)}, the task and report records and the accounting of an agent capability.`,
    seeAlso('tools.md', 'budget.md', 'events.md', 'reasons.md', 'checkpoint.md'),
    page.rootOf('subagent.json', '## Declared sub-agents'),
    page.nodeOf('tool-offer.json', 'subagentCatalogueEntry'),
    'Declared definitions use models.subagent.<id>, reads only, narrowing bounds and a pinned canonical digest. The catalogue is bounded by cataloguedSubagents (16) and subagentCatalogueBytes (4000). Built-ins remain fixed and are never declarations.',
    '## Built-in profiles',
    '',
    table(['Profile', 'Capabilities', 'Runtime tools', 'Model', 'Model requests', 'Tool calls', 'Milliseconds', 'Report', 'Description'], TASK_PROFILES.map((p) => [
      code(p.id),
      p.capabilities?.length ? p.capabilities.map((c) => code(c)).join(', ') : 'none',
      p.runtimeTools?.length ? p.runtimeTools.map((c) => code(c)).join(', ') : 'none',
      code(p.model),
      p.budget?.modelRequests ?? DEFAULT_TASK_BUDGET.modelRequests,
      p.budget?.toolCalls ?? DEFAULT_TASK_BUDGET.toolCalls,
      p.budget?.milliseconds ?? DEFAULT_TASK_BUDGET.milliseconds,
      code(p.report),
      p.description
    ])),
    'A profile that declares no budget takes the default; no profile may declare more than the ceiling, member by member, and its Runtime tools stay on the gate’s ceiling ([tools.md](tools.md)).',
    table(['Budget', 'Model requests', 'Tool calls', 'Milliseconds'], [
      ['default', DEFAULT_TASK_BUDGET.modelRequests, DEFAULT_TASK_BUDGET.toolCalls, DEFAULT_TASK_BUDGET.milliseconds],
      ['ceiling', TASK_BUDGET_CEILING.modelRequests, TASK_BUDGET_CEILING.toolCalls, TASK_BUDGET_CEILING.milliseconds]
    ]),
    '## Limits',
    '',
    table(['Constant', 'Value', 'Source comment'], [
      ...Object.entries(limits).map(([n, v]) => [code(n), v, page.doc('delegationContract', n)]),
      [code('RESULT_LIMIT'), RESULT_LIMIT, page.doc('delegation', 'RESULT_LIMIT')],
      [code('DEFAULT_CHILD_CONCURRENCY'), DEFAULT_CHILD_CONCURRENCY, page.doc('delegation', 'DEFAULT_CHILD_CONCURRENCY')]
    ]),
    'Bounds on one report:',
    table(['Member', 'Bound'], Object.entries(REPORT_BOUNDS).map(([k, v]) => [code(k), v])),
    '## The task and its report',
    ''
  )
  for (const name of Object.keys(page.schemas.read('delegated-task.json').$defs)) {
    page.add(page.nodeOf('delegated-task.json', name))
  }
  page.add(
    '## The agent capability’s accounting',
    '',
    page.schema('checkpoint.json').allOf.map((c) => c.description).filter(Boolean).join('\n\n'),
    `One response may start at most ${limits.MAX_AGENT_RUNS} agent runs on an agent capability, the parent's and the children's together (${code('MAX_AGENT_RUNS')}). Token figures count ${TOKEN_MEMBERS.map((m) => code(m)).join(', ')}.`
  )
  for (const name of ['tokenFigures', 'childFigures', 'figuresTotal', 'batchFigures', 'parentFigures', 'delegationReport']) {
    page.add(page.nodeOf('provenance.json', name))
  }
  page.add(page.nodeOf('checkpoint.json', 'handoff'))
}

async function criticPage(page) {
  const CRITIC_TOOLS = await page.use('refutation', 'CRITIC_TOOLS')
  const CRITIC_READERS = await page.use('refutation', 'CRITIC_READERS')
  const MAX_CRITIC_TURNS = await page.use('refutation', 'MAX_CRITIC_TURNS')
  const CRITIC_REHEARSALS = await page.use('refutation', 'CRITIC_REHEARSALS')
  const SETTLED_STATUS = await page.use('refutation', 'SETTLED_STATUS')
  const CRITIC_PASS_MILLISECONDS = await page.use('engines/codex/index', 'CRITIC_PASS_MILLISECONDS')
  const marker = await page.nameOnly('refutation', 'REFUTATION_MARKER')
  const TEST_DEFAULTS = await page.use('context/policy', 'TEST_DEFAULTS')
  const budget = page.schema('budget.json')
  const offer = page.schema('tool-offer.json')
  const criticTools = page.def('tool-offer.json', 'criticTools')
  page.schema('run-event.json')
  const CONTEXT_DEFAULTS = await page.use('context/policy', 'CONTEXT_DEFAULTS')
  // ADR-0010: the switch, from the schemas and the package's constants (criticSwitch.ts).
  const CRITIC_MODES = await page.use('criticSwitch', 'CRITIC_MODES')
  const CRITIC_REASONS = await page.use('criticSwitch', 'CRITIC_REASONS')
  const CRITIC_TRIGGERS = await page.use('criticSwitch', 'CRITIC_TRIGGERS')
  const MODEL_TIERS = await page.use('criticSwitch', 'MODEL_TIERS')
  const AGENT_TIERS = await page.use('criticSwitch', 'AGENT_TIERS')
  const REQUEST_REVIEW = await page.use('criticSwitch', 'REQUEST_REVIEW')
  const modes = page.def('tool-offer.json', 'criticMode')
  const tiers = page.def('tool-offer.json', 'criticReasoning')
  const reasons = page.def('provenance.json', 'criticReason')
  const decision = page.def('provenance.json', 'criticDecision')
  const requestReview = page.def('tool-offer.json', 'requestReview')
  agree(page, 'the modes', modes.enum, CRITIC_MODES)
  agree(page, 'the tiers', tiers.enum, [...new Set([...MODEL_TIERS, ...AGENT_TIERS])])
  agree(page, 'the reasons', reasons.enum, CRITIC_REASONS)
  agree(page, 'the triggers', decision.properties.trigger.enum, CRITIC_TRIGGERS)
  agree(page, 'the request_review name', [requestReview.allOf[1].properties.name.const], [REQUEST_REVIEW])
  contained(page, 'the reserved names', [REQUEST_REVIEW], new Set(page.def('tool-offer.json', 'reservedName').enum))
  const sessionMembers = ['critic', 'criticProject', 'criticReasoning', 'existingPack', 'reviewerAssigned']
  const tableMembers = ['criticDefault', 'criticReasoningFloor']
  for (const name of sessionMembers) if (offer.properties[name] === undefined) throw new Error(`critic.md: tool-offer.json has no ${name} member`)
  for (const name of tableMembers) if (budget.properties[name] === undefined) throw new Error(`critic.md: budget.json has no ${name} member`)

  page.add(
    'The critic reviews a proposal before it is delivered. This page lists its switch, the tools it may call, its bounds and the records it produces.',
    seeAlso('tools.md', 'events.md', 'budget.md', 'reasons.md'),
    '## The switch',
    '',
    `The modes: ${list(modes.enum)}. The tiers: ${list(MODEL_TIERS)} on the model capability, ${list(AGENT_TIERS)} on the agent capability. The run's word, then the project's, then the context budget's default decides the mode (ADR-0010).`,
    table(['Where', 'Member', 'Type', 'Default', 'Description'], [
      ...sessionMembers.map((name) => ['session (`tool-offer.json`)', code(name), typeText(offer.properties[name], 'tool-offer.json'), '', offer.properties[name].description]),
      ...tableMembers.map((name) => ['context budget (`budget.json`)', code(name), typeText(budget.properties[name], 'budget.json'),
        CONTEXT_DEFAULTS[name] === undefined ? '' : code(json(CONTEXT_DEFAULTS[name])), budget.properties[name].description])
    ]),
    nodeSection('### The modes', modes, 'tool-offer.json'),
    nodeSection('### The tiers', tiers, 'tool-offer.json'),
    '## What the critic may call',
    '',
    nodeSection('### The critic’s allow-list', criticTools, 'tool-offer.json').replace(/\nType: .*\n/, '\n'),
    table(['Group', 'Names'], [
      ['Runtime reads (`CRITIC_TOOLS`)', CRITIC_TOOLS.map((n) => code(n)).join(', ')],
      ['Harness context readers (`CRITIC_READERS`)', CRITIC_READERS.map((n) => code(n)).join(', ')]
    ]),
    '## Bounds',
    '',
    table(['Bound', 'Value', 'Source comment'], [
      [code('MAX_CRITIC_TURNS'), MAX_CRITIC_TURNS, page.doc('refutation', 'MAX_CRITIC_TURNS')],
      [code('CRITIC_REHEARSALS'), CRITIC_REHEARSALS, page.doc('refutation', 'CRITIC_REHEARSALS')],
      [`${code('CRITIC_PASS_MILLISECONDS')} (the codex engine)`, CRITIC_PASS_MILLISECONDS, page.doc('engines/codex/index', 'CRITIC_PASS_MILLISECONDS')],
      [`${code('testCallsPerTurn')} (shared with the loop)`, TEST_DEFAULTS.testCallsPerTurn, 'The cap on `experimental_test_cases` calls in one turn; see [budget.md](budget.md).']
    ]),
    '## The Runtime’s settled words',
    '',
    'A check counts only when the Runtime answers with its settled word for the tool.',
    table(['Tool', 'Settled status'], Object.entries(SETTLED_STATUS).map(([k, v]) => [code(k), code(v)])),
    '## Reports and records',
    '',
    page.nodeOf('run-event.json', 'critique', '### The `critique` event'),
    page.nodeOf('run-event.json', 'criticCase', '### A declared case'),
    page.nodeOf('run-event.json', 'caseOutcome', '### An outcome'),
    page.nodeOf('run-event.json', 'caseComparison', '### A comparison'),
    page.nodeOf('provenance.json', 'criticFigures', '### What one critic pass cost'),
    '### The marker',
    '',
    `The critic's request is marked by the constant ${code(marker.name)} in \`refutation.ts\` (named here, not its text).`,
    '## Triggers',
    '',
    `Under ${code('auto')}, in the order read: ${list(CRITIC_TRIGGERS)}.`,
    '## The decision',
    '',
    page.nodeOf('provenance.json', 'criticDecision', '### The decision, the report\'s row'),
    page.nodeOf('provenance.json', 'criticReason', '### The reasons'),
    page.nodeOf('run-event.json', 'critic_decided', '### The `critic_decided` event'),
    page.nodeOf('tool-offer.json', 'requestReview', `### The tool ${code(REQUEST_REVIEW)}`)
  )
}

async function eventsPage(page) {
  const root = page.schema('run-event.json')
  const kinds = root.properties.type.enum
  if (!Array.isArray(root.oneOf) || root.oneOf.length !== kinds.length) throw new Error('events.md: run-event.json oneOf and the type enum differ in length')
  const rows = []
  const sections = []
  for (const entry of root.oneOf) {
    const ref = entry.$ref
    let schema
    let file
    let name
    let shown
    if (ref.startsWith('#/$defs/')) {
      name = ref.slice('#/$defs/'.length)
      file = 'run-event.json'
      schema = root.$defs[name]
      shown = `run-event.json $defs/${name}`
    } else {
      file = ref
      schema = page.schema(file)
      shown = file
    }
    const kind = schema.properties?.type?.const
    if (!kinds.includes(kind)) throw new Error(`events.md: ${shown} does not carry a kind named in the type enum`)
    rows.push([code(kind), code(shown), (schema.required ?? []).filter((r) => r !== 'type').map((r) => code(r)).join(', ')])
    sections.push(nodeSection(`### ${code(kind)}`, schema, file))
  }
  const seen = rows.map((r) => r[0].slice(1, -1))
  agree(page, 'the event kinds', kinds, seen)
  page.add(
    root.description,
    seeAlso('tools.md', 'delegation.md', 'critic.md', 'skills.md', 'budget.md', 'reasons.md', 'checkpoint.md'),
    `## The ${kinds.length} kinds`,
    '',
    table(['Kind', 'Defined in', 'Required beyond type'], rows),
    '## Every kind with its members',
    '',
    ...sections
  )
}

async function budgetPage(page) {
  const CONTEXT_DEFAULTS = await page.use('context/policy', 'CONTEXT_DEFAULTS')
  const HOST_PARTS = await page.use('prompt', 'HOST_PARTS')
  const budget = page.schema('budget.json')
  const members = Object.keys(budget.properties)
  const absent = Object.keys(CONTEXT_DEFAULTS).filter((k) => !members.includes(k))
  if (absent.length) throw new Error(`budget.md: the package has a default for ${absent.join(', ')} that budget.json does not name`)
  const hostPartNames = []
  for (const part of HOST_PARTS) {
    hostPartNames.push([part.name, part.layer, part.injection, part.bound, await page.nameOf('prompt', part.header), part.end ? 'its end line' : ''])
  }

  const others = [
    ['execution/state', ['REQUEST_INCREMENT', 'MAX_REQUEST_ALLOWANCE', 'MAX_CHECKPOINT_BYTES', 'MAX_PROMPT_DIGESTS']],
    ['execution/progress', ['REPEATED_WORK_LIMIT']],
    ['engines/contract', ['MAX_TURNS']],
    ['engines/vercel/retry', ['MODEL_RETRIES', 'RETRY_BACKOFF_MS', 'RETRY_WAIT_LIMIT_MS']],
    ['context/chat', ['INDEX_SHARE']],
    ['images', ['IMAGE_BYTES']]
  ]
  const otherRows = []
  for (const [moduleName, names] of others) {
    for (const name of names) {
      const value = await page.use(moduleName, name)
      otherRows.push([code(name), code(moduleName + '.ts'), Array.isArray(value) ? value.join(', ') : value, page.doc(moduleName, name)])
    }
  }

  page.add(
    'The context budget’s members with their defaults and what each bounds, the host’s parts of a request and the other bounds the package holds.',
    seeAlso('tools.md', 'skills.md', 'delegation.md', 'critic.md', 'checkpoint.md', 'reasons.md'),
    '## The table',
    '',
    budget.description,
    table(['Member', 'Required', 'Type', 'Default', 'What it bounds'], members.map((name) => {
      const p = budget.properties[name]
      const fromCode = name in CONTEXT_DEFAULTS ? code(json(CONTEXT_DEFAULTS[name])) : ('default' in p ? code(json(p.default)) : '')
      return [code(name), (budget.required ?? []).includes(name) ? 'yes' : '', typeText(p, 'budget.json'), fromCode, p.description ?? '']
    })),
    `${closedness(budget)} Defaults are the package's \`CONTEXT_DEFAULTS\`.`,
    '## Definitions',
    '',
    ...['modelBudget', 'configuration', 'requestAllowance', 'toolCallAllowance', 'budgetIncreaseAnswer', 'deskLimits', 'stateLocation', 'hostPart'].map((n) => page.nodeOf('budget.json', n)),
    '## The host’s three parts of every request',
    '',
    'In their layers’ order. The header and end line are named by their constants, never given.',
    table(['Part', 'Layer', 'Injection point', 'Bounded by', 'Header constant', 'End line'], hostPartNames.map(([name, layer, injection, bound, header, end]) => [code(name), layer, code(injection), code(bound), code(header), end])),
    '## Other bounds in the package',
    '',
    table(['Constant', 'File', 'Value', 'Source comment'], otherRows)
  )
}

async function reasonsPage(page) {
  const catalog = page.data('contract/catalog.json')
  const COMPLETION_REASONS = await page.use('completion', 'COMPLETION_REASONS')
  const RETRYABLE_REFUSAL_CLASSES = await page.use('failures', 'RETRYABLE_REFUSAL_CLASSES')
  const RETRYABLE_OVERLOAD_CODES = await page.use('failures', 'RETRYABLE_OVERLOAD_CODES')
  const questionFailureMessage = await page.use('questionFailure', 'questionFailureMessage')
  const guardrail = page.def('run-event.json', 'guardrail')
  const code23 = page.def('failure.json', 'code')
  page.schema('failure.json')
  page.schema('question.json')
  const formatFailure = page.def('question.json', 'formatFailure')
  agree(page, 'the failure classes', code23.enum, Object.keys(catalog.failures))
  contained(page, 'the recoveries the catalogue names', Object.values(catalog.failures).map((f) => f.recovery), new Set(page.def('failure.json', 'recovery').enum))

  page.add(
    'The guardrail’s actions and reasons, the failure classes with their recovery and message, the completion reasons and the question format failures.',
    seeAlso('tools.md', 'skills.md', 'events.md', 'budget.md', 'critic.md'),
    '## Guardrail',
    '',
    guardrail.description,
    table(['Member', 'Values'], [
      [code('action'), list(guardrail.properties.action.enum)],
      [code('reason'), list(guardrail.properties.reason.enum)]
    ]),
    '## Failure classes',
    '',
    page.def('failure.json', 'code').description,
    table(['Class', 'Recovery', 'Message', 'Retryable refusal'], Object.entries(catalog.failures).map(([name, f]) => [code(name), code(f.recovery), f.message, RETRYABLE_REFUSAL_CLASSES.includes(name) ? 'yes' : ''])),
    `Overload codes a refusal is retried on: ${RETRYABLE_OVERLOAD_CODES.map((c) => code(c)).join(', ')}.`,
    Object.keys(catalog.aliases).length
      ? ['Aliases a provider body may name, read as a class:', table(['Alias', 'Class'], Object.entries(catalog.aliases).map(([a, c]) => [code(a), code(c)]))].join('\n\n')
      : '',
    page.nodeOf('failure.json', 'source', '### Failure source'),
    page.nodeOf('failure.json', 'outcome', '### Failure outcome'),
    page.nodeOf('failure.json', 'recovery', '### Recovery'),
    page.nodeOf('failure.json', 'toolFailureContent', '### A failed tool’s content'),
    '## Completion reasons',
    '',
    page.def('provenance.json', 'completion').description ?? '',
    `Finish reasons: ${COMPLETION_REASONS.map((r) => code(r)).join(', ')}.`,
    '## Question format failures',
    '',
    formatFailure.description,
    table(['Code', 'Message'], formatFailure.properties.questionFailure.enum.map((c) => [code(c), questionFailureMessage(c) ?? '']))
  )
}

async function checkpointPage(page) {
  page.add(
    seeAlso('events.md', 'budget.md', 'delegation.md', 'skills.md', 'reasons.md'),
    page.rootOf('checkpoint.json', '## The checkpoint')
  )
  for (const name of Object.keys(page.schema('checkpoint.json').$defs)) {
    page.add(page.nodeOf('checkpoint.json', name))
  }
}

async function policyPage(page) {
  page.add(seeAlso('budget.md', 'critic.md', 'events.md', 'checkpoint.md'),
    page.rootOf('policy.json', '## The policy table'))
  for (const name of Object.keys(page.schema('policy.json').$defs).sort()) page.add(page.nodeOf('policy.json', name))
}

async function recordsPage(page) {
  page.add(
    seeAlso('policy.md', 'checkpoint.md', 'events.md', 'reasons.md'),
    'The host owns storage and deletion. The package writes self-describing records, refuses expired checkpoints and exports only records the host supplies; it has no store.',
    'ADR-0024: writers use checkpoint version 2 and event/policy version 1. Readers also accept checkpoint 1 and historical events/policy layers with no version. Newer versions and unknown structural members are refused by name. Historical reads and exports preserve the supplied version and lifecycle metadata; a resumed snapshot writes the current checkpoint version without extending retention.',
    'UnknownRecordMember keeps its member pointer and bounded message on the diagnostic error. Public refusal events use fixed input-independent detail, including startup before an output guard exists.',
    page.rootOf('export.json', '## The export manifest'),
    page.nodeOf('export.json', 'record'),
    page.nodeOf('provenance.json', 'recordDigest'),
    page.nodeOf('provenance.json', 'retainUntil'),
    page.nodeOf('run-event.json', 'export_written')
  )
}

async function providersPage(page) {
 const records = await page.use('capabilities','PROVIDER_CAPABILITIES')
 page.schema('capability.json')
 page.add('Every role selects a host connection through models.<role>.provider: same, other, or a connected ID. The package refuses an unavailable provider before any request. Cross-family failover is refused because prefix and cache do not carry.',
 '## Tier map', table(['Family','low','medium','high'], Object.values(records).map(r=>[r.family,...['low','medium','high'].map(t=>code(r.tiers[t]))])),
 '## Fallback tier map', table(['Family','low','medium','high'],Object.values(records).filter(r=>r.fallbackTiers).map(r=>[r.family,...['low','medium','high'].map(t=>code(r.fallbackTiers[t]))])),
 'The optional fallbackTiers capability member supplies Anthropic enabled budgets and Gemini levels. Connection overrides apply to this map on every fallback request. A different critic binding opens its slot using the selected connection, even at the author’s family and tier; the identical binding and tier retain learned refusals. Resume refuses a changed family before dispatch and retains saved crossProvider rows with continued: false in a stopped checkpoint.',
 '## Wire compatibility',table(['Family','Cache breakpoints','Thinking','Tool schemas narrowed','Images'],Object.values(records).map(r=>[r.family,r.cacheBreakpoints,r.reasoning.kind,r.toolSchemas==='gemini-subset',r.vision])),
 'The host may override a connection’s tiers and endpoint capabilities. Model loops retain off/on/ultra as their existing vocabulary; on uses medium and ultra uses high. Agent capability tiers are passed by their host binding. The matrix describes the default model wires, not the properties of every model served behind them.')
}
async function memoryPage(page) {
 page.add('The host owns acceptance, storage, revision and revocation. The author proposes through remember, including Plan mode; children and the critic cannot propose. Recalled records are data, never instructions, at ADR-0005 layer 5. Bare text is refused in v0.9.1. Optional metadata-only lifecycle callbacks run even when prompt recall is disabled.',page.rootOf('memory.json','## Record'),page.nodeOf('memory.json','answer'),page.nodeOf('memory.json','lifecycleUpdate'),page.nodeOf('memory.json','lifecycleAnswer'),page.nodeOf('tool-offer.json','remember'),page.nodeOf('provenance.json','memories'),page.nodeOf('provenance.json','memoryRecall'), 'Bounds: memoryProposalBytes (1,000 UTF-8 text bytes), memoryProposalsPerRun (4 across resume), memoryBytes (4,000 canonical JSON UTF-8 bytes before framing). Each recall is framed under the run marker with kind and source. Revoked or expired memories are withheld; a returned revoked state updates the restored report.');
}
async function corpusPage(page) {
 page.add('ADR-0022 records a run only after the output guard redacts the complete entry. Replay answers from the recording and compares decisions and outcomes, never model prose or a live provider.',page.rootOf('corpus-entry.json','## Entry manifest'),page.nodeOf('run-event.json','recorded'),page.nodeOf('run-event.json','guardrail'))
}
const PAGES = [
 ['records.md','Records',recordsPage],
 ['corpus.md','The evaluation corpus',corpusPage],
 ['memory.md','Memory',memoryPage],
 ['providers.md','Providers',providersPage],
  ['policy.md', 'The policy table', policyPage],
  ['tools.md', 'Tools', toolsPage],
  ['skills.md', 'Skills', skillsPage],
  ['delegation.md', 'Delegation', delegationPage],
  ['critic.md', 'The critic', criticPage],
  ['events.md', 'Run events', eventsPage],
  ['budget.md', 'The context budget', budgetPage],
  ['reasons.md', 'Reasons', reasonsPage],
  ['checkpoint.md', 'The checkpoint', checkpointPage]
]

// ---------------------------------------------------------------------------------------------
// Generate, write, check

async function generate(options) {
  const { createServer } = await import('vite')
  const server = await createServer({
    root: harness,
    configFile: false,
    logLevel: 'silent',
    appType: 'custom',
    server: { middlewareMode: true, hmr: false, watch: null },
    optimizeDeps: { noDiscovery: true, include: [] }
  })
  try {
    const schemas = new Schemas(options.schemas)
    const pkg = new Package(server)
    const pages = new Map()
    for (const [file, title, build] of PAGES) {
      const page = new Page(file, title, schemas, pkg)
      await build(page)
      pages.set(file, page.render())
    }
    return pages
  } finally {
    await server.close()
  }
}

function write(dir, pages) {
  mkdirSync(dir, { recursive: true })
  for (const [file, text] of pages) writeFileSync(join(dir, file), text)
}

function diff(label, existing, generated) {
  const a = mkdtempSync(join(tmpdir(), 'gen-reference-diff-'))
  try {
    writeFileSync(join(a, 'a'), existing)
    writeFileSync(join(a, 'b'), generated)
    const result = spawnSync('diff', ['-u', '--label', `${label} (committed)`, '--label', `${label} (generated)`, join(a, 'a'), join(a, 'b')], { encoding: 'utf8' })
    if (result.error) return `${label} differs from the generated page`
    return result.stdout.split('\n').slice(0, 60).join('\n')
  } finally {
    rmSync(a, { recursive: true, force: true })
  }
}

async function main() {
  const options = parseArguments(process.argv.slice(2))
  const pages = await generate(options)
  if (!options.check) {
    write(options.out, pages)
    console.log(`reference: wrote ${pages.size} pages to docs/reference`)
    return 0
  }
  const scratch = mkdtempSync(join(tmpdir(), 'gen-reference-'))
  try {
    write(scratch, pages)
    const problems = []
    for (const [file, text] of pages) {
      const path = join(options.out, file)
      if (!existsSync(path)) problems.push(`${file} is missing from docs/reference`)
      else if (readFileSync(path, 'utf8') !== text) problems.push(diff(file, readFileSync(path, 'utf8'), text))
    }
    if (existsSync(options.out)) {
      for (const file of readdirSync(options.out)) if (file.endsWith('.md') && !pages.has(file)) problems.push(`${file} is in docs/reference but no source generates it`)
    }
    if (problems.length) {
      console.error(problems.join('\n\n'))
      console.error('\nreference: out of date; run node scripts/gen-reference.mjs and commit the pages')
      return 1
    }
    console.log(`reference: ${pages.size} pages match their sources`)
    return 0
  } finally {
    rmSync(scratch, { recursive: true, force: true })
  }
}

main().then(
  (status) => process.exit(status),
  (error) => {
    console.error(`reference: ${error.message}`)
    process.exit(1)
  }
)
