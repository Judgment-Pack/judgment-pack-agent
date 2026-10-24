import {ProviderReleaseUnknown} from './release'
import {canonicalJSON} from '../context/chat'
import {AIFailureError,failure,normalizeFailure} from '../failures'
import {readExecution,type ExecutionCheckpoint} from '../execution/state'
import {mutateCheckpoint} from '../execution/mutation'
import {writeRunEvent,checkRecord,checkRecordVersion,readRunEvent} from '../recordReader'
import {CHECKPOINT_SCHEMA,recordDigest,sha256,writeRecord,type RecordGuard,type RecordsPolicy} from '../records'
import type {AssistantEvent,EngineSession,McpToolResult} from '../engine'
import {checkpointCallbackRunStore,type RunStore,type RunHead} from './run'
import {StorageConflict,requireId,storageId,STORAGE_RECORD_BYTES} from './common'
export interface DurableScope {execution:string;role:string;task?:string}
export interface ArtifactObservation {kind:'artifact'|'dialect';id:string;digest:string;access?:'read'|'produced'}
export interface DurableDispatch {id:string;scope:DurableScope;kind:'model'|'agent'|'host'|'runtime';source:string;tool:string;fingerprint:string;request:string;requestDigest:string;state:'claimed'|'result'|'unknown';read:boolean;result?:unknown;failure?:import('../failures').AIFailure;artifacts?:ArtifactObservation[]}
export interface DurableObservation {id:string;scope:DurableScope;kind:'operation'|'artifact-read'|'artifact-produced';source:string;dispatchId:string;commitId:string;artifact?:ArtifactObservation}
export interface DurableBook {dispatches:DurableDispatch[];observations:DurableObservation[];children:{scope:DurableScope;checkpoint:ExecutionCheckpoint}[];pendingHandoff?:{callId:string;request:import('../delegationContract').DelegationRequest}}
export interface DurableRunInput {store:RunStore;id:string;/** Explicit host action. load/get/reload never resume. */resume?:boolean}
export class ContinuationNotRecoverable extends AIFailureError {constructor(){super({...failure('storage','storage','not-started'),detail:'ContinuationNotRecoverable: exact safe continuation bytes cannot be retained.'});this.name='ContinuationNotRecoverable'}}
export class StorageOutcomeUnknown extends AIFailureError {constructor(cause?:unknown){super({...failure('storage','storage','unknown'),detail:'StorageOutcomeUnknown: the dispatched operation result was not acknowledged.'});this.name='StorageOutcomeUnknown';if(cause!==undefined)Object.defineProperty(this,'cause',{value:cause})}}
export class StorageAcknowledgementInvalid extends AIFailureError {constructor(){super({...failure('storage','storage','unknown'),detail:'StorageAcknowledgementInvalid: the host did not acknowledge this exact transaction.'});this.name='StorageAcknowledgementInvalid'}}
export class HostOutcomeUnknown extends AIFailureError {constructor(){super({...failure('unknown','tool','unknown'),detail:'HostOutcomeUnknown: this operation may have run; it was not run again.'});this.name='HostOutcomeUnknown'}}
export class ContinuationIdentityChanged extends AIFailureError {constructor(){super({...failure('stale-state','context','not-started'),detail:'ContinuationIdentityChanged: the admitted continuation identity differs.'});this.name='ContinuationIdentityChanged'}}
export const durableOwner=Symbol('Agent durable writer')
/** Trusted coordinator accounting, before the root dispatch claim is frozen. */
export const prepareProviderDispatch=Symbol('Agent coordinator provider preparation')
export interface DurableBinding {writer:RunWriter;scope:DurableScope;root:boolean}
const modelClaim=Symbol('private live model claim')
type PendingModel={readonly [modelClaim]:true;readonly request:string}
export type ModelOutcome={ok:true}|{ok:false;error:unknown;verifiedRefusal?:true}
const encoder=new TextEncoder(),digest=(text:string)=>`sha256:${sha256(text)}`
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
 private revision:string|null
 private tail:Promise<unknown>=Promise.resolve()
 private fatal:unknown
 private book:DurableBook
 private root?:()=>ExecutionCheckpoint
 private policy?:RecordsPolicy
 private guard?:()=>RecordGuard|undefined
 private latest?:ExecutionCheckpoint
 private readonly models=new WeakMap<object,{binding:DurableBinding;claim:DurableDispatch}>()
 private recovered:Set<string>
 decorate:(state:ExecutionCheckpoint)=>ExecutionCheckpoint=state=>state
 constructor(readonly store:RunStore,readonly id:string,head?:RunHead,legacy?:ExecutionCheckpoint){
  if(store.durability==='single-writer-callback')throw new ContinuationNotRecoverable()
  if(head&&(head.runId!==id||!storageId(head.revision)||!storageId(head.commitId)||head.cursor!==null&&!storageId(head.cursor)))throw new StorageAcknowledgementInvalid()
  this.revision=head?.revision??null
  this.latest=head?.checkpoint?readExecution(JSON.parse(new TextDecoder('utf-8',{fatal:true}).decode(head.checkpoint))):legacy
  if(this.latest&&this.latest.id!==id)throw new ContinuationIdentityChanged()
  this.book=structuredClone(this.latest?.durable??{dispatches:[],observations:[],children:[]})
  this.recovered=new Set(this.book.dispatches.map(row=>row.id))
  this.book.dispatches=this.book.dispatches.map(row=>row.state==='claimed'?{...row,state:'unknown'}:row)
 }
 get checkpoint(){return this.latest?structuredClone(this.latest):undefined}
 get failed(){return this.fatal!==undefined}
 bind(snapshot:()=>ExecutionCheckpoint,policy:RecordsPolicy,guard:()=>RecordGuard|undefined){this.root=snapshot;this.policy=policy;this.guard=guard}
 private enqueue<T>(run:()=>Promise<T>):Promise<T>{const next=this.tail.then(async()=>{if(this.fatal!==undefined)throw this.fatal;try{return await run()}catch(error){this.fatal=error;throw error}});this.tail=next.catch(()=>undefined);return next}
 private finalize(state:ExecutionCheckpoint):ExecutionCheckpoint {
  const projected=this.decorate(structuredClone(state));if(projected.handoff)delete this.book.pendingHandoff;if(this.book.pendingHandoff)mutateCheckpoint(projected,{status:'running'});mutateCheckpoint(projected,{durable:structuredClone(this.book)})
  if(!this.policy)throw new ContinuationNotRecoverable()
  const before=canonicalJSON(projected.durable)
  const saved=writeRecord(CHECKPOINT_SCHEMA,{...projected},this.policy,this.guard?.(),new Date(),state.retainUntil)
  if(canonicalJSON(saved.durable)!==before||encoder.encode(canonicalJSON(saved)).length>STORAGE_RECORD_BYTES)throw new ContinuationNotRecoverable()
  checkRecord('checkpoint',saved)
  if(saved.id!==this.id||saved.digest!==recordDigest(saved))throw new ContinuationIdentityChanged()
  return saved
 }
 private async publish(binding:DurableBinding,state?:ExecutionCheckpoint,event?:AssistantEvent,observe?:{dispatch:DurableDispatch;artifacts:ArtifactObservation[]},signal?:AbortSignal){
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
  const root=binding.root&&state?state:this.root?.()??this.latest
  if(!root)throw new ContinuationNotRecoverable()
  const checkpoint=this.finalize(root)
  let output=event?writeRunEvent(event):undefined
  if(output&&(output.type==='execution_checkpoint'||output.type==='budget_paused'))output={...output,checkpoint:binding.root?checkpoint:state??output.checkpoint,...(output.type==='execution_checkpoint'?{durable:true}:{})}
  if(output)checkRecord('event',output)
  const receipt=await this.store.commit({runId:this.id,expectedRevision:this.revision,commitId,checkpoint:encoder.encode(canonicalJSON(checkpoint)),events:output?[encoder.encode(canonicalJSON({version:1,scope:binding.scope,event:output}))]:[]},signal)
  if(!receipt||receipt.commitId!==commitId||!storageId(receipt.revision)||receipt.revision===this.revision||receipt.cursor!==null&&!storageId(receipt.cursor)||output&&receipt.cursor===null)throw new StorageAcknowledgementInvalid()
  this.revision=receipt.revision;this.latest=checkpoint
  signal?.throwIfAborted()
  return output
 }
 save(binding:DurableBinding,state:ExecutionCheckpoint,signal?:AbortSignal){return this.enqueue(()=>this.publish(binding,state,undefined,undefined,signal)).then(()=>undefined)}
 event(binding:DurableBinding,event:AssistantEvent,signal?:AbortSignal){return this.enqueue(()=>{if(binding.root&&event.type==='delegation_requested'){if(!event.callId)throw new ContinuationNotRecoverable();this.book.pendingHandoff={callId:event.callId,request:structuredClone(event.request)}}return this.publish(binding,event.type==='execution_checkpoint'||event.type==='budget_paused'?event.checkpoint:undefined,event,undefined,signal)}).then(result=>result!)}
 recovering(binding:DurableBinding,kind:DurableDispatch['kind']):boolean {
  return this.book.dispatches.some(row=>this.recovered.has(row.id)&&row.kind===kind&&scopeKey(row.scope)===scopeKey(binding.scope)&&row.state==='unknown'&&(kind!=='agent'||!this.latest?.handoff||this.latest.handoff.state==='continued'&&!!this.latest.handoff.continuation&&row.request.includes(JSON.stringify(this.latest.handoff.continuation).slice(1,-1))))
 }
 prepared(binding:DurableBinding,kind:DurableDispatch['kind'],source:string,tool:string,request:string,consume=false):string {
  if(kind!=='model'&&kind!=='agent')return request
  const pending=[...this.book.dispatches].reverse().find(row=>this.recovered.has(row.id)&&row.kind===kind&&scopeKey(row.scope)===scopeKey(binding.scope)&&row.state==='unknown')
  if(pending&&(kind!=='agent'||!this.latest?.handoff||this.latest.handoff.state==='continued'&&!!this.latest.handoff.continuation&&pending.request.includes(JSON.stringify(this.latest.handoff.continuation).slice(1,-1)))){
   if(kind==='model'&&!pending.read)throw new HostOutcomeUnknown()
   if(pending.source!==source||pending.tool!==tool)throw new ContinuationIdentityChanged()
   // The original admitted claim stays visible to coordinator accounting until this invocation returns.
   void consume
   safeRequest(pending.request);return pending.request
  }
  return request
 }
 startModel(binding:DurableBinding,source:string,tool:string,request:string,signal?:AbortSignal):Promise<PendingModel>{
  return this.invoke(binding,'model',source,tool,request,true,async raw=>Object.freeze({[modelClaim]:true as const,request:raw}),()=>false,()=>[],signal)
 }
 async finishModel(binding:DurableBinding,handle:object,outcome:ModelOutcome,signal?:AbortSignal):Promise<void>{
  await this.enqueue(async()=>{
   const held=this.models.get(handle)
   if(!held||held.binding.writer!==this||binding.writer!==this||held.binding.root!==binding.root||scopeKey(held.binding.scope)!==scopeKey(binding.scope)||!held.claim.read||held.claim.state!=='claimed')throw new ContinuationIdentityChanged()
   if(!outcome.ok&&outcome.verifiedRefusal){const f=normalizeFailure(outcome.error,'model');if(f.source!=='model'||f.outcome!=='refused'||f.status===undefined||f.status<400||f.status>=500||f.status===408)throw new ContinuationIdentityChanged()}
   this.models.delete(handle)
   held.claim.state=outcome.ok||outcome.verifiedRefusal?'result':'unknown'
   if(!outcome.ok)held.claim.failure=normalizeFailure(outcome.error,'model')
   try{await this.publish(binding,undefined,undefined,undefined,signal)}catch(cause){throw new StorageOutcomeUnknown(cause)}
   if(outcome.ok||outcome.verifiedRefusal){
    for(const row of this.book.dispatches)if(row.kind==='model'&&row.read&&scopeKey(row.scope)===scopeKey(binding.scope)&&row.state==='unknown')this.recovered.delete(row.id)
   }else this.fatal=outcome.error??new AIFailureError(failure('unknown','model','unknown'))
  })
 }
 async invokeModel(binding:DurableBinding,source:string,tool:string,request:string,execute:(request:string)=>Promise<Response>,verify:(response:Response)=>Promise<{response:Response;refusal?:unknown}>,signal:AbortSignal):Promise<Response>{
  const handle=await this.startModel(binding,source,tool,request,signal)
  let response:Response|undefined,outcome:ModelOutcome={ok:false,error:new ContinuationNotRecoverable()}
  try{
   signal.throwIfAborted();if(this.fatal!==undefined)throw this.fatal
   response=await execute(handle.request)
   const verified=await verify(response);response=verified.response
   outcome=verified.refusal===undefined?{ok:true}:{ok:false,error:verified.refusal,...(response.status>=400&&response.status<500&&response.status!==408&&normalizeFailure(verified.refusal,'model').outcome==='refused'?{verifiedRefusal:true as const}:{})}
  }catch(error){outcome={ok:false,error}}
  await this.finishModel(binding,handle,outcome,signal)
  if(!outcome.ok&&!outcome.verifiedRefusal)throw outcome.error
  signal.throwIfAborted()
  return response!
 }
 async invoke<T>(binding:DurableBinding,kind:DurableDispatch['kind'],source:string,tool:string,request:string,read:boolean,execute:(request:string)=>Promise<T>,successful:(value:T)=>boolean=()=>true,artifacts:(value:T)=>ArtifactObservation[]=()=>[],signal?:AbortSignal):Promise<T>{
  safeRequest(request);requireId(source);requireId(tool)
  request=this.prepared(binding,kind,source,tool,request,true)
  const scope=structuredClone(binding.scope),fingerprint=digest(canonicalJSON({kind,scope,source,tool,request:JSON.parse(request)}))
  let reused:DurableDispatch|undefined,claim!:DurableDispatch
  await this.enqueue(async()=>{
   reused=this.book.dispatches.find(row=>!read&&!row.read&&row.kind===kind&&row.fingerprint===fingerprint)
   if(reused)return
   if(this.book.dispatches.length>=400)throw new ContinuationNotRecoverable()
   claim={id:crypto.randomUUID(),scope,kind,source,tool,fingerprint,request,requestDigest:digest(request),state:'claimed',read}
   this.book.dispatches.push(claim);await this.publish(binding,undefined,undefined,undefined,signal)
  })
  if(reused){if(reused.state!=='result'||reused.result===undefined)throw new HostOutcomeUnknown();return structuredClone(reused.result) as T}
  signal?.throwIfAborted();if(this.fatal!==undefined)throw this.fatal
  let value:T
  const finishRecovery=()=>{if(kind==='model'||kind==='agent')for(const row of this.book.dispatches)if(row.kind===kind&&scopeKey(row.scope)===scopeKey(binding.scope)&&row.state==='unknown')this.recovered.delete(row.id)}
  try{
   value=await execute(request)
   if(kind==='model'){
    if(!value||typeof value!=='object'||!(value as unknown as PendingModel)[modelClaim]||this.models.has(value))throw new AIFailureError(failure('invalid-response','model','unknown'))
    this.models.set(value,{binding:{...binding,scope:{...binding.scope}},claim});return value
   }
   finishRecovery()
  }catch(error){
   if(kind!=='model')finishRecovery()
   try{await this.enqueue(async()=>{claim.state='unknown';claim.failure=normalizeFailure(error,kind==='host'||kind==='runtime'?'tool':'agent');await this.publish(binding,undefined,undefined,undefined,signal)})}catch{throw new StorageOutcomeUnknown()}
   if(kind==='model')this.fatal=error
   throw error
  }
  // Provider responses/transports/private reasoning are not stored. Exact tool results are guarded.
  try{await this.enqueue(async()=>{
   claim.state='result'
   if(kind==='host'||kind==='runtime')claim.result=structuredClone(value)
   const observed=successful(value)?artifacts(value):[]
   claim.artifacts=structuredClone(observed)
   for(const item of observed){requireId(item.id);if(!['artifact','dialect'].includes(item.kind)||!/^sha256:[a-f0-9]{64}$/.test(item.digest))throw new ContinuationNotRecoverable()}
   await this.publish(binding,undefined,undefined,successful(value)&&['host','runtime'].includes(kind)?{dispatch:claim,artifacts:observed}:undefined,signal)
  })}catch{throw new StorageOutcomeUnknown()}
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
  const bytes=encoder.encode(canonicalJSON(state))
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
 const writer=new RunWriter(store,id,head,execution),binding:DurableBinding={writer,scope:{execution:id,role:'author'},root:true}
 return {...session,context:{...input,execution,[durableOwner]:binding,checkpoint:state=>writer.save(binding,state,session.signal)}}
}
export function childDurability<T extends EngineSession>(session:T,parent:EngineSession,task:string):T{
 const binding=parent.context?.[durableOwner];if(!binding)return session
 const child:DurableBinding={writer:binding.writer,scope:{execution:task,role:'author',task},root:false}
 return {...session,context:{...session.context,runStore:undefined,[durableOwner]:child,execution:binding.writer.checkpoint?.durable?.children.find(row=>row.scope.task===task)?.checkpoint,checkpoint:state=>binding.writer.save(child,state,session.signal)}}
}
export function scopeFor(binding:DurableBinding,role:string):DurableBinding{return {...binding,scope:{...binding.scope,role}}}
export const hostSucceeded=(result:McpToolResult)=>result.isError!==true

/** Decode a committed root-log envelope; the host still binds its resource scope. */
export function readStoredRunEvent(bytes:Uint8Array):{version:1;scope:DurableScope;event:AssistantEvent}{
 if(bytes.length>STORAGE_RECORD_BYTES)throw new ContinuationNotRecoverable()
 const value=JSON.parse(new TextDecoder('utf-8',{fatal:true}).decode(bytes))
 if(!value||value.version!==1||Object.keys(value).some(k=>!['version','scope','event'].includes(k))||!value.scope||Object.keys(value.scope).some(k=>!['execution','role','task'].includes(k)))throw new ContinuationIdentityChanged()
 requireId(value.scope.execution);requireId(value.scope.role);if(value.scope.task!==undefined)requireId(value.scope.task)
 return {version:1,scope:structuredClone(value.scope),event:readRunEvent(value.event)}
}
