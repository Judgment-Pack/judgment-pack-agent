import {describe,it,expect,vi} from 'vitest'
import type {AssistantEvent,EngineSession,HostTool} from '../engine'
import {vercel} from '../engines/vercel'
import {answerWith} from '../testing/scenarioDriver'
import {toolListDigest} from '../toolSources'
import {canonicalJSON} from '../context/chat'
import {evidenceFailure,availablePlanEvidence} from '../execution/planActions'
import {planDigest} from '../execution/plan'
import {readExecution} from '../execution/state'
import {MemoryRunStore,type RunStore} from './run'
import {LeaseCapabilityRefused,ReferenceEffectHost,fenceOf,fencedRunStore,reauthorizationOf,unattendedRunStore,type EffectHost,type LeaseLifetime} from './lease'
import {admitJobs,completeJob,exportJobs,jobIdentity,jobKeyDigest,jobProjection,jobReadTool,jobReport,jobStartResult,jobStartTool,listJobs,readJob,reconcileJob,type JobAnswer,type JobCompletionEvidence,type JobLifetime,type JobReconciliation,type JobRegistration,type JobStartAnswer,type RunJob} from './job'
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
async function drive(store:RunStore,tools:HostTool[],call:(...args:never[])=>Promise<Response>,extra:{resume?:boolean;approval?:{callId:string;approved:boolean};jobContinuation?:RunJob;signal?:AbortSignal}={}):Promise<AssistantEvent[]>{
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
/** What a decision on a held start must carry: nothing while #277's member is not on this branch. */
const HELD_PROOF=(_asked:unknown):Record<string,never>=>({})
vi.setConfig({testTimeout:120_000})

describe('a start is registered with its claim, before it is admitted (#229)',()=>{
 it('the job exists, unproven, when the effect host is asked; the row and the model hold only what the package composed',async()=>{
  const clock={at:T0},store=new DurableJobStore({now:()=>new Date(clock.at)}),inner=new ReferenceEffectHost(store)
  const lease=await store.acquireLease({resource:RUN,owner:'worker-a',expectedRevision:null,durationMs:HOUR}),fence=fenceOf(lease)
  const seen:(string|undefined)[]=[]
  let keyAt:string|undefined,requested:string|undefined
  const host:EffectHost={effectFencing:'enforced',dispatch:async(admission,perform,signal)=>{seen.push((await readJob(store,RUN,jobIdentity(RUN,admission.operation.operation)))?.state);return inner.dispatch(admission,perform,signal)}}
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
   const held=await leased(),start=vi.fn(started),first=provider([CALL,{say:'refused'}])
   const events=await drive(held.view,[await startTool(start,{mint})],first.call)
   expect(start).not.toHaveBeenCalled()
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
  const run=async(closeFirst:boolean)=>{
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
   if(closeFirst)await reconcileJob(store,reconciliation(job.job))
   const continued=await reauthorizedRunStore({store,effectHost:host,resumed}),repeat=provider([CALL,{say:'done'}])
   const events=await drive(continued,[tool],repeat.call,{resume:true})
   return {store,start,job,events,row:(await headRows(store)).dispatches.find(d=>d.id===job.operation)!,after:(await readJob(store,RUN,job.job))!}
  }
  const open=await run(false)
  expect(open.start).toHaveBeenCalledTimes(1)
  expect(open.after).toMatchObject({state:'started',handle:SECRET,reclaimed:{owner:'worker-b'}});expect(open.row.state).toBe('result')
  const closed=await run(true)
  expect(closed.start).not.toHaveBeenCalled()
  expect(closed.after.state).toBe('not-started');expect(closed.row.state).toBe('unknown')
  expect(closed.row.failure).toMatchObject({code:'authentication',outcome:'not-started'})
  expect(closed.events.some(e=>e.type==='tool_result'&&e.isError===true&&JSON.stringify(e).includes('JobRefused: job-settled'))).toBe(true)
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
  const events=await drive(view,[held.tool,jobReadTool(view)],next.call,{resume:true,jobContinuation:resumed.job!})
  expect(events.at(-1)?.type).toBe('end')
  expect(next.state.bodies[0]).toContain('JOB RESULT:');expect(next.state.bodies[0]).toContain('succeeded')
  expect(next.state.bodies[0]).not.toContain(SECRET)
  // The read answers every time, and the second says it is unchanged.
  const reads=events.filter(e=>e.type==='tool_result'&&e.name==='job_read') as Extract<AssistantEvent,{type:'tool_result'}>[]
  expect(JSON.stringify(reads[0])).toContain('\\"changed\\":true');expect(JSON.stringify(reads[1])).toContain('\\"changed\\":false');expect(JSON.stringify(reads[1])).toContain('do not poll')
  expect(reads.every(r=>r.isError!==true)).toBe(true)
  expect(JSON.stringify(events)).not.toContain(SECRET)
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
 it('complete_plan_step reads the job afresh, so a job that failed since the turn began cannot close a step',async()=>{
  const held=await leased(),tool=await startTool(started),job=()=>listJobs(held.store,RUN).then(p=>p.jobs[0]!)
  const first=provider([CALL,{say:'running'}])
  await drive(held.view,[tool],first.call)
  const j=await job()
  await completeJob(held.store,completion(j.job,{outcome:'failed'}))
  const list=vi.spyOn(held.store,'listJobs')
  const revision=planDigest([{step:'Run it',status:'in_progress',id:'step-1'}] as never)
  const plan={call:{tool:'update_plan',arguments:{plan:[{step:'Run it',status:'in_progress'}],explanation:'start'}}}
  const close={call:{tool:'complete_plan_step',arguments:{id:'step-1',expectedRevision:revision,outcome:'completed',evidence:{operations:[j.operation],artifacts:[]}}}}
  const next=provider([plan,close,{say:'x'}])
  const events=await drive(held.view,[tool],next.call,{resume:true})
  expect(list).toHaveBeenCalled()
  expect(events.find(e=>e.type==='plan_completion_refused')).toMatchObject({reason:'unsuccessful-operation',step:'step-1'})
 })
})

describe('a held start goes through the ordinary approval (#229)',()=>{
 it('a start the person is asked about registers nothing until it is decided, then runs once with its row\'s bytes',async()=>{
  const held=await leased(),start=vi.fn(async(_:{job:string;request:string}):Promise<JobStartAnswer>=>started()),tool=await startTool(start,{approve:'ask'}),first=provider([CALL])
  const events=await drive(held.view,[tool],first.call)
  const asked=events.find(e=>e.type==='approval_requested') as Extract<AssistantEvent,{type:'approval_requested'}>
  expect(asked).toBeDefined();expect(start).not.toHaveBeenCalled();expect((await exportJobs(held.store,RUN)).jobs).toEqual([])
  const next=provider([{say:'Running.'}])
  const continued=await drive(held.view,[tool],next.call,{resume:true,approval:{callId:asked.callId,approved:true,...HELD_PROOF(asked)}})
  expect(continued.at(-1)?.type).toBe('end')
  expect(start).toHaveBeenCalledTimes(1)
  const job=(await listJobs(held.store,RUN)).jobs[0]!
  expect(job.state).toBe('started')
  expect(start.mock.calls[0]![0].request).toBe((await headRows(held.store)).dispatches.find(d=>d.id===job.operation)!.request)
  expect(next.state.bodies[0]).toContain('started, not finished')
 })
 it('a person who refuses leaves no job',async()=>{
  const held=await leased(),start=vi.fn(started),tool=await startTool(start,{approve:'ask'}),first=provider([CALL])
  const events=await drive(held.view,[tool],first.call),asked=events.find(e=>e.type==='approval_requested') as Extract<AssistantEvent,{type:'approval_requested'}>
  await drive(held.view,[tool],provider([{say:'Stopped.'}]).call,{resume:true,approval:{callId:asked.callId,approved:false}})
  expect(start).not.toHaveBeenCalled();expect((await exportJobs(held.store,RUN)).jobs).toEqual([])
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
