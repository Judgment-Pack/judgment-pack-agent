import {describe,it,expect,vi} from 'vitest'
import type {AssistantEvent,EngineSession,HostTool} from '../engine'
import {vercel} from '../engines/vercel'
import {answerWith} from '../testing/scenarioDriver'
import {toolListDigest} from '../toolSources'
import {canonicalJSON} from '../context/chat'
import {MemoryRunStore,type RunStore} from './run'
import {LeaseTable,ReferenceEffectHost,fenceOf,fencedRunStore,type LeaseFence,type LeaseLifetime} from './lease'
import {approvalBinding,pinsDigest,pinsFrom} from './resume'
import {WaitTable,checkpointBytesDigest,limitRun,parkRun,readWakeEvidence,resumeParked,wakeKeyDigest,wakeRun,waitReport,type WaitClaim,type WaitLifetime,type WaitPark,type WakeEvidence} from './wait'
import pauseVector from '../../../contract/vectors/approval/pause.json'
import extendedVector from '../../../contract/vectors/plan/extended-checkpoints.json'
import nativeVector from '../../../contract/vectors/plan/native-checkpoints.json'
import decisionVector from '../../../contract/vectors/wait/decision.json'

/* #265: the pre-release batch for durable waits, deadlines and revalidation (the MEDIUM findings of the #264 and #266
 * security reviews). The Go twin is go/context/run_wait_review_test.go. */
const T0=Date.parse('2026-01-01T00:00:00.000Z'),MINUTE=60_000,HOUR=60*MINUTE
const iso=(ms:number)=>new Date(ms).toISOString()
const bytes=(value:unknown)=>new TextEncoder().encode(canonicalJSON(value))
const waitRefusal=(reason:string)=>expect.objectContaining({name:'WaitRefused',reason})
/** Stands for a host store kind whose leases and waits outlive its process. */
class DurableWaitStore extends MemoryRunStore {override readonly leaseLifetime:LeaseLifetime='durable';override readonly waitLifetime:WaitLifetime='durable'}
function clocked(){const clock={at:T0};const store=new DurableWaitStore({now:()=>new Date(clock.at)});return {clock,store}}
/** A fresh, high-entropy key per wait (#265 M1). */
const keyOf=(name:string)=>`wk.${name}.Q7z2Xm9LpV4cR8tNb3Yh6Df1Gj5K`
const MODEL={provider:'openai',model:'fixture'}
const asked=pauseVector.items[1]!.checkpoint as Record<string,unknown>
const budget=(used:number,limit:number)=>({...structuredClone(asked),approval:undefined,used,limit})
async function unchanged(store:RunStore,resource:string,wait:string){
 // The store's own record, whatever a forging subclass returns.
 const held=(await MemoryRunStore.prototype.readWait.call(store,resource,wait))!
 const {model,...rest}=held.pins
 return {version:1,resource,wait,...rest,model:{...model,status:'available'},policy:{digest:held.pins.policy}}
}
/** A run paused at `checkpoint`, committed under a fresh lease. */
async function paused(store:DurableWaitStore,run:string,checkpoint:unknown){
 const lease=await store.acquireLease({resource:run,owner:'worker-a',expectedRevision:null,durationMs:HOUR})
 await store.commitFenced({runId:run,expectedRevision:null,commitId:'pause',checkpoint:bytes(checkpoint),events:[]},fenceOf(lease))
 return fenceOf(lease)
}
const approvalPark=(store:DurableWaitStore,fence:LeaseFence,key:string,over:Partial<Parameters<typeof parkRun>[0]>={})=>parkRun({store,fence,wait:'wait-1',continuation:'k-1',condition:{event:{kind:'approval-decision',subject:'call_2_1'}},model:MODEL,limits:{approvalTtlMs:HOUR},wakeKeyDigest:wakeKeyDigest(key),release:[],...over})
const decision=(run:string,key:string,over:Partial<WakeEvidence>={}):WakeEvidence=>({version:1,resource:run,wait:'wait-1',evidence:'decision-1',kind:'event',event:{kind:'approval-decision',subject:'call_2_1'},key,...over})
const approve=async(store:DurableWaitStore,run:string,over:Partial<Parameters<typeof resumeParked>[0]>={})=>resumeParked({store,resource:run,wait:'wait-1',evidence:'decision-1',owner:'worker-b',durationMs:24*HOUR,approval:{callId:'call_2_1',approved:true},current:await unchanged(store,run,over.wait??'wait-1'),...over})

describe('#265 M1: a wake key belongs to exactly one wait',()=>{
 it('a short or repetitive key is refused wherever a key is read',()=>{
  for(const key of ['a'.repeat(36),'ab'.repeat(16),'wk.short.Q7z2Xm9LpV4cR8tNb3Yh6D'.slice(0,31)]){
   expect(()=>wakeKeyDigest(key),key).toThrow(waitRefusal('invalid'))
   expect(()=>readWakeEvidence({version:1,resource:'r',wait:'w',evidence:'e',kind:'time',key}),key).toThrow(waitRefusal('invalid'))
  }
  expect(wakeKeyDigest(keyOf('a'))).toMatch(/^sha256:[a-f0-9]{64}$/)
 })
 it('a park whose key digest another wait holds is refused key-reused, in another run or in the same one; the refused park keeps its lease and parks nothing',async()=>{
  const {clock,store}=clocked()
  await approvalPark(store,await paused(store,'run-a',asked),keyOf('a'))
  const b=await paused(store,'run-b',asked)
  await expect(approvalPark(store,b,keyOf('a'))).rejects.toEqual(waitRefusal('key-reused'))
  expect(await store.readLease('run-b')).toMatchObject({state:'held',epoch:'1'});expect(await store.readWait('run-b','wait-1')).toBeUndefined()
  await approvalPark(store,b,keyOf('b'))
  // Run A's evidence re-addressed to run B is no longer B's: B's key is its own.
  await expect(wakeRun(store,decision('run-b',keyOf('a')))).rejects.toEqual(waitRefusal('unauthenticated'))
  // The same run: a later wait of a run never takes an earlier wait's key.
  const c=await paused(store,'run-c',budget(20,20))
  await parkRun({store,fence:c,wait:'wait-1',continuation:'k-1',condition:{wakeAt:iso(T0+MINUTE)},model:MODEL,wakeKeyDigest:wakeKeyDigest(keyOf('c')),release:[]})
  clock.at=T0+MINUTE
  await wakeRun(store,{version:1,resource:'run-c',wait:'wait-1',evidence:'tick-1',kind:'time',key:keyOf('c')})
  const resumed=await resumeParked({store,resource:'run-c',wait:'wait-1',evidence:'tick-1',owner:'worker-b',durationMs:HOUR,grant:{from:20,to:30,at:iso(T0)},current:await unchanged(store,'run-c','wait-1')})
  await store.commitFenced({runId:'run-c',expectedRevision:(await store.load('run-c'))!.revision,commitId:'pause-2',checkpoint:bytes({...budget(30,30),grants:[{from:20,to:30,at:iso(T0)}]}),events:[]},resumed.fence)
  const again={store,fence:resumed.fence,wait:'wait-2',continuation:'k-2',condition:{wakeAt:iso(T0+HOUR)},model:MODEL,release:[]}
  await expect(parkRun({...again,wakeKeyDigest:wakeKeyDigest(keyOf('c'))})).rejects.toEqual(waitRefusal('key-reused'))
  expect(await store.readLease('run-c')).toMatchObject({state:'held',epoch:'2'})
  expect((await parkRun({...again,wakeKeyDigest:wakeKeyDigest(keyOf('c2'))})).wait.state).toBe('parked')
 })
})

describe('#265 M3 and #266 M5: no pause is parked that nothing can resume',()=>{
 it('a plan-drift pause and a provider-managed pause are refused not-resumable; nothing is parked and the lease stays held',async()=>{
  const cases:[string,unknown][]=[['plan-drift',extendedVector.items[5]],['provider-managed plan-drift',nativeVector.items[1]],['provider-managed budget',{...structuredClone(nativeVector.items[1]) as Record<string,unknown>,planState:{...(nativeVector.items[1] as {planState:Record<string,unknown>}).planState,pending:undefined,driftPolicy:undefined}}]]
  for(const [name,checkpoint] of cases){
   const {store}=clocked(),fence=await paused(store,'run-p',JSON.parse(JSON.stringify(checkpoint)))
   await expect(parkRun({store,fence,wait:'wait-1',continuation:'k',condition:{wakeAt:iso(T0+HOUR)},model:MODEL,wakeKeyDigest:wakeKeyDigest(keyOf('p')),release:[]}),name).rejects.toEqual(waitRefusal('not-resumable'))
   expect(await store.readWait('run-p','wait-1'),name).toBeUndefined();expect(await store.readLease('run-p'),name).toMatchObject({state:'held'})
  }
 })
})

describe('#265 M4: the budget grant\'s time is read by one rule in both languages',()=>{
 it('a grant is dated by a store-clock time; any other text is no grant',async()=>{
  const parkedBudget=async()=>{
   const {clock,store}=clocked()
   await parkRun({store,fence:await paused(store,'run-g',budget(20,20)),wait:'wait-1',continuation:'k',condition:{wakeAt:iso(T0+MINUTE)},model:MODEL,wakeKeyDigest:wakeKeyDigest(keyOf('g')),release:[]})
   clock.at=T0+MINUTE;await wakeRun(store,{version:1,resource:'run-g',wait:'wait-1',evidence:'tick-1',kind:'time',key:keyOf('g')})
   return store
  }
  const grant=async(store:DurableWaitStore,at:string)=>resumeParked({store,resource:'run-g',wait:'wait-1',evidence:'tick-1',owner:'worker-b',durationMs:HOUR,grant:{from:20,to:30,at},current:await unchanged(store,'run-g','wait-1')})
  const refused=await parkedBudget()
  for(const {problem,grant:{at}} of decisionVector.readerRefused)await expect(grant(refused,at),problem).rejects.toEqual(waitRefusal('decision-required'))
  for(const {at} of decisionVector.items)expect((await grant(await parkedBudget(),at)).grant,at).toEqual({from:20,to:30,at})
 })
})

describe('#265 M5: the safeguards the reviewer\'s mutants removed',()=>{
 async function twoWaits(){
  const {clock,store}=clocked()
  await parkRun({store,fence:await paused(store,'run-m',budget(20,20)),wait:'wait-1',continuation:'k-1',condition:{wakeAt:iso(T0+MINUTE)},model:MODEL,wakeKeyDigest:wakeKeyDigest(keyOf('m1')),release:[]})
  clock.at=T0+MINUTE;await wakeRun(store,{version:1,resource:'run-m',wait:'wait-1',evidence:'tick-1',kind:'time',key:keyOf('m1')})
  const resumed=await resumeParked({store,resource:'run-m',wait:'wait-1',evidence:'tick-1',owner:'worker-b',durationMs:HOUR,grant:{from:20,to:30,at:iso(T0)},current:await unchanged(store,'run-m','wait-1')})
  await store.commitFenced({runId:'run-m',expectedRevision:(await store.load('run-m'))!.revision,commitId:'pause-2',checkpoint:bytes({...budget(30,30),grants:[{from:20,to:30,at:iso(T0)}]}),events:[]},resumed.fence)
  await parkRun({store,fence:resumed.fence,wait:'wait-2',continuation:'k-2',condition:{wakeAt:iso(T0+2*MINUTE)},model:MODEL,wakeKeyDigest:wakeKeyDigest(keyOf('m2')),release:[]})
  return {clock,store}
 }
 it('MR1: a wake reaches the wait it names, never the run\'s parked one',async()=>{
  const {clock,store}=await twoWaits()
  clock.at=T0+2*MINUTE
  const parked=await store.readWait('run-m','wait-2')
  await expect(wakeRun(store,{version:1,resource:'run-m',wait:'wait-1',evidence:'tick-2',kind:'time',key:keyOf('m1')})).rejects.toEqual(waitRefusal('stale'))
  expect(await store.readWait('run-m','wait-2')).toEqual(parked)
  expect((await wakeRun(store,{version:1,resource:'run-m',wait:'wait-2',evidence:'tick-2',kind:'time',key:keyOf('m2')})).evidence).toBe('tick-2')
  expect((await store.readWait('run-m','wait-2'))!.state).toBe('woken');expect((await store.readWait('run-m','wait-1'))!.state).toBe('resumed')
 })
 it('MR2: a wake after the claim never re-opens the resumed wait, so it is never claimed twice',async()=>{
  const {store}=await twoWaits()
  const resumed=(await store.readWait('run-m','wait-1'))!
  expect(await wakeRun(store,{version:1,resource:'run-m',wait:'wait-1',evidence:'tick-1',kind:'time',key:keyOf('m1')})).toEqual(resumed.disposition)
  await expect(wakeRun(store,{version:1,resource:'run-m',wait:'wait-1',evidence:'tick-9',kind:'time',key:keyOf('m1')})).rejects.toEqual(waitRefusal('stale'))
  expect(await store.readWait('run-m','wait-1')).toEqual(resumed)
  await expect(resumeParked({store,resource:'run-m',wait:'wait-1',evidence:'tick-1',owner:'worker-c',durationMs:HOUR,grant:{from:20,to:30,at:iso(T0)},current:await unchanged(store,'run-m','wait-1')})).rejects.toEqual(waitRefusal('already-resumed'))
 })
 it('MR4: a park the store refuses leaves the caller\'s lease held, and the caller commits under it',async()=>{
  const {store}=clocked()
  await approvalPark(store,await paused(store,'run-a',asked),keyOf('a'))
  const b=await paused(store,'run-b',asked)
  await expect(approvalPark(store,b,keyOf('a'))).rejects.toEqual(waitRefusal('key-reused'))
  await expect(approvalPark(store,b,keyOf('b'),{limits:{approvalTtlMs:HOUR,run:iso(T0)}})).rejects.toEqual(waitRefusal('deadline-passed'))
  expect(await store.readLease('run-b')).toMatchObject({state:'held',epoch:'1'})
  await expect(store.commitFenced({runId:'run-b',expectedRevision:(await store.load('run-b'))!.revision,commitId:'after',checkpoint:null,events:[bytes({version:1})]},b)).resolves.toBeDefined()
 })
 it('MR5: the same park under another owner or epoch is a conflict, not the committed park',async()=>{
  const {store}=clocked(),fence=await paused(store,'run-a',asked)
  const parked=await approvalPark(store,fence,keyOf('a'))
  const head=(await store.load('run-a'))!
  const park:WaitPark={version:1,resource:'run-a',wait:'wait-1',reason:'approval',pending:parked.wait.pending,condition:parked.wait.condition,limits:parked.wait.limits!,approval:parked.wait.approval!,continuation:'k-1',checkpoint:{revision:head.revision,digest:checkpointBytesDigest(head.checkpoint!)},pins:parked.wait.pins,wakeKeyDigest:parked.wait.wakeKeyDigest,release:[]}
  expect(await store.parkWait(park,fence)).toEqual(parked)
  await expect(store.parkWait(park,{...fence,owner:'worker-x'})).rejects.toEqual(waitRefusal('conflict'))
  await expect(store.parkWait(park,{...fence,epoch:'9'})).rejects.toEqual(waitRefusal('conflict'))
 })
})

describe('#265 M6 and #266 M6: resumeParked reads what the store returns',()=>{
 async function woken(store:DurableWaitStore,clock:{at:number}){
  await approvalPark(store,await paused(store,'run-a',asked),keyOf('a'))
  clock.at=T0+MINUTE;await wakeRun(store,decision('run-a',keyOf('a')))
 }
 it('a malformed wait from the store is refused invalid, never read past',async()=>{
  for(const forge of [(w:Record<string,unknown>)=>({...w,pending:{kind:'budget',unit:'model-requests',used:1,limit:2}}),(w:Record<string,unknown>)=>{const {pending:_,...rest}=w;return rest},(w:Record<string,unknown>)=>({...w,wait:'wait-9'})]){
   class Forged extends DurableWaitStore {override async readWait(resource:string,wait:string){const held=await super.readWait(resource,wait);return held&&forge(held as never) as never}}
   const clock={at:T0},store=new Forged({now:()=>new Date(clock.at)})
   await woken(store,clock)
   await expect(approve(store,'run-a')).rejects.toEqual(waitRefusal('invalid'))
   expect(await store.readLease('run-a')).toMatchObject({state:'released',epoch:'1'})
  }
 })
 it('a claim result that is not this wait resumed under the returned lease, within the run deadline and the approval expiry, is refused invalid',async()=>{
  const forgeries:[string,(out:{wait:Record<string,unknown>;lease:Record<string,unknown>})=>unknown][]=[
   ['a lease past the approval expiry',out=>({...out,lease:{...out.lease,expiry:iso(T0+24*HOUR)}})],
   ['a wait that is not resumed',out=>({...out,wait:{...out.wait,state:'woken',resumed:undefined,revalidation:undefined}})],
   ['another owner\'s lease',out=>({...out,lease:{...out.lease,owner:'worker-x'}})],
   ['another epoch than the claim\'s',out=>({...out,lease:{...out.lease,epoch:'7'}})],
   ['another wait',out=>({...out,wait:{...out.wait,wait:'wait-9'}})],
   ['a released lease',out=>({...out,lease:{...out.lease,state:'released'}})],
  ]
  for(const [name,forge] of forgeries){
   class Forged extends DurableWaitStore {override async resumeWait(claim:WaitClaim){return forge(structuredClone(await super.resumeWait(claim)) as never) as never}}
   const clock={at:T0},store=new Forged({now:()=>new Date(clock.at)})
   await woken(store,clock)
   await expect(approve(store,'run-a'),name).rejects.toEqual(waitRefusal('invalid'))
  }
 })
})

describe('#266 M1: a run deadline set after the park reaches the wait',()=>{
 it('the claim of a woken wait is refused at the wait\'s own deadline, which bounds no lease itself',async()=>{
  const {clock,store}=clocked()
  await parkRun({store,fence:await paused(store,'run-w',budget(20,20)),wait:'wait-1',continuation:'k',condition:{wakeAt:iso(T0+MINUTE)},model:MODEL,limits:{waitMs:HOUR},wakeKeyDigest:wakeKeyDigest(keyOf('w')),release:[]})
  clock.at=T0+MINUTE;await wakeRun(store,{version:1,resource:'run-w',wait:'wait-1',evidence:'tick-1',kind:'time',key:keyOf('w')})
  const input={store,resource:'run-w',wait:'wait-1',evidence:'tick-1',owner:'worker-b',durationMs:HOUR,grant:{from:20,to:30,at:iso(T0)}}
  clock.at=T0+HOUR
  await expect(resumeParked({...input,current:await unchanged(store,'run-w','wait-1')})).rejects.toEqual(waitRefusal('deadline-passed'))
  expect(await store.readLease('run-w')).toMatchObject({state:'released',epoch:'1'})
 })
 it('the wait is re-stamped with the run\'s deadline: no wake or claim from it, and its deadline wake ends it',async()=>{
  const {clock,store}=clocked()
  await approvalPark(store,await paused(store,'run-a',asked),keyOf('a'),{limits:{approvalTtlMs:4*HOUR}})
  clock.at=T0+MINUTE
  await limitRun(store,{version:1,resource:'run-a',run:iso(T0+10*MINUTE)})
  expect((await store.readWait('run-a','wait-1'))!.deadline).toEqual({at:iso(T0+10*MINUTE),by:'run',run:iso(T0+10*MINUTE),approval:iso(T0+4*HOUR)})
  clock.at=T0+20*MINUTE
  await expect(wakeRun(store,decision('run-a',keyOf('a')))).rejects.toEqual(waitRefusal('deadline-passed'))
  const expired=await wakeRun(store,{version:1,resource:'run-a',wait:'wait-1',evidence:'deadline-1',kind:'deadline',key:keyOf('a')})
  expect(expired).toMatchObject({kind:'deadline',at:iso(T0+20*MINUTE)})
  expect((await waitReport(store,'run-a')).entries.map(e=>e.decision)).toEqual(['park','deadline'])
  await expect(store.acquireLease({resource:'run-a',owner:'worker-x',expectedRevision:(await store.load('run-a'))!.revision,durationMs:HOUR})).rejects.toEqual(waitRefusal('closed'))
 })
 it('a woken wait is re-stamped too, and its claim is refused at the run\'s deadline; nothing is leased',async()=>{
  const {clock,store}=clocked()
  await approvalPark(store,await paused(store,'run-a',asked),keyOf('a'),{limits:{approvalTtlMs:4*HOUR}})
  clock.at=T0+5*MINUTE;await wakeRun(store,decision('run-a',keyOf('a')))
  await limitRun(store,{version:1,resource:'run-a',run:iso(T0+6*MINUTE)})
  clock.at=T0+7*MINUTE
  await expect(approve(store,'run-a')).rejects.toEqual(waitRefusal('deadline-passed'))
  expect(await store.readLease('run-a')).toMatchObject({state:'released',epoch:'1'});expect((await store.readWait('run-a','wait-1'))!.state).toBe('woken')
 })
 it('a run limit before the park: the wait carries the earliest deadline it can, and the store\'s own deadline refuses a wake and a claim at once',async()=>{
  const {store}=clocked()
  await approvalPark(store,await paused(store,'run-a',asked),keyOf('a'),{limits:{approvalTtlMs:4*HOUR}})
  await wakeRun(store,decision('run-a',keyOf('a')))
  await limitRun(store,{version:1,resource:'run-a',run:'1970-01-01T00:00:00.000Z'})
  expect((await store.readWait('run-a','wait-1'))!.deadline).toEqual({at:iso(T0+1),by:'run',run:iso(T0+1),approval:iso(T0+4*HOUR)})
  // At the park's own instant: the wait's stamp is not yet due, but the run's deadline is past.
  await expect(approve(store,'run-a')).rejects.toEqual(waitRefusal('deadline-passed'))
  const {store:other}=clocked()
  await approvalPark(other,await paused(other,'run-a',asked),keyOf('a'),{limits:{approvalTtlMs:4*HOUR}})
  await limitRun(other,{version:1,resource:'run-a',run:'1970-01-01T00:00:00.000Z'})
  await expect(wakeRun(other,decision('run-a',keyOf('a')))).rejects.toEqual(waitRefusal('deadline-passed'))
 })
})

// A paused act call through the real vercel engine.
const ACT={name:'create_issue',description:'Create one issue.',inputSchema:{type:'object'},taxonomy:{operation:'act',effect:'act'} as const}
async function actTool(execute:HostTool['execute']):Promise<HostTool>{
 return {...ACT,effectClass:'act',source:{kind:'custom',id:'tracker',trust:'person',version:'1',toolListDigest:await toolListDigest([ACT] as never),identity:{kind:'connection',label:'tracker'}},approve:()=>'ask',execute} as HostTool
}
function provider(turns:Parameters<typeof answerWith>[0][]){
 const state={requests:0}
 return {state,call:async()=>{const turn=turns[state.requests];state.requests++;if(!turn)throw Error('no scripted turn');return answerWith(turn,'stream',state.requests-1)}}
}
async function drive(store:RunStore,id:string,tools:HostTool[],call:()=>Promise<Response>,extra:{resume?:boolean;approval?:{callId:string;approved:boolean}}={}):Promise<AssistantEvent[]>{
 const session:EngineSession={prompt:'File the issue.',testPrompt:'',tools:[],callTool:vi.fn(async()=>({content:[]})),hostTools:tools,signal:new AbortController().signal,allowConversation:true,critic:'off',
  context:{currentRequest:{message:'File the issue.',input:'issue'},runStore:{store,id,...(extra.resume?{resume:true}:{})},...(extra.approval?{approval:extra.approval}:{})},model:{family:'openai-compatible',model:'fixture',call}} as EngineSession
 const out:AssistantEvent[]=[];for await(const event of vercel.start(session))out.push(event);return out
}
async function engineApproval(){
 const {clock,store}=clocked(),host=new ReferenceEffectHost(store),execute=vi.fn(async()=>({content:[{type:'text' as const,text:'created'}]}))
 const a=await store.acquireLease({resource:'run-e',owner:'worker-a',expectedRevision:null,durationMs:HOUR})
 const events=await drive(fencedRunStore(store,fenceOf(a),host),'run-e',[await actTool(execute)],provider([{call:{tool:'tracker__create_issue',arguments:{title:'Mileage'}}}]).call)
 const call=(events.find(e=>e.type==='approval_requested') as Extract<AssistantEvent,{type:'approval_requested'}>).callId
 const parked=await parkRun({store,fence:fenceOf(a),wait:'wait-1',continuation:'k-1',condition:{event:{kind:'approval-decision',subject:call}},model:MODEL,limits:{approvalTtlMs:HOUR},wakeKeyDigest:wakeKeyDigest(keyOf('e')),release:[]})
 const wake:WakeEvidence={version:1,resource:'run-e',wait:'wait-1',evidence:'decision-1',kind:'event',event:{kind:'approval-decision',subject:call},key:keyOf('e')}
 return {clock,store,host,execute,call,parked,wake}
}

describe('#266 M4: no unfenced commit continues a run a wait holds',()=>{
 it('the raw store refuses a commit to a parked, woken or closed run, and the attended engine over it runs nothing',async()=>{
  const {clock,store,execute,call,parked,wake}=await engineApproval()
  const head=(await store.load('run-e'))!
  const outside=(commitId:string)=>store.commit({runId:'run-e',expectedRevision:head.revision,commitId,checkpoint:null,events:[bytes({version:1})]})
  await expect(outside('outside-1')).rejects.toEqual(waitRefusal('wait-pending'))
  const attended=await drive(store,'run-e',[await actTool(execute)],provider([{say:'Filed.'}]).call,{resume:true,approval:{callId:call,approved:true}})
  expect(attended.some(e=>e.type==='error')).toBe(true);expect(execute).not.toHaveBeenCalled()
  clock.at=T0+MINUTE;await wakeRun(store,wake)
  await expect(outside('outside-2')).rejects.toEqual(waitRefusal('wait-pending'))
  clock.at=T0+HOUR
  await wakeRun(store,{version:1,resource:'run-e',wait:'wait-1',evidence:'deadline-1',kind:'deadline',key:keyOf('e')})
  await expect(outside('outside-3')).rejects.toEqual(waitRefusal('closed'))
  expect((await store.load('run-e'))!.revision).toBe(parked.wait.checkpoint.revision);expect((await store.readWait('run-e','wait-1'))!.state).toBe('expired')
 })
})

describe('#266 N1: an approved call that has not settled by its expiry ends its wait with an unknown outcome',()=>{
 it('the deadline wake, due at the approval\'s expiry, ends the resumed wait; the run is closed for good and nothing dispatches the call',async()=>{
  const {clock,store,host,execute,parked,wake}=await engineApproval()
  clock.at=T0+MINUTE;await wakeRun(store,wake)
  const resumed=await approve(store,'run-e',{approval:{callId:parked.wait.approval!.callId,approved:true}})
  // The claimer records the decision and stops before it dispatches: the call is approved and unsettled.
  const head=(await store.load('run-e'))!,approved=JSON.parse(new TextDecoder().decode(head.checkpoint!))
  approved.approval.state='approved'
  await store.commitFenced({runId:'run-e',expectedRevision:head.revision,commitId:'approved',checkpoint:bytes(approved),events:[]},resumed.fence)
  const deadline={version:1 as const,resource:'run-e',wait:'wait-1',evidence:'deadline-1',kind:'deadline' as const,key:keyOf('e')}
  clock.at=T0+HOUR-1
  await expect(wakeRun(store,deadline)).rejects.toEqual(waitRefusal('not-due'))
  clock.at=T0+HOUR
  await expect(store.acquireLease({resource:'run-e',owner:'worker-c',expectedRevision:(await store.load('run-e'))!.revision,durationMs:HOUR})).rejects.toEqual(waitRefusal('deadline-passed'))
  const ended=await wakeRun(store,deadline)
  expect(ended).toMatchObject({evidence:'deadline-1',kind:'deadline',at:iso(T0+HOUR)})
  expect(await wakeRun(store,deadline)).toEqual(ended)
  await expect(wakeRun(store,{...deadline,evidence:'deadline-2'})).rejects.toEqual(waitRefusal('stale'))
  const held=(await store.readWait('run-e','wait-1'))!
  expect(held).toMatchObject({state:'expired',resumed:{owner:'worker-b',epoch:'2'},expired:ended})
  expect((await waitReport(store,'run-e')).entries.map(e=>e.decision)).toEqual(['park','wake','resume','deadline'])
  // Closed for good: no lease, no commit, and the takeover never runs the call.
  clock.at=T0+30*24*HOUR
  await expect(store.acquireLease({resource:'run-e',owner:'worker-c',expectedRevision:(await store.load('run-e'))!.revision,durationMs:HOUR})).rejects.toEqual(waitRefusal('closed'))
  await expect(store.commit({runId:'run-e',expectedRevision:(await store.load('run-e'))!.revision,commitId:'late',checkpoint:null,events:[bytes({version:1})]})).rejects.toEqual(waitRefusal('closed'))
  const continued=await drive(fencedRunStore(store,resumed.fence,host),'run-e',[await actTool(execute)],provider([{say:'Filed.'}]).call,{resume:true,approval:resumed.approval})
  expect(continued.some(e=>e.type==='error')).toBe(true);expect(execute).not.toHaveBeenCalled()
 })
 it('a settled call is never ended by it: the deadline wake on its resumed wait stays stale',async()=>{
  const {clock,store,host,execute,parked,wake}=await engineApproval()
  clock.at=T0+MINUTE;await wakeRun(store,wake)
  const resumed=await approve(store,'run-e',{approval:{callId:parked.wait.approval!.callId,approved:true}})
  await drive(fencedRunStore(store,resumed.fence,host),'run-e',[await actTool(execute)],provider([{say:'Filed.'}]).call,{resume:true,approval:resumed.approval})
  expect(execute).toHaveBeenCalledOnce()
  clock.at=T0+HOUR
  await expect(wakeRun(store,{version:1,resource:'run-e',wait:'wait-1',evidence:'deadline-1',kind:'deadline',key:keyOf('e')})).rejects.toEqual(waitRefusal('stale'))
  expect((await store.readWait('run-e','wait-1'))!.state).toBe('resumed')
 })
})

describe('#266 N2: a later wait\'s claim is bounded by an earlier unsettled approval, in both languages',()=>{
 it('the budget wait claimed while the approval is unsettled gets a lease that ends at the approval\'s expiry',()=>{
  const clock={at:T0},leases=new LeaseTable(()=>new Date(clock.at)),table=new WaitTable(()=>new Date(clock.at))
  const first=bytes(asked),approval=JSON.parse(new TextDecoder().decode(first))
  table.park({version:1,resource:'run-t',wait:'wait-1',reason:'approval',pending:{kind:'approval',callId:'call_2_1',name:'tracker__create_issue',tool:'create_issue'},condition:{event:{kind:'approval-decision',subject:'call_2_1'}},limits:{approvalTtlMs:HOUR},approval:approvalBinding(approval.approval),continuation:'k-1',checkpoint:{revision:'rev-1',digest:checkpointBytesDigest(first)},pins:pinsFrom(approval,MODEL),wakeKeyDigest:wakeKeyDigest(keyOf('t1')),release:[]},fenceOf(leases.acquire({resource:'run-t',owner:'worker-a',expectedRevision:'rev-1',durationMs:HOUR},'rev-1')),{revision:'rev-1',checkpoint:first},leases)
  clock.at=T0+MINUTE
  table.wake({version:1,resource:'run-t',wait:'wait-1',evidence:'decision-1',kind:'event',event:{kind:'approval-decision',subject:'call_2_1'},key:keyOf('t1')})
  const claim=(wait:string,evidence:string,revision:string,pins:ReturnType<typeof pinsFrom>):WaitClaim=>({resource:'run-t',wait,evidence,lease:{resource:'run-t',owner:'worker-b',expectedRevision:revision,durationMs:24*HOUR},reconciled:[],revalidation:{version:1,resource:'run-t',wait,pins:pinsDigest(pins),evidence:'sha256:'+'e'.repeat(64),changes:[]}})
  const claimed=table.resume(claim('wait-1','decision-1','rev-1',pinsFrom(approval,MODEL)),{revision:'rev-1',checkpoint:first},leases)
  expect(claimed.lease.expiry).toBe(iso(T0+HOUR))
  // A budget pause over the unsettled approval: a checkpoint the package's writer does not produce, driven directly.
  const second=bytes({...approval,approval:{...approval.approval,state:'approved'},used:20,limit:20}),later=JSON.parse(new TextDecoder().decode(second))
  table.park({version:1,resource:'run-t',wait:'wait-2',reason:'budget',pending:{kind:'budget',unit:'model-requests',used:20,limit:20},condition:{wakeAt:iso(T0+2*MINUTE)},continuation:'k-2',checkpoint:{revision:'rev-2',digest:checkpointBytesDigest(second)},pins:pinsFrom(later,MODEL),wakeKeyDigest:wakeKeyDigest(keyOf('t2')),release:[]},fenceOf(claimed.lease),{revision:'rev-2',checkpoint:second},leases)
  clock.at=T0+2*MINUTE
  table.wake({version:1,resource:'run-t',wait:'wait-2',evidence:'tick-1',kind:'time',key:keyOf('t2')})
  const budgetClaim=table.resume(claim('wait-2','tick-1','rev-2',pinsFrom(later,MODEL)),{revision:'rev-2',checkpoint:second},leases)
  expect(budgetClaim.lease.expiry).toBe(iso(T0+HOUR))
 })
})
