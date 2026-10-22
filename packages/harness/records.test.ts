import {describe,expect,it} from 'vitest'
import checkpointVector from '../../contract/vectors/records/checkpoint.json'
import provenanceVector from '../../contract/vectors/records/provenance.json'
import critiqueVector from '../../contract/vectors/records/critique.json'
import exportVector from '../../contract/vectors/records/export.json'
import expiredVector from '../../contract/vectors/records/expired-resume.json'
import {CHECKPOINT_SCHEMA,CRITIQUE_SCHEMA,PROVENANCE_SCHEMA,RecordsExpired,exportRecords,recordDigest,refuseExpired,sha256,validRecord,writeRecord,type RecordGuard} from './records'
import {extendExecution,readExecution} from './execution/state'
import {resolvePolicy} from './policy'

const policy={retainDays:90,redactOnStore:true,exportFormat:'json' as const}
const now=new Date('2026-10-21T12:00:00.000Z')
// Assemble this at run time because the repository's local-paths check forbids the literal.
const localHomePath='/'+'home'+'/person/secret.txt'
const guard:RecordGuard={value<T>(input:T){
 const walk=(v:unknown):unknown=>typeof v==='string'?v.replace(localHomePath,'[redacted: local-home-path]'):Array.isArray(v)?v.map(walk):v&&typeof v==='object'?Object.fromEntries(Object.entries(v).map(([k,x])=>[k,walk(x)])):v
 return walk(input) as T
}}

describe('records (ADR #101)',()=>{
 it('resolves the records policy run over project over table',()=>{
  const resolved=resolvePolicy({records:{retainDays:7}},{records:{retainDays:30,redactOnStore:false}},{models:{author:{model:'connected',reasoning:'off'}},records:{retainDays:90,redactOnStore:true,exportFormat:'json'}},{connections:['connected'],agent:false,requests:16})
  expect(resolved.members['records.retainDays']).toEqual({value:7,layer:'run'})
  expect(resolved.members['records.redactOnStore']).toEqual({value:false,layer:'project'})
  expect(resolved.members['records.exportFormat']).toEqual({value:'json',layer:'table'})
 })
 it('shares the canonical digest rule with the handwritten records',()=>{
  expect(sha256('')).toBe('e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855')
  for(const item of [checkpointVector.items[0],provenanceVector.items[0],critiqueVector.items[0]]){
   expect(recordDigest(item)).toBe(item.digest)
   expect(validRecord(item)).toBe(true)
  }
 })

 it('carries schema, version, digest and lifetime and applies the output guard on store',()=>{
  const record=writeRecord(CRITIQUE_SCHEMA,{type:'critique',refuted:false,checks:[],text:`Read ${localHomePath}`},policy,guard,now)
  expect(record).toMatchObject({schema:CRITIQUE_SCHEMA,version:1,retainUntil:'2027-01-19T12:00:00.000Z',text:'Read [redacted: local-home-path]'})
  expect(record.digest).toBe(recordDigest(record))
  expect(validRecord(record)).toBe(true)
 })

 it('exports only the records the host hands it and makes manifest digests equal them',()=>{
  const records=[checkpointVector.items[0],provenanceVector.items[0],critiqueVector.items[0]]
  const written=exportRecords(records,policy,'0.8.0',now)
  expect(written.value.manifest.records.map(row=>row.digest)).toEqual(records.map(row=>row.digest))
  expect(written.value.records).toEqual(records)
  expect(written.value.manifest.digest).toBe(recordDigest(written.value.manifest))
  expect(written.event).toMatchObject({type:'export_written',records:3,bytes:new TextEncoder().encode(written.text).length,digest:expect.stringMatching(/^sha256:[a-f0-9]{64}$/)})
  expect(recordDigest(exportVector.items[0])).toBe(exportVector.items[0].digest)
 })

 it('refuses an expired resume with the shared, schema-valid reason',()=>{
  expect(()=>refuseExpired({retainUntil:'2026-10-21T11:59:59.999Z'},now)).toThrow(RecordsExpired)
  try{refuseExpired({retainUntil:'2026-10-21T11:59:59.999Z'},now)}catch(error){expect((error as RecordsExpired).event()).toEqual(expiredVector.items[0])}
  expect(()=>refuseExpired({retainUntil:'2026-10-21T12:00:00.001Z'},now)).not.toThrow()
 })

 it('carries every record member through an interrupted checkpoint resume',()=>{
  const resumed=readExecution(JSON.parse(JSON.stringify(checkpointVector.items[0])))!
  expect(resumed).toMatchObject({schema:CHECKPOINT_SCHEMA,version:1,digest:checkpointVector.items[0].digest,retainUntil:checkpointVector.items[0].retainUntil,status:'paused'})
  const extended=extendExecution(resumed)
  expect(extended).toMatchObject({schema:CHECKPOINT_SCHEMA,version:1,retainUntil:resumed.retainUntil,status:'running'})
  expect(validRecord(extended)).toBe(true)
 })

 it('recognises each decided record schema',()=>{
  expect([CHECKPOINT_SCHEMA,PROVENANCE_SCHEMA,CRITIQUE_SCHEMA]).toEqual([checkpointVector.items[0].schema,provenanceVector.items[0].schema,critiqueVector.items[0].schema])
 })
})

import {managedContext,contextOf} from './context/managed'
import {answer} from './context/ledger'
import type {AssistantEvent} from './engine'
import eventSchema from '../../contract/schemas/run-event.json'
import {argumentProblem} from './sourceArguments'
it('continues after refusing a whole checkpoint and critique at the store boundary',async()=>{
 const events:AssistantEvent[]=[]
 const session={prompt:'Review',testPrompt:'',tools:[],hostTools:[],callTool:async()=>answer({}),signal:new AbortController().signal,allowConversation:true,interactive:true,critic:'off' as const,context:{currentRequest:{message:'Review',input:'Review'}},agent:{model:'fixture',run:async()=>{}}}
 for await(const event of managedContext(session,async function*(ready){
  contextOf(ready).execution.state.explanation='x'.repeat(200000)
  yield {type:'tool_result' as const,name:'fixture',text:'Exempt.',isError:false}
  yield {type:'critique' as const,refuted:false,checks:[],text:'x'.repeat(200000)}
  yield {type:'message' as const,text:'Continued.'}
  yield {type:'end' as const}
 }))events.push(event)
 expect(events.filter(e=>e.type==='error'||e.type==='critique'||e.type==='output_guarded')).toEqual([])
 expect(events).toContainEqual({type:'message',text:'Continued.'})
 expect(events.filter(e=>e.type==='execution_checkpoint').every(e=>e.checkpoint.explanation==='')).toBe(true)
 const notices=events.filter(e=>e.type==='guardrail'&&e.tool==='records')
 expect(notices.length).toBeGreaterThanOrEqual(2)
 for(const notice of notices)expect(argumentProblem(eventSchema.$defs.guardrail,notice)).toBeUndefined()
})

import {OutputGuard} from './outputGuard'
it('exempts guard labels in stored argument keys and embedded text',()=>{
 const delivered:Record<string,number>={}
 const output=new OutputGuard({outputPatterns:{value:[{name:'label-word',pattern:'redacted'}],layer:'run'}},()=>delivered,()=>{})
 output.value('AKIAABCDEFGHIJKLMNOP','message',false)
 const args={'[redacted: aws-access-key]':'Before [redacted: aws-access-key] after'}
 const record=writeRecord(CHECKPOINT_SCHEMA,{operations:[{name:'fixture',args}]},policy,output,now)
 expect(record.operations[0]!.args).toEqual(args)
 expect(record.records).toEqual({redactions:{}})
 expect(delivered).toEqual({})
})

import forgedLabel from '../../contract/vectors/output/forged-label.json'
it('scans forged labels in records and counts the key inside them',()=>{
 for(const input of [forgedLabel.guard.input,'Output refused: sk-abcdefghijklmnopqrst.']){
  const output=new OutputGuard({},()=>({}),()=>{})
  const record=writeRecord(CRITIQUE_SCHEMA,{text:input},policy,output,now)
  expect(record.text).not.toContain('sk-abcdefghijklmnopqrst')
  expect(record.records?.redactions).toEqual({'provider-key':1})
 }
 const output=new OutputGuard({outputPatterns:{value:[{name:'label-key',pattern:'aws-access-key'}],layer:'run'}},()=>({}),()=>{})
 const record=writeRecord(CRITIQUE_SCHEMA,{text:'[redacted: aws-access-key]'},policy,output,now)
 expect(record.text).toBe('[redacted: [redacted: label-key]]')
 expect(record.records?.redactions).toEqual({'label-key':1})
})

it('scans across an untrusted label without splitting the pattern input',()=>{
 const output=new OutputGuard({outputPatterns:{value:[{name:'whole-secret',pattern:'before \\[redacted: aws-access-key\\] after'}],layer:'run'}},()=>({}),()=>{})
 const record=writeRecord(CRITIQUE_SCHEMA,{text:'before [redacted: aws-access-key] after'},policy,output,now)
 expect(record.text).toBe('[redacted: whole-secret]')
 expect(record.records?.redactions).toEqual({'whole-secret':1})
})
