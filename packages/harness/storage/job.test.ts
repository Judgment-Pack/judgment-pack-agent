import {describe,it,expect,vi} from 'vitest'
import type {AssistantEvent,EngineSession,HostTool} from '../engine'
import {vercel} from '../engines/vercel'
import {answerWith} from '../testing/scenarioDriver'
import {toolListDigest} from '../toolSources'
import {canonicalJSON} from '../context/chat'
import {evidenceFailure,availablePlanEvidence} from '../execution/planActions'
import {approvalShownDigest,readExecution} from '../execution/state'
import {MemoryRunStore,type RunStore} from './run'
import {AIFailureError,failure} from '../failures'
import {sha256} from '../records'
import {ChatContext} from '../context/chat'
import {StoredChatScope} from '../context/storedChat'
import {declareSubagents,type SubagentDeclaration} from '../subagents'
import {CONTEXT_DEFAULTS} from '../context/policy'
import declaration from '../../../contract/vectors/subagents/declaration.json'
import modelVector from '../../../contract/vectors/job/model.json'
import chains from '../../../contract/vectors/principal/chains.json'
import type {AdmissionAnswer,PrincipalBinding,RunPrincipal} from '../principal'
import {RECORD_DEFAULTS} from '../records'
import type {ExecutionCheckpoint} from '../execution/state'
import {RUN_COMPLETE,RunWriter,type DurableBinding} from './writer'
import {LeaseCapabilityRefused,LeaseRefused,ReferenceEffectHost,certifyEffectHost,fenceOf,fencedRunStore,reauthorizationOf,unattendedRunStore,type EffectHost,type EffectOperation,type LeaseLifetime} from './lease'
import {JOB_CANCEL_UNRECORDED,JOB_START_UNRECORDED,JobRefused,readJobStartAnswer,JOB_ABANDON_OWNER,JOB_CANCEL_SCHEMA,abandonJobs,acknowledgeJobCancel,jobCancelCard,jobCancelOperation,jobCancelResult,jobCancelTool,jobReportFromExport,type JobCancelInput,admitJobs,completeJob,exportJobs,gatewayActionMembers,jobIdentity,jobKeyDigest,jobProjection,jobReadTool,jobReport,jobStartResult,jobStartTool,listJobs,readJob,reconcileJob,type JobAnswer,type JobCompletionEvidence,type JobLifetime,type JobReconciliation,type JobRegistration,type JobStartAnswer,type RunJob,type JobDelivery} from './job'
import {exportWaits,parkRun,reauthorizedRunStore,resumeParked,wakeKeyDigest,wakeRun,type WaitLifetime} from './wait'

/* #229: run-owned jobs through the real Vercel engine, the package's writer and the reference effect host. The Go twins are
 * go/context/run_job_test.go and go/adapter/jobtests. */
const T0=Date.parse('2026-01-01T00:00:00.000Z'),MINUTE=60_000,HOUR=60*MINUTE
const iso=(ms:number)=>new Date(ms).toISOString()
const jobRefusal=(reason:string)=>expect.objectContaining({name:'JobRefused',reason})
const waitRefusal=(reason:string)=>expect.objectContaining({name:'WaitRefused',reason})
/** Stands for a host store kind whose leases, waits and jobs outlive its process. */
class DurableJobStore extends MemoryRunStore {override readonly leaseLifetime:LeaseLifetime='durable';override readonly waitLifetime:WaitLifetime='durable';override readonly jobLifetime:JobLifetime='durable'}
const MODEL={provider:'openai',model:'fixture'},RUN='run-j'
const SECRET='SENTINEL-EXTERNAL-HANDLE-4f1c9e7a2b',RECEIPT='SENTINEL-RECEIPT-8d3b6a0e5c'
const KEY='job.key.0001.Q7z2Xm9LpV4cR8tNb3Yh6Df1Gj5K',WAKE='wk.job.0001.Hs8Pq3Lz6Wd1Rm9Tc4Vb7Nx2Ka5U'
const START={name:'start_job',description:'Start one background job.',inputSchema:{type:'object'},taxonomy:{operation:'run',effect:'act'} as const}
async function startTool(start:(input:{job:string;request:string})=>Promise<JobStartAnswer>,over:{mint?:(job:string)=>Promise<string>;approve?:'allow'|'ask'}={}):Promise<HostTool>{
 return jobStartTool({...START,source:{kind:'custom',id:'runner',trust:'person',version:'1',toolListDigest:await toolListDigest([START] as never),identity:{kind:'connection',label:'runner'}} as never,approve:(()=>over.approve??'allow') as never,mint:over.mint??(async()=>jobKeyDigest(KEY)),start:start as never})
}
function provider(turns:Parameters<typeof answerWith>[0][]){
 const state={requests:0,bodies:[] as string[]}
 return {state,call:async(_path?:unknown,init?:{body?:unknown})=>{state.bodies.push(String(init?.body??''));const turn=turns[state.requests];state.requests++;if(!turn)throw Error('no scripted turn');return answerWith(turn,'stream',state.requests-1)}}
}
/** #330: a finished run is resumed by no plain resume, and a job continuation only where a claimed wait bears it out. A later
 * turn here resumes the run as one stopped before its end would be: its head is left running, under the run's own fence. A
 * continuation handed explicitly is the test's own case. */
async function laterTurn(store:RunStore,extra:{resume?:boolean;jobContinuation?:RunJob}):Promise<void>{
 if(!extra.resume||extra.jobContinuation)return
 await interrupted(store)
}
/** A finished head left running, as a run stopped before its end leaves it. */
async function interrupted(store:RunStore):Promise<void>{
 const head=await store.load(RUN),saved=head?.checkpoint?JSON.parse(new TextDecoder().decode(head.checkpoint)):undefined
 if(saved?.status!=='complete')return
 await store.commit({runId:RUN,expectedRevision:head!.revision,commitId:crypto.randomUUID(),checkpoint:new TextEncoder().encode(JSON.stringify({...saved,status:'running'})),events:[]})
}
async function drive(store:RunStore,tools:HostTool[],call:(...args:never[])=>Promise<Response>,extra:{resume?:boolean;approval?:{callId:string;approved:boolean;shownDigest?:string};jobContinuation?:RunJob;signal?:AbortSignal}={}):Promise<AssistantEvent[]>{
 await laterTurn(store,extra)
 const session:EngineSession={prompt:'Run the job.',testPrompt:'',tools:[],callTool:vi.fn(async()=>({content:[]})),hostTools:tools,signal:extra.signal??new AbortController().signal,allowConversation:true,critic:'off',
  context:{currentRequest:{message:'Run the job.',input:'job'},runStore:{store,id:RUN,...(extra.resume?{resume:true}:{})},...(extra.approval?{approval:extra.approval}:{}),...(extra.jobContinuation?{jobContinuation:extra.jobContinuation}:{})},model:{family:'openai-compatible',model:'fixture',call}} as unknown as EngineSession
 const out:AssistantEvent[]=[];for await(const event of vercel.start(session))out.push(event);return out
}
const CALL={call:{tool:'runner__start_job',arguments:{name:'nightly'}}}
const READ=(job:string)=>({call:{tool:'job_read',arguments:{job}}})
async function leased(make:(now:()=>Date)=>DurableJobStore=now=>new DurableJobStore({now}),resource=RUN){
 const clock={at:T0},store=make(()=>new Date(clock.at)),host=new ReferenceEffectHost(store)
 const lease=await store.acquireLease({resource,owner:'worker-a',expectedRevision:null,durationMs:HOUR}),fence=fenceOf(lease)
 return {clock,store,host,fence,view:unattendedRunStore({store,fence,effectHost:host})}
}
const started=async():Promise<JobStartAnswer>=>({started:true,handle:SECRET,receipt:RECEIPT})
const headRows=async(store:RunStore,resource=RUN)=>{const head=await store.load(resource);return readExecution(JSON.parse(new TextDecoder().decode(head!.checkpoint!)))!.durable!}
const completion=(job:string,over:Partial<JobCompletionEvidence>={}):JobCompletionEvidence=>({version:1,resource:RUN,job,generation:1,evidence:'done-1',outcome:'succeeded',key:KEY,...over})
const reconciliation=(job:string,over:Partial<JobReconciliation>={}):JobReconciliation=>({version:1,resource:RUN,job,generation:1,evidence:'find-1',finding:'not-started',key:KEY,...over})
const parkJob=(store:RunStore,fence:ReturnType<typeof fenceOf>,job:string,over:Record<string,unknown>={})=>parkRun({store,fence,wait:'wait-j',continuation:'k-j',job,condition:{event:{kind:'job-completion',subject:job}},model:MODEL,wakeKeyDigest:wakeKeyDigest(WAKE),release:[],...over} as never)
const current=async(store:RunStore,wait='wait-j')=>{
 const held=(await MemoryRunStore.prototype.readWait.call(store,RUN,wait))!,{model,...rest}=held.pins
 return {version:1,resource:RUN,wait,...rest,model:{...model,status:'available'},policy:{digest:held.pins.policy}}
}
/** A run whose model started one job and ended its turn. */
async function oneJob(answer:()=>Promise<JobStartAnswer>=started,make?:(now:()=>Date)=>DurableJobStore){
 const held=await leased(make),start=vi.fn(async()=>answer()),first=provider([CALL,{say:'The job is running.'}])
 const tool=await startTool(start as never),events=await drive(held.view,[tool,jobReadTool(held.view)],first.call)
 const job=(await listJobs(held.store,RUN)).jobs[0]!
 return {...held,start,first,tool,events,job}
}
/** What a decision on a held start must carry. */
const HELD_PROOF=(asked:{request?:string;args?:Record<string,unknown>})=>({shownDigest:approvalShownDigest(asked)})
vi.setConfig({testTimeout:120_000})

describe('a start is registered with its claim, before it is admitted (#229)',()=>{
 it('the job exists, unproven, when the effect host is asked; the row and the model hold only what the package composed',async()=>{
  const clock={at:T0},store=new DurableJobStore({now:()=>new Date(clock.at)})
  const lease=await store.acquireLease({resource:RUN,owner:'worker-a',expectedRevision:null,durationMs:HOUR}),fence=fenceOf(lease)
  const seen:(string|undefined)[]=[]
  let keyAt:string|undefined,requested:string|undefined
  const host=await certifyEffectHost(authority=>{const inner=new ReferenceEffectHost(authority);return {effectFencing:'enforced',dispatch:async(admission,perform,signal)=>{seen.push((await readJob(store,RUN,jobIdentity(RUN,admission.operation.operation)))?.state);return inner.dispatch(admission,perform,signal)}} as EffectHost},store)
  seen.length=0
  const view=fencedRunStore(store,fence,host),first=provider([CALL,{say:'Running.'}])
  const tool=await startTool(async input=>{requested=input.request;keyAt=input.job;return started()})
  const events=await drive(view,[tool],first.call)
  expect(seen).toEqual(['unproven'])
  const job=(await listJobs(store,RUN)).jobs[0]!
  expect(job).toMatchObject({job:keyAt,state:'started',handle:SECRET,receipt:RECEIPT,source:expect.any(String),tool:'runner__start_job',registered:{owner:'worker-a',epoch:'1',at:iso(T0)},generation:1})
  expect(job.job).toBe(jobIdentity(RUN,job.operation))
  const rows=await headRows(store),row=rows.dispatches.find(d=>d.id===job.operation)!
  expect(row.state).toBe('result');expect(row.request).toBe(requested);expect(job.requestDigest).toBe(row.requestDigest)
  expect((row.result as {content:{text:string}[]}).content[0]!.text).toBe(jobStartResult(job).content![0]!.text)
  // M15: a handle and a receipt go only to the job record.
  const stored=await store.readEvents(RUN,null,100),everything=[...first.state.bodies,JSON.stringify(rows),JSON.stringify(events),...stored.items.map(i=>new TextDecoder().decode(i.bytes))].join('\n')
  expect(everything).not.toContain(SECRET);expect(everything).not.toContain(RECEIPT);expect(everything).toContain('started, not finished')
 })
 it('a claim and its job commit together: a refused registration leaves no row, runs nothing, and the run goes on (M5)',async()=>{
  const held=await leased(),start=vi.fn(started)
  const taken=provider([CALL,{say:'one'}]),tool=await startTool(start)
  await drive(held.view,[tool],taken.call)
  expect(start).toHaveBeenCalledTimes(1)
  const rowsBefore=(await headRows(held.store)).dispatches.filter(d=>d.kind==='host').length
  const again=provider([{call:{tool:'runner__start_job',arguments:{name:'other'}}},{say:'refused'}])
  const events=await drive(held.view,[tool],again.call,{resume:true})
  expect(start).toHaveBeenCalledTimes(1)
  expect(events.some(e=>e.type==='tool_result'&&e.isError===true&&JSON.stringify(e).includes('JobRefused: key-reused'))).toBe(true)
  expect((await headRows(held.store)).dispatches.filter(d=>d.kind==='host').length).toBe(rowsBefore)
  expect((await listJobs(held.store,RUN)).jobs).toHaveLength(1)
  expect(events.at(-1)?.type).toBe('end')
 })
 it('a key the host cannot mint, or one of another form, leaves no row and runs nothing (M5)',async()=>{
  for(const mint of [async()=>{throw Error('no key')},async()=>'not-a-digest']){
   const held=await leased(),start=vi.fn(started),first=provider([CALL,{say:'refused'}]),registered:unknown[]=[]
   const fenced=held.store.commitFenced.bind(held.store),history=held.store.commitHistoryFenced.bind(held.store)
   vi.spyOn(held.store,'commitFenced').mockImplementation(async(input,...rest)=>{if(input.job)registered.push(input.job);return fenced(input,...rest)})
   vi.spyOn(held.store,'commitHistoryFenced').mockImplementation(async(input,...rest)=>{if(input.run.job)registered.push(input.run.job);return history(input,...rest)})
   const events=await drive(held.view,[await startTool(start,{mint})],first.call)
   expect(start).not.toHaveBeenCalled()
   // The writer checks it itself: the store is never handed it.
   expect(registered).toEqual([])
   expect(events.some(e=>e.type==='tool_result'&&e.isError===true&&JSON.stringify(e).includes('JobRefused: invalid'))).toBe(true)
   expect((await headRows(held.store)).dispatches.filter(d=>d.kind==='host')).toEqual([])
   expect((await listJobs(held.store,RUN)).jobs).toEqual([])
  }
 })
 it('an unfenced commit carrying a registration is refused, and a fenced one is not (the store\'s own check)',async()=>{
  const store=new DurableJobStore()
  const registration={job:jobIdentity(RUN,'op-1'),operation:'op-1',source:'s',tool:'t',requestDigest:`sha256:${'a'.repeat(64)}`,keyDigest:jobKeyDigest(KEY)}
  await expect(store.commit({runId:RUN,expectedRevision:null,commitId:'c1',checkpoint:new TextEncoder().encode('{}'),events:[],job:registration})).rejects.toEqual(jobRefusal('invalid'))
  expect(await store.load(RUN)).toBeUndefined();expect((await store.exportJobs(RUN)).jobs).toEqual([])
 })
 it('a throw, or an answer that is not typed, leaves the job unproven; a repeat is answered from the job and never sent again',async()=>{
  for(const answer of [async()=>{throw Error('lost')},async()=>({content:[{type:'text',text:'ok'}]})]){
   const held=await oneJob(answer as never)
   expect(held.job.state).toBe('unproven');expect(held.job.handle).toBeUndefined();expect(held.job.reclaimed).toBeUndefined()
   const row=(await headRows(held.store)).dispatches.find(d=>d.id===held.job.operation)!
   expect(row.state).toBe('unknown')
   const repeat=provider([CALL,{say:'told'}]),events=await drive(held.view,[held.tool],repeat.call,{resume:true})
   expect(held.start).toHaveBeenCalledTimes(1)
   // With no grant the repeat asks the store for no re-claim.
   expect((await readJob(held.store,RUN,held.job.job))?.reclaimed).toBeUndefined()
   const answered=events.find(e=>e.type==='tool_result') as Extract<AssistantEvent,{type:'tool_result'}>
   expect(JSON.stringify(answered)).toContain('not proven to have started')
   expect(answered.isError).toBe(true)
  }
 })
 it('a typed not-started closes the job; the row says so and nothing is running',async()=>{
  const held=await oneJob(async()=>({started:false}))
  expect(held.job.state).toBe('not-started');expect(held.job.closed?.by).toBe('answer')
  const row=(await headRows(held.store)).dispatches.find(d=>d.id===held.job.operation)!
  expect((row.result as {isError?:boolean}).isError).toBe(true)
  expect(JSON.stringify(row.result)).toContain('it did not start')
 })
 it('M2: a typed answer is kept when the call\'s signal ended while it ran',async()=>{
  const held=await leased(),stop=new AbortController(),first=provider([CALL,{say:'never'}])
  const tool=await startTool(async()=>{stop.abort();return started()})
  await drive(held.view,[tool],first.call,{signal:stop.signal}).catch(()=>undefined)
  // The stop ends the run at once, and the writer keeps the answer as it lands.
  await vi.waitFor(async()=>expect((await listJobs(held.store,RUN)).jobs[0]!.state).toBe('started'))
  expect((await listJobs(held.store,RUN)).jobs[0]!.handle).toBe(SECRET)
 })
 it('M2: a typed answer is kept when the dispatch closed while it ran',async()=>{
  const held=await leased(),writer=new RunWriter(held.view,RUN),binding:DurableBinding={writer,root:true,scope:{execution:RUN,role:'author'}}
  writer.bind(()=>({version:2,id:RUN,requestKey:'a'.repeat(64),phase:'work',status:'running',unit:'model-requests',used:0,limit:20,grants:[],plan:[],explanation:'',operations:[],context:{scope:'fixture',entries:[]}}) as ExecutionCheckpoint,RECORD_DEFAULTS,()=>undefined)
  await writer.attachJobs()
  await expect(writer.invoke(binding,'host','source','start','{"n":1}',false,async()=>{writer.closeDispatch(binding);return started() as never},undefined,undefined,undefined,undefined,undefined,undefined,undefined,true,{mint:async()=>jobKeyDigest(KEY),bind:()=>undefined})).rejects.toMatchObject({name:'AbortError'})
  const job=(await listJobs(held.store,RUN)).jobs[0]!
  expect(job.state).toBe('started');expect(job.handle).toBe(SECRET)
 })
 it('M2: an answer that arrives after the lease ran out is kept: it may be the only copy of the handle',async()=>{
  const held=await leased(),first=provider([CALL,{say:'late'}])
  const tool=await startTool(async()=>{held.clock.at=T0+2*HOUR;return started()})
  await drive(held.view,[tool],first.call).catch(()=>undefined)
  const job=(await listJobs(held.store,RUN)).jobs[0]!
  expect(job.state).toBe('started');expect(job.handle).toBe(SECRET)
  // The row could not be committed under that fence: it stays claimed, and is never re-sent.
  expect((await headRows(held.store)).dispatches.find(d=>d.id===job.operation)!.state).toBe('claimed')
 })
 it('another fence cannot answer for the start, and a grant moves who may (the store)',async()=>{
  const held=await oneJob(async()=>{throw Error('lost')})
  const answer:JobAnswer={version:1,resource:RUN,job:held.job.job,generation:1,answer:'started',handle:'h'}
  await expect(held.store.answerJob(answer,{version:1,resource:RUN,owner:'worker-b',epoch:'1'})).rejects.toEqual(jobRefusal('unauthenticated'))
  await expect(held.store.answerJob(answer,{version:1,resource:RUN,owner:'worker-a',epoch:'2'})).rejects.toEqual(jobRefusal('unauthenticated'))
  await expect(held.store.answerJob(answer,{version:1,resource:'run-other',owner:'worker-a',epoch:'1'})).rejects.toEqual(jobRefusal('unauthenticated'))
  await expect(held.store.answerJob(answer,held.fence)).resolves.toMatchObject({state:'started',handle:'h'})
 })
})

describe('the store\'s registration, and what the package holds itself to (#229)',()=>{
 it('a repeated commit id must carry the same registration',async()=>{
  const {store,fence}=await leased()
  const registration=(key:string,operation='op-1')=>({job:jobIdentity(RUN,operation),operation,source:'s',tool:'t',requestDigest:`sha256:${'a'.repeat(64)}`,keyDigest:jobKeyDigest(key)})
  const commit=(job:JobRegistration)=>store.commitFenced({runId:RUN,expectedRevision:null,commitId:'claim-1',checkpoint:new TextEncoder().encode('{}'),events:[],job},fence)
  const first=await commit(registration(KEY))
  await expect(commit(registration(KEY))).resolves.toEqual(first)
  await expect(commit(registration(WAKE,'op-2'))).rejects.toMatchObject({name:'StorageCommitMismatch'})
  expect((await store.exportJobs(RUN)).jobs).toHaveLength(1)
 })
 it('the park reads the job itself: a store that would take a settled or unproven job is not asked',async()=>{
  const settled=await oneJob(),parked=vi.spyOn(settled.store,'parkWait')
  await completeJob(settled.store,completion(settled.job.job))
  await expect(parkJob(settled.store,settled.fence,settled.job.job)).rejects.toEqual(jobRefusal('job-settled'))
  expect(parked).not.toHaveBeenCalled()
  const unproven=await oneJob(async()=>{throw Error('lost')}),held=vi.spyOn(unproven.store,'parkWait')
  await expect(parkJob(unproven.store,unproven.fence,unproven.job.job)).rejects.toEqual(jobRefusal('job-unproven'))
  expect(held).not.toHaveBeenCalled()
 })
 it('a resume reads the job through the reader: a store that answers another job is refused',async()=>{
  const held=await oneJob(),{store,job}=held
  await parkJob(store,held.fence,job.job)
  await completeJob(store,completion(job.job))
  Object.assign(store,{readJob:async()=>({...job,resource:'run-other',job:jobIdentity('run-other',job.operation)})})
  await expect(resumeParked({store,resource:RUN,wait:'wait-j',evidence:'done-1',owner:'worker-b',durationMs:HOUR,current:await current(store)})).rejects.toEqual(waitRefusal('invalid'))
 })
 it('a continuation must be this run\'s job',async()=>{
  const held=await oneJob(),first=provider([{say:'never'}])
  const foreign={...held.job,resource:'run-other',job:jobIdentity('run-other',held.job.operation)}
  // #330: at a finished head, a continuation no claimed wait bears out is RunComplete first; this reads the session's own check
  // at a head left running.
  await interrupted(held.view)
  const events=await drive(held.view,[held.tool],first.call,{resume:true,jobContinuation:foreign})
  expect(first.state.requests).toBe(0);expect(JSON.stringify(events)).toContain('JobRefused: invalid')
 })
 it('a parked approval carries the digest of what the person was shown to the continuation, and nothing of another form',async()=>{
  const clock={at:T0},store=new DurableJobStore({now:()=>new Date(clock.at)}),a=await store.acquireLease({resource:RUN,owner:'worker-a',expectedRevision:null,durationMs:HOUR})
  const view=unattendedRunStore({store,fence:fenceOf(a),effectHost:new ReferenceEffectHost(store)}),tool=await startTool(started,{approve:'ask'})
  const events=await drive(view,[tool],provider([CALL]).call),asked=events.find(e=>e.type==='approval_requested') as Extract<AssistantEvent,{type:'approval_requested'}>
  await parkRun({store,fence:fenceOf(a),wait:'wait-a',continuation:'k',condition:{event:{kind:'decision',subject:asked.callId}},model:MODEL,limits:{approvalTtlMs:HOUR},wakeKeyDigest:wakeKeyDigest(WAKE),release:[]})
  await wakeRun(store,{version:1,resource:RUN,wait:'wait-a',evidence:'decided',kind:'event',event:{kind:'decision',subject:asked.callId},key:WAKE})
  const input={store,resource:RUN,wait:'wait-a',evidence:'decided',owner:'worker-b',durationMs:HOUR,current:await current(store,'wait-a')}
  await expect(resumeParked({...input,approval:{callId:asked.callId,approved:true,shownDigest:'not-a-digest'}})).rejects.toEqual(waitRefusal('decision-required'))
  const shownDigest=`sha256:${'e'.repeat(64)}`
  await expect(resumeParked({...input,approval:{callId:asked.callId,approved:true,shownDigest}})).resolves.toMatchObject({approval:{shownDigest}})
 })
})

describe('completion and reconciliation, from the host only (#229)',()=>{
 it('a completion needs the job\'s key and no lease; a duplicate, a foreign run and a stale one change nothing',async()=>{
  const held=await oneJob()
  await held.store.releaseLease(held.fence)
  await expect(completeJob(held.store,completion(held.job.job,{key:'job.key.wrong.Zc3Ri7Oq1Xe5Pk9Ls2Gu6Ty8Mn4'}))).rejects.toEqual(jobRefusal('unauthenticated'))
  await expect(completeJob(held.store,completion(held.job.job,{resource:'run-other'}))).rejects.toEqual(jobRefusal('no-job'))
  const done=await completeJob(held.store,completion(held.job.job))
  expect(done).toMatchObject({state:'completed',completion:{evidence:'done-1',outcome:'succeeded'},handle:SECRET})
  await expect(completeJob(held.store,completion(held.job.job))).resolves.toEqual(done)
  await expect(completeJob(held.store,completion(held.job.job,{outcome:'failed'}))).rejects.toEqual(jobRefusal('conflict'))
  await expect(completeJob(held.store,completion(held.job.job,{evidence:'done-2',outcome:'failed'}))).rejects.toEqual(jobRefusal('stale'))
  expect(await readJob(held.store,RUN,held.job.job)).toEqual(done)
 })
 it('M1: a reconciliation needs the key and no lease; first write wins; unknown changes nothing',async()=>{
  const held=await oneJob(async()=>{throw Error('lost')})
  await held.store.releaseLease(held.fence)
  await expect(reconcileJob(held.store,reconciliation(held.job.job,{finding:'unknown'}))).resolves.toMatchObject({state:'unproven'})
  await expect(reconcileJob(held.store,reconciliation(held.job.job,{key:'job.key.wrong.Zc3Ri7Oq1Xe5Pk9Ls2Gu6Ty8Mn4'}))).rejects.toEqual(jobRefusal('unauthenticated'))
  const closed=await reconcileJob(held.store,reconciliation(held.job.job))
  expect(closed).toMatchObject({state:'not-started',closed:{by:'reconcile',evidence:'find-1'}})
  await expect(reconcileJob(held.store,reconciliation(held.job.job))).resolves.toEqual(closed)
  await expect(reconcileJob(held.store,reconciliation(held.job.job,{finding:'started',handle:'h'}))).rejects.toEqual(jobRefusal('conflict'))
 })
 it('H1: a completion or a start after not-started is kept as a contradiction, reported, and moves nothing',async()=>{
  const held=await oneJob(async()=>{throw Error('lost')})
  await reconcileJob(held.store,reconciliation(held.job.job))
  const contradicted=await completeJob(held.store,completion(held.job.job))
  expect(contradicted.state).toBe('not-started')
  expect(contradicted.contradictions).toEqual([expect.objectContaining({kind:'completed',evidence:'done-1',outcome:'succeeded'})])
  const again=await reconcileJob(held.store,reconciliation(held.job.job,{evidence:'find-2',finding:'started',handle:'h2'}))
  expect(again.contradictions).toHaveLength(2);expect(again.contradictions![1]).toMatchObject({kind:'started',handle:'h2',evidence:'find-2'})
  await expect(completeJob(held.store,completion(held.job.job))).resolves.toEqual(again)
  const report=await jobReport(held.store,RUN)
  expect(report.entries.map(e=>[e.decision,e.contradicts])).toEqual([['register',undefined],['not-started',undefined],['contradiction','completed'],['contradiction','started']])
  expect(jobProjection(again).contradicted).toBe(true)
  expect(JSON.stringify(report)).not.toContain(KEY)
  // The eighth is kept; the ninth is refused rather than growing the record.
  let last=again
  for(let i=3;i<=8;i++)last=await completeJob(held.store,completion(held.job.job,{evidence:`done-${i}`}))
  expect(last.contradictions).toHaveLength(8)
  await expect(completeJob(held.store,completion(held.job.job,{evidence:'done-9'}))).rejects.toEqual(jobRefusal('stale'))
 })
 it('H2: a grant re-sends a start only while its job is unproven; a closed job refuses it, and the grant is not spent',async()=>{
  // A trusted refusal under the credential check leaves the job unproven with a typed row, and the run parks for it.
  const refusal={cause:'expired' as const,connection:'runner-conn',principal:`sha256:${'c'.repeat(64)}`,permissions:['jobs:write'],requires:['jobs:write']}
  const run=async(close?:Partial<JobReconciliation>)=>{
   const clock={at:T0},store=new DurableJobStore({now:()=>new Date(clock.at)}),creds={refused:true as boolean}
   const host=new ReferenceEffectHost(store,{credentials:async()=>creds.refused?refusal:undefined})
   const a=await store.acquireLease({resource:RUN,owner:'worker-a',expectedRevision:null,durationMs:HOUR}),view=unattendedRunStore({store,fence:fenceOf(a),effectHost:host})
   const start=vi.fn(started),tool=await startTool(start),first=provider([CALL,{say:'stopped'}])
   await drive(view,[tool],first.call)
   expect(start).not.toHaveBeenCalled()
   const job=(await listJobs(store,RUN)).jobs[0]!;expect(job.state).toBe('unproven')
   await parkRun({store,fence:fenceOf(a),wait:'wait-r',continuation:'k',condition:{event:{kind:'reauthorized',subject:'runner-conn'}},model:MODEL,wakeKeyDigest:wakeKeyDigest(WAKE),release:[],reauthorization:reauthorizationOf(view)!})
   clock.at=T0+MINUTE
   await wakeRun(store,{version:1,resource:RUN,wait:'wait-r',evidence:'refreshed',kind:'event',event:{kind:'reauthorized',subject:'runner-conn'},key:WAKE})
   clock.at=T0+2*MINUTE
   const resumed=await resumeParked({store,resource:RUN,wait:'wait-r',evidence:'refreshed',owner:'worker-b',durationMs:HOUR,current:await current(store,'wait-r'),reauthorization:{version:1,resource:RUN,wait:'wait-r',connection:'runner-conn',action:'refresh',principal:refusal.principal,permissions:['jobs:write'],actor:'person-owner',at:iso(T0+MINUTE)}})
   creds.refused=false
   if(close)await reconcileJob(store,reconciliation(job.job,close))
   const continued=await reauthorizedRunStore({store,effectHost:host,resumed}),repeat=provider([CALL,{say:'done'}])
   const events=await drive(continued,[tool],repeat.call,{resume:true})
   return {store,start,job,events,row:(await headRows(store)).dispatches.find(d=>d.id===job.operation)!,after:(await readJob(store,RUN,job.job))!}
  }
  const open=await run()
  expect(open.start).toHaveBeenCalledTimes(1)
  expect(open.after).toMatchObject({state:'started',handle:SECRET,reclaimed:{owner:'worker-b'}});expect(open.row.state).toBe('result')
  const closed=await run({})
  expect(closed.start).not.toHaveBeenCalled()
  expect(closed.after.state).toBe('not-started');expect(closed.row.state).toBe('unknown')
  expect(closed.row.failure).toMatchObject({code:'authentication',outcome:'not-started'})
  // H-1: what it is told is what the job says, not that its input was invalid.
  expect(closed.events.find(e=>e.type==='tool_result')).toMatchObject({isError:true,text:jobStartResult(closed.after).content![0]!.text})
  expect(JSON.stringify(closed.events)).not.toContain('JobRefused')
  const running=await run({finding:'started',handle:'H1'})
  expect(running.start).not.toHaveBeenCalled()
  expect(running.after).toMatchObject({state:'started',handle:'H1'});expect(running.row.state).toBe('unknown')
  expect(running.events.find(e=>e.type==='tool_result')).toMatchObject({text:jobStartResult(running.after).content![0]!.text})
  expect(JSON.stringify(running.events)).not.toContain('JobRefused')
 })
 it('a re-claim is under the current fence (the store)',async()=>{
  const held=await oneJob(async()=>{throw Error('lost')})
  await expect(held.store.reclaimJob(RUN,held.job.job,{...held.fence,owner:'worker-x'})).rejects.toMatchObject({name:'LeaseRefused',reason:'other-owner'})
  await expect(held.store.reclaimJob('run-other',held.job.job,held.fence)).rejects.toMatchObject({name:'LeaseRefused',reason:'resource-mismatch'})
  await expect(held.store.reclaimJob(RUN,held.job.job,held.fence)).resolves.toMatchObject({reclaimed:{owner:'worker-a',epoch:'1'}})
 })
})

describe('job waits: only a completion wakes one, and nothing wakes it twice (#229)',()=>{
 it('a model ends its turn, the run parks, the completion wakes it, the resume says why, and no model polls',async()=>{
  const held=await oneJob(),{store,job}=held
  expect(held.first.state.requests).toBe(2)
  const parked=await parkJob(store,held.fence,job.job)
  expect(parked.wait).toMatchObject({reason:'job',state:'parked',pending:{kind:'job',job:job.job,generation:1}})
  expect(parked.attention).toMatchObject({needs:'job-completion',subject:job.job,reason:'job'})
  await expect(store.acquireLease({resource:RUN,owner:'worker-x',expectedRevision:null,durationMs:HOUR})).rejects.toEqual(waitRefusal('wait-pending'))
  await expect(resumeParked({store,resource:RUN,wait:'wait-j',evidence:'done-1',owner:'worker-b',durationMs:HOUR,current:await current(store)})).rejects.toEqual(waitRefusal('unacknowledged'))
  held.clock.at=T0+MINUTE
  const done=await completeJob(store,completion(job.job,{resultDigest:`sha256:${'d'.repeat(64)}`}))
  const woken=(await exportWaits(store,RUN)).waits[0]!
  expect(woken.state).toBe('woken');expect(woken.disposition).toEqual({evidence:'done-1',kind:'event',at:iso(T0+MINUTE),digest:done.completion!.digest})
  expect(store.jobLifetime).toBe('durable')
  // A replay wakes nothing more, and a decision or an answer is not a job's to take.
  await expect(completeJob(store,completion(job.job,{resultDigest:`sha256:${'d'.repeat(64)}`}))).resolves.toEqual(done)
  await expect(resumeParked({store,resource:RUN,wait:'wait-j',evidence:'done-1',owner:'worker-b',durationMs:HOUR,current:await current(store),approval:{callId:'x',approved:true}})).rejects.toEqual(waitRefusal('decision-required'))
  const resumed=await resumeParked({store,resource:RUN,wait:'wait-j',evidence:'done-1',owner:'worker-b',durationMs:HOUR,current:await current(store)})
  expect(resumed.job).toEqual(done)
  const view=unattendedRunStore({store,fence:resumed.fence,effectHost:held.host}),next=provider([READ(job.job),READ(job.job),{say:'It succeeded.'}])
  // #330 HIGH-1: the claimed wait is on this job: a continuation of another job of the run is not borne out, and nothing runs.
  const other=provider([CALL,{say:'x'}]),mismatched=await drive(view,[held.tool,jobReadTool(view)],other.call,{resume:true,jobContinuation:{...resumed.job!,job:jobIdentity(RUN,'op-other')}})
  expect(other.state.requests).toBe(0);expect(mismatched.filter(e=>e.type==='error')).toEqual([expect.objectContaining({failure:expect.objectContaining({detail:RUN_COMPLETE})})])
  const events=await drive(view,[held.tool,jobReadTool(view)],next.call,{resume:true,jobContinuation:resumed.job!})
  expect(events.at(-1)?.type).toBe('end')
  expect(next.state.bodies[0]).toContain('JOB RESULT:');expect(next.state.bodies[0]).toContain('succeeded')
  expect(next.state.bodies[0]).not.toContain(SECRET)
  // The read answers every time, and the second says it is unchanged.
  const reads=events.filter(e=>e.type==='tool_result'&&e.name==='job_read') as Extract<AssistantEvent,{type:'tool_result'}>[]
  expect(JSON.stringify(reads[0])).toContain('\\"changed\\":true');expect(JSON.stringify(reads[1])).toContain('\\"changed\\":false');expect(JSON.stringify(reads[1])).toContain('do not poll')
  expect(reads.every(r=>r.isError!==true)).toBe(true)
  expect(JSON.stringify(events)).not.toContain(SECRET)
  // #330 HIGH-1: the same continuation delivered again, after it ran, meets a head that has moved: refused RunComplete, and
  // the model is not asked.
  const again=provider([CALL,{say:'x'}]),redelivered=await drive(view,[held.tool,jobReadTool(view)],again.call,{resume:true,jobContinuation:resumed.job!})
  expect(again.state.requests).toBe(0);expect(held.start).toHaveBeenCalledTimes(1)
  expect(redelivered.filter(e=>e.type==='error')).toEqual([expect.objectContaining({failure:expect.objectContaining({detail:RUN_COMPLETE})})])
 })
 it('#330 HIGH-1: a continuation no claimed wait bears out is refused at a finished run, and nothing starts again',async()=>{
  const held=await oneJob(),second=provider([CALL,{say:'x'}])
  const events=await drive(held.view,[held.tool,jobReadTool(held.view)],second.call,{resume:true,jobContinuation:held.job})
  expect(second.state.requests).toBe(0);expect(held.start).toHaveBeenCalledTimes(1);expect((await listJobs(held.store,RUN)).jobs).toHaveLength(1)
  expect(events.filter(e=>e.type==='error')).toEqual([expect.objectContaining({failure:expect.objectContaining({detail:RUN_COMPLETE})})])
  // A wait parked on that job and not claimed bears nothing out either.
  await parkJob(held.store,held.fence,held.job.job)
  const third=provider([CALL,{say:'x'}]),unclaimed=await drive(held.view,[held.tool,jobReadTool(held.view)],third.call,{resume:true,jobContinuation:held.job})
  expect(third.state.requests).toBe(0);expect(held.start).toHaveBeenCalledTimes(1)
  expect(unclaimed.filter(e=>e.type==='error')).toEqual([expect.objectContaining({failure:expect.objectContaining({detail:RUN_COMPLETE})})])
 })
 it('the lost wake-up: a job settled before the park refuses it; an unproven or a foreign one too',async()=>{
  const held=await oneJob()
  await completeJob(held.store,completion(held.job.job))
  await expect(parkJob(held.store,held.fence,held.job.job)).rejects.toEqual(jobRefusal('job-settled'))
  // The store's own check: the same park, as if the job settled after the package read it.
  await expect(parkJob(held.store,held.fence,jobIdentity(RUN,'no-such-op'))).rejects.toEqual(jobRefusal('no-job'))
  const unproven=await oneJob(async()=>{throw Error('lost')})
  await expect(parkJob(unproven.store,unproven.fence,unproven.job.job)).rejects.toEqual(jobRefusal('job-unproven'))
  const closed=await oneJob(async()=>({started:false}))
  await expect(parkJob(closed.store,closed.fence,closed.job.job)).rejects.toEqual(jobRefusal('job-settled'))
 })
 it('the store refuses a settled job in the park\'s own transaction',async()=>{
  const held=await oneJob(),{store,fence,job}=held
  const head=(await store.load(RUN))!,digest=`sha256:${(await import('../records')).sha256(new TextDecoder().decode(head.checkpoint!))}`
  const park=(wakeKeyDigest_:string)=>({version:1 as const,resource:RUN,wait:'wait-j',reason:'job' as const,pending:{kind:'job' as const,job:job.job,generation:1 as const},condition:{event:{kind:'job-completion',subject:job.job}},continuation:'k',checkpoint:{revision:head.revision,digest},pins:{model:MODEL,policy:`sha256:${'a'.repeat(64)}`,sources:[],artifacts:[]},wakeKeyDigest:wakeKeyDigest_,release:[]})
  await completeJob(store,completion(job.job))
  await expect(store.parkWait(park(wakeKeyDigest(WAKE)),fence)).rejects.toEqual(jobRefusal('job-settled'))
  expect((await store.readLease(RUN))?.state).toBe('held')
  expect((await exportWaits(store,RUN)).waits).toEqual([])
 })
 it('a job wait is held to its job, and to no chat (the store\'s reader)',async()=>{
  const held=await oneJob(),{store,fence,job}=held
  const head=(await store.load(RUN))!,digest=`sha256:${(await import('../records')).sha256(new TextDecoder().decode(head.checkpoint!))}`
  const park={version:1 as const,resource:RUN,wait:'wait-j',reason:'job' as const,pending:{kind:'job' as const,job:job.job,generation:1 as const},condition:{event:{kind:'job-completion',subject:job.job}},continuation:'k',checkpoint:{revision:head.revision,digest},pins:{model:MODEL,policy:`sha256:${'a'.repeat(64)}`,sources:[],artifacts:[]},wakeKeyDigest:wakeKeyDigest(WAKE),release:[]}
  await expect(store.parkWait({...park,condition:{event:{kind:'anything',subject:job.job}}},fence)).rejects.toEqual(waitRefusal('invalid'))
  await expect(store.parkWait({...park,condition:{event:{kind:'job-completion',subject:'another'}}},fence)).rejects.toEqual(waitRefusal('invalid'))
  await expect(store.parkWait({...park,chat:{id:'chat-1',revision:'r'}},fence)).rejects.toEqual(waitRefusal('invalid'))
  await expect(store.parkWait({...park,pending:{...park.pending,generation:2 as never}},fence)).rejects.toEqual(waitRefusal('invalid'))
  await expect(store.parkWait({...park,condition:{...park.condition,wakeAt:iso(T0+HOUR)}},fence)).rejects.toEqual(waitRefusal('invalid'))
  await expect(store.parkWait(park,fence)).resolves.toMatchObject({wait:{state:'parked'}})
 })
 it('a stopped checkpoint has nothing to continue, whatever its job',async()=>{
  const held=await oneJob(),{store,job}=held,head=(await store.load(RUN))!
  const stopped=JSON.parse(new TextDecoder().decode(head.checkpoint!));stopped.status='stopped'
  vi.spyOn(store,'load').mockResolvedValue({...head,checkpoint:new TextEncoder().encode(JSON.stringify(stopped))})
  await expect(parkJob(store,held.fence,job.job)).rejects.toEqual(waitRefusal('nothing-pending'))
 })
 it('a job park refuses a pending decision and a chat binding',async()=>{
  const held=await leased(),first=provider([CALL,{say:'never'}])
  await drive(held.view,[await startTool(started,{approve:'ask'})],first.call)
  const job=jobIdentity(RUN,'unused')
  await expect(parkJob(held.store,held.fence,job)).rejects.toEqual(waitRefusal('decision-required'))
  const other=await oneJob()
  await expect(parkJob(other.store,other.fence,other.job.job,{chat:{store:{consistency:'cas',get:async()=>undefined} as never,id:'chat-1'}})).rejects.toMatchObject({name:'WaitCapabilityRefused',reason:'chat-store-unsupported'})
 })
 it('only a completion wakes a job wait; a timeout does not close the run, and a deadline or a timeout already come wakes nothing',async()=>{
  const held=await oneJob(),{store,job,clock}=held
  await parkJob(store,held.fence,job.job,{timeoutAt:iso(T0+10*MINUTE)})
  await expect(wakeRun(store,{version:1,resource:RUN,wait:'wait-j',evidence:'forged',kind:'event',event:{kind:'job-completion',subject:job.job},key:WAKE})).rejects.toEqual(waitRefusal('mismatch'))
  clock.at=T0+10*MINUTE
  // A completion at or after the timeout is recorded and wakes nothing: only the timeout ends the wait.
  await completeJob(store,completion(job.job))
  expect((await exportWaits(store,RUN)).waits[0]!.state).toBe('parked')
  await wakeRun(store,{version:1,resource:RUN,wait:'wait-j',evidence:'timeout-1',kind:'timeout',key:WAKE})
  expect((await exportWaits(store,RUN)).waits[0]!.state).toBe('timed-out')
  const resumed=await resumeParked({store,resource:RUN,wait:'wait-j',evidence:'timeout-1',owner:'worker-b',durationMs:HOUR,current:await current(store)})
  expect(resumed.job?.state).toBe('completed');expect(resumed.wait.state).toBe('resumed')
  await store.releaseLease(resumed.fence)
  await expect(store.acquireLease({resource:RUN,owner:'worker-c',expectedRevision:(await store.load(RUN))!.revision,durationMs:HOUR})).resolves.toMatchObject({epoch:'3'})
 })
 it('a run deadline that has come wakes nothing either, and a key belongs to one job or one wait',async()=>{
  const held=await oneJob(),{store,job,clock}=held
  await expect(parkJob(store,held.fence,job.job,{wakeKeyDigest:jobKeyDigest(KEY)})).rejects.toEqual(waitRefusal('key-reused'))
  await parkJob(store,held.fence,job.job,{limits:{waitMs:MINUTE}})
  clock.at=T0+MINUTE
  await completeJob(store,completion(job.job))
  expect((await exportWaits(store,RUN)).waits[0]!.state).toBe('parked')
 })
})

describe('the model\'s read and plan evidence (#229)',()=>{
 it('reaches only this run\'s jobs: another run\'s, an absent one and a malformed id answer no-job alike',async()=>{
  const held=await oneJob(),other=await leased(undefined,'run-other')
  const tool=jobReadTool(other.view)
  const signal=new AbortController().signal
  // The other run's view cannot see this run's job, though the id is real.
  await expect(tool.execute({job:held.job.job},signal)).rejects.toEqual(jobRefusal('no-job'))
  const here=jobReadTool(held.view)
  await expect(here.execute({job:jobIdentity(RUN,'no-such-op')},signal)).rejects.toEqual(jobRefusal('no-job'))
  await expect(here.execute({job:'job-1'},signal)).rejects.toEqual(jobRefusal('no-job'))
  await expect(here.execute({job:held.job.job,x:1},signal)).rejects.toEqual(jobRefusal('no-job'))
  const answer=await here.execute({job:held.job.job},signal)
  expect(answer.content![0]!.text).toBe(canonicalJSON({...jobProjection(held.job),changed:true}))
  expect(answer.content![0]!.text).not.toContain(SECRET)
  // A store that answers with another run's job is refused, not shown.
  const liar=new DurableJobStore();Object.assign(liar,{readJob:async()=>held.job})
  await expect(readJob(liar,'run-other',held.job.job)).rejects.toEqual(jobRefusal('invalid'))
 })
 it('H3, M3: a read row is never evidence; a start row is evidence only once its job succeeded, whatever its row says',async()=>{
  const held=await leased(),first=provider([CALL,{say:'running'}]),tool=await startTool(started)
  await drive(held.view,[tool],first.call)
  const job=(await listJobs(held.store,RUN)).jobs[0]!
  const read=provider([READ(job.job),{say:'read'}])
  await drive(held.view,[tool,jobReadTool(held.view)],read.call,{resume:true})
  const book=await headRows(held.store),scope={execution:RUN,role:'author'}
  const readRow=book.dispatches.find(d=>d.tool==='job_read')!,startRow=book.dispatches.find(d=>d.id===job.operation)!
  expect(readRow.state).toBe('result');expect(startRow.state).toBe('result')
  expect(book.observations.filter(o=>o.dispatchId===readRow.id||o.dispatchId===startRow.id)).toEqual([])
  const cites=(id:string,jobs?:Map<string,RunJob>)=>evidenceFailure({operations:[id],artifacts:[]},{book,scope,...(jobs?{jobs}:{})})
  // With or without a job on record, neither row passes on its own.
  expect(cites(readRow.id)).toBe('unsuccessful-operation');expect(cites(startRow.id)).toBe('unsuccessful-operation')
  const withJob=(j:RunJob)=>new Map([[j.operation,j]])
  expect(cites(startRow.id,withJob(job))).toBe('unsuccessful-operation')
  const failed=await completeJob(held.store,completion(job.job,{outcome:'failed'}))
  expect(cites(startRow.id,withJob(failed))).toBe('unsuccessful-operation')
  expect(cites(readRow.id,withJob(failed))).toBe('unsuccessful-operation')
  const succeeded={...failed,completion:{...failed.completion!,outcome:'succeeded' as const}}
  expect(cites(startRow.id,withJob(succeeded))).toBeUndefined()
  expect(availablePlanEvidence({book,scope,jobs:withJob(succeeded)}).operations.map(o=>o.id)).toEqual([startRow.id])
  expect(availablePlanEvidence({book,scope,jobs:withJob(failed)}).operations).toEqual([])
  // M3: the row is unknown (a crash after the start), and a job that succeeded still proves it, in this run's scope only.
  const unknown={...book,dispatches:book.dispatches.map(d=>d.id===startRow.id?{...d,state:'unknown' as const,result:undefined}:d)}
  expect(evidenceFailure({operations:[startRow.id],artifacts:[]},{book:unknown,scope,jobs:withJob(succeeded)})).toBeUndefined()
  expect(evidenceFailure({operations:[startRow.id],artifacts:[]},{book:unknown,scope:{execution:'other',role:'author'},jobs:withJob(succeeded)})).toBe('wrong-scope')
 })
 // complete_plan_step's fresh read of a cited job: storage/jobEvidence.test.ts.
})

describe('a held start goes through the ordinary approval (#229)',()=>{
 it('a start the person is asked about registers nothing until it is decided, then runs once with its row\'s bytes',async()=>{
  const held=await leased(),start=vi.fn(async(_:{job:string;request:string}):Promise<JobStartAnswer>=>started()),tool=await startTool(start,{approve:'ask'}),first=provider([CALL])
  const events=await drive(held.view,[tool],first.call)
  const asked=events.find(e=>e.type==='approval_requested') as Extract<AssistantEvent,{type:'approval_requested'}>&{request?:string;args?:Record<string,unknown>}
  expect(asked).toBeDefined();expect(start).not.toHaveBeenCalled();expect((await exportJobs(held.store,RUN)).jobs).toEqual([])
  const next=provider([{say:'Running.'}])
  // ApprovalNotShown: no digest, or another one, is refused before the claim; the pause stands and nothing has run.
  const unproven=async(approval:Record<string,unknown>)=>{const out=await drive(held.view,[tool],provider([{say:'never'}]).call,{resume:true,approval:{callId:asked.callId,approved:true,...approval}});expect(JSON.stringify(out)).toContain('ApprovalNotShown');expect(start).not.toHaveBeenCalled();expect((await exportJobs(held.store,RUN)).jobs).toEqual([])}
  await unproven({});await unproven({shownDigest:`sha256:${'0'.repeat(64)}`})
  const continued=await drive(held.view,[tool],next.call,{resume:true,approval:{callId:asked.callId,approved:true,...HELD_PROOF(asked)}})
  expect(continued.at(-1)?.type).toBe('end')
  expect(start).toHaveBeenCalledTimes(1)
  const job=(await listJobs(held.store,RUN)).jobs[0]!
  expect(job.state).toBe('started')
  expect(start.mock.calls[0]![0].request).toBe((await headRows(held.store)).dispatches.find(d=>d.id===job.operation)!.request)
  // What was shown is what the row holds: the digest the person's decision proved is the job's.
  expect(job.requestDigest).toBe(HELD_PROOF(asked).shownDigest)
  expect(next.state.bodies[0]).toContain('started, not finished')
 })
 it('a person who refuses leaves no job',async()=>{
  const held=await leased(),start=vi.fn(started),tool=await startTool(start,{approve:'ask'}),first=provider([CALL])
  const events=await drive(held.view,[tool],first.call),asked=events.find(e=>e.type==='approval_requested') as Extract<AssistantEvent,{type:'approval_requested'}>
  await drive(held.view,[tool],provider([{say:'Stopped.'}]).call,{resume:true,approval:{callId:asked.callId,approved:false}})
  expect(start).not.toHaveBeenCalled();expect((await exportJobs(held.store,RUN)).jobs).toEqual([])
 })
})

describe('after a start ran, the model is told what its job says (#229, H-1)',()=>{
 const ran=async(start:(input:{job:string},held:Awaited<ReturnType<typeof leased>>)=>Promise<unknown>)=>{
  const held=await leased(),calls=vi.fn(),first=provider([CALL,{say:'x'}])
  const tool=await startTool((async(input:{job:string})=>{calls();return start(input,held)}) as never)
  const events=await drive(held.view,[tool],first.call),job=(await listJobs(held.store,RUN)).jobs[0]!
  return {job,events,calls,row:(await headRows(held.store)).dispatches.find(d=>d.id===job.operation)!}
 }
 const CASES:[string,(input:{job:string},held:Awaited<ReturnType<typeof leased>>)=>Promise<unknown>,string][]=[
  ['an answer that is not typed',async()=>({content:[{type:'text',text:'ok'}]}),'unproven'],
  ['no answer at all',async()=>undefined,'unproven'],
  ['a start that throws, with the host\'s words',async()=>{throw new AIFailureError({...failure('tool-failed','tool','unknown'),detail:'SENTINEL-HOST-TEXT: created job H1 then failed'})},'unproven'],
  ['a handle that conflicts with a reconciliation',async(input,held)=>{await reconcileJob(held.store,reconciliation(input.job,{finding:'started',handle:'H1'}));return {started:true,handle:'H2'}},'started'],
  ['a late not-started on a completed job',async(input,held)=>{await completeJob(held.store,completion(input.job));return {started:false}},'completed'],
 ]
 for(const [name,start,state] of CASES)it(name,async()=>{
  const {job,events,calls,row}=await ran(start)
  expect(calls).toHaveBeenCalledTimes(1);expect(job.state).toBe(state)
  expect(row.state).toBe('unknown');expect(row.failure).toEqual(modelVector.startFailure)
  expect(events.find(e=>e.type==='tool_result')).toMatchObject({text:jobStartResult(job).content![0]!.text})
  expect(JSON.stringify(events)).not.toMatch(/JobRefused|invalid-tool-input|SENTINEL-HOST-TEXT/)
 })
 it('a handle and a receipt the record cannot hold are kept as the digest of their bytes, and the job has started',async()=>{
  const long='h'.repeat(300),receipt='r'.repeat(300),{job,row,events}=await ran(async()=>({started:true,handle:long,receipt}))
  expect(job).toMatchObject({state:'started',handle:`sha256:${sha256(long)}`,handleDigested:true,receipt:`sha256:${sha256(receipt)}`})
  expect(row.state).toBe('result')
  expect(events.find(e=>e.type==='tool_result')).toMatchObject({isError:false,text:jobStartResult(job).content![0]!.text})
  expect(JSON.stringify(events)).not.toContain('hhhh')
 })
 it('R-1: a repeat answers from the job whatever the row holds: a completed job, and evidence after a closure',async()=>{
  for(const [answer,evidence,state] of [[started,completion,'completed'],[async()=>({started:false} as JobStartAnswer),completion,'not-started']] as const){
   const held=await oneJob(answer),calls=vi.mocked(held.start)
   await completeJob(held.store,evidence(held.job.job))
   const repeat=await drive(held.view,[held.tool],provider([CALL,{say:'x'}]).call,{resume:true}),job=(await readJob(held.store,RUN,held.job.job))!
   expect(job.state).toBe(state);expect(calls).toHaveBeenCalledTimes(1)
   expect(repeat.find(e=>e.type==='tool_result')).toMatchObject({text:jobStartResult(job).content![0]!.text})
   expect(JSON.stringify(repeat.find(e=>e.type==='tool_result'))).not.toContain('nothing is running')
  }
 })
 it('R-1: with its job record lost, a repeat of a start with a result still answers its row, and starts nothing',async()=>{
  const held=await oneJob()
  const rows=(held.store as unknown as {jobs:{rows:Map<string,unknown>}}).jobs.rows;rows.delete(RUN)
  const repeat=await drive(held.view,[held.tool],provider([CALL,{say:'x'}]).call,{resume:true})
  expect(held.start).toHaveBeenCalledTimes(1)
  expect(repeat.find(e=>e.type==='tool_result')).toMatchObject({text:expect.stringContaining('started, not finished')})
 })
 it('M-3: a closed job the answer contradicts says evidence says it ran, not that nothing is running',async()=>{
  const {job,row,events}=await ran(async(input,held)=>{await reconcileJob(held.store,reconciliation(input.job));return started()})
  expect(job.state).toBe('not-started');expect(job.contradictions).toHaveLength(1)
  const text=jobStartResult(job).content![0]!.text
  expect(text).toContain('evidence since says it ran');expect(text).not.toContain('nothing is running')
  expect(events.find(e=>e.type==='tool_result')).toMatchObject({text});expect(row.state).toBe('result')
 })
 it('a gateway start reaches the members through its job: the key its effect host admitted',async()=>{
  const seen:unknown[]=[],keys:string[]=[]
  const held=await leased(),inner=new ReferenceEffectHost(held.store)
  const host=await certifyEffectHost(authority=>{const own=new ReferenceEffectHost(authority);return {effectFencing:'enforced',dispatch:async(admission,perform,signal)=>{keys.push(admission.idempotencyKey);return own.dispatch(admission,perform,signal)}} as EffectHost},held.store)
  void inner;keys.length=0
  const view=fencedRunStore(held.store,held.fence,host)
  const tool=await startTool((async(input:{job:string})=>{seen.push(gatewayActionMembers((await readJob(held.store,RUN,input.job))!));return started()}) as never)
  await drive(view,[tool],provider([CALL,{say:'x'}]).call)
  expect(seen).toEqual([{actionKey:keys[0]!.slice('sha256:'.length),keyScope:'authority'}])
 })
})
const CANCEL={name:'cancel_job',description:'Cancel one background job.'}
/** A start and its cancel, declared as a pair on one source. */
async function pair(over:{id?:string;start?:()=>Promise<unknown>;cancel?:(input:JobCancelInput)=>Promise<unknown>;approve?:'allow'|'ask'|'refuse';mint?:string}={}){
 const id=over.id??'runner',source={kind:'custom',id,trust:'person',version:'1',toolListDigest:await toolListDigest([START,{...CANCEL,inputSchema:JOB_CANCEL_SCHEMA,taxonomy:{operation:'run',effect:'act'}}] as never),identity:{kind:'connection',label:id}} as never
 const start=jobStartTool({...START,source,approve:(()=>'allow') as never,mint:async()=>jobKeyDigest(over.mint??KEY),start:(over.start??started) as never})
 const asked=vi.fn(()=>over.approve??'allow'),sent=vi.fn(over.cancel??(async()=>undefined))
 return {start,cancel:jobCancelTool({...CANCEL,source,start,approve:asked as never,cancel:sent}),asked,sent,id}
}
const cancelCall=(job:string,id='runner',over:Record<string,unknown>={})=>({call:{tool:`${id}__cancel_job`,arguments:{job,generation:1,...over}}})
const toolResultOf=(events:AssistantEvent[])=>events.find(e=>e.type==='tool_result') as {text:string;isError?:boolean}|undefined
/** A run whose model started one job through the pair and ended its turn. */
async function started2(tools:Awaited<ReturnType<typeof pair>>,make?:(now:()=>Date)=>DurableJobStore){
 const held=await leased(make)
 await drive(held.view,[tools.start,tools.cancel],provider([{call:{tool:`${tools.id}__start_job`,arguments:{name:'nightly'}}},{say:'running'}]).call)
 return {...held,job:(await listJobs(held.store,RUN)).jobs[0]!}
}
describe('a cancel asks; only a completion says it stopped (#229)',()=>{
 it('requested under the fence before admission; what is sent is the record, not the model; no state moves; its row is no evidence',async()=>{
  const seen:unknown[]=[]
  const tools=await pair({cancel:async input=>{seen.push(input,(await readJob(held.store,RUN,input.job))?.cancel?.requested.by)}})
  const held=await started2(tools),first=provider([cancelCall(held.job.job),{say:'asked'}])
  const events=await drive(held.view,[tools.start,tools.cancel],first.call,{resume:true})
  expect(tools.sent).toHaveBeenCalledTimes(1)
  expect(seen[0]).toMatchObject({job:held.job.job,generation:1,source:held.job.source,tool:held.job.tool,handle:SECRET})
  expect(seen[1]).toBe('tool')
  const job=(await readJob(held.store,RUN,held.job.job))!,rows=await headRows(held.store),row=rows.dispatches.find(d=>d.id===job.cancel?.requested.operation)!
  expect(job.state).toBe('started');expect(job.cancel?.requested).toMatchObject({by:'tool',owner:'worker-a',epoch:held.fence.epoch})
  expect(row.state).toBe('result');expect(row.tool).toBe('runner__cancel_job')
  expect(toolResultOf(events)?.text).toBe(jobCancelResult(job).content![0]!.text)
  expect(JSON.stringify(events.filter(e=>e.type==='tool_result'))).not.toContain(SECRET)
  expect(row.artifacts??[]).toEqual([])
  expect(availablePlanEvidence({book:rows,scope:{execution:RUN,role:'author'},jobs:new Map()}).operations.map(o=>o.id)).not.toContain(row.id)
 })
 it('H5: another source\'s cancel cannot reach a job: no-job before anyone is asked and before any claim',async()=>{
  const prod=await pair(),other=await pair({id:'other',mint:'job.key.0002.Hs8Pq3Lz6Wd1Rm9Tc4Vb7Nx2Ka5U'})
  const held=await started2(prod)
  const events=await drive(held.view,[prod.start,prod.cancel,other.start,other.cancel],provider([cancelCall(held.job.job,'other'),{say:'x'}]).call,{resume:true})
  expect(other.asked).not.toHaveBeenCalled();expect(other.sent).not.toHaveBeenCalled();expect(prod.sent).not.toHaveBeenCalled()
  expect(JSON.stringify(toolResultOf(events))).toContain('JobRefused: no-job')
  expect((await headRows(held.store)).dispatches.filter(d=>d.tool.includes('cancel_job'))).toEqual([])
  expect((await readJob(held.store,RUN,held.job.job))?.cancel).toBeUndefined()
 })
 it('H5: a cancel is declared with a start of its source, and offered only beside it',async()=>{
  const a=await pair(),b=await pair({id:'other'})
  expect(()=>jobCancelTool({...CANCEL,source:b.start.source!,start:a.start,cancel:async()=>undefined})).toThrow(expect.objectContaining({name:'JobCapabilityRefused',reason:'job-host-unsupported'}))
  expect(()=>jobCancelTool({...CANCEL,source:a.start.source!,start:{name:'x',source:a.start.source} as never,cancel:async()=>undefined})).toThrow(expect.objectContaining({reason:'job-host-unsupported'}))
  // Its source's list holds the cancel alone, so the source is offered, and the pair is not.
  const alone={kind:'custom',id:'runner',trust:'person',version:'1',toolListDigest:await toolListDigest([{...CANCEL,inputSchema:JOB_CANCEL_SCHEMA,taxonomy:{operation:'run',effect:'act'}}] as never),identity:{kind:'connection',label:'runner'}} as never
  const held=await leased(),first=provider([{say:'x'}])
  const events=await drive(held.view,[jobCancelTool({...CANCEL,source:alone,start:a.start,cancel:async()=>undefined})],first.call)
  expect(JSON.stringify(events)).toContain('JobCapabilityRefused: job-host-unsupported');expect(first.state.requests).toBe(0)
 })
 it('M4: what the record refuses is refused before the card and the claim: no job, another generation, a settled job, one with no handle',async()=>{
  const open=await pair(),held=await started2(open)
  const absent=await drive(held.view,[open.start,open.cancel],provider([cancelCall(jobIdentity(RUN,'no-such')),{say:'x'}]).call,{resume:true})
  expect(JSON.stringify(toolResultOf(absent))).toContain('JobRefused: no-job')
  const generation=await drive(held.view,[open.start,open.cancel],provider([cancelCall(held.job.job,'runner',{generation:2}),{say:'x'}]).call,{resume:true})
  expect(JSON.stringify(toolResultOf(generation))).not.toContain('accepted')
  expect(open.sent).not.toHaveBeenCalled();expect(open.asked).not.toHaveBeenCalled();expect((await headRows(held.store)).dispatches.filter(d=>d.tool.includes('cancel_job'))).toEqual([])
  await completeJob(held.store,completion(held.job.job))
  const settled=await drive(held.view,[open.start,open.cancel],provider([cancelCall(held.job.job),{say:'x'}]).call,{resume:true})
  expect(JSON.stringify(toolResultOf(settled))).toContain('JobRefused: job-settled');expect(open.asked).not.toHaveBeenCalled()
  const unproven=await pair({start:async()=>({content:[]})}),lost=await started2(unproven)
  expect(lost.job.state).toBe('unproven')
  const events=await drive(lost.view,[unproven.start,unproven.cancel],provider([cancelCall(lost.job.job),{say:'x'}]).call,{resume:true})
  expect(JSON.stringify(toolResultOf(events))).toContain('JobRefused: job-unproven');expect(unproven.asked).not.toHaveBeenCalled();expect(unproven.sent).not.toHaveBeenCalled()
 })
 it('M4: a held cancel goes through the ordinary approval, proving what the person was shown; the card reads the record',async()=>{
  const tools=await pair({approve:'ask'}),held=await started2(tools)
  const first=await drive(held.view,[tools.start,tools.cancel],provider([cancelCall(held.job.job)]).call,{resume:true})
  const asked=first.find(e=>e.type==='approval_requested') as unknown as {callId:string;request?:string;args?:Record<string,unknown>}
  expect(tools.sent).not.toHaveBeenCalled();expect((await readJob(held.store,RUN,held.job.job))?.cancel).toBeUndefined()
  expect(jobCancelCard((await readJob(held.store,RUN,held.job.job))!)).toEqual({source:held.job.source,tool:held.job.tool,state:'started',generation:1,job:held.job.job})
  const refused=await drive(held.view,[tools.start,tools.cancel],provider([{say:'x'}]).call,{resume:true,approval:{callId:asked.callId,approved:true}})
  expect(JSON.stringify(refused)).toContain('ApprovalNotShown');expect(tools.sent).not.toHaveBeenCalled()
  await drive(held.view,[tools.start,tools.cancel],provider([{say:'done'}]).call,{resume:true,approval:{callId:asked.callId,approved:true,...HELD_PROOF(asked)}})
  expect(tools.sent).toHaveBeenCalledTimes(1)
 })
 it('a held cancel whose job settled before the decision is refused from the record: no claim, and nothing sent',async()=>{
  const tools=await pair({approve:'ask'}),held=await started2(tools)
  const first=await drive(held.view,[tools.start,tools.cancel],provider([cancelCall(held.job.job)]).call,{resume:true})
  const asked=first.find(e=>e.type==='approval_requested') as unknown as {callId:string;request?:string;args?:Record<string,unknown>}
  await completeJob(held.store,completion(held.job.job))
  const next=provider([{say:'x'}])
  await drive(held.view,[tools.start,tools.cancel],next.call,{resume:true,approval:{callId:asked.callId,approved:true,...HELD_PROOF(asked)}})
  expect(tools.sent).not.toHaveBeenCalled();expect(next.state.bodies.join('\n')).toContain('JobRefused: job-settled')
  expect((await headRows(held.store)).dispatches.filter(d=>d.tool.includes('cancel_job'))).toEqual([])
 })
 it('R-6: a handle kept as its digest is never sent as the reference: the host gets it as a digest',async()=>{
  const long='h'.repeat(300),tools=await pair({start:async()=>({started:true,handle:long})}),held=await started2(tools)
  await drive(held.view,[tools.start,tools.cancel],provider([cancelCall(held.job.job),{say:'x'}]).call,{resume:true})
  const sent=tools.sent.mock.calls[0]![0]
  expect(sent.handle).toBeUndefined();expect(sent.handleDigest).toBe(`sha256:${sha256(long)}`)
 })
 it('a repeat is answered from the job as it reads now, and is never sent again',async()=>{
  const tools=await pair(),held=await started2(tools),call=cancelCall(held.job.job)
  await drive(held.view,[tools.start,tools.cancel],provider([call,{say:'x'}]).call,{resume:true})
  await acknowledgeJobCancel(held.store,{version:1,resource:RUN,job:held.job.job,generation:1,evidence:'word-1',outcome:'accepted',key:KEY})
  const repeat=await drive(held.view,[tools.start,tools.cancel],provider([call,{say:'x'}]).call,{resume:true})
  const job=(await readJob(held.store,RUN,held.job.job))!
  expect(tools.sent).toHaveBeenCalledTimes(1);expect(job.cancel?.acknowledged?.outcome).toBe('accepted')
  expect(toolResultOf(repeat)?.text).toBe(jobCancelResult(job).content![0]!.text)
 })
 it('a cancel that throws: one fixed failure on the row, no host text, the model answered from the job, and nothing sent again',async()=>{
  const tools=await pair({cancel:async()=>{throw new AIFailureError({...failure('tool-failed','tool','unknown'),detail:'SENTINEL-HOST-TEXT: half sent'})}}),held=await started2(tools),call=cancelCall(held.job.job)
  const events=await drive(held.view,[tools.start,tools.cancel],provider([call,{say:'x'}]).call,{resume:true})
  const job=(await readJob(held.store,RUN,held.job.job))!,row=(await headRows(held.store)).dispatches.find(d=>d.id===job.cancel!.requested.operation)!
  expect(row.state).toBe('unknown');expect(row.failure).toEqual(modelVector.cancelFailures.unrecorded)
  expect(toolResultOf(events)?.text).toBe(jobCancelResult(job).content![0]!.text)
  expect(JSON.stringify(events)).not.toMatch(/SENTINEL-HOST-TEXT|invalid-tool-input/)
  await drive(held.view,[tools.start,tools.cancel],provider([call,{say:'x'}]).call,{resume:true})
  expect(tools.sent).toHaveBeenCalledTimes(1)
 })
 it('a job that settles after it was asked about: nothing is sent, the row says so, and the model is answered from the job',async()=>{
  const tools=await pair(),held=await started2(tools),call=cancelCall(held.job.job)
  const ask=held.store.requestJobCancel.bind(held.store)
  vi.spyOn(held.store,'requestJobCancel').mockImplementation(async(...args)=>{await completeJob(held.store,completion(held.job.job));return ask(...args)})
  const events=await drive(held.view,[tools.start,tools.cancel],provider([call,{say:'x'}]).call,{resume:true})
  const job=(await readJob(held.store,RUN,held.job.job))!,rows=(await headRows(held.store)).dispatches
  expect(tools.sent).not.toHaveBeenCalled();expect(job.state).toBe('completed');expect(job.cancel).toBeUndefined()
  expect(rows.find(d=>d.tool==='runner__cancel_job')).toMatchObject({state:'unknown',failure:modelVector.cancelFailures.notSent})
  expect(toolResultOf(events)).toMatchObject({isError:true,text:jobCancelResult(job,false).content![0]!.text})
 })
 it('F-1: a cancel that never left says it was not sent: an acknowledgement lost after requested, and an effect host that fails',async()=>{
  // An acknowledgement lost after `requested` is recorded: what a crash there leaves.
  const lost=await pair(),held=await started2(lost),call=cancelCall(held.job.job),ask=held.store.requestJobCancel.bind(held.store)
  vi.spyOn(held.store,'requestJobCancel').mockImplementationOnce(async(...args)=>{await ask(...args);throw Error('acknowledgement lost')})
  const first=await drive(held.view,[lost.start,lost.cancel],provider([call,{say:'x'}]).call,{resume:true})
  const job=(await readJob(held.store,RUN,held.job.job))!
  expect(lost.sent).not.toHaveBeenCalled();expect(job.cancel?.requested.by).toBe('tool')
  expect(toolResultOf(first)).toMatchObject({isError:true,text:jobCancelResult(job,false).content![0]!.text})
  // A repeat is answered from the job, and its note claims no send.
  const repeat=await drive(held.view,[lost.start,lost.cancel],provider([call,{say:'x'}]).call,{resume:true})
  expect(lost.sent).not.toHaveBeenCalled();expect(toolResultOf(repeat)?.text).toContain('may not have been delivered')
  // An effect host that fails before anything is handed to the host.
  const failing={on:false},clock={at:T0},store=new DurableJobStore({now:()=>new Date(clock.at)})
  const host=await certifyEffectHost(authority=>{const own=new ReferenceEffectHost(authority);return {effectFencing:'enforced',dispatch:async(admission,perform,signal)=>{if(failing.on)throw Error('the authority is down');return own.dispatch(admission,perform,signal)}} as EffectHost},store)
  const lease=await store.acquireLease({resource:RUN,owner:'worker-a',expectedRevision:null,durationMs:HOUR}),view=fencedRunStore(store,fenceOf(lease),host)
  const down=await pair()
  await drive(view,[down.start,down.cancel],provider([{call:{tool:'runner__start_job',arguments:{name:'nightly'}}},{say:'x'}]).call)
  const downJob=(await listJobs(store,RUN)).jobs[0]!;failing.on=true
  const events=await drive(view,[down.start,down.cancel],provider([cancelCall(downJob.job),{say:'x'}]).call,{resume:true})
  const after=(await readJob(store,RUN,downJob.job))!
  expect(down.sent).not.toHaveBeenCalled();expect(after.cancel?.requested.by).toBe('tool')
  expect(toolResultOf(events)).toMatchObject({isError:true,text:jobCancelResult(after,false).content![0]!.text})
 })
 it('F-1: a delegation that refuses at the counted read: the cancel was not sent, and the model is told so',async()=>{
  const BINDING={sub:chains.items[0]![0]!.sub,rootActor:chains.items[0]![0]!.act.agent,host:chains.items[0]![0]!.host} as PrincipalBinding
  const TT=Date.parse('2026-10-30T01:00:00.000Z'),store=new DurableJobStore({now:()=>new Date(TT)}),host=new ReferenceEffectHost(store)
  const lease=await store.acquireLease({resource:RUN,owner:'worker-a',expectedRevision:null,durationMs:HOUR,principal:BINDING}),view=unattendedRunStore({store,fence:fenceOf(lease),effectHost:host})
  const principal=(answer:()=>AdmissionAnswer=()=>({admission:'admit'})):RunPrincipal=>({chain:JSON.stringify(chains.items[0]),keys:chains.keys,software:chains.software,audience:chains.audience,clock:()=>TT,admission:()=>Promise.resolve(answer())} as never)
  // The vectors' chain admits source notes, tools write_note and read_note: the pair takes those names.
  const WS={name:'write_note',description:'Start one background job.',inputSchema:{type:'object'},taxonomy:{operation:'run',effect:'act'} as const},CS={name:'read_note',description:'Cancel one background job.',inputSchema:JOB_CANCEL_SCHEMA,taxonomy:{operation:'run',effect:'act'} as const}
  const notes={kind:'custom',id:'notes',trust:'person',version:'1',toolListDigest:await toolListDigest([WS,CS] as never),identity:{kind:'connection',label:'notes'}} as never
  const sent=vi.fn(async(_:JobCancelInput)=>undefined),start=jobStartTool({...WS,source:notes,approve:(()=>'allow') as never,mint:async()=>jobKeyDigest(KEY),start:started as never})
  const cancel=jobCancelTool({name:CS.name,description:CS.description,source:notes,start,approve:(()=>'allow') as never,cancel:sent})
  const go=async(p:RunPrincipal,call:(...a:never[])=>Promise<Response>,resume=false)=>{
   await laterTurn(view,{resume})
   const session={prompt:'Write.',testPrompt:'',tools:[],callTool:vi.fn(async()=>({content:[]})),hostTools:[start,cancel],signal:new AbortController().signal,allowConversation:true,critic:'off',context:{principal:p,currentRequest:{message:'Write.',input:'n'},runStore:{store:view,id:RUN,...(resume?{resume:true}:{})}},model:{family:'openai-compatible',model:'fixture',call}} as never as EngineSession
   const out:AssistantEvent[]=[];for await(const event of vercel.start(session))out.push(event);return out
  }
  await go(principal(),provider([{call:{tool:'notes__write_note',arguments:{name:'n'}}},{say:'x'}]).call)
  const job=(await listJobs(store,RUN)).jobs[0]!
  // The reads before the claim admit; the counted read, after `requested`, refuses.
  let nth=0
  const events=await go(principal(()=>{nth++;return nth>=3?({admission:'refuse',refusal:'revoked'} as never):{admission:'admit'}}),provider([{call:{tool:'notes__read_note',arguments:{job:job.job,generation:1}}},{say:'x'}]).call,true)
  const after=(await readJob(store,RUN,job.job))!
  expect(sent).not.toHaveBeenCalled();expect(after.cancel?.requested.by).toBe('tool')
  expect(toolResultOf(events)).toMatchObject({isError:true,text:jobCancelResult(after,false).content![0]!.text})
 })
 it('F-2: what is sent is the record of the job asked for: a store that answers another job sends nothing',async()=>{
  const tools=await pair({mint:'job.key.0001.Q7z2Xm9LpV4cR8tNb3Yh6Df1Gj5K'}),held=await leased()
  let n=0
  // One handle for both jobs, so that only the job's identity tells the answer from the record asked of (F-2b compares the rest).
  const two=jobStartTool({...START,source:tools.start.source!,approve:(()=>'allow') as never,mint:async()=>jobKeyDigest(n++===0?KEY:'job.key.0002.Hs8Pq3Lz6Wd1Rm9Tc4Vb7Nx2Ka5U'),start:(async()=>({started:true,handle:'H-SAME'})) as never})
  const cancel=jobCancelTool({...CANCEL,source:tools.start.source!,start:two,approve:(()=>'allow') as never,cancel:tools.sent})
  await drive(held.view,[two,cancel],provider([{call:{tool:'runner__start_job',arguments:{name:'one'}}},{call:{tool:'runner__start_job',arguments:{name:'two'}}},{say:'x'}]).call)
  const [first,second]=(await listJobs(held.store,RUN)).jobs
  const ask=held.store.requestJobCancel.bind(held.store)
  vi.spyOn(held.store,'requestJobCancel').mockImplementation(async(...args)=>{await ask(...args);return (await readJob(held.store,RUN,second!.job))!})
  const events=await drive(held.view,[two,cancel],provider([cancelCall(first!.job),{say:'x'}]).call,{resume:true})
  expect(tools.sent).not.toHaveBeenCalled()
  expect(toolResultOf(events)).toMatchObject({isError:true,text:expect.stringContaining('not sent')})
 })
 it('the host\'s word by key moves no state; job_read shows it; only a completion with cancelled says it stopped',async()=>{
  const tools=await pair(),held=await started2(tools)
  await drive(held.view,[tools.start,tools.cancel],provider([cancelCall(held.job.job),{say:'x'}]).call,{resume:true})
  await held.store.releaseLease(held.fence)
  const worded=await acknowledgeJobCancel(held.store,{version:1,resource:RUN,job:held.job.job,generation:1,evidence:'word-1',outcome:'accepted',key:KEY})
  expect(worded.state).toBe('started');expect(jobProjection(worded).cancel).toBe('accepted')
  const done=await completeJob(held.store,completion(held.job.job,{outcome:'cancelled'}))
  expect(done).toMatchObject({state:'completed',completion:{outcome:'cancelled'},cancel:{acknowledged:{outcome:'accepted'}}})
  expect(jobReportFromExport(await exportJobs(held.store,RUN)).entries.map(e=>e.decision)).toEqual(['register','start','cancel-requested','cancel-acknowledged','complete'])
 })
 it('a sub-agent is offered no cancel tool',async()=>{
  const tools=await pair()
  const admitted=await declareSubagents([{...structuredClone(declaration.items[0]),tools:[tools.cancel.name]} as unknown as SubagentDeclaration],[tools.cancel],[],CONTEXT_DEFAULTS)
  expect(admitted.subagents).toEqual([])
 })
})
describe('an abandon takes a fresh fence, and its cancels are stable (#229, M9)',()=>{
 const abandon=(held:Awaited<ReturnType<typeof started2>>,sent:(input:JobCancelInput)=>Promise<unknown>,command='cmd-1')=>abandonJobs({store:held.store,effectHost:held.host,resource:RUN,command,durationMs:HOUR,cancel:sent})
 it('the lease under the reserved owner at a new epoch; old workers get stale-epoch; no run commit; a cancel through the effect host; no policy asked',async()=>{
  const tools=await pair({approve:'refuse'}),held=await started2(tools),sent=vi.fn(async(_:JobCancelInput)=>undefined),before=(await held.store.load(RUN))!.revision
  const answer=await abandon(held,sent)
  expect(answer.fence).toMatchObject({owner:JOB_ABANDON_OWNER,resource:RUN});expect(Number(answer.fence!.epoch)).toBeGreaterThan(Number(held.fence.epoch))
  expect(answer.jobs).toEqual([{job:held.job.job,delivery:'sent'}])
  expect(sent).toHaveBeenCalledWith(expect.objectContaining({job:held.job.job,generation:1,handle:SECRET}))
  expect(tools.asked).not.toHaveBeenCalled()
  expect((await held.store.load(RUN))!.revision).toBe(before)
  await expect(held.store.commitFenced({runId:RUN,expectedRevision:before,commitId:'late',checkpoint:new TextEncoder().encode('{}'),events:[]},held.fence)).rejects.toMatchObject({name:'LeaseRefused',reason:'stale-epoch'})
  expect((await readJob(held.store,RUN,held.job.job))?.cancel?.requested).toMatchObject({by:'command',command:'cmd-1',owner:JOB_ABANDON_OWNER,epoch:answer.fence!.epoch,operation:jobCancelOperation('cmd-1',held.job.job)})
  expect((await readJob(held.store,RUN,held.job.job))?.state).toBe('started')
 })
 /** #325: a run with two started jobs, A under a marked digest and B under a plain handle, or B left in flight. */
 async function twoJobs(over:{completeA?:boolean;bInFlight?:boolean}={}){
  const held=await leased(),a=jobIdentity(RUN,'op-1'),b=jobIdentity(RUN,'op-2'),digest=`sha256:${'d'.repeat(64)}`,keyB='job.key.0002.Q7z2Xm9LpV4cR8tNb3Yh6Df1Gj5K'
  const claim=(job:string,operation:string,key:string,expectedRevision:string|null)=>held.store.commitFenced({runId:RUN,expectedRevision,commitId:`c-${operation}`,checkpoint:new TextEncoder().encode('{}'),events:[],job:{job,operation,source:'runner',tool:'start_job',requestDigest:`sha256:${'a'.repeat(64)}`,keyDigest:jobKeyDigest(key)}},held.fence)
  const first=await claim(a,'op-1',KEY,null);await claim(b,'op-2',keyB,first.revision)
  await held.store.answerJob({version:1,resource:RUN,job:a,generation:1,answer:'started',handle:digest,handleDigested:true},held.fence)
  // A completed A was asked a cancel first, so only its state tells an abandon not to send it.
  if(over.completeA){await held.store.requestJobCancel({version:1,resource:RUN,job:a,generation:1,by:'tool',operation:'cancel-a'},held.fence);await held.store.completeJob(completion(a))}
  if(!over.bInFlight)await held.store.answerJob({version:1,resource:RUN,job:b,generation:1,answer:'started',handle:'kit-handle-b'},held.fence)
  return {...held,a,b,digest,sent:vi.fn(async(_:JobCancelInput)=>undefined)}
 }
 it('#325: an abandon decides each job from its record: one whose answer the record does not bear out is sent nothing, and the others still go',async()=>{
  type Held=Awaited<ReturnType<typeof twoJobs>>
  const faults:[string,{answer?:(job:RunJob,held:Held)=>RunJob;read?:(held:Held)=>Promise<RunJob|undefined>;completeA?:boolean},JobDelivery][]=[
   ['the record',{},'sent'],
   ['drops the marker',{answer:({handleDigested:_,...job})=>job},'invalid'],
   ['another handle',{answer:job=>({...job,handle:`sha256:${'f'.repeat(64)}`})},'invalid'],
   ['another source',{answer:job=>({...job,source:'kit-other-source'})},'invalid'],
   ['another tool',{answer:job=>({...job,tool:'kit-other-tool'})},'invalid'],
   // Another job's record, bearing the answer out in all else: only its identity tells it apart.
   ['a record of another job',{read:async held=>{const other=(await MemoryRunStore.prototype.readJob.call(held.store,RUN,held.b))!;return {...other,handle:held.digest,handleDigested:true}}},'invalid'],
   ['no record',{read:async()=>undefined},'invalid'],
   // LOW-1: an answer that calls a completed job started sends it nothing: its record says it has settled.
   ['a completed job called started',{completeA:true,answer:({completion:_,closed:__,...job})=>({...job,state:'started'})},'settled'],
  ]
  for(const [name,fault,delivery] of faults){
   const held=await twoJobs({completeA:fault.completeA}),abandonOf=held.store.abandonJobs.bind(held.store),readOf=held.store.readJob.bind(held.store)
   held.store.abandonJobs=async(input,signal)=>{const taken=await abandonOf(input,signal);return {...taken,jobs:taken.jobs.map(job=>job.job===held.a&&fault.answer?fault.answer(job,held):job)}}
   held.store.readJob=async(resource,job,signal)=>job===held.a&&fault.read?fault.read(held):readOf(resource,job,signal)
   const answer=await abandonJobs({store:held.store,effectHost:held.host,resource:RUN,command:'cmd-1',durationMs:HOUR,cancel:held.sent})
   expect(answer.jobs,name).toEqual([{job:held.a,delivery},{job:held.b,delivery:'sent'}])
   expect(held.sent,name).toHaveBeenCalledWith(expect.objectContaining({job:held.b,handle:'kit-handle-b'}))
   if(delivery==='sent'){expect(held.sent).toHaveBeenCalledTimes(2);expect(held.sent).toHaveBeenCalledWith(expect.objectContaining({job:held.a,handleDigest:held.digest}));expect(held.sent.mock.calls.find(([input])=>input.job===held.a)![0]).not.toHaveProperty('handle')}
   else expect(held.sent,name).toHaveBeenCalledTimes(1)
  }
 })
 it('#325 MEDIUM-1: a start that answers between the store\'s transaction and the read-back is decided by its record, and its cancel is asked and sent',async()=>{
  const held=await twoJobs({bInFlight:true}),abandonOf=held.store.abandonJobs.bind(held.store)
  let unproven:string|undefined
  held.store.abandonJobs=async(input,signal)=>{
   const taken=await abandonOf(input,signal);unproven=taken.jobs.find(job=>job.job===held.b)?.state
   // A stale worker's start answers under its own fence, as M2 lets it.
   await held.store.answerJob({version:1,resource:RUN,job:held.b,generation:1,answer:'started',handle:'kit-handle-b'},held.fence)
   return taken
  }
  const answer=await abandonJobs({store:held.store,effectHost:held.host,resource:RUN,command:'cmd-1',durationMs:HOUR,cancel:held.sent})
  expect(unproven).toBe('unproven')
  expect(answer.jobs).toEqual([{job:held.a,delivery:'sent'},{job:held.b,delivery:'sent'}])
  expect(held.sent).toHaveBeenCalledTimes(2);expect(held.sent).toHaveBeenCalledWith(expect.objectContaining({job:held.b,handle:'kit-handle-b',operation:jobCancelOperation('cmd-1',held.b)}))
  expect((await held.store.readJob(RUN,held.b))?.cancel?.requested).toMatchObject({by:'command',command:'cmd-1',owner:JOB_ABANDON_OWNER,epoch:answer.fence!.epoch,operation:jobCancelOperation('cmd-1',held.b)})
  // A retry of the command sends nothing twice.
  held.store.abandonJobs=abandonOf
  const again=await abandonJobs({store:held.store,effectHost:held.host,resource:RUN,command:'cmd-1',durationMs:HOUR,cancel:held.sent})
  expect(again.jobs).toEqual([{job:held.a,delivery:'already-admitted'},{job:held.b,delivery:'already-admitted'}]);expect(held.sent).toHaveBeenCalledTimes(2)
 })
 it('#325 MEDIUM-1: a late start whose asked cancel the store answers with another handle is sent nothing; the others still go',async()=>{
  const held=await twoJobs({bInFlight:true}),abandonOf=held.store.abandonJobs.bind(held.store),askOf=held.store.requestJobCancel.bind(held.store)
  held.store.abandonJobs=async(input,signal)=>{const taken=await abandonOf(input,signal);await held.store.answerJob({version:1,resource:RUN,job:held.b,generation:1,answer:'started',handle:'kit-handle-b'},held.fence);return taken}
  held.store.requestJobCancel=async(ask,fence,signal)=>{const asked=await askOf(ask,fence,signal);return ask.job===held.b?{...asked,handle:'kit-other-handle'}:asked}
  const answer=await abandonJobs({store:held.store,effectHost:held.host,resource:RUN,command:'cmd-1',durationMs:HOUR,cancel:held.sent})
  expect(answer.jobs).toEqual([{job:held.a,delivery:'sent'},{job:held.b,delivery:'invalid'}])
  expect(held.sent).toHaveBeenCalledTimes(1);expect(held.sent).toHaveBeenCalledWith(expect.objectContaining({job:held.a}))
 })
 it('a retry of the command sends nothing twice: the effect host admits its stable operation once',async()=>{
  const tools=await pair(),held=await started2(tools),sent=vi.fn(async(_:JobCancelInput)=>undefined)
  await abandon(held,sent)
  const again=await abandon(held,sent)
  expect(sent).toHaveBeenCalledTimes(1);expect(again.jobs).toEqual([{job:held.job.job,delivery:'already-admitted'}])
 })
 it('past a deadline no lease can be granted: nothing is sent, and the record and the answer say undelivered',async()=>{
  const tools=await pair(),held=await started2(tools),sent=vi.fn(async(_:JobCancelInput)=>undefined)
  await held.store.limitRun({version:1,resource:RUN,run:iso(T0+MINUTE)});held.clock.at=T0+MINUTE
  const answer=await abandon(held,sent)
  expect(answer.fence).toBeUndefined();expect(answer.jobs).toEqual([{job:held.job.job,delivery:'undelivered'}]);expect(sent).not.toHaveBeenCalled()
  expect((await readJob(held.store,RUN,held.job.job))?.cancel?.requested.undelivered).toBe(true)
 })
 it('what has no handle, has settled, or has the host\'s acceptance is not sent; a digest is never the reference; a failed cancel says so',async()=>{
  const none=await pair({start:async()=>({content:[]})}),lost=await started2(none),sent=vi.fn(async(_:JobCancelInput)=>undefined)
  expect((await abandon(lost,sent)).jobs).toEqual([{job:lost.job.job,delivery:'no-handle'}]);expect(sent).not.toHaveBeenCalled()
  const settled=await pair(),done=await started2(settled)
  await completeJob(done.store,completion(done.job.job))
  expect((await abandon(done,sent)).jobs).toEqual([{job:done.job.job,delivery:'settled'}]);expect(sent).not.toHaveBeenCalled()
  const denied=await pair(),word2=await started2(denied)
  await reconcileJob(word2.store,reconciliation(word2.job.job,{evidence:'find-x',finding:'unknown'}))
  await word2.store.reconcileJob(reconciliation(word2.job.job,{evidence:'find-y',finding:'started',handle:SECRET}))
  expect((await abandon(word2,sent)).jobs[0]!.delivery).toBe('sent');sent.mockClear()
  const closed=await pair({start:async()=>({started:false})}),shut=await started2(closed)
  await completeJob(shut.store,completion(shut.job.job))
  expect((await abandon(shut,sent)).jobs).toEqual([{job:shut.job.job,delivery:'contradicted'}]);expect(sent).not.toHaveBeenCalled()
  const accepted=await pair(),word=await started2(accepted)
  await drive(word.view,[accepted.start,accepted.cancel],provider([cancelCall(word.job.job),{say:'x'}]).call,{resume:true})
  await acknowledgeJobCancel(word.store,{version:1,resource:RUN,job:word.job.job,generation:1,evidence:'word-1',outcome:'accepted',key:KEY})
  expect((await abandon(word,sent)).jobs).toEqual([{job:word.job.job,delivery:'acknowledged'}]);expect(sent).not.toHaveBeenCalled()
  const long=await pair({start:async()=>({started:true,handle:'h'.repeat(300)})}),digested=await started2(long)
  await abandon(digested,sent)
  expect(sent.mock.calls[0]![0]).toMatchObject({handleDigest:`sha256:${sha256('h'.repeat(300))}`});expect(sent.mock.calls[0]![0]).not.toHaveProperty('handle')
  const failing=await pair(),broke=await started2(failing)
  expect((await abandon(broke,async()=>{throw Error('down')})).jobs).toEqual([{job:broke.job.job,delivery:'failed'}])
 })
 it('an abandon holds its store to the reserved owner: a lease of another owner is refused, and nothing is sent',async()=>{
  const tools=await pair(),held=await started2(tools),sent=vi.fn(async(_:JobCancelInput)=>undefined),abandoned=held.store.abandonJobs.bind(held.store)
  vi.spyOn(held.store,'abandonJobs').mockImplementation(async(...args)=>{const taken=await abandoned(...args);return {...taken,lease:{...taken.lease!,owner:'worker-z'}}})
  await expect(abandon(held,sent)).rejects.toMatchObject({name:'LeaseRefused',reason:'invalid'});expect(sent).not.toHaveBeenCalled()
 })
 it('an effect host that only declares enforced is refused before anything moves',async()=>{
  const tools=await pair(),held=await started2(tools),declared={effectFencing:'enforced',dispatch:async(_:unknown,perform:()=>Promise<unknown>)=>perform()} as unknown as EffectHost
  await expect(abandonJobs({store:held.store,effectHost:declared,resource:RUN,command:'c',durationMs:HOUR,cancel:async()=>undefined})).rejects.toBeInstanceOf(LeaseCapabilityRefused)
  expect((await held.store.readLease(RUN))?.epoch).toBe(held.fence.epoch)
 })
})
describe('what is refused before an offer or a park (#229)',()=>{
 it('M-2: a view with no effect host, or one that only declares enforced, reaches no offer, no row and no start',async()=>{
  for(const host of [undefined,{effectFencing:'enforced',dispatch:async(_:unknown,perform:()=>Promise<unknown>)=>perform()} as unknown as EffectHost]){
   const store=new DurableJobStore({now:()=>new Date(T0)}),lease=await store.acquireLease({resource:RUN,owner:'worker-a',expectedRevision:null,durationMs:HOUR})
   const start=vi.fn(started),first=provider([CALL,{say:'x'}])
   const events=await drive(fencedRunStore(store,fenceOf(lease),host),[await startTool(start)],first.call)
   expect(JSON.stringify(events)).toContain('LeaseCapabilityRefused: effect-host-unfenced')
   expect(start).not.toHaveBeenCalled();expect(first.state.requests).toBe(0)
   expect((await store.exportJobs(RUN)).jobs).toEqual([])
  }
 })
 it('M-4: a run at its request limit does not park: a claim would be spent and nothing could continue',async()=>{
  const held=await leased(),tool=await startTool(started)
  const session={prompt:'Run the job.',testPrompt:'',tools:[],callTool:vi.fn(async()=>({content:[]})),hostTools:[tool],signal:new AbortController().signal,allowConversation:true,critic:'off',model:{family:'openai-compatible',model:'fixture',call:provider([CALL,{say:'running'}]).call},context:{requestCap:2,currentRequest:{message:'Run the job.',input:'job'},runStore:{store:held.view,id:RUN}}} as unknown as EngineSession
  for await(const _ of vercel.start(session)){}
  const job=(await listJobs(held.store,RUN)).jobs[0]!
  await expect(parkJob(held.store,held.fence,job.job)).rejects.toEqual(waitRefusal('not-resumable'))
  expect((await exportWaits(held.store,RUN)).waits).toEqual([])
  expect((await held.store.readLease(RUN))?.state).toBe('held')
 })
 it('M-5: a run under a stored chat does not park: its chat could not take the continuation',async()=>{
  const held=await leased(),scope=new StoredChatScope(held.view as never,'chat-1',new ChatContext({scope:'fixture'}))
  const start=vi.fn(started),first=provider([CALL,{say:'running'}])
  const session={prompt:'Run the job.',testPrompt:'',tools:[],callTool:vi.fn(async()=>({content:[]})),hostTools:[await startTool(start)],signal:new AbortController().signal,allowConversation:true,critic:'off',context:{currentRequest:{message:'Run the job.',input:'job'},chat:scope,runStore:{store:held.view,id:RUN,context:scope}},model:{family:'openai-compatible',model:'fixture',call:first.call}} as unknown as EngineSession
  const out:AssistantEvent[]=[];for await(const event of vercel.start(session))out.push(event)
  // R-2: refused before any offer, so nothing tells the model the host resumes it.
  expect(JSON.stringify(out)).toContain('JobCapabilityRefused: chat-store-unsupported')
  expect(start).not.toHaveBeenCalled();expect(first.state.requests).toBe(0);expect((await listJobs(held.store,RUN)).jobs).toEqual([])
 })
 it('R-2: a head that holds a stored chat is refused with no scope passed',async()=>{
  const held=await leased()
  vi.spyOn(held.store,'readHistoryHead').mockResolvedValue({context:{chatId:'chat-1'}} as never)
  const writer=new RunWriter(unattendedRunStore({store:held.store,fence:held.fence,effectHost:held.host}),RUN)
  await expect(writer.attachJobs()).rejects.toMatchObject({name:'JobCapabilityRefused',reason:'chat-store-unsupported'})
 })
 it('M-5: a park reads the history head as well: a job from before the chat cannot wait under it',async()=>{
  const held=await oneJob()
  const chatted=Object.create(held.store,{readHistoryHead:{value:async()=>({context:{chatId:'chat-1'}})}}) as RunStore
  await expect(parkJob(chatted,held.fence,held.job.job)).rejects.toMatchObject({name:'WaitCapabilityRefused',reason:'chat-store-unsupported'})
  expect((await exportWaits(held.store,RUN)).waits).toEqual([])
 })
 it('M-6: a job with no row in the head means jobs and rows from different moments: refused before anything is offered',async()=>{
  const held=await leased(),start=vi.fn(started),tool=await startTool(start)
  await drive(held.view,[tool],provider([{say:'nothing yet'}]).call)
  const head=(await held.store.load(RUN))!,lost=jobIdentity(RUN,'op-restored-away')
  await held.store.commitFenced({runId:RUN,expectedRevision:head.revision,commitId:'restored',checkpoint:head.checkpoint,events:[],job:{job:lost,operation:'op-restored-away',source:'runner',tool:'start_job',requestDigest:`sha256:${'a'.repeat(64)}`,keyDigest:jobKeyDigest(KEY)}},held.fence)
  const repeat=provider([CALL,{say:'x'}]),events=await drive(held.view,[tool],repeat.call,{resume:true})
  expect(JSON.stringify(events)).toContain('JobCapabilityRefused: store-inconsistent')
  expect(start).not.toHaveBeenCalled();expect(repeat.state.requests).toBe(0)
  expect((await held.store.exportJobs(RUN)).jobs).toHaveLength(1)
 })
 it('M-8: a sub-agent is offered no job tool: the declaration is withheld, and a child session is refused by name',async()=>{
  const held=await leased(),read=jobReadTool(held.view),start=await startTool(started)
  for(const tool of [read,start]){
   const definition={...structuredClone(declaration.items[0]),tools:[tool.name]} as unknown as SubagentDeclaration
   const admitted=await declareSubagents([definition],[tool],[],CONTEXT_DEFAULTS)
   expect(admitted.subagents).toEqual([])
   expect(admitted.withheld).toEqual([expect.objectContaining({detail:'A sub-agent is offered no job tool: a job belongs to the run that started it.'})])
  }
  const session={prompt:'Read.',testPrompt:'',tools:[],callTool:vi.fn(async()=>({content:[]})),hostTools:[read],signal:new AbortController().signal,allowConversation:true,critic:'off',childDefinition:{id:'child'},context:{currentRequest:{message:'Read.',input:'r'},runStore:{store:held.view,id:RUN}},model:{family:'openai-compatible',model:'fixture',call:provider([{say:'x'}]).call}} as unknown as EngineSession
  const out:AssistantEvent[]=[];for await(const event of vercel.start(session))out.push(event)
  expect(JSON.stringify(out)).toContain('JobCapabilityRefused: child-unsupported')
 })
})
describe('capability and what stays unchanged (#229)',()=>{
 it('job tools need a fenced store whose jobs outlive its process, and an effect host; a gateway start needs the key scope',async()=>{
  const memory=new MemoryRunStore(),lease=await memory.acquireLease({resource:RUN,owner:'a',expectedRevision:null,durationMs:HOUR})
  expect(()=>jobReadTool(fencedRunStore(memory,fenceOf(lease),new ReferenceEffectHost(memory)))).toThrow(expect.objectContaining({name:'JobCapabilityRefused',reason:'store-lifetime'}))
  expect(()=>jobReadTool(memory)).toThrow(expect.objectContaining({name:'JobCapabilityRefused',reason:'store-unsupported'}))
  expect(()=>admitJobs({store:memory})).toThrow(expect.objectContaining({name:'JobCapabilityRefused',reason:'store-lifetime'}))
  const durable=new DurableJobStore()
  expect(()=>admitJobs({store:durable})).toThrow(LeaseCapabilityRefused)
  expect(()=>admitJobs({store:durable,effectHost:new ReferenceEffectHost(durable)})).not.toThrow()
  expect(()=>admitJobs({store:{durability:'host',load:async()=>undefined,commit:async()=>{throw Error()},readEvents:async()=>{throw Error()}} as never})).toThrow(expect.objectContaining({name:'JobCapabilityRefused',reason:'store-unsupported'}))
  expect(()=>jobStartTool({...START,source:{kind:'gateway',id:'g'} as never,mint:async()=>'',start:async()=>({started:false})})).toThrow(expect.objectContaining({reason:'job-host-unsupported'}))
  expect(()=>jobStartTool({...START,source:{kind:'gateway',id:'g'} as never,gatewayKeyScope:true,mint:async()=>'',start:async()=>({started:false})})).not.toThrow()
 })
 it('a run offered a job tool with no durable store refuses by name, before any request',async()=>{
  const tool=await startTool(started),call=vi.fn(async()=>{throw Error('no request expected')})
  const session:EngineSession={prompt:'Run the job.',testPrompt:'',tools:[],callTool:vi.fn(async()=>({content:[]})),hostTools:[tool],signal:new AbortController().signal,allowConversation:true,critic:'off',context:{currentRequest:{message:'Run the job.',input:'job'}},model:{family:'openai-compatible',model:'fixture',call}} as unknown as EngineSession
  const out:AssistantEvent[]=[];for await(const event of vercel.start(session))out.push(event)
  expect(call).not.toHaveBeenCalled()
  expect(JSON.stringify(out)).toContain('JobCapabilityRefused: store-unsupported')
 })
 it('with no job tool offered, a store with the job port is never asked about jobs and no commit carries one',async()=>{
  const held=await leased(),commits=vi.spyOn(held.store,'commitFenced'),history=vi.spyOn(held.store,'commitHistoryFenced')
  const spies=(['answerJob','reconcileJob','completeJob','reclaimJob','readJob','listJobs','exportJobs'] as const).map(name=>vi.spyOn(held.store,name))
  const act={...START,name:'plain',effectClass:'act',source:{kind:'custom',id:'runner',trust:'person',version:'1',toolListDigest:await toolListDigest([{...START,name:'plain'}] as never),identity:{kind:'connection',label:'runner'}},approve:()=>'allow',execute:async()=>({content:[{type:'text',text:'ok'}]})} as unknown as HostTool
  const first=provider([{call:{tool:'runner__plain',arguments:{name:'nightly'}}},{say:'done'}])
  await drive(held.view,[act],first.call)
  for(const spy of spies)expect(spy).not.toHaveBeenCalled()
  for(const call of [...commits.mock.calls,...history.mock.calls.map(c=>[(c[0] as {run:unknown}).run])])expect(call[0]).not.toHaveProperty('job')
  expect((await exportJobs(held.store,RUN)).jobs).toEqual([])
 })
})

/* The batch of #303 and #310: what the reviews of the job core and of cancel left. */
/** An effect host that performs and then refuses, as a host that re-checks after `perform` would. */
async function refusingAfter(store:DurableJobStore,refusal:()=>Error){
 const armed={on:false}
 const host=await certifyEffectHost(authority=>{const own=new ReferenceEffectHost(authority);return {effectFencing:'enforced',dispatch:async(admission,perform,signal)=>{const out=await own.dispatch(admission,perform,signal);if(armed.on)throw refusal();return out}} as EffectHost},store)
 return {host,armed}
}
describe('the batch: a host that refuses after perform, and the cancel\'s answer (#303, #310)',()=>{
 for(const [name,refusal] of [['LeaseRefused',()=>new LeaseRefused('stale-epoch')],['JobRefused',()=>new JobRefused('invalid')],['an authored input refusal',()=>new AIFailureError({...failure('invalid-tool-input','tool','not-started'),detail:'SENTINEL-AUTHORED'})]] as const)it(`R-4: a start whose effect host refuses after it ran keeps its answer and reads as ran (${name})`,async()=>{
  const clock={at:T0},store=new DurableJobStore({now:()=>new Date(clock.at)}),{host,armed}=await refusingAfter(store,refusal)
  const view=fencedRunStore(store,fenceOf(await store.acquireLease({resource:RUN,owner:'worker-a',expectedRevision:null,durationMs:HOUR})),host)
  const start=vi.fn(started),tool=await startTool(start);armed.on=true
  const events=await drive(view,[tool],provider([CALL,{say:'x'}]).call)
  const job=(await listJobs(store,RUN)).jobs[0]!,row=(await headRows(store)).dispatches.find(d=>d.id===job.operation)!
  expect(start).toHaveBeenCalledTimes(1);expect(job.state).toBe('started');expect(job.handle).toBe(SECRET)
  expect(row.state).toBe('unknown');expect(row.failure?.detail).toBe(JOB_START_UNRECORDED)
  expect(toolResultOf(events)?.text).toBe(jobStartResult(job).content![0]!.text)
  expect(JSON.stringify(events)).not.toContain('SENTINEL-AUTHORED')
 })
 it('F-17: a cancel whose effect host refuses after it was handed over reads as maybe sent, never as not started',async()=>{
  const clock={at:T0},store=new DurableJobStore({now:()=>new Date(clock.at)}),{host,armed}=await refusingAfter(store,()=>new LeaseRefused('stale-epoch'))
  const view=fencedRunStore(store,fenceOf(await store.acquireLease({resource:RUN,owner:'worker-a',expectedRevision:null,durationMs:HOUR})),host)
  const tools=await pair()
  await drive(view,[tools.start,tools.cancel],provider([{call:{tool:'runner__start_job',arguments:{name:'nightly'}}},{say:'x'}]).call)
  const job=(await listJobs(store,RUN)).jobs[0]!;armed.on=true
  const events=await drive(view,[tools.start,tools.cancel],provider([cancelCall(job.job),{say:'x'}]).call,{resume:true})
  const after=(await readJob(store,RUN,job.job))!,row=(await headRows(store)).dispatches.find(d=>d.id===after.cancel!.requested.operation)!
  expect(tools.sent).toHaveBeenCalledTimes(1);expect(row.state).toBe('unknown');expect(row.failure?.detail).toBe(JOB_CANCEL_UNRECORDED)
  expect(toolResultOf(events)?.text).toBe(jobCancelResult(after).content![0]!.text)
 })
 it('F-20: an effect host that refuses with LeaseRefused before anything was handed over: the cancel was not sent',async()=>{
  const failing={on:false},clock={at:T0},store=new DurableJobStore({now:()=>new Date(clock.at)})
  const host=await certifyEffectHost(authority=>{const own=new ReferenceEffectHost(authority);return {effectFencing:'enforced',dispatch:async(admission,perform,signal)=>{if(failing.on)throw new LeaseRefused('other-owner');return own.dispatch(admission,perform,signal)}} as EffectHost},store)
  const view=fencedRunStore(store,fenceOf(await store.acquireLease({resource:RUN,owner:'worker-a',expectedRevision:null,durationMs:HOUR})),host)
  const tools=await pair()
  await drive(view,[tools.start,tools.cancel],provider([{call:{tool:'runner__start_job',arguments:{name:'nightly'}}},{say:'x'}]).call)
  const job=(await listJobs(store,RUN)).jobs[0]!;failing.on=true
  const events=await drive(view,[tools.start,tools.cancel],provider([cancelCall(job.job),{say:'x'}]).call,{resume:true})
  const after=(await readJob(store,RUN,job.job))!
  expect(tools.sent).not.toHaveBeenCalled()
  expect(toolResultOf(events)).toMatchObject({isError:true,text:jobCancelResult(after,false).content![0]!.text})
 })
 // The marker case: a handle kept as its digest, answered without its marker, would go out as the reference.
 const dropMarker=(asked:RunJob):RunJob=>{const {handleDigested:_,...rest}=asked;return rest}
 for(const [what,firstHandle,alter] of [['another job\'s handle','H-ONE',(asked:RunJob):RunJob=>({...asked,handle:'H-TWO'})],['its digest marker dropped','L'.repeat(300),dropMarker],['another source','H-ONE',(asked:RunJob):RunJob=>({...asked,source:'other-source'})],['another tool','H-ONE',(asked:RunJob):RunJob=>({...asked,tool:'other-tool'})]] as const)it(`F-2b: a store that answers the job asked for with ${what} sends nothing`,async()=>{
  const tools=await pair(),held=await leased()
  let n=0
  const two=jobStartTool({...START,source:tools.start.source!,approve:(()=>'allow') as never,mint:async()=>jobKeyDigest(n++===0?KEY:'job.key.0002.Hs8Pq3Lz6Wd1Rm9Tc4Vb7Nx2Ka5U'),start:(async()=>({started:true,handle:n===1?firstHandle:'H-TWO'})) as never})
  const cancel=jobCancelTool({...CANCEL,source:tools.start.source!,start:two,approve:(()=>'allow') as never,cancel:tools.sent})
  await drive(held.view,[two,cancel],provider([{call:{tool:'runner__start_job',arguments:{name:'one'}}},{call:{tool:'runner__start_job',arguments:{name:'two'}}},{say:'x'}]).call)
  const first=(await listJobs(held.store,RUN)).jobs.find(job=>job.handle!=='H-TWO')
  expect(first?.handleDigested).toBe(firstHandle==='H-ONE'?undefined:true)
  const ask=held.store.requestJobCancel.bind(held.store)
  vi.spyOn(held.store,'requestJobCancel').mockImplementation(async(...args)=>alter(await ask(...args)))
  const events=await drive(held.view,[two,cancel],provider([cancelCall(first!.job),{say:'x'}]).call,{resume:true})
  expect(tools.sent).not.toHaveBeenCalled()
  expect(toolResultOf(events)).toMatchObject({isError:true,text:expect.stringContaining('not sent')})
 })
 it('F-3: `cancel` is handed the identity it is admitted under: its row\'s, or the abandon\'s operation',async()=>{
  const seen:JobCancelInput[]=[],tools=await pair({cancel:async input=>{seen.push(input)}}),held=await started2(tools)
  await drive(held.view,[tools.start,tools.cancel],provider([cancelCall(held.job.job),{say:'x'}]).call,{resume:true})
  const job=(await readJob(held.store,RUN,held.job.job))!
  expect(seen[0]!.operation).toBe(job.cancel!.requested.operation)
  const other=await pair(),again=await started2(other),sent=vi.fn(async(_:JobCancelInput)=>undefined)
  await abandonJobs({store:again.store,effectHost:again.host,resource:RUN,command:'cmd-op',durationMs:HOUR,cancel:sent})
  expect(sent.mock.calls[0]![0].operation).toBe(jobCancelOperation('cmd-op',again.job.job))
 })
 it('F-14: an abandon\'s cancel is admitted as the abandon\'s, not as its start',async()=>{
  const tools=await pair(),held=await started2(tools),admitted:EffectOperation[]=[]
  const host=await certifyEffectHost(authority=>{const own=new ReferenceEffectHost(authority);return {effectFencing:'enforced',dispatch:async(admission,perform,signal)=>{admitted.push(admission.operation);return own.dispatch(admission,perform,signal)}} as EffectHost},held.store)
  admitted.length=0
  await abandonJobs({store:held.store,effectHost:host,resource:RUN,command:'cmd-name',durationMs:HOUR,cancel:async()=>undefined})
  expect(admitted).toEqual([expect.objectContaining({tool:JOB_ABANDON_OWNER,source:held.job.source,operation:jobCancelOperation('cmd-name',held.job.job)})])
 })
 it('F-13: a contradicted job is called contradicted by the cancel tool, as the abandon calls it',async()=>{
  const tools=await pair({start:async()=>({started:false})}),held=await started2(tools)
  await completeJob(held.store,completion(held.job.job))
  const events=await drive(held.view,[tools.start,tools.cancel],provider([cancelCall(held.job.job),{say:'x'}]).call,{resume:true})
  expect(JSON.stringify(events)).toContain('JobRefused: job-contradicted')
  const answer=await abandonJobs({store:held.store,effectHost:held.host,resource:RUN,command:'cmd-c',durationMs:HOUR,cancel:async()=>undefined})
  expect(answer.jobs).toEqual([{job:held.job.job,delivery:'contradicted'}])
 })
 it('F-5: a found start\'s long handle is kept as its digest and marked, never sent as the reference, and its late answer agrees',async()=>{
  const long='L'.repeat(300),answer=vi.fn(async():Promise<JobStartAnswer>=>{throw Error('lost')})
  const tools=await pair({start:answer as never}),held=await started2(tools)
  expect(held.job.state).toBe('unproven')
  const found=await reconcileJob(held.store,{...reconciliation(held.job.job),finding:'started',handle:long})
  expect(found).toMatchObject({state:'started',handle:`sha256:${sha256(long)}`,handleDigested:true})
  const seen:JobCancelInput[]=[]
  await abandonJobs({store:held.store,effectHost:held.host,resource:RUN,command:'cmd-f5',durationMs:HOUR,cancel:async input=>{seen.push(input)}})
  expect(seen[0]).toMatchObject({handleDigest:`sha256:${sha256(long)}`});expect(seen[0]).not.toHaveProperty('handle')
  // The start's own late answer of the same bytes reads the same: no conflict.
  const late=await held.store.answerJob({version:1,resource:RUN,job:held.job.job,generation:1,answer:'started',handle:`sha256:${sha256(long)}`,handleDigested:true},held.fence)
  expect(late.handleDigested).toBe(true)
 })
 it('F-9, F-10: an abandon the store refuses moves no lease, and one on a run it has never seen takes none',async()=>{
  const held=await leased(),before=await held.store.readLease(RUN)
  await expect(held.store.abandonJobs({version:1,resource:RUN,command:'',durationMs:HOUR})).rejects.toMatchObject({name:'JobRefused',reason:'invalid'})
  expect(await held.store.readLease(RUN)).toEqual(before)
  const unseen=await held.store.abandonJobs({version:1,resource:'run-never',command:'cmd-1',durationMs:HOUR})
  expect(unseen).toEqual({jobs:[]});expect(await held.store.readLease('run-never')).toBeUndefined()
  await expect(held.store.acquireLease({resource:'run-never',owner:'worker-a',expectedRevision:null,durationMs:HOUR})).resolves.toMatchObject({owner:'worker-a'})
 })
 it('#316 review, MEDIUM-3: a run leased before its first commit is a run seen: its abandon takes the lease and fences the worker\'s first claim',async()=>{
  const held=await leased(),job=jobIdentity(RUN,'op-1')
  const taken=await held.store.abandonJobs({version:1,resource:RUN,command:'cmd-stop',durationMs:HOUR})
  expect(taken).toEqual({lease:expect.objectContaining({owner:JOB_ABANDON_OWNER,epoch:'2',state:'held'}),jobs:[]})
  await expect(held.store.commitFenced({runId:RUN,expectedRevision:null,commitId:'c1',checkpoint:new TextEncoder().encode('{}'),events:[],job:{job,operation:'op-1',source:'runner',tool:'start_job',requestDigest:`sha256:${'a'.repeat(64)}`,keyDigest:jobKeyDigest(KEY)}},held.fence)).rejects.toMatchObject({name:'LeaseRefused',reason:'stale-epoch'})
  expect(await held.store.load(RUN)).toBeUndefined();expect(await held.store.readJob(RUN,job)).toBeUndefined()
 })
 it('F-11: a head restored from before a cancel\'s row is refused before anything is offered: the cancel would be asked, and sent, again',async()=>{
  const tools=await pair(),held=await started2(tools)
  const before=(await held.store.load(RUN))!
  await drive(held.view,[tools.start,tools.cancel],provider([cancelCall(held.job.job),{say:'x'}]).call,{resume:true})
  expect(tools.sent).toHaveBeenCalledTimes(1)
  const now=(await held.store.load(RUN))!
  await held.store.commitFenced({runId:RUN,expectedRevision:now.revision,commitId:'restored',checkpoint:before.checkpoint,events:[]},held.fence)
  const repeat=provider([cancelCall(held.job.job),{say:'x'}]),events=await drive(held.view,[tools.start,tools.cancel],repeat.call,{resume:true})
  expect(JSON.stringify(events)).toContain('JobCapabilityRefused: store-inconsistent')
  expect(tools.sent).toHaveBeenCalledTimes(1);expect(repeat.state.requests).toBe(0)
 })
 it('R-7: an empty receipt is no receipt, as Go\'s zero value is',()=>{
  expect(readJobStartAnswer({started:true,handle:'h',receipt:''})).toEqual({started:true,handle:'h'})
 })
})
