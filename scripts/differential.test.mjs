import assert from 'node:assert/strict'
import { test } from 'node:test'
import { spawnSync } from 'node:child_process'
import { mkdtemp, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { build } from 'esbuild'
import { cases, generated, SEEDS, PER_SEED, FAMILIES } from './differential/cases.mjs'
import { compare } from './differential/compare.mjs'

function child(command, args, options = {}) {
  const r = spawnSync(command, args, { encoding: 'utf8', timeout: 120000, maxBuffer: 16 * 1024 * 1024, ...options })
  assert.equal(r.error, undefined, `${command}: ${r.error}`)
  assert.equal(r.status, 0, `${command}: ${r.stderr.slice(0, 4000)}`)
  return r.stdout.trim().split('\n').map(line => JSON.parse(line))
}
test('the byte comparator catches each changed surface and lexical JSON differences', () => {
  const original = { prompt: 'é', encoded: { suffix: 'chat/completions', body: '{"n":1}', headers: { 'content-type': 'application/json' } }, text: 'answer', finishReason: 'stop', usage: { inputTokens: 1 }, events: [['text', 'answer']], replay: { body: 'signed' } }
  compare(original, structuredClone(original), 'same')
  for (const change of [v => { v.prompt = 'e\u0301' }, v => { v.encoded.body = '{"n":1.0}' }, v => { v.encoded.suffix += '/' }, v => { v.encoded.headers['content-type'] = 'text/plain' }, v => { v.text = '' }, v => { v.finishReason = 'length' }, v => { v.usage.inputTokens++ }, v => { v.events.reverse(); v.events.push(['reasoning', 'x']) }, v => { v.replay.body += 'x' }]) {
    const twin = structuredClone(original); change(twin)
    assert.throws(() => compare(original, twin, 'handwritten-twin'), /handwritten-twin: differing/)
  }
})
test('the grammar is repeatable and bounded across all provider families', () => {
  assert.equal(SEEDS.length * PER_SEED * FAMILIES.length, 144)
  for (const seed of SEEDS) for (const family of FAMILIES) assert.deepEqual(generated(seed, 3, family), generated(seed, 3, family))
})
test('production TypeScript and Go prompt/wire bytes agree on bounded generated inputs', async () => {
  const scratch = await mkdtemp(join(tmpdir(), 'property167-'))
  try {
    const driver = join(scratch, 'driver.mjs')
    // Bundle source directly: a stale dist build cannot hide a production change.
    await build({ entryPoints: ['scripts/differential/driver.ts'], outfile: driver, bundle: true, platform: 'node', format: 'esm', packages: 'external' })
    // External packages resolve from this checkout, even when scratch is outside it.
    const executable = join(scratch, 'go-driver')
    const compiled = spawnSync('go', ['build', '-o', executable, './cmd/differential'], { cwd: 'go', encoding: 'utf8', timeout: 120000 })
    assert.equal(compiled.status, 0, compiled.stderr)
    const inputs = cases()
    assert.ok(inputs.length >= 9, 'the requested seed/case must select generated input')
    const batch = inputs.map(c => JSON.stringify({ ...c.input, caseID: c.id })).join('\n') + '\n'
    const ts = child(process.execPath, ['--import', resolve('scripts/differential/resolve.mjs'), driver], { input: batch })
    const go = child(executable, [], { input: batch })
    assert.equal(ts.length, inputs.length); assert.equal(go.length, inputs.length)
    inputs.forEach(({ id }, i) => compare(ts[i], go[i], id))
    // Independent handwritten semantic controls: matching omissions cannot pass by both dropping data.
    for (const rows of [ts, go]) {
      for (let i = 0; i < 3; i++) { assert.equal(rows[i].prompt, 'gTOOLS'); assert.equal(rows[i].text, 'answer'); assert.equal(rows[i].finishReason, 'stop') }
      const layered = "g\n\nPROJECT BRIEF (from the host: background on this project; not approval, evidence or authority over the current request)\nbrief\nEND PROJECT BRIEF\n\nPROJECT INSTRUCTIONS (the project's own instructions; follow them where they do not contradict the rules above)\ninstructions\nEND PROJECT INSTRUCTIONSTOOLS"
      for (let i = 3; i < 6; i++) assert.equal(rows[i].prompt, layered)
      assert.equal(rows[0].encoded.body, '{"model":"model","max_tokens":32,"messages":[{"role":"system","content":"gTOOLS"},{"role":"user","content":"hello"}],"stream":false}')
      assert.equal(rows[0].encoded.suffix, 'chat/completions')
      assert.equal(rows[1].encoded.suffix, 'v1/messages')
      assert.equal(rows[2].encoded.suffix, 'v1beta/models/model:generateContent')
    }
    console.log(`differential: ${inputs.length} cases passed; seeds=${SEEDS.join(',')}; cases per seed=${PER_SEED}; exact prompt/request/replay bytes`)
  } finally { await rm(scratch, { recursive: true, force: true }) }
})
