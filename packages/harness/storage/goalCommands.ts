import {canonicalJSON} from '../context/chat'
import {readExecution,type ExecutionCheckpoint} from '../execution/state'
import {evidenceFailure} from '../execution/planActions'
import {storageId} from './common'
import type {HistoryRunStore} from './history'
import {GoalCapabilityRefused,GoalRefused,goalCommandDigest,goalReportFromRecord,readGoalCommand,readGoalEntry,readGoalInspect,readRunGoal,type GoalAnswer,type GoalCommand,type GoalFinding,type GoalPlanSteps,type GoalReport,type GoalStore,type RunGoal} from './goal'
import {JOB_PAGE_LIMIT,abandonJobs,admitJobs,type JobAbandonAnswer,type JobCancelSeam,type JobStore,type RunJob} from './job'
import {leasedViewOf,readRunLease,type EffectHost,type LeaseFence,type LeaseStore} from './lease'
import type {RunStore} from './run'
import {checkpointBytesDigest,type WaitStore} from './wait'
import {verifyTaskEvidence} from './writer'
/* The host's goal commands (ADR-0025 decision 8, #230). None calls a model, commits to the run or writes a checkpoint, wait,
 * budget or approval: each is one goal transaction, and an abandon adds the job abandon's own. The package reads what only it
 * can (the head's accepted Plan and its evidence) and hands the store the head it read; the store decides against the head it
 * holds. The Go twin is go/context/run_goal.go. */
type GoalPort=RunStore&GoalStore&LeaseStore&WaitStore&JobStore&{leaseLifetime?:unknown;waitLifetime?:unknown;jobLifetime?:unknown}
const invalid=():never=>{throw new GoalRefused('invalid')}
function goalPort(store:RunStore):GoalPort{
 const base=leasedViewOf(store)?.store??store,held=base as Partial<GoalPort>
 if(held.goalVersion!==1||held.jobVersion!==1||held.waitVersion!==1||held.leaseVersion!==1||['commandGoal','inspectGoal','recordGoalDeliveries','readGoal','listJobs','load'].some(name=>typeof (held as Record<string,unknown>)[name]!=='function')||base.durability==='single-writer-callback')throw new GoalCapabilityRefused('store-unsupported')
 if(held.goalLifetime!=='durable'||held.jobLifetime!=='durable'||held.waitLifetime!=='durable'||held.leaseLifetime!=='durable')throw new GoalCapabilityRefused('store-lifetime')
 return held as GoalPort
}
/** An objective needs a store whose goal record, leases, waits and jobs outlive its process. Anything else refuses by name. */
export function admitGoals(store:RunStore):void{goalPort(store)}
const object=(v:unknown):v is Record<string,unknown>=>v!==null&&typeof v==='object'&&!Array.isArray(v)
function readBack(value:unknown,resource:string):RunGoal{const goal=readRunGoal(structuredClone(value));return goal.resource===resource?goal:invalid()}
/** Whatever the store answers is read through the readers, and it must be this command's own accepted entry. */
function answerOf(value:unknown,command:GoalCommand):GoalAnswer{
 if(!object(value))return invalid()
 const goal=readBack(value.goal,command.resource),entry=readGoalEntry(structuredClone(value.entry))
 if(entry.command!==command.command||entry.kind!==command.kind||entry.digest!==goalCommandDigest(command)||entry.outcome!=='accepted'||!goal.journal.some(row=>canonicalJSON(row)===canonicalJSON(entry)))return invalid()
 const lease=value.lease===undefined||command.kind!=='pause'&&command.kind!=='resume'?undefined:readRunLease(structuredClone(value.lease))
 if(lease&&lease.resource!==command.resource)return invalid()
 return {goal,entry,...(lease?{lease}:{})}
}
const checkpointOf=(bytes:Uint8Array):ExecutionCheckpoint|undefined=>{try{return readExecution(JSON.parse(new TextDecoder('utf-8',{fatal:true}).decode(bytes)))}catch{return undefined}}
/** The run head the command names, as the store holds it now; undefined where it is not that head. */
async function headOf(port:GoalPort,command:GoalCommand,signal?:AbortSignal):Promise<{revision:string;checkpoint:Uint8Array}|undefined>{
 const head=await port.load(command.resource,signal)
 return head&&head.checkpoint!==null&&head.revision===command.head!.revision&&checkpointBytesDigest(head.checkpoint)===command.head!.digest?{revision:head.revision,checkpoint:head.checkpoint}:undefined
}
/** M11: a bind takes the head's accepted Plan, nonempty unique steps with origins, and no step already closed. */
async function bindFinding(port:GoalPort,command:GoalCommand,signal?:AbortSignal):Promise<{plan:GoalPlanSteps}|{refused:GoalFinding}>{
 const head=await headOf(port,command,signal)
 if(!head)return {refused:{reason:'head-changed'}}
 const state=checkpointOf(head.checkpoint),accepted=state?.planState?.accepted
 if(!state||!accepted||!storageId(accepted.id))return {refused:{reason:'plan-invalid'}}
 const rows:{step:string;at:number;index:number}[]=[]
 for(const step of command.steps!){
  const at=state.plan.findIndex(row=>row.id===step),row=state.plan[at]
  if(!row?.origin||row.origin.plan!==accepted.id||row.origin.revision!==accepted.revision)return {refused:{reason:'plan-invalid'}}
  if(row.status==='completed'||row.status==='not-needed')return {refused:{reason:'step-settled',step}}
  rows.push({step,at,index:row.origin.index})
 }
 return {plan:{id:accepted.id,revision:accepted.revision,steps:rows.sort((a,b)=>a.at-b.at).map(({step,index})=>({step,index}))}}
}
async function runJobs(port:GoalPort,resource:string,signal?:AbortSignal):Promise<Map<string,RunJob>>{
 const jobs=new Map<string,RunJob>()
 for(let after:string|null=null;;){const page:{jobs:RunJob[];next:string|null}=await port.listJobs(resource,after,JOB_PAGE_LIMIT,signal);for(const job of page.jobs)jobs.set(job.operation,job);if(page.next===null)return jobs;after=page.next}
}
/** An achievement's evidence, re-verified from the head's own book: the accepted Plan as bound, nothing pending, no required
 * step changed, removed or moved out of the required steps' order (M17), each required step completed with evidence that
 * proves it now, and a delegated one by the writer's history check (M16). */
async function achieveFinding(port:GoalPort,command:GoalCommand,signal?:AbortSignal):Promise<{read:number;refused?:GoalFinding}>{
 const held=await port.readGoal(command.resource,signal)
 if(held===undefined)throw new GoalRefused('no-objective')
 // H-1: the finding is of this record, at this revision: the store takes it only at the command's own expected revision.
 const goal=readBack(held,command.resource),read=goal.revision,bound=goal.objectives.at(-1)?.plan
 if(!bound)return {read,refused:{reason:'plan-unbound'}}
 const head=await headOf(port,command,signal)
 if(!head)return {read,refused:{reason:'head-changed'}}
 const state=checkpointOf(head.checkpoint)
 if(!state)return {read,refused:{reason:'lineage-changed'}}
 if(state.status==='paused'||state.approval?.state==='asked'||state.planState?.pending!==undefined)return {read,refused:{reason:'decision-pending'}}
 const accepted=state.planState?.accepted
 if(!accepted||accepted.id!==bound.id||accepted.revision!==bound.revision)return {read,refused:{reason:'lineage-changed'}}
 // M17: steps the model added, and moves among them, are no departure here; a required step changed, removed or reordered
 // against the other required steps is, whoever approved it.
 const at=bound.steps.map(required=>state.plan.findIndex(row=>row.origin?.plan===accepted.id&&row.origin.revision===accepted.revision&&row.origin.index===required.index))
 if(at.some((index,k)=>index<0||state.plan[index]!.step!==accepted.steps[bound.steps[k]!.index]||k>0&&index<=at[k-1]!))return {read,refused:{reason:'lineage-changed'}}
 for(const [k,required] of bound.steps.entries()){const row=state.plan[at[k]!]!;if(row.status!=='completed'||!row.evidence)return {read,refused:{reason:'step-incomplete',step:required.step}}}
 const proof={book:state.durable??{dispatches:[],observations:[],children:[]},scope:{execution:command.resource,role:'author'},jobs:await runJobs(port,command.resource,signal)}
 const history=port as Partial<HistoryRunStore>
 let delegated:Map<string,{task:string;checkpointDigest:string;resultDigest:string;eventDigest:string;commitId:string}>|null|undefined
 for(const [k,required] of bound.steps.entries()){
  const evidence=state.plan[at[k]!]!.evidence!
  if(!evidence.child){if(evidenceFailure(evidence,proof))return {read,refused:{reason:'evidence-refused',step:required.step}};continue}
  // M16: a delegated step is proven only by the history transaction that recorded its child; a store without history cannot.
  if(history.historyVersion!==1||typeof history.readHistoryHead!=='function')return {read,refused:{reason:'history-unsupported',step:required.step}}
  if(delegated===undefined)delegated=await verifyTaskEvidence(port as unknown as HistoryRunStore,{run:command.resource,revision:head.revision,checkpoint:head.checkpoint,saved:state.planState?.taskSchedule?.rows.filter(row=>row.child?.result)??[],scope:proof.scope},signal).catch(()=>null)
  const row=state.planState?.taskSchedule?.rows.find(item=>item.id===required.step),verified=delegated?.get(required.step)
  if(!verified||!row?.child?.result||evidence.operations.length||evidence.artifacts.length||canonicalJSON(evidence.child)!==canonicalJSON({task:verified.task,checkpointDigest:verified.checkpointDigest,resultDigest:verified.resultDigest,eventDigest:verified.eventDigest,commitId:verified.commitId})||canonicalJSON(row.child.result)!==canonicalJSON({checkpointDigest:verified.checkpointDigest,resultDigest:verified.resultDigest,eventDigest:verified.eventDigest,commitId:verified.commitId}))return {read,refused:{reason:'evidence-refused',step:required.step}}
 }
 return {read}
}
/** A host's goal command: create, bind, achieve, pause, resume, block or unblock (abandon is abandonGoal). One goal transaction:
 * no model call, no run commit, no checkpoint, wait, budget or approval written. Accepted: the record and the command's entry.
 * Refused: GoalRefused, journaled where the run has a goal record. A repeat of the command answers as the first did while its
 * entry is in the journal. */
export async function goalCommand(input:{store:RunStore;command:GoalCommand;signal?:AbortSignal}):Promise<GoalAnswer>{
 const port=goalPort(input.store),command=readGoalCommand(input.command)
 if(command.kind==='abandon')return invalid()
 const found=command.kind==='bind'?await bindFinding(port,command,input.signal):command.kind==='achieve'?await achieveFinding(port,command,input.signal):{}
 return answerOf(await port.commandGoal({version:1,command,...found},input.signal),command)
}
export interface GoalAbandonAnswer extends GoalAnswer {fence?:LeaseFence;jobs:JobAbandonAnswer['jobs']}
/** ADR-0025 decision 8: abandon the objective, then ask a cancel of every started job of the run (abandonJobs: the lease under
 * its reserved owner at a new epoch, the command standing for the person's approval). The objective's record holds what the
 * abandon left, the run's waits not resumed or ended and its jobs not settled, and each attempt's deliveries are journaled:
 * the job abandon closes and journals nothing itself. A retry of the command is answered from the record and, while the objective
 * it abandoned is still the current one, sends again only what the effect host has not admitted; after another objective is
 * created it refuses revision-changed and sends nothing (#313 H-2). None of it proves anything stopped. */
export async function abandonGoal(input:{store:RunStore;effectHost:EffectHost;command:GoalCommand;durationMs:number;cancel:JobCancelSeam['cancel'];signal?:AbortSignal}):Promise<GoalAbandonAnswer>{
 const port=goalPort(input.store),command=readGoalCommand(input.command)
 if(command.kind!=='abandon')return invalid()
 admitJobs({store:port,effectHost:input.effectHost})
 const answer=answerOf(await port.commandGoal({version:1,command},input.signal),command)
 // H-2: a repeat is answered from the journal whatever has happened since. Only while the objective this command abandoned is
 // still the current one does it ask anything of the run's jobs; otherwise nothing is sent, no lease is taken and nothing is
 // journaled.
 if(answer.goal.objectives.at(-1)?.abandoned?.command!==command.command)throw new GoalRefused('revision-changed')
 const taken=await abandonJobs({store:port,effectHost:input.effectHost,resource:command.resource,command:command.command,durationMs:input.durationMs,cancel:input.cancel,...(input.signal?{signal:input.signal}:{})})
 const goal=readBack(await port.recordGoalDeliveries({version:1,resource:command.resource,command:command.command,deliveries:taken.jobs},input.signal),command.resource)
 return {goal,entry:answer.entry,...(taken.fence?{fence:taken.fence}:{}),jobs:taken.jobs}
}
/** M12: the record, with the inspection counted and no revision moved. Undefined where the run has none. */
export async function inspectGoal(input:{store:RunStore;resource:string;command:string;actor:string;signal?:AbortSignal}):Promise<RunGoal|undefined>{
 const held=readGoalInspect({version:1,resource:input.resource,command:input.command,actor:input.actor}),goal=await goalPort(input.store).inspectGoal(held,input.signal)
 return goal===undefined?undefined:readBack(goal,held.resource)
}
export async function readGoal(store:RunStore,resource:string,signal?:AbortSignal):Promise<RunGoal|undefined>{const goal=await goalPort(store).readGoal(resource,signal);return goal===undefined?undefined:readBack(goal,resource)}
export async function goalReport(store:RunStore,resource:string,signal?:AbortSignal):Promise<GoalReport|undefined>{const goal=await readGoal(store,resource,signal);return goal===undefined?undefined:goalReportFromRecord(goal)}
