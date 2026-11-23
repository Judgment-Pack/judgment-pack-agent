// @vitest-environment node
import { createHash } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { DelegationRefused, PrincipalInputInvalid, admitCall, admitStanding, canon, narrowChild, openPrincipal, type AdmissionPort, type AdmissionRead, type Audience, type ChildLinkRequest, type SkillLoadPort, type SkillRef } from './principal'
import { SkillGate, type Skill } from './skills'
import { descriptorApprovalProblem, descriptorSnapshotDigest, readDescriptorSnapshot } from './descriptorSnapshot'
import type { ExecutionCheckpoint, SourceFigures } from './execution/state'

// ADR-0030: the specification's admission read cases (RFC 0030 §2a, tables 4 and 5), copied byte for byte (a
// case's "note" member is commentary and is not read); this digest is the one the specification's
// conformance/delegation/README.md states.
const ADMISSION_READS_SHA256 = 'a620f8e7280652a1c9402c4862ab3ebafa9b8286569a327fdbdd12492f5291e2'
const dir = new URL('../../contract/testdata/principal/', import.meta.url)
const raw = readFileSync(new URL('admission-reads.json', dir))
type Case = {name: string; table: number; applies: Record<string, string>; senders: string[]; readVersion: '1' | '2'; inputs: {chain: string | null; at: string; audience: Audience; call?: {source: string; tool: string; effect: string; count: boolean}; version?: string | null; skills?: SkillRef[]; load?: {skill: SkillRef; skills: SkillRef[]}; restore?: SkillRef[]}; body: string | string[] | null; answer: string | string[] | null; expect: {sender: string; reads: number; loaded?: boolean | string[]; dropped?: string[]}}
const cases = (JSON.parse(raw.toString('utf8')) as {cases: Case[]}).cases
const statements = JSON.parse(readFileSync(new URL('spec-ed25519-statements.json', dir), 'utf8')) as {keys: Record<string, unknown[]>; vectors: {name: string; chain: unknown}[]}
const root = statements.vectors.find(v => v.name === 'a valid root')!.chain
const chainText = typeof root === 'string' ? root : JSON.stringify(root)
const software = {package: 'judgment-pack-agent', version: '0.13.0'}
const admitAll: AdmissionPort = () => ({admission: 'admit'})
const parsed = (text: string): unknown => { try { return JSON.parse(text) } catch { return text } }

const open = (version: '1' | '2' | undefined, at: string, admission: AdmissionPort = admitAll, skillLoad?: SkillLoadPort, child?: (request: ChildLinkRequest) => never | {refusal: 'not-parent-actor'}) =>
  openPrincipal({chain: chainText, keys: statements.keys as never, software, audience: 'agent-host-1', admission, clock: () => Date.parse(at), ...(version ? {readVersions: {desk: version}} : {}), ...(skillLoad ? {skillLoad} : {}), ...(child ? {child} : {})})

const skillOf = (ref: SkillRef): Skill => ({kind: 'method', tags: [], id: ref.id, name: ref.id, description: 'synthetic', instructions: 'synthetic', tools: [], resources: [], source: {kind: 'local', id: 'skills', trust: 'connected'}, digest: ref.digest, bytes: 10}) as unknown as Skill

/** A run's skill gate over catalogued skills of one source, with `loaded` restored (no read), under `principal`. */
function gateOf(principal: Awaited<ReturnType<typeof open>> | undefined, catalogued: SkillRef[], loaded: SkillRef[], skillsPerRun = 4) {
  const rows = [{source: 'skills'} as SourceFigures]
  const state = {skills: loaded.map(ref => ({id: ref.id, digest: ref.digest, bytes: 10, loadedAt: '2026-10-30T00:30:00.000Z'}))} as unknown as ExecutionCheckpoint
  const events: Record<string, unknown>[] = []
  const gate = new SkillGate(catalogued.map(skillOf), {skillsPerRun}, {state: () => state, write: patch => { Object.assign(state, patch) }, sources: () => rows, publishSources: () => {}, marker: () => 'marker', agentWire: false, notify: event => { events.push(event as never) }, principal: () => principal})
  gate.restore()
  const refusal = () => [...events].reverse().find(e => e.type === 'guardrail') as {refusal?: {name: string}; reason?: string; detail: string; principal?: unknown} | undefined
  return {gate, state, rows, events, refusal, load: (name: string) => gate.loadTool().execute({name}, new AbortController().signal)}
}
const ids = (state: ExecutionCheckpoint) => (state.skills ?? []).map(load => load.id)

describe('the specification admission read cases (ADR-0030)', () => {
  it('copies the cases byte for byte and cites them', () => {
    expect(createHash('sha256').update(raw).digest('hex')).toBe(ADMISSION_READS_SHA256)
    const citation = JSON.parse(readFileSync(new URL('spec-citation.json', dir), 'utf8')) as {files: {copy: string; sha256: string; cases: number}[]}
    expect(citation.files.find(f => f.copy === 'admission-reads.json')).toMatchObject({sha256: ADMISSION_READS_SHA256, cases: cases.length})
    expect(cases.filter(c => c.senders.includes('library'))).toHaveLength(25)
  })

  for (const c of cases.filter(c => c.senders.includes('library'))) {
    const neverEmits = (c.applies.sender ?? '').startsWith('never emits')
    it(c.name, async () => {
      if (c.table === 4) {
        const bodies: string[] = []
        const principal = await open(c.readVersion, c.inputs.at, read => { bodies.push(canon(read)); return parsed(c.answer as string) as never })
        // The over-cap case describes 760 skills the library can never hold (skillsPerRun); it is run as a body never emitted.
        const call = c.inputs.call!
        const got = await admitCall(principal, {audience: c.inputs.audience, source: call.source, tool: call.tool, effect: call.effect, version: c.inputs.version, skills: () => Array.isArray(c.inputs.skills) ? c.inputs.skills : []}, call.count)
        expect([got?.name ?? 'admit', bodies.length]).toEqual([c.expect.sender, c.expect.reads])
        if (neverEmits) {
          expect(bodies[0]).not.toBe(c.body)
          expect(bodies[0]).toContain(`"readVersion":"${c.readVersion}"`)
        } else expect(bodies[0]).toBe(c.body)
        return
      }
      const answers = Array.isArray(c.answer) ? c.answer : [c.answer]
      const bodies: string[] = []
      const load: SkillLoadPort = read => { bodies.push(canon(read)); const text = answers[bodies.length - 1]; return (text == null ? null : parsed(text)) as never }
      const principal = c.inputs.chain === null ? undefined : await open(c.readVersion, c.inputs.at, admitAll, load)
      if (c.inputs.restore) {
        const run = gateOf(principal, c.inputs.restore, c.inputs.restore)
        await run.gate.holdDelegation()
        expect(bodies).toEqual(c.body)
        expect(ids(run.state)).toEqual(c.expect.loaded)
        expect(run.gate.current().map(s => s.id)).toEqual(c.expect.loaded)
        expect(run.refusal()?.refusal?.name).toBe(c.expect.sender)
        expect(run.rows[0]!.skills).toBe(0)
        return
      }
      const asked = c.inputs.load!
      const run = gateOf(principal, [asked.skill, ...asked.skills], asked.skills)
      let verdict = 'admit'
      try { await run.load(asked.skill.id) } catch { verdict = run.refusal()?.refusal?.name ?? 'refused' }
      expect([verdict, bodies.length, ids(run.state).includes(asked.skill.id)]).toEqual([c.expect.sender, c.expect.reads, c.expect.loaded])
      expect(run.rows[0]!.skills).toBe(1)
      if (bodies.length > 0) expect(bodies[0] === c.body).toBe(!neverEmits)
    })
  }
})

describe('read version "2" in the library (ADR-0030)', () => {
  const digest = `sha256:${'e'.repeat(64)}`
  const a = {id: 'expense-policy', digest: `sha256:${'a'.repeat(64)}`}, b = {id: 'issue-style', digest: `sha256:${'b'.repeat(64)}`}

  it('leaves "1" and no map unchanged byte for byte, with no skill-load read', async () => {
    const reads: string[] = []
    let loads = 0
    for (const version of [undefined, '1'] as const) {
      const principal = await open(version, '2026-10-30T01:00:00.000Z', read => { reads.push(canon(read)); return {admission: 'admit'} }, () => { loads++; return {load: 'admit'} })
      expect(principal.readVersion).toBe('1')
      await admitCall(principal, {audience: {kind: 'gateway', name: 'gateway:test'}, source: 'tickets/live', tool: 'query', effect: 'read-external', version: digest, skills: () => [a]}, true)
      await admitStanding(principal, 'read_context', 'read', undefined, () => [a])
      const run = gateOf(principal, [a], [])
      await run.load(a.id)
      expect(ids(run.state)).toEqual([a.id])
    }
    expect(loads).toBe(0)
    expect(reads[0]).toBe('{"at":"2026-10-30T01:00:00.000Z","audience":{"kind":"gateway","name":"gateway:test"},"call":{"count":true,"effect":"read-external","source":"tickets/live","tool":"query"},"chain":["sha256:2da3f48b6ab326b9664ead97c1c777c63e059ea90ef78ee0fe391c1c4f88e429"],"host":"desk","readVersion":"1"}')
    expect([reads[2], reads[3]]).toEqual([reads[0], reads[1]])
  })

  it('refuses a read version it does not name, and "2" with skills and no skill-load port', async () => {
    for (const readVersions of [{desk: '3'}, {desk: ''}, {'a b': '2'}]) {
      await expect(openPrincipal({chain: chainText, keys: statements.keys as never, software, audience: 'agent-host-1', admission: admitAll, readVersions: readVersions as never})).rejects.toBeInstanceOf(PrincipalInputInvalid)
    }
    const principal = await open('2', '2026-10-30T01:00:00.000Z')
    await expect(gateOf(principal, [a], []).gate.holdDelegation()).rejects.toThrow(/skill-load port/)
    await expect(gateOf(principal, [], []).gate.holdDelegation()).resolves.toBeUndefined()
  })

  it('reads a load after the budget claim and before the checkpoint write; a refusal is charged and loads nothing', async () => {
    let reads = 0
    let answer: unknown = {load: 'admit'}
    let held: ExecutionCheckpoint | undefined
    const principal = await open('2', '2026-10-30T01:00:00.000Z', admitAll, read => { reads++; expect(ids(held!)).not.toContain(read.load.skill.id); return answer as never })
    const run = gateOf(principal, [a, b], [], 2)
    held = run.state
    await expect(run.load('unknown')).rejects.toThrow()
    expect(reads).toBe(0)
    await run.load(a.id)
    await run.load(a.id)
    expect([reads, ids(run.state)]).toEqual([1, [a.id]])
    answer = {load: 'refuse', refusal: 'skill-refused'}
    await expect(run.load(b.id)).rejects.toThrow(/skill-refused/)
    expect([reads, ids(run.state), run.gate.current().length, run.rows[0]!.skills]).toEqual([2, [a.id], 1, 2])
    expect(run.refusal()).toMatchObject({reason: 'delegation refused', refusal: {name: 'skill-refused'}})
    expect(run.refusal()?.principal).toBeDefined()
    expect(run.events.some(e => e.type === 'skill_loaded' && (e.skill as {id: string}).id === b.id)).toBe(false)
    await expect(run.load(b.id)).rejects.toThrow()
    expect([reads, run.refusal()?.reason]).toEqual([2, 'skill budget'])
    const loop = gateOf(principal, [b], [], 4)
    reads = 0
    for (let i = 0; i < 9; i++) await loop.load(b.id).catch(() => undefined)
    expect([reads, loop.rows[0]!.skills]).toEqual([4, 4])
  })

  it('takes the loaded set under the gate lock', async () => {
    let release!: () => void
    let asked!: () => void
    const askedP = new Promise<void>(resolve => { asked = resolve })
    const principal = await open('2', '2026-10-30T01:00:00.000Z', admitAll, () => { asked(); return new Promise(resolve => { release = () => resolve({load: 'admit'}) }) })
    const run = gateOf(principal, [a], [])
    const loading = run.load(a.id)
    await askedP
    let seen: SkillRef[] | undefined
    const reading = run.gate.principalSkills().then(set => { seen = set })
    await new Promise(resolve => setTimeout(resolve, 20))
    expect(seen).toBeUndefined()
    release()
    await loading
    await reading
    expect(seen).toEqual([a])
  })

  it('keeps the child link request at "1" and names the parent its own loaded set', async () => {
    const standing: AdmissionRead[] = []
    const requests: ChildLinkRequest[] = []
    const parent = await open('2', '2026-10-30T01:00:00.000Z', read => { standing.push(read); return {admission: 'admit'} }, undefined, request => { requests.push(request); return {refusal: 'not-parent-actor'} })
    await expect(narrowChild(parent, {task: 'task-1', profile: 'review', tools: []}, undefined, () => [a])).rejects.toBeInstanceOf(DelegationRefused)
    expect(requests.map(r => r.readVersion)).toEqual(['1'])
    expect([standing[0]!.readVersion, standing[0]!.call.skills]).toEqual(['2', [a]])
  })
})

describe('descriptor snapshots of versions 3 and 4 (ADR-0030, ruling 6)', () => {
  type Version = {snapshotVersion: string; snapshotText: string; snapshotDigest: string; platform: string; binding: string; operation: 'live' | 'write'; endpoint: string | null; tools: Record<string, never>; descriptorDigests: Record<string, string>; refusals: {name: string; find: string; replace: string}[]; accepts: {name: string; find: string; replace: string}[]}
  const v = JSON.parse(readFileSync(new URL('../../contract/testdata/descriptor-snapshot/v3-v4.json', import.meta.url), 'utf8')) as {versions: Version[]}
  const bytes = (text: string) => new TextEncoder().encode(text)
  for (const version of v.versions) {
    it(`reads version ${version.snapshotVersion} as the gateway does`, async () => {
      const approval = {platform: version.platform, binding: version.binding, operation: version.operation, ...(version.endpoint ? {endpoint: version.endpoint} : {}), toolTaxonomies: version.tools}
      const accept = (text: string) => { try { return descriptorApprovalProblem(readDescriptorSnapshot(bytes(text)), approval) === '' } catch { return false } }
      const snapshot = readDescriptorSnapshot(bytes(version.snapshotText))
      expect([accept(version.snapshotText), await descriptorSnapshotDigest(bytes(version.snapshotText)), snapshot.snapshotVersion, snapshot.executable !== undefined]).toEqual([true, version.snapshotDigest, version.snapshotVersion, version.snapshotVersion === '4'])
      for (const [tool, digest] of Object.entries(version.descriptorDigests)) expect(snapshot.tools[tool]!.descriptorDigest).toBe(digest)
      for (const edit of version.accepts) expect([edit.name, accept(version.snapshotText.replace(edit.find, edit.replace))]).toEqual([edit.name, true])
      for (const edit of version.refusals) expect([edit.name, version.snapshotText.split(edit.find).length, accept(version.snapshotText.replace(edit.find, edit.replace))]).toEqual([edit.name, 2, false])
    })
  }
})

describe('a standing read at "2" the issuing host did not read (ADR-0030)', () => {
  for (const name of ['read-version-unsupported', 'read-malformed'] as const) {
    it(`reports ${name} after one read and never resends at "1"`, async () => {
      const versions: string[] = []
      const principal = await open('2', '2026-10-30T01:00:00.000Z', read => { versions.push(read.readVersion); return {refusal: name} })
      expect(await admitStanding(principal, 'read_context', 'read')).toEqual({name})
      expect(versions).toEqual(['2'])
    })
  }
})
