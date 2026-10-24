import {describe,it,expect} from 'vitest'
import {MemoryRunStore,checkpointCallbackRunStore} from './run'
import type {RunCommit,RunStore} from './run'
import {MemoryChatStore} from './chat'
import {chatStoreConformance,chatStoreFaultConformance,runStoreConformance,runStoreFaultConformance,StorageConformanceFailure} from './conformance'
import {StorageCommitMismatch,StorageConflict,StorageCursorInvalid,StorageInputError,STORAGE_RECORD_BYTES,STORAGE_TRANSACTION_BYTES} from './common'
const bytes=(value:string)=>new TextEncoder().encode(value)
const text=(value:Uint8Array|null|undefined)=>value?new TextDecoder().decode(value):undefined
const opening=():RunCommit=>({runId:'opaque-run',expectedRevision:null,commitId:'opening',checkpoint:bytes('{"id":"opaque-run"}'),events:[bytes('{"type":"opened"}')]})
const seeded=()=>new MemoryChatStore([{chat:{id:'chat-alpha',interaction:'plan',hostOnly:{nested:[null,0,'kept']},plans:[{futurePlanField:{exact:'kept'}}]}},{chat:{id:'chat-beta',pack:{id:'opaque-artifact'}},kind:'draft'}])
describe('storage kinds',()=>{
 it('runs the reusable RunStore conformance properties',async()=>{await runStoreConformance(()=>new MemoryRunStore())})
 it('runs the reusable ChatStore conformance properties',async()=>{await chatStoreConformance(seeded)})
 it('runs parameterized physical-fault conformance for both ports',async()=>{
  await runStoreFaultConformance(phase=>{let armed=true;const fault=()=>{if(armed){armed=false;throw new Error('injected commit fault')}};return new MemoryRunStore(phase==='before'?{beforeCommit:fault}:{afterCommit:fault})})
  await chatStoreFaultConformance(phase=>{let armed=true;const fault=()=>{if(armed){armed=false;throw new Error('injected chat fault')}};return new MemoryChatStore([{chat:{id:'chat-alpha',interaction:'plan',hostOnly:{exact:true}}}],phase==='before'?{beforeUpdate:fault}:{afterUpdate:fault})})
 })
 it('names a preservation property when a host loses stored bytes',async()=>{
  const make=():RunStore=>{
   const actual=new MemoryRunStore()
   return {durability:'memory',load:async id=>{const head=await actual.load(id);return head?{...head,checkpoint:bytes('{}')}:undefined},readEvents:actual.readEvents.bind(actual),commit:actual.commit.bind(actual)}
  }
  await expect(runStoreConformance(make)).rejects.toMatchObject({property:'run.bytes.preserved'})
  await expect(runStoreConformance(make)).rejects.toBeInstanceOf(StorageConformanceFailure)
 })
 it('leaves head, receipt and events absent on a precommit fault',async()=>{
  let fail=true
  const store=new MemoryRunStore({beforeCommit:()=>{if(fail)throw new Error('injected precommit')}})
  await expect(store.commit(opening())).rejects.toThrow('injected precommit')
  expect(await store.load('opaque-run')).toBeUndefined();expect((await store.readEvents('opaque-run',null,1)).items).toEqual([])
  fail=false;await store.commit(opening());expect((await store.readEvents('opaque-run',null,1)).items).toHaveLength(1)
 })
 it('resolves a lost acknowledgement by an identical receipt without duplicate events',async()=>{
  const store=new MemoryRunStore({afterCommit:()=>{throw new Error('lost acknowledgement')}})
  await expect(store.commit(opening())).rejects.toThrow('lost acknowledgement')
  const committed=await store.load('opaque-run');expect(committed).toBeDefined()
  const retry=await store.commit(opening());expect(retry.revision).toBe(committed!.revision)
  expect((await store.readEvents('opaque-run',null,100)).items).toHaveLength(1)
  await expect(store.commit({...opening(),checkpoint:bytes('{"different":true}')})).rejects.toBeInstanceOf(StorageCommitMismatch)
 })
 it('freezes queued inputs and refuses a cancelled queued commit before its callback',async()=>{
  let release!:()=>void;const wait=new Promise<void>(resolve=>{release=resolve}),seen:string[]=[]
  const store=checkpointCallbackRunStore<string>(async value=>{seen.push(value);await wait},raw=>text(raw)!)
  const input=opening(),first=store.commit(input)
  input.checkpoint![0]=32;input.events[0]![0]=32
  const controller=new AbortController(),queued=store.commit({...opening(),runId:'second'},controller.signal)
  controller.abort();release();await first
  await expect(queued).rejects.toThrow();expect(seen).toEqual(['{"id":"opaque-run"}'])
  expect(text((await store.load('opaque-run'))?.checkpoint)).toBe('{"id":"opaque-run"}')
 })
 it('marks callback limits explicitly and publishes only after acknowledgement',async()=>{
  let release!:()=>void;const wait=new Promise<void>(resolve=>{release=resolve})
  const store=checkpointCallbackRunStore(async value=>{expect(value).toEqual({id:'opaque-run'});await wait},raw=>JSON.parse(text(raw)!))
  expect(store.durability).toBe('single-writer-callback')
  const pending=store.commit(opening());await Promise.resolve();await Promise.resolve()
  expect(await store.load('opaque-run')).toBeUndefined();release();await pending
  await expect(runStoreConformance(()=>store)).rejects.toMatchObject({property:'run.durability.declared'})
 })
 it('rechecks cancellation after a precommit wait before invoking the save callback',async()=>{
  let release!:()=>void,entered!:()=>void
  const wait=new Promise<void>(resolve=>{release=resolve}),started=new Promise<void>(resolve=>{entered=resolve}),controller=new AbortController()
  let saved=false
  const store=new MemoryRunStore({beforeCommit:async()=>{entered();await wait}},async()=>{saved=true})
  const pending=store.commit(opening(),controller.signal);await started;controller.abort();release()
  await expect(pending).rejects.toThrow();expect(saved).toBe(false);expect(await store.load('opaque-run')).toBeUndefined()
 })
 it('names unexpected host refusals as a bounded conformance property',async()=>{
  const store=new MemoryRunStore();store.commit=async()=>{throw new Error('host record content')}
  await expect(runStoreConformance(()=>store)).rejects.toMatchObject({property:'run.operation',message:'Storage conformance failed: run.operation'})
 })
 it('does not publish the callback adapter state after a rejected save' ,async()=>{
  const store=checkpointCallbackRunStore(async()=>{throw new Error('save failed')},text)
  await expect(store.commit(opening())).rejects.toThrow('save failed')
  expect(await store.load('opaque-run')).toBeUndefined()
 })
 it('returns bounded errors without including attacker-controlled identifiers or bodies',async()=>{
  const store=new MemoryRunStore(),receipt=await store.commit(opening())
  await expect(store.commit({...opening(),commitId:'conflict',checkpoint:bytes('secret payload')})).rejects.toMatchObject({name:'StorageConflict',message:'StorageConflict: The run changed; no storage write was applied.',actualRevision:receipt.revision})
  for(const bad of [null as never,{...opening(),events:Array(1) as Uint8Array[]},{...opening(),runId:''},{...opening(),commitId:'x'.repeat(257)},{...opening(),checkpoint:new Uint8Array([255])},{...opening(),checkpoint:bytes('x'.repeat(STORAGE_RECORD_BYTES+1))},{...opening(),checkpoint:null,events:[]},{...opening(),checkpoint:bytes('x'.repeat(STORAGE_RECORD_BYTES)),events:[bytes('x'.repeat(STORAGE_RECORD_BYTES)),bytes('x')]}])await expect(store.commit(bad)).rejects.toBeInstanceOf(StorageInputError)
  for(const limit of [0,101,1.5,NaN])await expect(store.readEvents('opaque-run',null,limit)).rejects.toBeInstanceOf(StorageInputError)
  for(const cursor of ['missing','\n'])await expect(store.readEvents('opaque-run',cursor,1)).rejects.toBeInstanceOf(StorageCursorInvalid)
  expect((await store.load('opaque-run'))?.revision).toBe(receipt.revision)
 })
 it('bounds event page bytes as well as count and exposes only a committed prefix',async()=>{
  const store=new MemoryRunStore(),big=bytes('x'.repeat(STORAGE_RECORD_BYTES)),first=await store.commit({...opening(),checkpoint:null,events:[big,big]})
  await store.commit({...opening(),expectedRevision:first.revision,commitId:'extra',checkpoint:null,events:[bytes('tail')]})
  const page=await store.readEvents('opaque-run',null,100)
  expect(page.items.reduce((sum,event)=>sum+event.bytes.length,0)).toBe(STORAGE_TRANSACTION_BYTES);expect(page.hasMore).toBe(true)
  expect(text((await store.readEvents('opaque-run',page.nextCursor,100)).items[0]?.bytes)).toBe('tail')
 })
 it('keeps chat listing snapshots stable across updates and scopes their cursors',async()=>{
  const store=seeded(),page=await store.list({limit:1}),beta=await store.get('chat-beta')
  await store.update('chat-beta',{interaction:'work'},beta!.revision)
  expect((await store.list({limit:1,cursor:page.nextCursor})).items[0]?.chat.interaction).toBeUndefined()
  expect((await store.get('chat-beta'))?.chat.interaction).toBe('work')
  await expect(store.list({limit:1,cursor:'invented'})).rejects.toBeInstanceOf(StorageCursorInvalid)
 })
 it('detaches chat patches, preserves JSON payloads and rejects lossy or forbidden updates atomically',async()=>{
  const store=seeded(),original=(await store.get('chat-alpha'))!,patch={plans:[{opaquePayload:{future:[null,0,'value']}}]}
  const updated=await store.update('chat-alpha',patch,original.revision);patch.plans[0]!.opaquePayload.future[2]='changed'
  expect((await store.get('chat-alpha'))?.chat.plans).toEqual([{opaquePayload:{future:[null,0,'value']}}])
  const deep:unknown[]=[];let nested=deep;for(let i=0;i<130;i++){const child:unknown[]=[];nested.push(child);nested=child}
  for(const invalid of [{plans:deep},{}, {id:'replacement'},{plans:[undefined]},{plans:[NaN]},{plans:[new Date()]},{interaction:undefined}])await expect(store.update('chat-alpha',invalid as never,updated.revision)).rejects.toBeInstanceOf(StorageInputError)
  expect((await store.get('chat-alpha'))?.revision).toBe(updated.revision)
 })
 it('does not infer identity from locators or share cursors between host scopes',async()=>{
  const a=new MemoryRunStore(),b=new MemoryRunStore(),receipt=await a.commit(opening())
  await expect(b.readEvents('opaque-run',receipt.cursor,1)).rejects.toBeInstanceOf(StorageCursorInvalid)
  const token=await b.commit(opening());expect(token.revision).not.toBe(receipt.revision)
  expect(text((await b.load('opaque-run'))?.checkpoint)).toBe(text((await a.load('opaque-run'))?.checkpoint))
  await expect(a.commit({...opening(),commitId:'stale',expectedRevision:token.revision})).rejects.toBeInstanceOf(StorageConflict)
 })
})
