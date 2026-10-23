import {CheckpointIncrement,CheckpointMutation,mutateCheckpoint,type CheckpointPatch} from './mutation'
import {ExecutionProgressTracker} from './progress'
import type {ContextToolPolicy} from '../context/toolPolicy'
import {authoredFailure,toolInputFailure} from '../failures'
import type {AssistantEvent,EngineSession,HostTool,McpToolResult} from '../engine'
import {answer,contextDigest,type ContextLedger} from '../context/ledger'
import {readCacheKey} from '../context/chat'
import {MAX_TURNS} from '../engines/contract'
import {utf8Bytes} from '../context/policy'
import {frameBlock} from '../framing'
import {ExecutionPaused,MAX_CHECKPOINT_BYTES,MAX_PROMPT_DIGESTS,readApprovalDecision,readExecution,refusedResult,unknownOutcome,validTaskSteps,type ExecutionCheckpoint} from './state'
import {CHECKPOINT_SCHEMA,CRITIQUE_SCHEMA,PROVENANCE_SCHEMA,recordsPolicy,refuseExpired,writeRecord,type RecordGuard,type RecordsPolicy} from '../records'

export const EXECUTION_INSTRUCTIONS = `TASK EXECUTION: For work requiring several stages, use update_plan to track concrete pending, in_progress and completed steps. Skip planning overhead for simple replies. Use the current accepted plan when present; do not repeatedly re-plan or ask the user to repeat an already authorized request. Update progress at meaningful boundaries, and preserve completed work across continuation. A task list is working state, not approval or proof: mark a step complete only when its required result exists, and distinguish a proposed change from a saved change. Keep a multi-stage task plan current; it estimates work but cannot grant more requests. An unsaved working object remains in the exact arguments of its completed operations. Continue from those retained arguments after validation or evaluation; rereading the saved workspace does not retrieve an unsaved proposal. Recheck the saved revision before applying changes. Use tool results already obtained; do not reread unchanged instructions or schemas or scan the entire conversation. On continuation, inspect the latest completed result and its exact arguments before doing more work. A newer saved workspace object takes precedence over an older chat draft when editing that object; preserve its identity. For a focused change, inspect the relevant contract once, make the smallest proposal, verify the affected behavior and return it. Do not restart discovery after context compaction. Before repeating a failed read, correct its arguments using the current context index. If blocked, state the exact missing information. When requests run low, finish the concrete result or call request_budget_increase with the reason and next concrete step. Desk owns budget extensions; this tool requests but never grants one. Never modify expected tests merely to match actual results. Current user instructions and host permissions remain authoritative.`

/** A tool-call cap as the request state states it (agent#13): `response`, a parent's and its children's together; `task`, one child's. */
export interface ToolAllowance {used:number;limit:number;scope:'response'|'task'}

export class ExecutionController {
 readonly progress=new ExecutionProgressTracker()
 private checkpoint=new CheckpointMutation()
 get state():ExecutionCheckpoint{return this.checkpoint.read()}
 get revision(){return this.checkpoint.revision}
 mutate(patch:CheckpointPatch){return this.checkpoint.apply(patch)}
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
 async initialize(){
  const requestKey=await contextDigest(JSON.stringify({request:this.session.context?.currentRequest??this.session.prompt,phase:this.session.context?.phase??'work',purpose:this.session.purpose??null}))
  const restored=readExecution(this.session.context?.execution)
  if(restored){
   if(restored.retainUntil)refuseExpired(restored as {retainUntil:string})
   if(restored.requestKey!==requestKey||restored.phase!==(this.session.context?.phase??'work'))throw toolInputFailure('The request or execution phase changed. Start a new response with the current context.')
   // ADR-0008: a paused act call continues only with the person's decision for that call.
   const given=this.session.context?.approval
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
   this.mutate({version:1,id:crypto.randomUUID(),requestKey,phase:this.session.context?.phase??'work',status:'running',unit:'model'in this.session?'model-requests':'provider-managed',used:0,limit:'model'in this.session?MAX_TURNS:1,grants:[],plan:[],explanation:'',operations:[],context:{scope:this.ledger.scope,entries:[]}})
   const plan=this.session.context?.plan
   if(plan&&['accepted','started'].includes(plan.status))this.mutate({plan:plan.steps.map(step=>({step,status:'pending'}))})
  }
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
 get approvalAsked(){return this.state.approval?.state==='asked'}
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
 claim(){if(this.remaining<=0)throw new ExecutionPaused();const usage=this.state.usage??{rows:[]};this.usageRow(usage,'model').requests++;this.mutate({used:new CheckpointIncrement(1),usage})}
 /**
  * A first allowance below MAX_TURNS, for a delegated child (agent#15): its profile's request budget,
  * held further to what the response's shared allowance can still give it, so its loop plans its
  * final answer within what it may spend. A restored execution keeps its own.
  */
 limitTo(requests:number){if(!this.restored)this.mutate({limit:Math.max(1,Math.min(this.state.limit,Math.floor(requests)))})}
 /** The instructions and the state together, as an agent's prompt carries them. */
 guidance(){return EXECUTION_INSTRUCTIONS+'\n'+this.requestState()}
 /** The part of the guidance that changes between requests: the repetition note, the request
  * counters, the plan, the operations index. A model loop sends it after the history, at the end
  * of every request, and EXECUTION_INSTRUCTIONS once in its stable system text. */
 requestState(){const tools=this.toolAllowance?.();return this.progress.guidance+'\n'+JSON.stringify({requests:this.state.unit==='model-requests'?{unit:this.state.unit,used:this.state.used,limit:this.state.limit,remaining:this.remaining}:{unit:this.state.unit,note:'The subscription provider manages its internal model loop; tool calls are not model-request counts.'},...(tools?{toolCalls:{unit:'tool-calls',used:tools.used,limit:tools.limit,remaining:Math.max(0,tools.limit-tools.used),scope:tools.scope,note:tools.scope==='response'?'Tool calls this response may make, yours and your delegated children’s together; the context tools are not counted.':'Tool calls this task may make; the context tools are not counted.'}}:{}),tasks:this.state.plan,explanation:this.state.explanation,retainedOperations:this.operationsReference,...(this.restoredResults.length?{resumeFromCompletedResults:this.restoredResults}:{}),completedOperationCount:this.state.operations.length})+((loaded)=>loaded?'\n'+loaded:'')(this.loadedSchemas?.())+((skills)=>skills?'\n'+skills:'')(this.loadedSkills?.())+((draft)=>draft?'\n'+draft:'')(this.loadedDrafts?.())}
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
   if(op){op.status=event.isError?'failed':'complete';op.result={isError:event.isError,content:[{type:'text',text:event.text}],...(event.structured===undefined?{}:{structuredContent:structuredClone(event.structured)})};if(event.callId)this.inFlight.delete(event.callId);this.progress.observe(op);this.mutate({operations})}
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
  */
 wrapRead(name:string,execute:(args:Record<string,unknown>)=>Promise<McpToolResult>,policy:ContextToolPolicy={},readOnly=false,kept?:{runtime:boolean;offered?:boolean}){
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
   if(!result.isError&&chat)await chat.keep(this.ledger,name,args,result,chatKey)
   if(chatKey===undefined&&immutable&&!result.isError)this.cache.set(key,structuredClone(result))
   return result
  }
 }
 tool():HostTool {
  return {name:'update_plan',effect:'plan',description:'Track this task’s execution progress. Use for multi-stage work, not simple answers. This does not approve a plan, increase limits, save artifacts or prove success. Keep completed steps and explain material changes. At most one step is in progress.',inputSchema:{type:'object',properties:{explanation:{type:'string',maxLength:2000},plan:{type:'array',minItems:1,maxItems:20,items:{type:'object',properties:{step:{type:'string',maxLength:1000},status:{type:'string',enum:['pending','in_progress','completed']},detail:{type:'string',maxLength:1500}},required:['step','status'],additionalProperties:false}}},required:['explanation','plan'],additionalProperties:false},execute:async(args,signal)=>{
   signal.throwIfAborted()
   if(!validTaskSteps(args.plan)||typeof args.explanation!=='string'||args.explanation.length>2000)throw toolInputFailure('Provide one to twenty task steps, at most one in progress, and a short explanation.')
   this.mutate({plan:args.plan,explanation:args.explanation})
   return answer({status:'progress-recorded',plan:this.state.plan,...(this.state.unit==='model-requests'?{remaining:this.remaining}:{budget:'provider-managed'}),permissionsUnchanged:true})
  }}
 }
 budgetTool():HostTool {
  return {name:'request_budget_increase',effect:'plan',description:'Pause near the request limit and ask the person for more compute. Explain useful remaining work and the next concrete step. This never grants more requests or action permissions. Use ask_user for missing policy decisions instead.',inputSchema:{type:'object',properties:{reason:{type:'string',maxLength:1000},nextStep:{type:'string',maxLength:900}},required:['reason','nextStep'],additionalProperties:false},execute:async(args,signal)=>{
   signal.throwIfAborted()
   if(typeof args.reason!=='string'||!args.reason.trim()||args.reason.length>1000||typeof args.nextStep!=='string'||!args.nextStep.trim()||args.nextStep.length>900)throw toolInputFailure('Explain why more requests are needed and the next concrete step.')
   if(this.state.unit!=='model-requests')return answer({status:'unavailable',message:'The subscription agent owns its internal request budget. Return findings or ask for missing information.'})
   if(this.remaining>2)return answer({status:'within-allowance',remaining:this.remaining,message:'Continue useful work within the current allowance. Do not ask for an increase yet.'})
   this.mutate({reason:args.reason+' Next: '+args.nextStep});this.pauseRequested=true
   return answer({status:'approval-required',message:'Desk will pause and present the request. Do not perform more work until the person continues.'})
  }}
 }
 snapshot(status=this.state.status){
  this.mutate({usage:this.usage()})
  const retain=this.state.retainUntil
  const payload=this.state
  mutateCheckpoint(payload,{status,context:this.ledger.snapshot(),schema:undefined,digest:undefined,retainUntil:undefined})
  const snapshot=writeRecord(CHECKPOINT_SCHEMA,{...payload},this.recordPolicy,this.recordGuard?.(),new Date(),retain)
  if(utf8Bytes(JSON.stringify(snapshot))>MAX_CHECKPOINT_BYTES)throw toolInputFailure('The execution checkpoint is full. Completed work remains visible; start a focused follow-up before further work.')
  return structuredClone(snapshot)
 }
}
