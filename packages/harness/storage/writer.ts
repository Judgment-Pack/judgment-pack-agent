import {validNativePlanAttempt,nativePlanReceiptMatches,verifyNativePlanEvidence,type NativePlanAttempt} from './nativePlanPause'
import type {NativeCaptureBindings} from './physicalDispatch'
import {WriterCapture} from './writerCapture'
import {observeRepeat,repeatKey,canonicalRepeatArgs,validRepeatTools,RepeatedToolLimit,type RepeatTools,type RepeatExecution,type RepeatPolicy,type RepeatDetected} from '../repeatTools'
import {REPEAT_REMINDER} from '../prompt'
import {TaskStepRefused,taskDefinitions,taskStatuses,validateTaskPlan,requireTaskProfiles,reconcileTaskSchedule,reserveTaskStep,readyTaskSteps,bindTaskChildren,type TaskSchedule,type TaskStepEnvelope,type TaskEnvelopeBinding,type TaskCompletionEvidence,readTaskStepEnvelope} from '../taskSteps'
import {readDelegationRequest,type DelegationRequest,type DelegatedTask} from '../delegationContract'
import type {SubagentRecord} from '../subagents'
import {ExecutionPaused} from '../execution/state'
import {openNativeWitness,closeNativeWitness,admittedNativeWitness} from './physicalDispatch'
import {UsageJournalOwner,type UsageBudgetInput,type UsageDispatchFacts} from './usageJournal'
import {UsageBudgetIdentityChanged,UsageBudgetUnknown,UsageBudgetUnsupported,type UsageAmounts} from './usageBudget'
import type {StoredChatScope} from '../context/storedChat'
import type {HistoryRunStore,RunHistoryReceipt} from './history'
import {LiveHistoryRecorder} from '../history/live'
import {prepareHistoryOriginal,prepareHistoryCompaction,type PreparedHistoryObservation} from '../history/recording'
import {historyBytesDigest,historyText,readHistoryRequestJSON,validHistoryRef,HISTORY_MAX_ROWS,HISTORY_MAX_EVENTS,HISTORY_TOTAL_BYTES,type HistoryObjectRef} from '../history/codec'
import {readHistoryHead,readHistoryEntry,copyCorpusManifestEnvelope} from '../history/view'
import type {TurnRoute} from '../turnRoute'
import {retainedRepresentation} from '../context/ledger'
import {ProviderReleaseUnknown} from './release'
import {fencedEffect,isNeverAdmittedFailure,takeFreshReauthorization,takeReauthorizedDispatch} from './lease'
import {readingTool} from '../framing'
import {canonicalJSON,exactArgumentsText,exactlyHeld} from '../context/chat'
import {AIFailureError,DURABLE_REQUEST_UNRECORDABLE,failure,normalizeFailure} from '../failures'
import {readExecution,type ExecutionCheckpoint} from '../execution/state'
import {mutateCheckpoint} from '../execution/mutation'
import {writeRunEvent,checkRecord,checkRecordVersion,readRunEvent} from '../recordReader'
import {CHECKPOINT_SCHEMA,recordDigest,sha256,writeRecord,validRecord,type RecordGuard,type RecordsPolicy} from '../records'
import type {AssistantEvent,EngineSession,McpToolResult,HostTool} from '../engine'
import {checkpointCallbackRunStore,type RunStore,type RunHead} from './run'
import {StorageConflict,requireId,storageId,STORAGE_RECORD_BYTES} from './common'
export interface DurableScope {execution:string;role:string;task?:string}
export interface ArtifactObservation {kind:'artifact'|'dialect';id:string;digest:string;access?:'read'|'produced'}
export interface DurableDispatch {planPause?:import('../execution/plan').NativePlanPause;id:string;scope:DurableScope;kind:'model'|'agent'|'host'|'runtime';source:string;tool:string;fingerprint:string;request:string;requestDigest:string;state:'claimed'|'result'|'unknown';read:boolean;result?:unknown;repeatResultJson?:string;failure?:import('../failures').AIFailure;artifacts?:ArtifactObservation[]}
export interface DurableObservation {id:string;scope:DurableScope;kind:'operation'|'artifact-read'|'artifact-produced';source:string;dispatchId:string;commitId:string;artifact?:ArtifactObservation}
export interface DurableBook {dispatches:DurableDispatch[];observations:DurableObservation[];children:{scope:DurableScope;checkpoint:ExecutionCheckpoint}[];pendingHandoff?:{callId:string;request:import('../delegationContract').DelegationRequest}}
export interface DurableRunInput {store:RunStore;id:string;/** Explicit host action. load/get/reload never resume. */resume?:boolean;context?:StoredChatScope}
export class ContinuationNotRecoverable extends AIFailureError {constructor(){super({...failure('storage','storage','not-started'),detail:'ContinuationNotRecoverable: exact safe continuation bytes cannot be retained.'});this.name='ContinuationNotRecoverable'}}
export class StorageOutcomeUnknown extends AIFailureError {constructor(cause?:unknown){super({...failure('storage','storage','unknown'),detail:'StorageOutcomeUnknown: the dispatched operation result was not acknowledged.'});this.name='StorageOutcomeUnknown';if(cause!==undefined)Object.defineProperty(this,'cause',{value:cause})}}
export class StorageAcknowledgementInvalid extends AIFailureError {constructor(){super({...failure('storage','storage','unknown'),detail:'StorageAcknowledgementInvalid: the host did not acknowledge this exact transaction.'});this.name='StorageAcknowledgementInvalid'}}
export class HostOutcomeUnknown extends AIFailureError {constructor(){super({...failure('unknown','tool','unknown'),detail:'HostOutcomeUnknown: this operation may have run; it was not run again.'});this.name='HostOutcomeUnknown'}}
/**
 * A write that reads the same as one recorded in this run and is written differently (#275): the same
 * numbers in another spelling, a repeated name, two names Go's encoding/json folds together. It may be
 * that write and it may not, so it is neither run, which could perform a write twice, nor answered from
 * that record, which could report a write nothing performed.
 */
export class DurableRequestConflict extends AIFailureError {constructor(){super({...failure('invalid-tool-input','tool','not-started'),detail:'DurableRequestConflict: this call reads the same as a write already recorded in this run but is written differently, so it may be that write. It was not run and was not answered from that record. Repeat that write exactly for its result; a different write needs different values.'});this.name='DurableRequestConflict'}}
/**
 * A call whose arguments the durable record cannot hold as written (#275): a repeated member name, a
 * number out of range or past what JavaScript holds exactly, or a lone surrogate. Refused before anything is claimed, and the
 * run goes on. JSON.parse has already rounded a host write's integer past 2^53 and read 1e400 as
 * Infinity, so the write those arguments would run is not the one the model wrote (`exactlyHeld`).
 */
export class DurableRequestUnrecordable extends AIFailureError {constructor(){super({...failure('invalid-tool-input','tool','not-started'),detail:DURABLE_REQUEST_UNRECORDABLE});this.name='DurableRequestUnrecordable'}}
export class ContinuationIdentityChanged extends AIFailureError {constructor(){super({...failure('stale-state','context','not-started'),detail:'ContinuationIdentityChanged: the admitted continuation identity differs.'});this.name='ContinuationIdentityChanged'}}
/**
 * What a request with a recorded row's fingerprint is to that row (#275), from the exact text. The
 * fingerprint is published, persisted and re-derived when a book is read; it says two requests read alike,
 * not that they are one write. The same text is the same write, and so are two requests their parsed form
 * says exactly: they differ only in whitespace, a string's escapes or the order of names that do not fold
 * together, which the fingerprint has always left out, so a resumed run's repeat of a write is never
 * performed twice. Otherwise exact numbers that differ make a different write. What remains is the same
 * values written differently, and nothing here says which they are. A host tool's request is always its
 * canonical JSON, so this decides for rows another writer recorded (`compareDurableRequests` in
 * go/context/run_writer.go) and for a direct caller's text. It reads only the row's request, which every
 * published release has recorded byte for byte, so an old row needs no migration.
 */
export function compareDurableRequests(recorded:string,request:string):'same'|'different'|'conflict'{
 if(recorded===request||exactArgumentsText(recorded)&&exactArgumentsText(request))return 'same'
 try{return canonicalRepeatArgs(recorded)!==canonicalRepeatArgs(request)?'different':'conflict'}catch{return 'conflict'}
}
export {exactlyHeld}
const recordableRequest=(request:string)=>{try{readHistoryRequestJSON(request);return true}catch{return false}}
export const durableOwner=Symbol('Agent durable writer')
/** Trusted coordinator accounting, before the root dispatch claim is frozen. */
export const prepareProviderDispatch=Symbol('Agent coordinator provider preparation')
interface NativeTaskEnvelopeSource {limit:number;childReserve:number;counts:()=>{execution:string;count:number}[];reserve:(scope:DurableScope)=>number}
export interface DurableBinding {writer:RunWriter;scope:DurableScope;root:boolean}
// Registration is keyed by source-created tool identity. No name, symbol or wire member grants it.
const taskDelegationTools=new WeakMap<HostTool,{binding:DurableBinding;schema:string}>()
export function copyTaskDelegationTool(original:HostTool,wrapped:HostTool):HostTool{
 const held=taskDelegationTools.get(original)
 if(held){if(wrapped.name!==original.name||canonicalJSON(wrapped.inputSchema)!==held.schema)throw new ContinuationIdentityChanged();taskDelegationTools.set(wrapped,held)}
 return wrapped
}
export function registeredTaskDelegationTool(tool:HostTool):boolean{
 const held=taskDelegationTools.get(tool)
 return !!held&&tool.name==='delegate_plan_steps'&&canonicalJSON(tool.inputSchema)===held.schema
}
export function normalizeTaskDelegation(tool:HostTool,binding:DurableBinding|undefined,args:Record<string,unknown>,signal?:AbortSignal):Promise<DelegationRequest>|undefined{
 const held=taskDelegationTools.get(tool)
 if(!held)return undefined
 if(!binding||held.binding.writer!==binding.writer||held.binding.root!==binding.root||scopeKey(held.binding.scope)!==scopeKey(binding.scope)||tool.name!=='delegate_plan_steps'||canonicalJSON(tool.inputSchema)!==held.schema)throw new ContinuationIdentityChanged()
 return binding.writer.prepareTaskDelegation(binding,args,signal)
}
const modelClaim=Symbol('private live model claim')
type PendingModel={readonly [modelClaim]:true;readonly request:string}
export type ModelOutcome={ok:true}|{ok:false;error:unknown;verifiedRefusal?:true}
type NativeRouteClaim={scope:DurableScope;id:string;requestDigest:string;request:string;source:string;tool:string;route:string;live:boolean;released:boolean;needed?:string;consumed:boolean}
class DispatchCanceled {constructor(readonly reason:unknown){}}
const encoder=new TextEncoder(),digest=(text:string)=>`sha256:${sha256(text)}`
/** Preserve lexical retained-value evidence before recursively canonicalizing checkpoint bytes. */
function retainedCheckpoint(checkpoint:ExecutionCheckpoint,populate:boolean,policy?:RecordsPolicy):string{
 const signatures:unknown[]=[]
 const visit=(state:ExecutionCheckpoint)=>{
  for(const entry of state.context.entries){
   const held=retainedRepresentation(entry)
   if(sha256(held.text)!==entry.digest||encoder.encode(held.text).length!==entry.bytes||!entry.id.startsWith(state.context.scope+':'))throw new ContinuationNotRecoverable()
   if(populate)entry.valueJSON=held.text
   else if(entry.valueJSON===undefined)throw new ContinuationNotRecoverable()
   signatures.push([entry.id,entry.digest,entry.bytes,entry.valueJSON])
  }
  for(const child of state.durable?.children??[]){
   checkRecord('checkpoint',child.checkpoint)
   if(child.checkpoint.id!==child.scope.execution||child.checkpoint.schema!==CHECKPOINT_SCHEMA||!validRecord(child.checkpoint))throw new ContinuationNotRecoverable()
   const until=child.checkpoint.retainUntil
   visit(child.checkpoint)
   if(populate)mutateCheckpoint(child.checkpoint,writeRecord(CHECKPOINT_SCHEMA,{...child.checkpoint},policy!,undefined,new Date(),until))
  }
 }
 try{visit(checkpoint);return canonicalJSON(signatures)}catch{throw new ContinuationNotRecoverable()}
}
const scopeKey=(scope:DurableScope)=>canonicalJSON(scope)
const checkpointProfiles=new WeakMap<object,string>()
/** Internal identity of the acknowledged legacy callback profile; never journal authority. */
export const legacyCheckpointIdentity=(save:object|undefined)=>save?checkpointProfiles.get(save):undefined
function safeRequest(text:string){
 if(encoder.encode(text).length>STORAGE_RECORD_BYTES)throw new ContinuationNotRecoverable()
 // Only prepared bodies, never transport headers. Explicit provider reasoning blocks cannot persist.
 let value:unknown;try{value=JSON.parse(text)}catch{throw new ContinuationNotRecoverable()}
 const visit=(v:unknown,depth=0):void=>{if(depth>64)throw new ContinuationNotRecoverable();if(!v||typeof v!=='object')return;if(['reasoning','thinking','redacted_thinking'].includes(String((v as {type?:unknown}).type)))throw new ContinuationNotRecoverable();for(const [key,child] of Object.entries(v)){if(key==='body'&&typeof child==='string'){try{visit(JSON.parse(child),depth+1)}catch(error){if(error instanceof ContinuationNotRecoverable)throw error}}else visit(child,depth+1)}}
 visit(value)
}
export class RunWriter {
 private readonly repeatRequestCharges=new Map<string,(body:string)=>Promise<void>>()
 attachRepeatRequestCharge(binding:DurableBinding,charge:(body:string)=>Promise<void>){if(binding.writer!==this||!this.repeatState(binding)||this.repeatRequestCharges.has(binding.scope.execution))throw new ContinuationIdentityChanged();this.repeatRequestCharges.set(binding.scope.execution,charge)}
 repeatRequestChargeInstalled(binding:DurableBinding){return binding.writer===this&&this.repeatRequestCharges.has(binding.scope.execution)&&this.repeatState(binding)!==undefined}
 private repeatTools?:RepeatTools
 private repeatScopes=new Map<string,{accept:(state:RepeatExecution)=>void;event:(event:AssistantEvent)=>void}>()
 /** Original source-owned admission; a retained counter is never a new host policy grant. */
 attachRepeatScope(binding:DurableBinding,configuration:RepeatPolicy|undefined,source:{accept:(state:RepeatExecution)=>void;event:(event:AssistantEvent)=>void}){
  if(binding.writer!==this)throw new ContinuationIdentityChanged()
  const prior=this.repeatTools?.executions.find(row=>row.execution===binding.scope.execution)
  if(!configuration){if(prior)throw new ContinuationIdentityChanged();return}
  if(prior&&(prior.remindAt!==configuration.remindAt||prior.stopAt!==configuration.stopAt)||!prior&&this.restoredTaskHead&&(binding.root||this.book.children.some(child=>child.scope.execution===binding.scope.execution)))throw new ContinuationIdentityChanged()
  if(!prior){this.repeatTools??={executions:[]};if(this.repeatTools.executions.length>=33)throw new ContinuationNotRecoverable();this.repeatTools.executions.push({execution:binding.scope.execution,...configuration,count:0,reminded:false,reminderDelivered:false,stopped:false,seen:[]})}
  this.repeatScopes.set(binding.scope.execution,source);source.accept(this.repeatState(binding)!)
 }
 repeatState(binding:DurableBinding):RepeatExecution|undefined{if(binding.writer!==this)throw new ContinuationIdentityChanged();return structuredClone(this.repeatTools?.executions.find(row=>row.execution===binding.scope.execution))}
 private assertRepeatAdmission(binding:DurableBinding){if(this.repeatTools?.executions.find(row=>row.execution===binding.scope.execution)?.stopped)throw new DispatchCanceled(new RepeatedToolLimit())}
 private claimRepeatReminder(binding:DurableBinding,claim:DurableDispatch){const row=this.repeatTools?.executions.find(row=>row.execution===binding.scope.execution);if(row?.reminded&&!row.reminderDelivered&&(claim.kind==='model'||claim.kind==='agent')&&claim.request.includes(REPEAT_REMINDER))row.reminderDelivered=true}

 private readonly restoredTaskHead:boolean
 private configuredTaskEnvelope?:TaskEnvelopeBinding
 private nativeTaskEnvelope?:NativeTaskEnvelopeSource
 private nativeTaskCoordinator?:import('../taskSteps').TaskCoordinatorBinding
 private taskSchedule?:TaskSchedule
 private taskScopes=new Map<string,{snapshot:()=>ExecutionCheckpoint;profiles:()=>SubagentRecord[];accept:(state:ExecutionCheckpoint)=>void;charge:(kind:'model'|'agent',role:string,request:string)=>Promise<void>}>()
 private taskRoot?:string
 private liveTaskPending?:string
 private readonly taskResults=new Map<string,TaskCompletionEvidence>()
 private budget?:UsageJournalOwner
 private contextSource?:StoredChatScope
 private nativeClaims=new WeakMap<object,NativeRouteClaim>()
 private nativePlanSuperseded=new Map<string,string>()
 private nativeSuperseded=new Map<string,{id:string;requestDigest:string;route:string}>()
 get hasLiveCapture(){return this.capture!==undefined}
 private captureRootBinding?:DurableBinding
 private capture?:WriterCapture
 private history?:LiveHistoryRecorder
 private acknowledgedCheckpoint?:Uint8Array
 private revision:string|null
 private tail:Promise<unknown>=Promise.resolve()
 private fatal:unknown
 private dispatchClosed=false
 /** #226: a trusted re-authorization refusal that closed dispatch; nothing after it is committed or dispatched. */
 private closure:unknown
 private closed():unknown{return this.closure??new DOMException('The operation was aborted','AbortError')}
 private stopped=false
 private book:DurableBook
 private root?:()=>ExecutionCheckpoint
 private policy?:RecordsPolicy
 private guard?:()=>RecordGuard|undefined
 private latest?:ExecutionCheckpoint
 private readonly models=new WeakMap<object,{binding:DurableBinding;claim:DurableDispatch}>()
 private readonly planDelivery=new Map<AssistantEvent,{scope:string;bytes:string}>()
 private recovered:Set<string>
 decorate:(state:ExecutionCheckpoint)=>ExecutionCheckpoint=state=>state
 constructor(readonly store:RunStore,readonly id:string,head?:RunHead,legacy?:ExecutionCheckpoint){
  if(store.durability==='single-writer-callback')throw new ContinuationNotRecoverable()
  if(head&&(head.runId!==id||!storageId(head.revision)||!storageId(head.commitId)||head.cursor!==null&&!storageId(head.cursor)))throw new StorageAcknowledgementInvalid()
  this.restoredTaskHead=head!==undefined||legacy!==undefined
  this.revision=head?.revision??null
  if((store as Partial<HistoryRunStore>).historyVersion===1)this.history=new LiveHistoryRecorder(store as HistoryRunStore,id,this.revision,legacy!==undefined&&!head,head?.checkpoint)
  if(head?.checkpoint)this.acknowledgedCheckpoint=new Uint8Array(head.checkpoint)
  this.latest=head?.checkpoint?readExecution(JSON.parse(new TextDecoder('utf-8',{fatal:true}).decode(head.checkpoint))):legacy
  if(this.latest&&this.latest.id!==id)throw new ContinuationIdentityChanged()
  if(this.latest?.status==='stopped'){this.dispatchClosed=true;this.stopped=true}
  this.repeatTools=structuredClone(this.latest?.repeatTools)
  if(this.repeatTools){
   if(!validRepeatTools(this.repeatTools))throw new ContinuationIdentityChanged()
   for(const execution of this.repeatTools.executions){
    for(const item of execution.seen){const row=this.latest?.durable?.dispatches.find(row=>row.id===item.dispatchId);if(!row||row.scope.execution!==execution.execution||repeatKey(row)!==item.keyDigest)throw new ContinuationIdentityChanged()}
    for(const row of this.latest?.durable?.dispatches??[])if(row.scope.execution===execution.execution&&['host','runtime'].includes(row.kind)&&row.state==='result'&&!row.failure&&!execution.seen.some(item=>item.dispatchId===row.id))throw new ContinuationIdentityChanged()
   }
  }
  for(const row of this.latest?.durable?.dispatches??[])if(row.repeatResultJson!==undefined&&(!this.repeatTools?.executions.some(state=>state.execution===row.scope.execution&&state.seen.some(item=>item.dispatchId===row.id))||!repeatKey(row)))throw new ContinuationIdentityChanged()
  this.taskSchedule=structuredClone(this.latest?.planState?.taskSchedule)
  this.book=structuredClone(this.latest?.durable??{dispatches:[],observations:[],children:[]})
  this.recovered=new Set(this.book.dispatches.map(row=>row.id))
  this.book.dispatches=this.book.dispatches.map(row=>row.state==='claimed'?{...row,state:'unknown'}:row)
 }
 /** Enrolled once by durableSession before any physical admission. */
 enrollCapture(binding:DurableBinding,manifest:import('../corpus').CorpusManifest,sink:import('../corpus').CorpusSink){
  if(binding.writer!==this||!binding.root||binding.scope.execution!==this.id||binding.scope.task!==undefined||binding.scope.role!=='author'||this.capture||!this.history||this.book.dispatches.some(row=>row.kind==='model'||row.kind==='agent')&&this.revision===null)throw new ContinuationIdentityChanged()
  this.captureRootBinding=binding
  this.capture=new WriterCapture(this,this.store as HistoryRunStore,this.id,copyCorpusManifestEnvelope(manifest) as import('../corpus').CorpusManifest,sink,this.book.dispatches.some(row=>row.kind==='model'||row.kind==='agent'))
 }
 async finishCapture(binding:DurableBinding,signal?:AbortSignal){
  if(binding!==this.captureRootBinding||!binding.root||binding.writer!==this||binding.scope.execution!==this.id||binding.scope.role!=='author'||binding.scope.task!==undefined)return undefined
  return this.enqueue(async()=>this.capture?.finish(this.latest,this.revision,this.acknowledgedCheckpoint,this.latest?.policy,signal))
 }
 /** @internal Bind one explicit shared host capability; retained receipts never recreate a live ticket. */
 async attachUsage(binding:DurableBinding,input?:UsageBudgetInput,signal?:AbortSignal){
  if(binding.writer!==this)throw new UsageBudgetIdentityChanged()
  if(this.budget){if(!input||!this.budget.matches(input))throw new UsageBudgetIdentityChanged();return}
  if(!input){if(this.latest?.usageBudget)throw new UsageBudgetIdentityChanged();return}
  if(!binding.root||this.latest&&!this.latest.usageBudget)throw new UsageBudgetIdentityChanged()
  this.budget=new UsageJournalOwner(input,this.id,this.latest?.usageBudget)
  await this.budget.reconcile(signal)
 }
 /** @internal Source-owned controllers register charging, never a caller-provided available balance. */
 attachTaskScope(binding:DurableBinding,source:{snapshot:()=>ExecutionCheckpoint;profiles:()=>SubagentRecord[];accept:(state:ExecutionCheckpoint)=>void;charge:(kind:'model'|'agent',role:string,request:string)=>Promise<void>},configuration?:TaskStepEnvelope){
  if(binding.writer!==this||binding.root&&!this.rootBinding(binding)||!binding.root&&!this.taskRoot)throw new ContinuationIdentityChanged()
  if(binding.root){
   const saved=this.latest?.planState?.taskEnvelope
   const expected=configuration===undefined?undefined:{run:this.id,execution:binding.scope.execution,scopeDigest:digest(scopeKey(binding.scope)),configuration:readTaskStepEnvelope(configuration)}
   const retained=saved??this.configuredTaskEnvelope
   if(expected&&source.snapshot().unit!=='provider-managed'||(retained!==undefined||this.restoredTaskHead)&&canonicalJSON(retained??null)!==canonicalJSON(expected??null))throw new ContinuationIdentityChanged()
   this.configuredTaskEnvelope=expected;this.taskRoot=binding.scope.execution
   // Bind original writer-owned pins before a first pending candidate is created.
   if(expected||this.nativeTaskCoordinator){const pinned=source.snapshot();if(!pinned.planState)throw new ContinuationIdentityChanged();mutateCheckpoint(pinned,{planState:{...pinned.planState,...(expected?{taskEnvelope:structuredClone(expected)}:{}),...(this.nativeTaskCoordinator?{taskCoordinator:structuredClone(this.nativeTaskCoordinator)}:{})}});source.accept(pinned)}
   if(this.taskSchedule){
    if(this.taskSchedule.binding.run!==this.id||this.taskSchedule.binding.execution!==this.id||this.taskSchedule.binding.scopeDigest!==digest(scopeKey(binding.scope)))throw new ContinuationIdentityChanged()
    for(const row of this.taskSchedule.rows)for(const claim of row.claims){const physical=this.book.dispatches.find(dispatch=>dispatch.id===claim.id);if(!physical||physical.requestDigest!==claim.requestDigest||physical.scope.execution!==(row.child?.execution??this.id)||physical.scope.task!==(row.child?.task)||!['model','agent'].includes(physical.kind))throw new ContinuationIdentityChanged()}
   }
  }
  if(this.taskScopes.size>=33&&!this.taskScopes.has(binding.scope.execution))throw new ContinuationNotRecoverable()
  this.taskScopes.set(binding.scope.execution,source)
 }
 /** @internal Create the sole registered plan action; a copied public declaration is not authority. */
 taskDelegationTool(binding:DurableBinding,execute:(request:DelegationRequest,signal:AbortSignal)=>Promise<McpToolResult>):HostTool{
  if(!this.rootBinding(binding))throw new ContinuationIdentityChanged()
  const tool:HostTool={name:'delegate_plan_steps',taxonomy:{operation:'run',effect:'read'},description:'Delegate one to three distinct ready package steps from the exact current plan revision. Existing child and response limits remain in force. Completion requires a separate complete_plan_step call with acknowledged child evidence.',interaction:'delegation',inputSchema:{type:'object',properties:{planRevision:{type:'string',pattern:'^sha256:[a-f0-9]{64}$'},steps:{type:'array',minItems:1,maxItems:3,uniqueItems:true,items:{type:'string',pattern:'^step-[1-9][0-9]{0,15}$'}}},required:['planRevision','steps'],additionalProperties:false},execute:async(args,signal)=>execute(await normalizeTaskDelegation(tool,binding,args,signal)!,signal)}
  taskDelegationTools.set(tool,{binding:{...binding,scope:structuredClone(binding.scope)},schema:canonicalJSON(tool.inputSchema)})
  return tool
 }
 private taskDelegationRequest(root:ExecutionCheckpoint,steps:string[]):DelegationRequest{
  const ready=readyTaskSteps(taskDefinitions(root.plan),taskStatuses(root.plan))
  const tasks=steps.map(id=>{
   const step=root.plan.find(step=>step.id===id),row=this.taskSchedule?.rows.find(row=>row.id===id)
   if(!step?.delegation||!row||row.child||!ready.includes(id))throw new TaskStepRefused('child-not-admitted')
   const objective=step.step+(step.detail?'\n\n'+step.detail:'')
   if(objective.length>2000)throw new TaskStepRefused('delegation-objective-too-large')
   return {title:'Plan '+id,objective,profile:step.delegation.id}
  })
  const request=readDelegationRequest({tasks})
  request.tasks.forEach((task,index)=>task.subagent=structuredClone(root.plan.find(step=>step.id===steps[index])!.delegation!))
  requireTaskProfiles(taskDefinitions(root.plan),this.taskScopes.get(this.taskRoot!)!.profiles())
  return request
 }
 /** Persist the whole exact association before handoff release or any child's physical claim. */
 prepareTaskDelegation(binding:DurableBinding,args:Record<string,unknown>,signal?:AbortSignal):Promise<DelegationRequest>{return this.enqueue(async()=>{
  try{
   if(!this.rootBinding(binding)||!this.hasTaskSteps||!this.taskSchedule)throw new TaskStepRefused('task-plan-required')
   if(Object.keys(args).some(key=>!['planRevision','steps'].includes(key))||args.planRevision!==this.taskSchedule.binding.planRevision||!Array.isArray(args.steps)||args.steps.length<1||args.steps.length>3||!args.steps.every(step=>typeof step==='string')||new Set(args.steps).size!==args.steps.length)throw new TaskStepRefused('invalid-delegation-selection')
   if(this.taskSchedule.pending)throw new TaskStepRefused('delegation-outcome-unknown')
   const root=this.taskState(),request=this.taskDelegationRequest(root,args.steps as string[]),next=structuredClone(this.taskSchedule)
   next.pending={planRevision:next.binding.planRevision,requestDigest:digest(canonicalJSON(request)),steps:[...args.steps] as string[]}
   this.taskSchedule=next
   await this.publish(binding,root,{type:'execution_checkpoint',checkpoint:root},undefined,signal)
   this.liveTaskPending=next.pending.requestDigest
   this.taskScopes.get(this.taskRoot!)!.accept(this.latest!)
   return request
  }catch(error){if(error instanceof TaskStepRefused)throw new DispatchCanceled(error);throw error}
 })}
 /** Bind the actual legacy task IDs in the original CAS before publication or start. */
 bindTaskDelegation(binding:DurableBinding,request:DelegationRequest,tasks:readonly DelegatedTask[],signal?:AbortSignal):Promise<void>{return this.enqueue(async()=>{
  if(!this.hasTaskSteps)return
  const selected=this.taskSchedule?.pending,requestDigest=digest(canonicalJSON(request))
  if(!selected){
   const handoff=this.latest?.handoff
   if(handoff?.state==='children'&&canonicalJSON(handoff.request)===canonicalJSON(request)&&canonicalJSON(handoff.tasks.map(task=>task.id))!==canonicalJSON(tasks.map(task=>task.id)))throw new ContinuationIdentityChanged()
   if(tasks.some(task=>this.taskSchedule?.rows.some(row=>row.child?.task===task.id)))for(const [index,task]of tasks.entries()){
    const row=this.taskSchedule!.rows.find(row=>row.child?.task===task.id),expected=request.tasks[index]
    if(!row||!expected||canonicalJSON(row.child!.profile)!==canonicalJSON(task.subagent)||task.profile!==expected.profile||task.objective!==expected.objective||task.title!==expected.title)throw new ContinuationIdentityChanged()
    if(task.state==='queued'&&this.book.dispatches.some(claim=>claim.scope.execution===task.id&&claim.scope.task===task.id&&(claim.kind==='model'||claim.kind==='agent')))throw new HostOutcomeUnknown()
   }
   return
  }
  try{
   const handoff=this.latest?.handoff?.request??this.book.pendingHandoff?.request
   if(!this.rootBinding(binding)||selected.planRevision!==this.taskSchedule!.binding.planRevision||selected.requestDigest!==requestDigest||this.liveTaskPending!==requestDigest&&(!handoff||digest(canonicalJSON(handoff))!==requestDigest)||tasks.length!==selected.steps.length)throw new TaskStepRefused('delegation-identity-changed')
   const root=this.taskState(),derived=this.taskDelegationRequest(root,selected.steps)
   if(canonicalJSON(derived)!==canonicalJSON(request))throw new TaskStepRefused('delegation-identity-changed')
   for(const [index,task] of tasks.entries()){const expected=request.tasks[index]!;if(task.state!=='queued'||task.title!==expected.title||task.objective!==expected.objective||task.profile!==expected.profile||canonicalJSON(task.subagent)!==canonicalJSON(expected.subagent)||task.resumes||task.reused)throw new TaskStepRefused('delegation-identity-changed')}
   const current=structuredClone(this.taskSchedule!);delete current.pending
   const next=bindTaskChildren(current,taskDefinitions(root.plan),taskStatuses(root.plan),tasks.map((task,index)=>({step:selected.steps[index]!,task:task.id,profile:task.subagent!})))
   delete next.pending;this.taskSchedule=next
   await this.publish(binding,root,{type:'execution_checkpoint',checkpoint:root},undefined,signal)
   this.liveTaskPending=undefined;this.taskScopes.get(this.taskRoot!)!.accept(this.latest!)
  }catch(error){if(error instanceof TaskStepRefused)throw new DispatchCanceled(error);throw error}
 })}
 get hasTaskSteps(){return this.taskRoot!==undefined}
 private taskState(){const root=this.taskRoot?this.taskScopes.get(this.taskRoot)?.snapshot():undefined;if(!root)throw new ContinuationIdentityChanged();return this.decorate(root)}
 private successfulTaskChild(state:ExecutionCheckpoint,child:import('../taskSteps').TaskChildBinding,task:DelegatedTask):boolean{
  if(task.id!==child.task||task.state!=='complete'||task.failure||task.problem||task.partial||task.reused||!task.report||canonicalJSON(task.subagent)!==canonicalJSON(child.profile))return false
  const checkpoints=state.durable?.children.filter(row=>row.scope.execution===child.execution&&row.scope.task===child.task)??[]
  const physical=state.durable?.dispatches.filter(row=>row.scope.execution===child.execution&&row.scope.task===child.task&&(row.kind==='model'||row.kind==='agent'))??[]
  return checkpoints.length===1&&checkpoints[0]!.checkpoint.status==='complete'&&physical.some(row=>row.state==='result'&&!row.failure)&&physical.every(row=>row.state==='result'&&(!row.failure||row.failure.outcome==='refused'))
 }
 /** Only the coordinator's terminal task publisher invokes this path, after physical results. */
 commitTaskResult(binding:DurableBinding,event:Extract<AssistantEvent,{type:'delegated_task'}>,signal?:AbortSignal):Promise<AssistantEvent>{return this.enqueue(async()=>{
  const row=this.taskSchedule?.rows.find(row=>row.child?.task===event.task.id)
  if(!row||event.task.state!=='complete'||event.task.failure||event.task.partial||event.task.reused||event.task.problem)return (await this.publish(binding,undefined,event,undefined,signal))!
  if(!this.successfulTaskChild({...this.taskState(),durable:this.book},row.child!,event.task))return (await this.publish(binding,undefined,event,undefined,signal))!
  return (await this.publish(binding,undefined,event,undefined,signal,[],row.id))!
 })}
 taskCompletionProof(binding:DurableBinding):TaskCompletionEvidence[]{if(!this.rootBinding(binding))return [];return structuredClone([...this.taskResults.values()])}
 /** A retained tuple never grants proof. Restore it only from the exact original history CAS. */
 restoreTaskEvidence(binding:DurableBinding,signal?:AbortSignal){return this.enqueue(async()=>{
  if(!this.rootBinding(binding)||!this.hasTaskSteps)throw new ContinuationIdentityChanged()
  const saved=this.taskSchedule?.rows.filter(row=>row.child?.result)??[]
  if(!saved.length)return
  const store=this.store as Partial<HistoryRunStore>;if(store.historyVersion!==1)return
  const invalid=():never=>{throw new ContinuationIdentityChanged()},port=store as HistoryRunStore,head=readHistoryHead(await port.readHistoryHead(this.id,signal))
  if(head.runId!==this.id||head.executionRevision!==this.revision||!this.acknowledgedCheckpoint||canonicalJSON(head.checkpoint)!==canonicalJSON({digest:historyBytesDigest(this.acknowledgedCheckpoint),bytes:this.acknowledgedCheckpoint.length}))return invalid()
  let bytes=0,sequence=0,eventCount=0,previous:string|null=null
  const verified=new Map<string,TaskCompletionEvidence>()
  const object=async(ref:HistoryObjectRef)=>{if(!validHistoryRef(ref)||(bytes+=ref.bytes)>HISTORY_TOTAL_BYTES)return invalid();const page=await port.readHistoryObject({kind:'run',id:this.id},head.revision,ref.digest,0,ref.bytes,signal);if(page.digest!==ref.digest||page.offset!==0||page.total!==ref.bytes||page.value.length!==ref.bytes||page.nextOffset!==ref.bytes||page.hasMore||historyBytesDigest(page.value)!==ref.digest)return invalid();return page.value}
  while(sequence<head.commitCount){
   if(sequence>=HISTORY_MAX_ROWS)return invalid()
   const page=await port.readHistoryPage(this.id,head.revision,sequence,100,signal)
   if(Object.keys(page).sort().join(',')!=='afterSequence,hasMore,items,nextSequence,revision,total'||page.revision!==head.revision||page.afterSequence!==sequence||!Array.isArray(page.items)||!page.items.length||page.items.length>100||page.total!==head.commitCount||page.nextSequence!==sequence+page.items.length||page.hasMore!==(page.nextSequence<head.commitCount))return invalid()
   for(const input of page.items){
    const transaction=readHistoryEntry(input);if(transaction.sequence!==++sequence||transaction.previousDigest!==previous||transaction.eventStart!==eventCount+1||(eventCount+=transaction.eventCount)>HISTORY_MAX_EVENTS)return invalid();previous=transaction.digest
    for(const row of saved){const child=row.child!,result=child.result!;if(transaction.commitId!==result.commitId)continue
     if(verified.has(row.id)||!transaction.checkpoint)return invalid()
     const state=readExecution(JSON.parse(historyText(await object(transaction.checkpoint))))
     if(!state||state.id!==this.id||canonicalJSON(state.planState?.taskSchedule?.rows.find(item=>item.id===row.id)?.child)!==canonicalJSON(child))return invalid()
     let matched=false
     for(const ref of transaction.events){const raw=await object(ref);if(historyBytesDigest(raw)!==result.eventDigest)continue;const envelope=readStoredRunEvent(raw),event=envelope.event
      if(matched||scopeKey(envelope.scope)!==scopeKey(binding.scope)||event.type!=='delegated_task'||digest(canonicalJSON(event.task))!==result.resultDigest||!this.successfulTaskChild(state,child,event.task)||state.durable!.children.find(item=>item.scope.execution===child.execution&&item.scope.task===child.task)?.checkpoint.digest!==result.checkpointDigest)return invalid()
      matched=true
     }
     if(!matched)return invalid()
     verified.set(row.id,{step:row.id,task:child.task,planRevision:child.planRevision,...result})
    }
   }
  }
  const again=readHistoryHead(await port.readHistoryHead(this.id,signal)),identity=(value:typeof head)=>canonicalJSON([value.runId,value.executionRevision,value.commitCount,value.eventCount,value.root,value.checkpoint])
  if(verified.size!==saved.length||previous!==head.root||eventCount!==head.eventCount||identity(again)!==identity(head))return invalid()
  for(const [step,result]of verified)this.taskResults.set(step,result)
 })}
 /** @internal The existing native coordinator supplies its own policy and per-execution counters. */
 attachNativeTaskEnvelope(binding:DurableBinding,source:NativeTaskEnvelopeSource){
  if(!this.rootBinding(binding)||!Number.isInteger(source.limit)||source.limit<1||source.limit>100||!Number.isInteger(source.childReserve)||source.childReserve<1||source.childReserve>source.limit)throw new ContinuationIdentityChanged()
  const expected:import('../taskSteps').TaskCoordinatorBinding={version:1,owner:'native-delegation',run:this.id,execution:binding.scope.execution,scopeDigest:digest(scopeKey(binding.scope)),limit:source.limit,childReserve:source.childReserve},retained=this.latest?.planState?.taskCoordinator
  if((retained!==undefined||this.restoredTaskHead)&&canonicalJSON(retained??null)!==canonicalJSON(expected)||this.nativeTaskCoordinator&&canonicalJSON(this.nativeTaskCoordinator)!==canonicalJSON(expected))throw new ContinuationIdentityChanged()
  this.nativeTaskCoordinator=expected;this.nativeTaskEnvelope=source
 }
 private taskRemaining(root:ExecutionCheckpoint,scope?:DurableScope):number|undefined{
  if(root.unit==='model-requests')return root.limit-root.used-(scope?.task?1:0)
  const physical=new Map<string,number>()
  for(const row of this.book.dispatches)if(row.kind==='agent')physical.set(row.scope.execution,(physical.get(row.scope.execution)??0)+1)
  let remaining:number|undefined
  if(this.nativeTaskEnvelope){
   const rows=this.nativeTaskEnvelope.counts(),counts=new Map<string,number>()
   for(const row of rows){if(counts.has(row.execution)||!Number.isInteger(row.count)||row.count<0)throw new ContinuationIdentityChanged();counts.set(row.execution,row.count)}
   // Exact original scopes corroborate counters; equal grand totals alone are insufficient.
   for(const execution of new Set([...counts.keys(),...physical.keys()]))if((counts.get(execution)??0)!==(physical.get(execution)??0))throw new ContinuationIdentityChanged()
   const reserve=scope?this.nativeTaskEnvelope.reserve(scope):0
   if(!Number.isInteger(reserve)||reserve<0||reserve>this.nativeTaskEnvelope.limit||!this.nativeTaskCoordinator||(!scope?.task&&reserve!==0)||(!!scope?.task&&reserve!==this.nativeTaskCoordinator.childReserve))throw new ContinuationIdentityChanged()
   remaining=this.nativeTaskEnvelope.limit-[...counts.values()].reduce((n,count)=>n+count,0)-reserve
  }
  if(this.configuredTaskEnvelope){const configured=this.configuredTaskEnvelope.configuration.limit-[...physical.values()].reduce((n,count)=>n+count,0);remaining=remaining===undefined?configured:Math.min(remaining,configured)}
  return remaining===undefined?undefined:Math.max(0,remaining)
 }
 private prepareTaskPlan(binding:DurableBinding,state:ExecutionCheckpoint){
  if(!this.hasTaskSteps)return state
  if(!binding.root)return state
  if(binding.scope.execution!==this.taskRoot)throw new ContinuationIdentityChanged()
  const next=structuredClone(state),previous=this.latest,definitions=taskDefinitions(next.plan)
  if(!next.planState)throw new ContinuationIdentityChanged()
  if(!next.plan.length)return next
  validateTaskPlan(next.plan);requireTaskProfiles(definitions,this.taskScopes.get(this.taskRoot!)!.profiles())
  const available=this.taskRemaining(this.taskState())
  if(available===undefined&&definitions.some(step=>step.budget))throw new TaskStepRefused('native-envelope-required')
  const bindingFacts={run:this.id,execution:next.id,planRevision:next.planState.revision,scopeDigest:digest(scopeKey(binding.scope)),unit:next.unit}
  mutateCheckpoint(next,{planState:{...next.planState,...(this.configuredTaskEnvelope?{taskEnvelope:structuredClone(this.configuredTaskEnvelope)}:{}),taskSchedule:reconcileTaskSchedule(this.taskSchedule,taskDefinitions(previous?.plan??[]),taskStatuses(previous?.plan??[]),bindingFacts,definitions,taskStatuses(next.plan),available??0)}})
  return next
 }
 private async claimTaskDispatch(binding:DurableBinding,claim:DurableDispatch){
  if(!this.hasTaskSteps||claim.kind!=='model'&&claim.kind!=='agent'||claim.kind==='model'&&JSON.parse(claim.request).method==='GET')return
  const root=this.taskState(),source=this.taskScopes.get(binding.scope.execution),available=this.taskRemaining(root,binding.scope)
  if(!source)throw new ContinuationIdentityChanged()
  if(available!==undefined&&available<=0)throw new DispatchCanceled(new ExecutionPaused())
  let next=this.taskSchedule
  if(next){
   const definitions=taskDefinitions(root.plan),statuses=taskStatuses(root.plan)
   const row=binding.scope.task?next.rows.find(row=>row.child?.task===binding.scope.task):next.rows.find(row=>statuses[row.id]==='in_progress')
   try{
    if(row)next=reserveTaskStep(next,definitions,statuses,row.id,{id:claim.id,requestDigest:claim.requestDigest},available)
    else {const held=next.rows.reduce((sum,row)=>sum+(['completed','not-needed'].includes(statuses[row.id]??'')?0:Math.max(0,(row.limit??0)-row.claims.length)),0);if(available!==undefined&&available<=held)throw new TaskStepRefused('step-budget-pause')}
   }catch(error){if(error instanceof TaskStepRefused&&error.reason==='step-budget-pause')throw new DispatchCanceled(new ExecutionPaused());throw new DispatchCanceled(error)}
  }
  await source.charge(claim.kind,binding.scope.role,claim.request)
  if(!binding.root){this.book.children=this.book.children.filter(row=>scopeKey(row.scope)!==scopeKey(binding.scope));this.book.children.push({scope:structuredClone(binding.scope),checkpoint:source.snapshot()})}
  this.taskSchedule=next
 }
 get hasUsageBudget(){return this.budget!==undefined}
 /** Allocate, reserve and acknowledge this exact future dispatch through the original writer queue. */
 prepareUsage(binding:DurableBinding,facts:UsageDispatchFacts,signal?:AbortSignal):Promise<object|undefined>{return this.enqueue(async()=>{
  if(binding.writer!==this||binding.scope.execution!==facts.execution||binding.scope.role!==facts.role||binding.scope.task!==facts.task)throw new UsageBudgetIdentityChanged()
  if(!this.budget)return undefined
  if(this.dispatchClosed||signal?.aborted)throw new DispatchCanceled(signal?.reason??new DOMException('The operation was aborted','AbortError'))
  let ticket:object
  try{ticket=await this.budget.reserve(crypto.randomUUID(),facts,signal)}
  catch(error){this.budget.refusal(facts,error);await this.publish(binding,undefined,undefined,undefined,signal);throw error}
  await this.publish(binding,undefined,undefined,undefined,signal);return ticket
 })}
 private async observeUsage(binding:DurableBinding,id:string,observed:UsageAmounts,evidence:string,signal?:AbortSignal){
  if(!this.budget)return
  await this.enqueue(async()=>{let uncertain:unknown;try{await this.budget!.observe(id,observed,evidence,signal)}catch(error){uncertain=error}await this.publish(binding,undefined,undefined,undefined,signal);if(uncertain)throw uncertain})
 }
 /** @internal Root admission binds one host-owned exact chat scope. */
 attachContext(source:StoredChatScope){if(source.store!==this.store||this.contextSource)throw new ContinuationIdentityChanged();this.contextSource=source}
 get checkpoint(){return this.latest?structuredClone(this.latest):undefined}
 get failed(){return this.fatal!==undefined||this.dispatchClosed}
 bind(snapshot:()=>ExecutionCheckpoint,policy:RecordsPolicy,guard:()=>RecordGuard|undefined){this.root=snapshot;this.policy=policy;this.guard=guard}
 private async commitWithContext(input:import('./run').RunCommit,signal?:AbortSignal,terminal=false,checkpointScope?:string,state=this.latest,scope:DurableScope={execution:this.id,role:'author'}){
  input={...input,checkpoint:input.checkpoint===null?null:new Uint8Array(input.checkpoint),events:input.events.map(bytes=>new Uint8Array(bytes))}
  const detached={...input,checkpoint:input.checkpoint===null?null:new Uint8Array(input.checkpoint),events:input.events.map(bytes=>new Uint8Array(bytes))}
  const context=this.contextSource?await this.contextSource.prepare(signal,terminal,checkpointScope):undefined
  if(this.capture&&this.history&&state)await this.capture.prepare(this.history,state,signal)
  const prepared=this.history&&state?await this.history.prepare(state,scope,signal,context!==undefined):undefined
  const receipt=context||prepared?await (this.store as HistoryRunStore).commitHistory({run:detached,observations:prepared?.observations??[],...(prepared?.genesis?{genesis:true}:{}),...(context?{context:structuredClone(context)}:{})},signal):await this.store.commit(detached,signal)
  if(!receipt||receipt.commitId!==input.commitId||!storageId(receipt.revision)||receipt.revision===this.revision||receipt.cursor!==null&&!storageId(receipt.cursor)||input.events.length>0&&receipt.cursor===null)throw new StorageAcknowledgementInvalid()
  const history=(receipt as RunHistoryReceipt).history,c=history?.context
  if(context&&(!c||history.runId!==this.id||history.executionRevision!==receipt.revision||c.chatId!==context.chatId||c.turn!==context.turn||c.closed!==context.closed||c.privateScope!==context.privateScope||c.manifest.digest!==context.manifest.digest||c.manifest.bytes!==context.manifest.bytes||!storageId(c.revision)||c.revision===context.expectedRevision))throw new StorageAcknowledgementInvalid()
  if(prepared){try{await prepared.acknowledge(history,input,receipt.revision,signal)}catch{throw new StorageAcknowledgementInvalid()}}
  if(this.capture&&prepared)this.capture.admitted(receipt as RunHistoryReceipt,input.commitId,prepared)
  if(context)this.contextSource!.acknowledge(c!)
  if(input.checkpoint!==null)this.acknowledgedCheckpoint=new Uint8Array(input.checkpoint)
  return receipt
 }
 /** Private source-owned publication: use the original queue and return references only after acknowledgement. */
 private publishHistory(binding:DurableBinding,prepare:(scope:DurableScope)=>Promise<PreparedHistoryObservation>,signal?:AbortSignal):Promise<HistoryObjectRef>{binding={...binding,scope:structuredClone(binding.scope)};return this.enqueue(async()=>{
  if(binding.writer!==this||binding.scope.execution!==this.id&&!this.book.children.some(child=>scopeKey(child.scope)===scopeKey(binding.scope))||binding.scope.execution===this.id&&binding.scope.task!==undefined)throw new ContinuationIdentityChanged()
  if(this.dispatchClosed||this.stopped||signal?.aborted)throw new DispatchCanceled(signal?.reason??new DOMException('The operation was aborted','AbortError'))
  if(!this.latest||!this.acknowledgedCheckpoint||!this.history||!await this.history.recording(signal))throw new ContinuationNotRecoverable()
  const prepared=await prepare(binding.scope)
  if(scopeKey(prepared.observation.scope)!==scopeKey(binding.scope))throw new ContinuationIdentityChanged()
  const ref=await this.history.add(prepared,signal),receipt=await this.commitWithContext({runId:this.id,expectedRevision:this.revision,commitId:crypto.randomUUID(),checkpoint:new Uint8Array(this.acknowledgedCheckpoint),events:[]},signal,false,this.latest.context.scope,this.latest,binding.scope)
  this.revision=receipt.revision
  return ref
 })}
 /** @internal Optional recorder capability, inspected through the original writer queue. */
 historyRecording(signal?:AbortSignal){return this.enqueue(async()=>this.history?this.history.recording(signal):false)}
 /** @internal Retain public original inputs before a later compaction discards them. */
 historyOriginal(binding:DurableBinding,boundary:string,sourceJSON:string,format:'messages'|'context',ordinal:number,signal?:AbortSignal){return this.publishHistory(binding,scope=>prepareHistoryOriginal(scope,boundary,sourceJSON,format,ordinal),signal)}
 /** @internal Publish a replacement with already acknowledged exact-scope originals. */
 historyCompaction(binding:DurableBinding,boundary:string,originals:HistoryObjectRef[],replacementJSON:string,mode:'index'|'summary',prior:HistoryObjectRef[],legacyDigest:string,signal?:AbortSignal){const held=structuredClone({originals,prior});return this.publishHistory(binding,scope=>prepareHistoryCompaction(scope,boundary,held.originals,replacementJSON,mode,held.prior,legacyDigest),signal)}
 /** @internal Explicit host resume claims both revisions before exposing an attached writer. */
 async claimContext(checkpoint:Uint8Array,signal?:AbortSignal){const receipt=await this.commitWithContext({runId:this.id,expectedRevision:this.revision,commitId:crypto.randomUUID(),checkpoint,events:[]},signal,false,this.latest?.context.scope);this.revision=receipt.revision}
 private rootBinding(binding:DurableBinding){return binding.writer===this&&binding.root&&binding.scope.execution===this.id&&binding.scope.role==='author'&&binding.scope.task===undefined}
 /** Transfer returned parent snapshot ownership to its coordinator until the next root bind. */
 useAcknowledgedRoot(binding:DurableBinding){if(!this.rootBinding(binding)||!this.latest)throw new ContinuationIdentityChanged();this.root=undefined}
 /** Close admission synchronously before canceling provider work. */
 closeDispatch(binding:DurableBinding){if(!this.rootBinding(binding))throw new ContinuationIdentityChanged();this.dispatchClosed=true}
 /** Original writer/CAS, host-owned signal, at most five seconds. No physical work is retried. */
 async finalizeStop(binding:DurableBinding,signal:AbortSignal):Promise<AssistantEvent[]>{
  this.closeDispatch(binding)
  const bounded=AbortSignal.any([signal,AbortSignal.timeout(5000)])
  let attempted=false,abort:()=>void=()=>undefined
  const expired=new Promise<never>((_,reject)=>{
   abort=()=>{const error=attempted?new StorageOutcomeUnknown(bounded.reason):bounded.reason;if(attempted)this.fatal=error;reject(error)}
   bounded.addEventListener('abort',abort,{once:true});if(bounded.aborted)abort()
  })
  const saved=this.enqueue(async()=>{
   if(bounded.aborted)throw new DispatchCanceled(bounded.reason)
   if(this.stopped||!this.latest||!['running','paused'].includes(this.latest.status))throw new ContinuationIdentityChanged()
   const state=structuredClone(this.latest);mutateCheckpoint(state,{status:'stopped'})
   for(const row of this.book.dispatches)if(row.state==='claimed')row.state='unknown'
   const end=writeRunEvent({type:'end'})
   let checkpoint:AssistantEvent
   attempted=true
   try{checkpoint=(await this.publish(binding,state,{type:'execution_checkpoint',checkpoint:state},undefined,bounded,[end]))!}catch(cause){if(cause instanceof StorageConflict)throw cause;throw this.fatal??new StorageOutcomeUnknown(cause)}
   if(this.fatal!==undefined)throw this.fatal
   this.stopped=true;this.root=undefined
   return [checkpoint,end]
  })
  try{return await Promise.race([saved,expired])}finally{bounded.removeEventListener('abort',abort)}
 }

 private enqueue<T>(run:()=>Promise<T>):Promise<T>{const next=this.tail.then(async()=>{if(this.fatal!==undefined)throw this.fatal;try{return await run()}catch(error){if(!(error instanceof DispatchCanceled))this.fatal=error;throw error instanceof DispatchCanceled?error.reason:error}});this.tail=next.catch(()=>undefined);return next}
 private finalize(state:ExecutionCheckpoint):ExecutionCheckpoint {
  const owned=structuredClone(state)
  const policy=this.policy
  if(!policy)throw new ContinuationNotRecoverable()
  if(this.repeatTools){const current=this.repeatTools.executions.find(row=>row.execution===owned.id);mutateCheckpoint(owned,{repeatTools:this.repeatTools,version:3,...(current?.stopped?{status:'stopped' as const,reason:'RepeatedToolLimit'}:{})})}
  this.book.children=this.book.children.map(child=>{
   const current=this.repeatTools?.executions.find(row=>row.execution===child.scope.execution)
   if(!current?.stopped||child.checkpoint.status==='stopped'&&child.checkpoint.reason==='RepeatedToolLimit')return child
   // Validate the original signed child before changing host-owned stop state.
   // Re-sign under the same retention before parent validation; never bless a
   // corrupt child merely because the writer needs to record its repeat stop.
   checkRecord('checkpoint',child.checkpoint)
   if(child.checkpoint.id!==child.scope.execution||!validRecord(child.checkpoint))throw new ContinuationNotRecoverable()
   const stopped=structuredClone(child.checkpoint),retained=retainedCheckpoint(stopped,true,policy)
   mutateCheckpoint(stopped,{status:'stopped',reason:'RepeatedToolLimit'})
   const signed=writeRecord(CHECKPOINT_SCHEMA,{...stopped},policy,undefined,new Date(),child.checkpoint.retainUntil)
   if(retainedCheckpoint(signed,false)!==retained)throw new ContinuationNotRecoverable()
   return {...child,checkpoint:signed}
  })
  if(this.nativeTaskCoordinator){if(!owned.planState)throw new ContinuationIdentityChanged();mutateCheckpoint(owned,{planState:{...owned.planState,taskCoordinator:structuredClone(this.nativeTaskCoordinator)},version:3})}
  if(this.configuredTaskEnvelope){if(!owned.planState)throw new ContinuationIdentityChanged();mutateCheckpoint(owned,{planState:{...owned.planState,taskEnvelope:structuredClone(this.configuredTaskEnvelope)},version:3})}
  if(this.taskSchedule){if(owned.planState?.revision!==this.taskSchedule.binding.planRevision)throw new ContinuationIdentityChanged();mutateCheckpoint(owned,{planState:{...owned.planState,taskSchedule:structuredClone(this.taskSchedule)},version:3})}
  if(this.book.dispatches.some(row=>row.planPause))mutateCheckpoint(owned,{version:3})
  const projected=this.decorate(owned);if(this.budget)mutateCheckpoint(projected,{version:3,usageBudget:this.budget.snapshot()});if(projected.handoff)delete this.book.pendingHandoff;if(this.book.pendingHandoff&&!this.dispatchClosed&&!this.repeatTools?.executions.find(row=>row.execution===projected.id)?.stopped)mutateCheckpoint(projected,{status:'running'});mutateCheckpoint(projected,{durable:structuredClone(this.book)})
  const retained=retainedCheckpoint(projected,true,policy)
  const before=canonicalJSON(projected.durable)
  const saved=writeRecord(CHECKPOINT_SCHEMA,{...projected},policy,this.guard?.(),new Date(),state.retainUntil)
  if(retainedCheckpoint(saved,false)!==retained)throw new ContinuationNotRecoverable()
  if(state.planState&&(canonicalJSON(saved.plan)!==canonicalJSON(state.plan)||canonicalJSON(saved.planState)!==canonicalJSON(projected.planState)))throw new ContinuationNotRecoverable()
  if(this.budget&&canonicalJSON(saved.usageBudget)!==canonicalJSON(projected.usageBudget))throw new ContinuationNotRecoverable()
  if(this.repeatTools&&canonicalJSON(saved.repeatTools)!==canonicalJSON(projected.repeatTools))throw new ContinuationNotRecoverable()
  if(projected.turnRoute&&canonicalJSON(saved.turnRoute)!==canonicalJSON(projected.turnRoute))throw new ContinuationNotRecoverable()
  if(canonicalJSON(saved.durable)!==before||encoder.encode(canonicalJSON(saved)).length>STORAGE_RECORD_BYTES)throw new ContinuationNotRecoverable()
  checkRecord('checkpoint',saved)
  if(saved.id!==this.id||saved.digest!==recordDigest(saved))throw new ContinuationIdentityChanged()
  return saved
 }
 private async publish(binding:DurableBinding,state?:ExecutionCheckpoint,event?:AssistantEvent,observe?:{dispatch:DurableDispatch;artifacts:ArtifactObservation[]},signal?:AbortSignal,extra:AssistantEvent[]=[],taskResult?:string){
  if(this.stopped||signal?.aborted){this.dispatchClosed=true;throw new DispatchCanceled(signal?.reason??new DOMException('The operation was aborted','AbortError'))}
  const commitId=crypto.randomUUID()
  if(state&&!binding.root){
   const children=this.book.children.filter(row=>scopeKey(row.scope)!==scopeKey(binding.scope));children.push({scope:structuredClone(binding.scope),checkpoint:structuredClone(state)});this.book.children=children
  }
  if(this.book.children.length>32||this.book.observations.length+(observe?1+observe.artifacts.length:0)>400)throw new ContinuationNotRecoverable()
  if(observe){
   const {dispatch,artifacts}=observe
   const rows:DurableObservation[]=[{id:crypto.randomUUID(),scope:structuredClone(dispatch.scope),kind:'operation',source:dispatch.source,dispatchId:dispatch.id,commitId}]
   for(const artifact of artifacts)rows.push({id:crypto.randomUUID(),scope:structuredClone(dispatch.scope),kind:artifact.access==='read'||artifact.access===undefined&&dispatch.read?'artifact-read':'artifact-produced',source:dispatch.source,dispatchId:dispatch.id,commitId,artifact:structuredClone(artifact)})
   this.book.observations.push(...rows)
  }
  const root=binding.root&&state?state:this.hasTaskSteps?this.taskState():!binding.root&&this.latest?this.latest:this.root?.()??this.latest
  if(!root)throw new ContinuationNotRecoverable()
  const checkpoint=this.finalize(root)
  let candidate=event
  if(event?.type==='plan_drift_decided'&&event.nativeInvocation)candidate={...event,version:2}
  else if(this.taskSchedule&&event&&(event.type==='plan_revised'||event.type==='plan_departed'||event.type==='plan_completion_refused'||event.type==='plan_drift_decided'||event.type==='approval_requested'))candidate={...event,version:2}
  else if((this.budget||checkpoint.turnRoute)&&event&&(event.type==='execution_checkpoint'||event.type==='budget_paused'))candidate={...event,version:2}
  let output=candidate?writeRunEvent(candidate):undefined
  if(output&&(output.type==='execution_checkpoint'||output.type==='budget_paused'))output={...output,checkpoint:binding.root?checkpoint:state??output.checkpoint,...(output.type==='execution_checkpoint'?{durable:true}:{})}
  if(output)checkRecord('event',output)
  let verified:TaskCompletionEvidence|undefined
  if(taskResult){
   if(!output||output.type!=='delegated_task'||!this.rootBinding(binding))throw new ContinuationIdentityChanged()
   const row=checkpoint.planState?.taskSchedule?.rows.find(row=>row.id===taskResult),task=output.task
   if(!row?.child||!this.successfulTaskChild(checkpoint,row.child,task))throw new ContinuationIdentityChanged()
   const child=checkpoint.durable!.children.find(child=>child.scope.execution===task.id&&child.scope.task===task.id)!
   if(typeof child.checkpoint.digest!=='string')throw new ContinuationIdentityChanged()
   const result={checkpointDigest:child.checkpoint.digest,resultDigest:digest(canonicalJSON(task)),eventDigest:digest(canonicalJSON({version:1,scope:binding.scope,event:output})),commitId}
   const taskSchedule={...checkpoint.planState!.taskSchedule!,rows:checkpoint.planState!.taskSchedule!.rows.map(item=>item.id===taskResult?{...item,child:{...item.child!,result}}:item)}
   mutateCheckpoint(checkpoint,{planState:{...checkpoint.planState!,taskSchedule}})
   mutateCheckpoint(checkpoint,{digest:recordDigest(checkpoint)});checkRecord('checkpoint',checkpoint)
   verified={step:row.id,task:task.id,planRevision:row.child.planRevision,...result}
  }
  const input={runId:this.id,expectedRevision:this.revision,commitId,checkpoint:encoder.encode(canonicalJSON(checkpoint)),events:[...(output?[output]:[]),...extra].map(event=>encoder.encode(canonicalJSON({version:1,scope:binding.scope,event})))}
  const receipt=await this.commitWithContext(input,signal,checkpoint.status==='complete'||checkpoint.status==='stopped',checkpoint.context.scope,checkpoint,binding.scope)
  this.revision=receipt.revision;this.latest=checkpoint
  if(verified){this.taskSchedule=structuredClone(checkpoint.planState!.taskSchedule!);this.taskResults.set(verified.step,verified);this.taskScopes.get(this.taskRoot!)!.accept(checkpoint)}
  if(signal?.aborted)this.dispatchClosed=true
  return output
 }
 save(binding:DurableBinding,state:ExecutionCheckpoint,signal?:AbortSignal){return this.enqueue(()=>{if(this.dispatchClosed)throw new DispatchCanceled(this.closed());return this.publish(binding,state,undefined,undefined,signal)}).then(()=>undefined)}
 /** Commit once, then issue a private writer-owned delivery identity. It is never serialized. */
 /** A claim handle is private to one acknowledged native execution, never a host marker. */
 acknowledgeNativeRouteRelease(binding:DurableBinding,token:object|undefined,request:string,route:string,needed:string){
  const claim=token?this.nativeClaims.get(token):undefined
  if(!claim||binding.writer!==this||scopeKey(binding.scope)!==scopeKey(claim.scope)||!claim.live||claim.released||claim.consumed||canonicalJSON(JSON.parse(claim.request))!==request||claim.requestDigest!==digest(claim.request)||claim.route!==route||this.latest?.turnRoute?.bindingDigest!==route||this.latest.turnRoute.widened||!needed)throw new ContinuationIdentityChanged()
  claim.released=true;claim.needed=needed
 }
 private supersededNative(binding:DurableBinding,row:DurableDispatch){
  const plan=this.nativePlanSuperseded.get(row.id)
  if(plan){if(row.kind!=='agent'||row.state!=='unknown'||!row.read||scopeKey(row.scope)!==scopeKey(binding.scope)||canonicalJSON(row)!==plan)throw new ContinuationIdentityChanged();return true}
  // A Plan decision exempts its exact claim, never another unknown in that scope.
  if(row.kind==='agent'&&scopeKey(row.scope)===scopeKey(binding.scope)&&this.book.dispatches.some(old=>this.nativePlanSuperseded.has(old.id)&&scopeKey(old.scope)===scopeKey(binding.scope)))throw new HostOutcomeUnknown()
  if(row.kind!=='agent'||scopeKey(row.scope)!==scopeKey(binding.scope)||!this.latest?.turnRoute?.widened)return false
  const marker=this.latest.turnRoute.widenedInvocation,proof=this.nativeSuperseded.get(scopeKey(binding.scope))
  if(!marker||!proof||proof.route!==canonicalJSON(this.latest.turnRoute))throw new ContinuationIdentityChanged()
  if(row.id!==marker.dispatchId)throw new HostOutcomeUnknown()
  if(proof.id!==row.id||proof.requestDigest!==row.requestDigest||marker.requestDigest!==row.requestDigest||digest(row.request)!==row.requestDigest||row.state!=='unknown'||!row.read)throw new ContinuationIdentityChanged()
  return true
 }
 /** Corroborate only execution transactions; archive/feedback metadata is not authority. */
 restoreNativeRoute(binding:DurableBinding,route:TurnRoute,signal?:AbortSignal){return this.enqueue(async()=>{
  const invalid=():never=>{throw new ContinuationIdentityChanged()},marker=route.widenedInvocation,store=this.store as Partial<HistoryRunStore>
  if(binding.writer!==this||!route.widened||!marker||store.historyVersion!==1||canonicalJSON(this.latest?.turnRoute)!==canonicalJSON(route))return invalid()
  const old=this.book.dispatches.find(row=>row.id===marker.dispatchId)
  if(!old||old.kind!=='agent'||!old.read||old.state!=='unknown'||scopeKey(old.scope)!==scopeKey(binding.scope)||old.requestDigest!==marker.requestDigest||digest(old.request)!==marker.requestDigest)return invalid()
  const port=store as HistoryRunStore,head=readHistoryHead(await port.readHistoryHead(this.id,signal))
  if(head.runId!==this.id||head.executionRevision!==this.revision||!this.acknowledgedCheckpoint||canonicalJSON(head.checkpoint)!==canonicalJSON({digest:historyBytesDigest(this.acknowledgedCheckpoint),bytes:this.acknowledgedCheckpoint.length}))return invalid()
  let bytes=0,sequence=0,eventCount=0,previous:string|null=null,claimed=0,widened=false
  const object=async(ref:HistoryObjectRef)=>{
   if(!validHistoryRef(ref)||(bytes+=ref.bytes)>HISTORY_TOTAL_BYTES)return invalid()
   const page=await port.readHistoryObject({kind:'run',id:this.id},head.revision,ref.digest,0,ref.bytes,signal)
   if(page.digest!==ref.digest||page.offset!==0||page.total!==ref.bytes||page.value.length!==ref.bytes||page.nextOffset!==ref.bytes||page.hasMore||historyBytesDigest(page.value)!==ref.digest)return invalid()
   return page.value
  }
  const matches=(state:ExecutionCheckpoint,status:'claimed'|'unknown')=>{const row=state.durable?.dispatches.find(row=>row.id===old.id);return state.id===this.id&&row&&row.state===status&&row.kind==='agent'&&row.read&&scopeKey(row.scope)===scopeKey(old.scope)&&row.request===old.request&&row.requestDigest===old.requestDigest&&row.source===old.source&&row.tool===old.tool}
  while(sequence<head.commitCount){
   if(sequence>=HISTORY_MAX_ROWS)return invalid()
   const page=await port.readHistoryPage(this.id,head.revision,sequence,100,signal)
   if(Object.keys(page).sort().join(',')!=='afterSequence,hasMore,items,nextSequence,revision,total'||page.revision!==head.revision||page.afterSequence!==sequence||!Array.isArray(page.items)||!page.items.length||page.items.length>100||page.total!==head.commitCount||page.nextSequence!==sequence+page.items.length||page.hasMore!==(page.nextSequence<head.commitCount))return invalid()
   for(const input of page.items){
    const row=readHistoryEntry(input);if(row.sequence!==++sequence||row.previousDigest!==previous||row.eventStart!==eventCount+1||(eventCount+=row.eventCount)>HISTORY_MAX_EVENTS)return invalid();previous=row.digest
    let state:ExecutionCheckpoint|undefined
    if(!claimed&&row.checkpoint){state=readExecution(JSON.parse(historyText(await object(row.checkpoint))));if(state&&matches(state,'claimed')&&state.turnRoute?.bindingDigest===route.bindingDigest&&!state.turnRoute.widened)claimed=sequence}
    if(!widened)for(const ref of row.events){
     const event=readStoredRunEvent(await object(ref))
     if(event.event.type==='route_widened'&&scopeKey(event.scope)===scopeKey(binding.scope)&&canonicalJSON(event.event.route)===canonicalJSON(route)){
      if(!claimed||claimed>=sequence||!row.checkpoint)return invalid()
      state??=readExecution(JSON.parse(historyText(await object(row.checkpoint))))
      if(!state||!matches(state,'unknown')||canonicalJSON(state.turnRoute)!==canonicalJSON(route))return invalid()
      widened=true
     }
    }
   }
  }
  const again=readHistoryHead(await port.readHistoryHead(this.id,signal)),executionHead=(value:typeof head)=>canonicalJSON([value.runId,value.executionRevision,value.commitCount,value.eventCount,value.root,value.checkpoint])
  if(!widened||previous!==head.root||eventCount!==head.eventCount||executionHead(again)!==executionHead(head))return invalid()
  this.nativeSuperseded.set(scopeKey(binding.scope),{id:old.id,requestDigest:old.requestDigest,route:canonicalJSON(route)})
 })}
 /** Publish the exact route and event together through this original writer's existing CAS. */
 commitRoute(binding:DurableBinding,state:ExecutionCheckpoint,event:Extract<AssistantEvent,{type:'route_decided'|'route_widened'}>,signal?:AbortSignal,nativeClaim?:object){return this.enqueue(async()=>{
  const heldState=structuredClone(state),heldEvent=structuredClone(event)
  let proof:NativeRouteClaim|undefined
  if(heldEvent.type==='route_widened'){
   if(heldEvent.route.widenedInvocation||heldState.turnRoute?.widenedInvocation)throw new ContinuationIdentityChanged()
   if(nativeClaim){proof=this.nativeClaims.get(nativeClaim);const row=proof?this.book.dispatches.find(row=>row.id===proof!.id):undefined
    if(!proof||proof.live||!proof.released||proof.consumed||!row||row.state!=='unknown'||row.kind!=='agent'||!row.read||scopeKey(row.scope)!==scopeKey(binding.scope)||proof.route!==this.latest?.turnRoute?.bindingDigest||this.latest.turnRoute.widened||heldEvent.route.bindingDigest!==proof.route||heldEvent.route.widenedFor!==proof.needed||row.requestDigest!==proof.requestDigest||digest(row.request)!==proof.requestDigest)throw new ContinuationIdentityChanged()
    const marker={dispatchId:proof.id,requestDigest:proof.requestDigest};heldEvent.route.widenedInvocation=marker;mutateCheckpoint(heldState,{turnRoute:{...heldState.turnRoute!,widenedInvocation:{...marker}}})
   }
  }
  if(binding.writer!==this||heldState.id!==binding.scope.execution||heldState.version!==3||canonicalJSON(heldState.turnRoute)!==canonicalJSON(heldEvent.route))throw new ContinuationIdentityChanged()
  if(this.planDelivery.size>=400)throw new ContinuationNotRecoverable()
  const output=(await this.publish(binding,heldState,heldEvent,undefined,signal))!
  if(proof){proof.consumed=true;this.nativeSuperseded.set(scopeKey(binding.scope),{id:proof.id,requestDigest:proof.requestDigest,route:canonicalJSON(heldState.turnRoute)})}
  this.planDelivery.set(output,{scope:scopeKey(binding.scope),bytes:canonicalJSON(output)});return output
 })}
 async restoreNativePlanDecisions(binding:DurableBinding,signal?:AbortSignal){
  if(!this.rootBinding(binding))throw new ContinuationIdentityChanged()
  if(!this.latest)return
  for(const decision of this.latest.planState?.decisions??[]){if(!decision.nativeInvocation)continue
   const old=this.book.dispatches.find(row=>row.id===decision.nativeInvocation!.dispatchId);if(!old)throw new ContinuationIdentityChanged()
   const head=await this.store.load(this.id,signal);if(!head||head.revision!==this.revision||!this.acknowledgedCheckpoint||!head.checkpoint||historyBytesDigest(head.checkpoint)!==historyBytesDigest(this.acknowledgedCheckpoint))throw new ContinuationIdentityChanged()
   await verifyNativePlanEvidence(this.store,head,this.latest,old,decision,true,signal);this.nativePlanSuperseded.set(old.id,canonicalJSON(old))
  }
 }
 private async stageNativePlanDecision(binding:DurableBinding,state:ExecutionCheckpoint,event:Extract<AssistantEvent,{type:'plan_drift_decided'}>,signal?:AbortSignal):Promise<DurableDispatch|undefined>{
  if(!binding.root||binding.scope.role!=='author'||binding.scope.task!==undefined||!this.latest)return
  const pending=this.latest.planState?.pending
  if(!pending||pending.callId!==event.callId||pending.revision!==event.revision||pending.candidate.planState.revision!==event.candidateRevision)throw new ContinuationIdentityChanged()
  const rows=this.book.dispatches.filter(row=>row.kind==='agent'&&(row.state==='unknown'||row.state==='claimed')&&scopeKey(row.scope)===scopeKey(binding.scope)&&!this.nativePlanSuperseded.has(row.id))
  if(rows.length>1||rows.some(row=>row.state!=='unknown'||row.planPause?.callId!==event.callId||row.planPause.revision!==event.revision||row.planPause.candidateRevision!==event.candidateRevision))throw new HostOutcomeUnknown();const old=rows[0];if(!old)return
  const head=await this.store.load(this.id,signal);if(!head||head.revision!==this.revision||!this.acknowledgedCheckpoint||!head.checkpoint||historyBytesDigest(head.checkpoint)!==historyBytesDigest(this.acknowledgedCheckpoint))throw new ContinuationIdentityChanged()
  const invocation=await verifyNativePlanEvidence(this.store,head,this.latest,old,undefined,false,signal)
  const planState=structuredClone(state.planState),decision=planState?.decisions?.at(-1)
  if(!decision||decision.callId!==event.callId||decision.revision!==event.revision||decision.candidateRevision!==event.candidateRevision||decision.approved!==event.approved||decision.note!==event.note)throw new ContinuationIdentityChanged()
  decision.nativeInvocation=structuredClone(invocation);event.nativeInvocation=structuredClone(invocation);mutateCheckpoint(state,{planState,version:3});return old
 }
 commitPlan(binding:DurableBinding,state:ExecutionCheckpoint,event:import('../execution/controller').PlanEvent,signal?:AbortSignal){return this.enqueue(async()=>{
  if(this.dispatchClosed)throw new DispatchCanceled(new DOMException('The operation was aborted','AbortError'))
  if((event.type==='approval_requested'?(event.purpose==='plan-drift'?event.plan.execution:undefined):event.execution)!==binding.scope.execution)throw new ContinuationIdentityChanged()
  if(this.planDelivery.size>=400)throw new ContinuationNotRecoverable()
  const paused=event.type==='plan_drift_decided'?await this.stageNativePlanDecision(binding,state,event,signal):undefined
  const prepared=(()=>{try{return this.prepareTaskPlan(binding,state)}catch(error){throw new DispatchCanceled(error)}})()
  if(paused&&this.guard){const guarded=this.guard()?.value(event,'message',false);if(canonicalJSON(guarded)!==canonicalJSON(event))throw new ContinuationNotRecoverable()}
  const previous=this.taskSchedule
  this.taskSchedule=prepared.planState?.taskSchedule??this.taskSchedule
  let output:AssistantEvent
  try{output=(await this.publish(binding,prepared,event,undefined,signal))!}catch(error){this.taskSchedule=previous;if(paused&&!(error instanceof StorageConflict))throw new StorageOutcomeUnknown(error);throw error}
  if(paused)this.nativePlanSuperseded.set(paused.id,canonicalJSON(paused))
  this.planDelivery.set(output,{scope:scopeKey(binding.scope),bytes:canonicalJSON(output)})
  return output
 })}
 event(binding:DurableBinding,event:AssistantEvent,signal?:AbortSignal){return this.enqueue(async()=>{if(this.dispatchClosed)throw new DispatchCanceled(this.closed());const delivery=this.planDelivery.get(event);if(delivery){this.planDelivery.delete(event);if(delivery.scope!==scopeKey(binding.scope)||delivery.bytes!==canonicalJSON(event))throw new ContinuationIdentityChanged();return event}if(binding.root&&event.type==='delegation_requested'){if(!event.callId)throw new ContinuationNotRecoverable();this.book.pendingHandoff={callId:event.callId,request:structuredClone(event.request)}}return this.publish(binding,event.type==='execution_checkpoint'||event.type==='budget_paused'?event.checkpoint:undefined,event,undefined,signal)}).then(result=>result!)}
 recovering(binding:DurableBinding,kind:DurableDispatch['kind']):boolean {
  return this.book.dispatches.some(row=>this.recovered.has(row.id)&&row.kind===kind&&scopeKey(row.scope)===scopeKey(binding.scope)&&row.state==='unknown'&&!this.supersededNative(binding,row)&&(kind!=='agent'||!this.latest?.handoff||this.latest.handoff.state==='continued'&&!!this.latest.handoff.continuation&&row.request.includes(JSON.stringify(this.latest.handoff.continuation).slice(1,-1))))
 }
 prepared(binding:DurableBinding,kind:DurableDispatch['kind'],source:string,tool:string,request:string,consume=false):string {
  if(kind!=='model'&&kind!=='agent')return request
  const pending=[...this.book.dispatches].reverse().find(row=>this.recovered.has(row.id)&&row.kind===kind&&scopeKey(row.scope)===scopeKey(binding.scope)&&row.state==='unknown'&&!this.supersededNative(binding,row))
  if(pending&&(kind!=='agent'||!this.latest?.handoff||this.latest.handoff.state==='continued'&&!!this.latest.handoff.continuation&&pending.request.includes(JSON.stringify(this.latest.handoff.continuation).slice(1,-1)))){
   if(this.hasTaskSteps||this.taskSchedule||kind==='model'&&!pending.read)throw new HostOutcomeUnknown()
   if(pending.source!==source||pending.tool!==tool)throw new ContinuationIdentityChanged()
   // The original admitted claim stays visible to coordinator accounting until this invocation returns.
   void consume
   safeRequest(pending.request);return pending.request
  }
  return request
 }
 startModel(binding:DurableBinding,source:string,tool:string,request:string,signal?:AbortSignal,ticket?:object):Promise<PendingModel>{
  return this.invoke(binding,'model',source,tool,request,true,async raw=>Object.freeze({[modelClaim]:true as const,request:raw}),()=>false,()=>[],signal,ticket)
 }
 async finishModel(binding:DurableBinding,handle:object,outcome:ModelOutcome,signal?:AbortSignal):Promise<void>{
  await this.enqueue(async()=>{
   const held=this.models.get(handle)
   if(!held||held.binding.writer!==this||binding.writer!==this||held.binding.root!==binding.root||scopeKey(held.binding.scope)!==scopeKey(binding.scope)||!held.claim.read||!this.stopped&&held.claim.state!=='claimed')throw new ContinuationIdentityChanged()
   if(!outcome.ok&&outcome.verifiedRefusal){const f=normalizeFailure(outcome.error,'model');if(f.source!=='model'||f.outcome!=='refused'||f.status===undefined||f.status<400||f.status>=500||f.status===408)throw new ContinuationIdentityChanged()}
   // Verified response bytes cannot become a result before storage acknowledgement.
   if(signal?.aborted)outcome={ok:false,error:outcome.ok?signal.reason:outcome.error}
   this.models.delete(handle)
   if(this.stopped)return
   if(this.dispatchClosed&&outcome.ok)outcome={ok:false,error:new DOMException('The operation was aborted','AbortError')}
   held.claim.state=outcome.ok||outcome.verifiedRefusal?'result':'unknown'
   if(this.budget&&!outcome.ok&&!outcome.verifiedRefusal)this.budget.unknown(held.claim.id)
   if(!outcome.ok)held.claim.failure=normalizeFailure(outcome.error,'model')
   if(signal?.aborted){this.dispatchClosed=true;return}
   try{await this.publish(binding,undefined,undefined,undefined,signal)}catch(cause){throw new StorageOutcomeUnknown(cause)}
   if(outcome.ok||outcome.verifiedRefusal){
    this.capture?.acknowledge(held.claim)
    for(const row of this.book.dispatches)if(row.kind==='model'&&row.read&&scopeKey(row.scope)===scopeKey(binding.scope)&&row.state==='unknown')this.recovered.delete(row.id)
   }else this.dispatchClosed=true
  })
 }
 async invokeModel(binding:DurableBinding,source:string,tool:string,request:string,execute:(request:string)=>Promise<Response>,verify:(response:Response)=>Promise<{response:Response;refusal?:unknown;usage?:UsageAmounts}>,signal:AbortSignal,ticket?:object):Promise<Response>{
  const handle=await this.startModel(binding,source,tool,request,signal,ticket)
  let reported:UsageAmounts|undefined
  let response:Response|undefined,outcome:ModelOutcome={ok:false,error:new ContinuationNotRecoverable()}
  try{
   signal.throwIfAborted();if(this.fatal!==undefined)throw this.fatal
   response=await execute(handle.request)
   const verified=await verify(response);response=verified.response;reported=verified.usage
   const claim=this.models.get(handle)?.claim;if(claim)await this.capture?.model(claim,response)
   outcome=verified.refusal===undefined?{ok:true}:{ok:false,error:verified.refusal,...(response.status>=400&&response.status<500&&response.status!==408&&normalizeFailure(verified.refusal,'model').outcome==='refused'?{verifiedRefusal:true as const}:{})}
  }catch(error){outcome={ok:false,error}}
  const dispatch=this.models.get(handle)?.claim.id
  await this.finishModel(binding,handle,outcome,signal)
  if(this.budget&&dispatch&&(outcome.ok||outcome.verifiedRefusal))await this.observeUsage(binding,dispatch,{...reported,modelRequests:1},digest(canonicalJSON({id:dispatch,status:response!.status,usage:reported??{}})),signal)
  if(!outcome.ok&&!outcome.verifiedRefusal)throw outcome.error
  if(this.dispatchClosed)throw new DOMException('The operation was aborted','AbortError')
  signal.throwIfAborted()
  return response!
 }
 async invoke<T>(binding:DurableBinding,kind:DurableDispatch['kind'],source:string,tool:string,request:string,read:boolean,execute:(request:string,nativeWitness?:object,nativeClaim?:object,captureFrame?:(body:string)=>void)=>Promise<T>,successful:(value:T)=>boolean=()=>true,artifacts:(value:T)=>ArtifactObservation[]=()=>[],signal?:AbortSignal,ticket?:object,captureBindings?:NativeCaptureBindings,planAttempt?:NativePlanAttempt,admit?:(value:T)=>Promise<void>,recordable=true):Promise<T>{
  safeRequest(request);requireId(source);requireId(tool)
  request=this.prepared(binding,kind,source,tool,request,true)
  const scope=structuredClone(binding.scope),fingerprint=digest(canonicalJSON({kind,scope,source,tool,request:JSON.parse(request)}))
  if(binding.writer!==this)throw new ContinuationIdentityChanged()
  let reused:DurableDispatch|undefined,refusal:AIFailureError|undefined,claim!:DurableDispatch
  await this.enqueue(async()=>{
   this.assertRepeatAdmission(binding)
   if(this.repeatTools?.executions.some(row=>row.execution===binding.scope.execution))canonicalRepeatArgs(request)
   if(this.dispatchClosed)throw new DispatchCanceled(new DOMException('The operation was aborted','AbortError'))
   if(!read){
    const rows=this.book.dispatches.filter(row=>!row.read&&row.kind===kind&&row.fingerprint===fingerprint),decisions=rows.map(row=>compareDurableRequests(row.request,request))
    reused=rows[decisions.indexOf('same')]
    // #226: a dispatch the run holds as unknown is never sent again, except the one operation a resumed re-authorization
    // wait granted on this fenced view, whose recorded failure is the typed never-admitted refusal: it is claimed again
    // under its own identity, once, and its effect host admits it for the first time. The effect host admits the row's
    // digest while the tool runs these bytes, so the grant takes only the very bytes that were refused, and only a write
    // this run can hold exactly (#275). Anything else keeps the row's failure and the grant, and gets what any other call
    // gets from this row: an exact variant is HostOutcomeUnknown, the same values written differently are refused, and
    // different numbers are another write with a row and an identity of their own.
    if(reused&&reused.state==='unknown'&&(kind==='host'||kind==='runtime')&&recordable&&digest(request)===reused.requestDigest&&takeReauthorizedDispatch(this.store,{run:this.id,operation:reused.id,source,tool,requestDigest:reused.requestDigest},reused.failure)){
     claim=reused;reused=undefined;claim.state='claimed';delete claim.failure
     await this.publish(binding,undefined,undefined,undefined,signal);return
    }
    if(reused)return
    if(decisions.includes('conflict')){refusal=new DurableRequestConflict();return}
   }
   // A record answers first: a published release may have recorded what this release cannot.
   if((kind==='host'||kind==='runtime')&&(!recordable||!recordableRequest(request))){refusal=new DurableRequestUnrecordable();return}
   if(this.book.dispatches.length>=400)throw new ContinuationNotRecoverable()
   const budgeted=this.budget&&(kind==='model'||kind==='agent')
   const id=budgeted?ticket?this.budget!.consume(ticket,{...scope,source,tool,descriptor:request,kind:kind as 'model'|'agent'}):(()=>{throw new UsageBudgetUnknown()})():crypto.randomUUID()
   claim={id,scope,kind,source,tool,fingerprint,request,requestDigest:digest(request),state:'claimed',read}
   if(!this.hasTaskSteps&&kind==='model'&&this.repeatState(binding)&&JSON.parse(request).method!=='GET'){
    const charge=this.repeatRequestCharges.get(binding.scope.execution);if(!charge)throw new ContinuationIdentityChanged()
    if(!this.recovering(binding,'model'))await charge(JSON.parse(request).body??request)
   }
   await this.claimTaskDispatch(binding,claim)
   this.claimRepeatReminder(binding,claim)
   this.capture?.enroll(claim,captureBindings);this.book.dispatches.push(claim)
   if(planAttempt){if(kind!=='agent'||!validNativePlanAttempt(planAttempt,binding))throw new ContinuationIdentityChanged();const state=this.root?.()??this.latest;if(!state)throw new ContinuationIdentityChanged();mutateCheckpoint(state,{version:3});await this.publish(binding,state,{type:'execution_checkpoint',checkpoint:state,durable:true,version:2},undefined,signal);planAttempt.claim=structuredClone(claim)}else await this.publish(binding,undefined,undefined,undefined,signal)
  })
  if(refusal)throw refusal
  if(reused){if(reused.state!=='result'||reused.result===undefined)throw new HostOutcomeUnknown();return structuredClone(reused.result) as T}
  signal?.throwIfAborted();if(this.fatal!==undefined)throw this.fatal;if(this.dispatchClosed)throw new DOMException('The operation was aborted','AbortError')
  let value:T
  const nativeWitness=this.budget&&kind==='agent'&&ticket?openNativeWitness(binding,ticket):undefined
  const nativeClaim=kind==='agent'&&this.latest?.turnRoute&&!this.latest.turnRoute.widened?Object.freeze({}):undefined
  if(nativeClaim)this.nativeClaims.set(nativeClaim,{scope:structuredClone(scope),id:claim.id,requestDigest:claim.requestDigest,request,source,tool,route:this.latest!.turnRoute!.bindingDigest,live:true,released:false,consumed:false})
  // #242: `admit` is the source gate's receipt check. It runs after the execution and before anything of the result is
  // recorded; a result it refuses is recorded as the call's uncertain outcome with the refusal as its failure, never as a
  // result. Where the execution ended within its deadline, the check's own time is not the call's: what follows is
  // decided without the call's signal (`live`).
  let live=signal
  const finishRecovery=()=>{if(kind==='model'||kind==='agent')for(const row of this.book.dispatches)if(row.kind===kind&&scopeKey(row.scope)===scopeKey(binding.scope)&&row.state==='unknown')this.recovered.delete(row.id)}
  try{
   // #223: under a fenced store, a non-read host or runtime effect is admitted by the effect host with the fence; its
   // idempotency identity is the acknowledged claim's, never the epoch.
   const perform=()=>execute(request,nativeWitness,nativeClaim,this.capture&&kind==='agent'?body=>this.capture!.agent(claim,body):undefined)
   try{value=await ((kind==='host'||kind==='runtime')&&!read?fencedEffect(this.store,{run:this.id,operation:claim.id,source,tool,requestDigest:claim.requestDigest},perform,signal):perform())}finally{closeNativeWitness(nativeWitness);const held=nativeClaim?this.nativeClaims.get(nativeClaim):undefined;if(held)held.live=false}
   if(this.dispatchClosed)throw new DOMException('The operation was aborted','AbortError')
   if(kind==='model'){
    if(!value||typeof value!=='object'||!(value as unknown as PendingModel)[modelClaim]||this.models.has(value))throw new AIFailureError(failure('invalid-response','model','unknown'))
    this.models.set(value,{binding:{...binding,scope:{...binding.scope}},claim});return value
   }
   finishRecovery()
   if(admit&&!signal?.aborted){live=undefined;await admit(value)}
  }catch(thrown){
   // #226: the typed never-admitted failure means a refusal the package trusted for this very dispatch, and it is what lets
   // a resumed re-authorization wait send the row again. Any other failure of that shape (a tool's, after admission) is
   // recorded and reported as an unknown outcome, with nothing said about admission.
   const effect=kind==='host'||kind==='runtime',refused=effect&&takeFreshReauthorization(thrown)
   const error=effect&&!refused&&isNeverAdmittedFailure(normalizeFailure(thrown,'tool'))?new AIFailureError(failure('authentication','tool','unknown')):thrown
   if(kind!=='model')finishRecovery()
   const admitted=kind==='agent'&&admittedNativeWitness(nativeWitness,binding,ticket)&&!signal?.aborted&&!this.dispatchClosed
   try{await this.enqueue(async()=>{if(this.stopped)return;claim.state='unknown';if(this.budget&&(kind==='model'||kind==='agent')&&!admitted)this.budget.unknown(claim.id);claim.failure=normalizeFailure(error,kind==='host'||kind==='runtime'?'tool':'agent');if(live?.aborted||this.dispatchClosed){this.dispatchClosed=true;return}const state=this.root?.()??this.latest;if(state&&nativePlanReceiptMatches(planAttempt,binding,claim,state)){claim.planPause=structuredClone(planAttempt!.pause!);mutateCheckpoint(state,{version:3});await this.publish(binding,state,{type:'execution_checkpoint',checkpoint:state,durable:true,version:2},undefined,live)}else await this.publish(binding,undefined,undefined,undefined,live)})}catch{throw new StorageOutcomeUnknown()}
   if(this.budget&&admitted)await this.observeUsage(binding,claim.id,{providerInvocations:1},digest(canonicalJSON({id:claim.id,kind:'agent',state:'unknown',callback:true})),signal)
   if(kind==='model')this.dispatchClosed=true
   // #226: a trusted re-authorization refusal stops the run at the operation it refused: nothing after it is committed and
   // nothing more is dispatched, so the run's head holds it as the never-admitted dispatch a park binds.
   else if(refused){this.dispatchClosed=true;this.closure=error}
   throw error
  }
  if(live?.aborted||this.dispatchClosed){this.dispatchClosed=true;if(!this.stopped)claim.state='unknown';throw live?.reason??new DOMException('The operation was aborted','AbortError')}
  // Provider responses/transports/private reasoning are not stored. Exact tool results are guarded.
  try{await this.enqueue(async()=>{
   claim.state='result'
   if(kind==='host'||kind==='runtime'){if(this.repeatTools?.executions.some(row=>row.execution===binding.scope.execution)){const text=JSON.stringify(value);if(typeof text!=='string'||encoder.encode(text).length>STORAGE_RECORD_BYTES)throw new ContinuationNotRecoverable();claim.repeatResultJson=text}claim.result=structuredClone(value)}
   const observed=successful(value)?artifacts(value):[]
   claim.artifacts=structuredClone(observed)
   for(const item of observed){requireId(item.id);if(!['artifact','dialect'].includes(item.kind)||!/^sha256:[a-f0-9]{64}$/.test(item.digest))throw new ContinuationNotRecoverable()}
   const current=this.repeatTools?.executions.find(row=>row.execution===binding.scope.execution)
   let repeat:RepeatDetected|undefined
   if(current&&(kind==='host'||kind==='runtime')){const next=observeRepeat(current,claim);this.repeatTools!.executions[this.repeatTools!.executions.indexOf(current)]=next.state;repeat=next.event}
   await this.publish(binding,undefined,undefined,successful(value)&&['host','runtime'].includes(kind)?{dispatch:claim,artifacts:observed}:undefined,live,repeat?[writeRunEvent(repeat)]:[])
   if(kind==='agent')this.capture?.acknowledge(claim)
   if(current){const source=this.repeatScopes.get(binding.scope.execution);source?.accept(this.repeatState(binding)!);if(repeat){const output=writeRunEvent(repeat);this.planDelivery.set(output,{scope:scopeKey(binding.scope),bytes:canonicalJSON(output)});source?.event(output)}}
  })}catch{throw new StorageOutcomeUnknown()}
  if(this.budget&&kind==='agent')await this.observeUsage(binding,claim.id,{providerInvocations:1},digest(canonicalJSON({id:claim.id,kind:'agent',state:'result'})),signal)
  return value
 }
}
/** A callback acknowledges legacy checkpoint delivery. It cannot create an exact dispatch journal. */
function callbackProfile<T extends EngineSession>(session:T,store:RunStore,id:string,execution?:ExecutionCheckpoint,head?:RunHead):T {
 if(execution?.durable)throw new ContinuationNotRecoverable()
 if(execution&&execution.id!==id)throw new ContinuationIdentityChanged()
 if(head&&(head.runId!==id||!storageId(head.revision)||!storageId(head.commitId)||head.cursor!==null&&!storageId(head.cursor)))throw new StorageAcknowledgementInvalid()
 let revision=head?.revision??null,tail:Promise<unknown>=Promise.resolve(),fatal:unknown
 const checkpoint=(state:ExecutionCheckpoint):Promise<void>=>{
  if(state.durable)return Promise.reject(new ContinuationNotRecoverable())
  const bytes=encoder.encode(JSON.stringify(state))
  const next=tail.then(async()=>{
   if(fatal!==undefined)throw fatal
   const commitId=crypto.randomUUID(),receipt=await store.commit({runId:id,expectedRevision:revision,commitId,checkpoint:bytes,events:[]},session.signal)
   if(!receipt||receipt.commitId!==commitId||!storageId(receipt.revision)||receipt.revision===revision||receipt.cursor!==null&&!storageId(receipt.cursor))throw new StorageAcknowledgementInvalid()
   revision=receipt.revision;session.signal.throwIfAborted()
  })
  tail=next.catch(error=>{fatal=error});return next
 }
 checkpointProfiles.set(checkpoint,id)
 return {...session,context:{...session.context,runStore:undefined,execution,checkpoint}}
}
/** Internal admission used once for a root, then explicitly inherited by trusted child bindings. */
export async function durableSession<T extends EngineSession>(session:T):Promise<T>{
 const input=session.context
 if(input?.[durableOwner])return session
 const config=input?.runStore
 if(input?.usageBudget&&(!config||config.store.durability==='single-writer-callback'))throw new UsageBudgetUnsupported()
 if(input?.checkpoint&&checkpointProfiles.has(input.checkpoint)){if(input.execution?.durable)throw new ContinuationNotRecoverable();return session}
 if(!config&&!input?.checkpoint){if(input?.execution){checkRecordVersion('checkpoint',input.execution);if(input.execution.durable)throw new ContinuationNotRecoverable()};return session}
 if(config&&input?.checkpoint)throw new ContinuationIdentityChanged()
 const id=config?.id??input?.execution?.id??crypto.randomUUID();requireId(id)
 const store=config?.store??checkpointCallbackRunStore(input!.checkpoint!,bytes=>readExecution(JSON.parse(new TextDecoder().decode(bytes)))!)
 const head=config?await store.load(id,session.signal):undefined
 if(config&&!!head!==!!config.resume)throw new StorageConflict(null,head?.revision??null)
 if(head&&!head.checkpoint)throw new ContinuationNotRecoverable()
 const execution=head?.checkpoint?readExecution(JSON.parse(new TextDecoder().decode(head.checkpoint))):input?.execution
 if(head&&input?.execution&&canonicalJSON(execution)!==canonicalJSON(input.execution))throw new ContinuationIdentityChanged()
 if(store.durability==='single-writer-callback')return callbackProfile(session,store,id,execution,head)
 if(execution?.durable?.pendingHandoff&&!execution.handoff)throw new ProviderReleaseUnknown()
 if(execution?.durable?.dispatches.some(row=>row.failure?.detail?.startsWith('ProviderReleaseUnknown:')))throw new ProviderReleaseUnknown()
 if(config?.context){if(config.context.store!==store||(input?.chat!==config.context&&!(config.context.ledgerScope&&input?.chat===undefined)))throw new ContinuationIdentityChanged();if(head){const history=await config.context.store.readHistoryHead(id,session.signal);if(!history?.context||history.executionRevision!==head.revision||!config.context.matches(history.context))throw new ContinuationIdentityChanged()}if(config.context.ledgerScope&&(!execution||execution.context.scope!==config.context.ledgerScope||execution.status==='complete'||execution.status==='stopped'))throw new ContinuationIdentityChanged()}
 const writer=new RunWriter(store,id,head,execution),binding:DurableBinding={writer,scope:{execution:id,role:'author'},root:true}
 await writer.restoreNativePlanDecisions(binding,session.signal)
 if(session.record===true&&session.corpus&&(store as Partial<HistoryRunStore>).historyVersion===1)writer.enrollCapture(binding,session.corpus.manifest,session.corpus.sink)
 if(config?.context){writer.attachContext(config.context);if(head?.checkpoint)await writer.claimContext(head.checkpoint,session.signal)}
 return {...session,context:{...input,...(config?.context?.ledgerScope?{chat:undefined}:{}),execution,[durableOwner]:binding,checkpoint:state=>writer.save(binding,state,session.signal)}}
}
export function childDurability<T extends EngineSession>(session:T,parent:EngineSession,task:string):T{
 const binding=parent.context?.[durableOwner];if(!binding)return session
 const child:DurableBinding={writer:binding.writer,scope:{execution:task,role:'author',task},root:false}
 return {...session,context:{...session.context,runStore:undefined,usageBudget:parent.context?.usageBudget,[durableOwner]:child,execution:binding.writer.checkpoint?.durable?.children.find(row=>row.scope.task===task)?.checkpoint,checkpoint:state=>binding.writer.save(child,state,session.signal)}}
}
export function scopeFor(binding:DurableBinding,role:string):DurableBinding{return {...binding,scope:{...binding.scope,role}}}
export const hostSucceeded=(result:McpToolResult)=>result.isError!==true
/** #223: whether a host tool's call is a read for the writer (and so never reaches the effect host). This is the call's
 * effect, not the framing of its answer (`readingTool`): a tool with a source is a read only when its effect class is read,
 * and an owner-declared act or propose effect is never a read. Every other call is an effect: its outcome is recorded
 * before dependent work, an unknown outcome is never repeated, and under a fenced store the effect host admits it. */
export function durableReadingTool(tool:Pick<HostTool,'effect'|'delegation'|'interaction'|'source'|'effectClass'|'taxonomy'>):boolean{
 if(tool.taxonomy?.effect==='act'||tool.taxonomy?.effect==='propose')return false
 if(tool.source!==undefined)return tool.effectClass==='read'
 return readingTool(tool)
}

/** Decode a committed root-log envelope; the host still binds its resource scope. */
export function readStoredRunEvent(bytes:Uint8Array):{version:1;scope:DurableScope;event:AssistantEvent}{
 if(bytes.length>STORAGE_RECORD_BYTES)throw new ContinuationNotRecoverable()
 const value=JSON.parse(new TextDecoder('utf-8',{fatal:true}).decode(bytes))
 if(!value||value.version!==1||Object.keys(value).some(k=>!['version','scope','event'].includes(k))||!value.scope||Object.keys(value.scope).some(k=>!['execution','role','task'].includes(k)))throw new ContinuationIdentityChanged()
 requireId(value.scope.execution);requireId(value.scope.role);if(value.scope.task!==undefined)requireId(value.scope.task)
 return {version:1,scope:structuredClone(value.scope),event:readRunEvent(value.event)}
}
