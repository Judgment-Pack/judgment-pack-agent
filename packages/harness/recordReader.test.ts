import {describe,it,expect} from 'vitest'
import {createHash} from 'node:crypto'
import {readFileSync} from 'node:fs'
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
 it('keeps the historical inventory separate from current additions',()=>{
  const properties=shapes.current['checkpoint.json'].properties as Record<string,unknown>
  properties.unreleased={}
  try{expect(()=>readExecution({...checkpoint.items[0],unreleased:true})).toThrow(UnknownRecordMember);expect(readExecution({...checkpoint.items[1],unreleased:true})).toHaveProperty('unreleased',true)}finally{delete properties.unreleased}
 })
 it('freezes the historical member inventory',()=>{
  const raw=readFileSync('../../contract/records/historical-shapes.json')
  expect(createHash('sha256').update(raw).digest('hex')).toBe('71e8ab3df9fa5daf051e3913f90ab62b577932d3a0ba5c0fa966b59a54a43925')
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
