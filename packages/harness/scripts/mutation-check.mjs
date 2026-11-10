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
 *                        file, which is what a row needs to apply, and that the file still parses
 *                        with the row applied, as vitest's loader parses it; no test is run
 *   --row <text>         only the rows whose name contains the text (repeatable)
 *   --changed <git-ref>  only the rows whose file differs from the ref (`git diff --name-only`)
 *   --sample <n>         of the rows chosen, n of them, picked by --seed (default 0); the
 *                        same seed picks the same rows
 *   --shard <k>/<n>     every n-th row from the k-th (the full table's shards)
 *   --names              print the names of the rows chosen, one a line, and stop
 *   --no-mutant          run the whole suite in the scratch copy with no mutant, name what fails, and stop
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
 * **A failure counts only if the mutant caused it (agent#285).** The scratch copy holds the
 * repository's files, so the suite passes there as it does in the checkout. Whatever still
 * fails with no mutant counts for nothing: a test file that fails under a mutant is run once
 * with no mutant, and what fails then is taken away. A failure the whole suite alone found
 * must also fail again in a run of its own file under the mutant, so that load on the whole
 * suite cannot catch a row. The generated reference's check stays out of the whole suite's run,
 * so only a row that names it can be caught by it: its pages name source lines, so it fails for
 * any mutant that moves a line, whether or not anything holds the safeguard.
 *
 * **A mutant that stops a test file from loading is BROKEN, not NOT CAUGHT (agent#320).** A test
 * file that does not load runs no test, so nothing in it can fail: a row whose replacement no longer
 * fits its `find` (a stray parenthesis) would read as a safeguard nothing holds. Where no test
 * failed because of the mutant and a test file did not load under it, did load with no mutant, and
 * does not load again in a run of its own under the mutant, the row is BROKEN and names the load
 * error. A failure the mutant caused still makes the row caught.
 *
 * Exit 0 when every row chosen is caught (retired rows are listed, not run); 1 on a row
 * nothing caught, one that is broken, one that did not apply, or one that could not be decided.
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
  console.error('usage: mutation-check.mjs [--list] [--check] [--names] [--no-mutant] [--row <text>]... [--changed <git-ref>] [--sample <n>] [--seed <k>] [--shard <k>/<n>] [--whole] [--timeout <seconds>] [--verbose]')
  process.exit(2)
}

const options = { verbose: false, list: false, check: false, names: false, noMutant: false, rows: [], changed: null, sample: null, seed: 0, shard: [1, 1], whole: false, timeout: 900 }
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
  else if (flag === '--names') options.names = true
  else if (flag === '--no-mutant') options.noMutant = true
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

if (options.names) {
  for (const row of chosen) console.log(row.name)
  process.exit(0)
}

const retired = chosen.filter((row) => row.status === 'retired')
const runnable = chosen.filter((row) => row.status === 'caught')

/** How many times the row's find text occurs in the file as it stands. */
function occurrences(text, find) {
  if (find === '') return 0
  let count = 0
  for (let at = text.indexOf(find); at >= 0; at = text.indexOf(find, at + find.length)) count++
  return count
}

/**
 * Where a parse error is, from the error's own fields: `file:line:column` from its `loc` (the column
 * one-based, as the parser's code frame prints it), else its `id`, the file; the first of the error's
 * and its `errors`' that says. A message whose line is too long for a code frame names no place, and
 * these still do (agent#348). Undefined where none says.
 */
function located(error) {
  if (error === null || typeof error !== 'object') return undefined
  for (const held of [error, ...(Array.isArray(error.errors) ? error.errors : [])]) {
    const loc = held?.loc
    const file = typeof loc?.file === 'string' ? loc.file : typeof held?.id === 'string' ? held.id : undefined
    if (file !== undefined && Number.isInteger(loc?.line) && Number.isInteger(loc?.column)) return `${file}:${loc.line}:${loc.column + 1}`
    if (typeof held?.id === 'string') return held.id
  }
  return undefined
}

/** A load error as one line of a table cell: no colour codes, no scratch path, no `|`, bounded. */
function oneLine(message, scratch, error) {
  let text = String(message ?? '').replace(/\x1b\[[0-9;]*m/g, '')
  if (scratch) text = text.split(`${scratch}/`).join('')
  const lines = text.split('\n').map((line) => line.trim()).filter((line) => /[A-Za-z0-9]/.test(line) && !/^[│╭╰─]/.test(line))
  let at = text.match(/[\w./-]+:\d+:\d+/)?.[0] ?? located(error)
  if (at !== undefined && scratch) at = at.split(`${scratch}/`).join('')
  let shown = lines.slice(0, 2).join(' ')
  if (at !== undefined && !shown.includes(at)) shown += ` (at ${at})`
  shown = shown.replace(/\|/g, '/')
  return shown.length > 300 ? `${shown.slice(0, 297)}...` : shown
}

/** Whether a file's text parses as the test run would load it: the error, or null. Files of other kinds are not asked. */
let transform = null
async function parseError(file, text) {
  try {
    if (/\.json$/.test(file)) JSON.parse(text)
    else if (/\.[cm]?[jt]sx?$/.test(file)) {
      transform ??= (await import('vite')).transformWithOxc
      await transform(text, file)
    }
    return null
  } catch (error) {
    return oneLine(error?.message ?? error, undefined, error)
  }
}

if (options.check) {
  let bad = 0
  /** Each file's own parse error, asked once: a row is not blamed for a file that does not parse without it. */
  const unmutated = new Map()
  for (const row of runnable) {
    const path = join(PACKAGE, row.file)
    const text = existsSync(path) ? readFileSync(path, 'utf8') : null
    const count = text === null ? 0 : occurrences(text, row.find)
    if (count !== 1) {
      bad++
      console.log(`DOES NOT APPLY (${count} matches in ${row.file}): ${row.name}`)
    } else {
      if (!unmutated.has(row.file)) {
        const error = await parseError(row.file, text)
        unmutated.set(row.file, error)
        if (error !== null) {
          bad++
          console.log(`DOES NOT LOAD WITH NO MUTANT (${row.file}: ${error})`)
        }
      }
      if (unmutated.get(row.file) === null) {
        const error = await parseError(row.file, text.replace(row.find, () => row.replace))
        if (error !== null) {
          bad++
          console.log(`BROKEN (${row.file} does not load under the mutant: ${error}): ${row.name}`)
        }
      }
    }
    for (const test of row.tests) if (!existsSync(join(PACKAGE, test))) {
      bad++
      console.log(`NO SUCH TEST FILE ${test}: ${row.name}`)
    }
  }
  console.log(`${runnable.length} rows checked, ${bad} problems; ${retired.length} retired`)
  process.exit(bad === 0 ? 0 : 1)
}

if (runnable.length === 0 && !options.noMutant) {
  console.log(`no row chosen to run (${retired.length} retired)`)
  process.exit(0)
}

// The scratch copy: the repository's files as the checkout has them, so that a test reading the
// corpus, the Go sources, the scripts or the generated pages finds them; the built package, which
// the generated reference reads; and the installed packages by link.
const scratch = mkdtempSync(join(tmpdir(), 'mutation-'))
const copy = join(scratch, 'packages', 'harness')
const listed = spawnSync('git', ['ls-files', '-z', '--cached', '--others', '--exclude-standard'], { cwd: REPOSITORY, encoding: 'utf8', maxBuffer: 64 * 1024 * 1024 })
if (listed.status !== 0) usage(`git ls-files failed: ${listed.stderr}`)
for (const path of listed.stdout.split('\0').filter(Boolean)) {
  // A file deleted from the working tree is still listed; what the checkout does not have is not copied.
  if (existsSync(join(REPOSITORY, path))) cpSync(join(REPOSITORY, path), join(scratch, path), { recursive: true, verbatimSymlinks: true })
}
if (existsSync(join(PACKAGE, 'dist'))) cpSync(join(PACKAGE, 'dist'), join(copy, 'dist'), { recursive: true })
symlinkSync(join(REPOSITORY, 'node_modules'), join(scratch, 'node_modules'), 'dir')
process.on('exit', () => rmSync(scratch, { recursive: true, force: true }))
for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => process.exit(130))

const VITEST = join(REPOSITORY, 'node_modules', 'vitest', 'vitest.mjs')
if (!existsSync(VITEST)) usage('vitest is not installed: run npm ci first')

/**
 * One vitest run in the scratch copy: the files named, or the whole suite.
 * Resolves to the names of the tests that failed, [] where none did, or { undecided } where
 * the run timed out or ran nothing and no test file failed to load. The array's `unloaded` holds
 * [file, error] for each test file that did not load: it ran no test, so it can fail none.
 */
function run(files, report, excluded = []) {
  return new Promise((done) => {
    const args = [VITEST, 'run', ...files, ...excluded.flatMap((file) => ['--exclude', `**/${file}`]), '--reporter=json', `--outputFile=${report}`]
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
      const unloaded = []
      let ran = 0
      for (const file of parsed.testResults ?? []) {
        const name = file.name.replace(/^.*\/packages\/harness\//, '')
        if (file.status === 'failed' && (file.assertionResults ?? []).length === 0) unloaded.push([name, oneLine(file.message, scratch)])
        for (const test of file.assertionResults ?? []) {
          ran++
          if (test.status === 'failed') failed.push(`${name} > ${test.fullName}`)
        }
      }
      if (ran === 0 && failed.length === 0 && unloaded.length === 0) return done({ undecided: 'the run ran no test' })
      done(Object.assign(failed, { unloaded }))
    })
  })
}

/** Test files that fail for any mutant that moves a line: a row's own selection may name one, and the whole suite's run leaves it out. */
const LINE_SENSITIVE = ['providers-reference.test.ts']
let reportNumber = 0
const nextReport = () => join(scratch, `report-${reportNumber++}.json`)
const fileOf = (failure) => failure.split(' > ')[0]

if (options.noMutant) {
  const answer = await run([], nextReport())
  if (!Array.isArray(answer)) {
    console.log(`the whole suite with no mutant decided nothing: ${answer.undecided}`)
    process.exit(1)
  }
  for (const failure of answer) console.log(`fails with no mutant: ${failure}`)
  for (const [file, error] of answer.unloaded) console.log(`does not load with no mutant: ${file}: ${error}`)
  console.log(`the whole suite with no mutant: ${answer.length} failed, ${answer.unloaded.length} test file${answer.unloaded.length === 1 ? '' : 's'} did not load`)
  process.exit(answer.length === 0 && answer.unloaded.length === 0 ? 0 : 1)
}

/** What each test file fails with no mutant, from one run of it: a file is asked once. */
const noMutant = new Map()
/** The test files that do not load with no mutant, from the same runs. */
const unloadedWithNoMutant = new Set()
async function failingWithNoMutant(files) {
  const unknown = [...new Set(files)].filter((file) => !noMutant.has(file))
  if (unknown.length > 0) {
    const answer = await run(unknown, nextReport())
    if (!Array.isArray(answer)) return answer
    for (const file of unknown) noMutant.set(file, new Set())
    for (const failure of answer) {
      noMutant.get(fileOf(failure))?.add(failure)
      console.error(`mutation-baseline fails with no mutant: ${failure}`)
    }
    for (const [file, error] of answer.unloaded) {
      unloadedWithNoMutant.add(file)
      console.error(`mutation-baseline does not load with no mutant: ${file}: ${error}`)
    }
  }
  return new Set(files.flatMap((file) => [...noMutant.get(file)]))
}
/** The failures that are the mutant's: those of the answer that do not also fail with no mutant. */
async function beyondNoMutant(failures) {
  const clean = await failingWithNoMutant(failures.map(fileOf))
  return clean instanceof Set ? failures.filter((failure) => !clean.has(failure)) : clean
}

const results = []
/** One line per row as it ends, on the standard error stream: a shard stopped at its limit prints no
 * table, and a row's cost runs from seconds to many minutes (agent#281). */
const finished = (row, started) => {
  const last = results[results.length - 1]
  console.error(`mutation-row ${((Date.now() - started) / 1000).toFixed(1)} ${last.verdict.replace(/ /g, '-')} ${row.name}`)
}
const shown = (how, failures) => (options.verbose ? `${how}: ${failures.join('; ')}` : `${how}: ${failures[0]}${failures.length > 1 ? ` (+${failures.length - 1})` : ''}`)
for (const row of runnable) {
  const started = Date.now()
  const path = join(copy, row.file)
  const original = readFileSync(path, 'utf8')
  const count = occurrences(original, row.find)
  if (count !== 1) {
    results.push({ row, verdict: 'DID NOT APPLY', detail: `${count} matches of the row's find text in ${row.file}` })
    finished(row, started)
    continue
  }
  const underMutant = async (files, excluded = []) => {
    writeFileSync(path, original.replace(row.find, () => row.replace))
    try {
      return await run(files, nextReport(), excluded)
    } finally {
      writeFileSync(path, original)
    }
  }
  /** The test files that did not load under the mutant, by any run of the row: [file, error]. */
  const unloaded = new Map()
  const noteUnloaded = (answer) => {
    if (Array.isArray(answer)) for (const [file, error] of answer.unloaded) if (!unloaded.has(file)) unloaded.set(file, error)
  }
  /**
   * Where no failure of the mutant's caught the row: BROKEN when a test file that loads with no mutant
   * did not load under it, and does not load again in a run of its own under the mutant; else the
   * NOT CAUGHT this would have been. One file shows it, so the files are asked one at a time, the
   * selection's first, and the first that shows it ends the search: a mutant of a module every test
   * imports leaves nearly the whole suite unloaded, and asking all of it at once can take longer
   * than a run's bound.
   */
  const notCaught = async (how, detail) => {
    for (const file of unloaded.keys()) {
      const clean = await failingWithNoMutant([file])
      if (!(clean instanceof Set)) return results.push({ row, verdict: 'UNDECIDED', detail: `with no mutant: ${clean.undecided}` })
      if (unloadedWithNoMutant.has(file)) continue
      const again = await underMutant([file])
      if (!Array.isArray(again)) return results.push({ row, verdict: 'UNDECIDED', detail: `in its own file: ${again.undecided}` })
      const still = again.unloaded.find(([name]) => name === file)
      if (still) return results.push({ row, verdict: 'BROKEN', detail: `${how}: no test failed, and the mutant stops ${file} from loading: ${still[1]}` })
    }
    results.push({ row, verdict: 'NOT CAUGHT', detail })
  }
  const selection = !options.whole && row.tests.length > 0
  let decided = false
  if (selection) {
    const answer = await underMutant(row.tests)
    noteUnloaded(answer)
    if (Array.isArray(answer) && answer.length > 0) {
      const counted = await beyondNoMutant(answer)
      if (Array.isArray(counted) && counted.length > 0) {
        results.push({ row, verdict: 'caught', detail: shown('selected', counted) })
        decided = true
      } else if (!Array.isArray(counted)) {
        results.push({ row, verdict: 'UNDECIDED', detail: `with no mutant: ${counted.undecided}` })
        decided = true
      }
    }
  }
  if (!decided) {
    const how = selection ? 'whole, after its selection decided nothing' : 'whole'
    const answer = await underMutant([], LINE_SENSITIVE)
    noteUnloaded(answer)
    if (!Array.isArray(answer)) results.push({ row, verdict: 'UNDECIDED', detail: answer.undecided })
    else {
      const counted = answer.length > 0 ? await beyondNoMutant(answer) : []
      if (!Array.isArray(counted)) results.push({ row, verdict: 'UNDECIDED', detail: `with no mutant: ${counted.undecided}` })
      else if (counted.length === 0) await notCaught(how, `${how}: no test failed beyond the failures with no mutant`)
      else {
        // Only the whole suite saw these: they must fail again without the rest of the suite beside them.
        const again = await underMutant([...new Set(counted.map(fileOf))])
        if (!Array.isArray(again)) results.push({ row, verdict: 'UNDECIDED', detail: `in its own file: ${again.undecided}` })
        else {
          const kept = counted.filter((failure) => again.includes(failure))
          if (kept.length > 0) results.push({ row, verdict: 'caught', detail: shown(how, kept) })
          else await notCaught(how, `${how}: what failed did not fail again in its own file: ${counted[0]}`)
        }
      }
    }
  }
  finished(row, started)
}

console.log('| row | file | result |')
console.log('| --- | --- | --- |')
for (const { row, verdict, detail } of results) console.log(`| ${row.name} | ${row.file} | ${verdict}: ${detail} |`)
for (const row of retired) console.log(`| ${row.name} | ${row.file} | retired: ${row.reason} |`)
const count = (verdict) => results.filter((result) => result.verdict === verdict).length
console.log(`\n${chosen.length} rows chosen: ${count('caught')} caught, ${count('NOT CAUGHT')} not caught, ${count('BROKEN')} broken, ${count('DID NOT APPLY')} did not apply, ${count('UNDECIDED')} undecided, ${retired.length} retired`)
process.exit(count('caught') === runnable.length ? 0 : 1)
