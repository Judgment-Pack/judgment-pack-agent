import {describe,it,expect,vi} from 'vitest'
import type {AssistantEvent,EngineSession,HostTool} from '../engine'
import {vercel} from '../engines/vercel'
import {answerWith} from '../testing/scenarioDriver'
import {toolListDigest} from '../toolSources'
import {StorageConformanceFailure} from './conformance'
import {MemoryRunStore,type RunStore} from './run'
import {ReferenceEffectHost,fenceOf,fencedRunStore,type LeaseAcquire,type LeaseFence,type LeaseLifetime} from './lease'
import {ReauthorizationRefused} from './reauth'
import {WaitRefused,checkpointBytesDigest,parkRun,resumeParked,waitReport,wakeKeyDigest,wakeRun,type WaitClaim,type WaitLifetime,type WaitPark,type WaitRunStore,type WakeEvidence} from './wait'
import {runStoreWaitConformance} from './waitConformance'

/* #265 M6: the wait conformance kit over the memory kind, a real restart over a fresh store instance, and stores that
 * leave a guarantee out. The Go twin is go/context/run_wait_conformance_test.go. */
const T0=Date.parse('2026-01-01T00:00:00.000Z'),MINUTE=60_000,HOUR=60*MINUTE
/** Stands for a host store kind whose leases and waits outlive its process. */
class DurableWaitStore extends MemoryRunStore {override readonly leaseLifetime:LeaseLifetime='durable';override readonly waitLifetime:WaitLifetime='durable'}
type Held=Record<string,unknown>
/** A restart: a fresh instance over a structured copy of everything the old one held (its runs, history journal, lease
 * table and wait table), so it shares no object with the old one. `drop` leaves members out, as a store that does not
 * keep them would. */
function restarted<T extends MemoryRunStore>(old:MemoryRunStore,make:()=>T,drop:string[]=[]):T{
 const fresh=make(),from=old as unknown as Held,to=fresh as unknown as Held
 to.runs=structuredClone(from.runs)
 for(const table of ['leases','waits','journal']){
  for(const [name,value] of Object.entries(from[table] as Held))if(typeof value!=='function'&&!drop.includes(`${table}.${name}`))(to[table] as Held)[name]=structuredClone(value)
 }
 return fresh
}
const memoryKind=(drop:string[]=[],make:(now:()=>Date)=>MemoryRunStore=now=>new DurableWaitStore({now}))=>(now:()=>Date,from?:WaitRunStore)=>from?restarted(from as MemoryRunStore,()=>make(now),drop):make(now)

describe('the wait conformance kit (#265 M6)',()=>{
 it('the memory kind passes, restarted over a copy of its state',async()=>{
  await expect(runStoreWaitConformance(memoryKind())).resolves.toBeUndefined()
 })
 it('the process-lifetime memory kind passes the same properties; its lifetime is declared, not certified',async()=>{
  await expect(runStoreWaitConformance(memoryKind([],now=>new MemoryRunStore({now})))).resolves.toBeUndefined()
 })
 it('a store that leaves a guarantee out fails the property that names it',async()=>{
  const fails=(property:string)=>expect.objectContaining({name:'StorageConformanceFailure',property})
  class LeasesAnyRun extends DurableWaitStore {override acquireLease(request:LeaseAcquire){const self=this as unknown as {leases:{acquire:(r:LeaseAcquire,v:string|null)=>unknown};runs:Map<string,{head:{revision:string}}>};return Promise.resolve().then(()=>self.leases.acquire(request,self.runs.get(request.resource)?.head.revision??null)) as never}}
  await expect(runStoreWaitConformance(memoryKind([],now=>new LeasesAnyRun({now})))).rejects.toEqual(fails('wait.closure.lease'))
  class AnyKey extends DurableWaitStore {override wakeWait(evidence:WakeEvidence){const self=this as unknown as {waits:{rows:Map<string,{wait:string;wakeKeyDigest:string}[]>}};for(const row of self.waits.rows.get(evidence.resource)??[])if(row.wait===evidence.wait)row.wakeKeyDigest=wakeKeyDigest(evidence.key);return super.wakeWait(evidence)}}
  await expect(runStoreWaitConformance(memoryKind([],now=>new AnyKey({now})))).rejects.toEqual(fails('wait.key.other-wait'))
  await expect(runStoreWaitConformance(memoryKind(['waits.approvals']))).rejects.toEqual(fails('wait.restart.approval-ceiling'))
  await expect(runStoreWaitConformance(memoryKind(['waits.ceilings']))).rejects.toEqual(fails('wait.restart.earliest'))
  await expect(runStoreWaitConformance(memoryKind(['waits.rows','waits.events']))).rejects.toEqual(fails('wait.restart.parked'))
  await expect(runStoreWaitConformance(()=>{throw new Error('unavailable')})).rejects.toBeInstanceOf(StorageConformanceFailure)
 })
 it('#226: a store that leaves a re-authorization guarantee out fails the property that names it',async()=>{
  const fails=(property:string)=>expect.objectContaining({name:'StorageConformanceFailure',property})
  // A park that takes any disposition as the park's own: a proof minted under another lease is parked.
  class AnyLease extends DurableWaitStore {override parkWait(park:WaitPark,fence:LeaseFence){return super.parkWait(park.reauthorization?{...park,reauthorization:{...park.reauthorization,fence}}:park,fence)}}
  await expect(runStoreWaitConformance(memoryKind([],now=>new AnyLease({now})))).rejects.toEqual(fails('reauth.park.stale'))
  // A claim that takes any principal as the parked one: a replaced identity continues as a refresh.
  class AnyPrincipal extends DurableWaitStore {override async resumeWait(claim:WaitClaim){const held=await this.readWait(claim.resource,claim.wait);return super.resumeWait(claim.reauthorized&&held?.reauthorization?{...claim,reauthorized:{...claim.reauthorized,principal:held.reauthorization.principal}}:claim)}}
  await expect(runStoreWaitConformance(memoryKind([],now=>new AnyPrincipal({now})))).rejects.toEqual(fails('reauth.claim.identity'))
  // A claim that takes the permissions the credentials held at the park: narrower ones after the refresh pass.
  class OldPermissions extends DurableWaitStore {override async resumeWait(claim:WaitClaim){const held=await this.readWait(claim.resource,claim.wait);return super.resumeWait(claim.reauthorized&&held?.reauthorization?{...claim,reauthorized:{...claim.reauthorized,permissions:held.reauthorization.permissions}}:claim)}}
  await expect(runStoreWaitConformance(memoryKind([],now=>new OldPermissions({now})))).rejects.toEqual(fails('reauth.claim.narrower'))
  // A park that does not attach the run's unsettled approval.
  class NoApproval extends DurableWaitStore {override async parkWait(park:WaitPark,fence:LeaseFence){const approvals=(this as unknown as {waits:{approvals:Map<string,unknown>}}).waits.approvals,saved=new Map(approvals);if(park.reauthorization)approvals.clear();try{return await super.parkWait(park,fence)}finally{for(const [k,v] of saved)approvals.set(k,v)}}}
  await expect(runStoreWaitConformance(memoryKind([],now=>new NoApproval({now})))).rejects.toEqual(fails('reauth.approval.bound'))
  // A restart that loses the refresh a claim recorded.
  const losesRefresh=(now:()=>Date,from?:WaitRunStore)=>{const next=memoryKind()(now,from);if(from)for(const list of (next as unknown as {waits:{rows:Map<string,{reauthorized?:unknown}[]>}}).waits.rows.values())for(const row of list)delete row.reauthorized;return next}
  await expect(runStoreWaitConformance(losesRefresh)).rejects.toEqual(fails('reauth.restart.claimed'))
 })
 it('#226 M4: a store that skips a documented re-authorization obligation fails the property that names it',async()=>{
  const fails=(property:string)=>expect.objectContaining({name:'StorageConformanceFailure',property})
  type Row={reason:string;state:string;approval?:unknown;reauthorization?:unknown;deadline?:{approval?:string}}
  const rowsOf=(store:unknown)=>[...(store as {waits:{rows:Map<string,Row[]>}}).waits.rows.values()].flat()
  // The head check: any unknown row will do, whatever its failure, its place or its operation.
  class SomeUnknownRow extends DurableWaitStore {override async parkWait(park:WaitPark,fence:LeaseFence){
   if(!park.reauthorization)return super.parkWait(park,fence)
   const run=(this as unknown as {runs:Map<string,{head:{checkpoint:Uint8Array}}>}).runs.get(park.resource)!,bytes=run.head.checkpoint
   if(!new TextDecoder().decode(bytes).includes('"state":"unknown"'))throw new ReauthorizationRefused('unbound')
   const op=park.reauthorization.operation,stand=new TextEncoder().encode(JSON.stringify({durable:{dispatches:[{id:op.operation,kind:'host',read:false,state:'unknown',source:op.source,tool:op.tool,requestDigest:op.requestDigest,failure:{code:'authentication',source:'tool',outcome:'not-started'}}]}}))
   run.head.checkpoint=stand
   try{return await super.parkWait({...park,checkpoint:{...park.checkpoint,digest:checkpointBytesDigest(stand)}},fence)}finally{run.head.checkpoint=bytes}
  }}
  await expect(runStoreWaitConformance(memoryKind([],now=>new SomeUnknownRow({now})))).rejects.toEqual(fails('reauth.park.unbound.not-last'))
  // The stale check compares the epoch only.
  class EpochOnly extends DurableWaitStore {override parkWait(park:WaitPark,fence:LeaseFence){return super.parkWait(park.reauthorization&&park.reauthorization.fence.epoch===fence.epoch?{...park,reauthorization:{...park.reauthorization,fence}}:park,fence)}}
  await expect(runStoreWaitConformance(memoryKind([],now=>new EpochOnly({now})))).rejects.toEqual(fails('reauth.park.stale-owner'))
  // A restart that loses the attached approval, or a resumed wait's disposition.
  const restarting=(lose:(row:Row)=>void)=>(now:()=>Date,from?:WaitRunStore)=>{const next=memoryKind()(now,from);if(from)for(const row of rowsOf(next))lose(row);return next}
  await expect(runStoreWaitConformance(restarting(row=>{if(row.reason==='reauthorization')delete row.approval}))).rejects.toEqual(fails('reauth.restart.bound'))
  await expect(runStoreWaitConformance(restarting(row=>{if(row.reason==='reauthorization'&&row.state==='resumed')delete row.reauthorization}))).rejects.toEqual(fails('reauth.restart.disposition'))
  // The claim's lease is not bounded by the attached approval.
  class Unbounded extends DurableWaitStore {override async resumeWait(claim:WaitClaim){
   const approvals=(this as unknown as {waits:{approvals:Map<string,unknown>}}).waits.approvals,saved=approvals.get(claim.resource)
   if(claim.reauthorized)approvals.delete(claim.resource)
   try{return await super.resumeWait(claim)}finally{if(saved)approvals.set(claim.resource,saved)}
  }}
  await expect(runStoreWaitConformance(memoryKind([],now=>new Unbounded({now})))).rejects.toEqual(fails('reauth.approval.claim-bounded'))
  // limitRun drops the attached approval time.
  class DropsApproval extends DurableWaitStore {override async limitRun(limit:Parameters<MemoryRunStore['limitRun']>[0]){const out=await super.limitRun(limit);for(const row of rowsOf(this))if(row.reason==='reauthorization'&&row.state!=='resumed'&&row.deadline)delete row.deadline.approval;return out}}
  await expect(runStoreWaitConformance(memoryKind([],now=>new DropsApproval({now})))).rejects.toEqual(fails('reauth.limit.later'))
  // A timed-out or expired reauthorization wait does not close its run.
  const notTerminal=(state:string)=>class extends DurableWaitStore {override async acquireLease(request:LeaseAcquire){try{return await super.acquireLease(request)}catch(error){if((error as {reason?:string}).reason==='closed'&&(await this.exportWaits(request.resource)).waits.some(w=>w.reason==='reauthorization'&&w.state===state))throw new WaitRefused('wait-pending');throw error}}}
  const TimedOut=notTerminal('timed-out'),Expired=notTerminal('expired')
  await expect(runStoreWaitConformance(memoryKind([],now=>new TimedOut({now})))).rejects.toEqual(fails('reauth.timeout.closed'))
  await expect(runStoreWaitConformance(memoryKind([],now=>new Expired({now})))).rejects.toEqual(fails('reauth.deadline.closed'))
  // An event after the timeout, or after the deadline, still wakes the wait.
  const late=(reason:string)=>class extends DurableWaitStore {override async wakeWait(evidence:WakeEvidence){try{return await super.wakeWait(evidence)}catch(error){if((error as {reason?:string}).reason===reason&&(await this.readWait(evidence.resource,evidence.wait))?.reason==='reauthorization')return {evidence:evidence.evidence,kind:evidence.kind,at:new Date().toISOString(),digest:'sha256:'+'0'.repeat(64)};throw error}}}
  const LateTimeout=late('timed-out'),LateDeadline=late('deadline-passed')
  await expect(runStoreWaitConformance(memoryKind([],now=>new LateTimeout({now})))).rejects.toEqual(fails('reauth.timeout.late'))
  await expect(runStoreWaitConformance(memoryKind([],now=>new LateDeadline({now})))).rejects.toEqual(fails('reauth.deadline.late'))
 },120_000)
})

// A paused act call through the real vercel engine.
const ACT={name:'create_issue',description:'Create one issue.',inputSchema:{type:'object'},taxonomy:{operation:'act',effect:'act'} as const}
async function actTool(execute:HostTool['execute']):Promise<HostTool>{
 return {...ACT,effectClass:'act',source:{kind:'custom',id:'tracker',trust:'person',version:'1',toolListDigest:await toolListDigest([ACT] as never),identity:{kind:'connection',label:'tracker'}},approve:()=>'ask',execute} as HostTool
}
function provider(turns:Parameters<typeof answerWith>[0][]){
 const state={requests:0,inFlight:0}
 return {state,call:async()=>{state.inFlight++;try{const turn=turns[state.requests];state.requests++;if(!turn)throw Error('no scripted turn');return answerWith(turn,'stream',state.requests-1)}finally{state.inFlight--}}}
}
async function drive(store:RunStore,id:string,tools:HostTool[],call:()=>Promise<Response>,extra:{resume?:boolean;approval?:{callId:string;approved:boolean;note?:string}}={}):Promise<AssistantEvent[]>{
 const session:EngineSession={prompt:'File the issue.',testPrompt:'',tools:[],callTool:vi.fn(async()=>({content:[]})),hostTools:tools,signal:new AbortController().signal,allowConversation:true,critic:'off',
  context:{currentRequest:{message:'File the issue.',input:'issue'},runStore:{store,id,...(extra.resume?{resume:true}:{})},...(extra.approval?{approval:extra.approval}:{})},model:{family:'openai-compatible',model:'fixture',call}} as EngineSession
 const out:AssistantEvent[]=[];for await(const event of vercel.start(session))out.push(event);return out
}

describe('a durable approval wait across real store restarts (#265 M6)',()=>{
 it('parks, restarts, wakes, restarts, claims, restarts and runs the call once: nothing but the copied state survives each restart',async()=>{
  const clock={at:T0},now=()=>new Date(clock.at),key='wk.restart.Q7z2Xm9LpV4cR8tNb3Yh6Df1Gj5K'
  let store=new DurableWaitStore({now})
  const restart=()=>{const old=store;store=restarted(old,()=>new DurableWaitStore({now}));expect(store).not.toBe(old)}
  const execute=vi.fn(async()=>({content:[{type:'text' as const,text:'created'}]}))
  const a=await store.acquireLease({resource:'run-r',owner:'worker-a',expectedRevision:null,durationMs:HOUR})
  const first=provider([{call:{tool:'tracker__create_issue',arguments:{title:'Mileage'}}}])
  const events=await drive(fencedRunStore(store,fenceOf(a),new ReferenceEffectHost(store)),'run-r',[await actTool(execute)],first.call)
  const call=(events.find(e=>e.type==='approval_requested') as Extract<AssistantEvent,{type:'approval_requested'}>).callId
  expect(first.state).toEqual({requests:1,inFlight:0});expect(execute).not.toHaveBeenCalled()
  const parked=await parkRun({store,fence:fenceOf(a),wait:'wait-1',continuation:'k-1',condition:{event:{kind:'approval-decision',subject:call}},model:{provider:'openai',model:'fixture'},limits:{approvalTtlMs:HOUR},wakeKeyDigest:wakeKeyDigest(key),release:[{resource:'provider-account',outcome:'released'}]})
  restart()
  // The new instance holds the run closed, and its parked wait, from what it kept.
  await expect(store.acquireLease({resource:'run-r',owner:'worker-x',expectedRevision:parked.wait.checkpoint.revision,durationMs:HOUR})).rejects.toEqual(expect.objectContaining({name:'WaitRefused',reason:'wait-pending'}))
  expect(await store.readWait('run-r','wait-1')).toEqual(parked.wait)
  clock.at=T0+MINUTE
  const wake:WakeEvidence={version:1,resource:'run-r',wait:'wait-1',evidence:'decision-1',kind:'event',event:{kind:'approval-decision',subject:call},key}
  const disposition=await wakeRun(store,wake)
  restart()
  expect(await wakeRun(store,wake)).toEqual(disposition)
  const held=(await store.readWait('run-r','wait-1'))!,{model,...pins}=held.pins
  const resumed=await resumeParked({store,resource:'run-r',wait:'wait-1',evidence:'decision-1',owner:'worker-b',durationMs:24*HOUR,approval:{callId:call,approved:true},current:{version:1,resource:'run-r',wait:'wait-1',...pins,model:{...model,status:'available'},policy:{digest:held.pins.policy}}})
  expect(resumed.lease).toMatchObject({epoch:'2',expiry:new Date(T0+HOUR).toISOString()})
  restart()
  // The claim's lease, the approval's ceiling and the claimed wait are all the new instance's.
  expect((await store.renewLease(resumed.fence,24*HOUR)).expiry).toBe(new Date(T0+HOUR).toISOString())
  const second=provider([{say:'The issue was filed.'}])
  const continued=await drive(fencedRunStore(store,resumed.fence,new ReferenceEffectHost(store)),'run-r',[await actTool(execute)],second.call,{resume:true,approval:resumed.approval})
  expect(continued.some(e=>e.type==='error')).toBe(false);expect(execute).toHaveBeenCalledOnce()
  restart()
  expect(JSON.parse(new TextDecoder().decode((await store.load('run-r'))!.checkpoint!)).approval.state).toBe('answered')
  expect((await waitReport(store,'run-r')).entries.map(e=>e.decision)).toEqual(['park','wake','resume'])
  // The call settled before the restart: its expiry no longer bounds the run.
  await store.releaseLease(resumed.fence)
  clock.at=T0+2*HOUR
  await expect(store.acquireLease({resource:'run-r',owner:'worker-c',expectedRevision:(await store.load('run-r'))!.revision,durationMs:HOUR})).resolves.toMatchObject({epoch:'3',expiry:new Date(T0+3*HOUR).toISOString()})
 })
})
