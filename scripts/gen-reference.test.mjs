// Tests for scripts/gen-reference.mjs: generation is deterministic, and `--check` fails when a schema
// changes without the regenerated pages. Run with `node --test scripts/gen-reference.test.mjs`
// after `npm run build` (the generator reads packages/harness/dist).
import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { cpSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { after, before, test } from 'node:test'
import { fileURLToPath } from 'node:url'

const repo = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const script = join(repo, 'scripts/gen-reference.mjs')
const schemasDir = join(repo, 'contract/schemas')
const PAGES = ['budget.md', 'checkpoint.md', 'corpus.md', 'critic.md', 'delegation.md', 'events.md', 'memory.md', 'policy.md', 'providers.md', 'reasons.md', 'records.md', 'skills.md', 'tools.md']

let scratch
const run = (...args) => spawnSync(process.execPath, [script, ...args], { cwd: repo, encoding: 'utf8', timeout: 240_000 })

before(() => {
  scratch = mkdtempSync(join(tmpdir(), 'gen-reference-test-'))
})
after(() => {
  rmSync(scratch, { recursive: true, force: true })
})

test('two runs write the same twelve pages, byte for byte', () => {
  const a = join(scratch, 'first')
  const b = join(scratch, 'second')
  for (const out of [a, b]) {
    const result = run('--out', out)
    assert.equal(result.status, 0, result.stderr)
  }
  assert.deepEqual(readdirSync(a).sort(), PAGES)
  assert.deepEqual(readdirSync(b).sort(), PAGES)
  for (const page of PAGES) {
    const first = readFileSync(join(a, page))
    assert.ok(first.length > 0, `${page} is empty`)
    assert.ok(first.equals(readFileSync(join(b, page))), `${page} differs between two runs`)
  }
})

test('--check passes on pages just written and fails after a schema enum value changes', () => {
  const out = join(scratch, 'first')
  const unchanged = run('--check', '--out', out)
  assert.equal(unchanged.status, 0, unchanged.stderr)

  const copy = join(scratch, 'schemas')
  cpSync(schemasDir, copy, { recursive: true })
  const path = join(copy, 'failure.json')
  const schema = JSON.parse(readFileSync(path, 'utf8'))
  const before = schema.$defs.source.enum[0]
  schema.$defs.source.enum[0] = `${before}-renamed`
  writeFileSync(path, `${JSON.stringify(schema, null, 2)}\n`)

  const changed = run('--check', '--schemas', copy, '--out', out)
  assert.equal(changed.status, 1, changed.stdout + changed.stderr)
  assert.match(changed.stderr, /reasons\.md/)
  assert.ok(changed.stderr.includes(`${before}-renamed`), 'the diff names the changed value')
  assert.match(changed.stderr, /out of date/)
})

test('--check fails when a page is missing or an unrelated page is present', () => {
  const out = join(scratch, 'trimmed')
  cpSync(join(scratch, 'first'), out, { recursive: true })
  rmSync(join(out, 'budget.md'))
  writeFileSync(join(out, 'extra.md'), '# extra\n')
  const result = run('--check', '--out', out)
  assert.equal(result.status, 1, result.stdout + result.stderr)
  assert.match(result.stderr, /budget\.md is missing/)
  assert.match(result.stderr, /extra\.md is in docs\/reference/)
})

test('a hand-edited provider matrix is rejected', () => {
 const out=join(scratch,'provider-drift')
 assert.equal(run('--out',out).status,0)
 const path=join(out,'providers.md')
 writeFileSync(path,readFileSync(path,'utf8').replace('anthropic','invented-family'))
 const checked=run('--check','--out',out)
 assert.notEqual(checked.status,0)
 assert.match(checked.stderr,/providers.md/)
})


test('the named run report narrative is preserved beside generated references', () => {
  const out = join(scratch, 'report-narrative')
  assert.equal(run('--out', out).status, 0)
  const path = join(out, 'run-report.md')
  const narrative = '# Run report\n\nHost-owned projection guidance.\n'
  writeFileSync(path, narrative)
  const checked = run('--check', '--out', out)
  assert.equal(checked.status, 0, checked.stderr)
  assert.equal(readFileSync(path, 'utf8'), narrative)
})
