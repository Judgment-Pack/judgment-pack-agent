import {readTaskCoordinatorBinding} from '../taskSteps'
import {taskConfigured,validateTaskPlan,readTaskEnvelopeBinding} from '../taskSteps'
import {canonicalJSON} from '../context/chat'
import {sha256} from '../records'
import {toolInputFailure} from '../failures'
import type {TaskStep} from './state'
import {validRecordedStep,validPlanExtensions} from './planActions'

export const PLAN_HISTORY_LIMIT=32, PLAN_DIFF_LIMIT=40, PLAN_STEP_LIMIT=20
export interface PlanChange {id:string;kind:'added'|'removed'|'changed'|'status'|'reordered';before?:TaskStep;after?:TaskStep;from?:number;to?:number}
export interface PlanRevision {sequence:number;previous:string;revision:string;explanation:string;diff:PlanChange[]}
export type DepartureKind='added'|'removed'|'changed'|'reordered'
export interface PlanOrigin {plan:string;revision:string;index:number}
export interface StepEvidence {child?:import('../taskSteps').TaskChildResult&{task:string};operations:string[];artifacts:{kind:'artifact'|'dialect';id:string;digest:string}[]}
export interface AcceptedPlan {id:string;revision:string;sourceRevision:number;objective:string;steps:string[];references:import('../planning/model').PlanReference[];validatedReferences:import('../planning/model').CurrentPlanReference[];acceptedAt:string}
export interface Departure {kind:DepartureKind;step:string;index?:number}
export type CompletionRefusal='invalid-input'|'active-step-required'|'revision-changed'|'evidence-unavailable'|'missing-evidence'|'unknown-operation'|'unsuccessful-operation'|'unobserved-revision'|'wrong-scope'
export interface PlanRefusal {revision:string;reason:CompletionRefusal;step?:string}
export interface PendingDeparture {purpose:'plan-drift';callId:string;revision:string;candidate:IdentifiedPlan;explanation:string;departures:Departure[]}
export interface NativePlanPause {callId:string;revision:string;candidateRevision:string}
export interface NativePlanInvocation {dispatchId:string;requestDigest:string}
export interface PlanDecision {nativeInvocation?:NativePlanInvocation;callId:string;revision:string;candidateRevision:string;approved:boolean;note?:string}
export interface PlanState {taskCoordinator?:import('../taskSteps').TaskCoordinatorBinding;taskEnvelope?:import('../taskSteps').TaskEnvelopeBinding;taskSchedule?:import('../taskSteps').TaskSchedule;decisions?:PlanDecision[];decisionsDiscarded?:number;revision:string;sequence:number;nextId:number;discarded:number;history:PlanRevision[];accepted?:AcceptedPlan;driftPolicy?:DepartureKind[];departures?:{revision:string;explanation:string;changes:Departure[]}[];departuresDiscarded?:number;pending?:PendingDeparture;refusals?:PlanRefusal[];refusalsDiscarded?:number}
export interface IdentifiedPlan {plan:TaskStep[];planState:PlanState}
const object=(v:unknown):v is Record<string,unknown>=>!!v&&typeof v==='object'&&!Array.isArray(v)
const count=(v:unknown,min=0):v is number=>Number.isSafeInteger(v)&&Number(v)>=min
const digest=(v:unknown):v is string=>typeof v==='string'&&/^sha256:[a-f0-9]{64}$/.test(v)
export const planStepId=(v:unknown):v is string=>typeof v==='string'&&/^step-[1-9][0-9]{0,15}$/.test(v)&&count(Number(v.slice(5)),1)
/** Content identity includes the ordered complete steps, but excludes explanation, history and allocator. */
export const planDigest=(plan:TaskStep[])=>`sha256:${sha256(canonicalJSON(plan))}`
const refusal=()=>toolInputFailure('PlanIdentityInvalid: use each current step id once, or omit id for a new step; the plan was left untouched.')
/** Historical reads are untouched. The controller calls this only at its next current write. */
export function identifyPlan(plan:TaskStep[],saved?:PlanState):IdentifiedPlan{
 if(saved)return {plan:structuredClone(plan),planState:structuredClone(saved)}
 const steps=plan.map((step,i)=>({...structuredClone(step),id:`step-${i+1}`}))
 return {plan:steps,planState:{revision:planDigest(steps),sequence:0,nextId:steps.length+1,discarded:0,history:[]}}
}
/** Pure candidate construction: every check finishes before either checkpoint or history changes. */
export function revisePlan(current:IdentifiedPlan,incoming:TaskStep[],explanation:string,closure=false):IdentifiedPlan{
 const state=structuredClone(current.planState),hadSchedule=state.taskSchedule!==undefined,old=new Map(current.plan.map((s,i)=>[s.id!,{s,i}]))
 if(state.sequence>=Number.MAX_SAFE_INTEGER||state.nextId>Number.MAX_SAFE_INTEGER-incoming.filter(step=>step.id===undefined).length)throw refusal()
 const seen=new Set<string>(),plan=incoming.map(input=>{
  if(input.id!==undefined&&(!old.has(input.id)||seen.has(input.id)))throw refusal()
  const prior=input.id===undefined?undefined:old.get(input.id)?.s
  if(!closure&&(input.status==='completed'||input.status==='not-needed'||prior?.status==='completed'||prior?.status==='not-needed')&&(!prior||input.status!==prior.status||input.step!==prior.step||input.detail!==prior.detail))throw toolInputFailure('PlanCompletionActionRequired: terminal work is closed with complete_plan_step; the plan was left untouched.')
  const id=input.id??`step-${state.nextId++}`;seen.add(id)
  // Existing package-owned members survive. Input is closed by the tool's reader/schema.
  const step={...(old.get(id)?.s??{}),...structuredClone(input),id};if(input.detail===undefined)delete step.detail;return step
 })
 // Detached proposals carry no admission schedule; the original writer reconciles its held facts.
 delete state.taskSchedule
 if(plan.some(taskConfigured)||hadSchedule)validateTaskPlan(plan)
 const diff:PlanChange[]=[]
 for(const [id,{s,i}] of old){
  const to=plan.findIndex(step=>step.id===id)
  if(to<0){diff.push({id,kind:'removed',before:structuredClone(s),from:i});continue}
  const after=plan[to]!
  if(s.step!==after.step||s.detail!==after.detail||canonicalJSON([s.dependsOn,s.budget,s.delegation])!==canonicalJSON([after.dependsOn,after.budget,after.delegation]))diff.push({id,kind:'changed',before:structuredClone(s),after:structuredClone(after)})
  if(s.status!==after.status)diff.push({id,kind:'status',before:structuredClone(s),after:structuredClone(after)})
  if(i!==to)diff.push({id,kind:'reordered',from:i,to})
 }
 for(const [to,step] of plan.entries())if(!old.has(step.id!))diff.push({id:step.id!,kind:'added',after:structuredClone(step),to})
 if(diff.length>PLAN_DIFF_LIMIT)throw toolInputFailure('PlanRevisionTooLarge: a plan revision may record at most forty changes; the plan was left untouched.')
 const previous=state.revision,revision=planDigest(plan),sequence=state.sequence+1
 state.history.push({sequence,previous,revision,explanation,diff})
 if(state.history.length>PLAN_HISTORY_LIMIT){state.history.shift();state.discarded++}
 return {plan,planState:{...state,revision,sequence}}
}
function validHistoryStep(value:unknown,id:string,accepted?:AcceptedPlan):boolean{return object(value)&&value.id===id&&typeof value.step==='string'&&!!value.step.trim()&&value.step.length<=1000&&validRecordedStep(value)&&(value.origin===undefined||accepted!==undefined&&object(value.origin)&&value.origin.plan===accepted.id&&value.origin.revision===accepted.revision&&Number(value.origin.index)<accepted.steps.length)&&['pending','in_progress','completed','not-needed'].includes(String(value.status))&&(value.detail===undefined||typeof value.detail==='string'&&value.detail.length<=1500)}
function validChange(value:unknown,nextId:number,accepted?:AcceptedPlan):boolean{
 if(!object(value)||!planStepId(value.id)||Number(value.id.slice(5))>=nextId)return false
 const position=(v:unknown)=>count(v)&&v<PLAN_STEP_LIMIT
 const before=value.before===undefined||validHistoryStep(value.before,value.id,accepted),after=value.after===undefined||validHistoryStep(value.after,value.id,accepted)
 if(!before||!after)return false
 switch(value.kind){
  case 'added':return value.after!==undefined&&value.before===undefined&&position(value.to)&&value.from===undefined
  case 'removed':return value.before!==undefined&&value.after===undefined&&position(value.from)&&value.to===undefined
  case 'reordered':return position(value.from)&&position(value.to)&&value.before===undefined&&value.after===undefined&&value.from!==value.to
  case 'changed':case 'status':return value.before!==undefined&&value.after!==undefined&&value.from===undefined&&value.to===undefined
  default:return false
 }
}
/** Semantic continuity supplements the closed current record graph and generated schema. */
export function validPlanState(plan:TaskStep[],value:unknown):value is PlanState{
 if(value===undefined)return plan.every(step=>step.id===undefined&&step.origin===undefined&&step.evidence===undefined&&step.notNeededReason===undefined&&step.status!=='not-needed')
 if(!object(value)||!validPlanExtensions(plan,value)||!digest(value.revision)||!count(value.sequence)||!count(value.nextId,1)||!count(value.discarded)||!Array.isArray(value.history)||value.history.length>PLAN_HISTORY_LIMIT||value.discarded+value.history.length!==value.sequence||value.history.length!==Math.min(value.sequence,PLAN_HISTORY_LIMIT)||value.revision!==planDigest(plan)||new Set(plan.map(s=>s.id)).size!==plan.length||!plan.every(s=>planStepId(s.id)&&Number(s.id.slice(5))<Number(value.nextId)))return false
 if(value.taskCoordinator){try{readTaskCoordinatorBinding(value.taskCoordinator)}catch{return false}}
 if(value.taskEnvelope){try{readTaskEnvelopeBinding(value.taskEnvelope)}catch{return false}}
 if(plan.some(taskConfigured)||value.taskSchedule){try{validateTaskPlan(plan,value.taskSchedule as import('../taskSteps').TaskSchedule|undefined);if(value.taskSchedule&&(value.taskSchedule as import('../taskSteps').TaskSchedule).binding.planRevision!==value.revision)return false}catch{return false}}
 let previous:string|undefined
 for(const [i,row] of value.history.entries()){
  if(!object(row)||row.sequence!==value.discarded+i+1||!digest(row.previous)||!digest(row.revision)||previous!==undefined&&row.previous!==previous||typeof row.explanation!=='string'||row.explanation.length>2000||!Array.isArray(row.diff)||row.diff.length>PLAN_DIFF_LIMIT||!row.diff.every(d=>validChange(d,value.nextId as number,value.accepted as AcceptedPlan|undefined)))return false
  previous=row.revision
 }
 return value.history.length===0||previous===value.revision
}
