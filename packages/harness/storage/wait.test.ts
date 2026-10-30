import {describe,it,expect,vi} from 'vitest'
import type {AssistantEvent,EngineSession,HostTool} from '../engine'
import {vercel} from '../engines/vercel'
import {answerWith} from '../testing/scenarioDriver'
import {toolListDigest} from '../toolSources'
import {canonicalJSON} from '../context/chat'
import {questionTool} from '../questions'
import {MemoryRunStore,checkpointCallbackRunStore,type RunStore} from './run'
import {MemoryChatStore} from './chat'
import {ReferenceEffectHost,fenceOf,fencedRunStore,type LeaseFence,type LeaseLifetime} from './lease'
import {WAIT_CAPABILITY_REASONS,WAIT_REFUSAL_REASONS,WaitCapabilityRefused,WaitRefused,admitWaits,answerDigest,attentionFor,attentionId,checkpointBytesDigest,exportWaits,parkRun,questionDigest,readAttentionDelivery,readAttentionEvent,readRunWait,readWaitCapabilityRefusal,readWaitExport,readWaitRefusal,readWaitReport,readWakeEvidence,reportFromExport,resumeParked,wakeEvidenceDigest,wakeKeyDigest,waitReport,wakeRun,type WaitLifetime,type WakeEvidence} from './wait'
import waitVector from '../../../contract/vectors/wait/wait.json'
import evidenceVector from '../../../contract/vectors/wait/evidence.json'
import attentionVector from '../../../contract/vectors/wait/attention.json'
import reportVector from '../../../contract/vectors/wait/report.json'
import refusalVector from '../../../contract/vectors/wait/refusal.json'
import capabilityVector from '../../../contract/vectors/wait/capability.json'
import pauseVector from '../../../contract/vectors/approval/pause.json'

const T0=Date.parse('2026-01-01T00:00:00.000Z'),MINUTE=60_000,HOUR=60*MINUTE
const refusal=(reason:string)=>expect.objectContaining({name:'WaitRefused',reason})
const leaseRefusal=(reason:string)=>expect.objectContaining({name:'LeaseRefused',reason})
const capability=(reason:string)=>expect.objectContaining({name:'WaitCapabilityRefused',reason})
/** Stands for a host store kind whose leases and waits outlive its process. */
class DurableWaitStore extends MemoryRunStore {override readonly leaseLifetime:LeaseLifetime='durable';override readonly waitLifetime:WaitLifetime='durable'}
function clocked(){const clock={at:T0};const store=new DurableWaitStore({now:()=>new Date(clock.at)});return {clock,store}}
const KEY='host-wake-key-for-tests-0001',OTHER_KEY='host-wake-key-for-other-run-02'
const iso=(ms:number)=>new Date(ms).toISOString()
const decode=(bytes:Uint8Array)=>new TextDecoder().decode(bytes)

describe('shared wait vectors (#224, #228)',()=>{
 it('both readers accept every wait and refuse every refused record',()=>{
  for(const item of waitVector.items)expect(readRunWait(structuredClone(item))).toEqual(item)
  for(const item of [...waitVector.refused,...waitVector.readerRefused])expect(()=>readRunWait(structuredClone(item.record)),item.problem).toThrow(refusal('invalid'))
 })
 it('evidence: readers, digests without the key, and key digests',()=>{
  evidenceVector.items.forEach((item,i)=>{expect(readWakeEvidence(structuredClone(item))).toEqual(item);expect(wakeEvidenceDigest(item as WakeEvidence)).toBe(evidenceVector.digests[i])})
  for(const [key,digest] of evidenceVector.keys.digests)expect(wakeKeyDigest(key!)).toBe(digest)
  for(const item of [...evidenceVector.refused,...evidenceVector.readerRefused])expect(()=>readWakeEvidence(structuredClone(item.evidence)),item.problem).toThrow(refusal('invalid'))
  // The vector's dispositions are the evidence digests the waits retained.
  const byEvidence=new Map(evidenceVector.items.map((item,i)=>[item.evidence,evidenceVector.digests[i]]))
  for(const wait of waitVector.items)if(wait.disposition&&byEvidence.has(wait.disposition.evidence))expect(wait.disposition.digest).toBe(byEvidence.get(wait.disposition.evidence))
 })
 it('attention: one event per wait, exactly the event each park records, and deliveries apart from it',()=>{
  attentionVector.items.forEach((item,i)=>{expect(readAttentionEvent(structuredClone(item))).toEqual(item);expect(attentionFor(readRunWait(waitVector.items[i]))).toEqual(item)})
  expect(attentionVector.items).toHaveLength(waitVector.items.length)
  for(const item of attentionVector.deliveries)expect(readAttentionDelivery(structuredClone(item))).toEqual(item)
  for(const item of [...attentionVector.refused,...attentionVector.readerRefused])expect(()=>readAttentionEvent(structuredClone(item.event)),item.problem).toThrow(refusal('invalid'))
  for(const item of attentionVector.deliveriesRefused)expect(()=>readAttentionDelivery(structuredClone(item.delivery)),item.problem).toThrow(refusal('invalid'))
  expect(attentionId('run-approval','wait-1')).toBe(waitVector.items[0]!.attention)
 })
 it('report: both languages derive the handwritten report from each export at the host time',()=>{
  for(const item of reportVector.exports)expect(readWaitExport(structuredClone(item))).toEqual(item)
  for(const item of reportVector.items)expect(readWaitReport(structuredClone(item))).toEqual(item)
  expect(reportVector.derivations.map(d=>d.report)).toEqual(reportVector.items.map((_,i)=>i))
  for(const d of reportVector.derivations)expect(reportFromExport(structuredClone(reportVector.exports[d.export]) as never,new Date(d.now))).toEqual(reportVector.items[d.report])
  for(const item of reportVector.refused)expect(()=>readWaitReport(structuredClone(item.report)),item.problem).toThrow(refusal('invalid'))
  for(const item of reportVector.readerRefused){
   if('export' in item)expect(()=>readWaitExport(structuredClone(item.export)),item.problem).toThrow(refusal('invalid'))
   else expect(()=>readWaitReport(structuredClone(item.report)),item.problem).toThrow(refusal('invalid'))
  }
 })
 it('refusals: every reason both languages give, and nothing else',()=>{
  expect(refusalVector.items.map(item=>readWaitRefusal(item).reason)).toEqual([...WAIT_REFUSAL_REASONS])
  expect(refusalVector.items.map(item=>new WaitRefused(item.reason as never).record())).toEqual(refusalVector.items)
  for(const item of refusalVector.refused)expect(()=>readWaitRefusal(item)).toThrow(refusal('invalid'))
  expect(capabilityVector.items.map(item=>readWaitCapabilityRefusal(item).reason)).toEqual([...WAIT_CAPABILITY_REASONS])
  expect(capabilityVector.items.map(item=>new WaitCapabilityRefused(item.reason as never).record())).toEqual(capabilityVector.items)
  for(const item of capabilityVector.refused)expect(()=>readWaitCapabilityRefusal(item)).toThrow(refusal('invalid'))
  expect(new WaitRefused('stale').failure).toMatchObject({code:'stale-state',outcome:'not-started'})
  expect(new WaitCapabilityRefused('store-lifetime').failure).toMatchObject({code:'configuration',outcome:'not-started'})
 })
})

// A paused act call through the real vercel engine: the host's policy asks, the run ends paused, nothing held.
const ACT={name:'create_issue',description:'Create one issue.',inputSchema:{type:'object'},taxonomy:{operation:'act',effect:'act'} as const}
async function actTool(execute:HostTool['execute']):Promise<HostTool>{
 return {...ACT,effectClass:'act',source:{kind:'custom',id:'tracker',trust:'person',version:'1',toolListDigest:await toolListDigest([ACT] as never),identity:{kind:'connection',label:'tracker'}},approve:()=>'ask',execute} as HostTool
}
interface Provider {requests:number;inFlight:number}
function provider(turns:Parameters<typeof answerWith>[0][]):{state:Provider;call:()=>Promise<Response>}{
 const state:Provider={requests:0,inFlight:0}
 return {state,call:async()=>{state.inFlight++;try{const turn=turns[state.requests];state.requests++;if(!turn)throw Error('no scripted turn');return answerWith(turn,'stream',state.requests-1)}finally{state.inFlight--}}}
}
async function drive(store:RunStore,id:string,tools:HostTool[],call:()=>Promise<Response>,extra:{resume?:boolean;approval?:{callId:string;approved:boolean;note?:string};interactive?:boolean}={}):Promise<AssistantEvent[]>{
 const session:EngineSession={prompt:'File the issue.',testPrompt:'',tools:[],callTool:vi.fn(async()=>({content:[]})),hostTools:tools,signal:new AbortController().signal,allowConversation:true,critic:'off',...(extra.interactive?{interactive:true}:{}),
  context:{currentRequest:{message:'File the issue.',input:'issue'},runStore:{store,id,...(extra.resume?{resume:true}:{})},...(extra.approval?{approval:extra.approval}:{})},model:{family:'openai-compatible',model:'fixture',call}} as EngineSession
 const out:AssistantEvent[]=[];for await(const event of vercel.start(session))out.push(event);return out
}
const evidenceFor=(over:Partial<WakeEvidence>&Pick<WakeEvidence,'evidence'|'kind'>):WakeEvidence=>({version:1,resource:'run-approval',wait:'wait-1',key:KEY,...over})

describe('a durable approval wait across a real store restart (#224)',()=>{
 it('parks atomically with no held provider, wakes once, resumes under a new lease and runs the call once',async()=>{
  const {clock,store}=clocked(),host=new ReferenceEffectHost(store),execute=vi.fn(async()=>({content:[{type:'text' as const,text:'created'}]}))
  const a=await store.acquireLease({resource:'run-approval',owner:'worker-a',expectedRevision:null,durationMs:HOUR})
  const first=provider([{call:{tool:'tracker__create_issue',arguments:{title:'Mileage'}}}])
  const events=await drive(fencedRunStore(store,fenceOf(a),host),'run-approval',[await actTool(execute)],first.call)
  const asked=events.find(e=>e.type==='approval_requested') as Extract<AssistantEvent,{type:'approval_requested'}>
  expect(asked).toBeDefined();expect(events.at(-1)?.type).toBe('end');expect(events.some(e=>e.type==='error')).toBe(false)
  // No provider is held open for the person: the one request has returned and the engine has ended.
  expect(first.state).toEqual({requests:1,inFlight:0});expect(execute).not.toHaveBeenCalled()
  const paused=(await store.load('run-approval'))!,before=(await store.readEvents('run-approval',null,100)).items.map(e=>decode(e.bytes))
  const parked=await parkRun({store,fence:fenceOf(a),wait:'wait-1',continuation:'continue-1',condition:{event:{kind:'approval-decision',subject:asked.callId},wakeAt:iso(T0+6*HOUR)},timeoutAt:iso(T0+48*HOUR),wakeKeyDigest:wakeKeyDigest(KEY),release:[{resource:'provider-account',outcome:'released'}]})
  expect(parked.wait).toMatchObject({state:'parked',reason:'approval',pending:{kind:'approval',callId:asked.callId,name:'tracker__create_issue',tool:'create_issue'},parked:{owner:'worker-a',epoch:'1',at:iso(T0)},checkpoint:{revision:paused.revision,digest:checkpointBytesDigest(paused.checkpoint!)}})
  expect(parked.attention).toEqual({version:1,type:'needs_attention',id:attentionId('run-approval','wait-1'),resource:'run-approval',wait:'wait-1',reason:'approval',needs:'approval-decision',subject:asked.callId,condition:{event:{kind:'approval-decision',subject:asked.callId},wakeAt:iso(T0+6*HOUR)},timeoutAt:iso(T0+48*HOUR),releaseUncertain:false,at:iso(T0)})
  // The lease was released in the park's transaction; the parking driver can commit nothing more.
  expect(await store.readLease('run-approval')).toMatchObject({state:'released',epoch:'1'})
  await expect(store.commitFenced({runId:'run-approval',expectedRevision:paused.revision,commitId:'late',checkpoint:paused.checkpoint,events:[]},fenceOf(a))).rejects.toEqual(leaseRefusal('released'))
  // The park wrote nothing into the run's own checkpoint or event log: those bytes are unchanged.
  const after=(await store.load('run-approval'))!
  expect(after.revision).toBe(paused.revision);expect(decode(after.checkpoint!)).toBe(decode(paused.checkpoint!))
  expect((await store.readEvents('run-approval',null,100)).items.map(e=>decode(e.bytes))).toEqual(before)
  for(const word of ['wait-1','needs_attention','wakeKeyDigest'])expect([decode(after.checkpoint!),...before].join('\n')).not.toContain(word)

  // A new process: nothing but the store and the effect host survives.
  clock.at+=HOUR
  const wake=evidenceFor({evidence:'decision-1',kind:'event',event:{kind:'approval-decision',subject:asked.callId}})
  const disposition=await wakeRun(store,wake)
  expect(disposition).toEqual({evidence:'decision-1',kind:'event',at:iso(T0+HOUR),digest:wakeEvidenceDigest(wake)})
  clock.at+=MINUTE
  expect(await wakeRun(store,wake)).toEqual(disposition)
  // A wake is not the approval: without the person's decision for this call nothing resumes.
  await expect(resumeParked({store,resource:'run-approval',wait:'wait-1',evidence:'decision-1',owner:'worker-b',durationMs:HOUR})).rejects.toEqual(refusal('decision-required'))
  await expect(resumeParked({store,resource:'run-approval',wait:'wait-1',evidence:'decision-1',owner:'worker-b',durationMs:HOUR,approval:{callId:'another-call',approved:true}})).rejects.toEqual(refusal('decision-required'))
  await expect(resumeParked({store,resource:'run-approval',wait:'wait-1',evidence:'decision-1',owner:'worker-b',durationMs:HOUR,grant:{from:20,to:30,at:iso(T0)}})).rejects.toEqual(refusal('decision-required'))
  // An acknowledgement the store does not hold is reconciled by reading, never by dispatching.
  await expect(resumeParked({store,resource:'run-approval',wait:'wait-1',evidence:'decision-unseen',owner:'worker-b',durationMs:HOUR,approval:{callId:asked.callId,approved:true}})).rejects.toEqual(refusal('unacknowledged'))
  expect(await store.readLease('run-approval')).toMatchObject({state:'released',epoch:'1'})
  const resumed=await resumeParked({store,resource:'run-approval',wait:'wait-1',evidence:'decision-1',owner:'worker-b',durationMs:HOUR,approval:{callId:asked.callId,approved:true,note:'Go ahead.'}})
  expect(resumed.lease).toMatchObject({owner:'worker-b',epoch:'2',state:'held'});expect(resumed.wait).toMatchObject({state:'resumed',resumed:{owner:'worker-b',epoch:'2',at:iso(T0+HOUR+MINUTE)}})
  expect(resumed.approval).toEqual({callId:asked.callId,approved:true,note:'Go ahead.'})
  await expect(resumeParked({store,resource:'run-approval',wait:'wait-1',evidence:'decision-1',owner:'worker-c',durationMs:HOUR,approval:{callId:asked.callId,approved:true}})).rejects.toEqual(refusal('already-resumed'))
  const second=provider([{say:'The issue was filed.'}])
  const continued=await drive(fencedRunStore(store,resumed.fence,host),'run-approval',[await actTool(execute)],second.call,{resume:true,approval:resumed.approval})
  expect(continued.some(e=>e.type==='error')).toBe(false);expect(continued.at(-1)?.type).toBe('end')
  expect(execute).toHaveBeenCalledOnce();expect(second.state).toEqual({requests:1,inFlight:0})
  expect(JSON.parse(decode((await store.load('run-approval'))!.checkpoint!)).approval.state).toBe('answered')
  // The stale driver's fence is refused at the store after the takeover.
  await expect(store.commitFenced({runId:'run-approval',expectedRevision:(await store.load('run-approval'))!.revision,commitId:'stale',checkpoint:paused.checkpoint,events:[]},fenceOf(a))).rejects.toEqual(leaseRefusal('stale-epoch'))
  const report=await waitReport(store,'run-approval')
  expect(report.entries.map(e=>e.decision)).toEqual(['park','wake','resume'])
  expect(report.entries[0]).toMatchObject({owner:'worker-a',epoch:'1',subject:asked.callId,release:'clean'})
  expect(report.entries[2]).toMatchObject({owner:'worker-b',epoch:'2'})
 })
 it('legacy pauses park without being rewritten, and stay continuable the old way',async()=>{
  const {store}=clocked(),legacy=pauseVector.items[1]!.checkpoint as Record<string,unknown>,bytes=new TextEncoder().encode(canonicalJSON(legacy))
  const a=await store.acquireLease({resource:'run-legacy',owner:'worker-a',expectedRevision:null,durationMs:HOUR})
  await store.commitFenced({runId:'run-legacy',expectedRevision:null,commitId:'legacy-pause',checkpoint:bytes,events:[]},fenceOf(a))
  const parked=await parkRun({store,fence:fenceOf(a),wait:'wait-legacy',continuation:'continue-legacy',condition:{event:{kind:'approval-decision',subject:'call_2_1'}},wakeKeyDigest:wakeKeyDigest(KEY),release:[]})
  expect(parked.wait.pending).toEqual({kind:'approval',callId:'call_2_1',name:'tracker__create_issue',tool:'create_issue'})
  expect(decode((await store.load('run-legacy'))!.checkpoint!)).toBe(canonicalJSON(legacy))
  expect(JSON.parse(decode((await store.load('run-legacy'))!.checkpoint!)).version).toBe(1)
 })
 it('a budget pause is a budget wait: a wake is not a grant, and only a grant from the paused limit resumes it',async()=>{
  const {clock,store}=clocked()
  const a=await store.acquireLease({resource:'run-approval',owner:'worker-a',expectedRevision:null,durationMs:HOUR})
  const budget={...structuredClone(pauseVector.items[1]!.checkpoint as Record<string,unknown>),approval:undefined,used:20,limit:20}
  await store.commitFenced({runId:'run-approval',expectedRevision:null,commitId:'budget-pause',checkpoint:new TextEncoder().encode(canonicalJSON(budget)),events:[]},fenceOf(a))
  const parked=await parkRun({store,fence:fenceOf(a),wait:'wait-1',continuation:'continue-budget',condition:{wakeAt:iso(T0+HOUR)},wakeKeyDigest:wakeKeyDigest(KEY),release:[]})
  expect(parked.wait.pending).toEqual({kind:'budget',unit:'model-requests',used:20,limit:20});expect(parked.attention).toMatchObject({needs:'budget-grant'});expect(parked.attention.subject).toBeUndefined()
  await expect(wakeRun(store,evidenceFor({evidence:'tick-early',kind:'time'}))).rejects.toEqual(refusal('not-due'))
  clock.at+=HOUR
  await wakeRun(store,evidenceFor({evidence:'tick-1',kind:'time'}))
  await expect(resumeParked({store,resource:'run-approval',wait:'wait-1',evidence:'tick-1',owner:'worker-b',durationMs:HOUR})).rejects.toEqual(refusal('decision-required'))
  await expect(resumeParked({store,resource:'run-approval',wait:'wait-1',evidence:'tick-1',owner:'worker-b',durationMs:HOUR,grant:{from:10,to:30,at:iso(T0)}})).rejects.toEqual(refusal('decision-required'))
  await expect(resumeParked({store,resource:'run-approval',wait:'wait-1',evidence:'tick-1',owner:'worker-b',durationMs:HOUR,approval:{callId:'x',approved:true}})).rejects.toEqual(refusal('decision-required'))
  const resumed=await resumeParked({store,resource:'run-approval',wait:'wait-1',evidence:'tick-1',owner:'worker-b',durationMs:HOUR,grant:{from:20,to:30,at:iso(T0+HOUR)}})
  expect(resumed.grant).toEqual({from:20,to:30,at:iso(T0+HOUR)});expect(resumed.approval).toBeUndefined()
 })
})

async function parkedApproval(timeoutAt?:number){
 const {clock,store}=clocked()
 const a=await store.acquireLease({resource:'run-approval',owner:'worker-a',expectedRevision:null,durationMs:HOUR})
 await store.commitFenced({runId:'run-approval',expectedRevision:null,commitId:'pause',checkpoint:new TextEncoder().encode(canonicalJSON(pauseVector.items[1]!.checkpoint)),events:[]},fenceOf(a))
 const parked=await parkRun({store,fence:fenceOf(a),wait:'wait-1',continuation:'continue-1',condition:{event:{kind:'approval-decision',subject:'call_2_1'},wakeAt:iso(T0+HOUR)},...(timeoutAt===undefined?{}:{timeoutAt:iso(timeoutAt)}),wakeKeyDigest:wakeKeyDigest(KEY),release:[]})
 return {clock,store,fence:fenceOf(a),parked}
}
const decisionEvent=evidenceFor({evidence:'decision-1',kind:'event',event:{kind:'approval-decision',subject:'call_2_1'}})
describe('wake evidence: one atomic winner; duplicate, stale, conflicting and cross-run wakes change nothing (#224)',()=>{
 for(const order of ['event-first','time-first'] as const)it(`event or time, raced ${order}: exactly one wins and the other is stale`,async()=>{
  const {clock,store}=await parkedApproval()
  clock.at=T0+HOUR
  const time=evidenceFor({evidence:'tick-1',kind:'time'})
  const raced=order==='event-first'?[wakeRun(store,decisionEvent),wakeRun(store,time)]:[wakeRun(store,time),wakeRun(store,decisionEvent)]
  const settled=await Promise.allSettled(raced)
  expect(settled.filter(s=>s.status==='fulfilled')).toHaveLength(1)
  expect(settled.filter(s=>s.status==='rejected').map(s=>(s as PromiseRejectedResult).reason)).toEqual([refusal('stale')])
  const winner=(settled.find(s=>s.status==='fulfilled') as PromiseFulfilledResult<{evidence:string}>).value
  expect(winner.evidence).toBe(order==='event-first'?'decision-1':'tick-1')
  expect((await store.readWait('run-approval','wait-1'))!.disposition!.evidence).toBe(winner.evidence)
 })
 it('duplicates keep their disposition; a conflicting duplicate, a stale or a foreign wake changes nothing',async()=>{
  const {clock,store}=await parkedApproval()
  const first=await wakeRun(store,decisionEvent)
  clock.at+=MINUTE
  expect(await wakeRun(store,decisionEvent)).toEqual(first)
  const held=await store.readWait('run-approval','wait-1')
  await expect(wakeRun(store,{...decisionEvent,event:{kind:'approval-decision',subject:'call-other'}})).rejects.toEqual(refusal('conflict'))
  clock.at=T0+2*HOUR
  await expect(wakeRun(store,evidenceFor({evidence:'tick-late',kind:'time'}))).rejects.toEqual(refusal('stale'))
  await expect(wakeRun(store,{...decisionEvent,evidence:'decision-2'})).rejects.toEqual(refusal('stale'))
  // Cross-run: another run's key, or this wait named under another run, is refused before anything is read.
  await expect(wakeRun(store,{...decisionEvent,evidence:'decision-3',key:OTHER_KEY})).rejects.toEqual(refusal('unauthenticated'))
  await expect(wakeRun(store,{...decisionEvent,resource:'run-other'})).rejects.toEqual(refusal('no-wait'))
  expect(await store.readWait('run-approval','wait-1')).toEqual(held)
 })
 it('a wake must match the condition it was parked on, and be due',async()=>{
  const {clock,store}=await parkedApproval(T0+3*HOUR)
  const held=await store.readWait('run-approval','wait-1')
  await expect(wakeRun(store,{...decisionEvent,event:{kind:'approval-decision',subject:'call-9'}})).rejects.toEqual(refusal('mismatch'))
  await expect(wakeRun(store,{...decisionEvent,event:{kind:'job-complete',subject:'call_2_1'}})).rejects.toEqual(refusal('mismatch'))
  await expect(wakeRun(store,{...decisionEvent,answer:{selected:[],text:'yes'},actor:'person'})).rejects.toEqual(refusal('mismatch'))
  await expect(wakeRun(store,evidenceFor({evidence:'tick-0',kind:'time'}))).rejects.toEqual(refusal('not-due'))
  clock.at=T0+3*HOUR-1
  await expect(wakeRun(store,evidenceFor({evidence:'deadline-0',kind:'timeout'}))).rejects.toEqual(refusal('not-due'))
  expect(await store.readWait('run-approval','wait-1')).toEqual(held)
  // Equal time is due.
  clock.at=T0+3*HOUR
  expect((await wakeRun(store,evidenceFor({evidence:'deadline-1',kind:'timeout'}))).kind).toBe('timeout')
  // A timed-out approval is never approved by its timeout.
  await expect(resumeParked({store,resource:'run-approval',wait:'wait-1',evidence:'deadline-1',owner:'worker-b',durationMs:HOUR,approval:{callId:'call_2_1',approved:true}})).rejects.toEqual(refusal('timed-out'))
 })
 it('park refusals: one parked wait per run, an unchanged head, and no pending decision is none',async()=>{
  const {store,fence,parked}=await parkedApproval()
  await expect(parkRun({store,fence,wait:'wait-1',continuation:'continue-1',condition:{event:{kind:'approval-decision',subject:'call_2_1'},wakeAt:iso(T0+HOUR)},wakeKeyDigest:wakeKeyDigest(KEY),release:[]})).resolves.toEqual(parked)
  await expect(parkRun({store,fence,wait:'wait-1',continuation:'continue-other',condition:{event:{kind:'approval-decision',subject:'call_2_1'}},wakeKeyDigest:wakeKeyDigest(KEY),release:[]})).rejects.toEqual(refusal('conflict'))
  // The parking driver's released fence parks nothing more.
  await expect(parkRun({store,fence,wait:'wait-2',continuation:'continue-2',condition:{event:{kind:'approval-decision',subject:'call_2_1'}},wakeKeyDigest:wakeKeyDigest(KEY),release:[]})).rejects.toEqual(leaseRefusal('released'))
  const b=await store.acquireLease({resource:'run-approval',owner:'worker-b',expectedRevision:parked.wait.checkpoint.revision,durationMs:HOUR})
  await expect(parkRun({store,fence:fenceOf(b),wait:'wait-2',continuation:'continue-2',condition:{event:{kind:'approval-decision',subject:'call_2_1'}},wakeKeyDigest:wakeKeyDigest(KEY),release:[]})).rejects.toEqual(refusal('wait-pending'))
  const {store:empty}=clocked(),c=await empty.acquireLease({resource:'run-empty',owner:'worker-a',expectedRevision:null,durationMs:HOUR})
  await expect(parkRun({store:empty,fence:fenceOf(c),wait:'w',continuation:'k',condition:{wakeAt:iso(T0)},wakeKeyDigest:wakeKeyDigest(KEY),release:[]})).rejects.toEqual(refusal('nothing-pending'))
  const running={...structuredClone(pauseVector.items[1]!.checkpoint as Record<string,unknown>),status:'running',approval:undefined}
  await empty.commitFenced({runId:'run-empty',expectedRevision:null,commitId:'running',checkpoint:new TextEncoder().encode(canonicalJSON(running)),events:[]},fenceOf(c))
  await expect(parkRun({store:empty,fence:fenceOf(c),wait:'w',continuation:'k',condition:{wakeAt:iso(T0)},wakeKeyDigest:wakeKeyDigest(KEY),release:[]})).rejects.toEqual(refusal('nothing-pending'))
  await expect(parkRun({store:empty,fence:fenceOf(c),wait:'w',continuation:'k',condition:{event:{kind:'answer',subject:'q'}},question:'q-unknown',wakeKeyDigest:wakeKeyDigest(KEY),release:[]})).rejects.toEqual(refusal('nothing-pending'))
 })
 it('the store refuses a park whose head moved after it was read',async()=>{
  const {store}=clocked()
  const a=await store.acquireLease({resource:'run-moved',owner:'worker-a',expectedRevision:null,durationMs:HOUR})
  const bytes=new TextEncoder().encode(canonicalJSON(pauseVector.items[1]!.checkpoint))
  const receipt=await store.commitFenced({runId:'run-moved',expectedRevision:null,commitId:'pause',checkpoint:bytes,events:[]},fenceOf(a))
  const park={version:1 as const,resource:'run-moved',wait:'w',reason:'approval' as const,pending:{kind:'approval' as const,callId:'call_2_1',name:'tracker__create_issue',tool:'create_issue'},condition:{wakeAt:iso(T0)},continuation:'k',checkpoint:{revision:receipt.revision,digest:checkpointBytesDigest(bytes)},wakeKeyDigest:wakeKeyDigest(KEY),release:[]}
  await store.commitFenced({runId:'run-moved',expectedRevision:receipt.revision,commitId:'moved',checkpoint:null,events:[new TextEncoder().encode('{"version":1}')]},fenceOf(a))
  await expect(store.parkWait({...park,checkpoint:{revision:(await store.load('run-moved'))!.revision,digest:checkpointBytesDigest(bytes)}},{...fenceOf(a),owner:'worker-x'})).rejects.toEqual(leaseRefusal('other-owner'))
  await expect(store.parkWait(park,fenceOf(a))).rejects.toEqual(refusal('checkpoint-changed'))
  await expect(store.parkWait({...park,checkpoint:{revision:(await store.load('run-moved'))!.revision,digest:'sha256:'+'0'.repeat(64)}},fenceOf(a))).rejects.toEqual(refusal('checkpoint-changed'))
  expect(await store.readLease('run-moved')).toMatchObject({state:'held'});expect(await store.readWait('run-moved','w')).toBeUndefined()
 })
})

describe('the store claims a continuation once (#224)',()=>{
 it('a second claim is already-resumed and a timed-out approval is never claimed',async()=>{
  const {clock,store,parked}=await parkedApproval(T0+2*HOUR)
  clock.at=T0+2*HOUR
  await wakeRun(store,evidenceFor({evidence:'deadline-1',kind:'timeout'}))
  const b=await store.acquireLease({resource:'run-approval',owner:'worker-b',expectedRevision:parked.wait.checkpoint.revision,durationMs:HOUR})
  await expect(store.resumeWait('run-approval','wait-1','deadline-1',fenceOf(b),[])).rejects.toEqual(refusal('timed-out'))
  const second=await parkedApproval()
  const c=await second.store.acquireLease({resource:'run-approval',owner:'worker-c',expectedRevision:second.parked.wait.checkpoint.revision,durationMs:HOUR})
  // The store claims nothing for a wake it does not hold: a parked wait, or another evidence id.
  await expect(second.store.resumeWait('run-approval','wait-1','decision-1',fenceOf(c),[])).rejects.toEqual(refusal('unacknowledged'))
  second.clock.at+=MINUTE;await wakeRun(second.store,decisionEvent)
  await expect(second.store.resumeWait('run-approval','wait-1','decision-unseen',fenceOf(c),[])).rejects.toEqual(refusal('unacknowledged'))
  expect((await second.store.resumeWait('run-approval','wait-1','decision-1',fenceOf(c),[])).state).toBe('resumed')
  await expect(second.store.resumeWait('run-approval','wait-1','decision-1',fenceOf(c),[])).rejects.toEqual(refusal('already-resumed'))
 })
 it('only the run\'s latest retained question is pending',async()=>{
  const {store}=clocked()
  const a=await store.acquireLease({resource:'run-q2',owner:'worker-a',expectedRevision:null,durationMs:HOUR})
  const prompt={title:'Buyer',question:'Who first?',selection:'single',options:[],recommendedOptionId:null,recommendation:null}
  const envelope=(id:string,title:string)=>new TextEncoder().encode(JSON.stringify({version:1,scope:{execution:'run-q2',role:'author'},event:{type:'user_question',id,question:{...prompt,title}}}))
  const complete={...structuredClone(pauseVector.items[1]!.checkpoint as Record<string,unknown>),status:'complete',approval:undefined}
  await store.commitFenced({runId:'run-q2',expectedRevision:null,commitId:'asked',checkpoint:new TextEncoder().encode(canonicalJSON(complete)),events:[envelope('q-0','Earlier'),envelope('q-1','Later')]},fenceOf(a))
  const input={store,fence:fenceOf(a),wait:'w',continuation:'k',condition:{event:{kind:'answer',subject:'q-1'}},wakeKeyDigest:wakeKeyDigest(KEY),release:[]}
  await expect(parkRun({...input,question:'q-0'})).rejects.toEqual(refusal('nothing-pending'))
  expect((await parkRun({...input,question:'q-1'})).wait.pending).toMatchObject({kind:'question',event:'q-1',question:{title:'Later'}})
 })
})

describe('question waits: real answers with provenance; a timeout is unanswered (#224)',()=>{
 async function asked(){
  const {clock,store}=clocked(),host=new ReferenceEffectHost(store)
  const a=await store.acquireLease({resource:'run-question',owner:'worker-a',expectedRevision:null,durationMs:HOUR})
  const interview={title:'Buyer',question:'Who should we focus on as the first buyer?',selection:'single',options:[{id:'operations',label:'Head of operations',description:'Owns recurring exception approvals.'},{id:'risk',label:'Risk or compliance lead',description:'Owns oversight.'}],recommendedOptionId:'operations',recommendation:'A starting hypothesis.'}
  const model=provider([{call:{tool:'ask_user',arguments:interview}}])
  const events=await drive(fencedRunStore(store,fenceOf(a),host),'run-question',[questionTool],model.call,{interactive:true})
  const question=events.find(e=>e.type==='user_question') as Extract<AssistantEvent,{type:'user_question'}>
  expect(question).toBeDefined();expect(events.at(-1)?.type).toBe('end');expect(model.state).toEqual({requests:1,inFlight:0})
  const parked=await parkRun({store,fence:fenceOf(a),wait:'wait-q',continuation:'continue-q',condition:{event:{kind:'question-answer',subject:question.id}},timeoutAt:iso(T0+2*HOUR),wakeKeyDigest:wakeKeyDigest(KEY),release:[{resource:'provider-account',outcome:'uncertain'}],question:question.id,retainUntil:iso(T0+10*HOUR)})
  return {clock,store,question,parked,interview}
 }
 const answer=(id:string,over:Partial<WakeEvidence>={}):WakeEvidence=>({version:1,resource:'run-question',wait:'wait-q',evidence:'answer-1',kind:'event',event:{kind:'question-answer',subject:id},answer:{selected:['risk'],text:''},actor:'person-reviewer',key:KEY,...over})
 it('retains the question, options and recommendation from the event log; records the real answer, its actor and time',async()=>{
  const {clock,store,question,parked,interview}=await asked()
  expect(parked.wait.pending).toEqual({kind:'question',event:question.id,questionDigest:questionDigest(interview as never),question:interview})
  expect(parked.attention).toMatchObject({needs:'question-answer',subject:question.id,releaseUncertain:true})
  // A recommendation is not an answer, an empty draft is not one, and an option not offered is not one.
  await expect(wakeRun(store,answer(question.id,{answer:{selected:[],text:'   '}}))).rejects.toEqual(refusal('mismatch'))
  await expect(wakeRun(store,answer(question.id,{answer:{selected:['it'],text:''}}))).rejects.toEqual(refusal('mismatch'))
  const {answer:_answer,actor:_actor,...unanswered}=answer(question.id)
  await expect(wakeRun(store,unanswered)).rejects.toEqual(refusal('mismatch'))
  clock.at+=MINUTE
  await wakeRun(store,answer(question.id))
  const woken=(await store.readWait('run-question','wait-q'))!
  expect(woken.resolution).toEqual({kind:'answered',at:iso(T0+MINUTE),actor:'person-reviewer',answerDigest:answerDigest({selected:['risk'],text:''}),answer:{selected:['risk'],text:''}})
  // The uncertain release is retained: resuming needs the host's reconciliation of it.
  await expect(resumeParked({store,resource:'run-question',wait:'wait-q',evidence:'answer-1',owner:'worker-b',durationMs:HOUR})).rejects.toEqual(refusal('release-uncertain'))
  expect(await store.readLease('run-question')).toMatchObject({state:'released'})
  await expect(resumeParked({store,resource:'run-question',wait:'wait-q',evidence:'answer-1',owner:'worker-b',durationMs:HOUR,reconciled:[{resource:'provider-account',outcome:'uncertain'}]})).rejects.toEqual(refusal('release-uncertain'))
  await expect(resumeParked({store,resource:'run-question',wait:'wait-q',evidence:'answer-1',owner:'worker-b',durationMs:HOUR,approval:{callId:question.id,approved:true},reconciled:[{resource:'provider-account',outcome:'released'}]})).rejects.toEqual(refusal('decision-required'))
  const resumed=await resumeParked({store,resource:'run-question',wait:'wait-q',evidence:'answer-1',owner:'worker-b',durationMs:HOUR,reconciled:[{resource:'provider-account',outcome:'released'}]})
  expect(resumed.resolution).toEqual(woken.resolution);expect(resumed.wait.reconciled).toEqual([{resource:'provider-account',outcome:'released'}])
 })
 it('a timeout ends the question unanswered and never picks the recommended option',async()=>{
  const {clock,store}=await asked()
  clock.at=T0+2*HOUR
  const disposition=await wakeRun(store,{version:1,resource:'run-question',wait:'wait-q',evidence:'deadline-1',kind:'timeout',key:KEY})
  const timed=(await store.readWait('run-question','wait-q'))!
  expect(timed.state).toBe('timed-out');expect(timed.resolution).toEqual({kind:'unanswered',at:disposition.at})
  expect(JSON.stringify(timed.resolution)).not.toContain('operations')
  const resumed=await resumeParked({store,resource:'run-question',wait:'wait-q',evidence:'deadline-1',owner:'worker-b',durationMs:HOUR,reconciled:[{resource:'provider-account',outcome:'not-held'}]})
  expect(resumed.resolution).toEqual({kind:'unanswered',at:disposition.at})
  const report=await waitReport(store,'run-question')
  expect(report.entries.map(e=>e.decision)).toEqual(['park','timeout','unanswered','resume'])
  expect(report.entries[2]!.content!.question!.recommendedOptionId).toBe('operations');expect(report.entries.some(e=>e.decision==='answer')).toBe(false)
 })
})

describe('attention, delivery and the report (#228)',()=>{
 it('one attention event per committed park; delivery is apart from it and a failed one rolls back nothing',async()=>{
  const {store,parked}=await parkedApproval()
  const failed=await store.recordDelivery({attention:parked.attention.id,delivery:'mail-1',outcome:'failed'})
  expect(await store.recordDelivery({attention:parked.attention.id,delivery:'mail-1',outcome:'failed'})).toEqual(failed)
  await expect(store.recordDelivery({attention:parked.attention.id,delivery:'mail-1',outcome:'sent'})).rejects.toEqual(refusal('conflict'))
  await store.recordDelivery({attention:parked.attention.id,delivery:'mail-2',outcome:'sent'})
  await store.recordDelivery({attention:parked.attention.id,delivery:'mail-2',outcome:'sent'})
  await expect(store.recordDelivery({attention:attentionId('run-approval','wait-9'),delivery:'mail-3',outcome:'sent'})).rejects.toEqual(refusal('no-wait'))
  const exported=await exportWaits(store,'run-approval')
  expect(exported.attention).toEqual([parked.attention]);expect(exported.deliveries.map(d=>[d.delivery,d.outcome])).toEqual([['mail-1','failed'],['mail-2','sent']])
  expect(exported.waits).toEqual([parked.wait]);expect(exported.waits[0]!.state).toBe('parked')
 })
 it('report sources survive compaction and a store round trip; redacted and expired content stays unavailable; a report grants no resume',async()=>{
  const {clock,store,parked}=await (async()=>{
   const {clock,store}=clocked(),host=new ReferenceEffectHost(store)
   const a=await store.acquireLease({resource:'run-question',owner:'worker-a',expectedRevision:null,durationMs:HOUR})
   const interview={title:'Region',question:'Which region first?',selection:'single',options:[{id:'north',label:'North',description:''},{id:'south',label:'South',description:''}],recommendedOptionId:'north',recommendation:'Most sites.'}
   const events=await drive(fencedRunStore(store,fenceOf(a),host),'run-question',[questionTool],provider([{call:{tool:'ask_user',arguments:interview}}]).call,{interactive:true})
   const question=events.find(e=>e.type==='user_question') as Extract<AssistantEvent,{type:'user_question'}>
   const parked=await parkRun({store,fence:fenceOf(a),wait:'wait-q',continuation:'continue-q',condition:{event:{kind:'question-answer',subject:question.id}},wakeKeyDigest:wakeKeyDigest(KEY),release:[],question:question.id,retainUntil:iso(T0+10*HOUR)})
   clock.at+=MINUTE
   await wakeRun(store,{version:1,resource:'run-question',wait:'wait-q',evidence:'answer-1',kind:'event',event:{kind:'question-answer',subject:question.id},answer:{selected:['south'],text:'Closer to the team.'},actor:'person-lead',key:KEY})
   return {clock,store,parked}
  })()
  const resumed=await resumeParked({store,resource:'run-question',wait:'wait-q',evidence:'answer-1',owner:'worker-b',durationMs:HOUR})
  const report=await waitReport(store,'run-question',new Date(T0+HOUR))
  expect(report.entries.map(e=>[e.decision,e.content?.state])).toEqual([['park','available'],['wake',undefined],['answer','available'],['resume',undefined]])
  expect(report.entries[2]).toMatchObject({actor:'person-lead',at:iso(T0+MINUTE),content:{answer:{selected:['south'],text:'Closer to the team.'}}})
  // The run goes on and its retained context is compacted: the report reads the waits, not the prompt.
  const head=(await store.load('run-question'))!,checkpoint=JSON.parse(decode(head.checkpoint!))
  const compacted={...checkpoint,context:{scope:checkpoint.context.scope,entries:[]}}
  await store.commitFenced({runId:'run-question',expectedRevision:head.revision,commitId:'compacted',checkpoint:new TextEncoder().encode(canonicalJSON(compacted)),events:[]},resumed.fence)
  expect(await waitReport(store,'run-question',new Date(T0+HOUR))).toEqual(report)
  // A store round trip of the source: the exported records read back give the same report.
  const exported=await exportWaits(store,'run-question'),text=JSON.stringify(exported)
  expect(reportFromExport(JSON.parse(text),new Date(T0+HOUR))).toEqual(report)
  // The report and the export carry no key, no fence and no decision.
  expect(JSON.stringify(report)).not.toContain(KEY);expect(text).not.toContain(KEY)
  for(const word of ['wakeKeyDigest','fence','approved','grant'])expect(JSON.stringify(report)).not.toContain(word)
  // A report cannot resume: the continuation was claimed, and its contents are no decision.
  await expect(resumeParked({store,resource:'run-question',wait:'wait-q',evidence:report.entries[1]!.evidence!,owner:'worker-c',durationMs:HOUR})).rejects.toEqual(refusal('already-resumed'))
  // Expired: from retainUntil, by the host's time; the digests and every identity are kept.
  clock.at=T0+10*HOUR
  const expired=await waitReport(store,'run-question',new Date(T0+10*HOUR))
  expect(expired.entries.map(e=>e.content?.state)).toEqual(['expired',undefined,'expired',undefined])
  expect(expired.entries[0]!.content).toEqual({state:'expired',digest:parked.wait.pending.kind==='question'?parked.wait.pending.questionDigest:''})
  // Redacted: the content is removed in the store, references stay.
  const redacted=await store.redactWait('run-question','wait-q')
  expect(redacted.redacted).toBe(true);expect(JSON.stringify(redacted)).not.toContain('Closer to the team');expect(JSON.stringify(redacted)).not.toContain('Which region first')
  const after=await waitReport(store,'run-question',new Date(T0+HOUR))
  expect(after.entries.map(e=>e.content?.state)).toEqual(['redacted',undefined,'redacted',undefined])
  expect(after.entries.map(e=>({...e,content:e.content?.digest}))).toEqual(report.entries.map(e=>({...e,content:e.content?.digest})))
 })
})

describe('capability refusals and off-byte parity (#224)',()=>{
 it('stores and hosts that cannot keep the guarantee refuse by name',async()=>{
  const memory=new MemoryRunStore(),durable=new DurableWaitStore(),fence:LeaseFence={version:1,resource:'r',owner:'o',epoch:'1'}
  const callback=checkpointCallbackRunStore(async()=>{},bytes=>bytes),plain:RunStore={durability:'host',load:memory.load.bind(memory),commit:memory.commit.bind(memory),readEvents:memory.readEvents.bind(memory)}
  expect(()=>admitWaits({store:memory})).toThrow(capability('store-lifetime'))
  expect(()=>admitWaits({store:callback})).toThrow(capability('store-unsupported'))
  expect(()=>admitWaits({store:plain})).toThrow(capability('store-unsupported'))
  class LeaseOnly extends MemoryRunStore {override readonly leaseLifetime:LeaseLifetime='durable'}
  expect(()=>admitWaits({store:new LeaseOnly()})).toThrow(capability('store-lifetime'))
  expect(()=>admitWaits({store:durable})).not.toThrow()
  const legacy={consistency:'single-writer-legacy' as const,get:async()=>undefined,list:async()=>({items:[],hasMore:false}),update:async()=>{throw Error('unused')}}
  expect(()=>admitWaits({store:durable,chatStore:legacy})).toThrow(capability('chat-store-unsupported'))
  expect(()=>admitWaits({store:durable,chatStore:new MemoryChatStore()})).not.toThrow()
  for(const store of [memory,callback,plain])await expect(parkRun({store,fence,wait:'w',continuation:'k',condition:{wakeAt:iso(T0)},wakeKeyDigest:wakeKeyDigest(KEY),release:[]})).rejects.toBeInstanceOf(WaitCapabilityRefused)
  await expect(wakeRun(memory,{version:1,resource:'r',wait:'w',evidence:'e',kind:'time',key:KEY})).rejects.toEqual(capability('store-lifetime'))
  await expect(exportWaits(plain,'r')).rejects.toEqual(capability('store-unsupported'))
 })
 it('a stored chat scope is bound at park and revalidated through the ChatStore on resume',async()=>{
  const {clock,store}=clocked(),chats=new MemoryChatStore([{chat:{id:'chat-1',interaction:'work'}}])
  const a=await store.acquireLease({resource:'run-approval',owner:'worker-a',expectedRevision:null,durationMs:HOUR})
  await store.commitFenced({runId:'run-approval',expectedRevision:null,commitId:'pause',checkpoint:new TextEncoder().encode(canonicalJSON(pauseVector.items[1]!.checkpoint)),events:[]},fenceOf(a))
  const head=(await chats.get('chat-1'))!
  const parked=await parkRun({store,fence:fenceOf(a),wait:'wait-1',continuation:'k',condition:{event:{kind:'approval-decision',subject:'call_2_1'}},wakeKeyDigest:wakeKeyDigest(KEY),release:[],chat:{store:chats,id:'chat-1'}})
  expect(parked.wait.chat).toEqual({id:'chat-1',revision:head.revision})
  clock.at+=MINUTE;await wakeRun(store,decisionEvent)
  const input={store,resource:'run-approval',wait:'wait-1',evidence:'decision-1',owner:'worker-b',durationMs:HOUR,approval:{callId:'call_2_1',approved:false}}
  await expect(resumeParked(input)).rejects.toEqual(capability('chat-store-unsupported'))
  await chats.update('chat-1',{interaction:'plan'},head.revision)
  await expect(resumeParked({...input,chat:chats})).rejects.toEqual(refusal('chat-changed'))
  expect(await store.readLease('run-approval')).toMatchObject({state:'released',epoch:'1'})
 })
 it('a resume whose run moved while parked is refused and takes no lease',async()=>{
  const {clock,store,parked}=await parkedApproval()
  clock.at+=MINUTE;await wakeRun(store,decisionEvent)
  await store.commit({runId:'run-approval',expectedRevision:parked.wait.checkpoint.revision,commitId:'outside',checkpoint:null,events:[new TextEncoder().encode('{"version":1}')]})
  await expect(resumeParked({store,resource:'run-approval',wait:'wait-1',evidence:'decision-1',owner:'worker-b',durationMs:HOUR,approval:{callId:'call_2_1',approved:true}})).rejects.toEqual(leaseRefusal('revision-changed'))
  expect(await store.readLease('run-approval')).toMatchObject({state:'released',epoch:'1'});expect((await store.readWait('run-approval','wait-1'))!.state).toBe('woken')
 })
 it('the store revalidates the head and the fence when it claims the continuation',async()=>{
  const {clock,store,parked}=await parkedApproval()
  clock.at+=MINUTE;await wakeRun(store,decisionEvent)
  const b=await store.acquireLease({resource:'run-approval',owner:'worker-b',expectedRevision:parked.wait.checkpoint.revision,durationMs:HOUR})
  await expect(store.resumeWait('run-approval','wait-1','decision-1',{...fenceOf(b),owner:'worker-x'},[])).rejects.toEqual(leaseRefusal('other-owner'))
  await store.commitFenced({runId:'run-approval',expectedRevision:parked.wait.checkpoint.revision,commitId:'moved',checkpoint:null,events:[new TextEncoder().encode('{"version":1}')]},fenceOf(b))
  await expect(store.resumeWait('run-approval','wait-1','decision-1',fenceOf(b),[])).rejects.toEqual(refusal('checkpoint-changed'))
  expect((await store.readWait('run-approval','wait-1'))!.state).toBe('woken')
 })
 it('off-byte parity: with nothing configured a paused run stores the same bytes and the store keeps no wait',async()=>{
  const record=async()=>{
   const store=new MemoryRunStore({now:()=>new Date(T0)}),execute=vi.fn(async()=>({content:[]}))
   await drive(store,'parity',[await actTool(execute)],provider([{call:{tool:'tracker__create_issue',arguments:{title:'Mileage'}}}]).call)
   const shape=(value:unknown):unknown=>Array.isArray(value)?value.map(shape):value&&typeof value==='object'?Object.fromEntries(Object.entries(value).map(([k,v])=>[k,shape(v)])):typeof value
   const head=(await store.load('parity'))!,events=(await store.readEvents('parity',null,100)).items.map(e=>decode(e.bytes))
   return {store,checkpoint:shape(JSON.parse(decode(head.checkpoint!))),events:events.map(e=>shape(JSON.parse(e))),text:[decode(head.checkpoint!),...events].join('\n')}
  }
  const one=await record(),two=await record()
  expect(one.checkpoint).toEqual(two.checkpoint);expect(one.events).toEqual(two.events)
  for(const word of ['needs_attention','wakeKeyDigest','"wait"','attention'])expect(one.text).not.toContain(word)
  expect((await one.store.exportWaits('parity')).waits).toEqual([])
 })
})
