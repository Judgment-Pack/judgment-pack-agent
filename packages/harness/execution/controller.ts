import {ExecutionProgressTracker} from './progress'
import type {ContextToolPolicy} from '../context/toolPolicy'
import {toolInputFailure} from '../failures'
import type {AssistantEvent,EngineSession,HostTool,McpToolResult} from '../engine'
import {answer,contextDigest,type ContextLedger} from '../context/ledger'
import {MAX_TURNS} from '../engines/contract'
import {utf8Bytes} from '../context/policy'
import {ExecutionPaused,MAX_CHECKPOINT_BYTES,readExecution,validTaskSteps,type ExecutionCheckpoint} from './state'

export const EXECUTION_INSTRUCTIONS = `TASK EXECUTION: For work requiring several stages, use update_plan to track concrete pending, in_progress and completed steps. Skip planning overhead for simple replies. Use the current accepted plan when present; do not repeatedly re-plan or ask the user to repeat an already authorized request. Update progress at meaningful boundaries, and preserve completed work across continuation. A task list is working state, not approval or proof: mark a step complete only when its required result exists, and distinguish a proposed change from a saved change. Keep a multi-stage task plan current; it estimates work but cannot grant more requests. An unsaved working object remains in the exact arguments of its completed operations. Continue from those retained arguments after validation or evaluation; rereading the saved workspace does not retrieve an unsaved proposal. Recheck the saved revision before applying changes. Use tool results already obtained; do not reread unchanged instructions or schemas or scan the entire conversation. On continuation, inspect the latest completed result and its exact arguments before doing more work. A newer saved workspace object takes precedence over an older chat draft when editing that object; preserve its identity. For a focused change, inspect the relevant contract once, make the smallest proposal, verify the affected behavior and return it. Do not restart discovery after context compaction. Before repeating a failed read, correct its arguments using the current context index. If blocked, state the exact missing information. When requests run low, finish the concrete result or call request_budget_increase with the reason and next concrete step. Desk owns budget extensions; this tool requests but never grants one. Never modify expected tests merely to match actual results. Current user instructions and host permissions remain authoritative.`

export class ExecutionController {
 readonly progress=new ExecutionProgressTracker()
 state!:ExecutionCheckpoint
 pauseRequested=false
 private operationsReference:unknown
 private restoredResults:unknown[]=[]
 private inFlight=new Map<string,string>()
 private cache=new Map<string,McpToolResult>()
 constructor(private session:EngineSession,private ledger:ContextLedger){}
 async initialize(){
  const requestKey=await contextDigest(JSON.stringify({request:this.session.context?.currentRequest??this.session.prompt,phase:this.session.context?.phase??'work',purpose:this.session.purpose??null}))
  const restored=readExecution(this.session.context?.execution)
  if(restored){
   if(restored.requestKey!==requestKey||restored.phase!==(this.session.context?.phase??'work'))throw toolInputFailure('The request or execution phase changed. Start a new response with the current context.')
   if(restored.status==='paused'||restored.used>=restored.limit)throw new ExecutionPaused()
   if(restored.unit!==('model'in this.session?'model-requests':'provider-managed'))throw toolInputFailure('Continue this response with the same connection type, or start a new response.')
   await this.ledger.restore(restored.context)
   this.progress.seed(restored.operations)
   this.state={...restored,operations:restored.operations.map(op=>op.status==='running'?{...op,status:'interrupted'}:op),status:'running'}
  }else{
   this.state={version:1,id:crypto.randomUUID(),requestKey,phase:this.session.context?.phase??'work',status:'running',unit:'model'in this.session?'model-requests':'provider-managed',used:0,limit:'model'in this.session?MAX_TURNS:1,grants:[],plan:[],explanation:'',operations:[],context:{scope:this.ledger.scope,entries:[]}}
   const plan=this.session.context?.plan
   if(plan&&['accepted','started'].includes(plan.status))this.state.plan=plan.steps.map(step=>({step,status:'pending'}))
  }
 }
 get remaining(){return this.state.limit-this.state.used}
 claim(){if(this.remaining<=0)throw new ExecutionPaused();this.state.used++}
 /** The instructions and the state together, as an agent's prompt carries them. */
 guidance(){return EXECUTION_INSTRUCTIONS+'\n'+this.requestState()}
 /** The part of the guidance that changes between requests: the repetition note, the request
  * counters, the plan, the operations index. A model loop sends it after the history, at the end
  * of every request, and EXECUTION_INSTRUCTIONS once in its stable system text. */
 requestState(){return this.progress.guidance+'\n'+JSON.stringify({requests:this.state.unit==='model-requests'?{unit:this.state.unit,used:this.state.used,limit:this.state.limit,remaining:this.remaining}:{unit:this.state.unit,note:'The subscription provider manages its internal model loop; tool calls are not model-request counts.'},tasks:this.state.plan,explanation:this.state.explanation,retainedOperations:this.operationsReference,...(this.restoredResults.length?{resumeFromCompletedResults:this.restoredResults}:{}),completedOperationCount:this.state.operations.length})}
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
   this.state.operations.push({id,name:event.name,args:structuredClone(event.args??{}),status:'running'})
  }
  if(event.type==='tool_result'){
   const op=event.callId?this.state.operations.find(o=>o.id===this.inFlight.get(event.callId!)):[...this.state.operations].reverse().find(o=>o.name===event.name&&o.status==='running')
   if(op){op.status=event.isError?'failed':'complete';op.result={isError:event.isError,content:[{type:'text',text:event.text}],...(event.structured===undefined?{}:{structuredContent:structuredClone(event.structured)})};if(event.callId)this.inFlight.delete(event.callId);this.progress.observe(op)}
  }
 }
 recordCompletion(result:import('../completion').ModelCompletion,phase:'work'|'synthesis'|'review'){
  this.state.completions=[...(this.state.completions??[]),{request:this.state.used,phase,result}]
 }
 clearReadCache(){this.cache.clear()}
 /** Only immutable session reads are cached. Workflow writes and evaluations are never cached. */
 wrapRead(name:string,execute:(args:Record<string,unknown>)=>Promise<McpToolResult>,policy:ContextToolPolicy={},readOnly=false){
  // A mistaken hint on a write can never suppress or replay that operation.
  const immutable=policy.immutable===true&&readOnly
  return async(args:Record<string,unknown>)=>{
   const key=name+':'+JSON.stringify(args)
   if(immutable&&this.cache.has(key))return structuredClone(this.cache.get(key)!)
   const result=await execute(args)
   if(immutable&&!result.isError)this.cache.set(key,structuredClone(result))
   return result
  }
 }
 tool():HostTool {
  return {name:'update_plan',effect:'plan',description:'Track this task’s execution progress. Use for multi-stage work, not simple answers. This does not approve a plan, increase limits, save artifacts or prove success. Keep completed steps and explain material changes. At most one step is in progress.',inputSchema:{type:'object',properties:{explanation:{type:'string',maxLength:2000},plan:{type:'array',minItems:1,maxItems:20,items:{type:'object',properties:{step:{type:'string',maxLength:1000},status:{type:'string',enum:['pending','in_progress','completed']},detail:{type:'string',maxLength:1500}},required:['step','status'],additionalProperties:false}}},required:['explanation','plan'],additionalProperties:false},execute:async(args,signal)=>{
   signal.throwIfAborted()
   if(!validTaskSteps(args.plan)||typeof args.explanation!=='string'||args.explanation.length>2000)throw toolInputFailure('Provide one to twenty task steps, at most one in progress, and a short explanation.')
   this.state.plan=structuredClone(args.plan);this.state.explanation=args.explanation
   return answer({status:'progress-recorded',plan:this.state.plan,...(this.state.unit==='model-requests'?{remaining:this.remaining}:{budget:'provider-managed'}),permissionsUnchanged:true})
  }}
 }
 budgetTool():HostTool {
  return {name:'request_budget_increase',effect:'plan',description:'Pause near the request limit and ask the person for more compute. Explain useful remaining work and the next concrete step. This never grants more requests or action permissions. Use ask_user for missing policy decisions instead.',inputSchema:{type:'object',properties:{reason:{type:'string',maxLength:1000},nextStep:{type:'string',maxLength:900}},required:['reason','nextStep'],additionalProperties:false},execute:async(args,signal)=>{
   signal.throwIfAborted()
   if(typeof args.reason!=='string'||!args.reason.trim()||args.reason.length>1000||typeof args.nextStep!=='string'||!args.nextStep.trim()||args.nextStep.length>900)throw toolInputFailure('Explain why more requests are needed and the next concrete step.')
   if(this.state.unit!=='model-requests')return answer({status:'unavailable',message:'The subscription agent owns its internal request budget. Return findings or ask for missing information.'})
   if(this.remaining>2)return answer({status:'within-allowance',remaining:this.remaining,message:'Continue useful work within the current allowance. Do not ask for an increase yet.'})
   this.state.reason=args.reason+' Next: '+args.nextStep;this.pauseRequested=true
   return answer({status:'approval-required',message:'Desk will pause and present the request. Do not perform more work until the person continues.'})
  }}
 }
 snapshot(status=this.state.status){
  const snapshot={...this.state,status,context:this.ledger.snapshot()}
  if(utf8Bytes(JSON.stringify(snapshot))>MAX_CHECKPOINT_BYTES)throw toolInputFailure('The execution checkpoint is full. Completed work remains visible; start a focused follow-up before further work.')
  return structuredClone(snapshot)
 }
}
