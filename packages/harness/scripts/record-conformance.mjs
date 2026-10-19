#!/usr/bin/env node
/**
 * Regenerate the conformance session's recorded runtime, reproducibly.
 *
 *   npm run conformance:record --workspace @judgment-pack/harness -- <jpack binary> <project dir>
 *   npm run conformance:verify --workspace @judgment-pack/harness -- <jpack binary> <project dir>
 *
 * `record` writes `certification/runtime.json`; `verify` records to memory and
 * byte-compares against the committed file, so a hand-edited or stale fixture
 * is a failure rather than a claim. An optional `--fixture <path>` after the
 * project directory names another file to write or compare, which is how
 * `certification/recorder.test.ts` runs this script against a stand-in binary
 * without touching the committed fixture.
 *
 * **This script records the Runtime's answers to the scenario's scripted calls
 * and nothing else.** No model is asked anything. It needs a `jpack` binary and
 * a project directory that declares an audit trail; the committed fixture names
 * the binary it was recorded from by its SHA-256, and a binary with another
 * digest records a fixture that differs in that member at least, which is what
 * `verify` then says.
 *
 * **Why this exists.** The fixture used to carry its provenance as prose — a
 * tag and a short commit — with no way to check it and no recorder in the
 * repository. A recorded answer somebody edited by hand would have gone on
 * calling itself recorded, and the conformance session would have been
 * certifying engines against it. The fixture now carries the binary's SHA-256
 * and the runtime's full commit, and this file is what put them there.
 *
 * **Byte-determinism.** The runtime's answers to these six calls carry no
 * timestamp, no run id and no path: the recorder writes them exactly as they
 * arrived, with a stable member order and a trailing newline, so two runs of
 * the same binary over the same project produce the same bytes. `verify` is
 * what says so; it is not asserted here.
 *
 * It is `.mjs` rather than TypeScript because it drives a subprocess and runs
 * under plain node, outside the page's toolchain and outside `tsc`'s program.
 */
import { spawn } from 'node:child_process'
import { createHash } from 'node:crypto'
import { readFileSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const HERE = dirname(fileURLToPath(import.meta.url))
const CONFORMANCE = join(HERE, '..', 'certification')
const SCENARIO = join(CONFORMANCE, 'scenario.json')
const COMMITTED = join(CONFORMANCE, 'runtime.json')
/** The path the fixture's `recorder` member names, from the repository's root: this file. */
const RECORDER_PATH = 'packages/harness/scripts/record-conformance.mjs'

/** One JSON-RPC conversation with `jpack mcp` over stdio. */
function driver(binary, cwd) {
  const child = spawn(binary, ['mcp'], { cwd, stdio: ['pipe', 'pipe', 'inherit'] })
  const waiting = new Map()
  let buffer = ''
  child.stdout.on('data', (chunk) => {
    buffer += chunk
    let newline
    while ((newline = buffer.indexOf('\n')) >= 0) {
      const line = buffer.slice(0, newline).trim()
      buffer = buffer.slice(newline + 1)
      if (line === '') continue
      const message = JSON.parse(line)
      const settle = waiting.get(message.id)
      if (settle) {
        waiting.delete(message.id)
        settle(message)
      }
    }
  })
  let id = 0
  return {
    request(method, params) {
      const next = (id += 1)
      const answered = new Promise((settle) => waiting.set(next, settle))
      child.stdin.write(`${JSON.stringify({ jsonrpc: '2.0', id: next, method, params })}\n`)
      return answered
    },
    notify(method, params) {
      child.stdin.write(`${JSON.stringify({ jsonrpc: '2.0', method, params })}\n`)
    },
    stop() {
      child.stdin.end()
      child.kill('SIGTERM')
    }
  }
}

async function record(binary, project) {
  const scenario = JSON.parse(readFileSync(SCENARIO, 'utf8'))
  const five = scenario.scenarioTools
  const jpack = driver(binary, project)

  const initialized = await jpack.request('initialize', {
    protocolVersion: '2025-06-18',
    capabilities: {},
    clientInfo: { name: 'judgment-pack-agent-conformance-recorder', version: '1' }
  })
  jpack.notify('notifications/initialized', {})

  const listed = await jpack.request('tools/list', {})
  const served = listed.result.tools.filter((tool) => five.includes(tool.name))
  const missing = five.filter((name) => !served.some((tool) => tool.name === name))
  if (missing.length > 0) {
    throw new Error(`this runtime does not serve ${missing.join(', ')}`)
  }
  served.sort((left, right) => five.indexOf(left.name) - five.indexOf(right.name))

  const calls = []
  for (const step of scenario.steps) {
    if (step.kind !== 'tool_call') continue
    // T7 is `write_file`, which the runtime has no such tool for and the gate
    // never lets out of the page. A recording of it would be a recording of an
    // arrival that must not happen.
    if (!five.includes(step.tool)) continue
    // The arguments the **gate** lets out of the page, which is why the
    // evaluate carries `rehearsal: true` here and not in the scenario.
    const args = { ...step.arguments }
    if (step.tool === 'experimental_evaluate') args.rehearsal = true
    const answered = await jpack.request('tools/call', { name: step.tool, arguments: args })
    if (answered.error) {
      throw new Error(`${step.id} ${step.tool} was refused: ${answered.error.message}`)
    }
    calls.push({ step: step.id, tool: step.tool, arguments: args, result: answered.result })
  }
  jpack.stop()

  const recorded = {
    $comment:
      'Recorded by packages/harness/scripts/record-conformance.mjs. Do not hand-edit: run ' +
      '`npm run conformance:verify --workspace @judgment-pack/harness -- <binary> <project>` and it will say so. ' +
      '`tools` is tools/list filtered to the five the assistant may call, exactly as served; ' +
      '`calls` is one tools/call answer per scenario step T1-T6, recorded with the arguments ' +
      'the gate lets out of the page — which is why T6 carries rehearsal: true. T7 ' +
      '(write_file) is deliberately absent: a call that reaches the scripted server under that ' +
      'name is a test failure, which is the point of the step.',
    recorder: RECORDER_PATH,
    runtime: {
      binarySha256: createHash('sha256').update(readFileSync(binary)).digest('hex'),
      serverInfo: initialized.result.serverInfo,
      protocolVersion: initialized.result.protocolVersion,
      note:
        'A release build reports its own version; a binary built from a tagged tree without ' +
        'release ldflags reports 0.0.0-dev, so `binarySha256` and `sourceCommit` are what ' +
        'identify it. `sourceCommit` is stated by whoever ran the recorder — it is not read ' +
        'off the binary — and is the one member here that is a claim rather than a measurement.',
      sourceCommit: process.env.JPACK_COMMIT ?? 'unstated',
      project:
        'The directory named on the command line. It must declare an audit trail, so that a ' +
        'recording pass that wrote one would leave the evidence behind.'
    },
    tools: served,
    calls
  }
  return `${JSON.stringify(recorded, null, 2)}\n`
}

const argv = process.argv.slice(2)
const flag = argv.indexOf('--fixture')
if (flag >= 0 && argv[flag + 1] === undefined) {
  console.error('--fixture needs a path')
  process.exit(2)
}
const RUNTIME = flag >= 0 ? resolve(argv[flag + 1]) : COMMITTED
if (flag >= 0) argv.splice(flag, 2)
const [mode, binary, project] = argv
if (mode !== 'record' && mode !== 'verify') {
  console.error('usage: record-conformance.mjs record|verify <jpack binary> <project dir> [--fixture <path>]')
  process.exit(2)
}
if (!binary || !project) {
  console.error('both a runtime binary and a project directory are required')
  process.exit(2)
}

const produced = await record(resolve(binary), resolve(project))
if (mode === 'record') {
  writeFileSync(RUNTIME, produced)
  console.log(`wrote ${RUNTIME} (${produced.length} bytes)`)
  process.exit(0)
}
const committed = readFileSync(RUNTIME, 'utf8')
if (committed === produced) {
  console.log('runtime.json matches this binary and this project, byte for byte')
  process.exit(0)
}
console.error(
  `runtime.json does not match what this binary and project produce ` +
    `(committed ${committed.length} bytes, recorded ${produced.length}). ` +
    `Re-record it, or say why the fixture and the runtime disagree.`
)
process.exit(1)
