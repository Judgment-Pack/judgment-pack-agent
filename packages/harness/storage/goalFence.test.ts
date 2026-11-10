import {describe,it,expect,vi} from 'vitest'
import {MemoryRunStore} from './run'
import {LeaseRefused,ReferenceEffectHost,fenceOf,unattendedRunStore,type LeaseFence,type LeaseLifetime} from './lease'
import {JOB_ABANDON_OWNER,abandonJobs,jobIdentity,jobKeyDigest,type JobCancelInput,type JobLifetime} from './job'
import {checkpointBytesDigest,type WaitLifetime} from './wait'
import {GoalCapabilityRefused,GoalRefused,GoalTable,type GoalCommand,type GoalLifetime} from './goal'
import {abandonGoal,goalCommand,readGoal} from './goalCommands'

/* #315: what a worker's view may command, and the abandon's remaining edges. L-3: a command sent through a fenced view presents
 * that view's fence, and a worker a takeover displaced is refused LeaseRefused with nothing journaled, seized, sent or changed.
 * L-2: a retry from a new process after a crash between the abandon's sends and their journal hands `cancel` the same operation.
 * L-6: a terminal objective's jobs are the host's abandonJobs to stop. #321 L-3: M-4 against a second abandon. The Go twin is
 * go/context/run_goal_fence_test.go. */
const T0=Date.parse('2026-01-01T00:00:00.000Z'),HOUR=3_600_000
class DurableGoalStore extends MemoryRunStore {override readonly leaseLifetime:LeaseLifetime='durable';override readonly waitLifetime:WaitLifetime='durable';override readonly jobLifetime:JobLifetime='durable';override readonly goalLifetime:GoalLifetime='durable'}
const RUN='run-f',OTHER='run-o',bytes=(text:string)=>new TextEncoder().encode(text)
const KEY='fence.job-a.Q7z2Xm9LpV4cR8tNb3Yh6Df1',REQUEST=`sha256:${'a'.repeat(64)}`
let ids=0
const command=(kind:GoalCommand['kind'],expectedRevision:number|null,over:Partial<GoalCommand>={}):GoalCommand=>({version:1,resource:RUN,command:`cmd-${++ids}`,actor:'person-1',expectedRevision,kind,...over})
async function opened(){
 const clock={at:T0},store=new DurableGoalStore({now:()=>new Date(clock.at)}),host=new ReferenceEffectHost(store)
 const open=async(run:string,owner:string)=>{const made=await store.commit({runId:run,expectedRevision:null,commitId:`${run}-open`,checkpoint:bytes(`{"t":"${run}"}`),events:[]});return fenceOf(await store.acquireLease({resource:run,owner,expectedRevision:made.revision,durationMs:HOUR}))}
 const a=await open(RUN,'worker-a'),other=await open(OTHER,'worker-o')
 const view=(fence:LeaseFence)=>unattendedRunStore({store,fence,effectHost:host})
 return {clock,store,host,a,other,view}
}
/** One job of the run, started under `fence`: a running job an abandon must ask to stop. */
async function started(store:DurableGoalStore,fence:LeaseFence):Promise<string>{
 const job=jobIdentity(RUN,'op-1'),head=(await store.load(RUN))!
 await store.commitFenced({runId:RUN,expectedRevision:head.revision,commitId:'run-f-op-1',checkpoint:bytes('{"t":"op-1"}'),events:[],job:{job,operation:'op-1',source:'test-source',tool:'test-start',requestDigest:REQUEST,keyDigest:jobKeyDigest(KEY)}},fence)
 await store.answerJob({version:1,resource:RUN,job,generation:1,answer:'started',handle:'test-handle'},fence)
 return job
}
/** The worker's lease lapses and worker-b takes the run: worker-a's view is the displaced one. */
async function takenOver(held:Awaited<ReturnType<typeof opened>>):Promise<LeaseFence>{
 held.clock.at+=HOUR+1
 return fenceOf(await held.store.acquireLease({resource:RUN,owner:'worker-b',expectedRevision:(await held.store.load(RUN))!.revision,durationMs:HOUR}))
}
/** The exact refusal a stale view gets: the lease refusal, its record's bytes and its failure. */
async function refusedStale(attempt:Promise<unknown>,reason='stale-epoch'):Promise<void>{
 const error=await attempt.then(()=>undefined,(caught:unknown)=>caught)
 expect(error).toBeInstanceOf(LeaseRefused)
 expect(error).not.toBeInstanceOf(GoalRefused)
 const refused=error as LeaseRefused
 expect(refused.name).toBe('LeaseRefused')
 expect(JSON.stringify(refused.record())).toBe(`{"version":1,"reason":"${reason}"}`)
 expect(refused.message).toBe(`LeaseRefused: ${reason}.`)
 expect(refused.failure).toEqual({version:1,code:'stale-state',source:'storage',outcome:'not-started',detail:`LeaseRefused: ${reason}.`})
}
const snapshot=async(store:DurableGoalStore)=>({goal:await readGoal(store,RUN),lease:await store.readLease(RUN),jobs:await store.exportJobs(RUN),waits:await store.exportWaits(RUN),head:await store.load(RUN)})

vi.setConfig({testTimeout:60_000})
describe('#315 L-3: a goal command through a worker\'s view carries its fence',()=>{
 it('a displaced worker\'s view is refused for create, pause and abandon, by the exact bytes, and nothing changes; the holder\'s is decided',async()=>{
  const held=await opened(),job=await started(held.store,held.a),stale=held.view(held.a)
  const b=await takenOver(held),current=held.view(b),sent:string[]=[],cancel=async(input:JobCancelInput)=>{sent.push(input.operation)}
  // Create: the stale view writes no record.
  const before=await snapshot(held.store)
  await refusedStale(goalCommand({store:stale,command:command('create',null,{objective:'objective-1'})}))
  expect(await snapshot(held.store)).toEqual(before);expect(before.goal).toBeUndefined()
  const created=await goalCommand({store:current,command:command('create',null,{objective:'objective-1'})})
  expect(created.goal.objectives.at(-1)).toMatchObject({id:'objective-1',state:'active'})
  // Pause and abandon: refused before the record is read for a repeat, journaled nowhere; the abandon seizes and sends nothing.
  const pause=command('pause',created.goal.revision),abandon=command('abandon',created.goal.revision)
  const made=await snapshot(held.store)
  await refusedStale(goalCommand({store:stale,command:pause}))
  await refusedStale(abandonGoal({store:stale,effectHost:held.host,command:abandon,durationMs:HOUR,cancel}))
  expect(await snapshot(held.store)).toEqual(made);expect(sent).toEqual([])
  expect(made.lease).toMatchObject({owner:'worker-b',epoch:b.epoch,state:'held'})
  // The holder's commands, the same ones, are decided: the refusals journaled nothing a repeat would answer.
  const paused=await goalCommand({store:current,command:pause})
  expect(paused.entry).toMatchObject({command:pause.command,kind:'pause',outcome:'accepted'})
  // The host's own commands, through the store, carry no fence and stay the host's.
  const resumed=await goalCommand({store:held.store,command:command('resume',paused.goal.revision)})
  const gone=await abandonGoal({store:current,effectHost:held.host,command:{...abandon,expectedRevision:resumed.goal.revision},durationMs:HOUR,cancel})
  expect(gone.goal.objectives.at(-1)).toMatchObject({state:'abandoned'});expect(gone.fence?.owner).toBe(JOB_ABANDON_OWNER)
  expect(gone.jobs).toEqual([{job,delivery:'sent'}]);expect(sent).toHaveLength(1)
  // The abandon took the lease: the view it came through is stale now, and its retry goes through the store.
  await refusedStale(abandonGoal({store:current,effectHost:held.host,command:{...abandon,expectedRevision:resumed.goal.revision},durationMs:HOUR,cancel}))
  const retry=await abandonGoal({store:held.store,effectHost:held.host,command:{...abandon,expectedRevision:resumed.goal.revision},durationMs:HOUR,cancel})
  expect(retry.entry).toEqual(gone.entry);expect(sent).toHaveLength(1);expect(retry.jobs).toEqual([{job,delivery:'already-admitted'}])
 })
 it('a current fence of another run is refused resource-mismatch, and nothing changes',async()=>{
  const held=await opened(),created=await goalCommand({store:held.view(held.a),command:command('create',null,{objective:'objective-1'})})
  const before=await snapshot(held.store)
  await refusedStale(goalCommand({store:held.view(held.other),command:command('pause',created.goal.revision)}),'resource-mismatch')
  expect(await snapshot(held.store)).toEqual(before)
 })
 it('the store decides the fence: a transition\'s fence is read and checked by the table, and a table without a checker refuses it',async()=>{
  const held=await opened(),now=()=>new Date(T0)
  const create=command('create',null,{objective:'objective-1'}),context=()=>({waits:[],jobs:[]})
  expect(()=>new GoalTable(now).command({version:1,command:create,fence:held.a},context)).toThrow(expect.objectContaining({name:'GoalCapabilityRefused',reason:'store-unsupported'}))
  expect(()=>new GoalTable(now).command({version:1,command:create,fence:held.a},context)).toThrow(GoalCapabilityRefused)
  // #361 LOW: an asynchronous checker is a check the table cannot wait for, so it refuses, whether the check would pass or not.
  for(const check of [async()=>undefined,async()=>{throw new LeaseRefused('stale-epoch')}]){const table=new GoalTable(now,check);expect(()=>table.command({version:1,command:create,fence:held.a},context)).toThrow(expect.objectContaining({name:'GoalCapabilityRefused',reason:'store-unsupported'}));expect(table.read(RUN)).toBeUndefined()}
  for(const fence of [{...held.a,epoch:'0'},{...held.a,version:2},{...held.a,extra:true},'fence',null])expect(()=>new GoalTable(now,()=>undefined).command({version:1,command:create,fence} as never,context)).toThrow(expect.objectContaining({name:'GoalRefused',reason:'invalid'}))
  const checked:LeaseFence[]=[],table=new GoalTable(now,fence=>{checked.push(fence)})
  expect(table.command({version:1,command:create,fence:held.a},context).entry.outcome).toBe('accepted');expect(checked).toEqual([held.a])
  expect(table.read(RUN)!.journal).toHaveLength(1)
  expect(()=>table.command({version:1,command:command('pause',1),fence:held.other},context)).toThrow(expect.objectContaining({name:'LeaseRefused',reason:'resource-mismatch'}))
  expect(checked).toHaveLength(1);expect(table.read(RUN)!.journal).toHaveLength(1)
 })
})

describe('#315 the abandon\'s remaining edges',()=>{
 /** A host whose cancel is idempotent by operation, as the guide asks: what reached the job's own system, by operation. */
 const idempotent=()=>{const calls:string[]=[],delivered=new Set<string>();return {calls,delivered,cancel:async(input:JobCancelInput)=>{calls.push(input.operation);delivered.add(input.operation)}}}
 it('L-2: a crash after the sends and before their journal, then a retry from a new process, cancels under the same operation',async()=>{
  const held=await opened(),job=await started(held.store,held.a),host=idempotent()
  const created=await goalCommand({store:held.store,command:command('create',null,{objective:'objective-1'})}),abandon=command('abandon',created.goal.revision)
  // The process ends between the cancel's send and the journal of its delivery.
  vi.spyOn(held.store,'recordGoalDeliveries').mockRejectedValueOnce(new Error('the process ended'))
  await expect(abandonGoal({store:held.store,effectHost:held.host,command:abandon,durationMs:HOUR,cancel:host.cancel})).rejects.toThrow('the process ended')
  const crashed=(await held.store.readLease(RUN))!,requested=(await held.store.readJob(RUN,job))!.cancel!.requested
  expect(host.calls).toHaveLength(1);expect(crashed.owner).toBe(JOB_ABANDON_OWNER)
  expect(requested).toMatchObject({by:'command',command:abandon.command,operation:host.calls[0],owner:JOB_ABANDON_OWNER,epoch:crashed.epoch})
  expect((await readGoal(held.store,RUN))!.journal.filter(entry=>entry.outcome==='delivered')).toEqual([])
  // A new process: a new effect host, whose admissions began empty, over the same durable store.
  const retry=await abandonGoal({store:held.store,effectHost:new ReferenceEffectHost(held.store),command:abandon,durationMs:HOUR,cancel:host.cancel})
  expect(host.calls).toHaveLength(2);expect(host.calls[1]).toBe(host.calls[0]);expect(host.delivered.size).toBe(1)
  expect(retry.fence!.epoch).not.toBe(crashed.epoch)
  expect(retry.jobs).toEqual([{job,delivery:'sent'}])
  expect(retry.goal.journal.filter(entry=>entry.outcome==='delivered')).toEqual([expect.objectContaining({command:abandon.command,deliveries:[{job,delivery:'sent'}]})])
  // The job's record holds the request of the attempt that crashed, under its own epoch: the first request stands.
  expect((await held.store.readJob(RUN,job))!.cancel!.requested).toEqual(requested)
 })
 it('L-2: with an effect host whose admissions outlive its process, the retry is admitted once and sends nothing again',async()=>{
  const held=await opened(),job=await started(held.store,held.a),host=idempotent()
  const created=await goalCommand({store:held.store,command:command('create',null,{objective:'objective-1'})}),abandon=command('abandon',created.goal.revision)
  vi.spyOn(held.store,'recordGoalDeliveries').mockRejectedValueOnce(new Error('the process ended'))
  await expect(abandonGoal({store:held.store,effectHost:held.host,command:abandon,durationMs:HOUR,cancel:host.cancel})).rejects.toThrow('the process ended')
  const retry=await abandonGoal({store:held.store,effectHost:held.host,command:abandon,durationMs:HOUR,cancel:host.cancel})
  expect(host.calls).toHaveLength(1);expect(retry.jobs).toEqual([{job,delivery:'already-admitted'}])
 })
 it('L-6: an abandon of a terminal objective refuses objective-terminal and sends nothing; the host\'s abandonJobs stops its jobs',async()=>{
  const held=await opened(),job=await started(held.store,held.a),host=idempotent()
  const created=await goalCommand({store:held.store,command:command('create',null,{objective:'objective-1'})})
  const head=(await held.store.load(RUN))!,at={revision:head.revision,digest:checkpointBytesDigest(head.checkpoint!)}
  const bound=await held.store.commandGoal({version:1,command:command('bind',created.goal.revision,{steps:['step-1'],head:at}),plan:{id:'plan-1',revision:`sha256:${'c'.repeat(64)}`,steps:[{step:'step-1',index:0}]}})
  const achieve=command('achieve',bound.goal.revision,{head:at,waive:[job]})
  const achieved=await held.store.commandGoal({version:1,command:achieve,read:bound.goal.revision})
  expect(achieved.goal.objectives.at(-1)).toMatchObject({state:'achieved',achieved:{waived:[job]}})
  const lease=await held.store.readLease(RUN)
  await expect(abandonGoal({store:held.store,effectHost:held.host,command:command('abandon',achieved.goal.revision),durationMs:HOUR,cancel:host.cancel})).rejects.toEqual(expect.objectContaining({name:'GoalRefused',reason:'objective-terminal'}))
  expect(host.calls).toEqual([]);expect(await held.store.readLease(RUN)).toEqual(lease)
  const stopped=await abandonJobs({store:held.store,effectHost:held.host,resource:RUN,command:'host-stops-the-jobs',durationMs:HOUR,cancel:host.cancel})
  expect(stopped.jobs).toEqual([{job,delivery:'sent'}]);expect(host.calls).toHaveLength(1)
 })
 it('#321 L-3: M-4 holds against a second abandon: the first one\'s delivery does not let a create over the second through',async()=>{
  const held=await opened(),created=await goalCommand({store:held.store,command:command('create',null,{objective:'objective-1'})})
  const first=await abandonGoal({store:held.store,effectHost:held.host,command:command('abandon',created.goal.revision),durationMs:HOUR,cancel:async()=>undefined})
  expect(first.goal.journal.at(-1)).toMatchObject({kind:'abandon',outcome:'delivered'})
  const second=await goalCommand({store:held.store,command:command('create',first.goal.revision,{objective:'objective-2'})})
  // The second abandon's goal transaction is journaled, and the process ends before its deliveries are.
  const gone=await held.store.commandGoal({version:1,command:command('abandon',second.goal.revision)})
  expect(gone.goal.objectives.at(-1)).toMatchObject({id:'objective-2',state:'abandoned'})
  const next=command('create',gone.goal.revision,{objective:'objective-3'})
  await expect(goalCommand({store:held.store,command:next})).rejects.toEqual(expect.objectContaining({name:'GoalRefused',reason:'abandon-unfinished'}))
  expect((await readGoal(held.store,RUN))!.journal.at(-1)).toMatchObject({command:next.command,kind:'create',outcome:'abandon-unfinished'})
 })
})
