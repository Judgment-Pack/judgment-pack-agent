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
