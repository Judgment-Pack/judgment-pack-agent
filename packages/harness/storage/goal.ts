import {AIFailureError,failure} from '../failures'
import {canonicalJSON} from '../context/chat'
import {sha256} from '../records'
import {planStepId} from '../execution/plan'
import {storageId} from './common'
import {JOB_RUN_LIMIT,type JobDelivery,type JobOutcome,type JobState,type RunJob} from './job'
import type {RunLease} from './lease'
import {checkpointBytesDigest,terminalWait,type RunWait,type WaitReason,type WaitState} from './wait'
/* One durable goal (ADR-0025 decision 8, #230; contract/schemas/run-goal.json). A run may have one objective at a time, kept
 * by the store beside the run, never in its checkpoint or event log, and changed only by host commands: no model call and no
 * run commit. Each command carries its id, its actor and the record's expected revision, and the store decides it in one
 * transaction and journals it, accepted or refused. Paused is a disposition, never a state. The package checks what only it
 * can read (the head's accepted Plan and its evidence) and the store checks that the head it read is still the head. The host
 * owns scheduling, clocks, notifications and who may issue a command. The Go twin is go/context/run_goal.go. */
export const GOAL_RECORD_VERSION=1 as const
/** A run's objectives, the current one last. History is never overwritten: each earlier one is kept whole. */
export const GOAL_OBJECTIVE_LIMIT=8
/** M12: the journal's bound. Past it the oldest refused entry goes first, then the oldest entry, and `discarded` counts them,
 * so no command is ever refused for size. */
export const GOAL_JOURNAL_LIMIT=64
export const GOAL_STEP_LIMIT=20
export const GOAL_WAIT_LIMIT=16
export type GoalState='active'|'achieved'|'abandoned'|'blocked'
export type GoalCommandKind='create'|'bind'|'achieve'|'pause'|'resume'|'block'|'unblock'|'abandon'
export type GoalLifetime='durable'|'process'
export type GoalRefusalReason='invalid'|'conflict'|'revision-changed'|'no-objective'|'objective-exists'|'objective-reused'|'goals-exhausted'|'objective-terminal'|'objective-paused'|'not-paused'|'objective-blocked'|'not-blocked'|'plan-bound'|'plan-unbound'|'plan-invalid'|'plan-reused'|'step-settled'|'head-changed'|'lineage-changed'|'decision-pending'|'wait-pending'|'step-incomplete'|'evidence-refused'|'history-unsupported'|'jobs-unsettled'|'waiver-invalid'|'wait-unresolved'|'lease-required'|'abandon-unfinished'
export type GoalCapabilityReason='store-unsupported'|'store-lifetime'
export const GOAL_STATES:readonly GoalState[]=['active','achieved','abandoned','blocked']
export const GOAL_COMMAND_KINDS:readonly GoalCommandKind[]=['create','bind','achieve','pause','resume','block','unblock','abandon']
export const GOAL_REFUSAL_REASONS:readonly GoalRefusalReason[]=['invalid','conflict','revision-changed','no-objective','objective-exists','objective-reused','goals-exhausted','objective-terminal','objective-paused','not-paused','objective-blocked','not-blocked','plan-bound','plan-unbound','plan-invalid','plan-reused','step-settled','head-changed','lineage-changed','decision-pending','wait-pending','step-incomplete','evidence-refused','history-unsupported','jobs-unsettled','waiver-invalid','wait-unresolved','lease-required','abandon-unfinished']
export const GOAL_CAPABILITY_REASONS:readonly GoalCapabilityReason[]=['store-unsupported','store-lifetime']
/** What only the package can find, from the head it read: the store records it as the command's refusal. */
export const GOAL_FINDINGS:readonly GoalRefusalReason[]=['head-changed','plan-invalid','step-settled','decision-pending','lineage-changed','step-incomplete','evidence-refused','history-unsupported','plan-unbound']
/** A refusal that names a step. */
const STEP_FINDINGS:readonly GoalRefusalReason[]=['step-settled','step-incomplete','evidence-refused','history-unsupported']
/** Neither is journaled: an invalid command has no id to journal, and a conflict is another command's id. */
const UNJOURNALED:readonly GoalRefusalReason[]=['invalid','conflict']
const WAIT_REASONS:readonly WaitReason[]=['approval','budget','question','reauthorization','job']
const LEFT_WAIT_STATES:readonly WaitState[]=['parked','woken','timed-out']
const JOB_STATES:readonly JobState[]=['unproven','started','not-started','completed']
/** A job a goal lists is one that did not succeed: its outcome is never succeeded. */
const JOB_OUTCOMES:readonly JobOutcome[]=['failed','cancelled','unknown']
const JOB_DELIVERIES:readonly JobDelivery[]=['sent','already-admitted','failed','undelivered','no-handle','settled','contradicted','acknowledged','invalid']
export interface GoalStamp {command:string;actor:string;at:string}
/** A required step: its plan step id and its origin's index in the accepted Plan, which survives a reorder. */
export interface GoalStep {step:string;index:number}
export interface GoalPlanSteps {id:string;revision:string;steps:GoalStep[]}
export interface GoalPlan extends GoalPlanSteps {bound:GoalStamp}
/** The run head a bind or an achievement was decided against: its revision and the digest of its checkpoint bytes. */
export interface GoalHead {revision:string;digest:string}
export interface GoalJob {job:string;state:JobState;outcome?:JobOutcome;contradicted?:true}
export interface GoalLeftWait {wait:string;reason:WaitReason;state:WaitState}
export interface GoalBlock extends GoalStamp {condition:string}
/** Unsettled jobs a command waived by id, and the settled ones that did not succeed, contradictions included. */
export interface GoalAchievement extends GoalStamp {head:GoalHead;waived?:string[];unsuccessful?:GoalJob[]}
/** What the abandon left: the run's waits not resumed or ended, and its jobs not settled, as they were in its transaction. */
export interface GoalAbandonment extends GoalStamp {waits:GoalLeftWait[];jobs:GoalJob[]}
export interface Objective {id:string;state:GoalState;created:GoalStamp;plan?:GoalPlan;paused?:GoalStamp;blocked?:GoalBlock;achieved?:GoalAchievement;abandoned?:GoalAbandonment}
export interface GoalDelivery {job:string;delivery:JobDelivery}
export interface GoalEntry {command:string;kind:GoalCommandKind;actor:string;at:string;objective:string;outcome:'accepted'|'delivered'|GoalRefusalReason;revision:number;digest:string;step?:string;deliveries?:GoalDelivery[]}
export interface GoalInspections {count:number;last:{command:string;actor:string;at:string}}
export interface RunGoal {version:1;kind:'goal';resource:string;revision:number;objectives:Objective[];journal:GoalEntry[];discarded?:number;inspections?:GoalInspections}
export interface GoalCommand {version:1;resource:string;command:string;actor:string;expectedRevision:number|null;kind:GoalCommandKind;objective?:string;steps?:string[];head?:GoalHead;waive?:string[];condition?:string}
export interface GoalFinding {reason:GoalRefusalReason;step?:string}
/** What the store is handed: the host's command, and what the package found from the head (a bind's steps, or a refusal). An
 * achievement carries `read`, the goal record's revision its finding was computed from: the store refuses revision-changed unless
 * it is the command's expected revision, so a finding read before another command landed is never taken for that command's record
 * (#313 H-1). */
export interface GoalTransition {version:1;command:GoalCommand;plan?:GoalPlanSteps;refused?:GoalFinding;read?:number}
export interface GoalInspect {version:1;resource:string;command:string;actor:string}
export interface GoalDeliveries {version:1;resource:string;command:string;deliveries:GoalDelivery[]}
/** An accepted command's answer. `lease` (a pause or a resume): the run's lease as the store holds it, since a held lease
 * runs on to its expiry and paused is not stopped. */
export interface GoalAnswer {goal:RunGoal;entry:GoalEntry;lease?:RunLease}
export type GoalReportEntry=Omit<GoalEntry,'digest'>
export interface GoalReport {version:1;resource:string;revision:number;objectives:Objective[];entries:GoalReportEntry[];discarded:number;inspections?:GoalInspections}

/** A named goal refusal. Where the run has a goal record the store journaled it, and nothing else changed. */
export class GoalRefused extends AIFailureError {
 constructor(readonly reason:GoalRefusalReason){super({...failure('stale-state','storage','not-started'),detail:`GoalRefused: ${reason}.`});this.name='GoalRefused'}
 record():{version:1;reason:GoalRefusalReason}{return {version:1,reason:this.reason}}
}
/** A store that cannot give the guarantee refuses by name before any command; it never pretends. */
export class GoalCapabilityRefused extends AIFailureError {
 constructor(readonly reason:GoalCapabilityReason){super({...failure('configuration','storage','not-started'),detail:`GoalCapabilityRefused: ${reason}.`});this.name='GoalCapabilityRefused'}
 record():{version:1;reason:GoalCapabilityReason}{return {version:1,reason:this.reason}}
}
const TIME=/^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{3}Z$/,DIGEST=/^sha256:[a-f0-9]{64}$/
const object=(v:unknown):v is Record<string,unknown>=>v!==null&&typeof v==='object'&&!Array.isArray(v)&&(Object.getPrototypeOf(v)===Object.prototype||Object.getPrototypeOf(v)===null)
const invalid=():never=>{throw new GoalRefused('invalid')}
function shape(v:unknown,required:readonly string[],optional:readonly string[]=[]):Record<string,unknown>{
 if(!object(v)||required.some(k=>!Object.hasOwn(v,k))||Object.keys(v).some(k=>!required.includes(k)&&!optional.includes(k)))return invalid()
 for(const k of optional)if(Object.hasOwn(v,k)&&v[k]===undefined)return invalid()
 return v
}
const id=(v:unknown):string=>storageId(v)?v:invalid()
const digest=(v:unknown):string=>typeof v==='string'&&DIGEST.test(v)?v:invalid()
const one=<T extends string>(v:unknown,values:readonly T[]):T=>values.includes(v as T)?v as T:invalid()
const time=(v:unknown):string=>{if(typeof v!=='string'||!TIME.test(v))return invalid();const ms=Date.parse(v);return Number.isSafeInteger(ms)&&new Date(ms).toISOString()===v?v:invalid()}
const count=(v:unknown,min:number,max=Number.MAX_SAFE_INTEGER):number=>typeof v==='number'&&Number.isSafeInteger(v)&&v>=min&&v<=max?v:invalid()
function list<T>(v:unknown,min:number,max:number,read:(item:unknown)=>T,key?:(item:T)=>string):T[]{
 if(!Array.isArray(v)||v.length<min||v.length>max)return invalid()
 const out=v.map(read)
 if(key&&new Set(out.map(key)).size!==out.length)return invalid()
 return out
}
const stepId=(v:unknown):string=>planStepId(v)?v:invalid()
function readStamp(v:unknown,extra:readonly string[]=[],optional:readonly string[]=[]):GoalStamp&Record<string,unknown>{const held=shape(v,['command','actor','at',...extra],optional);return {...held,command:id(held.command),actor:id(held.actor),at:time(held.at)}}
const stamp=(v:unknown):GoalStamp=>{const held=readStamp(v);return {command:held.command,actor:held.actor,at:held.at}}
export function readGoalHead(v:unknown):GoalHead{const held=shape(v,['revision','digest']);return {revision:id(held.revision),digest:digest(held.digest)}}
function readSteps(v:unknown):GoalStep[]{
 const steps=list(v,1,GOAL_STEP_LIMIT,item=>{const held=shape(item,['step','index']);return {step:stepId(held.step),index:count(held.index,0,GOAL_STEP_LIMIT-1)}},step=>step.step)
 if(new Set(steps.map(step=>step.index)).size!==steps.length)return invalid()
 return steps
}
export function readGoalPlanSteps(v:unknown):GoalPlanSteps{const held=shape(v,['id','revision','steps']);return {id:id(held.id),revision:digest(held.revision),steps:readSteps(held.steps)}}
function readPlan(v:unknown):GoalPlan{const held=shape(v,['id','revision','steps','bound']);return {id:id(held.id),revision:digest(held.revision),steps:readSteps(held.steps),bound:stamp(held.bound)}}
function readJob(v:unknown):GoalJob{
 const held=shape(v,['job','state'],['outcome','contradicted']),job:GoalJob={job:digest(held.job),state:one(held.state,JOB_STATES)}
 // An outcome exactly when completed; a contradiction only on a job closed as not started.
 if((job.state==='completed')!==(held.outcome!==undefined)||held.contradicted!==undefined&&(held.contradicted!==true||job.state!=='not-started'))return invalid()
 if(held.outcome!==undefined)job.outcome=one(held.outcome,JOB_OUTCOMES)
 if(held.contradicted===true)job.contradicted=true
 return job
}
const jobKey=(job:GoalJob)=>job.job
function readObjective(v:unknown):Objective{
 const held=shape(v,['id','state','created'],['plan','paused','blocked','achieved','abandoned'])
 const objective:Objective={id:id(held.id),state:one(held.state,GOAL_STATES),created:stamp(held.created)}
 if(held.plan!==undefined)objective.plan=readPlan(held.plan)
 if(held.paused!==undefined)objective.paused=stamp(held.paused)
 if(held.blocked!==undefined){const block=readStamp(held.blocked,['condition']);objective.blocked={command:block.command,actor:block.actor,at:block.at,condition:id(block.condition)}}
 if(held.achieved!==undefined){
  const done=readStamp(held.achieved,['head'],['waived','unsuccessful'])
  objective.achieved={command:done.command,actor:done.actor,at:done.at,head:readGoalHead(done.head)}
  if(done.waived!==undefined)objective.achieved.waived=list(done.waived,1,JOB_RUN_LIMIT,digest,job=>job)
  if(done.unsuccessful!==undefined){objective.achieved.unsuccessful=list(done.unsuccessful,1,JOB_RUN_LIMIT,readJob,jobKey);if(objective.achieved.unsuccessful.some(job=>job.state!=='not-started'&&(job.state!=='completed'||job.outcome==='succeeded')))return invalid()}
 }
 if(held.abandoned!==undefined){
  const left=readStamp(held.abandoned,['waits','jobs'])
  objective.abandoned={command:left.command,actor:left.actor,at:left.at,waits:list(left.waits,0,GOAL_WAIT_LIMIT,item=>{const wait=shape(item,['wait','reason','state']);return {wait:id(wait.wait),reason:one(wait.reason,WAIT_REASONS),state:one(wait.state,LEFT_WAIT_STATES)}},wait=>wait.wait),jobs:list(left.jobs,0,JOB_RUN_LIMIT,readJob,jobKey)}
  if(objective.abandoned.jobs.some(job=>job.state!=='unproven'&&job.state!=='started'))return invalid()
 }
 // Each state holds exactly what says it. Paused is a disposition of an open objective; a terminal one has none (L2).
 const s=objective.state,proves=s==='active'?!objective.blocked&&!objective.achieved&&!objective.abandoned:s==='blocked'?!!objective.blocked&&!objective.achieved&&!objective.abandoned:s==='achieved'?!!objective.achieved&&!!objective.plan&&!objective.blocked&&!objective.abandoned&&!objective.paused:!!objective.abandoned&&!objective.blocked&&!objective.achieved&&!objective.paused
 return proves?objective:invalid()
}
const terminal=(state:GoalState)=>state==='achieved'||state==='abandoned'
function readEntry(v:unknown,report=false):GoalEntry{
 const held=shape(v,['command','kind','actor','at','objective','outcome','revision',...(report?[]:['digest'])],['step','deliveries'])
 const outcome=one(held.outcome,['accepted','delivered',...GOAL_REFUSAL_REASONS.filter(reason=>!UNJOURNALED.includes(reason))] as const)
 const entry:GoalEntry={command:id(held.command),kind:one(held.kind,GOAL_COMMAND_KINDS),actor:id(held.actor),at:time(held.at),objective:id(held.objective),outcome,revision:count(held.revision,1),digest:report?'':digest(held.digest)}
 // Deliveries exactly on an abandon's delivered entry; a step only on a refusal about one.
 if((outcome==='delivered')!==(held.deliveries!==undefined)||outcome==='delivered'&&entry.kind!=='abandon'||held.step!==undefined&&!STEP_FINDINGS.includes(outcome as GoalRefusalReason))return invalid()
 if(held.step!==undefined)entry.step=stepId(held.step)
 if(held.deliveries!==undefined)entry.deliveries=readDeliveryList(held.deliveries)
 return entry
}
export const readGoalEntry=(v:unknown):GoalEntry=>readEntry(v)
const readDeliveryList=(v:unknown):GoalDelivery[]=>list(v,0,JOB_RUN_LIMIT,item=>{const held=shape(item,['job','delivery']);return {job:digest(held.job),delivery:one(held.delivery,JOB_DELIVERIES)}},delivery=>delivery.job)
function readInspections(v:unknown):GoalInspections{const held=shape(v,['count','last']),last=shape(held.last,['command','actor','at']);return {count:count(held.count,1),last:{command:id(last.command),actor:id(last.actor),at:time(last.at)}}}
function readObjectives(v:unknown):Objective[]{
 const objectives=list(v,1,GOAL_OBJECTIVE_LIMIT,readObjective,objective=>objective.id)
 if(objectives.slice(0,-1).some(objective=>!terminal(objective.state)))return invalid()
 const plans=objectives.filter(objective=>objective.plan).map(objective=>canonicalJSON([objective.plan!.id,objective.plan!.revision]))
 return new Set(plans).size===plans.length?objectives:invalid()
}
/** Reads a version 1 goal record and holds every rule the schema cannot state: one open objective at most, and only the last;
 * identities never reused; no Plan bound twice in a run (M11); each state holding exactly what proves it. */
export function readRunGoal(v:unknown):RunGoal{
 const held=shape(v,['version','kind','resource','revision','objectives','journal'],['discarded','inspections'])
 if(held.version!==GOAL_RECORD_VERSION||held.kind!=='goal')return invalid()
 const goal:RunGoal={version:1,kind:'goal',resource:id(held.resource),revision:count(held.revision,1),objectives:readObjectives(held.objectives),journal:list(held.journal,0,GOAL_JOURNAL_LIMIT,entry=>readEntry(entry))}
 if(held.discarded!==undefined)goal.discarded=count(held.discarded,1)
 if(held.inspections!==undefined)goal.inspections=readInspections(held.inspections)
 return goal
}
/** Which members each command carries. */
const MEMBERS:Record<GoalCommandKind,[readonly string[],readonly string[]]>={create:[['objective'],[]],bind:[['steps','head'],[]],achieve:[['head'],['waive']],pause:[[],[]],resume:[[],[]],block:[['condition'],[]],unblock:[[],[]],abandon:[[],[]]}
export function readGoalCommand(v:unknown):GoalCommand{
 if(!object(v))return invalid()
 const kind=one(v.kind,GOAL_COMMAND_KINDS),[required,optional]=MEMBERS[kind]
 const held=shape(v,['version','resource','command','actor','expectedRevision','kind',...required],optional)
 if(held.version!==GOAL_RECORD_VERSION)return invalid()
 const command:GoalCommand={version:1,resource:id(held.resource),command:id(held.command),actor:id(held.actor),expectedRevision:held.expectedRevision===null?null:count(held.expectedRevision,1),kind}
 if(held.objective!==undefined)command.objective=id(held.objective)
 if(held.steps!==undefined)command.steps=list(held.steps,1,GOAL_STEP_LIMIT,stepId,step=>step)
 if(held.head!==undefined)command.head=readGoalHead(held.head)
 if(held.waive!==undefined)command.waive=list(held.waive,1,JOB_RUN_LIMIT,digest,job=>job)
 if(held.condition!==undefined)command.condition=id(held.condition)
 return command
}
export function readGoalTransition(v:unknown):GoalTransition{
 const held=shape(v,['version','command'],['plan','refused','read'])
 if(held.version!==GOAL_RECORD_VERSION)return invalid()
 const command=readGoalCommand(held.command),transition:GoalTransition={version:1,command}
 // An achievement carries the revision it read, and nothing else does.
 if((held.read!==undefined)!==(command.kind==='achieve'))return invalid()
 if(held.read!==undefined)transition.read=count(held.read,1)
 if(held.plan!==undefined)transition.plan=readGoalPlanSteps(held.plan)
 if(held.refused!==undefined){const refused=shape(held.refused,['reason'],['step']),reason=one(refused.reason,GOAL_FINDINGS);transition.refused={reason,...(refused.step===undefined?{}:{step:STEP_FINDINGS.includes(reason)?stepId(refused.step):invalid()})}}
 // A bind carries its steps or what refused them, never both; an achievement may carry a finding; nothing else carries either.
 if(command.kind==='bind'?(transition.plan===undefined)===(transition.refused===undefined):transition.plan!==undefined||transition.refused!==undefined&&command.kind!=='achieve')return invalid()
 return transition
}
export function readGoalInspect(v:unknown):GoalInspect{const held=shape(v,['version','resource','command','actor']);if(held.version!==GOAL_RECORD_VERSION)return invalid();return {version:1,resource:id(held.resource),command:id(held.command),actor:id(held.actor)}}
export function readGoalDeliveries(v:unknown):GoalDeliveries{const held=shape(v,['version','resource','command','deliveries']);if(held.version!==GOAL_RECORD_VERSION)return invalid();return {version:1,resource:id(held.resource),command:id(held.command),deliveries:readDeliveryList(held.deliveries)}}
export function readGoalRefusal(v:unknown):{version:1;reason:GoalRefusalReason}{const held=shape(v,['version','reason']);if(held.version!==GOAL_RECORD_VERSION)return invalid();return {version:1,reason:one(held.reason,GOAL_REFUSAL_REASONS)}}
export function readGoalCapabilityRefusal(v:unknown):{version:1;reason:GoalCapabilityReason}{const held=shape(v,['version','reason']);if(held.version!==GOAL_RECORD_VERSION)return invalid();return {version:1,reason:one(held.reason,GOAL_CAPABILITY_REASONS)}}
/** A command's digest: a repeat of its id must carry exactly this. */
export const goalCommandDigest=(command:GoalCommand)=>`sha256:${sha256(canonicalJSON(readGoalCommand(command)))}`
export const goalDeliveryDigest=(input:GoalDeliveries)=>{const held=readGoalDeliveries(input);return `sha256:${sha256(canonicalJSON({command:held.command,deliveries:held.deliveries}))}`}
export const goalJob=(job:RunJob):GoalJob=>({job:job.job,state:job.state,...(job.completion?{outcome:job.completion.outcome}:{}),...(job.contradictions?{contradicted:true as const}:{})})
const unsettledJob=(job:RunJob)=>job.state==='unproven'||job.state==='started'
const unresolvedWait=(wait:RunWait|undefined)=>wait===undefined||wait.state!=='resumed'&&!terminalWait(wait)
/** M12: an entry past the bound pushes out the oldest refused entry, else the oldest, and is counted. Nothing is refused. */
function append(goal:RunGoal,entry:GoalEntry):void{
 goal.journal.push(entry)
 if(goal.journal.length<=GOAL_JOURNAL_LIMIT)return
 const refused=goal.journal.findIndex(row=>row.outcome!=='accepted'&&row.outcome!=='delivered')
 goal.journal.splice(refused<0?0:refused,1);goal.discarded=(goal.discarded??0)+1
}
/** What a goal transaction reads of the run, in the same transaction. */
export interface GoalContext {head?:{revision:string;checkpoint:Uint8Array|null};waits:readonly RunWait[];jobs:readonly RunJob[];lease?:RunLease}

/** The goal transitions, held in memory. MemoryRunStore calls them inside its own serialized transactions, with the run's head,
 * waits, jobs and lease as they are in that transaction; a host store kind implements the same transitions. */
export class GoalTable {
 private rows=new Map<string,RunGoal>()
 constructor(private readonly clock:()=>Date=()=>new Date()){}
 private now():string{const t=this.clock();const ms=t instanceof Date?t.getTime():NaN;return Number.isSafeInteger(ms)?new Date(ms).toISOString():invalid()}
 read(resource:string):RunGoal|undefined{const row=this.rows.get(id(resource));return row?structuredClone(row):undefined}
 /** M8: a paused objective's run is leased by no acquisition and no resume. A held lease runs on. */
 admitLease(resource:string):void{if(this.rows.get(resource)?.objectives.at(-1)?.paused)throw new GoalRefused('objective-paused')}
 /** An objective requires a leased run, chosen at create: from then on an unfenced commit of the run is refused. */
 admitUnfenced(resource:string):void{if(this.rows.has(resource))throw new GoalRefused('lease-required')}
 command(input:GoalTransition,context:(resource:string)=>GoalContext):GoalAnswer{
  const held=readGoalTransition(input),c=held.command,row=this.rows.get(c.resource),digested=goalCommandDigest(c)
  // A repeat of a command id: the same command is answered as it was; another under that id is conflict.
  const prior=row?.journal.find(entry=>entry.command===c.command&&entry.outcome!=='delivered')
  if(prior){if(prior.digest!==digested)throw new GoalRefused('conflict');if(prior.outcome!=='accepted')throw new GoalRefused(prior.outcome as GoalRefusalReason);return this.answer(row!,prior,c,context(c.resource))}
  const at=this.now(),current=row?.objectives.at(-1),by:GoalStamp={command:c.command,actor:c.actor,at}
  const refuse=(reason:GoalRefusalReason,step?:string):never=>{
   if(row)append(row,{command:c.command,kind:c.kind,actor:c.actor,at,objective:c.kind==='create'?c.objective!:current!.id,outcome:reason,revision:row.revision,digest:digested,...(step===undefined?{}:{step})})
   throw new GoalRefused(reason)
  }
  if((row?.revision??null)!==c.expectedRevision)return refuse('revision-changed')
  // H-1: an achievement's finding is the record's at the command's revision, or it is no finding of this record.
  if(c.kind==='achieve'&&held.read!==c.expectedRevision)return refuse('revision-changed')
  const run=context(c.resource)
  const headIs=(head:GoalHead)=>run.head!==undefined&&run.head.checkpoint!==null&&run.head.revision===head.revision&&checkpointBytesDigest(run.head.checkpoint)===head.digest
  let next:Objective
  if(c.kind==='create'){
   if(current&&!terminal(current.state))return refuse('objective-exists')
   if(row?.objectives.some(objective=>objective.id===c.objective))return refuse('objective-reused')
   if((row?.objectives.length??0)>=GOAL_OBJECTIVE_LIMIT)return refuse('goals-exhausted')
   // #318 M-4: an abandon with no deliveries journaled is unfinished: its jobs may never have been asked to stop, and once another
   // objective exists its retry cannot finish it. The host retries the abandon first.
   if(current?.state==='abandoned'&&!row!.journal.some(entry=>entry.command===current.abandoned!.command&&entry.outcome==='delivered'))return refuse('abandon-unfinished')
   // M10: a wait parked before the abandon, or after it (an abandon closes nothing), is resumed or ended before another
   // objective is created over a terminal one.
   if(current?.abandoned?.waits.some(left=>unresolvedWait(run.waits.find(wait=>wait.wait===left.wait))))return refuse('wait-unresolved')
   if(current&&run.waits.some(wait=>unresolvedWait(wait)))return refuse('wait-unresolved')
   next={id:c.objective!,state:'active',created:by}
  }else{
   if(!current)return refuse('no-objective')
   if(terminal(current.state))return refuse('objective-terminal')
   next=structuredClone(current)
   if(c.kind==='pause'){if(next.paused)return refuse('objective-paused');next.paused=by}
   else if(c.kind==='resume'){if(!next.paused)return refuse('not-paused');delete next.paused}
   else if(c.kind==='block'){if(next.state==='blocked')return refuse('objective-blocked');next.state='blocked';next.blocked={...by,condition:c.condition!}}
   else if(c.kind==='unblock'){if(next.state!=='blocked')return refuse('not-blocked');next.state='active';delete next.blocked}
   else if(c.kind==='bind'){
    if(next.plan)return refuse('plan-bound')
    if(!headIs(c.head!))return refuse('head-changed')
    if(held.refused)return refuse(held.refused.reason,held.refused.step)
    // M11: a Plan an earlier objective of this run bound is never bound again.
    if(row!.objectives.some(objective=>objective.plan?.id===held.plan!.id&&objective.plan.revision===held.plan!.revision))return refuse('plan-reused')
    next.plan={...structuredClone(held.plan!),bound:by}
   }else if(c.kind==='achieve'){
    if(next.state==='blocked')return refuse('objective-blocked')
    if(!next.plan)return refuse('plan-unbound')
    if(!headIs(c.head!))return refuse('head-changed')
    if(held.refused)return refuse(held.refused.reason,held.refused.step)
    if(run.waits.some(wait=>unresolvedWait(wait)))return refuse('wait-pending')
    // Unsettled jobs block unless waived by id; settled ones that did not succeed are listed, contradictions included.
    const open=run.jobs.filter(unsettledJob),waived=c.waive??[]
    if(waived.some(job=>!open.some(row=>row.job===job)))return refuse('waiver-invalid')
    if(open.some(row=>!waived.includes(row.job)))return refuse('jobs-unsettled')
    const unsuccessful=run.jobs.filter(job=>job.state==='not-started'||job.state==='completed'&&job.completion!.outcome!=='succeeded').map(goalJob)
    next.state='achieved';delete next.paused
    next.achieved={...by,head:{...c.head!},...(waived.length?{waived:[...waived]}:{}),...(unsuccessful.length?{unsuccessful}:{})}
   }else{
    // The abandon closes nothing: what it left is recorded, so that a later create can be held to it (M10).
    const waits=run.waits.filter(wait=>unresolvedWait(wait)).map(wait=>({wait:wait.wait,reason:wait.reason,state:wait.state}))
    next.state='abandoned';delete next.paused;delete next.blocked
    next.abandoned={...by,waits,jobs:run.jobs.filter(unsettledJob).map(goalJob)}
   }
  }
  const goal:RunGoal=row??{version:1,kind:'goal',resource:c.resource,revision:0,objectives:[],journal:[]}
  if(c.kind==='create')goal.objectives.push(next);else goal.objectives[goal.objectives.length-1]=next
  goal.revision++
  const entry:GoalEntry={command:c.command,kind:c.kind,actor:c.actor,at,objective:next.id,outcome:'accepted',revision:goal.revision,digest:digested}
  append(goal,entry);this.rows.set(c.resource,goal)
  return this.answer(goal,entry,c,run)
 }
 private answer(goal:RunGoal,entry:GoalEntry,command:GoalCommand,run:GoalContext):GoalAnswer{return {goal:structuredClone(goal),entry:structuredClone(entry),...((command.kind==='pause'||command.kind==='resume')&&run.lease?{lease:structuredClone(run.lease)}:{})}}
 /** M12: a read, coalesced into a count and a last row; it moves no revision. A repeat of the last id is not counted again. */
 inspect(input:GoalInspect):RunGoal|undefined{
  const held=readGoalInspect(input),row=this.rows.get(held.resource)
  if(!row)return undefined
  if(row.inspections?.last.command!==held.command)row.inspections={count:(row.inspections?.count??0)+1,last:{command:held.command,actor:held.actor,at:this.now()}}
  return structuredClone(row)
 }
 /** What became of each job after the abandon that abandoned the current objective: its deliveries live only in its answer,
  * so each attempt of that command is journaled. */
 deliver(input:GoalDeliveries):RunGoal{
  const held=readGoalDeliveries(input),row=this.rows.get(held.resource),current=row?.objectives.at(-1)
  if(!row||!current?.abandoned||current.abandoned.command!==held.command)return invalid()
  append(row,{command:held.command,kind:'abandon',actor:current.abandoned.actor,at:this.now(),objective:current.id,outcome:'delivered',revision:row.revision,digest:goalDeliveryDigest(held),deliveries:structuredClone(held.deliveries)})
  return structuredClone(row)
 }
}

/** The goal port a store kind implements beside its lease, wait and job ports. Every transition is atomic with the store's
 * commits and checked against its own clock, and the record survives the store's restarts. commandGoal decides one command in
 * one transaction against the record's revision, the run's head, waits, jobs and lease as they are in it, and journals it,
 * accepted or refused; a repeat of a command id answers as the first did, with the record as it is now, while its entry is in
 * the journal, and another command under it is conflict. An achievement's finding is taken only from the record at the command's
 * revision (`read`). The store trusts the transition it is handed: a direct caller of commandGoal can claim any `read` and any
 * finding, so only the package's goalCommand composes them from what it read (#318 L-11). While the
 * current objective is paused, acquireLease and resumeWait refuse objective-paused; once the run has a goal record an unfenced
 * commit refuses lease-required. The package checks none of this in a host store; runStoreGoalConformance
 * (storage/goalConformance) does. */
export interface GoalStore {
 readonly goalVersion:1
 /** durable: the record survives the store's process. process: it ends with it (memory test kinds). */
 readonly goalLifetime:GoalLifetime
 /** Accepted: the record and the command's entry. Refused: GoalRefused, journaled where the run has a goal record. */
 commandGoal(input:GoalTransition,signal?:AbortSignal):Promise<GoalAnswer>
 /** The record, counting the inspection; undefined, and nothing written, where the run has none. */
 inspectGoal(input:GoalInspect,signal?:AbortSignal):Promise<RunGoal|undefined>
 /** Journals an abandon's deliveries on the objective that command abandoned. */
 recordGoalDeliveries(input:GoalDeliveries,signal?:AbortSignal):Promise<RunGoal>
 /** A plain read: no count. */
 readGoal(resource:string,signal?:AbortSignal):Promise<RunGoal|undefined>
}
/** The goal report of one run, read from its record, never from a prompt. */
export function goalReportFromRecord(source:RunGoal):GoalReport{
 const held=readRunGoal(structuredClone(source))
 return {version:1,resource:held.resource,revision:held.revision,objectives:held.objectives,entries:held.journal.map(({digest:_,...entry})=>entry),discarded:held.discarded??0,...(held.inspections?{inspections:held.inspections}:{})}
}
export function readGoalReport(v:unknown):GoalReport{
 const held=shape(v,['version','resource','revision','objectives','entries','discarded'],['inspections'])
 if(held.version!==GOAL_RECORD_VERSION)return invalid()
 return {version:1,resource:id(held.resource),revision:count(held.revision,1),objectives:readObjectives(held.objectives),entries:list(held.entries,0,GOAL_JOURNAL_LIMIT,entry=>{const {digest:_,...rest}=readEntry(entry,true);return rest}),discarded:count(held.discarded,0),...(held.inspections===undefined?{}:{inspections:readInspections(held.inspections)})}
}
