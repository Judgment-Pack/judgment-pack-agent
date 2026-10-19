#!/usr/bin/env node
// Run the certification suite against one adapter: `npm run certify -- --adapter <id>`.
//
// Every adapter runs the suite's own checks of the recorded scenario and of the
// seal (certification/conformance.test.ts, certification/scriptedModel.test.ts),
// with the legs of every other adapter left out. Then, by the adapter's kind,
// exactly the checks the Desk runs over every engine of that kind:
//
// - a model engine: the conformance session's legs, `engine <id>`,
//   `engine <id> · thinking`, `engine <id> · strict alternation` (an endpoint
//   that refuses a broken turn order, agent#9) and `engine <id> · text tool
//   calls` (the scenario on an endpoint without native tool calls, agent#4), in
//   certification/conformance.test.ts, and the six certified scenarios of
//   agent#13, `engine <id> · scenarios`, in certification/scenarios.test.ts;
// - an agent engine: the recorded scenario behind the real tool gate and the
//   agent-engine checks, `agent engine <id>`, in engines/<id>/<id>.test.ts,
//   and the same six scenarios, `agent engine <id> · scenarios`.
//
// The ids and kinds are the registry's own (engines/index.ts,
// ENGINE_CAPABILITIES), read here rather than copied. A run in which none of
// the adapter's own checks ran fails: vitest passes a name pattern that matches
// nothing, so the checks are counted from its JSON report.
import { spawnSync } from 'node:child_process'
import { mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const { ENGINE_CAPABILITIES } = await import(resolve(root, 'engines/index.ts'))

function adapterArgument(argv) {
  for (let i = 0; i < argv.length; i++) {
    if (argv[i] === '--adapter') return argv[i + 1]
    if (argv[i].startsWith('--adapter=')) return argv[i].slice('--adapter='.length)
  }
  return undefined
}

const adapter = adapterArgument(process.argv.slice(2))
if (!Object.hasOwn(ENGINE_CAPABILITIES, adapter ?? '')) {
  console.error(`usage: npm run certify -- --adapter <${Object.keys(ENGINE_CAPABILITIES).join('|')}>`)
  process.exit(2)
}
const kind = ENGINE_CAPABILITIES[adapter]

const bin = [resolve(root, 'node_modules/.bin/vitest'), resolve(root, '../../node_modules/.bin/vitest')]
const reports = mkdtempSync(join(tmpdir(), 'certify-'))

/** One vitest run; its JSON report is written beside the others. */
function run(args, report) {
  const reporting = ['--reporter=default', '--reporter=json', `--outputFile.json=${report}`]
  for (const candidate of bin) {
    const result = spawnSync(candidate, ['run', ...args, ...reporting], { cwd: root, stdio: 'inherit' })
    if (result.error && result.error.code === 'ENOENT') continue
    if (result.error) throw result.error
    return result.status ?? 1
  }
  console.error('vitest is not installed: run npm ci first')
  return 1
}

/** The checks of each certified scenario that passed, by the number their names begin with (agent#13). */
const scenarioChecks = new Map()

/** The adapter's own checks that passed, by their describe names. */
function ownChecks(report) {
  let parsed
  try {
    parsed = JSON.parse(readFileSync(report, 'utf8'))
  } catch {
    return 0
  }
  const own =
    kind === 'agent'
      ? (top) => top === `agent engine ${adapter}` || top === `agent engine ${adapter} · scenarios`
      : (top) =>
          [
            `engine ${adapter}`,
            `engine ${adapter} · thinking`,
            `engine ${adapter} · strict alternation`,
            `engine ${adapter} · text tool calls`,
            `engine ${adapter} · scenarios`
          ].includes(top)
  let count = 0
  for (const file of parsed.testResults ?? []) {
    for (const test of file.assertionResults ?? []) {
      const top = test.ancestorTitles?.[0] ?? ''
      if (test.status !== 'passed' || !own(top)) continue
      count++
      const scenario = top.endsWith(' · scenarios') ? /^(\d) · /.exec(test.title ?? '')?.[1] : undefined
      if (scenario !== undefined) scenarioChecks.set(scenario, (scenarioChecks.get(scenario) ?? 0) + 1)
    }
  }
  return count
}

// The legs of every other adapter are left out by their describe names.
const others = Object.keys(ENGINE_CAPABILITIES).filter((id) => id !== adapter)
const leaveOut = others.length === 0 ? '' : `^(?!(agent )?engine (${others.join('|')})\\b)`
const runs = [['certification', ...(leaveOut ? ['--testNamePattern', leaveOut] : [])]]
if (kind === 'agent') {
  runs.push([`engines/${adapter}/${adapter}.test.ts`, '--testNamePattern', `^agent engine ${adapter}\\b`])
}

let status = 0
let checks = 0
try {
  for (const [index, args] of runs.entries()) {
    console.log(`certify ${adapter}: vitest run ${args.join(' ')}`)
    const report = join(reports, `run-${index}.json`)
    const code = run(args, report)
    if (code !== 0) status = code
    checks += ownChecks(report)
  }
} finally {
  rmSync(reports, { recursive: true, force: true })
}
if (checks === 0) {
  console.error(`certify ${adapter}: no check of the ${kind} engine ${adapter} ran`)
  if (status === 0) status = 1
}
if (scenarioChecks.size > 0) {
  const listed = [...scenarioChecks].sort(([a], [b]) => a.localeCompare(b)).map(([id, count]) => `${id}: ${count}`)
  console.log(`certify ${adapter}: certified scenarios, checks passed by scenario (${listed.join(', ')})`)
}
console.log(
  status === 0
    ? `certify ${adapter}: passed, ${checks} of the ${kind} engine's own checks among them`
    : `certify ${adapter}: FAILED`
)
process.exit(status)
