/**
 * The recorder that wrote `runtime.json`, run here against a stand-in binary (agent#37).
 *
 * The fixture's `recorder` member names `scripts/record-conformance.mjs`, and the script is in this
 * repository: `record` writes the fixture from a `jpack` binary and a project directory, `verify`
 * records to memory and compares byte for byte. The recorded binary is not in this repository, so
 * what is held here is the machinery, over a stand-in that answers from the committed fixture:
 * what the recorder writes from those answers is the committed file in every member but the
 * binary's own digest, `verify` of a file it just wrote is a match, `verify` of the committed file
 * against another binary is a refusal that says so, and a hand edit of one recorded answer is one.
 * No model is asked anything; the stand-in is a script and the recorder talks to it over stdio.
 *
 * Re-recording against the recorded runtime is a command, not a test:
 * `npm run conformance:verify --workspace @judgment-pack/harness -- <jpack binary> <project dir>`.
 */
import { spawnSync } from 'node:child_process'
import { chmodSync, existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import runtime from './runtime.json'

const HERE = dirname(fileURLToPath(import.meta.url))
const RECORDER = resolve(HERE, '..', 'scripts', 'record-conformance.mjs')
const COMMITTED = resolve(HERE, 'runtime.json')

/** A `jpack mcp` that answers from the fixture: initialize, tools/list (with one tool more than the five) and tools/call by tool and arguments. */
const STAND_IN = `#!/usr/bin/env node
const fs = require('node:fs')
const fixture = JSON.parse(fs.readFileSync(process.env.STAND_IN_FIXTURE, 'utf8'))
const canonical = (value) => value === null || typeof value !== 'object' ? JSON.stringify(value)
  : Array.isArray(value) ? '[' + value.map(canonical).join(',') + ']'
  : '{' + Object.keys(value).sort().map((key) => JSON.stringify(key) + ':' + canonical(value[key])).join(',') + '}'
if (process.argv[2] !== 'mcp') process.exit(2)
let buffer = ''
process.stdin.setEncoding('utf8')
process.stdin.on('data', (chunk) => {
  buffer += chunk
  let newline
  while ((newline = buffer.indexOf('\\n')) >= 0) {
    const line = buffer.slice(0, newline).trim()
    buffer = buffer.slice(newline + 1)
    if (line === '') continue
    const message = JSON.parse(line)
    if (message.id === undefined) continue
    const reply = (body) => process.stdout.write(JSON.stringify({ jsonrpc: '2.0', id: message.id, ...body }) + '\\n')
    if (message.method === 'initialize') {
      reply({ result: { protocolVersion: fixture.runtime.protocolVersion, serverInfo: fixture.runtime.serverInfo, capabilities: { tools: {} } } })
    } else if (message.method === 'tools/list') {
      reply({ result: { tools: [...fixture.tools].reverse().concat([{ name: 'a_tool_the_scenario_never_grants', inputSchema: { type: 'object' } }]) } })
    } else if (message.method === 'tools/call') {
      const found = fixture.calls.find((call) => call.tool === message.params.name && canonical(call.arguments) === canonical(message.params.arguments))
      if (found) reply({ result: found.result })
      else reply({ error: { code: -32602, message: 'no recorded answer for ' + message.params.name } })
    }
  }
})
process.stdin.on('end', () => process.exit(0))
`

let work = ''
let binary = ''

beforeAll(() => {
  work = mkdtempSync(join(tmpdir(), 'recorder-'))
  writeFileSync(join(work, 'package.json'), '{"type":"commonjs"}')
  binary = join(work, 'jpack')
  writeFileSync(binary, STAND_IN)
  chmodSync(binary, 0o755)
})
afterAll(() => rmSync(work, { recursive: true, force: true }))

/** The recorder, as a person would run it; the project directory is any directory, which the stand-in never reads. */
function recorder(mode: 'record' | 'verify', fixture: string, path = binary) {
  const result = spawnSync(process.execPath, [RECORDER, mode, path, work, '--fixture', fixture], {
    encoding: 'utf8',
    env: { ...process.env, STAND_IN_FIXTURE: COMMITTED, JPACK_COMMIT: runtime.runtime.sourceCommit },
    timeout: 60_000
  })
  return { status: result.status, out: result.stdout + result.stderr }
}

describe('the conformance recorder', () => {
  it('is the file the fixture names, in this repository', () => {
    expect(runtime.recorder).toBe('packages/harness/scripts/record-conformance.mjs')
    expect(existsSync(RECORDER)).toBe(true)
    expect(RECORDER.endsWith(runtime.recorder.replace(/^packages\/harness\//, ''))).toBe(true)
  })

  it('writes from a runtime’s answers the committed fixture, in every member but the binary’s own digest', () => {
    const written = join(work, 'recorded.json')
    const done = recorder('record', written)
    expect(done.status, done.out).toBe(0)
    const recorded = JSON.parse(readFileSync(written, 'utf8')) as typeof runtime
    // The five tools in the scenario's order, filtered from the listing; one answer per step T1 to T6; none for the write.
    expect(recorded.tools.map((tool) => tool.name)).toEqual(runtime.tools.map((tool) => tool.name))
    expect(recorded.calls.map((call) => call.step)).toEqual(['T1', 'T2', 'T3', 'T4', 'T5', 'T6'])
    expect(recorded.calls.find((call) => call.step === 'T6')!.arguments).toMatchObject({ rehearsal: true })
    const { binarySha256, ...stated } = recorded.runtime
    const { binarySha256: committedSha, ...committed } = runtime.runtime
    expect(binarySha256).toMatch(/^[0-9a-f]{64}$/)
    expect(binarySha256).not.toBe(committedSha)
    expect({ ...recorded, runtime: stated }).toEqual({ ...runtime, runtime: committed })
  })

  it('verifies a file it just wrote byte for byte, and refuses the committed one against another binary', () => {
    const written = join(work, 'recorded-again.json')
    expect(recorder('record', written).status).toBe(0)
    const same = recorder('verify', written)
    expect(same.status, same.out).toBe(0)
    expect(same.out).toContain('byte for byte')
    const other = recorder('verify', COMMITTED)
    expect(other.status).toBe(1)
    expect(other.out).toContain('does not match')
  })

  // #42: an absent binary was an unhandled spawn error with a stack trace.
  it('fails closed when the binary is absent, naming the path and the digest the fixture expects', () => {
    const absent = join(work, 'no-such-jpack')
    for (const mode of ['record', 'verify'] as const) {
      const done = recorder(mode, COMMITTED, absent)
      expect(done.status).toBe(1)
      expect(done.out).toContain('binary not found')
      expect(done.out).toContain(absent)
      expect(done.out).toContain(runtime.runtime.binarySha256)
      expect(done.out).not.toContain('ENOENT')
      expect(done.out).not.toMatch(/\n\s+at /)
    }
    // Nothing was written for a record that had no binary.
    const written = join(work, 'never-written.json')
    expect(recorder('record', written, absent).status).toBe(1)
    expect(existsSync(written)).toBe(false)
  })

  it('refuses a fixture with one recorded answer edited by hand', () => {
    const written = join(work, 'edited.json')
    expect(recorder('record', written).status).toBe(0)
    const edited = readFileSync(written, 'utf8').replace('"status": "valid"', '"status": "invalid"')
    expect(edited).not.toBe(readFileSync(written, 'utf8'))
    writeFileSync(written, edited)
    expect(recorder('verify', written).status).toBe(1)
  })
})
