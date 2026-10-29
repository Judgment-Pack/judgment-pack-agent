import {closePlan,changedDepartures,appendRefusal,contentDigest,validPlanInput,availablePlanEvidence} from './planActions'
import {resolvePlanReference} from '../planning/references'
import {identifyPlan,revisePlan} from './plan'
import {CheckpointIncrement,CheckpointMutation,mutateCheckpoint,type CheckpointPatch} from './mutation'
import {ExecutionProgressTracker} from './progress'
import type {ContextToolPolicy} from '../context/toolPolicy'
import {authoredFailure,toolInputFailure} from '../failures'
import type {AssistantEvent,EngineSession,HostTool,McpToolResult} from '../engine'
import {answer,contextDigest,type ContextLedger} from '../context/ledger'
import {readCacheKey,canonicalJSON as importCanonicalPlan} from '../context/chat'
import {MAX_TURNS} from '../engines/contract'
import {utf8Bytes} from '../context/policy'
import {frameBlock} from '../framing'
import {ExecutionPaused,RequestCapRefusal,MAX_CHECKPOINT_BYTES,MAX_PROMPT_DIGESTS,validRequestCap,readApprovalDecision,readExecution,refusedResult,unknownOutcome,type ExecutionCheckpoint} from './state'
import {CHECKPOINT_SCHEMA,CRITIQUE_SCHEMA,PROVENANCE_SCHEMA,recordsPolicy,refuseExpired,writeRecord,type RecordGuard,type RecordsPolicy} from '../records'

export type PlanEvent=Extract<AssistantEvent,{type:'plan_revised'|'plan_departed'|'plan_completion_refused'|'plan_drift_decided'|'approval_requested'}>
export const EXECUTION_INSTRUCTIONS = `TASK EXECUTION: For work requiring several stages, use update_plan to track concrete pending and in_progress steps. Close the active step with complete_plan_step and exact planRevision, using acknowledged operation ids or artifact observations; use not-needed with a reason for unnecessary work. update_plan may carry existing terminal rows unchanged. A plan decision applies only to its proposed task-list change and cannot approve a host action. Skip planning overhead for simple replies. Use the current accepted plan when present; do not repeatedly re-plan or ask the user to repeat an already authorized request. Update progress at meaningful boundaries, and preserve completed work across continuation. A task list is working state, not approval or proof: mark a step complete only when its required result exists, and distinguish a proposed change from a saved change. Keep a multi-stage task plan current; it estimates work but cannot grant more requests. An unsaved working object remains in the exact arguments of its completed operations. Continue from those retained arguments after validation or evaluation; rereading the saved workspace does not retrieve an unsaved proposal. Recheck the saved revision before applying changes. Use tool results already obtained; do not reread unchanged instructions or schemas or scan the entire conversation. On continuation, inspect the latest completed result and its exact arguments before doing more work. A newer saved workspace object takes precedence over an older chat draft when editing that object; preserve its identity. For a focused change, inspect the relevant contract once, make the smallest proposal, verify the affected behavior and return it. Do not restart discovery after context compaction. Before repeating a failed read, correct its arguments using the current context index. If blocked, state the exact missing information. When requests run low, finish the concrete result or call request_budget_increase with the reason and next concrete step. Desk owns budget extensions; this tool requests but never grants one. Never modify expected tests merely to match actual results. Current user instructions and host permissions remain authoritative.`

/** A tool-call cap as the request state states it (agent#13): `response`, a parent's and its children's together; `task`, one child's. */
export interface ToolAllowance {used:number;limit:number;scope:'response'|'task'}

export class ExecutionController {
 readonly progress=new ExecutionProgressTracker()
 private checkpoint=new CheckpointMutation()
 get state():ExecutionCheckpoint{return this.checkpoint.read()}
 get revision(){return this.checkpoint.revision}
 private requestCap?:number
 private capFailure?:RequestCapRefusal
 private initialized=false
 private capRefusal(code:'configuration'|'stale-state'='configuration'){return new RequestCapRefusal(code)}
 mutate(patch:CheckpointPatch){
  if(this.capFailure)throw this.capFailure
  return this.checkpoint.apply(patch,candidate=>{
   if(this.initialized&&(candidate.requestCap!==this.requestCap||this.requestCap!==undefined&&(candidate.unit!=='model-requests'||candidate.limit!==this.requestCap||!Number.isSafeInteger(candidate.used)||candidate.used<(this.state.used??0)||candidate.used>this.requestCap||candidate.grants.length!==0)))throw this.capRefusal()
  })
 }
 pauseRequested=false
 /**
  * agent#13: the tool-call cap this run works under, where a delegation coordinator holds one (set
  * through its link, `delegation.ts`): the response's, shared by a parent and its children, or a
  * child's own budget. Stated in the request state as `toolCalls`.
  */
 toolAllowance?:()=>ToolAllowance
 /** ADR-0006: the schemas load_tool loaded this run, as one data block, or ''; stated after the request state. */
 loadedSchemas?:()=>string
 /** ADR-0009: the skills loaded, as one block headed at the project instructions' rank, or ''; stated last, after the loaded schemas. */
 loadedSkills?:()=>string
 /** ADR-0015: mutable proposal drafts follow the loaded schemas and skills. */
 loadedDrafts?:()=>string
 private planQueue:Promise<unknown>=Promise.resolve()
 private serialPlan<T>(run:()=>Promise<T>):Promise<T>{const task=this.planQueue.then(run);this.planQueue=task.catch(()=>{});return task}
 taskProfiles?:()=>import('../subagents').SubagentRecord[]
 planRefused?:()=>void
 proof?:()=>import('./planActions').PlanProof|undefined
 publishPlan?: (state:ExecutionCheckpoint,event:PlanEvent)=>Promise<void>
 private planDecisionEvent?:Extract<AssistantEvent,{type:'plan_drift_decided'}>
 async publishPlanDecision(){if(this.planDecisionEvent){await this.publishPlan?.(this.snapshot(),this.planDecisionEvent);this.planDecisionEvent=undefined}}
 private restored=false
 /**
  * ADR-0008: what the restored approval asks of this run before its first request: `execute`, the
  * person approved the call and it runs once; `continue`, its result (or the refused result) is
  * written and the continuation carries it; `write`, as `continue`, the state rewritten first.
  */
 approvalStep?:'execute'|'continue'|'write'
 private attempt=1
 private operationsReference:unknown
 private restoredResults:unknown[]=[]
 private inFlight=new Map<string,string>()
 private cache=new Map<string,McpToolResult>()
 private usageConnection='unbound'
 private usageRole='author'
 private usageModel?:string
 private usageStarted=Date.now()
 private usageBound=false
 private usageSources=new Map<string,{calls:number;milliseconds:number;bytes:number}>()
 private readonly recordPolicy:RecordsPolicy
 recordGuard?:()=>RecordGuard|undefined
 constructor(private session:EngineSession,private ledger:ContextLedger,members?:import('../policy').PolicyMembers){this.recordPolicy=recordsPolicy(members??{})}
 get storagePolicy(){return this.recordPolicy}
 get storageGuard(){return this.recordGuard?.()}
 async initialize(){
  const requestCap=this.session.context?.requestCap
  if(this.initialized&&(this.requestCap!==undefined||requestCap!==undefined))throw this.capRefusal()
  if(requestCap!==undefined&&(!validRequestCap(requestCap)||!('model'in this.session)))throw this.capRefusal()
  this.requestCap=requestCap;this.initialized=true
  const requestKey=await contextDigest(JSON.stringify({request:this.session.context?.currentRequest??this.session.prompt,phase:this.session.context?.phase??'work',purpose:this.session.purpose??null}))
  const restored=readExecution(this.session.context?.execution)
  if(restored){
   if(restored.requestCap!==requestCap)throw this.capRefusal('stale-state')
   if(restored.retainUntil)refuseExpired(restored as {retainUntil:string})
   if(restored.requestKey!==requestKey||restored.phase!==(this.session.context?.phase??'work'))throw toolInputFailure('The request or execution phase changed. Start a new response with the current context.')
   for(const ref of restored.planState?.accepted?.references??[]){if(!this.session.context?.planResolver)throw toolInputFailure('PlanResolverRequired: verify accepted references before work.');await resolvePlanReference(ref,this.session.context.planResolver,this.session.signal)}
   // ADR-0008: a paused act call continues only with the person's decision for that call.
   let given=this.session.context?.approval
   if(restored.planState?.pending){
    const pending=restored.planState.pending,decision=readApprovalDecision(given)
    if(restored.approval?.state==='asked'||!decision||decision.callId!==pending.callId||pending.revision!==restored.planState.revision)throw toolInputFailure('PlanApprovalRequired: continue with the exact pending plan decision.')
    const planState=structuredClone(decision.approved?pending.candidate.planState:restored.planState);delete planState.pending
    const row={callId:pending.callId,revision:pending.revision,candidateRevision:pending.candidate.planState.revision,approved:decision.approved,...(decision.note===undefined?{}:{note:decision.note})}
    planState.decisions??=[];planState.decisionsDiscarded??=0;planState.decisions.push(row)
    if(planState.decisions.length>32){planState.decisions.shift();planState.decisionsDiscarded++}
    mutateCheckpoint(restored,{...(decision.approved?{plan:pending.candidate.plan,explanation:pending.explanation}:{}),planState,status:'running'})
    this.planDecisionEvent={type:'plan_drift_decided',execution:restored.id,...row}
    given=undefined
   }
   const decision=given===undefined?undefined:readApprovalDecision(given)
   if(given!==undefined&&decision===undefined)throw authoredFailure('configuration','The approval decision is not one this package reads: a callId, approved, and an optional note of at most 2,000 characters. Nothing was decided or run.','context')
   const asked=restored.approval?.state==='asked'
   if(decision!==undefined&&decision.callId!==restored.approval?.callId)throw authoredFailure('stale-state','This decision is for a call this response is not waiting on; nothing was decided or run. Continue it with the decision on the call it asked about.','context')
   // A decision on an act call already decided (refused, executed, answered) is no decision this response waits for.
   if(decision!==undefined&&!asked)throw authoredFailure('stale-state','This response’s act call was already decided; nothing was decided or run. Continue it without a decision.','context')
   if(asked&&decision===undefined)throw authoredFailure('stale-state',`This response waits for the person’s decision on ${restored.approval!.name}; continue it with that decision.`,'context')
   if((restored.status==='paused'&&!asked)||restored.used>=restored.limit)throw new ExecutionPaused()
   if(restored.unit!==('model'in this.session?'model-requests':'provider-managed'))throw toolInputFailure('Continue this response with the same connection type, or start a new response.')
   await this.ledger.restore(restored.context)
   this.progress.seed(restored.operations)
   this.checkpoint=new CheckpointMutation({...restored,operations:restored.operations.map(op=>op.status==='running'?{...op,status:'interrupted'}:op),status:'running'})
   for(const source of this.state.sources??[])this.usageSources.set(source.source,{calls:source.calls,milliseconds:source.milliseconds,bytes:source.bytes})
   this.restored=true
   const approval=this.state.approval
   if(approval?.state==='asked'){
    const decidedAt=new Date().toISOString()
    this.mutate({approval:decision!.approved
     ?{...approval,state:'approved',decidedAt,...(decision!.note===undefined?{}:{note:decision!.note})}
     :{...approval,state:'refused',decidedAt,...(decision!.note===undefined?{}:{note:decision!.note}),result:refusedResult(approval.name)}})
    this.approvalStep=decision!.approved?'execute':'write'
   }else if(approval?.state==='approved'){
    // Approved, and its result never written: it may have run, and it is not run again.
    this.mutate({approval:{...approval,state:'executed',result:unknownOutcome(approval.name)}})
    this.approvalStep='write'
   }else if(approval?.state==='executed'||approval?.state==='refused')this.approvalStep='continue'
  }else{
   this.mutate({version:2,id:crypto.randomUUID(),requestKey,phase:this.session.context?.phase??'work',status:'running',unit:'model'in this.session?'model-requests':'provider-managed',used:0,limit:requestCap??('model'in this.session?MAX_TURNS:1),...(requestCap===undefined?{}:{requestCap}),grants:[],plan:[],explanation:'',operations:[],context:{scope:this.ledger.scope,entries:[]}})
   const plan=this.session.context?.plan
   if(plan&&['accepted','started'].includes(plan.status))this.mutate({plan:plan.steps.map(step=>({step,status:'pending'}))})
  }
  this.mutate({...identifyPlan(this.state.plan,this.state.planState),version:this.state.version===3?3:2})
  const accepted=this.session.context?.plan
  if(!restored&&accepted&&['accepted','started'].includes(accepted.status)){
   if(!accepted.acceptedAt||!Number.isFinite(Date.parse(accepted.acceptedAt))||accepted.questions.length||accepted.steps.length<1||accepted.steps.length>20||!accepted.steps.every(s=>s.trim()&&s.length<=1000))throw toolInputFailure('PlanAcceptanceInvalid: use the host-selected accepted plan.')
   const policy=this.session.context?.planDrift??['removed']
   if(policy.length>4||new Set(policy).size!==policy.length||policy.some(k=>!['added','removed','changed','reordered'].includes(k)))throw toolInputFailure('PlanDriftPolicyInvalid: use a bounded departure-kind set.')
   const validatedReferences:import('../planning/model').CurrentPlanReference[]=[]
   for(const ref of accepted.references){if(!this.session.context?.planResolver)throw toolInputFailure('PlanResolverRequired: verify accepted references before work.');validatedReferences.push(await resolvePlanReference(ref,this.session.context.planResolver,this.session.signal))}
   const revision=contentDigest({objective:accepted.objective,steps:accepted.steps,questions:accepted.questions,references:accepted.references})
   const plan=this.state.plan.map((step,index)=>({...step,origin:{plan:accepted.id,revision,index}}))
   const identified=identifyPlan(plan)
   identified.planState.accepted={id:accepted.id,revision,sourceRevision:accepted.revision,objective:accepted.objective,steps:structuredClone(accepted.steps),references:structuredClone(accepted.references),validatedReferences,acceptedAt:accepted.acceptedAt}
   identified.planState.driftPolicy=[...policy]
   this.mutate(identified)
  }
  if(restored?.planState?.accepted&&accepted&&(accepted.id!==restored.planState.accepted.id||accepted.revision!==restored.planState.accepted.sourceRevision||accepted.acceptedAt!==restored.planState.accepted.acceptedAt||!['accepted','started'].includes(accepted.status)||contentDigest({objective:accepted.objective,steps:accepted.steps,questions:accepted.questions,references:accepted.references})!==restored.planState.accepted.revision))throw toolInputFailure('PlanAcceptanceChanged: resume with the original accepted plan.')
 }
 get remaining(){return this.state.limit-this.state.used}
 useConnection(connection:string,role='author',selectedModel?:string){
  const binding=this.session.policyConnections?.[connection]??this.session
  const model=selectedModel??('model'in binding?binding.model.model:binding.agent.model)
  if(this.usageBound&&(connection!==this.usageConnection||role!==this.usageRole||model!==this.usageModel)){this.recordUsageElapsed();this.recordSourceUsage()}
  if(!this.usageBound)this.usageStarted=Date.now()
  this.usageConnection=connection;this.usageRole=role;this.usageModel=model;this.usageBound=true
  const usage=this.state.usage??{rows:[]};this.usageRow(usage,this.state.unit==='provider-managed'?'agent':'model',connection,role);this.mutate({usage})
 }
 private recordUsageElapsed(){
  const usage=this.state.usage??{rows:[]},row=this.usageRow(usage,this.state.unit==='provider-managed'?'agent':'model')
  const now=Date.now();row.milliseconds+=Math.max(0,now-this.usageStarted);this.usageStarted=now
  this.mutate({usage})
 }
 private recordSourceUsage(){
  if(!this.usageBound)return
  for(const source of this.state.sources??[]){
   const before=this.usageSources.get(source.source)??{calls:0,milliseconds:0,bytes:0}
   const calls=Math.max(0,source.calls-before.calls),milliseconds=Math.max(0,source.milliseconds-before.milliseconds),bytes=Math.max(0,source.bytes-before.bytes)
   if(calls||milliseconds||bytes){const usage=this.state.usage??{rows:[]},row=this.usageRow(usage,source.source);row.invocations+=calls;row.milliseconds+=milliseconds;row.bytes+=bytes;this.mutate({usage})}
   this.usageSources.set(source.source,{calls:source.calls,milliseconds:source.milliseconds,bytes:source.bytes})
  }
 }
 private usageRow(usage:import('./state').UsageLedger,source:string,connection=this.usageConnection,role=this.usageRole){
  const model=source==='model'||source==='agent'?this.usageModel:undefined
  let row=usage.rows.find(row=>row.source===source&&row.connection===connection&&row.role===role&&row.model===model)
  if(!row){row={source,connection,role,requests:0,invocations:0,milliseconds:0,bytes:0};if(model)row.model=model;usage.rows.push(row)}
  return row
 }
 mergeChildUsage(id:string,incomingUsage:import('./state').UsageLedger,role:string){
  const childUsage=this.state.childUsage??[]
  const previous=childUsage.find(row=>row.id===id)
  const next={rows:incomingUsage.rows.filter(row=>row.source==='model'||row.source==='agent').map(row=>({...structuredClone(row),role}))}
  const usage=this.state.usage??{rows:[]}
  for(const [ledger,sign] of [[previous?.usage,-1],[next,1]] as const)for(const incoming of ledger?.rows??[]){
   let row=usage.rows.find(row=>row.source===incoming.source&&row.connection===incoming.connection&&row.role===incoming.role&&row.model===incoming.model)
   if(!row){row={...incoming,requests:0,invocations:0,milliseconds:0,bytes:0};delete row.tokens;usage.rows.push(row)}
   for(const key of ['requests','invocations','milliseconds','bytes'] as const)row[key]+=sign*incoming[key]
   for(const key of Object.keys(incoming.tokens??{}) as (keyof import('./state').UsageTokens)[]){row.tokens??={};row.tokens[key]=(row.tokens[key]??0)+sign*incoming.tokens![key]!}
  }
  if(previous)previous.usage=next;else childUsage.push({id,usage:next})
  this.mutate({usage,childUsage})
 }
 usage():import('./state').UsageLedger{
  if(this.usageBound)this.recordUsageElapsed()
  this.recordSourceUsage()
  return structuredClone(this.state.usage??{rows:[]})
 }
 /** ADR-0008: whether an act call of this run waits for the person's decision; no request is sent while one does. */
 get approvalAsked(){return !!this.state.planState?.pending||this.state.approval?.state==='asked'}
 /** ADR-0021: accept host input into the checkpointed queue, without placing it in a request. */
 queueSteers(input:import('../engine').EngineInput[]|undefined,bounds:{steerBytes:number;steersPerRun:number}){
  const events:import('../engine').AssistantEvent[]=[]
  const steers=this.state.steers??{queued:[],delivered:[]}
  while(input?.length){
   const value=input.shift() as unknown
   const steer=typeof value==='object'&&value!==null?(value as {steer?:unknown}).steer:undefined
   const text=typeof steer==='object'&&steer!==null?(steer as {text?:unknown}).text:undefined
   const bytes=typeof text==='string'?utf8Bytes(text):0
   const refuse=(detail:string)=>events.push({type:'guardrail',tool:'steer',action:'refused',reason:'steer refused',detail})
   if(this.approvalAsked){refuse('A steer is refused while this run waits for the person’s approval decision.');continue}
   if(typeof text!=='string'||text.length===0){refuse('A steer must carry non-empty text.');continue}
   if(bytes>bounds.steerBytes){refuse(`A steer is larger than the ${bounds.steerBytes} byte limit.`);continue}
   if(steers.queued.length+steers.delivered.length>=bounds.steersPerRun){refuse(`This run already accepted its ${bounds.steersPerRun} steers.`);continue}
   steers.queued.push({text,bytes})
  }
  this.mutate({steers})
  return events
 }
 /** ADR-0021: deliver the checkpointed queue only at this request boundary. */
 takeSteers(input:import('../engine').EngineInput[]|undefined,marker:string,atRequest:number,bounds:{steerBytes:number;steersPerRun:number}){
  const events=this.queueSteers(input,bounds)
  const report=this.state.steers!
  const queued=report.queued.splice(0)
  const blocks=queued.map(row=>{
   report.delivered.push({bytes:row.bytes,atRequest})
   events.push({type:'steered',bytes:row.bytes,atRequest})
   return frameBlock("the person's new instruction",row.text,marker)
  })
  this.mutate({steers:report})
  return {text:blocks.join('\n\n'),events}
 }
 claim(){if(this.capFailure)throw this.capFailure;if(this.remaining<=0)throw new ExecutionPaused();const usage=this.state.usage??{rows:[]};this.usageRow(usage,'model').requests++;this.mutate({used:new CheckpointIncrement(1),usage})}
 /**
  * Fix a fresh API execution to 1..MAX_TURNS requests, including delegated children.
  * Prefer context.requestCap before initialization. Only an identical fresh repeat is accepted;
  * invalid, changed, native or started/restored calls remain refused even if caught.
  */
 limitTo(requests:number){
  if(this.capFailure)throw this.capFailure
  const state=this.state
  if(!validRequestCap(requests)||state.unit!=='model-requests'||this.restored||state.used!==0||state.grants.length!==0||this.requestCap!==undefined&&this.requestCap!==requests){this.capFailure=this.capRefusal();throw this.capFailure}
  this.requestCap=requests;this.mutate({requestCap:requests,limit:requests})
 }
 /** The instructions and the state together, as an agent's prompt carries them. */
 guidance(){return EXECUTION_INSTRUCTIONS+'\n'+this.requestState()}
 /** The part of the guidance that changes between requests: the repetition note, the request
  * counters, the plan, the operations index. A model loop sends it after the history, at the end
  * of every request, and EXECUTION_INSTRUCTIONS once in its stable system text. */
 repeatGuidance?:()=>string
 requestState(){const tools=this.toolAllowance?.();return (this.repeatGuidance?.()??this.progress.guidance)+'\n'+JSON.stringify({requests:this.state.unit==='model-requests'?{unit:this.state.unit,used:this.state.used,limit:this.state.limit,remaining:this.remaining}:{unit:this.state.unit,note:'The subscription provider manages its internal model loop; tool calls are not model-request counts.'},...(tools?{toolCalls:{unit:'tool-calls',used:tools.used,limit:tools.limit,remaining:Math.max(0,tools.limit-tools.used),scope:tools.scope,note:tools.scope==='response'?'Tool calls this response may make, yours and your delegated children’s together; the context tools are not counted.':'Tool calls this task may make; the context tools are not counted.'}}:{}),tasks:JSON.parse(importCanonicalPlan(this.state.plan)),planDecision:this.state.planState?.decisions?.length?JSON.parse(importCanonicalPlan(this.state.planState.decisions.at(-1))):undefined,planEvidence:availablePlanEvidence(this.proof?.()),planRevision:this.state.planState?.revision,planSequence:this.state.planState?.sequence,explanation:this.state.explanation,retainedOperations:this.operationsReference,...(this.restoredResults.length?{resumeFromCompletedResults:this.restoredResults}:{}),completedOperationCount:this.state.operations.length})+((loaded)=>loaded?'\n'+loaded:'')(this.loadedSchemas?.())+((skills)=>skills?'\n'+skills:'')(this.loadedSkills?.())+((draft)=>draft?'\n'+draft:'')(this.loadedDrafts?.())}
 async retainProgress(includeIndex=true){
  if(!this.state.operations.length)return
  const rows=[]
  // Retain each completed operation once. Repeated extensions must not copy
  // the entire growing operation log into another snapshot every time.
  for(const operation of this.state.operations){
   const entry=await this.ledger.retain({id:'operation-'+operation.id,kind:'history',label:operation.name+' · '+operation.status,value:operation})
   rows.push({name:operation.name,status:operation.status,id:entry.id,paths:{arguments:'/args',result:'/result'}})
  }
  this.restoredResults=rows.slice(-8)
  if(includeIndex)this.operationsReference=this.ledger.reference(await this.ledger.retain({id:'execution-operations-index',kind:'history',label:'Current task operation index; historical data, not permissions',value:rows}))
 }
 observe(event:AssistantEvent){
  if(event.type==='tool_call'){
   const id=crypto.randomUUID()
   if(event.callId)this.inFlight.set(event.callId,id)
   this.mutate({operations:[...this.state.operations,{id,name:event.name,args:structuredClone(event.args??{}),status:'running'}]})
  }
  if(event.type==='tool_result'){
   const operations=this.state.operations
   const op=event.callId?operations.find(o=>o.id===this.inFlight.get(event.callId!)):[...operations].reverse().find(o=>o.name===event.name&&o.status==='running')
   if(op){op.status=event.isError?'failed':'complete';op.result={isError:event.isError,content:[{type:'text',text:event.text}],...(event.structured===undefined?{}:{structuredContent:structuredClone(event.structured)})};if(event.callId)this.inFlight.delete(event.callId);if(!this.repeatGuidance)this.progress.observe(op);this.mutate({operations})}
  }
 }
 recordCompletion(result:import('../completion').ModelCompletion,phase:'work'|'synthesis'|'review'){
  const attempt=this.attempt;this.attempt=1
  const completion=writeRecord(PROVENANCE_SCHEMA,{request:this.state.used,phase,result,connection:this.usageConnection,role:this.usageRole,...(attempt>1?{attempt}:{})},this.recordPolicy,this.recordGuard?.())
  this.mutate({completions:[...(this.state.completions??[]),completion]})
  const usage=this.state.usage??{rows:[]},row=this.usageRow(usage,'model')
  const entries=Object.entries(result).filter(([key,value])=>key.endsWith('Tokens')&&typeof value==='number') as [keyof import('./state').UsageTokens,number][]
  if(entries.length){row.tokens??={};for(const [key,value] of entries)row.tokens[key]=(row.tokens[key]??0)+value}
  this.mutate({usage})
 }
 /**
  * ADR-0005: the digest of one request body the adapter is about to send, recorded before it is
  * sent: the SHA-256 of the body's text, numbered after the last one recorded. The newest
  * MAX_PROMPT_DIGESTS are kept.
  */
 async recordPrompt(body:string){
  // ADR-0008: the continuation request carries the approval's result; once it is sent, the approval is answered.
  const approval=this.state.approval
  if(approval&&(approval.state==='executed'||approval.state==='refused'))this.mutate({approval:{...approval,state:'answered'}})
  const sha256=await contextDigest(body)
  const prompts=this.state.prompts??[]
  this.mutate({prompts:[...prompts,{request:(prompts.at(-1)?.request??0)+1,sha256}].slice(-MAX_PROMPT_DIGESTS)})
  const usage=this.state.usage??{rows:[]},source=this.state.unit==='provider-managed'?'agent':'model',row=this.usageRow(usage,source)
  row.bytes+=utf8Bytes(body)
  if(source==='agent')row.invocations++
  this.mutate({usage})
 }
 /** Which attempt at a model request is being sent, said before each one, so a new call starts again at 1. */
 beginAttempt(attempt:number){this.attempt=attempt}
 /** A refused attempt at a model request: its own row. */
 recordRefusal(phase:'work'|'synthesis'|'review',refused:{attempt:number;failure:import('../failures').AIFailure;waitMs?:number}){
  const row=writeRecord(PROVENANCE_SCHEMA,{request:this.state.used,phase,result:{finishReason:'error' as const},connection:this.usageConnection,role:this.usageRole,...refused},this.recordPolicy,this.recordGuard?.())
  this.mutate({completions:[...(this.state.completions??[]),row]})
 }
 recordCritique(event:Extract<AssistantEvent,{type:'critique'}>):Extract<AssistantEvent,{type:'critique'}>{
  const {schema:_,version:__,digest:___,retainUntil:____,...payload}=event
  return writeRecord(CRITIQUE_SCHEMA,payload,this.recordPolicy,this.recordGuard?.())
 }
 clearReadCache(){this.cache.clear()}
 /**
  * Only immutable session reads are cached. Workflow writes and evaluations are never cached.
  *
  * `kept` (agent#8): a session tool that reads (a runtime tool, `runtime: true`, or a host tool
  * declared a read), whose answers a chat-scoped context keeps. Where the run has one, each answer
  * is retained in the chat's ledger for later runs' `read_window`, `search` and index, and an
  * immutable read is served from the chat's read cache, under `readCacheKey`, without a call. A
  * read whose result depends on the Runtime (every runtime tool, and a host tool that declares
  * `contextPolicy.runtime`) is keyed by the Runtime the host names in `context.runtime`; without
  * that it is cached for the run only, as before. The cache serves only a name the run's session
  * offers (`kept.offered`, said false for a name it does not): the call then reaches the offer
  * check and the gate as any other, and no cached entry is added to the run's view.
  *
  * `admission` (#242): an external read's receipt check, from the source gate (`receiptAdmission`).
  * Its answer is retained or cached only once the receipt verifies; one that does not is answered as
  * the gate's refusal and kept nowhere a reader could reach.
  */
 wrapRead(name:string,execute:(args:Record<string,unknown>)=>Promise<McpToolResult>,policy:ContextToolPolicy={},readOnly=false,kept?:{runtime:boolean;offered?:boolean},admission?:{admit(result:McpToolResult):Promise<void>}){
  // A mistaken hint on a write can never suppress or replay that operation.
  const immutable=policy.immutable===true&&readOnly
  return async(args:Record<string,unknown>)=>{
   if((name==='read_window'||name==='read_context')&&typeof args.id==='string'&&args.id.startsWith('draft:'))return execute(args)
   const chat=kept?this.ledger.chat:undefined
   const chatKey=chat&&immutable?await readCacheKey(name,args,kept!.runtime||policy.runtime===true,this.session.context?.runtime):undefined
   // The cache serves only a name the run's session offers: an unoffered name goes on to the inner offer check and the gate, and a served entry never joins a child's visible set.
   if(chatKey!==undefined&&kept!.offered!==false){const held=chat!.cached(chatKey);if(held)return held}
   const key=name+':'+JSON.stringify(args)
   if(chatKey===undefined&&immutable&&this.cache.has(key))return structuredClone(this.cache.get(key)!)
   const result=await execute(args)
   await admission?.admit(result)
   if(!result.isError&&chat)await chat.keep(this.ledger,name,args,result,chatKey)
   if(chatKey===undefined&&immutable&&!result.isError)this.cache.set(key,structuredClone(result))
   return result
  }
 }
 tool():HostTool {
  return {name:'update_plan',taxonomy:{operation:'own-state',effect:'pure'},effect:'plan',description:'Track this task’s execution progress. Use for multi-stage work, not simple answers. This does not approve a plan, increase limits, save artifacts or prove success. Refer to current steps by their package id; omit id only for a new step. Keep completed steps and explain material changes. At most one step is in progress.',inputSchema:{type:'object',properties:{explanation:{type:'string',maxLength:2000},plan:{type:'array',minItems:1,maxItems:20,items:{type:'object',properties:{id:{type:'string',pattern:'^step-[1-9][0-9]{0,15}$'},step:{type:'string',maxLength:1000},status:{type:'string',enum:['pending','in_progress','completed','not-needed']},detail:{type:'string',maxLength:1500},...(this.session.context?.taskSteps===true?{dependsOn:{type:'array',maxItems:19,uniqueItems:true,items:{type:'string',pattern:'^step-[1-9][0-9]{0,15}$'}},budget:{type:'object',properties:{requests:{type:'integer',minimum:1,maximum:100}},required:['requests'],additionalProperties:false},...((this.taskProfiles?.().length??0)>0?{delegation:{enum:this.taskProfiles!()}}:{})}:{})},required:['step','status'],additionalProperties:false}}},required:['explanation','plan'],additionalProperties:false},execute:async(args,signal)=>this.serialPlan(async()=>{
   signal.throwIfAborted()
   if(this.state.planState?.pending)throw toolInputFailure('PlanApprovalRequired: this run waits for its plan decision.')
   if(Array.isArray(args.plan)&&args.plan.some(s=>s&&typeof s==='object'&&Object.keys(s).some(k=>!(this.session.context?.taskSteps===true?['id','step','status','detail','dependsOn','budget','delegation']:['id','step','status','detail']).includes(k)))){this.planRefused?.();throw toolInputFailure('PlanIdentityInvalid: package-owned task fields cannot be supplied.')}
   if(Object.keys(args).some(k=>!["explanation","plan"].includes(k))||!validPlanInput(args.plan,this.session.context?.taskSteps===true)||typeof args.explanation!=='string'||args.explanation.length>2000){this.planRefused?.();throw toolInputFailure('Provide one to twenty task steps, at most one in progress, and a short explanation.')}
   let next:ReturnType<typeof revisePlan>
   try{next=revisePlan(identifyPlan(this.state.plan,this.state.planState),args.plan,args.explanation)}catch(error){this.planRefused?.();throw error}
   const event:Extract<AssistantEvent,{type:'plan_revised'}>={type:'plan_revised',execution:this.state.id,...next.planState.history.at(-1)!}
   await this.applyPlan(next,args.explanation,event)
   return answer({status:this.state.planState?.pending?'approval-required':'progress-recorded',plan:this.state.plan,planRevision:this.state.planState!.revision,planSequence:this.state.planState!.sequence,...(this.state.unit==='model-requests'?{remaining:this.remaining}:{budget:'provider-managed'}),permissionsUnchanged:true})
  })}
 }
 approvalNotice():Extract<AssistantEvent,{type:'approval_requested'}>|undefined{
  const pending=this.state.planState?.pending
  if(pending)return {type:'approval_requested',purpose:'plan-drift',callId:pending.callId,plan:{execution:this.state.id,revision:pending.revision,candidateRevision:pending.candidate.planState.revision,explanation:pending.explanation,departures:structuredClone(pending.departures)}}
  const asked=this.state.approval
  if(asked?.state==='asked')return {type:'approval_requested',callId:asked.callId,name:asked.name,tool:asked.tool,source:structuredClone(asked.source),args:structuredClone(asked.args),...(asked.runtime?{runtime:structuredClone(asked.runtime)}:{})}
 }
 private async applyPlan(next:ReturnType<typeof identifyPlan>,explanation:string,event:PlanEvent){
  const before={plan:structuredClone(this.state.plan),planState:structuredClone(this.state.planState),explanation:this.state.explanation,status:this.state.status}
  try{
  const changes=changedDepartures(identifyPlan(this.state.plan,this.state.planState),next,explanation)
  if(changes.length){
   const state=next.planState;state.departures??=[];state.departuresDiscarded??=0
   state.departures.push({revision:state.revision,explanation,changes});if(state.departures.length>32){state.departures.shift();state.departuresDiscarded++}
   if(changes.some(change=>(state.driftPolicy??['removed']).includes(change.kind))){
    const pending={purpose:'plan-drift' as const,callId:crypto.randomUUID(),revision:this.state.planState!.revision,candidate:next,explanation,departures:changes}
    this.mutate({planState:{...this.state.planState!,pending},status:'paused'})
    await this.publishPlan?.(this.snapshot(),this.approvalNotice()!)
    return
   }
  }
  this.mutate({...next,explanation})
  if(changes.length)await this.publishPlan?.(this.snapshot(),{type:'plan_departed',execution:this.state.id,revision:next.planState.revision,acceptedRevision:next.planState.accepted!.revision,explanation,departures:changes})
  await this.publishPlan?.(this.snapshot(),event)
  }catch(error){this.mutate(before);this.planRefused?.();throw error}
 }
 completionTool():HostTool{return {name:'complete_plan_step',taxonomy:{operation:'own-state',effect:'pure'},effect:'plan',description:'Close only the current active step with acknowledged execution evidence, or as not-needed with a reason. Pass its exact id and current planRevision.',inputSchema:{type:'object',properties:{id:{type:'string',maxLength:100},expectedRevision:{type:'string',pattern:'^sha256:[a-f0-9]{64}$'},outcome:{enum:['completed','not-needed']},reason:{type:'string',minLength:1,maxLength:1500},evidence:{type:'object',properties:{...(this.session.context?.taskSteps===true?{child:{type:'object',properties:{task:{type:'string',maxLength:100},checkpointDigest:{type:'string',pattern:'^sha256:[a-f0-9]{64}$'},resultDigest:{type:'string',pattern:'^sha256:[a-f0-9]{64}$'},eventDigest:{type:'string',pattern:'^sha256:[a-f0-9]{64}$'},commitId:{type:'string',maxLength:100}},required:['task','checkpointDigest','resultDigest','eventDigest','commitId'],additionalProperties:false}}:{}),operations:{type:'array',maxItems:20,items:{type:'string',maxLength:256}},artifacts:{type:'array',maxItems:20,items:{type:'object',properties:{kind:{enum:['artifact','dialect']},id:{type:'string',maxLength:256},digest:{type:'string',pattern:'^sha256:[a-f0-9]{64}$'}},required:['kind','id','digest'],additionalProperties:false}}},required:['operations','artifacts'],additionalProperties:false}},required:['id','expectedRevision','outcome'],additionalProperties:false,oneOf:[{"properties":{"outcome":{"const":"completed"}},"required":["evidence"],"not":{"required":["reason"]}},{"properties":{"outcome":{"const":"not-needed"}},"required":["reason"],"not":{"required":["evidence"]}}]},execute:async(args,signal)=>this.serialPlan(async()=>{
  signal.throwIfAborted()
  if(this.state.planState?.pending)throw toolInputFailure('PlanApprovalRequired: this run waits for its plan decision.')
  const current=identifyPlan(this.state.plan,this.state.planState),result=closePlan(current,args,this.proof?.())
  if(result.reason){
   const next=appendRefusal(current,result.reason,typeof args.id==='string'?args.id:undefined);this.mutate(next)
   const event={type:'plan_completion_refused' as const,execution:this.state.id,...next.planState.refusals!.at(-1)!}
   await this.publishPlan?.(this.snapshot(),event)
   throw toolInputFailure(`${result.reason==='evidence-unavailable'?'PlanEvidenceUnavailable':'PlanEvidenceInvalid'}: completion refused (${result.reason}); the active step remains in progress.`)
  }
  const next=result.next!,row=next.planState.history.at(-1)!
  await this.applyPlan(next,row.explanation,{type:'plan_revised',execution:this.state.id,...row})
  return answer({status:args.outcome,plan:this.state.plan,planRevision:this.state.planState!.revision})
 })}}

 budgetTool():HostTool {
  return {name:'request_budget_increase',taxonomy:{operation:'ask',effect:'pure'},effect:'plan',description:'Pause near the request limit and ask the person for more compute. Explain useful remaining work and the next concrete step. This never grants more requests or action permissions. Use ask_user for missing policy decisions instead.',inputSchema:{type:'object',properties:{reason:{type:'string',maxLength:1000},nextStep:{type:'string',maxLength:900}},required:['reason','nextStep'],additionalProperties:false},execute:async(args,signal)=>{
   signal.throwIfAborted()
   if(typeof args.reason!=='string'||!args.reason.trim()||args.reason.length>1000||typeof args.nextStep!=='string'||!args.nextStep.trim()||args.nextStep.length>900)throw toolInputFailure('Explain why more requests are needed and the next concrete step.')
   if(this.state.unit!=='model-requests')return answer({status:'unavailable',message:'The subscription agent owns its internal request budget. Return findings or ask for missing information.'})
   if(this.remaining>2)return answer({status:'within-allowance',remaining:this.remaining,message:'Continue useful work within the current allowance. Do not ask for an increase yet.'})
   this.mutate({reason:args.reason+' Next: '+args.nextStep});this.pauseRequested=true
   return answer({status:'approval-required',message:'Desk will pause and present the request. Do not perform more work until the person continues.'})
  }}
 }
 snapshot(status=this.state.status){
  if(this.capFailure)throw this.capFailure
  this.mutate({usage:this.usage()})
  const retain=this.state.retainUntil
  const payload=this.state
  mutateCheckpoint(payload,{status,context:this.ledger.snapshot(),schema:undefined,digest:undefined,retainUntil:undefined})
  const snapshot=writeRecord(CHECKPOINT_SCHEMA,{...payload},this.recordPolicy,this.recordGuard?.(),new Date(),retain)
  if(utf8Bytes(JSON.stringify(snapshot))>MAX_CHECKPOINT_BYTES)throw toolInputFailure('The execution checkpoint is full. Completed work remains visible; start a focused follow-up before further work.')
  return structuredClone(snapshot)
 }
}
