#!/usr/bin/env node
/**
 * Break each safeguard on purpose, and say which test notices (agent#37).
 *
 *   npm run mutation --workspace @judgment-pack/harness -- [options]
 *
 * A passing test is not evidence that it tests anything. This applies one row of
 * `scripts/mutation-rows.json` at a time to a **scratch copy** of the package (the
 * working tree is never edited), runs the tests the row names, and records whether one
 * failed. A row that no test fails on is the interesting result: it names a safeguard
 * nothing holds in place, and it is reported as such and fails the run.
 *
 * Options:
 *
 *   --list               print the table (name, file, status, tests) and stop
 *   --check              only check that every row's `find` text occurs exactly once in its
 *                        file, which is what a row needs to apply; no test is run
 *   --row <text>         only the rows whose name contains the text (repeatable)
 *   --changed <git-ref>  only the rows whose file differs from the ref (`git diff --name-only`)
 *   --sample <n>         of the rows chosen, n of them, picked by --seed (default 0); the
 *                        same seed picks the same rows
 *   --shard <k>/<n>     every n-th row from the k-th (a scheduled run's shards)
 *   --whole              run the whole suite for every row instead of the row's tests
 *   --timeout <seconds>  the bound on one run (default 900)
 *   --verbose            name every test that failed, not the first
 *
 * **A row runs the tests that can notice it, and the whole suite decides what they
 * cannot.** The `tests` of a row are the test files that import the mutated file, or
 * import a module that does, and those that name the file in a string. Only a failing
 * test decides on that selection's word. A run in which nothing failed, one that timed
 * out, and one in which the mutated file did not load are each run again against the
 * whole suite, and that run decides. So the selection can only make a row faster, never
 * weaker: the table's `tests` are a starting point, not a claim.
 *
 * Exit 0 when every row chosen is caught (retired rows are listed, not run); 1 on a row
 * nothing caught, one that did not apply, or one that could not be decided.
 */
import { spawn, spawnSync } from 'node:child_process'
import { cpSync, existsSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const HERE = dirname(fileURLToPath(import.meta.url))
const PACKAGE = resolve(HERE, '..')
const REPOSITORY = resolve(PACKAGE, '..', '..')
const TABLE = join(HERE, 'mutation-rows.json')

function usage(message) {
  if (message) console.error(message)
  console.error('usage: mutation-check.mjs [--list] [--check] [--row <text>]... [--changed <git-ref>] [--sample <n>] [--seed <k>] [--shard <k>/<n>] [--whole] [--timeout <seconds>] [--verbose]')
  process.exit(2)
}

const options = { verbose: false, list: false, check: false, rows: [], changed: null, sample: null, seed: 0, shard: [1, 1], whole: false, timeout: 900 }
const argv = process.argv.slice(2)
for (let at = 0; at < argv.length; at++) {
  const flag = argv[at]
  const value = () => {
    const taken = argv[++at]
    if (taken === undefined) usage(`${flag} needs a value`)
    return taken
  }
  if (flag === '--list') options.list = true
  else if (flag === '--check') options.check = true
  else if (flag === '--whole') options.whole = true
  else if (flag === '--verbose') options.verbose = true
  else if (flag === '--row') options.rows.push(value())
  else if (flag === '--changed') options.changed = value()
  else if (flag === '--sample') options.sample = Number(value())
  else if (flag === '--seed') options.seed = Number(value())
  else if (flag === '--timeout') options.timeout = Number(value())
  else if (flag === '--shard') {
    const [k, n] = value().split('/').map(Number)
    if (!Number.isInteger(k) || !Number.isInteger(n) || k < 1 || k > n) usage('--shard takes k/n with 1 <= k <= n')
    options.shard = [k, n]
  } else usage(`unknown option ${flag}`)
}
if (options.sample !== null && !(Number.isInteger(options.sample) && options.sample > 0)) usage('--sample takes a positive integer')
if (!Number.isInteger(options.seed) || !(options.timeout > 0)) usage('--seed and --timeout take numbers')

const table = JSON.parse(readFileSync(TABLE, 'utf8'))
/** @type {{ name: string, file: string, find: string, replace: string, tests: string[], status: 'caught' | 'retired', reason?: string, origin: string, note?: string }[]} */
const all = table.rows
const names = new Set()
for (const row of all) {
  if (names.has(row.name)) usage(`the table names "${row.name}" twice`)
  names.add(row.name)
  if (row.status === 'retired' && !row.reason) usage(`the retired row "${row.name}" has no reason`)
  if (row.status !== 'retired' && row.status !== 'caught') usage(`the row "${row.name}" has the status ${row.status}`)
}

if (options.list) {
  for (const row of all) console.log(`${row.status.padEnd(7)} ${row.file}\t${row.name}${row.status === 'retired' ? `\t(${row.reason})` : `\t${row.tests.length} test files`}`)
  console.log(`${all.length} rows: ${all.filter((row) => row.status === 'caught').length} caught, ${all.filter((row) => row.status === 'retired').length} retired`)
  process.exit(0)
}

/** A small deterministic generator, so a sample's seed names its rows. */
function generator(seed) {
  let state = (seed + 0x9e3779b9) >>> 0
  return () => {
    state = (state + 0x6d2b79f5) >>> 0
    let t = state
    t = Math.imul(t ^ (t >>> 15), t | 1)
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61)
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}

let chosen = all.filter((row) => options.rows.length === 0 || options.rows.some((text) => row.name.includes(text)))
if (options.changed !== null) {
  const diff = spawnSync('git', ['diff', '--name-only', options.changed, '--', 'packages/harness'], { cwd: REPOSITORY, encoding: 'utf8' })
  if (diff.status !== 0) usage(`git diff against ${options.changed} failed: ${diff.stderr}`)
  const changed = new Set(diff.stdout.split('\n').filter(Boolean).map((path) => path.replace(/^packages\/harness\//, '')))
  chosen = chosen.filter((row) => changed.has(row.file))
}
if (options.sample !== null && chosen.length > options.sample) {
  const next = generator(options.seed)
  const keyed = chosen.map((row) => ({ row, key: next() }))
  keyed.sort((a, b) => a.key - b.key)
  const kept = new Set(keyed.slice(0, options.sample).map((entry) => entry.row.name))
  chosen = chosen.filter((row) => kept.has(row.name))
}
chosen = chosen.filter((_, at) => at % options.shard[1] === options.shard[0] - 1)

const retired = chosen.filter((row) => row.status === 'retired')
const runnable = chosen.filter((row) => row.status === 'caught')

/** How many times the row's find text occurs in the file as it stands. */
function occurrences(text, find) {
  if (find === '') return 0
  let count = 0
  for (let at = text.indexOf(find); at >= 0; at = text.indexOf(find, at + find.length)) count++
  return count
}

if (options.check) {
  let bad = 0
  for (const row of runnable) {
    const path = join(PACKAGE, row.file)
    const count = existsSync(path) ? occurrences(readFileSync(path, 'utf8'), row.find) : 0
    if (count !== 1) {
      bad++
      console.log(`DOES NOT APPLY (${count} matches in ${row.file}): ${row.name}`)
    }
    for (const test of row.tests) if (!existsSync(join(PACKAGE, test))) {
      bad++
      console.log(`NO SUCH TEST FILE ${test}: ${row.name}`)
    }
  }
  console.log(`${runnable.length} rows checked, ${bad} problems; ${retired.length} retired`)
  process.exit(bad === 0 ? 0 : 1)
}

if (runnable.length === 0) {
  console.log(`no row chosen to run (${retired.length} retired)`)
  process.exit(0)
}

// The scratch copy: the package, the contract it reads, and the installed packages by link.
const scratch = mkdtempSync(join(tmpdir(), 'mutation-'))
const copy = join(scratch, 'packages', 'harness')
cpSync(PACKAGE, copy, { recursive: true, filter: (from) => !/(^|[\\/])(node_modules|dist|\.vite|\.vite-temp)([\\/]|$)/.test(from.slice(PACKAGE.length)) })
cpSync(join(REPOSITORY, 'contract'), join(scratch, 'contract'), { recursive: true })
cpSync(join(REPOSITORY, 'package.json'), join(scratch, 'package.json'))
// The provider-matrix mutant changes a generated repository page. Its reference check reads
// the same built records and generator as the normal reference gate.
if(chosen.some(row=>row.file==='../../docs/reference/providers.md')) {
 cpSync(join(REPOSITORY,'docs/reference'),join(scratch,'docs/reference'),{recursive:true})
 cpSync(join(REPOSITORY,'scripts'),join(scratch,'scripts'),{recursive:true})
 cpSync(join(PACKAGE,'dist'),join(copy,'dist'),{recursive:true})
}
symlinkSync(join(REPOSITORY, 'node_modules'), join(scratch, 'node_modules'), 'dir')
process.on('exit', () => rmSync(scratch, { recursive: true, force: true }))
for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => process.exit(130))

const VITEST = join(REPOSITORY, 'node_modules', 'vitest', 'vitest.mjs')
if (!existsSync(VITEST)) usage('vitest is not installed: run npm ci first')

/**
 * One vitest run in the scratch copy: the files named, or the whole suite.
 * Resolves to the names of the tests that failed, [] where none did, or { undecided } where
 * the run timed out, ran nothing, or could not load the mutated file.
 */
function run(files, report) {
  return new Promise((done) => {
    const args = [VITEST, 'run', ...files, '--reporter=json', `--outputFile=${report}`]
    const child = spawn(process.execPath, args, { cwd: copy, detached: true, stdio: ['ignore', 'ignore', 'ignore'] })
    const timer = setTimeout(() => {
      try {
        process.kill(-child.pid, 'SIGKILL')
      } catch {
        /* already gone */
      }
      done({ undecided: `no answer inside ${options.timeout} s` })
    }, options.timeout * 1000)
    child.on('exit', () => {
      clearTimeout(timer)
      // Anything the run left behind goes with its group.
      try {
        process.kill(-child.pid, 'SIGKILL')
      } catch {
        /* nothing left */
      }
      let parsed
      try {
        parsed = JSON.parse(readFileSync(report, 'utf8'))
      } catch {
        return done({ undecided: 'the run wrote no report' })
      }
      const failed = []
      let ran = 0
      for (const file of parsed.testResults ?? []) {
        for (const test of file.assertionResults ?? []) {
          ran++
          if (test.status === 'failed') failed.push(`${file.name.replace(/^.*\/packages\/harness\//, '')} > ${test.fullName}`)
        }
      }
      if (failed.length > 0) return done(failed)
      done(ran === 0 ? { undecided: 'the run ran no test: the mutated file did not load' } : [])
    })
  })
}

const results = []
let reportNumber = 0
for (const row of runnable) {
  const path = join(copy, row.file)
  const original = readFileSync(path, 'utf8')
  const count = occurrences(original, row.find)
  if (count !== 1) {
    results.push({ row, verdict: 'DID NOT APPLY', detail: `${count} matches of the row's find text in ${row.file}` })
    continue
  }
  writeFileSync(path, original.replace(row.find, () => row.replace))
  try {
    const report = join(scratch, `report-${reportNumber++}.json`)
    let how = options.whole ? 'whole' : 'selected'
    let answer = options.whole || row.tests.length === 0 ? await run([], report) : await run(row.tests, report)
    if (!options.whole && row.tests.length > 0 && !(Array.isArray(answer) && answer.length > 0)) {
      how = 'whole, after its selection decided nothing'
      answer = await run([], join(scratch, `report-${reportNumber++}.json`))
    }
    if (Array.isArray(answer) && answer.length > 0) results.push({ row, verdict: 'caught', detail: options.verbose ? `${how}: ${answer.join('; ')}` : `${how}: ${answer[0]}${answer.length > 1 ? ` (+${answer.length - 1})` : ''}` })
    else if (Array.isArray(answer)) results.push({ row, verdict: 'NOT CAUGHT', detail: `${how}: no test failed` })
    else results.push({ row, verdict: 'UNDECIDED', detail: answer.undecided })
  } finally {
    writeFileSync(path, original)
  }
}

console.log('| row | file | result |')
console.log('| --- | --- | --- |')
for (const { row, verdict, detail } of results) console.log(`| ${row.name} | ${row.file} | ${verdict}: ${detail} |`)
for (const row of retired) console.log(`| ${row.name} | ${row.file} | retired: ${row.reason} |`)
const count = (verdict) => results.filter((result) => result.verdict === verdict).length
console.log(`\n${chosen.length} rows chosen: ${count('caught')} caught, ${count('NOT CAUGHT')} not caught, ${count('DID NOT APPLY')} did not apply, ${count('UNDECIDED')} undecided, ${retired.length} retired`)
process.exit(count('caught') === runnable.length ? 0 : 1)
