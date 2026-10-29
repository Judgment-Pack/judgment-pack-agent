import {describe,it,expect,vi} from 'vitest'
import type {AssistantSession,HostTool} from '../engine'
import {ChatContext} from '../context/chat'
import {StoredChatScope,restoreStoredChatScope} from '../context/storedChat'
import {toolListDigest} from '../toolSources'
import {contextualSession} from '../context/session'
import {canonicalJSON} from '../context/chat'
import {MemoryRunStore,checkpointCallbackRunStore,type RunStore} from './run'
import {MemoryChatStore} from './chat'
import {HostOutcomeUnknown,durableSession,durableOwner,type DurableBinding} from './writer'
import {runStoreLeaseConformance,StorageConformanceFailure} from './conformance'
import {certifyEffectHost,effectHostConformance,type EffectAdmission,type LeaseRunStore,LEASE_CAPABILITY_REASONS,LEASE_EPOCH_MAX,LEASE_REFUSAL_REASONS,LeaseCapabilityRefused,LeaseRefused,ReferenceEffectHost,admitUnattended,compareLeaseEpochs,effectAdmission,effectIdempotencyKey,fenceOf,fencedChatStore,fencedRunStore,nextLeaseEpoch,readEffectAdmission,readLeaseCapabilityRefusal,readLeaseFence,readLeaseRefusal,readRunLease,unattendedRunStore,type EffectHost,type LeaseFence,type LeaseLifetime} from './lease'
import leaseVector from '../../../contract/vectors/lease/lease.json'
import fenceVector from '../../../contract/vectors/lease/fence.json'
import admissionVector from '../../../contract/vectors/lease/admission.json'
import refusalVector from '../../../contract/vectors/lease/refusal.json'
import capabilityVector from '../../../contract/vectors/lease/capability.json'

const T0=Date.parse('2026-01-01T00:00:00.000Z'),MINUTE=60_000
const refusal=(reason:string)=>expect.objectContaining({name:'LeaseRefused',reason})
const capability=(reason:string)=>expect.objectContaining({name:'LeaseCapabilityRefused',reason})
/** The error, or the cause it carries: a writer wraps a refused commit as an unknown storage outcome. */
const named=(error:unknown):unknown=>error instanceof LeaseRefused||error instanceof LeaseCapabilityRefused?error:error&&typeof error==='object'&&'cause' in error?named((error as {cause:unknown}).cause):error
async function refused(run:Promise<unknown>):Promise<unknown>{try{await run}catch(error){return error}throw Error('expected a refusal')}
type Execute=AssistantSession['hostTools'][number]['execute']
const session=(store:RunStore,id:string,execute:Execute,resume=false):AssistantSession=>({prompt:'Work',testPrompt:'',tools:[],hostTools:[{name:'save_object',taxonomy:{operation:'own-state',effect:'pure'},effect:'workflow',description:'Save',inputSchema:{},execute}],callTool:vi.fn(async()=>({content:[]})),model:{family:'openai-compatible',model:'fixture',call:vi.fn(async()=>new Response('done'))},signal:new AbortController().signal,context:{currentRequest:{message:'Work',input:'Work'},runStore:{store,id,...(resume?{resume:true}:{})}}})
async function driver(store:RunStore,id:string,execute:Execute,resume=false){
 const ready=await contextualSession(session(store,id,execute,resume))
 return {close:()=>ready.context.close(),call:(value:number)=>ready.session.values.hostTools.find(t=>t.name==='save_object')!.execute({value},new AbortController().signal,`wire-${value}`)}
}
const saved=()=>vi.fn(async()=>({content:[{type:'text' as const,text:'saved'}]}))
function clocked(options:{afterCommit?:()=>void;beforeCommit?:()=>void}={}){const clock={at:T0};const store=new MemoryRunStore({now:()=>new Date(clock.at),...options});return {clock,store}}
const checkpoint=async(store:RunStore,id:string)=>JSON.parse(new TextDecoder().decode((await store.load(id))!.checkpoint!))

describe('shared lease vectors (#223)',()=>{
 it('both readers accept every lease, fence and admission and refuse every refused record',()=>{
  for(const item of leaseVector.items)expect(readRunLease(structuredClone(item))).toEqual(item)
  for(const item of leaseVector.refused)expect(()=>readRunLease(structuredClone(item.record)),item.problem).toThrow(LeaseRefused)
  for(const item of fenceVector.items)expect(readLeaseFence(structuredClone(item))).toEqual(item)
  for(const item of fenceVector.refused)expect(()=>readLeaseFence(structuredClone(item.fence)),item.problem).toThrow(expect.objectContaining({reason:'invalid'}))
  for(const item of admissionVector.items){expect(readEffectAdmission(structuredClone(item))).toEqual(item);expect(effectIdempotencyKey(item.operation)).toBe(item.idempotencyKey)}
  expect(canonicalJSON(admissionVector.items[0]!.operation)).toBe(admissionVector.canonical)
  const [first,takeover]=admissionVector.items
  expect(first!.fence.epoch).not.toBe(takeover!.fence.epoch);expect(effectAdmission(readLeaseFence(takeover!.fence),takeover!.operation).idempotencyKey).toBe(first!.idempotencyKey)
  for(const item of admissionVector.refused)expect(()=>readEffectAdmission(structuredClone(item.admission)),item.problem).toThrow(LeaseRefused)
  expect(refusalVector.items.map(item=>readLeaseRefusal(item).reason)).toEqual([...LEASE_REFUSAL_REASONS])
  expect(refusalVector.items.map(item=>new LeaseRefused(item.reason as never).record())).toEqual(refusalVector.items)
  for(const item of refusalVector.refused)expect(()=>readLeaseRefusal(item)).toThrow(LeaseRefused)
  expect(capabilityVector.items.map(item=>readLeaseCapabilityRefusal(item).reason)).toEqual([...LEASE_CAPABILITY_REASONS])
  expect(capabilityVector.items.map(item=>new LeaseCapabilityRefused(item.reason as never).record())).toEqual(capabilityVector.items)
  for(const item of capabilityVector.refused)expect(()=>readLeaseCapabilityRefusal(item)).toThrow(LeaseRefused)
 })
 it('encodes the epoch exactly: the largest is accepted, the next refuses as exhausted, no rounding',()=>{
  const {epochs}=fenceVector
  expect(LEASE_EPOCH_MAX).toBe(epochs.largest);expect(nextLeaseEpoch()).toBe(epochs.first)
  for(const [previous,next] of epochs.next)expect(nextLeaseEpoch(previous)).toBe(next)
  expect(()=>nextLeaseEpoch(epochs.exhausted)).toThrow(refusal('epoch-exhausted'))
  for(const [a,b,order] of epochs.order as [string,string,number][])expect(compareLeaseEpochs(a,b)).toBe(order)
  for(const item of fenceVector.refused)if(typeof item.fence.epoch==='string'&&item.fence.epoch!==fenceVector.items[0]!.epoch)expect(()=>nextLeaseEpoch(item.fence.epoch as string),item.problem).toThrow(refusal('invalid'))
 })
})

describe('the lease port (#223)',()=>{
 it('MemoryRunStore passes the reusable lease conformance under its own clock',async()=>{await runStoreLeaseConformance(now=>new MemoryRunStore({now}))})
 it('the lease conformance fails a store whose renewal revives an expired lease',async()=>{
  const reviving=(now:()=>Date)=>{const store=new MemoryRunStore({now});const renew=store.renewLease.bind(store);store.renewLease=async(fence,ms)=>{try{return await renew(fence,ms)}catch(error){if(!(error instanceof LeaseRefused&&error.reason==='expired'))throw error;const held=(await store.readLease(fence.resource))!;return {...held,expiry:new Date(now().getTime()+ms).toISOString()}}};return store}
  await expect(runStoreLeaseConformance(reviving)).rejects.toMatchObject({property:'lease.renew.no-revival'})
  await expect(runStoreLeaseConformance(reviving)).rejects.toBeInstanceOf(StorageConformanceFailure)
 })
 it('the store checks the fence again at publication, after its own awaits',async()=>{
  let armed=false
  const {clock,store}=clocked({beforeCommit:()=>{if(armed){armed=false;clock.at+=2*MINUTE}}})
  const a=await store.acquireLease({resource:'publication',owner:'worker-a',expectedRevision:null,durationMs:MINUTE})
  armed=true
  await expect(store.commitFenced({runId:'publication',expectedRevision:null,commitId:'late',checkpoint:new TextEncoder().encode('{}'),events:[]},fenceOf(a))).rejects.toEqual(refusal('expired'))
  expect(await store.load('publication')).toBeUndefined()
 })
 it('a chat store fences its updates under a chat-scoped lease',async()=>{
  const clock={at:T0},chats=new MemoryChatStore([{chat:{id:'chat-lease',interaction:'plan'}}],{now:()=>new Date(clock.at)}),head=(await chats.get('chat-lease'))!
  await expect(chats.acquireLease({resource:'chat-lease',owner:'worker-a',expectedRevision:'stale',durationMs:MINUTE})).rejects.toEqual(refusal('revision-changed'))
  const lease=await chats.acquireLease({resource:'chat-lease',owner:'worker-a',expectedRevision:head.revision,durationMs:MINUTE}),view=fencedChatStore(chats,fenceOf(lease))
  const updated=await view.update('chat-lease',{interaction:'work'},head.revision)
  await expect(fencedChatStore(chats,{...fenceOf(lease),owner:'worker-b'}).update('chat-lease',{interaction:'plan'},updated.revision)).rejects.toEqual(refusal('other-owner'))
  clock.at+=MINUTE
  await expect(view.update('chat-lease',{interaction:'plan'},updated.revision)).rejects.toEqual(refusal('expired'))
  expect((await chats.get('chat-lease'))!.chat.interaction).toBe('work')
  expect(()=>fencedChatStore({...chats,consistency:'single-writer-legacy',get:chats.get.bind(chats),list:chats.list.bind(chats),update:chats.update.bind(chats)},fenceOf(lease))).toThrow(capability('store-unsupported'))
  expect(()=>fencedChatStore({get:chats.get.bind(chats),list:chats.list.bind(chats),update:chats.update.bind(chats)},fenceOf(lease))).toThrow(capability('store-unsupported'))
 })
})

describe('competing drivers, takeover and stale commits (#223)',()=>{
 it('competing drivers: only the lease holder commits and dispatches',async()=>{
  const {store}=clocked(),host=new ReferenceEffectHost(store),execute=saved()
  const a=await store.acquireLease({resource:'competing',owner:'worker-a',expectedRevision:null,durationMs:MINUTE})
  await expect(store.acquireLease({resource:'competing',owner:'worker-b',expectedRevision:null,durationMs:MINUTE})).rejects.toEqual(refusal('held'))
  const first=await driver(fencedRunStore(store,fenceOf(a),host),'competing',execute)
  await first.call(1);expect(execute).toHaveBeenCalledOnce()
  const before=(await store.load('competing'))!.revision
  const forged:LeaseFence={...fenceOf(a),owner:'worker-b'}
  const error=await refused((async()=>{const second=await driver(fencedRunStore(store,forged,host),'competing',execute,true);await second.call(2)})())
  expect(named(error)).toEqual(refusal('other-owner'))
  await expect(host.dispatch(effectAdmission(forged,{run:'competing',operation:'forged-op',source:'host',tool:'save_object',requestDigest:'sha256:'+'0'.repeat(64)}),async()=>execute())).rejects.toEqual(refusal('other-owner'))
  expect(execute).toHaveBeenCalledOnce();expect((await store.load('competing'))!.revision).toBe(before)
  first.close()
 })
 it('takeover: the expired and then stale driver commits nothing; the new owner resumes without replay',async()=>{
  const {clock,store}=clocked(),host=new ReferenceEffectHost(store),execute=saved()
  const a=await store.acquireLease({resource:'takeover',owner:'worker-a',expectedRevision:null,durationMs:MINUTE})
  const first=await driver(fencedRunStore(store,fenceOf(a),host),'takeover',execute)
  await first.call(1);expect(execute).toHaveBeenCalledOnce()
  clock.at+=MINUTE
  expect(named(await refused(first.call(2)))).toEqual(refusal('expired'))
  const head=(await store.load('takeover'))!
  const b=await store.acquireLease({resource:'takeover',owner:'worker-b',expectedRevision:head.revision,durationMs:MINUTE})
  expect(compareLeaseEpochs(b.epoch,a.epoch)).toBe(1)
  // The first driver's writer latched its refusal; a new driver holding the old fence is the stale one.
  expect(named(await refused(first.call(3)))).toEqual(refusal('expired'));first.close()
  expect(named(await refused((async()=>{const stale=await driver(fencedRunStore(store,fenceOf(a),host),'takeover',execute,true);await stale.call(3)})()))).toEqual(refusal('stale-epoch'))
  await expect(store.commitFenced({runId:'takeover',expectedRevision:head.revision,commitId:'stale-direct',checkpoint:new TextEncoder().encode('{}'),events:[]},fenceOf(a))).rejects.toEqual(refusal('stale-epoch'))
  expect(execute).toHaveBeenCalledOnce();expect((await store.load('takeover'))!.revision).toBe(head.revision)
  const second=await driver(fencedRunStore(store,fenceOf(b),host),'takeover',execute,true)
  await second.call(1);expect(execute).toHaveBeenCalledOnce()
  await second.call(4);expect(execute).toHaveBeenCalledTimes(2)
  second.close()
 })
 it('the effect host itself refuses a fence that expired after the claim committed',async()=>{
  let armed=false
  const {clock,store}=clocked({afterCommit:()=>{if(armed){armed=false;clock.at+=2*MINUTE}}}),host=new ReferenceEffectHost(store),execute=saved()
  const a=await store.acquireLease({resource:'effect-host',owner:'worker-a',expectedRevision:null,durationMs:MINUTE})
  const first=await driver(fencedRunStore(store,fenceOf(a),host),'effect-host',execute)
  armed=true
  await refused(first.call(1))
  expect(execute).not.toHaveBeenCalled()
  expect(host.refusals).toEqual([{version:1,reason:'expired'}])
  const claim=(await checkpoint(store,'effect-host')).durable.dispatches.find((row:{kind:string;read:boolean})=>row.kind==='host'&&!row.read)
  expect(claim.state).toBe('claimed')
  first.close()
 })
 it('restart with an unknown in-flight effect: the takeover never dispatches it again',async()=>{
  const {clock,store}=clocked(),host=new ReferenceEffectHost(store)
  let started!:()=>void;const running=new Promise<void>(resolve=>started=resolve)
  const execute=vi.fn(async()=>{started();return new Promise<never>(()=>{})})
  const a=await store.acquireLease({resource:'in-flight',owner:'worker-a',expectedRevision:null,durationMs:MINUTE})
  const first=await driver(fencedRunStore(store,fenceOf(a),host),'in-flight',execute)
  void first.call(1).catch(()=>undefined);await running
  // The process ends here: nothing more is committed for it.
  const claim=(await checkpoint(store,'in-flight')).durable.dispatches.find((row:{kind:string;read:boolean})=>row.kind==='host'&&!row.read)
  expect(claim.state).toBe('claimed')
  clock.at+=2*MINUTE
  const b=await store.acquireLease({resource:'in-flight',owner:'worker-b',expectedRevision:(await store.load('in-flight'))!.revision,durationMs:MINUTE})
  const second=await driver(fencedRunStore(store,fenceOf(b),host),'in-flight',execute,true)
  await expect(second.call(1)).rejects.toBeInstanceOf(HostOutcomeUnknown)
  const operation={run:'in-flight',operation:claim.id,source:claim.source,tool:claim.tool,requestDigest:claim.requestDigest}
  expect(effectAdmission(fenceOf(b),operation).idempotencyKey).toBe(effectAdmission(fenceOf(a),operation).idempotencyKey)
  const perform=vi.fn(async()=>undefined)
  await expect(host.dispatch(effectAdmission(fenceOf(b),operation),perform)).rejects.toEqual(refusal('already-admitted'))
  expect(perform).not.toHaveBeenCalled();expect(execute).toHaveBeenCalledOnce()
  second.close()
 })
 it('restore does not revive an expired lease',async()=>{
  const {clock,store}=clocked(),host=new ReferenceEffectHost(store),execute=saved()
  const a=await store.acquireLease({resource:'restored',owner:'worker-a',expectedRevision:null,durationMs:MINUTE})
  const first=await driver(fencedRunStore(store,fenceOf(a),host),'restored',execute);await first.call(1);first.close()
  clock.at+=MINUTE
  await expect(store.renewLease(fenceOf(a),MINUTE)).rejects.toEqual(refusal('expired'))
  const error=await refused((async()=>{const restored=await driver(fencedRunStore(store,fenceOf(a),host),'restored',execute,true);await restored.call(2)})())
  expect(named(error)).toEqual(refusal('expired'))
  expect(execute).toHaveBeenCalledOnce()
  const next=await store.acquireLease({resource:'restored',owner:'worker-a',expectedRevision:(await store.load('restored'))!.revision,durationMs:MINUTE})
  expect(next.epoch).toBe('2')
 })
})

describe('truthful adapters and unattended admission (#223)',()=>{
 class DurableLeaseFixture extends MemoryRunStore {override readonly leaseLifetime:LeaseLifetime='durable'} // stands for a host store kind
 it('refuses unattended admission by name for every store or host that cannot give the guarantee',()=>{
  const memory=new MemoryRunStore(),host=new ReferenceEffectHost(memory),unfenced:EffectHost={effectFencing:'unsupported',dispatch:(_admission,perform)=>perform()}
  const callback=checkpointCallbackRunStore(async()=>{},bytes=>bytes),plain:RunStore={durability:'host',load:memory.load.bind(memory),commit:memory.commit.bind(memory),readEvents:memory.readEvents.bind(memory)}
  expect(()=>admitUnattended({store:memory,effectHost:host})).toThrow(capability('store-lifetime'))
  expect(()=>admitUnattended({store:callback,effectHost:host})).toThrow(capability('store-unsupported'))
  expect(()=>admitUnattended({store:plain,effectHost:host})).toThrow(capability('store-unsupported'))
  const durable=new DurableLeaseFixture(),own=new ReferenceEffectHost(durable)
  expect(()=>admitUnattended({store:durable})).toThrow(capability('effect-host-unfenced'))
  expect(()=>admitUnattended({store:durable,effectHost:unfenced})).toThrow(capability('effect-host-unfenced'))
  expect(()=>admitUnattended({store:durable,effectHost:own,chatStore:new MemoryChatStore()})).toThrow(capability('store-lifetime'))
  expect(()=>admitUnattended({store:durable,effectHost:own})).not.toThrow()
  expect(()=>admitUnattended({store:durable,effectHost:host})).toThrow(capability('effect-host-unfenced'))
  const fence:LeaseFence={version:1,resource:'r',owner:'o',epoch:'1'}
  expect(unattendedRunStore({store:durable,fence,effectHost:own}).durability).toBe('memory')
  expect(()=>unattendedRunStore({store:memory,fence,effectHost:host})).toThrow(capability('store-lifetime'))
  expect(()=>fencedRunStore(callback,fence)).toThrow(capability('store-unsupported'))
  expect(()=>fencedRunStore(memory,fence,unfenced)).toThrow(capability('effect-host-unfenced'))
  expect(new LeaseCapabilityRefused('store-lifetime').failure).toMatchObject({code:'configuration',outcome:'not-started'})
 })
 it('a fenced run with no effect host refuses a non-read effect before it runs',async()=>{
  const {store}=clocked(),execute=saved()
  const a=await store.acquireLease({resource:'no-host',owner:'worker-a',expectedRevision:null,durationMs:MINUTE})
  const first=await driver(fencedRunStore(store,fenceOf(a)),'no-host',execute)
  expect(named(await refused(first.call(1)))).toEqual(capability('effect-host-unfenced'))
  expect(execute).not.toHaveBeenCalled();first.close()
 })
 it('disabled-byte parity: a fenced run stores the same record shapes and versions as an unfenced run',async()=>{
  const shape=(value:unknown):unknown=>Array.isArray(value)?value.map(shape):value&&typeof value==='object'?Object.fromEntries(Object.entries(value).map(([k,v])=>[k,shape(v)])):typeof value
  const record=async(fenced:boolean)=>{
   const {store}=clocked(),execute=saved()
   const lease=fenced?await store.acquireLease({resource:'parity',owner:'worker-a',expectedRevision:null,durationMs:MINUTE}):undefined
   const first=await driver(lease?fencedRunStore(store,fenceOf(lease),new ReferenceEffectHost(store)):store,'parity',execute);await first.call(1);first.close()
   const events=(await store.readEvents('parity',null,100)).items.map(item=>new TextDecoder().decode(item.bytes))
   const text=new TextDecoder().decode((await store.load('parity'))!.checkpoint!)
   return {checkpoint:shape(JSON.parse(text)),version:JSON.parse(text).version,events:events.map(event=>shape(JSON.parse(event))),text:[text,...events].join('\n')}
  }
  const plain=await record(false),fenced=await record(true)
  expect(fenced.checkpoint).toEqual(plain.checkpoint);expect(fenced.events).toEqual(plain.events);expect(fenced.version).toBe(plain.version)
  for(const word of ['epoch','fence','lease','worker-a'])expect(fenced.text).not.toContain(word)
 })
})

// #223 review fix round: the effect of a call decides the writer's read, every port refuses a stale fence, and an effect
// host is admitted for unattended runs only when proven.
const SOURCE_ACT={name:'submit',description:'Submit one expense.',inputSchema:{type:'object'},taxonomy:{operation:'act',effect:'act'} as const}
async function sourceTool(kind:'gateway'|'mcp'|'custom',execute:Execute,effectClass:'act'|'read'='act'):Promise<HostTool>{
 const declared=effectClass==='act'?SOURCE_ACT:{...SOURCE_ACT,name:'lookup',taxonomy:{operation:'read',effect:'read'} as const}
 return {...declared,effectClass,source:{kind,id:'expenses',trust:'connected',version:'4',toolListDigest:await toolListDigest([declared] as never),identity:{kind:'person',label:'Reviewer'}},approve:()=>'allow',execute} as HostTool
}
async function toolDriver(store:RunStore,id:string,tool:HostTool,resume=false,chat?:StoredChatScope){
 const input=session(store,id,tool.execute,resume);input.hostTools=[tool]
 if(chat){input.context={...input.context!,runStore:{...input.context!.runStore!,context:chat} as never,chat:chat as never}}
 const ready=await contextualSession(input)
 const name=tool.source?`${tool.source.id}__${tool.name}`:tool.name
 return {close:()=>ready.context.close(),call:(value:number)=>ready.session.values.hostTools.find(t=>t.name===name)!.execute({value},new AbortController().signal,`wire-${value}`)}
}
class CountingHost implements EffectHost {readonly effectFencing='enforced' as const;admissions=0;constructor(readonly inner:ReferenceEffectHost){}dispatch<T>(admission:EffectAdmission,perform:(signal?:AbortSignal)=>Promise<T>,signal?:AbortSignal){this.admissions++;return this.inner.dispatch(admission,perform,signal)}}
describe('a source act is an effect, not a read (#223 review 1)',()=>{
 for(const kind of ['gateway','mcp','custom'] as const){
  it(`${kind} act: admitted by the effect host, refused after expiry, never dispatched twice across a takeover`,async()=>{
   let armed=false
   const {clock,store}=clocked({afterCommit:()=>{if(armed){armed=false;clock.at+=2*MINUTE}}}),host=new CountingHost(new ReferenceEffectHost(store))
   const once=saved(),a=await store.acquireLease({resource:`${kind}-act`,owner:'worker-a',expectedRevision:null,durationMs:MINUTE})
   const first=await toolDriver(fencedRunStore(store,fenceOf(a),host),`${kind}-act`,await sourceTool(kind,once))
   await first.call(1);expect(once).toHaveBeenCalledOnce();expect(host.admissions).toBe(1)
   const claims=(await checkpoint(store,`${kind}-act`)).durable.dispatches.filter((row:{kind:string})=>row.kind==='host')
   expect(claims.map((row:{read:boolean})=>row.read)).toEqual([false])
   // The lease expires between the claim commit and the dispatch: the host refuses, the act does not run.
   armed=true
   await refused(first.call(2));expect(once).toHaveBeenCalledOnce();expect(host.inner.refusals).toEqual([{version:1,reason:'expired'}])
   first.close()
   // A takeover re-issues the call whose outcome is unknown: it stays unknown, one physical dispatch in all.
   const b=await store.acquireLease({resource:`${kind}-act`,owner:'worker-b',expectedRevision:(await store.load(`${kind}-act`))!.revision,durationMs:MINUTE})
   const again=saved(),second=await toolDriver(fencedRunStore(store,fenceOf(b),host),`${kind}-act`,await sourceTool(kind,again),true)
   await expect(second.call(2)).rejects.toBeInstanceOf(HostOutcomeUnknown)
   await second.call(1);expect(again).not.toHaveBeenCalled();second.close()
  })
  it(`${kind} act in flight: the takeover never dispatches it again`,async()=>{
   const {clock,store}=clocked(),host=new ReferenceEffectHost(store)
   let physical=0,started!:()=>void;const running=new Promise<void>(resolve=>started=resolve)
   const hang=vi.fn(async()=>{physical++;started();return new Promise<never>(()=>{})}),again=vi.fn(async()=>{physical++;return {content:[]}})
   const a=await store.acquireLease({resource:`${kind}-flight`,owner:'worker-a',expectedRevision:null,durationMs:MINUTE})
   const first=await toolDriver(fencedRunStore(store,fenceOf(a),host),`${kind}-flight`,await sourceTool(kind,hang))
   void first.call(1).catch(()=>undefined);await running
   clock.at+=2*MINUTE
   const b=await store.acquireLease({resource:`${kind}-flight`,owner:'worker-b',expectedRevision:(await store.load(`${kind}-flight`))!.revision,durationMs:MINUTE})
   const second=await toolDriver(fencedRunStore(store,fenceOf(b),host),`${kind}-flight`,await sourceTool(kind,again),true)
   await expect(second.call(1)).rejects.toBeInstanceOf(HostOutcomeUnknown)
   expect(physical).toBe(1);second.close()
  })
 }
 it('a source read stays a read: no effect-host admission, recorded read',async()=>{
  const {store}=clocked(),host=new CountingHost(new ReferenceEffectHost(store)),read=saved()
  const a=await store.acquireLease({resource:'source-read',owner:'worker-a',expectedRevision:null,durationMs:MINUTE})
  const first=await toolDriver(fencedRunStore(store,fenceOf(a),host),'source-read',await sourceTool('custom',read,'read'))
  await first.call(1);expect(read).toHaveBeenCalledOnce();expect(host.admissions).toBe(0)
  expect((await checkpoint(store,'source-read')).durable.dispatches.filter((row:{kind:string})=>row.kind==='host').map((row:{read:boolean})=>row.read)).toEqual([true])
  first.close()
 })
})

describe('a stale driver cannot release the chat turn its takeover resumes (#223 review 2)',()=>{
 it('the stale close refuses and the takeover restores and resumes',async()=>{
  const {clock,store}=clocked(),host=new ReferenceEffectHost(store)
  const a=await store.acquireLease({resource:'chat-run',owner:'worker-a',expectedRevision:null,durationMs:MINUTE})
  const viewA=fencedRunStore(store,fenceOf(a),host) as never as import('./history').HistoryRunStore
  const scopeA=new StoredChatScope(viewA,'chat-lease-scope',new ChatContext({scope:'shared'}))
  const first=await toolDriver(viewA,'chat-run',{name:'save_object',taxonomy:{operation:'own-state',effect:'pure'},effect:'workflow',description:'Save',inputSchema:{},execute:saved()} as HostTool,false,scopeA)
  await first.call(1)
  const bound=(await store.readHistoryHead('chat-run'))!.context!
  clock.at+=2*MINUTE
  const b=await store.acquireLease({resource:'chat-run',owner:'worker-b',expectedRevision:(await store.load('chat-run'))!.revision,durationMs:MINUTE})
  first.close()
  expect(named(await refused(scopeA.close()))).toEqual(refusal('stale-epoch'))
  expect((await store.loadContextSnapshot('chat-lease-scope'))!.revision).toBe(bound.revision)
  const viewB=fencedRunStore(store,fenceOf(b),host) as never as import('./history').HistoryRunStore
  const restored=await restoreStoredChatScope(viewB,(await store.readHistoryHead('chat-run'))!.context!)
  const execute=saved(),second=await toolDriver(viewB,'chat-run',{name:'save_object',taxonomy:{operation:'own-state',effect:'pure'},effect:'workflow',description:'Save',inputSchema:{},execute} as HostTool,true,restored)
  await second.call(2);expect(execute).toHaveBeenCalledOnce();second.close()
 })
})

describe('the kits refuse what does not enforce the fence (#223 review 3)',()=>{
 type Break=(store:MemoryRunStore)=>void
 const breaks:Record<string,Break>={
  'checkFence checks expiry only':store=>{const check=store.checkFence.bind(store);store.checkFence=async fence=>{const row=await store.readLease(fence.resource);return check(row?{...fence,epoch:row.epoch,owner:row.owner}:fence)}},
  'commitHistoryFenced ignores the fence':store=>{store.commitHistoryFenced=(input,_fence,signal)=>store.commitHistory(input,signal)},
  'commitContextSnapshotFenced ignores the fence':store=>{store.commitContextSnapshotFenced=(input,commitId,_fence,signal)=>store.commitContextSnapshot(input,commitId,signal)},
  'renewLease ignores the epoch':store=>{const renew=store.renewLease.bind(store);store.renewLease=async(fence,ms)=>{const row=await store.readLease(fence.resource);return renew(row&&row.owner===fence.owner?{...fence,epoch:row.epoch}:fence,ms)}},
  'releaseLease ignores the epoch and owner':store=>{const release=store.releaseLease.bind(store);store.releaseLease=async fence=>{const row=await store.readLease(fence.resource);return release(row?{...fence,epoch:row.epoch,owner:row.owner}:fence)}},
 }
 for(const [name,broken] of Object.entries(breaks))it(`the lease conformance fails a store whose ${name}`,async()=>{
  await expect(runStoreLeaseConformance(now=>{const store=new MemoryRunStore({now});broken(store);return store as LeaseRunStore})).rejects.toBeInstanceOf(StorageConformanceFailure)
 })
 const hosts:Record<string,(authority:import('./lease').LeaseAuthority)=>EffectHost>={
  'performs without checking':()=>({effectFencing:'enforced',dispatch:(_admission,perform)=>perform()}),
  'skips the resource check':authority=>{const inner=new ReferenceEffectHost(authority);return {effectFencing:'enforced',dispatch:(admission,perform,signal)=>inner.dispatch({...admission,fence:{...admission.fence},operation:{...admission.operation,run:admission.fence.resource},idempotencyKey:effectIdempotencyKey({...admission.operation,run:admission.fence.resource})},perform,signal)}},
  'forgets admitted identities':authority=>({effectFencing:'enforced',dispatch:async(admission,perform)=>{await authority.checkFence(admission.fence);return perform()}}),
 }
 for(const [name,create] of Object.entries(hosts))it(`the effect-host conformance fails a host that ${name}, and unattended admission refuses it`,async()=>{
  await expect(effectHostConformance(create)).rejects.toBeInstanceOf(StorageConformanceFailure)
  const store=new DurableLeaseStoreFixture()
  expect(()=>admitUnattended({store,effectHost:create(store)})).toThrow(capability('effect-host-unfenced'))
  await expect(certifyEffectHost(create,store)).rejects.toBeInstanceOf(StorageConformanceFailure)
 })
 it('the reference host passes; a certified host is admitted and a declared one is not',async()=>{
  await effectHostConformance(authority=>new ReferenceEffectHost(authority))
  const store=new DurableLeaseStoreFixture(),wrap=(authority:import('./lease').LeaseAuthority):EffectHost=>{const inner=new ReferenceEffectHost(authority);return {effectFencing:'enforced',dispatch:(admission,perform,signal)=>inner.dispatch(admission,perform,signal)}}
  expect(()=>admitUnattended({store,effectHost:wrap(store)})).toThrow(capability('effect-host-unfenced'))
  const certified=await certifyEffectHost(wrap,store)
  expect(()=>admitUnattended({store,effectHost:certified})).not.toThrow()
  expect(()=>admitUnattended({store,effectHost:new ReferenceEffectHost(store)})).not.toThrow()
 })
})
class DurableLeaseStoreFixture extends MemoryRunStore {override readonly leaseLifetime:LeaseLifetime='durable'} // stands for a host store kind

describe('pinned safeguards (#223 review 4)',()=>{
 it('a runtime-kind effect under a fenced view is admitted by the effect host, which refuses an expired fence',async()=>{
  let armed=false
  const {clock,store}=clocked({afterCommit:()=>{if(armed){armed=false;clock.at+=2*MINUTE}}}),host=new ReferenceEffectHost(store),execute=vi.fn(async()=>({content:[]}))
  const a=await store.acquireLease({resource:'runtime-act',owner:'worker-a',expectedRevision:null,durationMs:MINUTE})
  const admitted=await durableSession(session(fencedRunStore(store,fenceOf(a),host),'runtime-act',saved()))
  const ready=await contextualSession(admitted),binding=(admitted.context as Record<symbol,unknown>)[durableOwner as never] as DurableBinding
  armed=true
  await refused(binding.writer.invoke(binding,'runtime','runtime','act_on_runtime','{"value":1}',false,execute))
  expect(execute).not.toHaveBeenCalled();expect(host.refusals).toEqual([{version:1,reason:'expired'}])
  ready.context.close()
 })
 it('the effect host refuses a fence for another run than the operation\'s',async()=>{
  const {store}=clocked(),host=new ReferenceEffectHost(store),perform=vi.fn(async()=>'performed')
  const a=await store.acquireLease({resource:'run-x',owner:'worker-a',expectedRevision:null,durationMs:MINUTE})
  await expect(host.dispatch(effectAdmission(fenceOf(a),{run:'run-y',operation:'op',source:'host',tool:'t',requestDigest:'sha256:'+'c'.repeat(64)}),perform)).rejects.toEqual(refusal('resource-mismatch'))
  expect(perform).not.toHaveBeenCalled();expect(host.refusals).toEqual([{version:1,reason:'resource-mismatch'}])
 })
})

describe('the kits test expiry on every fenced commit path (#253 R1)',()=>{
 const skipsExpiry=(store:MemoryRunStore,fence:LeaseFence)=>store.readLease(fence.resource).then(row=>!!row&&row.state==='held'&&row.epoch===fence.epoch&&row.owner===fence.owner)
 const breaks:Record<string,(store:MemoryRunStore)=>void>={
  'commitHistoryFenced skips the expiry check':store=>{const fenced=store.commitHistoryFenced.bind(store);store.commitHistoryFenced=async(input,fence,signal)=>await skipsExpiry(store,fence)?store.commitHistory(input,signal):fenced(input,fence,signal)},
  'commitContextSnapshotFenced skips the expiry check':store=>{const fenced=store.commitContextSnapshotFenced.bind(store);store.commitContextSnapshotFenced=async(input,commitId,fence,signal)=>await skipsExpiry(store,fence)?store.commitContextSnapshot(input,commitId,signal):fenced(input,commitId,fence,signal)},
 }
 for(const [name,broken] of Object.entries(breaks))it(`the lease conformance fails a store whose ${name}`,async()=>{
  await expect(runStoreLeaseConformance(now=>{const store=new MemoryRunStore({now});broken(store);return store as LeaseRunStore})).rejects.toMatchObject({property:expect.stringMatching(/^lease\.(history|snapshot)\.expired\.equal-time$/)})
 })
})

describe('certification is bound to the instance it checked (#253 R2)',()=>{
 const performs=(authority:import('./lease').LeaseAuthority):EffectHost=>{const inner=new ReferenceEffectHost(authority);return {effectFencing:'enforced',dispatch:(admission,perform,signal)=>inner.dispatch(admission,perform,signal)}}
 it('a host that performs while the authority is unavailable fails the kit and certification',async()=>{
  const failOpen=(authority:import('./lease').LeaseAuthority):EffectHost=>{const inner=new ReferenceEffectHost(authority);return {effectFencing:'enforced',dispatch:async(admission,perform,signal)=>{try{return await inner.dispatch(admission,perform,signal)}catch(error){if(error instanceof LeaseRefused)throw error;return perform(signal)}}}}
  await expect(effectHostConformance(failOpen)).rejects.toMatchObject({property:'host.authority-unavailable'})
  await expect(certifyEffectHost(failOpen,new DurableLeaseStoreFixture())).rejects.toMatchObject({property:'host.authority-unavailable'})
 })
 it('the factory is called once: the instance the kit exercised is the one admitted',async()=>{
  const store=new DurableLeaseStoreFixture(),products:EffectHost[]=[]
  const create=(authority:import('./lease').LeaseAuthority):EffectHost=>{const host=products.length?{effectFencing:'enforced' as const,dispatch:<T,>(_admission:EffectAdmission,perform:(signal?:AbortSignal)=>Promise<T>)=>perform()}:performs(authority);products.push(host);return host}
  const certified=await certifyEffectHost(create,store)
  expect(products).toHaveLength(1)
  expect(()=>admitUnattended({store,effectHost:certified})).not.toThrow()
  expect(()=>admitUnattended({store,effectHost:products[0]!})).toThrow(capability('effect-host-unfenced'))
  // The certified host now answers from the real store: a fence the store never issued is refused, nothing performed.
  const perform=vi.fn(async()=>'performed')
  await expect(certified.dispatch(effectAdmission({version:1,resource:'r',owner:'o',epoch:'1'},{run:'r',operation:'op',source:'host',tool:'t',requestDigest:'sha256:'+'d'.repeat(64)}),perform)).rejects.toEqual(refusal('no-lease'))
  expect(perform).not.toHaveBeenCalled()
 })
 it('a dispatch swapped after certification is refused',async()=>{
  const store=new DurableLeaseStoreFixture(),inner:EffectHost[]=[]
  const certified=await certifyEffectHost(authority=>{const host=performs(authority);inner.push(host);return host},store)
  ;(inner[0] as {dispatch:EffectHost['dispatch']}).dispatch=(_admission,perform)=>perform()
  expect(()=>admitUnattended({store,effectHost:certified})).toThrow(capability('effect-host-unfenced'))
  const perform=vi.fn(async()=>'performed')
  await expect(certified.dispatch(effectAdmission({version:1,resource:'r',owner:'o',epoch:'1'},{run:'r',operation:'op',source:'host',tool:'t',requestDigest:'sha256:'+'d'.repeat(64)}),perform)).rejects.toEqual(capability('effect-host-unfenced'))
  expect(perform).not.toHaveBeenCalled()
  expect(()=>{(certified as {dispatch:unknown}).dispatch=()=>undefined}).toThrow(TypeError)
  const reference=new ReferenceEffectHost(store)
  expect(()=>{(reference as {dispatch:unknown}).dispatch=()=>undefined}).toThrow(TypeError)
  expect(()=>Object.defineProperty(reference,'dispatch',{value:()=>undefined})).toThrow(TypeError)
  expect(()=>{(reference as {effectFencing:unknown}).effectFencing='enforced'}).toThrow(TypeError)
  expect(()=>admitUnattended({store,effectHost:reference})).not.toThrow()
 })
 it('a host over a no-op or another store\'s authority is refused, and evidence does not pass to another instance or a subclass',async()=>{
  const store=new DurableLeaseStoreFixture(),other=new DurableLeaseStoreFixture()
  expect(()=>admitUnattended({store,effectHost:new ReferenceEffectHost({checkFence:async()=>{}})})).toThrow(capability('effect-host-unfenced'))
  expect(()=>admitUnattended({store,effectHost:new ReferenceEffectHost(other)})).toThrow(capability('effect-host-unfenced'))
  const foreign=await certifyEffectHost(performs,other)
  expect(()=>admitUnattended({store,effectHost:foreign})).toThrow(capability('effect-host-unfenced'))
  class Sub extends ReferenceEffectHost {}
  expect(()=>admitUnattended({store,effectHost:new Sub(store)})).toThrow(capability('effect-host-unfenced'))
  const certified=await certifyEffectHost(performs,store)
  expect(()=>admitUnattended({store,effectHost:{effectFencing:'enforced',dispatch:certified.dispatch}})).toThrow(capability('effect-host-unfenced'))
 })
})
