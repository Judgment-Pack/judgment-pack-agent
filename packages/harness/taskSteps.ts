import {canonicalJSON} from './context/chat'
import {sha256} from './records'
import {validSubagentRecord,type SubagentRecord} from './subagents'

/** Detached candidate state only. None of these functions issues a physical dispatch grant. */
export interface TaskStepDefinition {id:string;dependsOn?:string[];budget?:{requests:number};delegation?:SubagentRecord}
export type TaskStepStatus='pending'|'in_progress'|'completed'|'not-needed'
export interface TaskScheduleBinding {run:string;execution:string;planRevision:string;scopeDigest:string;unit:'model-requests'|'provider-managed'}
export interface TaskStepClaim {id:string;requestDigest:string;state:'held'|'admitted'|'unknown'}
export interface TaskChildResult {checkpointDigest:string;resultDigest:string;eventDigest:string;commitId:string}
export interface TaskCompletionEvidence extends TaskChildResult {step:string;task:string;planRevision:string}
export interface TaskChildBinding {task:string;execution:string;planRevision:string;profile:SubagentRecord;result?:TaskChildResult}
export interface TaskStepRow {id:string;limit?:number;claims:TaskStepClaim[];child?:TaskChildBinding}
export interface TaskPendingDelegation {planRevision:string;requestDigest:string;steps:string[]}
export interface TaskSchedule {version:1;binding:TaskScheduleBinding;definitionDigest:string;rows:TaskStepRow[];pending?:TaskPendingDelegation}
export class TaskStepRefused extends Error {constructor(readonly reason:string){super(`TaskStepRefused: ${reason}`)}}
function fail(reason:string):never{throw new TaskStepRefused(reason)}
const object=(v:unknown):v is Record<string,unknown>=>!!v&&typeof v==='object'&&!Array.isArray(v)
const exact=(v:Record<string,unknown>,keys:string[])=>Object.keys(v).every(k=>keys.includes(k))
const digest=(v:unknown):v is string=>typeof v==='string'&&/^sha256:[a-f0-9]{64}$/.test(v)
const id=(v:unknown):v is string=>typeof v==='string'&&v.length>0&&v.length<=100&&!/[\u0000-\u001f]/.test(v)
const stepId=(v:unknown):v is string=>typeof v==='string'&&/^step-[1-9][0-9]{0,15}$/.test(v)&&Number.isSafeInteger(Number(v.slice(5)))
const count=(v:unknown,max=100):v is number=>Number.isInteger(v)&&Number(v)>=0&&Number(v)<=max
const hash=(v:unknown)=>`sha256:${sha256(canonicalJSON(v))}`
const definitionsDigest=(steps:TaskStepDefinition[])=>hash(steps.map(s=>({id:s.id,...(s.dependsOn?.length?{dependsOn:s.dependsOn}:{}),...(s.budget?{budget:s.budget}:{}),...(s.delegation?{delegation:s.delegation}:{})})))
const bounded=(v:unknown)=>new TextEncoder().encode(canonicalJSON(v)).length<=65536
export function validateTaskGraph(value:unknown):asserts value is TaskStepDefinition[]{
 if(!Array.isArray(value)||value.length<1||value.length>20)fail('invalid-steps')
 const ids=new Set<string>()
 for(const s of value){
  if(!object(s)||!exact(s,['id','dependsOn','budget','delegation'])||!stepId(s.id)||ids.has(s.id))fail('invalid-step-id')
  ids.add(s.id)
  if(s.dependsOn!==undefined&&(!Array.isArray(s.dependsOn)||s.dependsOn.length>19||new Set(s.dependsOn).size!==s.dependsOn.length||!s.dependsOn.every(stepId)))fail('invalid-dependencies')
  if(s.budget!==undefined&&(!object(s.budget)||!exact(s.budget,['requests'])||!count(s.budget.requests)||Number(s.budget.requests)<1))fail('invalid-share')
  if(s.delegation!==undefined&&!validSubagentRecord(s.delegation))fail('invalid-profile-pin')
 }
 const graph=new Map((value as TaskStepDefinition[]).map(s=>[s.id,s.dependsOn??[]])),active=new Set<string>(),done=new Set<string>()
 const visit=(name:string)=>{if(active.has(name))fail('dependency-cycle');if(done.has(name))return;active.add(name);for(const dependency of graph.get(name)!){if(dependency===name||!ids.has(dependency))fail('unknown-dependency');visit(dependency)}active.delete(name);done.add(name)}
 for(const name of ids)visit(name)
 if(!bounded(value))fail('capacity')
}
export function readyTaskSteps(steps:TaskStepDefinition[],statuses:Record<string,TaskStepStatus>):string[]{
 validateTaskGraph(steps)
 if(Object.keys(statuses).length!==steps.length||steps.some(s=>!['pending','in_progress','completed','not-needed'].includes(statuses[s.id]??'')))fail('invalid-statuses')
 return steps.filter(s=>statuses[s.id]==='pending'&&(s.dependsOn??[]).every(d=>statuses[d]==='completed'||statuses[d]==='not-needed')).map(s=>s.id)
}
function validBinding(v:unknown):v is TaskScheduleBinding{return object(v)&&exact(v,['run','execution','planRevision','scopeDigest','unit'])&&id(v.run)&&id(v.execution)&&digest(v.planRevision)&&digest(v.scopeDigest)&&['model-requests','provider-managed'].includes(String(v.unit))}
/** Restored claims stay held/unknown. Reading stored facts never recreates a once-only admission. */
export function readTaskSchedule(value:unknown,binding:TaskScheduleBinding,steps:TaskStepDefinition[]):TaskSchedule{
 validateTaskGraph(steps)
 if(!validBinding(binding)||!object(value)||!exact(value,['version','binding','definitionDigest','rows','pending'])||value.version!==1||canonicalJSON(value.binding)!==canonicalJSON(binding)||value.definitionDigest!==definitionsDigest(steps)||!Array.isArray(value.rows)||value.rows.length!==steps.length||!bounded(value))fail('identity-changed')
 const claims=new Set<string>(),children=new Set<string>();let size=0
 for(const [i,row] of value.rows.entries()){
  const step=steps[i]!
  if(!object(row)||!exact(row,['id','limit','claims','child'])||row.id!==step.id||row.limit!==step.budget?.requests||!Array.isArray(row.claims)||row.claims.length>100)fail('invalid-reservation')
  for(const claim of row.claims){if(!object(claim)||!exact(claim,['id','requestDigest','state'])||!id(claim.id)||claims.has(claim.id)||!digest(claim.requestDigest)||!['held','admitted','unknown'].includes(String(claim.state)))fail('invalid-reservation');claims.add(claim.id);size++}
  if(row.limit!==undefined&&row.claims.length>Number(row.limit))fail('share-exceeded')
  if(row.child!==undefined){const child=row.child
   if(!object(child)||!exact(child,['task','execution','planRevision','profile','result'])||!id(child.task)||!id(child.execution)||child.task!==child.execution||children.has(child.task)||!digest(child.planRevision)||!validSubagentRecord(child.profile)||!step.delegation||canonicalJSON(child.profile)!==canonicalJSON(step.delegation))fail('invalid-child-binding')
   children.add(child.task)
   if(child.result!==undefined&&(!object(child.result)||!exact(child.result,['checkpointDigest','resultDigest','eventDigest','commitId'])||!digest(child.result.checkpointDigest)||!digest(child.result.resultDigest)||!digest(child.result.eventDigest)||!id(child.result.commitId)))fail('invalid-child-evidence')
  }
 }
 if(value.pending!==undefined){const pending=value.pending;if(!object(pending)||!exact(pending,['planRevision','requestDigest','steps'])||pending.planRevision!==binding.planRevision||!digest(pending.requestDigest)||!Array.isArray(pending.steps)||pending.steps.length<1||pending.steps.length>3||new Set(pending.steps).size!==pending.steps.length||!pending.steps.every(name=>stepId(name)&&steps.some(step=>step.id===name&&step.delegation)&&!(value.rows as unknown as TaskStepRow[]).find(row=>row.id===name)?.child))fail('invalid-pending-delegation')}
 if(size>400)fail('capacity')
 return structuredClone(value) as unknown as TaskSchedule
}
export function createTaskSchedule(binding:TaskScheduleBinding,steps:TaskStepDefinition[],available:number):TaskSchedule{
 validateTaskGraph(steps)
 if(!validBinding(binding)||!count(available))fail('invalid-allowance')
 if(steps.reduce((n,s)=>n+(s.budget?.requests??0),0)>available)fail('shares-exceed-allowance')
 return {version:1,binding:structuredClone(binding),definitionDigest:definitionsDigest(steps),rows:steps.map(s=>({id:s.id,...(s.budget?{limit:s.budget.requests}:{}),claims:[]}))}
}
/** Must be called inside the original owner transaction before normal root/170 admission. */
export function reserveTaskStep(current:TaskSchedule,steps:TaskStepDefinition[],statuses:Record<string,TaskStepStatus>,step:string,claim:{id:string;requestDigest:string},available:number|undefined):TaskSchedule{
 const next=readTaskSchedule(current,current.binding,steps)
 if(!id(claim.id)||!digest(claim.requestDigest)||(available===undefined?(next.binding.unit!=='provider-managed'||steps.some(step=>step.budget!==undefined)):!count(available)))fail('invalid-reservation')
 if(next.rows.some(r=>r.claims.some(c=>c.id===claim.id)))fail('reservation-already-held')
 const at=steps.findIndex(s=>s.id===step);if(at<0)fail('unknown-step')
 readyTaskSteps(steps,statuses)
 if(!['pending','in_progress'].includes(statuses[step]??'')||!(steps[at]!.dependsOn??[]).every(d=>statuses[d]==='completed'||statuses[d]==='not-needed'))fail('dependencies-unready')
 const row=next.rows[at]!,others=next.rows.reduce((n,r)=>n+(r.id===step||statuses[r.id]==='completed'||statuses[r.id]==='not-needed'?0:Math.max(0,(r.limit??0)-r.claims.length)),0)
 if(available!==undefined&&available<=others||row.limit!==undefined&&row.claims.length>=row.limit)fail('step-budget-pause')
 row.claims.push({...claim,state:'held'});if(!bounded(next)||next.rows.reduce((n,r)=>n+r.claims.length,0)>400)fail('capacity')
 return next
}
/** The caller supplies the actual already-admitted catalogue, never a profile name fallback. */
export function requireTaskProfiles(steps:TaskStepDefinition[],admitted:readonly SubagentRecord[]):void{
 validateTaskGraph(steps)
 for(const step of steps)if(step.delegation&&!admitted.some(pin=>validSubagentRecord(pin)&&canonicalJSON(pin)===canonicalJSON(step.delegation)))fail('profile-not-admitted')
}

/** Reconcile only after the owner has accepted this exact plan revision; never resets held facts. */
export function reconcileTaskSchedule(current:TaskSchedule|undefined,oldSteps:TaskStepDefinition[],oldStatuses:Record<string,TaskStepStatus>,binding:TaskScheduleBinding,steps:TaskStepDefinition[],statuses:Record<string,TaskStepStatus>,available:number):TaskSchedule{
 validateTaskGraph(steps);readyTaskSteps(steps,statuses)
 if(!current)return createTaskSchedule(binding,steps,available)
 const saved=readTaskSchedule(current,current.binding,oldSteps);readyTaskSteps(oldSteps,oldStatuses)
 if(!validBinding(binding)||['run','execution','scopeDigest','unit'].some(k=>binding[k as keyof TaskScheduleBinding]!==saved.binding[k as keyof TaskScheduleBinding])||!count(available))fail('identity-changed')
 for(const row of saved.rows){const before=oldSteps.find(s=>s.id===row.id)!,after=steps.find(s=>s.id===row.id)
  if((row.claims.length||row.child||oldStatuses[row.id]==='in_progress')&&(!after||canonicalJSON(before)!==canonicalJSON(after)))fail('active-definition-changed')
 }
 const next:TaskSchedule={version:1,binding:structuredClone(binding),definitionDigest:definitionsDigest(steps),rows:steps.map(s=>{const prior=saved.rows.find(r=>r.id===s.id);return {id:s.id,...(s.budget?{limit:s.budget.requests}:{}),claims:structuredClone(prior?.claims??[]),...(prior?.child?{child:structuredClone(prior.child)}:{})}})}
 if(saved.pending){if(binding.planRevision!==saved.binding.planRevision)fail('delegation-pending');next.pending=structuredClone(saved.pending)}
 const held=next.rows.reduce((n,r)=>n+(['completed','not-needed'].includes(statuses[r.id]??'')?0:Math.max(0,(r.limit??0)-r.claims.length)),0)
 if(held>available)fail('shares-exceed-allowance')
 return readTaskSchedule(next,binding,steps)
}
/** Detached associations only. The original owner validates the whole batch before issuing any child. */
export function bindTaskChildren(current:TaskSchedule,steps:TaskStepDefinition[],statuses:Record<string,TaskStepStatus>,children:{step:string;task:string;profile:SubagentRecord}[]):TaskSchedule{
 const next=readTaskSchedule(current,current.binding,steps),ready=readyTaskSteps(steps,statuses)
 if(!Array.isArray(children)||children.length<1||children.length>20||new Set(children.map(c=>c.step)).size!==children.length||new Set(children.map(c=>c.task)).size!==children.length)fail('invalid-child-set')
 for(const child of children){const step=steps.find(s=>s.id===child.step),row=next.rows.find(r=>r.id===child.step)
  if(!step||!row||!ready.includes(child.step)||row.child||row.claims.length>0||!id(child.task)||!validSubagentRecord(child.profile)||!step.delegation||canonicalJSON(step.delegation)!==canonicalJSON(child.profile)||next.rows.some(r=>r.child?.task===child.task))fail('child-not-admitted')
 }
 for(const child of children)next.rows.find(r=>r.id===child.step)!.child={task:child.task,execution:child.task,planRevision:current.binding.planRevision,profile:structuredClone(child.profile)}
 return readTaskSchedule(next,current.binding,steps)
}

/** Project only configured fields; public task text and completion evidence are not definitions. */
export function taskDefinitions(steps:readonly import('./execution/state').TaskStep[]):TaskStepDefinition[]{return steps.map(s=>({id:s.id!,...(s.dependsOn===undefined?{}:{dependsOn:structuredClone(s.dependsOn)}),...(s.budget===undefined?{}:{budget:structuredClone(s.budget)}),...(s.delegation===undefined?{}:{delegation:structuredClone(s.delegation)})}))}
export function taskStatuses(steps:readonly import('./execution/state').TaskStep[]):Record<string,TaskStepStatus>{return Object.fromEntries(steps.map(s=>[s.id!,s.status]))}

/** Current plan semantics, shared by detached candidates and resumed checkpoint readers. */
export function validateTaskPlan(steps:readonly import('./execution/state').TaskStep[],schedule?:TaskSchedule):void{
 const definitions=taskDefinitions(steps),statuses=taskStatuses(steps)
 validateTaskGraph(definitions);readyTaskSteps(definitions,statuses)
 for(const step of steps)if(step.status==='in_progress'&&!(step.dependsOn??[]).every(dependency=>['completed','not-needed'].includes(statuses[dependency]??'')))fail('dependencies-unready')
 if(schedule)readTaskSchedule(schedule,schedule.binding,definitions)
}
export const taskConfigured=(step:import('./execution/state').TaskStep)=>step.dependsOn!==undefined||step.budget!==undefined||step.delegation!==undefined

/** Explicit host configuration; retained bytes alone never attach an execution envelope. */
export interface TaskStepEnvelope {id:string;revision:string;unit:'provider-invocations';limit:number}
export interface TaskEnvelopeBinding {run:string;execution:string;scopeDigest:string;configuration:TaskStepEnvelope}
export function readTaskStepEnvelope(value:unknown):TaskStepEnvelope{
 if(!object(value)||!exact(value,['id','revision','unit','limit'])||!id(value.id)||!digest(value.revision)||value.unit!=='provider-invocations'||!count(value.limit)||Number(value.limit)<1)fail('invalid-execution-envelope')
 return structuredClone(value) as unknown as TaskStepEnvelope
}
export function readTaskEnvelopeBinding(value:unknown):TaskEnvelopeBinding{
 if(!object(value)||!exact(value,['run','execution','scopeDigest','configuration'])||!id(value.run)||!id(value.execution)||!digest(value.scopeDigest))fail('invalid-execution-envelope')
 readTaskStepEnvelope(value.configuration)
 return structuredClone(value) as unknown as TaskEnvelopeBinding
}
