import {describe,it,expect} from 'vitest'
import {createHash} from 'node:crypto'
import {readFileSync,mkdirSync,mkdtempSync,writeFileSync} from 'node:fs'
import {tmpdir} from 'node:os'
import {join} from 'node:path'
import {spawnSync} from 'node:child_process'
import checkpoint from '../../contract/vectors/record-versions/checkpoint.json'
import events from '../../contract/vectors/record-versions/events.json'
import policy from '../../contract/vectors/record-versions/policy.json'
import shapes from './contract/record-shapes.json'
import {checkRecord,readRunEvent,readPolicyLayer,UnsupportedRecordVersion,UnknownRecordMember,InvalidRecord,writeRunEvent,versionEvents,type RecordKind} from './recordReader'
import {readExecution} from './execution/state'
import {writeRecord,recordDigest,exportRecords,CHECKPOINT_SCHEMA,RECORD_DEFAULTS} from './records'
import {canonicalJSON} from './context/chat'
import {resolvePolicy} from './policy'
const readers={checkpoint:readExecution,event:readRunEvent,policy:readPolicyLayer}
describe('versioned record readers',()=>{
 for(const [kind,vector] of [['checkpoint',checkpoint],['event',events],['policy',policy]] as const){
  for(const row of vector.cases)it(`${kind}: ${row.name}`,()=>{
   const value='raw' in row?JSON.parse(row.raw as string):structuredClone(row.input),before=JSON.stringify(value)
   if(row.outcome==='accepted')expect(readers[kind](value)).toEqual(value)
   else if(row.outcome==='InvalidRecord')expect(()=>readers[kind](value)).toThrow(InvalidRecord)
   else expect(()=>readers[kind](value)).toThrow(row.outcome==='UnsupportedRecordVersion'?UnsupportedRecordVersion:UnknownRecordMember)
   expect(JSON.stringify(value)).toBe(before)
  })
  it(`${kind}: consumes every positive shared vector`,()=>{for(const value of vector.items)expect(()=>checkRecord(kind,value)).not.toThrow()})
 }
 it('bounds structural visits and depth',()=>{
  expect(()=>readPolicyLayer({version:1,outputAllow:Array(200001).fill('kept')})).toThrow()
  const properties=shapes.current['policy.json'].properties as Record<string,unknown>
  properties.next={$ref:'policy.json'}
  let value:Record<string,unknown>={version:1}
  for(let i=0;i<100;i++)value={version:1,next:value}
  try{expect(()=>readPolicyLayer(value)).toThrow()}finally{delete properties.next}
 })
 it('keeps the checkpoint size bound separate from ordinary events',()=>{
  const text='x'.repeat(4*1024*1024+1)
  expect(readRunEvent({version:1,type:'message',text})).toEqual({version:1,type:'message',text})
  expect(()=>readExecution({...checkpoint.items[1],explanation:text})).toThrow()
 })
 it('keeps every published inventory separate from current additions',()=>{
  const properties=shapes.current['checkpoint.json'].properties as Record<string,unknown>
  properties.unreleased={}
  try{expect(()=>readExecution({...checkpoint.items[0],unreleased:true})).toThrow(UnknownRecordMember);expect(()=>readExecution({...checkpoint.items[1],unreleased:true})).toThrow(UnknownRecordMember)}finally{delete properties.unreleased}
 })
 it('keeps checkpoint1 on the historical graph when published members grow',()=>{
  const properties=shapes.published['checkpoint.json'].properties as Record<string,unknown>
  properties.unreleased={}
  try{
   expect(()=>readExecution({...checkpoint.items[0],unreleased:true})).toThrow(UnknownRecordMember)
   expect(()=>readExecution({...checkpoint.items[1],unreleased:true})).not.toThrow()
  }finally{delete properties.unreleased}
 })
 it('freezes the historical member inventory',()=>{
  const raw=readFileSync('../../contract/records/historical-shapes.json')
  expect(createHash('sha256').update(raw).digest('hex')).toBe('71e8ab3df9fa5daf051e3913f90ab62b577932d3a0ba5c0fa966b59a54a43925')
 })
 it('freezes the published checkpoint2/event1/policy1 graph',()=>{
  const raw=readFileSync('../../contract/records/published-v010-shapes.json')
  expect(createHash('sha256').update(raw).digest('hex')).toBe('bc2c49c0340af12c3f016b772ab90c5a0c834a2667605027d70ef7ac6c438df0')
  for(const [kind,file,input] of [['event','run-event.json',{version:1,type:'end',unreleased:true}],['policy','policy.json',{version:1,unreleased:true}]] as const){
   const properties=shapes.current[file].properties as Record<string,unknown>;properties.unreleased={}
   try{expect(()=>checkRecord(kind,input)).toThrow(UnknownRecordMember)}finally{delete properties.unreleased}
  }
 })
 it('refuses coordinated widening of every published graph copy',()=>{
  const root=mkdtempSync(join(tmpdir(),'published-v010-guard-'));mkdirSync(join(root,'contract/records'),{recursive:true});mkdirSync(join(root,'scripts'),{recursive:true})
  const frozen=JSON.parse(readFileSync('../../contract/records/published-v010-shapes.json','utf8'));frozen.graph['checkpoint.json'].properties.future={type:'string'}
  writeFileSync(join(root,'contract/records/published-v010-shapes.json'),JSON.stringify(frozen)+'\n')
  for(const path of ['packages/harness/contract','go/contract']){mkdirSync(join(root,path),{recursive:true});writeFileSync(join(root,path,path.startsWith('go')?'record_shapes.json':'record-shapes.json'),JSON.stringify({...shapes,published:frozen.graph})+'\n')}
  writeFileSync(join(root,'scripts/published-record-shapes.mjs'),readFileSync('../../scripts/published-record-shapes.mjs'))
  const result=spawnSync(process.execPath,['scripts/published-record-shapes.mjs'],{cwd:root,encoding:'utf8'})
  expect(result.status).not.toBe(0);expect(result.stderr).toContain('Published v0.10 record graph digest differs.')
 })
 it('retains lifecycle metadata on read/export and writes the current version before digest',()=>{
  const legacy={...checkpoint.items[0],policy:{pace:{value:'steady',layer:'run'}},schema:CHECKPOINT_SCHEMA,retainUntil:'2099-01-01T00:00:00.000Z',digest:''}
  legacy.digest=recordDigest(legacy)
  expect(readExecution(legacy)).toEqual(legacy)
  const text=canonicalJSON(legacy)
  const exported=exportRecords([legacy],RECORD_DEFAULTS,'test')
  expect(canonicalJSON(exported.value.records[0]!)).toBe(text)
  expect(exported.value.manifest.records[0]!.version).toBe(1)
  const written=writeRecord(CHECKPOINT_SCHEMA,legacy,RECORD_DEFAULTS,undefined,new Date(),legacy.retainUntil)
  expect(written.version).toBe(2);expect(written.retainUntil).toBe(legacy.retainUntil);expect(written.digest).toBe(recordDigest(written))
  expect(exportRecords([written],RECORD_DEFAULTS,'test').value.manifest.records[0]!.version).toBe(2)
 })
 it('carries every named provenance member through a read',()=>{
  const value={...checkpoint.items[0],used:1,completions:[{request:1,phase:'work',result:{finishReason:'stop'},connection:'primary',role:'author'}]}
  expect(readExecution(value)).toEqual(value)
 })
 it('keeps a synchronous iterator failure as a rejected read',async()=>{
  const cause=Error('source failed')
  const source={ [Symbol.asyncIterator](){return {next(){throw cause}}} }
  const iterator=versionEvents(source)[Symbol.asyncIterator]()
  await expect(iterator.next()).rejects.toBe(cause)
 })
 it.each(['return','throw'] as const)('forwards %s immediately while an event read is pending',async method=>{
  let release!:(step:IteratorResult<import('./engine').AssistantEvent>)=>void
  const pending=new Promise<IteratorResult<import('./engine').AssistantEvent>>(resolve=>{release=resolve})
  const calls:string[]=[]
  const cause=Error('consumer closed')
  const source={
   [Symbol.asyncIterator](){return this},
   next:()=>{calls.push('next');return pending},
   return:()=>{calls.push('return');release({done:true,value:undefined});return Promise.resolve({done:true as const,value:undefined})},
   throw:(error?:unknown)=>{calls.push('throw');release({done:true,value:undefined});return Promise.reject(error)}
  }
  const iterator=versionEvents(source)[Symbol.asyncIterator](),read=iterator.next()
  const closed=method==='return'?iterator.return!():iterator.throw!(cause)
  expect(calls).toEqual(['next',method]) // no turn of the event loop or pending read is needed to close
  expect(await read).toEqual({done:true,value:undefined})
  if(method==='return')expect(await closed).toEqual({done:true,value:undefined})
  else await expect(closed).rejects.toBe(cause)
 })
 it('stamps every event kind and queued/failure notices at the writer seam',async()=>{
  async function* input(){for(const event of events.items)yield readRunEvent(event)}
  const emitted=[];for await(const event of versionEvents(input()))emitted.push(event)
  expect(emitted).toHaveLength(events.items.length);expect(emitted.every(e=>e.version===1)).toBe(true)
  for(const event of emitted)expect(readRunEvent(event)).toEqual(event)
  expect(writeRunEvent({type:'end'})).toEqual({version:1,type:'end'})
 })
 it('keeps policy version admission outside the value fallback for every layer',()=>{
  const host={connections:['primary'],agent:false,requests:2}
  const table={models:{author:{model:'primary',reasoning:'off'}}}
  for(const layer of [0,1,2]){
   for(const version of [0,-1,1.5,null,'1',9007199254740992]){
    const inputs:[import('./policy').PolicyLayer,import('./policy').PolicyLayer,import('./policy').PolicyLayer]=[{},{},table]
    inputs[layer]={...inputs[layer],version} as never
    expect(()=>resolvePolicy(...inputs,host)).toThrow(InvalidRecord)
   }
   const inputs:[import('./policy').PolicyLayer,import('./policy').PolicyLayer,import('./policy').PolicyLayer]=[{},{},table]
   inputs[layer]={version:3,readBytes:1} as never
   expect(()=>resolvePolicy(...inputs,host)).toThrow(UnsupportedRecordVersion)
  }
 })
 it('does not turn policy version metadata into an admitted policy value',()=>{
  const host={connections:['primary'],agent:false,requests:2}
  const table={models:{author:{model:'primary',reasoning:'off'}}}
  const a=resolvePolicy({}, {}, table,host),b=resolvePolicy({version:1},{version:1},{...table,version:1},host)
  expect(b).toEqual(a);expect(b.members).not.toHaveProperty('version')
 })
 for(const kind of ['checkpoint','event','policy'] as RecordKind[])it(`${kind} newer refusal has a bounded schema-valid event`,()=>{
  for(const value of [{version:100},{version:100,payload:'x'.repeat(10000)}]){
   try{checkRecord(kind,value);throw Error('accepted future')}catch(error){expect(error).toBeInstanceOf(UnsupportedRecordVersion);const event=(error as UnsupportedRecordVersion).event();expect(()=>readRunEvent(event)).not.toThrow();expect(event.detail.length).toBeLessThan(100)}
  }
 })
})
