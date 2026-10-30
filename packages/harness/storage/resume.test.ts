import {describe,it,expect,vi} from 'vitest'
import type {AssistantEvent,EngineSession,HostTool} from '../engine'
import {vercel} from '../engines/vercel'
import {answerWith} from '../testing/scenarioDriver'
import {toolListDigest} from '../toolSources'
import {canonicalJSON} from '../context/chat'
import {MemoryRunStore,type RunStore} from './run'
import {questionTool} from '../questions'
import {LeaseTable,ReferenceEffectHost,effectAdmission,fenceOf,fencedRunStore,type LeaseLifetime} from './lease'
import {RESUME_REFUSAL_REASONS,ResumeRefused,approvalBinding,pinsDigest,pinsFrom,policyIdentity,readResumeEvidence,readResumePins,readResumeRefusal,readRevalidation,readRunLimit,readWaitDeadline,readWaitLimits,resumeEvidenceDigest,revalidateResume,stampDeadline,type ResumePins} from './resume'
import {WaitTable,checkpointBytesDigest,limitRun,parkRun,resumeParked,wakeKeyDigest,wakeRun,waitReport,type WaitLifetime,type WakeEvidence} from './wait'
import limitsVector from '../../../contract/vectors/resume/limits.json'
import pinsVector from '../../../contract/vectors/resume/pins.json'
import evidenceVector from '../../../contract/vectors/resume/evidence.json'
import revalidationVector from '../../../contract/vectors/resume/revalidation.json'
import refusalVector from '../../../contract/vectors/resume/refusal.json'
import pauseVector from '../../../contract/vectors/approval/pause.json'

const T0=Date.parse('2026-01-01T00:00:00.000Z'),MINUTE=60_000,HOUR=60*MINUTE
const iso=(ms:number)=>new Date(ms).toISOString()
const resumeRefusal=(reason:string)=>expect.objectContaining({name:'ResumeRefused',reason})
const waitRefusal=(reason:string)=>expect.objectContaining({name:'WaitRefused',reason})
const leaseRefusal=(reason:string)=>expect.objectContaining({name:'LeaseRefused',reason})
const ms=(text:string|undefined)=>text===undefined?undefined:Date.parse(text)
/** Stands for a host store kind whose leases and waits outlive its process. */
class DurableWaitStore extends MemoryRunStore {override readonly leaseLifetime:LeaseLifetime='durable';override readonly waitLifetime:WaitLifetime='durable'}
function clocked(){const clock={at:T0};const store=new DurableWaitStore({now:()=>new Date(clock.at)});return {clock,store}}
const KEY='host-wake-key-for-resume-0001'
const MODEL={provider:'openai',model:'fixture'}

describe('shared resume vectors (#225, #227)',()=>{
 it('limits, run limits and the deadline a park stamps: the earliest wins and a later run limit never extends one',()=>{
  for(const item of limitsVector.items)expect(readWaitLimits(structuredClone(item))).toEqual(item)
  for(const item of limitsVector.refused)expect(()=>readWaitLimits(structuredClone(item.limits)),item.problem).toThrow(resumeRefusal('invalid'))
  for(const item of limitsVector.runLimits.items)expect(readRunLimit(structuredClone(item))).toEqual(item)
  for(const item of limitsVector.runLimits.refused)expect(()=>readRunLimit(structuredClone(item.limit)),item.problem).toThrow(resumeRefusal('invalid'))
  for(const stamp of limitsVector.stamps){
   const limits='limits' in stamp?stamp.limits:undefined,ceiling='ceiling' in stamp?ms(stamp.ceiling as string):undefined
   expect(stampDeadline(limits,Date.parse(stamp.parkedAt),ceiling),stamp.case).toEqual('deadline' in stamp?stamp.deadline:undefined)
   if('deadline' in stamp)expect(readWaitDeadline(structuredClone(stamp.deadline),limits,stamp.parkedAt)).toEqual(stamp.deadline)
  }
  for(const item of limitsVector.deadlinesRefused)expect(()=>readWaitDeadline(structuredClone(item.deadline),'limits' in item?item.limits:undefined,item.parkedAt),item.problem).toThrow(resumeRefusal('invalid'))
 })
 it('pins: readers, digests, and the pins and binding a park reads from a retained checkpoint',()=>{
  pinsVector.items.forEach((item,i)=>{expect(readResumePins(structuredClone(item))).toEqual(item);expect(pinsDigest(item as ResumePins)).toBe(pinsVector.digests[i])})
  for(const [members,digest] of pinsVector.policies)expect(policyIdentity(members as never)).toBe(digest)
  for(const item of pinsVector.checkpoints){
   expect(pinsFrom(structuredClone(item.checkpoint) as never,item.model),item.case).toEqual(item.pins)
   if('approval' in item)expect(approvalBinding((item.checkpoint as unknown as {approval:never}).approval)).toEqual(item.approval)
  }
  for(const item of pinsVector.checkpointsRefused)expect(()=>pinsFrom(structuredClone(item.checkpoint) as never,item.model),item.problem).toThrow(resumeRefusal('invalid'))
  for(const item of [...pinsVector.refused,...pinsVector.readerRefused])expect(()=>readResumePins(structuredClone(item.pins)),item.problem).toThrow(resumeRefusal('invalid'))
 })
 it('evidence: readers and digests',()=>{
  evidenceVector.items.forEach((item,i)=>{expect(readResumeEvidence(structuredClone(item))).toEqual(item);expect(resumeEvidenceDigest(item as never)).toBe(evidenceVector.digests[i])})
  for(const item of [...evidenceVector.refused,...evidenceVector.readerRefused])expect(()=>readResumeEvidence(structuredClone(item.evidence)),item.problem).toThrow(resumeRefusal('invalid'))
 })
 it('revalidation: both languages derive each handwritten record, or give each handwritten refusal',()=>{
  for(const item of revalidationVector.items)expect(readRevalidation(structuredClone(item))).toEqual(item)
  for(const item of revalidationVector.cases){
   const run=()=>revalidateResume(structuredClone(item.wait) as never,structuredClone(item.evidence))
   if('revalidation' in item.outcome)expect(run(),item.case).toEqual(revalidationVector.items[item.outcome.revalidation as number])
   else expect(run,item.case).toThrow(resumeRefusal(item.outcome.refused as string))
  }
  for(const item of [...revalidationVector.refused,...revalidationVector.readerRefused])expect(()=>readRevalidation(structuredClone(item.revalidation)),item.problem).toThrow(resumeRefusal('invalid'))
 })
 it('refusals: every reason both languages give, and nothing else',()=>{
  expect(refusalVector.items.map(item=>readResumeRefusal(item).reason)).toEqual([...RESUME_REFUSAL_REASONS])
  expect(refusalVector.items.map(item=>new ResumeRefused(item.reason as never).record())).toEqual(refusalVector.items)
  for(const item of refusalVector.refused)expect(()=>readResumeRefusal(item)).toThrow(resumeRefusal('invalid'))
  expect(new ResumeRefused('model-retired').failure).toMatchObject({code:'stale-state',outcome:'not-started'})
 })
})

const ACT={name:'create_issue',description:'Create one issue.',inputSchema:{type:'object'},taxonomy:{operation:'act',effect:'act'} as const}
async function actTool(execute:HostTool['execute']):Promise<HostTool>{
 return {...ACT,effectClass:'act',source:{kind:'custom',id:'tracker',trust:'person',version:'1',toolListDigest:await toolListDigest([ACT] as never),identity:{kind:'connection',label:'tracker'}},approve:()=>'ask',execute} as HostTool
}
function provider(turns:Parameters<typeof answerWith>[0][]){
 const state={requests:0}
 return {state,call:async()=>{const turn=turns[state.requests];state.requests++;if(!turn)throw Error('no scripted turn');return answerWith(turn,'stream',state.requests-1)}}
}
async function drive(store:RunStore,id:string,tools:HostTool[],call:()=>Promise<Response>,extra:{resume?:boolean;approval?:{callId:string;approved:boolean};interactive?:boolean}={}):Promise<AssistantEvent[]>{
 const session:EngineSession={prompt:'File the issue.',testPrompt:'',tools:[],callTool:vi.fn(async()=>({content:[]})),hostTools:tools,signal:new AbortController().signal,allowConversation:true,critic:'off',...(extra.interactive?{interactive:true}:{}),
  context:{currentRequest:{message:'File the issue.',input:'issue'},runStore:{store,id,...(extra.resume?{resume:true}:{})},...(extra.approval?{approval:extra.approval}:{})},model:{family:'openai-compatible',model:'fixture',call}} as EngineSession
 const out:AssistantEvent[]=[];for await(const event of vercel.start(session))out.push(event);return out
}
/** The host's current evidence when nothing pinned changed: the wait's own pins, its model available. */
async function unchanged(store:DurableWaitStore,resource:string,wait:string){
 const held=(await store.readWait(resource,wait))!
 const {model,...rest}=held.pins
 return {version:1,resource,wait,...rest,model:{...model,status:'available'},policy:{digest:held.pins.policy}}
}
/** An approval paused through the real engine, then parked with an approval TTL. */
async function parkedApproval(ttl=HOUR,limits:{run?:string;waitMs?:number}={}){
 const {clock,store}=clocked(),host=new ReferenceEffectHost(store),execute=vi.fn(async()=>({content:[{type:'text' as const,text:'created'}]}))
 const a=await store.acquireLease({resource:'run-a',owner:'worker-a',expectedRevision:null,durationMs:HOUR})
 const events=await drive(fencedRunStore(store,fenceOf(a),host),'run-a',[await actTool(execute)],provider([{call:{tool:'tracker__create_issue',arguments:{title:'Mileage'}}}]).call)
 const asked=events.find(e=>e.type==='approval_requested') as Extract<AssistantEvent,{type:'approval_requested'}>
 const parked=await parkRun({store,fence:fenceOf(a),wait:'wait-1',continuation:'k-1',condition:{event:{kind:'approval-decision',subject:asked.callId}},model:MODEL,limits:{approvalTtlMs:ttl,...limits},wakeKeyDigest:wakeKeyDigest(KEY),release:[]})
 const decision:WakeEvidence={version:1,resource:'run-a',wait:'wait-1',evidence:'decision-1',kind:'event',event:{kind:'approval-decision',subject:asked.callId},key:KEY}
 return {clock,store,host,execute,asked,parked,decision,fence:fenceOf(a)}
}

describe('#265 M2: a committed park closes the run; the resume is the only continuation',()=>{
 it('the reviewer\'s case: no fresh lease continues a parked run without its wake, its decision and its claim',async()=>{
  const {clock,store,host,execute,asked,parked,decision,fence}=await parkedApproval()
  // P3: a worker leases the parked run against its parked revision. Refused: the wait holds the run.
  await expect(store.acquireLease({resource:'run-a',owner:'worker-x',expectedRevision:parked.wait.checkpoint.revision,durationMs:HOUR})).rejects.toEqual(waitRefusal('wait-pending'))
  // The parking driver's fence was released with the park, so it commits and admits nothing either.
  const head=(await store.load('run-a'))!
  await expect(store.commitFenced({runId:'run-a',expectedRevision:head.revision,commitId:'late',checkpoint:head.checkpoint,events:[]},fence)).rejects.toEqual(leaseRefusal('released'))
  await expect(host.dispatch(effectAdmission(fence,{run:'run-a',operation:'op-x',source:'tracker',tool:'create_issue',requestDigest:'sha256:'+'1'.repeat(64)}),async()=>'performed')).rejects.toEqual(leaseRefusal('released'))
  // P15: woken but unclaimed still holds the run, so the pause is never parked or claimed twice.
  clock.at+=MINUTE;await wakeRun(store,decision)
  await expect(store.acquireLease({resource:'run-a',owner:'worker-x',expectedRevision:parked.wait.checkpoint.revision,durationMs:HOUR})).rejects.toEqual(waitRefusal('wait-pending'))
  expect(await store.readLease('run-a')).toMatchObject({state:'released',epoch:'1'})
  // The resume path: the wake, the person's decision, the revalidation and the claim, which takes the one lease.
  const resumed=await resumeParked({store,resource:'run-a',wait:'wait-1',evidence:'decision-1',owner:'worker-b',durationMs:HOUR,approval:{callId:asked.callId,approved:true},current:await unchanged(store,'run-a','wait-1')})
  expect(resumed.lease).toMatchObject({owner:'worker-b',epoch:'2',state:'held'})
  await expect(store.acquireLease({resource:'run-a',owner:'worker-x',expectedRevision:parked.wait.checkpoint.revision,durationMs:HOUR})).rejects.toEqual(leaseRefusal('held'))
  const continued=await drive(fencedRunStore(store,resumed.fence,host),'run-a',[await actTool(execute)],provider([{say:'Filed.'}]).call,{resume:true,approval:resumed.approval})
  expect(continued.some(e=>e.type==='error')).toBe(false);expect(execute).toHaveBeenCalledOnce()
  // A resumed wait no longer holds the run: once the claim's lease is released the run is leased as before.
  await store.releaseLease(resumed.fence)
  await expect(store.acquireLease({resource:'run-a',owner:'worker-c',expectedRevision:(await store.load('run-a'))!.revision,durationMs:HOUR})).resolves.toMatchObject({epoch:'3'})
 })
 it('every wait not yet resumed holds the run in the wait transitions themselves, not only at the lease port',async()=>{
  // A host store kind implements the same transitions: here the table is driven directly, with a lease taken around the
  // store's own guard, as a store that forgot it would grant one.
  const clock={at:T0},leases=new LeaseTable(()=>new Date(clock.at)),table=new WaitTable(()=>new Date(clock.at))
  const checkpoint=new TextEncoder().encode(canonicalJSON(pauseVector.items[1]!.checkpoint)),head={revision:'rev-1',checkpoint}
  const paused=JSON.parse(new TextDecoder().decode(checkpoint))
  const park=(wait:string,fence:ReturnType<typeof fenceOf>)=>table.park({version:1,resource:'run-t',wait,reason:'approval',pending:{kind:'approval',callId:'call_2_1',name:'tracker__create_issue',tool:'create_issue'},condition:{event:{kind:'approval-decision',subject:'call_2_1'}},limits:{approvalTtlMs:HOUR},approval:approvalBinding(paused.approval),continuation:'k',checkpoint:{revision:'rev-1',digest:checkpointBytesDigest(checkpoint)},pins:pinsFrom(paused,MODEL),wakeKeyDigest:wakeKeyDigest(KEY),release:[]},fence,head,leases)
  park('wait-1',fenceOf(leases.acquire({resource:'run-t',owner:'worker-a',expectedRevision:'rev-1',durationMs:HOUR},'rev-1')))
  table.wake({version:1,resource:'run-t',wait:'wait-1',evidence:'decision-1',kind:'event',event:{kind:'approval-decision',subject:'call_2_1'},key:KEY})
  // Woken but unclaimed: a second park of the same pause is refused.
  expect(()=>park('wait-2',fenceOf(leases.acquire({resource:'run-t',owner:'worker-x',expectedRevision:'rev-1',durationMs:HOUR},'rev-1')))).toThrow(waitRefusal('wait-pending'))
  expect(()=>table.admitAcquire('run-t')).toThrow(waitRefusal('wait-pending'))
 })
 it('a wake the store does not hold is unacknowledged before any decision is asked for',async()=>{
  const {clock,store,decision}=await parkedApproval()
  const input={store,resource:'run-a',wait:'wait-1',evidence:'decision-unseen',owner:'worker-b',durationMs:HOUR,current:await unchanged(store,'run-a','wait-1')}
  await expect(resumeParked(input)).rejects.toEqual(waitRefusal('unacknowledged'))
  clock.at+=MINUTE;await wakeRun(store,decision)
  await expect(resumeParked(input)).rejects.toEqual(waitRefusal('unacknowledged'))
 })
 it('a run that ended at a terminal wait is closed for good',async()=>{
  const {clock,store,parked,asked}=await parkedApproval(HOUR)
  clock.at=T0+HOUR
  await wakeRun(store,{version:1,resource:'run-a',wait:'wait-1',evidence:'deadline-1',kind:'deadline',key:KEY})
  await expect(store.acquireLease({resource:'run-a',owner:'worker-x',expectedRevision:parked.wait.checkpoint.revision,durationMs:HOUR})).rejects.toEqual(waitRefusal('closed'))
  await expect(resumeParked({store,resource:'run-a',wait:'wait-1',evidence:'deadline-1',owner:'worker-b',durationMs:HOUR,approval:{callId:asked.callId,approved:true},current:await unchanged(store,'run-a','wait-1')})).rejects.toEqual(waitRefusal('unacknowledged'))
 })
})

describe('#225: deadlines and approval expiry by the store clock; equal time is past',()=>{
 it('an unattended approval needs a bounded positive TTL; without one nothing is parked',async()=>{
  const {store}=clocked()
  const a=await store.acquireLease({resource:'run-a',owner:'worker-a',expectedRevision:null,durationMs:HOUR})
  await store.commitFenced({runId:'run-a',expectedRevision:null,commitId:'pause',checkpoint:new TextEncoder().encode(canonicalJSON(pauseVector.items[1]!.checkpoint)),events:[]},fenceOf(a))
  const input={store,fence:fenceOf(a),wait:'wait-1',continuation:'k',condition:{event:{kind:'approval-decision',subject:'call_2_1'}},model:MODEL,wakeKeyDigest:wakeKeyDigest(KEY),release:[]}
  await expect(parkRun(input)).rejects.toEqual(waitRefusal('approval-ttl-required'))
  await expect(parkRun({...input,limits:{waitMs:HOUR}})).rejects.toEqual(waitRefusal('approval-ttl-required'))
  for(const approvalTtlMs of [0,-1,1.5,2_592_000_001])await expect(parkRun({...input,limits:{approvalTtlMs}})).rejects.toEqual(waitRefusal('invalid'))
  expect(await store.readWait('run-a','wait-1')).toBeUndefined();expect(await store.readLease('run-a')).toMatchObject({state:'held'})
  const parked=await parkRun({...input,limits:{approvalTtlMs:1}})
  expect(parked.wait.deadline).toEqual({at:iso(T0+1),by:'approval',approval:iso(T0+1)})
  expect(parked.wait.approval).toEqual(approvalBinding((pauseVector.items[1]!.checkpoint as unknown as {approval:never}).approval))
 })
 it('a decision a millisecond before the expiry continues; at the expiry nothing does, and the claim\'s lease ends there',async()=>{
  const {clock,store,host,execute,asked,decision}=await parkedApproval(HOUR)
  clock.at=T0+HOUR-2;await wakeRun(store,decision)
  clock.at=T0+HOUR-1
  const resumed=await resumeParked({store,resource:'run-a',wait:'wait-1',evidence:'decision-1',owner:'worker-b',durationMs:HOUR,approval:{callId:asked.callId,approved:true},current:await unchanged(store,'run-a','wait-1')})
  // The approval's expiry bounds the claim's lease; renewing the lease never renews the approval.
  expect(resumed.lease.expiry).toBe(iso(T0+HOUR))
  expect((await store.renewLease(resumed.fence,HOUR)).expiry).toBe(iso(T0+HOUR))
  clock.at=T0+HOUR
  await expect(store.renewLease(resumed.fence,HOUR)).rejects.toEqual(leaseRefusal('expired'))
  // At the expiry the approved call is admitted nowhere: the continuation's first commit is refused.
  const continued=await drive(fencedRunStore(store,resumed.fence,host),'run-a',[await actTool(execute)],provider([{say:'Filed.'}]).call,{resume:true,approval:resumed.approval})
  expect(continued.some(e=>e.type==='error')).toBe(true);expect(execute).not.toHaveBeenCalled()
 })
 it('at the expiry the claim is refused, a late decision changes nothing, and only the deadline wake ends the wait',async()=>{
  const first=await parkedApproval(HOUR)
  first.clock.at=T0+HOUR-1;await wakeRun(first.store,first.decision)
  first.clock.at=T0+HOUR
  await expect(resumeParked({store:first.store,resource:'run-a',wait:'wait-1',evidence:'decision-1',owner:'worker-b',durationMs:HOUR,approval:{callId:first.asked.callId,approved:true},current:await unchanged(first.store,'run-a','wait-1')})).rejects.toEqual(waitRefusal('deadline-passed'))
  expect(await first.store.readLease('run-a')).toMatchObject({state:'released',epoch:'1'})
  const {clock,store,decision}=await parkedApproval(HOUR)
  const deadline={version:1 as const,resource:'run-a',wait:'wait-1',evidence:'deadline-1',kind:'deadline' as const,key:KEY}
  clock.at=T0+HOUR-1
  await expect(wakeRun(store,deadline)).rejects.toEqual(waitRefusal('not-due'))
  clock.at=T0+HOUR
  const held=await store.readWait('run-a','wait-1')
  await expect(wakeRun(store,decision)).rejects.toEqual(waitRefusal('deadline-passed'))
  expect(await store.readWait('run-a','wait-1')).toEqual(held)
  const expired=await wakeRun(store,deadline)
  expect(expired).toMatchObject({evidence:'deadline-1',kind:'deadline',at:iso(T0+HOUR)})
  expect(await wakeRun(store,deadline)).toEqual(expired)
  expect((await store.readWait('run-a','wait-1'))!.state).toBe('expired')
  // A timeout is never an approval, and an expired wait never resumes.
  const report=await waitReport(store,'run-a')
  expect(report.entries.map(e=>e.decision)).toEqual(['park','deadline'])
  expect(report.entries[0]).toMatchObject({deadline:iso(T0+HOUR)})
 })
 it('a run deadline bounds every lease of the run, and a later limit never extends it',async()=>{
  const {clock,store}=clocked()
  const a=await store.acquireLease({resource:'run-r',owner:'worker-a',expectedRevision:null,durationMs:HOUR})
  expect(await limitRun(store,{version:1,resource:'run-r',run:iso(T0+30*MINUTE)})).toEqual({version:1,resource:'run-r',run:iso(T0+30*MINUTE)})
  // The held lease is shortened to the deadline, and no renewal passes it.
  expect(await store.readLease('run-r')).toMatchObject({expiry:iso(T0+30*MINUTE)})
  expect((await store.renewLease(fenceOf(a),HOUR)).expiry).toBe(iso(T0+30*MINUTE))
  expect(await limitRun(store,{version:1,resource:'run-r',run:iso(T0+2*HOUR)})).toEqual({version:1,resource:'run-r',run:iso(T0+30*MINUTE)})
  clock.at=T0+30*MINUTE-1
  await expect(store.commitFenced({runId:'run-r',expectedRevision:null,commitId:'c-1',checkpoint:new TextEncoder().encode('{}'),events:[]},fenceOf(a))).resolves.toBeDefined()
  clock.at=T0+30*MINUTE
  await expect(store.commitFenced({runId:'run-r',expectedRevision:(await store.load('run-r'))!.revision,commitId:'c-2',checkpoint:new TextEncoder().encode('{}'),events:[]},fenceOf(a))).rejects.toEqual(leaseRefusal('expired'))
  await expect(store.acquireLease({resource:'run-r',owner:'worker-b',expectedRevision:(await store.load('run-r'))!.revision,durationMs:HOUR})).rejects.toEqual(waitRefusal('deadline-passed'))
 })
 it('a park inherits the run deadline the store holds; a park\'s later run limit never extends it',async()=>{
  const {store}=clocked()
  await limitRun(store,{version:1,resource:'run-a',run:iso(T0+2*HOUR)})
  const a=await store.acquireLease({resource:'run-a',owner:'worker-a',expectedRevision:null,durationMs:HOUR})
  await store.commitFenced({runId:'run-a',expectedRevision:null,commitId:'pause',checkpoint:new TextEncoder().encode(canonicalJSON(pauseVector.items[1]!.checkpoint)),events:[]},fenceOf(a))
  const parked=await parkRun({store,fence:fenceOf(a),wait:'wait-1',continuation:'k',condition:{event:{kind:'approval-decision',subject:'call_2_1'}},model:MODEL,limits:{run:iso(T0+5*HOUR),approvalTtlMs:4*HOUR},wakeKeyDigest:wakeKeyDigest(KEY),release:[]})
  expect(parked.wait.deadline).toEqual({at:iso(T0+2*HOUR),by:'run',run:iso(T0+2*HOUR),approval:iso(T0+4*HOUR)})
  expect(parked.attention.deadline).toBe(iso(T0+2*HOUR))
 })
 it('a run limit already past refuses the park; nothing is committed',async()=>{
  const {clock,store}=clocked()
  const a=await store.acquireLease({resource:'run-a',owner:'worker-a',expectedRevision:null,durationMs:HOUR})
  await store.commitFenced({runId:'run-a',expectedRevision:null,commitId:'pause',checkpoint:new TextEncoder().encode(canonicalJSON(pauseVector.items[1]!.checkpoint)),events:[]},fenceOf(a))
  clock.at=T0+MINUTE
  await expect(parkRun({store,fence:fenceOf(a),wait:'wait-1',continuation:'k',condition:{event:{kind:'approval-decision',subject:'call_2_1'}},model:MODEL,limits:{run:iso(T0+MINUTE),approvalTtlMs:HOUR},wakeKeyDigest:wakeKeyDigest(KEY),release:[]})).rejects.toEqual(waitRefusal('deadline-passed'))
  expect(await store.readWait('run-a','wait-1')).toBeUndefined();expect(await store.readLease('run-a')).toMatchObject({state:'held'})
 })
 it('a decision delivered at its wait\'s timeout is refused; only the timeout ends the wait',async()=>{
  const {store,clock}=clocked()
  const a=await store.acquireLease({resource:'run-a',owner:'worker-a',expectedRevision:null,durationMs:HOUR})
  await store.commitFenced({runId:'run-a',expectedRevision:null,commitId:'pause',checkpoint:new TextEncoder().encode(canonicalJSON(pauseVector.items[1]!.checkpoint)),events:[]},fenceOf(a))
  await parkRun({store,fence:fenceOf(a),wait:'wait-1',continuation:'k',condition:{event:{kind:'approval-decision',subject:'call_2_1'}},timeoutAt:iso(T0+HOUR),model:MODEL,limits:{approvalTtlMs:2*HOUR},wakeKeyDigest:wakeKeyDigest(KEY),release:[]})
  clock.at=T0+HOUR
  await expect(wakeRun(store,{version:1,resource:'run-a',wait:'wait-1',evidence:'decision-late',kind:'event',event:{kind:'approval-decision',subject:'call_2_1'},key:KEY})).rejects.toEqual(waitRefusal('timed-out'))
  expect((await wakeRun(store,{version:1,resource:'run-a',wait:'wait-1',evidence:'timeout-1',kind:'timeout',key:KEY})).kind).toBe('timeout')
 })
 it('a run limit set by a park bounds the claim\'s lease; a deadline wake ends nothing after the claim',async()=>{
  const {clock,store,asked,decision}=await parkedApproval(4*HOUR,{run:iso(T0+30*MINUTE)})
  clock.at+=MINUTE;await wakeRun(store,decision)
  const resumed=await resumeParked({store,resource:'run-a',wait:'wait-1',evidence:'decision-1',owner:'worker-b',durationMs:HOUR,approval:{callId:asked.callId,approved:true},current:await unchanged(store,'run-a','wait-1')})
  expect(resumed.lease.expiry).toBe(iso(T0+30*MINUTE))
  clock.at=T0+30*MINUTE
  await expect(wakeRun(store,{version:1,resource:'run-a',wait:'wait-1',evidence:'deadline-1',kind:'deadline',key:KEY})).rejects.toEqual(waitRefusal('stale'))
  expect((await store.readWait('run-a','wait-1'))!.state).toBe('resumed')
 })
 it('a question its deadline ends before any answer is unanswered, and the report says so',async()=>{
  const {clock,store}=clocked(),host=new ReferenceEffectHost(store)
  const a=await store.acquireLease({resource:'run-q',owner:'worker-a',expectedRevision:null,durationMs:HOUR})
  const interview={title:'Region',question:'Which region first?',selection:'single',options:[{id:'north',label:'North',description:''},{id:'south',label:'South',description:''}],recommendedOptionId:'north',recommendation:'Most sites.'}
  const events=await drive(fencedRunStore(store,fenceOf(a),host),'run-q',[questionTool],provider([{call:{tool:'ask_user',arguments:interview}}]).call,{interactive:true})
  const question=events.find(e=>e.type==='user_question') as Extract<AssistantEvent,{type:'user_question'}>
  await parkRun({store,fence:fenceOf(a),wait:'wait-q',continuation:'k',condition:{event:{kind:'question-answer',subject:question.id}},model:MODEL,limits:{waitMs:HOUR,approvalTtlMs:MINUTE},wakeKeyDigest:wakeKeyDigest(KEY),release:[],question:question.id})
  const parked=(await store.readWait('run-q','wait-q'))!
  // An approval TTL is no question's: only the wait's limit applies.
  expect(parked.limits).toEqual({waitMs:HOUR});expect(parked.deadline).toEqual({at:iso(T0+HOUR),by:'wait',wait:iso(T0+HOUR)})
  clock.at=T0+HOUR
  await wakeRun(store,{version:1,resource:'run-q',wait:'wait-q',evidence:'deadline-q',kind:'deadline',key:KEY})
  const ended=(await store.readWait('run-q','wait-q'))!
  expect(ended.state).toBe('expired');expect(ended.resolution).toEqual({kind:'unanswered',at:iso(T0+HOUR)})
  expect((await waitReport(store,'run-q')).entries.map(e=>e.decision)).toEqual(['park','deadline','unanswered'])
 })
 it('an effect whose outcome is unknown is never performed again, whatever lease follows the claim',async()=>{
  const {clock,store,host,asked,decision}=await parkedApproval(HOUR)
  clock.at+=MINUTE;await wakeRun(store,decision)
  const resumed=await resumeParked({store,resource:'run-a',wait:'wait-1',evidence:'decision-1',owner:'worker-b',durationMs:HOUR,approval:{callId:asked.callId,approved:true},current:await unchanged(store,'run-a','wait-1')})
  const operation={run:'run-a',operation:'op-1',source:'tracker',tool:'create_issue',requestDigest:'sha256:'+'2'.repeat(64)}
  let performed=0
  await expect(host.dispatch(effectAdmission(resumed.fence,operation),async()=>{performed++;throw new Error('the connection dropped')})).rejects.toThrow('the connection dropped')
  // The claimer gives up its lease; another worker leases the run before the approval expires.
  await store.releaseLease(resumed.fence)
  const later=await store.acquireLease({resource:'run-a',owner:'worker-c',expectedRevision:(await store.load('run-a'))!.revision,durationMs:HOUR})
  await expect(host.dispatch(effectAdmission(fenceOf(later),operation),async()=>{performed++;return 'again'})).rejects.toEqual(leaseRefusal('already-admitted'))
  expect(performed).toBe(1)
 })
})

describe('#227: every unattended resume revalidates the pinned execution authority before anything is claimed',()=>{
 it('missing evidence, a changed model, a retired one: refused, nothing claimed, the run still closed',async()=>{
  const {clock,store,asked,decision}=await parkedApproval()
  clock.at+=MINUTE;await wakeRun(store,decision)
  const input={store,resource:'run-a',wait:'wait-1',evidence:'decision-1',owner:'worker-b',durationMs:HOUR,approval:{callId:asked.callId,approved:true}}
  const current=await unchanged(store,'run-a','wait-1')
  await expect(resumeParked(input)).rejects.toEqual(resumeRefusal('evidence-missing'))
  await expect(resumeParked({...input,current:{...current,sources:[]}})).rejects.toEqual(resumeRefusal('evidence-missing'))
  await expect(resumeParked({...input,current:{...current,model:{provider:'openai',model:'other',status:'available'}}})).rejects.toEqual(resumeRefusal('model-changed'))
  await expect(resumeParked({...input,current:{...current,model:{...current.model,status:'retired'}}})).rejects.toEqual(resumeRefusal('model-retired'))
  // A policy layer from the trusted host is accepted for the run, but the approval was reviewed under the old one.
  await expect(resumeParked({...input,current:{...current,policy:{digest:'sha256:'+'b'.repeat(64),layer:'sha256:'+'c'.repeat(64)}}})).rejects.toEqual(resumeRefusal('approval-changed'))
  expect((await store.readWait('run-a','wait-1'))!.state).toBe('woken');expect(await store.readLease('run-a')).toMatchObject({state:'released',epoch:'1'})
  await expect(store.acquireLease({resource:'run-a',owner:'worker-x',expectedRevision:(await store.load('run-a'))!.revision,durationMs:HOUR})).rejects.toEqual(waitRefusal('wait-pending'))
 })
 it('an explicitly admitted model is recorded with both identities, and the claim keeps the revalidation',async()=>{
  const {clock,store,asked,decision}=await parkedApproval()
  clock.at+=MINUTE;await wakeRun(store,decision)
  const current=await unchanged(store,'run-a','wait-1')
  const admitted={...current,model:{provider:'openai',model:'fixture-2',status:'available'},admissions:[{kind:'model',subject:'model',from:'openai/fixture',to:'openai/fixture-2',actor:'person-owner',at:iso(T0)}]}
  const resumed=await resumeParked({store,resource:'run-a',wait:'wait-1',evidence:'decision-1',owner:'worker-b',durationMs:HOUR,approval:{callId:asked.callId,approved:true},current:admitted})
  expect(resumed.revalidation.changes).toEqual([{kind:'model',subject:'model',from:'openai/fixture',to:'openai/fixture-2',actor:'person-owner',at:iso(T0)}])
  expect((await store.readWait('run-a','wait-1'))!.revalidation).toEqual(resumed.revalidation)
  expect(resumed.revalidation.pins).toBe(pinsDigest(resumed.wait.pins))
 })
})

describe('#266 fix round: the approval binds the run until its call settles',()=>{
 it('H1: no takeover after the claim outlives the approval: a lease taken before the expiry ends at it, none is taken at or after it, and the approved call never runs late',async()=>{
  const {clock,store,host,execute,asked,decision}=await parkedApproval(HOUR)
  clock.at=T0+MINUTE;await wakeRun(store,decision)
  const resumed=await resumeParked({store,resource:'run-a',wait:'wait-1',evidence:'decision-1',owner:'worker-b',durationMs:HOUR,approval:{callId:asked.callId,approved:true},current:await unchanged(store,'run-a','wait-1')})
  // The claimer records the decision (approved, not yet run: unsettled) and stops; its lease ends at the expiry. Another
  // worker takes over before it: its lease ends there too.
  const head=(await store.load('run-a'))!,approved=JSON.parse(new TextDecoder().decode(head.checkpoint!))
  approved.approval.state='approved'
  const receipt=await store.commitFenced({runId:'run-a',expectedRevision:head.revision,commitId:'approved',checkpoint:new TextEncoder().encode(canonicalJSON(approved)),events:[]},resumed.fence)
  await store.releaseLease(resumed.fence)
  clock.at=T0+30*MINUTE
  const takeover=await store.acquireLease({resource:'run-a',owner:'worker-c',expectedRevision:receipt.revision,durationMs:24*HOUR})
  expect(takeover.expiry).toBe(iso(T0+HOUR))
  expect((await store.renewLease(fenceOf(takeover),24*HOUR)).expiry).toBe(iso(T0+HOUR))
  // At the expiry nothing leases the run, and the takeover's lease commits and admits nothing: the call never runs.
  clock.at=T0+2*HOUR
  await expect(store.acquireLease({resource:'run-a',owner:'worker-d',expectedRevision:receipt.revision,durationMs:HOUR})).rejects.toEqual(waitRefusal('deadline-passed'))
  const continued=await drive(fencedRunStore(store,fenceOf(takeover),host),'run-a',[await actTool(execute)],provider([{say:'Filed.'}]).call,{resume:true,approval:resumed.approval})
  expect(continued.some(e=>e.type==='error')).toBe(true);expect(execute).not.toHaveBeenCalled()
 })
 it('H1: the claimer\'s own release and re-acquire are bounded the same way, renewal included',async()=>{
  const {clock,store,host,execute,asked,parked,decision}=await parkedApproval(HOUR)
  clock.at=T0+MINUTE;await wakeRun(store,decision)
  const resumed=await resumeParked({store,resource:'run-a',wait:'wait-1',evidence:'decision-1',owner:'worker-b',durationMs:HOUR,approval:{callId:asked.callId,approved:true},current:await unchanged(store,'run-a','wait-1')})
  await store.releaseLease(resumed.fence)
  const again=await store.acquireLease({resource:'run-a',owner:'worker-b',expectedRevision:parked.wait.checkpoint.revision,durationMs:24*HOUR})
  expect(again.expiry).toBe(iso(T0+HOUR))
  clock.at=T0+HOUR-1
  expect((await store.renewLease(fenceOf(again),24*HOUR)).expiry).toBe(iso(T0+HOUR))
  clock.at=T0+6*HOUR
  const continued=await drive(fencedRunStore(store,fenceOf(again),host),'run-a',[await actTool(execute)],provider([{say:'Filed.'}]).call,{resume:true,approval:resumed.approval})
  expect(continued.some(e=>e.type==='error')).toBe(true);expect(execute).not.toHaveBeenCalled()
 })
 it('H1: once the approved call settles, its expiry stops bounding the run',async()=>{
  const {clock,store,host,execute,asked,decision}=await parkedApproval(HOUR)
  clock.at=T0+MINUTE;await wakeRun(store,decision)
  const resumed=await resumeParked({store,resource:'run-a',wait:'wait-1',evidence:'decision-1',owner:'worker-b',durationMs:HOUR,approval:{callId:asked.callId,approved:true},current:await unchanged(store,'run-a','wait-1')})
  const continued=await drive(fencedRunStore(store,resumed.fence,host),'run-a',[await actTool(execute)],provider([{say:'Filed.'}]).call,{resume:true,approval:resumed.approval})
  expect(continued.some(e=>e.type==='error')).toBe(false);expect(execute).toHaveBeenCalledOnce()
  await store.releaseLease(resumed.fence)
  clock.at=T0+2*HOUR
  await expect(store.acquireLease({resource:'run-a',owner:'worker-c',expectedRevision:(await store.load('run-a'))!.revision,durationMs:HOUR})).resolves.toMatchObject({expiry:iso(T0+3*HOUR)})
 })
 it('M3: under the claim, a checkpoint that rewrites the reviewed call is refused, and the call runs as the person reviewed it',async()=>{
  const {clock,store,host,execute,asked,decision}=await parkedApproval(HOUR)
  clock.at=T0+MINUTE;await wakeRun(store,decision)
  const resumed=await resumeParked({store,resource:'run-a',wait:'wait-1',evidence:'decision-1',owner:'worker-b',durationMs:HOUR,approval:{callId:asked.callId,approved:true},current:await unchanged(store,'run-a','wait-1')})
  const head=(await store.load('run-a'))!,checkpoint=JSON.parse(new TextDecoder().decode(head.checkpoint!))
  const rewrite=(change:(c:{approval:Record<string,unknown>})=>void)=>{const copy=structuredClone(checkpoint);change(copy);return store.commitFenced({runId:'run-a',expectedRevision:head.revision,commitId:'rewrite',checkpoint:new TextEncoder().encode(canonicalJSON(copy)),events:[]},resumed.fence)}
  await expect(rewrite(c=>{c.approval.args={title:'Something else entirely'}})).rejects.toEqual(resumeRefusal('approval-changed'))
  await expect(rewrite(c=>{c.approval.callId='call-other'})).rejects.toEqual(resumeRefusal('approval-changed'))
  await expect(rewrite(c=>{(c.approval.source as {id:string}).id='ledger'})).rejects.toEqual(resumeRefusal('approval-changed'))
  await expect(rewrite(c=>{delete (c as {approval?:unknown}).approval})).rejects.toEqual(resumeRefusal('approval-changed'))
  expect((await store.load('run-a'))!.revision).toBe(head.revision)
  const calls:unknown[]=[]
  execute.mockImplementation((async(args:unknown)=>{calls.push(args);return {content:[{type:'text' as const,text:'created'}]}}) as never)
  await drive(fencedRunStore(store,resumed.fence,host),'run-a',[await actTool(execute)],provider([{say:'Filed.'}]).call,{resume:true,approval:resumed.approval})
  expect(calls).toEqual([{title:'Mileage'}])
 })
 it('M2: a run deadline at the Unix epoch bounds the claim\'s lease like any other',async()=>{
  const {clock,store,asked,parked,decision}=await parkedApproval(4*HOUR)
  clock.at=T0+MINUTE;await wakeRun(store,decision)
  await limitRun(store,{version:1,resource:'run-a',run:'1970-01-01T00:00:00.000Z'})
  const resumed=await resumeParked({store,resource:'run-a',wait:'wait-1',evidence:'decision-1',owner:'worker-b',durationMs:HOUR,approval:{callId:asked.callId,approved:true},current:await unchanged(store,'run-a','wait-1')})
  expect(resumed.lease.expiry).toBe('1970-01-01T00:00:00.000Z')
  await expect(store.commitFenced({runId:'run-a',expectedRevision:parked.wait.checkpoint.revision,commitId:'late',checkpoint:(await store.load('run-a'))!.checkpoint,events:[]},resumed.fence)).rejects.toEqual(leaseRefusal('expired'))
  await expect(store.renewLease(resumed.fence,HOUR)).rejects.toEqual(leaseRefusal('expired'))
 })
 it('M7: the claim checks the head\'s digest as well as its revision',()=>{
  const clock={at:T0},leases=new LeaseTable(()=>new Date(clock.at)),table=new WaitTable(()=>new Date(clock.at))
  const checkpoint=new TextEncoder().encode(canonicalJSON(pauseVector.items[1]!.checkpoint)),paused=JSON.parse(new TextDecoder().decode(checkpoint))
  const wait=table.park({version:1,resource:'run-t',wait:'w',reason:'approval',pending:{kind:'approval',callId:'call_2_1',name:'tracker__create_issue',tool:'create_issue'},condition:{event:{kind:'approval-decision',subject:'call_2_1'}},limits:{approvalTtlMs:HOUR},approval:approvalBinding(paused.approval),continuation:'k',checkpoint:{revision:'rev-1',digest:checkpointBytesDigest(checkpoint)},pins:pinsFrom(paused,MODEL),wakeKeyDigest:wakeKeyDigest(KEY),release:[]},fenceOf(leases.acquire({resource:'run-t',owner:'worker-a',expectedRevision:'rev-1',durationMs:HOUR},'rev-1')),{revision:'rev-1',checkpoint},leases).wait
  table.wake({version:1,resource:'run-t',wait:'w',evidence:'decision-1',kind:'event',event:{kind:'approval-decision',subject:'call_2_1'},key:KEY})
  const revalidation={version:1 as const,resource:'run-t',wait:'w',pins:pinsDigest(wait.pins),evidence:'sha256:'+'e'.repeat(64),changes:[]}
  const claim={resource:'run-t',wait:'w',evidence:'decision-1',lease:{resource:'run-t',owner:'worker-b',expectedRevision:'rev-1',durationMs:HOUR},reconciled:[],revalidation}
  // A store that can repeat a revision: the same revision over other bytes is another head.
  const other=new TextEncoder().encode(canonicalJSON({...paused,explanation:'moved'}))
  expect(()=>table.resume(claim,{revision:'rev-1',checkpoint:other},leases)).toThrow(waitRefusal('checkpoint-changed'))
  expect(table.resume(claim,{revision:'rev-1',checkpoint},leases).wait.state).toBe('resumed')
 })
})
