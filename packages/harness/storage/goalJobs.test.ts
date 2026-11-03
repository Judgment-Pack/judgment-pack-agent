import {describe,it,expect,vi} from 'vitest'
import type {AssistantEvent,EngineSession,HostTool} from '../engine'
import {vercel} from '../engines/vercel'
import {answerWith} from '../testing/scenarioDriver'
import {readExecution} from '../execution/state'
import {MemoryRunStore,type RunStore} from './run'
import {ReferenceEffectHost,fenceOf,unattendedRunStore,type LeaseLifetime} from './lease'
import {checkpointBytesDigest,parkRun,wakeKeyDigest,type WaitLifetime} from './wait'
import {completeJob,jobKeyDigest,jobStartTool,listJobs,type JobLifetime,type JobStartAnswer} from './job'
import {toolListDigest} from '../toolSources'
import type {GoalCommand,GoalLifetime} from './goal'
import {abandonGoal,goalCommand,readGoal} from './goalCommands'

/* #230: jobs at the objective, and its abandon, through the real Vercel engine, the package's writer, the reference effect host and
 * the memory store kind as a durable one. The Go twin is go/context/run_goal_flow_test.go. */
const T0=Date.parse('2026-01-01T00:00:00.000Z'),HOUR=3_600_000
class DurableGoalStore extends MemoryRunStore {override readonly leaseLifetime:LeaseLifetime='durable';override readonly waitLifetime:WaitLifetime='durable';override readonly jobLifetime:JobLifetime='durable';override readonly goalLifetime:GoalLifetime='durable'}
const refusal=(reason:string)=>expect.objectContaining({name:'GoalRefused',reason})
const PLAN={id:'accepted-plan',revision:1,objective:'Inspect and report',steps:['Inspect','Report'],questions:[],references:[],status:'accepted' as const,createdAt:'2026-01-01T00:00:00.000Z',acceptedAt:'2026-01-01T00:00:00.000Z'}
const RUN='run-g',MODEL={provider:'openai',model:'fixture'}
const WAKE='wk.goal.0001.Hs8Pq3Lz6Wd1Rm9Tc4Vb7Nx2Ka5U'
function provider(turns:Parameters<typeof answerWith>[0][]){
 const state={requests:0}
 return {state,call:async()=>{const turn=turns[state.requests];state.requests++;if(!turn)throw Error('no scripted turn');return answerWith(turn,'stream',state.requests-1)}}
}
async function drive(store:RunStore,tools:HostTool[],call:(...args:never[])=>Promise<Response>,extra:{resume?:boolean}={}):Promise<AssistantEvent[]>{
 const session:EngineSession={prompt:'Work.',testPrompt:'',tools:[],callTool:vi.fn(async()=>({content:[]})),hostTools:tools,signal:new AbortController().signal,allowConversation:true,critic:'off',
  context:{currentRequest:{message:'Work.',input:'work'},plan:PLAN,runStore:{store,id:RUN,...(extra.resume?{resume:true}:{})}},model:{family:'openai-compatible',model:'fixture',call}} as unknown as EngineSession
 const out:AssistantEvent[]=[];for await(const event of vercel.start(session))out.push(event);return out
}
async function leased(){
 const clock={at:T0},store=new DurableGoalStore({now:()=>new Date(clock.at)}),host=new ReferenceEffectHost(store)
 const fence=fenceOf(await store.acquireLease({resource:RUN,owner:'worker-a',expectedRevision:null,durationMs:HOUR}))
 return {clock,store,host,fence,view:unattendedRunStore({store,fence,effectHost:host})}
}
const checkpoint=async(store:RunStore)=>readExecution(JSON.parse(new TextDecoder().decode((await store.load(RUN))!.checkpoint!)))!
const headOf=async(store:RunStore)=>{const head=(await store.load(RUN))!;return {revision:head.revision,digest:checkpointBytesDigest(head.checkpoint!)}}
let ids=0
const command=(kind:GoalCommand['kind'],expectedRevision:number|null,over:Partial<GoalCommand>={}):GoalCommand=>({version:1,resource:RUN,command:`cmd-${++ids}`,actor:'person-1',expectedRevision,kind,...over})
const plan=(steps:{id?:string;step:string;status:string}[],explanation='work')=>({call:{tool:'update_plan',arguments:{plan:steps,explanation}}})
const close=(id:string,revision:string,operation:string)=>({call:{tool:'complete_plan_step',arguments:{id,expectedRevision:revision,outcome:'completed',evidence:{operations:[operation],artifacts:[]}}}})
const achieve=async(held:Awaited<ReturnType<typeof leased>>,revision:number,over:Partial<GoalCommand>={})=>goalCommand({store:held.store,command:command('achieve',revision,{head:await headOf(held.store),...over})})
const lastEntry=async(store:RunStore,resource=RUN)=>(await readGoal(store,resource))!.journal.at(-1)!


vi.setConfig({testTimeout:120_000})
describe('jobs and the abandon at the objective (#230)',()=>{
 const START={name:'start_job',description:'Start one background job.',inputSchema:{type:'object'},taxonomy:{operation:'run',effect:'act'} as const}
 const keys=['goal.job.0001.Q7z2Xm9LpV4cR8tNb3Yh6Df1Gj5K','goal.job.0002.Hs8Pq3Lz6Wd1Rm9Tc4Vb7Nx2Ka5U','goal.job.0003.Ka5Ue2Jy8Gt3Wo6Bf9Mn1Sv4Zc3R']
 async function startTool(){let n=0;return jobStartTool({...START,source:{kind:'custom',id:'runner',trust:'person',version:'1',toolListDigest:await toolListDigest([START] as never),identity:{kind:'connection',label:'runner'}} as never,approve:(()=>'allow') as never,mint:async()=>jobKeyDigest(keys[n++]!),start:async():Promise<JobStartAnswer>=>({started:true,handle:'runner-handle'})})}
 /** Three jobs: the first will prove the step, the second fails, the third is left running. */
 async function withJobs(){
  const held=await leased(),tool=await startTool(),call=(name:string)=>({call:{tool:'runner__start_job',arguments:{name}}})
  const first=provider([plan([{id:'step-1',step:'Inspect',status:'in_progress'},{id:'step-2',step:'Report',status:'pending'}]),call('a'),call('b'),call('c'),{say:'Started.'}])
  await drive(held.view,[tool],first.call)
  const created=await goalCommand({store:held.store,command:command('create',null,{objective:'objective-j'})})
  const bound=await goalCommand({store:held.store,command:command('bind',created.goal.revision,{steps:['step-1'],head:await headOf(held.store)})})
  const jobs=(await listJobs(held.store,RUN)).jobs
  await completeJob(held.store,{version:1,resource:RUN,job:jobs[0]!.job,generation:1,evidence:'done-a',outcome:'succeeded',key:keys[0]!})
  await completeJob(held.store,{version:1,resource:RUN,job:jobs[1]!.job,generation:1,evidence:'done-b',outcome:'failed',key:keys[1]!})
  const leg=provider([close('step-1',(await checkpoint(held.store)).planState!.revision,jobs[0]!.operation),{say:'Closed.'}])
  await drive(held.view,[tool],leg.call,{resume:true})
  return {...held,tool,jobs,bound}
 }
 it('a start cited as evidence proves its step only as its job reads now; unsettled jobs block unless waived; failures are listed',async()=>{
  const held=await withJobs(),revision=held.bound.goal.revision
  expect((await checkpoint(held.store)).plan[0]!.status).toBe('completed')
  await expect(achieve(held,revision)).rejects.toEqual(refusal('jobs-unsettled'))
  await expect(achieve(held,revision,{waive:[held.jobs[1]!.job]})).rejects.toEqual(refusal('waiver-invalid'))
  // A store whose job record says, since, that the cited job failed: the step is not proven.
  const list=held.store.listJobs.bind(held.store)
  const spy=vi.spyOn(held.store,'listJobs').mockImplementation(async(...args)=>{const page=await list(...args);return {...page,jobs:page.jobs.map(job=>job.job===held.jobs[0]!.job?{...job,completion:{...job.completion!,outcome:'failed' as const}}:job)}})
  await expect(achieve(held,revision,{waive:[held.jobs[2]!.job]})).rejects.toEqual(refusal('evidence-refused'))
  spy.mockRestore()
  const done=await achieve(held,revision,{waive:[held.jobs[2]!.job]})
  expect(done.goal.objectives[0]!.achieved).toMatchObject({waived:[held.jobs[2]!.job],unsuccessful:[{job:held.jobs[1]!.job,state:'completed',outcome:'failed'}]})
 })
 it('the abandon asks a cancel of every started job, records what it left and journals its deliveries; a retry sends nothing again',async()=>{
  const held=await withJobs(),revision=held.bound.goal.revision,sent:string[]=[]
  const cmd=command('abandon',revision)
  const first=await abandonGoal({store:held.store,effectHost:held.host,command:cmd,durationMs:HOUR,cancel:async input=>{sent.push(input.job)}})
  expect(first.goal.objectives[0]).toMatchObject({state:'abandoned',abandoned:{waits:[],jobs:[{job:held.jobs[2]!.job,state:'started'}]}})
  expect(sent).toEqual([held.jobs[2]!.job]);expect(first.fence?.owner).toBe('job-abandon')
  expect(first.goal.journal.at(-1)).toMatchObject({kind:'abandon',outcome:'delivered',deliveries:expect.arrayContaining([{job:held.jobs[2]!.job,delivery:'sent'}])})
  const retry=await abandonGoal({store:held.store,effectHost:held.host,command:cmd,durationMs:HOUR,cancel:async input=>{sent.push(input.job)}})
  expect(sent).toHaveLength(1);expect(retry.entry).toEqual(first.entry)
  expect(retry.goal.journal.at(-1)).toMatchObject({outcome:'delivered',deliveries:expect.arrayContaining([{job:held.jobs[2]!.job,delivery:'already-sent'}])})
 })
 it('H-2: a repeat of an old objective\'s abandon after a new objective started work sends nothing, takes no lease and writes nothing',async()=>{
  const held=await leased(),tool=await startTool(),call=(name:string)=>({call:{tool:'runner__start_job',arguments:{name}}})
  const created=await goalCommand({store:held.store,command:command('create',null,{objective:'objective-1'})})
  await drive(held.view,[tool],provider([call('a'),{say:'Started a.'}]).call)
  const sent:string[]=[],cancel=async(input:{job:string})=>{sent.push(input.job)}
  const abandonCmd=command('abandon',created.goal.revision)
  const abandoned=await abandonGoal({store:held.store,effectHost:held.host,command:abandonCmd,durationMs:HOUR,cancel})
  const jobA=(await listJobs(held.store,RUN)).jobs[0]!.job
  expect(sent).toEqual([jobA])
  const second=await goalCommand({store:held.store,command:command('create',abandoned.goal.revision,{objective:'objective-2'})})
  // Objective 2's run goes on under the abandon's fence and starts job B.
  await drive(unattendedRunStore({store:held.store,fence:abandoned.fence!,effectHost:held.host}),[tool],provider([call('b'),{say:'Started b.'}]).call,{resume:true})
  const jobB=(await listJobs(held.store,RUN)).jobs.map(job=>job.job).find(job=>job!==jobA)!
  const lease=await held.store.readLease(RUN),before=(await readGoal(held.store,RUN))!
  await expect(abandonGoal({store:held.store,effectHost:held.host,command:abandonCmd,durationMs:HOUR,cancel})).rejects.toEqual(refusal('revision-changed'))
  expect(sent).toEqual([jobA]);expect(sent).not.toContain(jobB)
  expect(await held.store.readLease(RUN)).toEqual(lease)
  const after=(await readGoal(held.store,RUN))!
  expect(after).toEqual(before);expect(after.objectives.at(-1)).toMatchObject({id:'objective-2',state:'active'})
  expect(after.journal.slice(-3).map(entry=>`${entry.kind}:${entry.outcome}:${entry.objective}`)).toEqual(['abandon:accepted:objective-1','abandon:delivered:objective-1','create:accepted:objective-2'])
  expect(second.goal.revision).toBe(after.revision)
 })
 it('M10: a wait parked before the abandon holds the next create until it is resumed or ended',async()=>{
  const held=await withJobs(),revision=held.bound.goal.revision
  await parkRun({store:held.store,fence:held.fence,wait:'wait-g',continuation:'k-g',job:held.jobs[2]!.job,condition:{event:{kind:'job-completion',subject:held.jobs[2]!.job}},model:MODEL,wakeKeyDigest:wakeKeyDigest(WAKE),release:[]} as never)
  const gone=await abandonGoal({store:held.store,effectHost:held.host,command:command('abandon',revision),durationMs:HOUR,cancel:async()=>undefined})
  expect(gone.goal.objectives[0]!.abandoned!.waits).toEqual([{wait:'wait-g',reason:'job',state:'parked'}])
  const next=command('create',gone.goal.revision,{objective:'objective-k'})
  await expect(goalCommand({store:held.store,command:next})).rejects.toEqual(refusal('wait-unresolved'))
  expect(await lastEntry(held.store)).toMatchObject({kind:'create',outcome:'wait-unresolved',objective:'objective-k'})
 })
})
