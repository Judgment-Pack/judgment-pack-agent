/**
 * #229: complete_plan_step reads a cited start's job afresh, at the close, not from the records the turn began with. These two
 * cases are in a file of their own so that the mutation row which removes the fresh read ("job C1") runs them alone, well inside
 * the table's bound: storage/job.test.ts, which holds the rest of the job tools, takes most of that bound under load. The Go
 * twin is TestJobsACloseReadsTheJobAfresh in go/adapter/jobtests.
 */
import {it,expect,vi} from 'vitest'
import type {AssistantEvent,EngineSession,HostTool} from '../engine'
import {vercel} from '../engines/vercel'
import {answerWith} from '../testing/scenarioDriver'
import {toolListDigest} from '../toolSources'
import {planDigest} from '../execution/plan'
import {MemoryRunStore,type RunStore} from './run'
import {ReferenceEffectHost,fenceOf,unattendedRunStore,type LeaseLifetime} from './lease'
import type {WaitLifetime} from './wait'
import {completeJob,jobKeyDigest,jobStartTool,listJobs,type JobCompletionEvidence,type JobLifetime,type JobStartAnswer} from './job'

const T0=Date.parse('2026-01-01T00:00:00.000Z'),HOUR=3_600_000,RUN='run-j'
const KEY='job.key.0001.Q7z2Xm9LpV4cR8tNb3Yh6Df1Gj5K'
/** Stands for a host store kind whose leases, waits and jobs outlive its process. */
class DurableJobStore extends MemoryRunStore {override readonly leaseLifetime:LeaseLifetime='durable';override readonly waitLifetime:WaitLifetime='durable';override readonly jobLifetime:JobLifetime='durable'}
const START={name:'start_job',description:'Start one background job.',inputSchema:{type:'object'},taxonomy:{operation:'run',effect:'act'} as const}
const started=async():Promise<JobStartAnswer>=>({started:true,handle:'SENTINEL-EXTERNAL-HANDLE-4f1c9e7a2b',receipt:'SENTINEL-RECEIPT-8d3b6a0e5c'})
async function startTool():Promise<HostTool>{
 return jobStartTool({...START,source:{kind:'custom',id:'runner',trust:'person',version:'1',toolListDigest:await toolListDigest([START] as never),identity:{kind:'connection',label:'runner'}} as never,approve:(()=>'allow') as never,mint:async()=>jobKeyDigest(KEY),start:started as never})
}
function provider(turns:Parameters<typeof answerWith>[0][]){
 const state={requests:0}
 return {state,call:async()=>{const turn=turns[state.requests];state.requests++;if(!turn)throw Error('no scripted turn');return answerWith(turn,'stream',state.requests-1)}}
}
/** #330: a later turn resumes the run as one stopped before its end would be: its head is left running, under its fence. */
async function interrupted(store:RunStore):Promise<void>{
 const head=await store.load(RUN),saved=head?.checkpoint?JSON.parse(new TextDecoder().decode(head.checkpoint)):undefined
 if(saved?.status!=='complete')return
 await store.commit({runId:RUN,expectedRevision:head!.revision,commitId:crypto.randomUUID(),checkpoint:new TextEncoder().encode(JSON.stringify({...saved,status:'running'})),events:[]})
}
async function drive(store:RunStore,tools:HostTool[],call:(...args:never[])=>Promise<Response>,resume=false):Promise<AssistantEvent[]>{
 if(resume)await interrupted(store)
 const session={prompt:'Run the job.',testPrompt:'',tools:[],callTool:vi.fn(async()=>({content:[]})),hostTools:tools,signal:new AbortController().signal,allowConversation:true,critic:'off',
  context:{currentRequest:{message:'Run the job.',input:'job'},runStore:{store,id:RUN,...(resume?{resume:true}:{})}},model:{family:'openai-compatible',model:'fixture',call}} as unknown as EngineSession
 const out:AssistantEvent[]=[];for await(const event of vercel.start(session))out.push(event);return out
}
async function leased(){
 const store=new DurableJobStore({now:()=>new Date(T0)}),host=new ReferenceEffectHost(store)
 const fence=fenceOf(await store.acquireLease({resource:RUN,owner:'worker-a',expectedRevision:null,durationMs:HOUR}))
 return {store,view:unattendedRunStore({store,fence,effectHost:host})}
}
const CALL={call:{tool:'runner__start_job',arguments:{name:'nightly'}}}
const completion=(job:string,over:Partial<JobCompletionEvidence>={}):JobCompletionEvidence=>({version:1,resource:RUN,job,generation:1,evidence:'done-1',outcome:'succeeded',key:KEY,...over})
const revision=planDigest([{step:'Run it',status:'in_progress',id:'step-1'}] as never)
const plan={call:{tool:'update_plan',arguments:{plan:[{step:'Run it',status:'in_progress'}],explanation:'start'}}}
const close=(operation:string)=>({call:{tool:'complete_plan_step',arguments:{id:'step-1',expectedRevision:revision,outcome:'completed',evidence:{operations:[operation],artifacts:[]}}}})

it('complete_plan_step reads the job afresh: a job that succeeded since the run opened closes its step',async()=>{
 const held=await leased(),tool=await startTool()
 await drive(held.view,[tool],provider([CALL,{say:'running'}]).call)
 const j=(await listJobs(held.store,RUN)).jobs[0]!
 const list=held.store.listJobs.bind(held.store)
 let opened=false
 vi.spyOn(held.store,'listJobs').mockImplementation(async(...args)=>{const page=await list(...args);if(!opened){opened=true;await completeJob(held.store,completion(j.job))}return page})
 const events=await drive(held.view,[tool],provider([plan,close(j.operation),{say:'x'}]).call,true)
 expect(events.some(e=>e.type==='plan_completion_refused')).toBe(false)
 expect(events.some(e=>e.type==='plan_revised'&&JSON.stringify(e).includes('recorded evidence'))).toBe(true)
})
it('complete_plan_step reads the job afresh, so a job that failed since the turn began cannot close a step',async()=>{
 const held=await leased(),tool=await startTool()
 await drive(held.view,[tool],provider([CALL,{say:'running'}]).call)
 const j=(await listJobs(held.store,RUN)).jobs[0]!
 await completeJob(held.store,completion(j.job,{outcome:'failed'}))
 const list=vi.spyOn(held.store,'listJobs')
 const events=await drive(held.view,[tool],provider([plan,close(j.operation),{say:'x'}]).call,true)
 expect(list).toHaveBeenCalled()
 expect(events.find(e=>e.type==='plan_completion_refused')).toMatchObject({reason:'unsuccessful-operation',step:'step-1'})
})
