import {admittedNativeCaptureBindings,type NativeCaptureBindings} from './physicalDispatch'
import {argumentProblem} from '../sourceArguments'
import manifestSchema from '../contract/schemas/corpus-entry.json'
import type {AssistantEvent} from '../engine'
import type {CorpusManifest,CorpusSink} from '../corpus'
import type {ExecutionCheckpoint} from '../execution/state'
import type {PolicyMembers} from '../policy'
import type {DurableDispatch} from './writer'
import type {HistoryRunStore,RunHistoryReceipt} from './history'
import {CaptureCollector,ReplayUnavailable,type CaptureToken} from '../history/captureCollector'
import {prepareCaptureExpectation} from '../history/recording'
import {prepareCapturePublication,commitCapturePublication} from '../history/capture'
import type {LiveHistoryRecorder,PreparedLiveHistory} from '../history/live'

/** Kept only by the original writer. Neither a retained journal nor a child owns its root. */
export class WriterCapture {
 private readonly collector:CaptureCollector
 private readonly tokens=new Map<string,CaptureToken>()
 private readonly bindings=new Map<string,Record<string,string>>()
 enroll(claim:DurableDispatch,proof?:NativeCaptureBindings){if(claim.kind!=='agent')return;const bindings=admittedNativeCaptureBindings(proof,claim.request);if(!bindings){this.unavailable=true;return}this.bindings.set(claim.id,bindings)}
 private pending:{claim:DurableDispatch;ref:{digest:string;bytes:number}}[]=[]
 private unavailable=false
 private finished=false
 constructor(private readonly owner:object,private readonly store:HistoryRunStore,private readonly id:string,private readonly manifest:CorpusManifest,private readonly sink:CorpusSink,uncovered:boolean){
  if(argumentProblem(manifestSchema,manifest)!==undefined||!sink||typeof sink.write!=='function')throw new ReplayUnavailable()
  this.collector=new CaptureCollector(owner,id,{execution:id,role:'author'})
  this.collector.bindPublisher(owner,store);this.unavailable=uncovered
 }
 async prepare(history:LiveHistoryRecorder,state:ExecutionCheckpoint,signal?:AbortSignal){
  if(this.unavailable||this.finished)return
  try{
   if(!await history.recording(signal))throw new ReplayUnavailable()
   for(const claim of state.durable?.dispatches??[]){
    if(!['model','agent'].includes(claim.kind)||this.tokens.has(claim.id)||this.pending.some(row=>row.claim.id===claim.id))continue
    if(claim.state!=='claimed'||!claim.read)throw new ReplayUnavailable()
    const identity=this.identity(claim),prepared=await prepareCaptureExpectation(identity,this.bindings.get(claim.id)),ref=await history.add(prepared,signal)
    this.pending.push({claim:structuredClone(claim),ref})
   }
  }catch{this.unavailable=true}
 }
 private identity(claim:DurableDispatch){return {scope:claim.scope,dispatchId:claim.id,kind:claim.kind as 'model'|'agent',source:claim.source,tool:claim.tool,requestDigest:claim.requestDigest,surface:claim.kind==='model'?'session-model' as const:'native-aggregate' as const}}
 admitted(receipt:RunHistoryReceipt,commitId:string,prepared:PreparedLiveHistory){
  if(this.unavailable||this.finished)return
  try{for(const {claim,ref}of this.pending){const ordinal=prepared.observations.findIndex(row=>row.digest===ref.digest&&row.bytes===ref.bytes);if(ordinal<0)throw new ReplayUnavailable();this.tokens.set(claim.id,this.collector.admit(this.owner,{identity:this.identity(claim),sequence:receipt.history.commitCount,ordinal,commitId,expectation:ref,request:claim.request}))}this.pending=[]}catch{this.unavailable=true}
 }
 async model(claim:DurableDispatch,response:Response){
  if(this.unavailable||this.finished)return
  try{const request=JSON.parse(claim.request),{suffix,...plain}=request,body=await response.clone().text();this.record(claim.id,{suffix,request:plain,response:{status:response.status,headers:Object.fromEntries(response.headers.entries()),body}})}catch{this.unavailable=true}
 }
 agent(claim:DurableDispatch,body:string){const aliases=this.bindings.get(claim.id);this.record(claim.id,{suffix:'agent.run',request:JSON.parse(claim.request),response:{status:200,headers:aliases?{'x-jp-native-tool-bindings':JSON.stringify(Object.fromEntries(Object.entries(aliases).sort()))}:{},body}})}
 private record(id:string,frame:unknown){if(this.unavailable||this.finished)return;try{const token=this.tokens.get(id);if(!token)throw new ReplayUnavailable();this.collector.record(this.owner,token,frame)}catch{this.unavailable=true}}
 acknowledge(claim:DurableDispatch){if(this.unavailable||this.finished)return;try{const token=this.tokens.get(claim.id);if(!token||claim.state!=='result')throw new ReplayUnavailable();this.collector.acknowledge(this.owner,token)}catch{this.unavailable=true}}
 async finish(checkpoint:ExecutionCheckpoint|undefined,revision:string|null,raw:Uint8Array|undefined,members:PolicyMembers|undefined,signal?:AbortSignal):Promise<AssistantEvent>{
  if(this.finished)throw new ReplayUnavailable();this.finished=true
  try{
   if(this.unavailable||!checkpoint||!revision||!raw||!['complete','stopped'].includes(checkpoint.status)||(checkpoint.durable?.children??[]).some(row=>!['complete','stopped'].includes(row.checkpoint.status)))throw new ReplayUnavailable()
   const {historyBytesDigest}=await import('../history/codec')
   const seal=this.collector.seal(this.owner,this.collector.rootToken(this.owner),{runId:this.id,executionRevision:revision,checkpoint:{digest:historyBytesDigest(raw),bytes:raw.length},status:checkpoint.status as 'complete'|'stopped'})
   const manifest=structuredClone(this.manifest);if(members)manifest.policy=structuredClone(members) as unknown as Record<string,unknown>
   let bytes=0
   const prepared=await prepareCapturePublication(this.store,seal,{commitId:crypto.randomUUID(),manifest,members,signal}),result=await commitCapturePublication(this.store,prepared,{sink:{write:async(id,files)=>{bytes=Object.values(files).reduce((sum,text)=>sum+new TextEncoder().encode(text).length,0);await this.sink.write(id,files)}},signal})
   if(result.capture!=='committed'||result.delivery!=='delivered')throw new ReplayUnavailable()
   return {type:'recorded',id:manifest.id,bytes}
  }catch{return {type:'guardrail',tool:'record',action:'refused',reason:'record refused',detail:'ReplayUnavailable: a complete supported capture could not be verified.'}}
 }
}
