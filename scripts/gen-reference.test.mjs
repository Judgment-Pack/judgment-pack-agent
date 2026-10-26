// Tests for scripts/gen-reference.mjs: generation is deterministic, and `--check` fails when a schema
// changes without the regenerated pages. Run with `node --test scripts/gen-reference.test.mjs`
// after `npm run build` (the generator reads packages/harness/dist).
import assert from 'node:assert/strict'
import {eventMatrix,requireCoverage} from './event-matrix/index.mjs'
import { spawnSync } from 'node:child_process'
import { cpSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs'
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

test('two runs write the same thirteen pages, byte for byte', () => {
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


test('approved API narratives are preserved beside generated references', () => {
  const out = join(scratch, 'report-narrative')
  assert.equal(run('--out', out).status, 0)
  const names = ['run-report.md', 'feedback.md', 'history-archive.md', 'history-capture.md', 'repeated-tools.md', 'task-steps.md']
  const narrative = '# Host API guidance\n\nHost-owned projection guidance.\n'
  for (const name of names) writeFileSync(join(out, name), narrative)
  const checked = run('--check', '--out', out)
  assert.equal(checked.status, 0, checked.stderr)
  for (const name of names) assert.equal(readFileSync(join(out, name), 'utf8'), narrative)
})


test('current record and feature guidance preserves published defaults and authority boundaries', () => {
  const out = join(scratch, 'current-records')
  const result = run('--out', out)
  assert.equal(result.status, 0, result.stderr)
  const records = readFileSync(join(out, 'records.md'), 'utf8')
  for (const row of ['| checkpoint | `2` | `3` |', '| event | `1` | `2` |', '| policy | `1` | `2` |']) assert.ok(records.includes(row), row)
  assert.match(records, /Report 2/)
  assert.match(records, /Report 1 remains frozen/)
  assert.match(records, /preserving the source checkpointVersion/)
  assert.match(records, /Neither report grants execution authority/)
  const checkpoint = readFileSync(join(out, 'checkpoint.md'), 'utf8')
  assert.match(checkpoint, /task schedules\/envelopes, usage budgets, per-turn routing, repeated-tool observations and runtime action approval/)
  assert.match(checkpoint, /cannot grant native resume, dispatch or approval authority/)
  assert.match(readFileSync(join(out, 'policy.md'), 'utf8'), /Current policy layers use version 2/)
})

test('event coverage rejects loss of the last emitter in either language', () => {
 const kinds=JSON.parse(readFileSync(join(schemasDir,'run-event.json'))).properties.type.enum
 const matrix=eventMatrix(repo,kinds)
 for(const language of Object.keys(matrix))for(const kind of kinds){
  const without={...matrix,[language]:{...matrix[language],emits:matrix[language].emits.filter(site=>site.kind!==kind)}}
  assert.throws(()=>requireCoverage(kinds,without),new RegExp(`${language} has no emitter for ${kind}`))
 }
 assert.ok(matrix.TypeScript.emits.some(s=>s.kind==='thinking_unavailable'&&s.file==='packages/harness/thinking.ts'))
 assert.ok(matrix.TypeScript.emits.some(s=>s.kind==='plan_drift_decided'&&s.file==='packages/harness/execution/controller.ts'))
 assert.ok(matrix.Go.emits.some(s=>s.kind==='plan_drift_decided'&&s.file==='go/context/plan_validation.go'))
 for(const language of Object.keys(matrix))for(const site of [...matrix[language].emits,...matrix[language].reads]){
  assert.ok(site.line>0)
  assert.doesNotMatch(site.file,/\.test\.ts$|_test\.go$|certification|examples|_gen\.go$/)
 }
 assert.ok(matrix.Go.reads.some(s=>s.kind==='execution_checkpoint'&&s.file==='go/context/run_report.go'))
})

test('event reference rejects stale emitter locations', () => {
 const out=join(scratch,'event-drift')
 assert.equal(run('--out',out).status,0)
 const path=join(out,'events.md')
 const original=readFileSync(path,'utf8')
 assert.match(original,/TypeScript emitters/)
 writeFileSync(path,original.replace(/packages\/harness\/[^`]+:[0-9]+/,'packages/harness/stale.ts:1'))
 const checked=run('--check','--out',out)
 assert.equal(checked.status,1,checked.stdout+checked.stderr)
 assert.match(checked.stderr,/events.md/)
})


test('syntax tracing ignores comments, unused literals and fixtures, and source removal fails coverage', () => {
 const root=join(scratch,'event-source')
 for(const dir of ['packages/harness','go','contract/schemas'])mkdirSync(join(root,dir),{recursive:true})
 const kinds=['message','end']
 writeFileSync(join(root,'contract/schemas/run-event.json'),JSON.stringify({properties:{type:{enum:kinds}}}))
 writeFileSync(join(root,'packages/harness/tsconfig.json'),JSON.stringify({compilerOptions:{target:'ES2022',strict:true},include:['*.ts']}))
 const tsSource=`type FixtureEvent = {type:'message';text:string}|{type:'end'}
function publish(event:FixtureEvent):void {}
// type: 'end' is a comment, not an emitter.
const unused = {type:'end'}
function factory():FixtureEvent { return {type:'message',text:'hello'} }
publish(factory())
publish({type:'end'})
`
 const tsPath=join(root,'packages/harness/source.ts')
 writeFileSync(tsPath,tsSource)
 writeFileSync(join(root,'packages/harness/unused.test.ts'),"publish({type:'end'})")
 const goSource=`package fixture
import "encoding/json"
type RunEvent struct { Message *RunEventMessage; End *RunEventEnd }
type RunEventMessage struct { Type string }
type RunEventEnd struct { Type string }
func emit(event RunEvent) {}
func unrelatedJSON() RunEvent {
 unused := "{\\\"type\\\":\\\"end\\\"}"
 _ = unused
 var unusedDecoded RunEvent
 _ = json.Unmarshal([]byte("{\\\"type\\\":\\\"end\\\"}"), &unusedDecoded)
 var event RunEvent
 _ = json.Unmarshal([]byte("{\\\"type\\\":\\\"message\\\"}"), &event)
 return event
}
func publishedJSON() {
 var published RunEvent
 alias := &published
 _ = json.Unmarshal([]byte("{\\\"type\\\":\\\"message\\\"}"), alias)
 emit(published)
}
func source() {
 unused := RunEvent{End:&RunEventEnd{Type:"end"}}
 _ = unused
 emit(RunEvent{Message:&RunEventMessage{Type:"message"}})
 emit(RunEvent{End:&RunEventEnd{Type:"end"}})
}
`
 const goPath=join(root,'go/source.go')
 writeFileSync(goPath,goSource)
 const matrix=eventMatrix(root,kinds)
 requireCoverage(kinds,matrix)
 assert.equal(matrix.TypeScript.emits.filter(s=>s.kind==='end').length,1)
 assert.equal(matrix.Go.emits.filter(s=>s.kind==='end').length,1)
 for(const destination of ['&event','alias']){
  const line=goSource.split('\n').findIndex(row=>row.includes('json.Unmarshal')&&row.endsWith(', '+destination+')'))+1
  assert.ok(line>0)
  assert.ok(matrix.Go.emits.some(site=>site.kind==='message'&&site.line===line),`decoded ${destination} must reach its returned/published event`)
 }
 writeFileSync(tsPath,tsSource.replace("publish({type:'end'})",''))
 assert.throws(()=>eventMatrix(root,kinds),/TypeScript has no emitter for end/)
 writeFileSync(tsPath,tsSource)
 writeFileSync(goPath,goSource.replace(' emit(RunEvent{End:&RunEventEnd{Type:"end"}})',''))
 assert.throws(()=>eventMatrix(root,kinds),/Go has no emitter for end/)
})


test('Go decoded event provenance distinguishes value snapshots and field instances', async (t) => {
 const root=join(scratch,'go-decoder-provenance')
 for(const dir of ['packages/harness','go','contract/schemas'])mkdirSync(join(root,dir),{recursive:true})
 const kinds=['message','end']
 writeFileSync(join(root,'contract/schemas/run-event.json'),JSON.stringify({properties:{type:{enum:kinds}}}))
 writeFileSync(join(root,'packages/harness/tsconfig.json'),JSON.stringify({compilerOptions:{target:'ES2022',strict:true},include:['*.ts']}))
 writeFileSync(join(root,'packages/harness/source.ts'),"type FixtureEvent={type:'message';text:string}|{type:'end'};function publish(event:FixtureEvent):void{};publish({type:'message',text:'x'});publish({type:'end'});")
 const endJSON='json.Unmarshal([]byte("{\\\"type\\\":\\\"end\\\"}"), '
 const cases=[
  ['copy-before-decode',`func decoded() RunEvent {var unused RunEvent;copied:=unused;_ = ${endJSON}&unused);return copied}`,false],
  ['other-field-instance',`type box struct {Event RunEvent}
func decoded() RunEvent {var unused, returned box;_ = ${endJSON}&unused.Event);return returned.Event}`,false],
  ['overwritten-after-decode',`func decoded() RunEvent {var event RunEvent;_ = ${endJSON}&event);event=RunEvent{};return event}`,false],
  ['dropped-decoded-return',`func decoded() RunEvent {var event RunEvent;_ = ${endJSON}&event);_ = event;return RunEvent{}}`,false],
  ['feature-stamped-decoded-return',`func StampFeatureEvent(event RunEvent) RunEvent {return event}
func decoded() RunEvent {var event RunEvent;_ = ${endJSON}&event);return StampFeatureEvent(event)}`,true],
  ['returned-after-decode',`func decoded() RunEvent {var event RunEvent;_ = ${endJSON}&event);copied:=event;return copied}`,true],
  ['returned-field',`type box struct {Event RunEvent}
func decoded() RunEvent {var returned box;_ = ${endJSON}&returned.Event);return returned.Event}`,true],
  ['published-pointer',`func decoded() {var event RunEvent;alias:=&event;second:=alias;_ = ${endJSON}second);emit(event)}`,true]
 ]
 for(const [name,body,decodedPublishes] of cases)await t.test(name,()=>{
  const prelude=`package fixture
import "encoding/json"
type RunEvent struct {Message *RunEventMessage;End *RunEventEnd}
type RunEventMessage struct {Type string}
type RunEventEnd struct {Type string}
func emit(event RunEvent) {}
func source(){emit(RunEvent{Message:&RunEventMessage{Type:"message"}})
 emit(RunEvent{End:&RunEventEnd{Type:"end"}})
}
`
  const path=join(root,'go/source.go'),source=prelude+body+'\n'
  writeFileSync(path,source)
  const compiled=spawnSync('go',['test',path],{encoding:'utf8',timeout:30000})
  assert.equal(compiled.status,0,`${name} must compile: ${compiled.stderr}`)
  const matrix=eventMatrix(root,kinds)
  assert.equal(matrix.Go.emits.filter(site=>site.kind==='end').length,decodedPublishes?2:1,`${name}: only a decoded value that reaches its sink may count`)
  writeFileSync(path,source.replace(' emit(RunEvent{End:&RunEventEnd{Type:"end"}})',''))
  if(decodedPublishes)requireCoverage(kinds,eventMatrix(root,kinds))
  else assert.throws(()=>eventMatrix(root,kinds),/Go has no emitter for end/,`${name}: removing the real emitter must refuse coverage`)
 })
})

// Handwritten versions of conditional route construction and the repeat writer's
// second-result payload flow. Each negative retains a real fallback emitter so
// its exact count, followed by removal of that fallback, checks the refusal gate.
test('Go constructed event flow follows conditional members and selected payload results', async (t) => {
 const root=join(scratch,'go-constructed-flow')
 for(const dir of ['packages/harness','go','contract/schemas'])mkdirSync(join(root,dir),{recursive:true})
 const kinds=['message','end']
 writeFileSync(join(root,'contract/schemas/run-event.json'),JSON.stringify({properties:{type:{enum:kinds}}}))
 writeFileSync(join(root,'packages/harness/tsconfig.json'),JSON.stringify({compilerOptions:{target:'ES2022',strict:true},include:['*.ts']}))
 writeFileSync(join(root,'packages/harness/source.ts'),"type FixtureEvent={type:'message';text:string}|{type:'end'};function publish(event:FixtureEvent):void{};publish({type:'message',text:'x'});publish({type:'end'});")
 const cases=[
  ['conditional-member',`func route(kind string) {var event RunEvent
 if kind == "end" {event.End=&RunEventEnd{Type:kind}} else {event.Message=&RunEventMessage{Type:kind}}
 _,_ = commit(event)
}
func invoke(){route("end");route("message")}`,true],
  ['second-result-payload',`func observe() (int,*RunEventEnd,error) {return 1,&RunEventEnd{Type:"end"},nil}
func repeat(){_,payload,_:=observe();_,_=commit(StampFeatureEvent(RunEvent{End:payload}))}`,true],
  ['unused-payload-factory',`func observe() (int,*RunEventEnd,error) {return 1,&RunEventEnd{Type:"end"},nil}
func repeat(){_,_=commit(RunEvent{})}`,false],
  ['dropped-payload-result',`func observe() (int,*RunEventEnd,error) {return 1,&RunEventEnd{Type:"end"},nil}
func repeat(){_,_,_=observe();_,_=commit(RunEvent{})}`,false],
  ['selected-empty-result',`func observe() (*RunEventEnd,*RunEventEnd) {return &RunEventEnd{Type:"end"},nil}
func repeat(){_,payload:=observe();_,_=commit(RunEvent{End:payload})}`,false],
  ['overwritten-member',`func route(){var event RunEvent;event.End=&RunEventEnd{Type:"end"};event.End=nil;_,_=commit(event)}`,false],
  ['overwritten-aggregate',`func route(){var event RunEvent;event.End=&RunEventEnd{Type:"end"};event=RunEvent{};_,_=commit(event)}`,false],
  ['other-instance',`func route(){var unused,event RunEvent;unused.End=&RunEventEnd{Type:"end"};_ = unused;_,_=commit(event)}`,false],
  ['copy-before-construction',`func route(){var original RunEvent;copied:=original;original.End=&RunEventEnd{Type:"end"};_,_=commit(copied)}`,false],
  ['publication-before-result-assignment',`func route(){var event RunEvent;event.End=&RunEventEnd{Type:"end"};event,_=commit(event)}`,true],
  ['copy-after-construction',`func route(){var original RunEvent;original.End=&RunEventEnd{Type:"end"};copied:=original;_,_=commit(copied)}`,true]
 ]
 const prelude=`package fixture
 type RunEvent struct {Message *RunEventMessage;End *RunEventEnd}
 type RunEventMessage struct {Type string}
 type RunEventEnd struct {Type string}
 func commit(event RunEvent) (RunEvent,error) {return event,nil}
 func StampFeatureEvent(event RunEvent) RunEvent {return event}
 func source(){_,_=commit(RunEvent{Message:&RunEventMessage{Type:"message"}})
 _,_=commit(RunEvent{End:&RunEventEnd{Type:"end"}})
 }
 `
 for(const [name,body,publishes] of cases)await t.test(name,()=>{
  const path=join(root,'go/source.go'),source=prelude+body+'\n'
  writeFileSync(path,source)
  const compiled=spawnSync('go',['test',path],{encoding:'utf8',timeout:30000})
  assert.equal(compiled.status,0,`${name} must compile: ${compiled.stderr}`)
  const matrix=eventMatrix(root,kinds)
  assert.equal(matrix.Go.emits.filter(site=>site.kind==='end').length,publishes?2:1,`${name}: only the constructed value at the publication point may count`)
  writeFileSync(path,source.replace(' _,_=commit(RunEvent{End:&RunEventEnd{Type:"end"}})',''))
  if(publishes)requireCoverage(kinds,eventMatrix(root,kinds))
  else assert.throws(()=>eventMatrix(root,kinds),/Go has no emitter for end/,`${name}: removing the fallback emitter must refuse coverage`)
 })
})
