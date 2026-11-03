import {describe,it,expect,vi} from 'vitest'
import type {AssistantEvent,EngineSession,HostTool} from '../engine'
import {vercel} from '../engines/vercel'
import {answerWith} from '../testing/scenarioDriver'
import {readExecution} from '../execution/state'
import {MemoryRunStore,checkpointCallbackRunStore,type RunStore} from './run'
import {ReferenceEffectHost,fenceOf,unattendedRunStore,type LeaseLifetime} from './lease'
import type {WaitLifetime} from './wait'
import type {JobLifetime} from './job'
import {GOAL_JOURNAL_LIMIT,goalReportFromRecord,readGoalReport,readRunGoal,type GoalCommand,type GoalLifetime} from './goal'
import {abandonGoal,admitGoals,goalCommand,goalReport,inspectGoal,readGoal} from './goalCommands'
import {checkpointBytesDigest} from './wait'

/* #230: one durable goal through the real Vercel engine, the package's writer and the memory store kind as a durable one. Every
 * command is the host's: no model is called. Jobs and the abandon are goalJobs.test.ts, a delegated step goalDelegated.test.ts. The
 * Go twin is go/context/run_goal_flow_test.go. */
const T0=Date.parse('2026-01-01T00:00:00.000Z'),HOUR=3_600_000
class DurableGoalStore extends MemoryRunStore {override readonly leaseLifetime:LeaseLifetime='durable';override readonly waitLifetime:WaitLifetime='durable';override readonly jobLifetime:JobLifetime='durable';override readonly goalLifetime:GoalLifetime='durable'}
const RUN='run-g'
const refusal=(reason:string)=>expect.objectContaining({name:'GoalRefused',reason})
const PLAN={id:'accepted-plan',revision:1,objective:'Inspect and report',steps:['Inspect','Report'],questions:[],references:[],status:'accepted' as const,createdAt:'2026-01-01T00:00:00.000Z',acceptedAt:'2026-01-01T00:00:00.000Z'}
const LOOKUP:HostTool={name:'lookup',effect:'read',taxonomy:{operation:'read',effect:'read'},description:'Read supplied evidence.',inputSchema:{type:'object'},execute:async()=>({content:[{type:'text',text:'Evidence.'}]})}
function provider(turns:Parameters<typeof answerWith>[0][]){
 const state={requests:0}
 return {state,call:async()=>{const turn=turns[state.requests];state.requests++;if(!turn)throw Error('no scripted turn');return answerWith(turn,'stream',state.requests-1)}}
}
async function drive(store:RunStore,tools:HostTool[],call:(...args:never[])=>Promise<Response>,extra:{resume?:boolean;plan?:typeof PLAN;planDrift?:string[]}={}):Promise<AssistantEvent[]>{
 const session:EngineSession={prompt:'Work.',testPrompt:'',tools:[],callTool:vi.fn(async()=>({content:[]})),hostTools:tools,signal:new AbortController().signal,allowConversation:true,critic:'off',
  context:{currentRequest:{message:'Work.',input:'work'},plan:extra.plan??PLAN,...(extra.planDrift?{planDrift:extra.planDrift}:{}),runStore:{store,id:RUN,...(extra.resume?{resume:true}:{})}},model:{family:'openai-compatible',model:'fixture',call}} as unknown as EngineSession
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
const lookups=async(store:RunStore)=>(await checkpoint(store)).durable!.dispatches.filter(row=>row.tool==='lookup').map(row=>row.id)
/** A copy of a store's whole state in a new instance, as after the store's process restarted. */
function restarted(from:RunStore,clock:{at:number}):DurableGoalStore{
 const fresh=new DurableGoalStore({now:()=>new Date(clock.at)}) as unknown as Record<string,any>,old=from as unknown as Record<string,any>
 fresh.runs=structuredClone(old.runs)
 for(const table of ['leases','waits','journal','jobs','goals'])for(const [name,value] of Object.entries(old[table] as object))if(typeof value!=='function')fresh[table][name]=structuredClone(value)
 return fresh as unknown as DurableGoalStore
}
type Held=Awaited<ReturnType<typeof leased>>
const again=(held:Held):Held=>{const clock={at:held.clock.at},store=restarted(held.store,clock),host=new ReferenceEffectHost(store);return {clock,store,host,fence:held.fence,view:unattendedRunStore({store,fence:held.fence,effectHost:host})}}
/** A run whose model began the accepted Plan's first step and read twice; then the objective is created and both steps bound. */
let built:Promise<Held&{binding:Awaited<ReturnType<typeof goalCommand>>;calls:number}>|undefined
function bound(){
 built??=(async()=>{
  const held=await leased(),first=provider([plan([{id:'step-1',step:'Inspect',status:'in_progress'},{id:'step-2',step:'Report',status:'pending'}]),{call:{tool:'lookup',arguments:{}}},{call:{tool:'lookup',arguments:{}}},{say:'Read.'}])
  await drive(held.view,[LOOKUP],first.call)
  const created=await goalCommand({store:held.store,command:command('create',null,{objective:'objective-1'})})
  const binding=await goalCommand({store:held.store,command:command('bind',created.goal.revision,{steps:['step-2','step-1'],head:await headOf(held.store)})})
  return {...held,binding,calls:first.state.requests}
 })()
 return built.then(held=>({...again(held),binding:held.binding,calls:held.calls}))
}
/** The model closes both required steps with the reads it made, over the next legs; `moves` is its plan between them. */
async function finish(held:Held,moves:Parameters<typeof plan>[0]=[{id:'step-1',step:'Inspect',status:'completed'},{id:'step-2',step:'Report',status:'in_progress'}]){
 const [a,b]=await lookups(held.store)
 const one=provider([close('step-1',(await checkpoint(held.store)).planState!.revision,a!),{say:'Closed one.'}])
 await drive(held.view,[LOOKUP],one.call,{resume:true})
 const two=provider([plan(moves,'next'),{say:'Moved.'}])
 await drive(held.view,[LOOKUP],two.call,{resume:true})
 const last=await checkpoint(held.store),step=last.plan.find(row=>row.origin?.index===1)!
 const three=provider([close(step.id!,last.planState!.revision,b!),{say:'Done.'}])
 await drive(held.view,[LOOKUP],three.call,{resume:true})
 return one.state.requests+two.state.requests+three.state.requests
}
const achieve=async(held:Held,revision:number,over:Partial<GoalCommand>={})=>goalCommand({store:held.store,command:command('achieve',revision,{head:await headOf(held.store),...over})})
const lastEntry=async(store:RunStore,resource=RUN)=>(await readGoal(store,resource))!.journal.at(-1)!

// L-7: two of these runs take 70 to 80 s unloaded; a slow or loaded runner gets the room to finish them.
vi.setConfig({testTimeout:300_000})
describe('the objective: host commands without a model turn (#230)',()=>{
 it('creates, binds the accepted Plan and is achieved by the steps\' own evidence; a helper step the model added is no departure (M17)',async()=>{
  const held=await bound()
  expect(held.binding.goal.objectives[0]!.plan).toMatchObject({id:'accepted-plan',steps:[{step:'step-1',index:0},{step:'step-2',index:1}]})
  // The model inserts a helper step before the second required one: departures() reads it as added and reordered.
  const model=await finish(held,[{id:'step-1',step:'Inspect',status:'completed'},{step:'Check the inputs',status:'pending'},{id:'step-2',step:'Report',status:'in_progress'}])
  expect((await checkpoint(held.store)).plan.map(row=>row.step)).toEqual(['Inspect','Check the inputs','Report'])
  const done=await achieve(held,held.binding.goal.revision)
  expect(done.goal.objectives[0]).toMatchObject({state:'achieved',achieved:{actor:'person-1',head:await headOf(held.store)}})
  expect(done.goal.objectives[0]!.achieved!.unsuccessful).toBeUndefined()
  // No command made a request, wrote the run or touched a lease.
  expect(model).toBe(6)
  expect(readRunGoal(done.goal)).toEqual(done.goal)
  expect(readGoalReport(goalReportFromRecord(done.goal))).toEqual(goalReportFromRecord(done.goal))
  // A terminal objective has no disposition, and a later one cannot reuse the steps' proof (M11).
  await expect(goalCommand({store:held.store,command:command('pause',done.goal.revision)})).rejects.toEqual(refusal('objective-terminal'))
  const next=await goalCommand({store:held.store,command:command('create',done.goal.revision,{objective:'objective-2'})})
  await expect(goalCommand({store:held.store,command:command('bind',next.goal.revision,{steps:['step-1'],head:await headOf(held.store)})})).rejects.toEqual(refusal('step-settled'))
  expect(await lastEntry(held.store)).toMatchObject({outcome:'step-settled',step:'step-1',objective:'objective-2'})
 })
 it('M17: a required step changed or moved against the other required step strands the achievement; so does a pending decision',async()=>{
  for(const [moves,reason] of [[[{id:'step-1',step:'Inspect',status:'in_progress'},{id:'step-2',step:'Report twice',status:'pending'}],'lineage-changed'],[[{id:'step-2',step:'Report',status:'pending'},{id:'step-1',step:'Inspect',status:'in_progress'}],'lineage-changed'],[[{id:'step-1',step:'Inspect',status:'in_progress'}],'decision-pending']] as const){
   const held=await bound(),leg=provider([plan(moves as never,'Change the plan.'),{say:'Changed.'}])
   await drive(held.view,[LOOKUP],leg.call,{resume:true})
   await expect(achieve(held,held.binding.goal.revision)).rejects.toEqual(refusal(reason))
   expect(await lastEntry(held.store)).toMatchObject({kind:'achieve',outcome:reason})
  }
 })
 it('refuses an incomplete step, a moved head, and a store whose job record no longer proves a step',async()=>{
  const held=await bound()
  await expect(achieve(held,held.binding.goal.revision)).rejects.toEqual(refusal('step-incomplete'))
  expect(await lastEntry(held.store)).toMatchObject({outcome:'step-incomplete',step:'step-1'})
  await expect(goalCommand({store:held.store,command:command('achieve',held.binding.goal.revision,{head:{revision:'another',digest:held.binding.goal.objectives[0]!.plan!.revision}})})).rejects.toEqual(refusal('head-changed'))
  await finish(held)
  // The evidence is read again from the head's book: a book whose read failed since proves nothing.
  const head=(await held.store.load(RUN))!,text=new TextDecoder().decode(head.checkpoint!),state=JSON.parse(text)
  const [a]=await lookups(held.store)
  const broken={...state,durable:{...state.durable,observations:state.durable.observations.filter((row:{dispatchId:string})=>row.dispatchId!==a)}}
  await held.store.commitFenced({runId:RUN,expectedRevision:head.revision,commitId:'tampered',checkpoint:new TextEncoder().encode(JSON.stringify(broken)),events:[]},held.fence)
  await expect(achieve(held,held.binding.goal.revision)).rejects.toEqual(refusal('evidence-refused'))
  expect(await lastEntry(held.store)).toMatchObject({outcome:'evidence-refused',step:'step-1'})
 })
 it('H-1: an achievement sent with the bind it follows is never accepted, whichever lands first',async()=>{
  // Sent together, either order may happen: read before the bind it is revision-changed, read after it the bound Plan's steps are
  // unfinished. Each order is also a case of its own, the first strictly revision-changed. A host store that does not compare the
  // revision read: the package's own finding, plan-unbound, still refuses.
  const REASONS={'together':['revision-changed','step-incomplete'],'the bind lands first':['step-incomplete'],'read before the bind':['revision-changed'],'a store that does not compare what was read':['plan-unbound']} as const
  for(const how of ['together','the bind lands first','read before the bind','a store that does not compare what was read'] as const){
   const held=await leased()
   const first=provider([plan([{id:'step-1',step:'Inspect',status:'in_progress'},{id:'step-2',step:'Report',status:'pending'}]),{call:{tool:'lookup',arguments:{}}},{say:'Read.'}])
   await drive(held.view,[LOOKUP],first.call)
   const created=await goalCommand({store:held.store,command:command('create',null,{objective:'objective-1'})})
   const head=await headOf(held.store),r=created.goal.revision
   const bind=()=>goalCommand({store:held.store,command:command('bind',r,{steps:['step-1','step-2'],head})})
   const achieveIt=()=>goalCommand({store:held.store,command:command('achieve',r+1,{head})})
   let refused:string|undefined
   if(how==='together'){
    const [bound,achieved]=await Promise.allSettled([bind(),achieveIt()])
    expect(bound.status).toBe('fulfilled');expect(achieved.status).toBe('rejected')
    refused=(achieved as PromiseRejectedResult).reason?.reason
   }else if(how==='the bind lands first'){
    await bind()
    refused=await achieveIt().then(()=>undefined,(error:{reason?:string})=>error.reason)
   }else{
    // The achievement reads the record, then the bind lands, then the achievement's transaction runs at the bind's revision.
    const store=held.store as unknown as {readGoal:(resource:string,signal?:AbortSignal)=>Promise<unknown>;commandGoal:(input:{command:GoalCommand;read?:number},signal?:AbortSignal)=>Promise<unknown>},real=store.readGoal.bind(store)
    let once=false
    store.readGoal=async(resource,signal)=>{const out=await real(resource,signal);if(!once){once=true;await bind()};return out}
    if(how==='a store that does not compare what was read'){const command=store.commandGoal.bind(store);store.commandGoal=(input,signal)=>command(input.command.kind==='achieve'?{...input,read:input.command.expectedRevision!}:input,signal)}
    await expect(achieveIt()).rejects.toEqual(refusal(REASONS[how][0]))
    refused=REASONS[how][0]
   }
   expect(REASONS[how]).toContain(refused)
   const goal=(await readGoal(held.store,RUN))!,steps=(await checkpoint(held.store)).plan.map(row=>row.status)
   expect(goal.objectives[0]).toMatchObject({state:'active',plan:{id:'accepted-plan'}});expect(goal.objectives[0]!.achieved).toBeUndefined()
   expect(steps).toEqual(['in_progress','pending'])
   expect(goal.journal.map(entry=>`${entry.kind}:${entry.outcome}`).sort()).toEqual([`achieve:${refused}`,'bind:accepted','create:accepted'])
  }
 })
 it('competing and repeated commands: one wins by revision, a repeat answers as the first did, another under its id is conflict',async()=>{
  const held=await bound(),revision=held.binding.goal.revision
  const a=command('pause',revision),b=command('block',revision,{condition:'waiting-on-data'})
  const both=await Promise.allSettled([goalCommand({store:held.store,command:a}),goalCommand({store:held.store,command:b})])
  expect(both.map(r=>r.status).sort()).toEqual(['fulfilled','rejected'])
  expect((both.find(r=>r.status==='rejected') as PromiseRejectedResult).reason).toEqual(refusal('revision-changed'))
  const won=(both.find(r=>r.status==='fulfilled') as PromiseFulfilledResult<Awaited<ReturnType<typeof goalCommand>>>).value
  const record=await readGoal(held.store,RUN)
  expect(record!.revision).toBe(revision+1)
  expect(record!.journal.slice(-2).map(e=>e.outcome).sort()).toEqual(['accepted','revision-changed'])
  const repeat=await goalCommand({store:held.store,command:won.entry.kind==='pause'?a:b})
  expect(repeat.entry).toEqual(won.entry);expect((await readGoal(held.store,RUN))!.journal).toEqual(record!.journal)
  await expect(goalCommand({store:held.store,command:{...a,actor:'person-2'}})).rejects.toEqual(refusal(won.entry.kind==='pause'?'conflict':'revision-changed'))
 })
 it('M8: a pause refuses an acquisition and a resume only; its answer reports the held lease, which commits on; resume writes nothing else',async()=>{
  const held=await bound()
  const paused=await goalCommand({store:held.store,command:command('pause',held.binding.goal.revision)})
  expect(paused.lease).toMatchObject({owner:'worker-a',epoch:held.fence.epoch,state:'held',expiry:new Date(T0+HOUR).toISOString()})
  // The held lease runs on: the run's own worker still commits a leg.
  const leg=provider([{say:'Still working.'}])
  await drive(held.view,[LOOKUP],leg.call,{resume:true})
  expect(leg.state.requests).toBe(1)
  await held.store.releaseLease(held.fence)
  const head=(await held.store.load(RUN))!.revision
  await expect(held.store.acquireLease({resource:RUN,owner:'worker-b',expectedRevision:head,durationMs:HOUR})).rejects.toEqual(refusal('objective-paused'))
  const before={head:await held.store.load(RUN),lease:await held.store.readLease(RUN),waits:await held.store.exportWaits(RUN),jobs:await held.store.exportJobs(RUN)}
  const resumed=await goalCommand({store:held.store,command:command('resume',paused.goal.revision)})
  expect(resumed.goal.objectives[0]!.paused).toBeUndefined()
  expect({head:await held.store.load(RUN),lease:await held.store.readLease(RUN),waits:await held.store.exportWaits(RUN),jobs:await held.store.exportJobs(RUN)}).toEqual(before)
  await expect(held.store.acquireLease({resource:RUN,owner:'worker-b',expectedRevision:head,durationMs:HOUR})).resolves.toMatchObject({owner:'worker-b'})
  // L2: an achievement under a pause is independent of it, and the objective it ends has no disposition.
  const again=await goalCommand({store:held.store,command:command('pause',resumed.goal.revision)})
  await expect(achieve(held,again.goal.revision)).rejects.toEqual(refusal('step-incomplete'))
 })
 it('an objective\'s run is leased: an unfenced commit is refused from create on, and the fenced view goes on',async()=>{
  const held=await bound(),head=(await held.store.load(RUN))!
  await expect(held.store.commit({runId:RUN,expectedRevision:head.revision,commitId:'unfenced',checkpoint:head.checkpoint,events:[]})).rejects.toEqual(refusal('lease-required'))
  expect((await held.store.load(RUN))!.revision).toBe(head.revision)
 })
 it('M12: an inspection is counted and moves nothing; the journal never refuses a command for size',async()=>{
  const held=await bound(),revision=held.binding.goal.revision
  for(const id of ['look-1','look-1','look-2'])await inspectGoal({store:held.store,resource:RUN,command:id,actor:'dashboard'})
  const looked=(await readGoal(held.store,RUN))!
  expect(looked.inspections).toEqual({count:2,last:{command:'look-2',actor:'dashboard',at:new Date(T0).toISOString()}})
  expect(looked.revision).toBe(revision)
  for(let i=0;i<GOAL_JOURNAL_LIMIT+3;i++)await expect(goalCommand({store:held.store,command:command('resume',revision)})).rejects.toEqual(refusal('not-paused'))
  const full=(await readGoal(held.store,RUN))!
  expect(full.journal).toHaveLength(GOAL_JOURNAL_LIMIT);expect(full.journal.slice(0,2).map(e=>e.kind)).toEqual(['create','bind'])
  const abandoned=await abandonGoal({store:held.store,effectHost:held.host,command:command('abandon',revision),durationMs:HOUR,cancel:async()=>undefined})
  expect(abandoned.goal.objectives[0]!.state).toBe('abandoned');expect(abandoned.goal.journal).toHaveLength(GOAL_JOURNAL_LIMIT)
  expect(abandoned.goal.journal.slice(-2).map(e=>e.outcome)).toEqual(['accepted','delivered'])
  expect(abandoned.goal.discarded).toBe(7)
 })
 it('survives a restart whole, and its report is the record\'s',async()=>{
  const held=await bound()
  await goalCommand({store:held.store,command:command('pause',held.binding.goal.revision)})
  const before=await readGoal(held.store,RUN),report=await goalReport(held.store,RUN)
  const after=again(held)
  expect(await readGoal(after.store,RUN)).toEqual(before);expect(await goalReport(after.store,RUN)).toEqual(report)
  await after.store.releaseLease(after.fence)
  await expect(after.store.acquireLease({resource:RUN,owner:'worker-b',expectedRevision:(await after.store.load(RUN))!.revision,durationMs:HOUR})).rejects.toEqual(refusal('objective-paused'))
 })
 it('refuses a store that cannot hold an objective, by name',async()=>{
  expect(()=>admitGoals(new MemoryRunStore())).toThrow(expect.objectContaining({name:'GoalCapabilityRefused',reason:'store-lifetime'}))
  expect(()=>admitGoals(checkpointCallbackRunStore(async()=>undefined,()=>undefined))).toThrow(expect.objectContaining({name:'GoalCapabilityRefused',reason:'store-unsupported'}))
  const held=await leased()
  expect(()=>admitGoals(held.view)).not.toThrow()
 })
 it('M17: a required step removed where the host lets removals through strands the achievement too',async()=>{
  const held=await leased(),first=provider([plan([{id:'step-1',step:'Inspect',status:'pending'},{id:'step-2',step:'Report',status:'pending'}]),{say:'Begun.'}])
  await drive(held.view,[LOOKUP],first.call,{planDrift:['added']})
  const created=await goalCommand({store:held.store,command:command('create',null,{objective:'objective-r'})})
  const bound=await goalCommand({store:held.store,command:command('bind',created.goal.revision,{steps:['step-1','step-2'],head:await headOf(held.store)})})
  // The first required step goes: no other check stands in for this one.
  await drive(held.view,[LOOKUP],provider([plan([{id:'step-2',step:'Report',status:'in_progress'}],'Drop the inspection.'),{say:'Dropped.'}]).call,{resume:true,planDrift:['added']})
  expect((await checkpoint(held.store)).plan.map(row=>row.id)).toEqual(['step-2'])
  await expect(achieve(held,bound.goal.revision)).rejects.toEqual(refusal('lineage-changed'))
 })
 it('binds only steps of the accepted Plan, and achieves only the accepted Plan it bound',async()=>{
  const held=await leased()
  await drive(held.view,[LOOKUP],provider([plan([{id:'step-1',step:'Inspect',status:'in_progress'},{step:'Check the inputs',status:'pending'},{id:'step-2',step:'Report',status:'pending'}]),{say:'Begun.'}]).call)
  const helper=(await checkpoint(held.store)).plan.find(row=>!row.origin)!.id!
  const created=await goalCommand({store:held.store,command:command('create',null,{objective:'objective-p'})})
  await expect(goalCommand({store:held.store,command:command('bind',created.goal.revision,{steps:['step-1',helper],head:await headOf(held.store)})})).rejects.toEqual(refusal('plan-invalid'))
  const bound=await goalCommand({store:held.store,command:command('bind',created.goal.revision,{steps:['step-1'],head:await headOf(held.store)})})
  // Another accepted Plan's head under this run: the objective bound this Plan, not that one.
  const other=await leased()
  await drive(other.view,[LOOKUP],provider([plan([{id:'step-1',step:'Inspect',status:'in_progress'},{id:'step-2',step:'Report',status:'pending'}]),{say:'Begun.'}]).call,{plan:{...PLAN,id:'other-plan'}})
  const foreign=(await other.store.load(RUN))!.checkpoint!,head=(await held.store.load(RUN))!
  await held.store.commitFenced({runId:RUN,expectedRevision:head.revision,commitId:'other-plan-head',checkpoint:foreign,events:[]},held.fence)
  await expect(achieve(held,bound.goal.revision)).rejects.toEqual(refusal('lineage-changed'))
 })
 it('reads the store\'s answer back: an entry that is not this command\'s is refused',async()=>{
  const held=await bound(),send=held.store.commandGoal.bind(held.store)
  vi.spyOn(held.store,'commandGoal').mockImplementation(async(input,signal)=>{const answer=await send(input,signal);return {...answer,entry:answer.goal.journal[0]!}})
  await expect(goalCommand({store:held.store,command:command('pause',held.binding.goal.revision)})).rejects.toEqual(refusal('invalid'))
 })
})
