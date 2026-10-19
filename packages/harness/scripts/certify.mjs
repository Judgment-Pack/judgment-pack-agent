#!/usr/bin/env node
// Run the certification suite against one adapter: `npm run certify -- --adapter <id>`.
//
// Every adapter runs the suite's own checks of the recorded scenario and of the
// seal (certification/conformance.test.ts, certification/scriptedModel.test.ts),
// with the legs of every other adapter left out. Then, by the adapter's kind,
// exactly the checks the Desk runs over every engine of that kind:
//
// - a model engine: the conformance session's legs, `engine <id>` and
//   `engine <id> · thinking`, in certification/conformance.test.ts;
// - an agent engine: the recorded scenario behind the real tool gate and the
//   agent-engine checks, `agent engine <id>`, in engines/<id>/<id>.test.ts.
//
// The kinds are the registry's (engines/index.ts, ENGINE_CAPABILITIES).
import { spawnSync } from 'node:child_process'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const ADAPTERS = { vercel: 'model', codex: 'agent' }

function adapterArgument(argv) {
  for (let i = 0; i < argv.length; i++) {
    if (argv[i] === '--adapter') return argv[i + 1]
    if (argv[i].startsWith('--adapter=')) return argv[i].slice('--adapter='.length)
  }
  return undefined
}

const adapter = adapterArgument(process.argv.slice(2))
if (!Object.hasOwn(ADAPTERS, adapter ?? '')) {
  console.error(`usage: npm run certify -- --adapter <${Object.keys(ADAPTERS).join('|')}>`)
  process.exit(2)
}

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const vitest = resolve(root, 'node_modules/.bin/vitest')
const bin = [vitest, resolve(root, '../../node_modules/.bin/vitest')]

function run(args) {
  for (const candidate of bin) {
    const result = spawnSync(candidate, ['run', ...args], { cwd: root, stdio: 'inherit' })
    if (result.error && result.error.code === 'ENOENT') continue
    if (result.error) throw result.error
    return result.status ?? 1
  }
  console.error('vitest is not installed: run npm ci first')
  return 1
}

// The legs of every other adapter are left out by their describe names.
const others = Object.keys(ADAPTERS).filter((id) => id !== adapter)
const leaveOut = others.length === 0 ? '' : `^(?!(agent )?engine (${others.join('|')})\\b)`
const runs = [['certification', ...(leaveOut ? ['--testNamePattern', leaveOut] : [])]]
if (ADAPTERS[adapter] === 'agent') {
  runs.push([`engines/${adapter}/${adapter}.test.ts`, '--testNamePattern', `^agent engine ${adapter}\\b`])
}

let status = 0
for (const args of runs) {
  console.log(`certify ${adapter}: vitest run ${args.join(' ')}`)
  const code = run(args)
  if (code !== 0) status = code
}
console.log(status === 0 ? `certify ${adapter}: passed` : `certify ${adapter}: FAILED`)
process.exit(status)
