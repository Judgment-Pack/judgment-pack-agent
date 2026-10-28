import {validateTaskGraph} from '../taskSteps'
import {canonicalJSON} from '../context/chat'
import {toolInputFailure} from '../failures'
import {sha256} from '../records'
import type {DurableBook,DurableScope} from '../storage/writer'
import type {TaskStep} from './state'
import {revisePlan,validPlanState,type IdentifiedPlan,type StepEvidence,type CompletionRefusal,type Departure,type AcceptedPlan} from './plan'
const object=(v:unknown):v is Record<string,unknown>=>!!v&&typeof v==='object'&&!Array.isArray(v)
const exact=(v:Record<string,unknown>,keys:string[])=>Object.keys(v).every(k=>keys.includes(k))
export interface PlanProof {book:DurableBook;scope:DurableScope;children?:import('../taskSteps').TaskCompletionEvidence[]}
const id=(v:unknown):v is string=>typeof v==='string'&&v.length>0&&v.length<=256
export const contentDigest=(value:unknown)=>`sha256:${sha256(canonicalJSON(value))}`
export function readEvidence(value:unknown):StepEvidence|undefined{
 if(!object(value)||!exact(value,['operations','artifacts','child'])||!Array.isArray(value.operations)||!Array.isArray(value.artifacts)||value.operations.length+value.artifacts.length>20||!value.operations.every(id)||new Set(value.operations).size!==value.operations.length)return
 if(!value.artifacts.every(a=>object(a)&&exact(a,['kind','id','digest'])&&['artifact','dialect'].includes(String(a.kind))&&id(a.id)&&typeof a.digest==='string'&&/^sha256:[a-f0-9]{64}$/.test(a.digest))||new Set(value.artifacts.map(a=>canonicalJSON(a))).size!==value.artifacts.length)return
 if(value.child!==undefined&&(!object(value.child)||!exact(value.child,['task','checkpointDigest','resultDigest','eventDigest','commitId'])||!id(value.child.task)||value.child.task.length>100||!id(value.child.commitId)||value.child.commitId.length>100||!['checkpointDigest','resultDigest','eventDigest'].every(key=>typeof (value.child as Record<string,unknown>)[key]==='string'&&/^sha256:[a-f0-9]{64}$/.test(String((value.child as Record<string,unknown>)[key])))))return
 return structuredClone(value) as unknown as StepEvidence
}
export function evidenceFailure(evidence:StepEvidence,proof:PlanProof|undefined):CompletionRefusal|undefined{
 if(!proof)return 'evidence-unavailable'
 if(evidence.child)return 'wrong-scope'
 if(evidence.operations.length+evidence.artifacts.length===0)return 'missing-evidence'
 const scope=canonicalJSON(proof.scope),same=(s:DurableScope)=>canonicalJSON(s)===scope
 for(const id of evidence.operations){
  const op=proof.book.dispatches.find(d=>d.id===id)
  if(!op)return 'unknown-operation'
  if(!same(op.scope))return 'wrong-scope'
  if(!['host','runtime'].includes(op.kind)||op.state!=='result'||op.failure||op.result===undefined||object(op.result)&&op.result.isError===true||!proof.book.observations.some(o=>o.kind==='operation'&&o.dispatchId===id&&same(o.scope)))return 'unsuccessful-operation'
 }
 for(const artifact of evidence.artifacts){
  if(!proof.book.observations.some(o=>(o.kind==='artifact-read'||o.kind==='artifact-produced')&&same(o.scope)&&o.artifact?.kind===artifact.kind&&o.artifact.id===artifact.id&&o.artifact.digest===artifact.digest&&proof.book.dispatches.some(d=>d.id===o.dispatchId&&same(d.scope)&&d.state==='result'&&!d.failure&&['host','runtime'].includes(d.kind)&&d.result!==undefined&&!(object(d.result)&&d.result.isError===true))))return 'unobserved-revision'
 }
}
export function closePlan(current:IdentifiedPlan,args:Record<string,unknown>,proof:PlanProof|undefined):{next?:IdentifiedPlan;reason?:CompletionRefusal}{
 if(!exact(args,['id','expectedRevision','outcome','evidence','reason'])||!id(args.id)||typeof args.expectedRevision!=='string'||!['completed','not-needed'].includes(String(args.outcome)))return {reason:'invalid-input'}
 if(args.expectedRevision!==current.planState.revision)return {reason:'revision-changed'}
 const delegated=current.planState.taskSchedule?.rows.find(row=>row.id===args.id)?.child
 const active=current.plan.find(s=>s.id===args.id&&(s.status==='in_progress'||delegated&&s.status==='pending'&&args.outcome==='completed'))
 if(!active)return {reason:'active-step-required'}
 let next:TaskStep
 if(args.outcome==='not-needed'){
  if(args.evidence!==undefined||typeof args.reason!=='string'||!args.reason.trim()||args.reason.length>1500)return {reason:'invalid-input'}
  next={...active,status:'not-needed',notNeededReason:args.reason}
 }else{
  const evidence=readEvidence(args.evidence)
  if(args.reason!==undefined||!evidence)return {reason:'invalid-input'}
  if(delegated){
   const child=evidence.child,verified=proof?.children?.find(row=>row.step===active.id&&row.task===delegated.task&&row.planRevision===delegated.planRevision)
   if(!child||evidence.operations.length||evidence.artifacts.length)return {reason:'missing-evidence'}
   if(!verified)return {reason:'evidence-unavailable'}
   const expected={task:verified.task,checkpointDigest:verified.checkpointDigest,resultDigest:verified.resultDigest,eventDigest:verified.eventDigest,commitId:verified.commitId}
   if(canonicalJSON(child)!==canonicalJSON(expected)||canonicalJSON(delegated.result)!==canonicalJSON({checkpointDigest:verified.checkpointDigest,resultDigest:verified.resultDigest,eventDigest:verified.eventDigest,commitId:verified.commitId}))return {reason:'wrong-scope'}
  }else{const reason=evidenceFailure(evidence,proof);if(reason)return {reason}}
  next={...active,status:'completed',evidence}
 }
 return {next:revisePlan(current,current.plan.map(s=>s.id===active.id?next:s),args.outcome==='completed'?'Active step completed with recorded evidence.':'Active step closed as not-needed.',true)}
}
export function departures(accepted:AcceptedPlan,plan:TaskStep[]):Departure[]{
 const result:Departure[]=[]
 for(let index=0;index<accepted.steps.length;index++){
  const at=plan.findIndex(s=>s.origin?.plan===accepted.id&&s.origin.revision===accepted.revision&&s.origin.index===index)
  if(at<0){result.push({kind:'removed',step:`accepted-${index}`,index});continue}
  const step=plan[at]!
  if(step.step!==accepted.steps[index])result.push({kind:'changed',step:step.id!,index})
  if(at!==index)result.push({kind:'reordered',step:step.id!,index})
 }
 for(const step of plan)if(!step.origin)result.push({kind:'added',step:step.id!})
 return result
}
/** Current departures relative to original acceptance; event history retains changes in this state. */
export function changedDepartures(before:IdentifiedPlan,after:IdentifiedPlan,explanation:string){
 const accepted=before.planState.accepted;if(!accepted)return []
 const changes:Departure[]=[]
 for(let index=0;index<accepted.steps.length;index++){
  const origin=(step:TaskStep)=>step.origin?.plan===accepted.id&&step.origin.revision===accepted.revision&&step.origin.index===index
  const from=before.plan.findIndex(origin),to=after.plan.findIndex(origin)
  if(from<0)continue
  const prior=before.plan[from]!
  if(to<0){changes.push({kind:'removed',step:prior.id!,index});continue}
  const next=after.plan[to]!
  if(prior.step!==next.step||prior.detail!==next.detail)changes.push({kind:'changed',step:next.id!,index})
  if(from!==to)changes.push({kind:'reordered',step:next.id!,index})
 }
 for(const step of after.plan)if(!step.origin&&!before.plan.some(prior=>prior.id===step.id))changes.push({kind:'added',step:step.id!})
 if(changes.length&&!explanation.trim())throw toolInputFailure('PlanDepartureExplanationRequired: explain departures from the accepted plan.')
 return changes
}

export function appendRefusal(current:IdentifiedPlan,reason:CompletionRefusal,step?:string):IdentifiedPlan{
 const next=structuredClone(current),state=next.planState
 state.refusals??=[];state.refusalsDiscarded??=0
 state.refusals.push({revision:state.revision,reason,...(step&&step.length<=100?{step}:{})})
 if(state.refusals.length>32){state.refusals.shift();state.refusalsDiscarded++}
 return next
}

export function validRecordedStep(value:unknown):boolean{
 if(!object(value))return false
 if(value.dependsOn!==undefined||value.budget!==undefined||value.delegation!==undefined){try{validateTaskGraph([{id:'step-1',...(value.dependsOn===undefined?{}:{dependsOn:[]}),...(value.budget===undefined?{}:{budget:value.budget}),...(value.delegation===undefined?{}:{delegation:value.delegation})}]);if(value.dependsOn!==undefined&&(!Array.isArray(value.dependsOn)||value.dependsOn.length>19||new Set(value.dependsOn).size!==value.dependsOn.length||!value.dependsOn.every(d=>typeof d==='string'&&/^step-[1-9][0-9]{0,15}$/.test(d))))return false}catch{return false}}
 if(value.origin!==undefined&&(!object(value.origin)||!exact(value.origin,['plan','revision','index'])||!id(value.origin.plan)||typeof value.origin.revision!=='string'||!/^sha256:[a-f0-9]{64}$/.test(value.origin.revision)||!Number.isSafeInteger(value.origin.index)||Number(value.origin.index)<0||Number(value.origin.index)>19))return false
 if(value.evidence!==undefined){const evidence=readEvidence(value.evidence);if(value.status!=='completed'||!evidence||evidence.operations.length+evidence.artifacts.length+(evidence.child?1:0)<1)return false}
 if(value.status==='not-needed')return typeof value.notNeededReason==='string'&&!!value.notNeededReason.trim()&&value.notNeededReason.length<=1500&&value.evidence===undefined
 return value.notNeededReason===undefined
}

/** Validate the relationships that a structural schema cannot express. Pending candidates
 * are detached, one revision ahead, and cannot themselves contain another proposal. */
export function validPlanExtensions(plan:TaskStep[],state:Record<string,unknown>):boolean{
 const digest=(v:unknown)=>typeof v==='string'&&/^sha256:[a-f0-9]{64}$/.test(v)
 const bounded=(rows:unknown,discarded:unknown)=>rows===undefined?discarded===undefined:Array.isArray(rows)&&rows.length<=32&&Number.isSafeInteger(discarded)&&Number(discarded)>=0
 if(!plan.every(validRecordedStep))return false
 for(const [rows,discarded] of [['departures','departuresDiscarded'],['refusals','refusalsDiscarded'],['decisions','decisionsDiscarded']])if(!bounded(state[rows!],state[discarded!]))return false
 const accepted=state.accepted
 if(accepted!==undefined){
  if(!object(accepted)||!id(accepted.id)||!digest(accepted.revision)||!Number.isSafeInteger(accepted.sourceRevision)||Number(accepted.sourceRevision)<1||typeof accepted.objective!=='string'||!accepted.objective.trim()||accepted.objective.length>2000||!Array.isArray(accepted.steps)||!accepted.steps.length||accepted.steps.length>20||!accepted.steps.every(s=>typeof s==='string'&&!!s.trim()&&s.length<=1000)||!Array.isArray(accepted.references)||accepted.references.length>8||typeof accepted.acceptedAt!=='string'||!Number.isFinite(Date.parse(accepted.acceptedAt)))return false
  if(!Array.isArray(accepted.validatedReferences)||accepted.validatedReferences.length!==accepted.references.length||!accepted.validatedReferences.every((ref,index)=>object(ref)&&exact(ref,['kind','id','digest'])&&['artifact','dialect'].includes(String(ref.kind))&&id(ref.id)&&typeof ref.digest==='string'&&/^[a-f0-9]{64}$/.test(ref.digest)&&(!object((accepted.references as unknown[])[index])||!('digest'in (accepted.references as Record<string,unknown>[])[index]!)||canonicalJSON(ref)===canonicalJSON((accepted.references as unknown[])[index]))))return false
  if(accepted.revision!==contentDigest({objective:accepted.objective,steps:accepted.steps,questions:[],references:accepted.references}))return false
 }
 const origins=new Set<number>()
 for(const step of plan)if(step.origin){if(!object(accepted)||step.origin.plan!==accepted.id||step.origin.revision!==accepted.revision||step.origin.index>=(accepted.steps as unknown[]).length||origins.has(step.origin.index))return false;origins.add(step.origin.index)}
 const policy=state.driftPolicy
 if(policy!==undefined&&(!object(accepted)||!Array.isArray(policy)||policy.length>4||new Set(policy).size!==policy.length||policy.some(k=>!['added','removed','changed','reordered'].includes(String(k)))))return false
 const departure=(v:unknown)=>object(v)&&['added','removed','changed','reordered'].includes(String(v.kind))&&typeof v.step==='string'&&v.step.length>0&&v.step.length<=100&&(v.index===undefined||Number.isSafeInteger(v.index)&&Number(v.index)>=0&&Number(v.index)<20)
 if(Array.isArray(state.departures)&&(!object(accepted)||!state.departures.every(v=>object(v)&&digest(v.revision)&&typeof v.explanation==='string'&&!!v.explanation.trim()&&v.explanation.length<=2000&&Array.isArray(v.changes)&&v.changes.length>0&&v.changes.length<=40&&v.changes.every(departure))))return false
 const reasons=['invalid-input','active-step-required','revision-changed','evidence-unavailable','missing-evidence','unknown-operation','unsuccessful-operation','unobserved-revision','wrong-scope']
 if(Array.isArray(state.refusals)&&!state.refusals.every(v=>object(v)&&digest(v.revision)&&reasons.includes(String(v.reason))&&(v.step===undefined||typeof v.step==='string'&&v.step.length>0&&v.step.length<=100)))return false
 if(Array.isArray(state.decisions)&&!state.decisions.every(v=>object(v)&&id(v.callId)&&digest(v.revision)&&digest(v.candidateRevision)&&typeof v.approved==='boolean'&&(v.note===undefined||typeof v.note==='string'&&v.note.length<=2000)))return false
 if(state.pending!==undefined){
  const p=state.pending
  if(!object(accepted)||!object(p)||p.purpose!=='plan-drift'||!id(p.callId)||p.revision!==state.revision||!object(p.candidate)||!Array.isArray(p.candidate.plan)||!object(p.candidate.planState)||p.candidate.planState.pending!==undefined||p.candidate.planState.sequence!==Number(state.sequence)+1||canonicalJSON(p.candidate.planState.accepted)!==canonicalJSON(accepted)||canonicalJSON(p.candidate.planState.driftPolicy)!==canonicalJSON(policy)||typeof p.explanation!=='string'||!p.explanation.trim()||p.explanation.length>2000||!Array.isArray(p.departures)||!p.departures.length||p.departures.length>40||!p.departures.every(departure))return false
  const candidate=p.candidate as unknown as IdentifiedPlan
  if(!validPlanState(candidate.plan,candidate.planState)||candidate.planState.history.at(-1)?.previous!==state.revision||canonicalJSON(changedDepartures({plan,planState:state as unknown as IdentifiedPlan['planState']},candidate,p.explanation))!==canonicalJSON(p.departures)||!p.departures.some(v=>(policy as string[]??['removed']).includes(String((v as Record<string,unknown>).kind))))return false
  try{
   const prior={plan,planState:{...state} as unknown as IdentifiedPlan['planState']};delete prior.planState.pending
   const ids=new Set(plan.map(step=>step.id)),incoming=candidate.plan.map(step=>({...(ids.has(step.id)?{id:step.id}:{}),step:step.step,status:step.status,...(step.detail===undefined?{}:{detail:step.detail}),...(step.dependsOn===undefined?{}:{dependsOn:structuredClone(step.dependsOn)}),...(step.budget===undefined?{}:{budget:structuredClone(step.budget)}),...(step.delegation===undefined?{}:{delegation:structuredClone(step.delegation)})}))
   const expected=revisePlan(prior,incoming,p.explanation),saved=expected.planState
   saved.departures??=[];saved.departuresDiscarded??=0;saved.departures.push({revision:saved.revision,explanation:p.explanation,changes:p.departures as Departure[]})
   if(saved.departures.length>32){saved.departures.shift();saved.departuresDiscarded++}
   if(canonicalJSON(expected)!==canonicalJSON(candidate))return false
  }catch{return false}

 }
 return true
}

export function validPlanInput(value:unknown,configured=false):value is TaskStep[]{
 return Array.isArray(value)&&value.length>0&&value.length<=20&&value.every(s=>object(s)&&exact(s,configured?['id','step','status','detail','dependsOn','budget','delegation']:['id','step','status','detail'])&&validRecordedStep(s)&&(s.id===undefined||typeof s.id==='string'&&/^step-[1-9][0-9]{0,15}$/.test(s.id))&&typeof s.step==='string'&&!!s.step.trim()&&s.step.length<=1000&&['pending','in_progress','completed','not-needed'].includes(String(s.status))&&(s.detail===undefined||typeof s.detail==='string'&&s.detail.length<=1500))&&value.filter(s=>s.status==='in_progress').length<=1
}

/** Only acknowledged evidence in this trusted run/role/task is exposed to the model. */
export function availablePlanEvidence(proof:PlanProof|undefined){
 if(!proof)return {available:false as const,operations:[],artifacts:[]}
 const operations=proof.book.dispatches.filter(d=>!evidenceFailure({operations:[d.id],artifacts:[]},proof)).map(d=>({id:d.id,tool:d.tool}))
 const artifacts:StepEvidence['artifacts']=[]
 for(const row of proof.book.observations)if(row.artifact&&!evidenceFailure({operations:[],artifacts:[row.artifact]},proof)&&!artifacts.some(a=>a.kind===row.artifact!.kind&&a.id===row.artifact!.id&&a.digest===row.artifact!.digest))artifacts.push({kind:row.artifact.kind,id:row.artifact.id,digest:row.artifact.digest})
 return {available:true as const,operations,artifacts,...(proof.children?.length?{children:structuredClone(proof.children)}:{})}
}
