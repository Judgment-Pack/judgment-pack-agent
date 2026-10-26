import {canonicalJSON} from '../context/chat'
import type {ExecutionCheckpoint} from '../execution/state'
import type {DurableScope} from '../storage/writer'
import type {RunCommit} from '../storage/run'
import type {HistoryRunStore,RunHistoryHead} from '../storage/history'
import type {HistoryObservation} from './types'
import {HistoryInputInvalid,HistoryChanged,historyBytesDigest,historyText,readHistoryChunkManifest,type HistoryObjectRef} from './codec'
import {readHistoryObservation} from './metadata'
import {copyHistoryEnvelope,readHistoryHead,readRetainedRunHistory,readHistoryEntry} from './view'
import {prepareHistoryGenesis,prepareHistoryOriginal,prepareHistoryRequest,stageHistoryObservation,type PreparedHistoryObservation} from './recording'
const invalid=():never=>{throw new HistoryInputInvalid()}
const contextSource=(state:ExecutionCheckpoint)=>JSON.stringify(state.context.entries.map(({id,kind,label,valueJSON,bytes,digest,revision})=>{
 if(valueJSON===undefined)return invalid();return {id,kind,label,valueJSON,bytes,digest,...(revision===undefined?{}:{revision})}
}))
export interface PreparedLiveHistory{observations:HistoryObjectRef[];genesis?:true;acknowledge:(head:unknown,input:RunCommit,executionRevision:string,signal?:AbortSignal)=>Promise<void>}
/** Original-writer private recorder. Staged evidence never grants execution or dispatch. */
export class LiveHistoryRecorder{
 private initialized=false
 private enabled=true
 private genesis:boolean
 private head?:RunHistoryHead
 private contexts=new Map<string,HistoryObjectRef>()
 private active=new Map<string,HistoryObjectRef[]>()
 private requests=new Set<string>()
 private pending:{ref:HistoryObjectRef;row:HistoryObservation;sourceDigest?:string;replacement?:string}[]=[]
 private replacements=new Map<string,HistoryObjectRef>()
 constructor(private readonly store:HistoryRunStore,private readonly runId:string,private readonly executionRevision:string|null,legacy=false,private readonly checkpoint?:Uint8Array|null){this.genesis=executionRevision===null&&!legacy;this.checkpoint=checkpoint===undefined?undefined:checkpoint===null?null:new Uint8Array(checkpoint)}
 private async initialize(signal?:AbortSignal){
  if(this.initialized)return
  const input=await this.store.readHistoryHead(this.runId,signal),head=input===undefined?undefined:readHistoryHead(input)
  if(head&&(head.runId!==this.runId||head.executionRevision!==this.executionRevision||this.checkpoint!==undefined&&canonicalJSON(head.checkpoint)!==canonicalJSON(this.checkpoint===null?null:{digest:historyBytesDigest(this.checkpoint),bytes:this.checkpoint.length})))invalid()
  if(this.genesis){if(head)throw new HistoryChanged()}
  else if(head?.coverage==='recording-incomplete'){
   const view=await readRetainedRunHistory(this.store,this.runId,signal)
   if(canonicalJSON(view.head)!==canonicalJSON(head))throw new HistoryChanged()
   const chunk=(ref:HistoryObjectRef)=>{const manifest=readHistoryChunkManifest(view.readObject(ref)),bytes=new Uint8Array(manifest.bytes);let offset=0;for(const part of manifest.parts){const value=view.readObject(part);bytes.set(value,offset);offset+=value.length}return {text:historyText(bytes),digest:manifest.digest}}
   let genesis=0
   for(const commit of view.commits)for(const ref of commit.observations){const row=readHistoryObservation(view.readObject(ref));if(row.kind==='genesis'){if(commit.sequence!==1||row.scope.execution!==this.runId||row.scope.role!=='author'||row.scope.task!==undefined)invalid();genesis++}else if(row.kind==='original'&&row.format==='context')this.contexts.set(canonicalJSON([row.scope,row.boundary,chunk(row.value).digest]),ref);else if(row.kind==='request'){this.requests.add(row.dispatchId);this.active.set(canonicalJSON(row.scope),row.compactions)}else if(row.kind==='compaction'){this.replacements.set(canonicalJSON([row.scope,chunk(row.replacement).text]),ref);this.active.set(canonicalJSON(row.scope),[...(this.active.get(canonicalJSON(row.scope))??[]).filter(item=>!row.prior.some(prior=>prior.digest===item.digest)),ref])}}
   if(genesis!==1)invalid()
  }else this.enabled=false
  this.head=head;this.initialized=true
 }
 async recording(signal?:AbortSignal){await this.initialize(signal);return this.enabled}
 /** Staging is private to the original serialized writer; it does not publish a usable reference. */
 async add(prepared:PreparedHistoryObservation,signal?:AbortSignal){
  await this.initialize(signal)
  if(!this.enabled)invalid()
  const ref=await stageHistoryObservation(this.store,this.runId,prepared,signal),row=readHistoryObservation(prepared.objects.find(object=>object.digest===ref.digest)!.value)
  let sourceDigest:string|undefined,replacement:string|undefined
  if(row.kind==='original'&&row.format==='context')sourceDigest=readHistoryChunkManifest(prepared.objects.find(object=>object.digest===row.value.digest)!.value).digest
  if(row.kind==='compaction'){const manifest=readHistoryChunkManifest(prepared.objects.find(object=>object.digest===row.replacement.digest)!.value),bytes=new Uint8Array(manifest.bytes);let offset=0;for(const part of manifest.parts){const object=prepared.objects.find(object=>object.digest===part.digest);if(!object)return invalid();bytes.set(object.value,offset);offset+=object.bytes}replacement=historyText(bytes)}
  if(this.pending.length>=256)invalid()
  this.pending.push({ref,row,...(sourceDigest===undefined?{}:{sourceDigest}),...(replacement===undefined?{}:{replacement})})
  return {...ref}
 }
 prior(scope:DurableScope,messages:unknown[]){const refs:HistoryObjectRef[]=[],seen=new Set<string>();for(const message of messages){const ref=this.replacements.get(canonicalJSON([scope,JSON.stringify(message)]));if(ref&&!seen.has(ref.digest)){refs.push({...ref});seen.add(ref.digest)}}return refs}
 async prepare(state:ExecutionCheckpoint,scope:DurableScope,signal?:AbortSignal,contextUpdate=false):Promise<PreparedLiveHistory|undefined>{
  await this.initialize(signal)
  if(!this.enabled&&!contextUpdate)return undefined
  if(this.enabled){
  if(this.genesis&&!this.pending.some(row=>row.row.kind==='genesis'))await this.add(await prepareHistoryGenesis({execution:this.runId,role:'author'}),signal)
  const context=async(state:ExecutionCheckpoint,scope:DurableScope)=>{
   const source=contextSource(state),sum=historyBytesDigest(new TextEncoder().encode(source)),id=canonicalJSON([scope,state.context.scope,sum])
   const existing=this.contexts.get(id)??this.pending.find(row=>row.row.kind==='original'&&row.row.format==='context'&&canonicalJSON([row.row.scope,row.row.boundary,row.sourceDigest])===id)?.ref
   if(existing)return existing
   return this.add(await prepareHistoryOriginal(scope,state.context.scope,source,'context',0),signal)
  }
  await context(state,{execution:this.runId,role:scope.execution===this.runId?scope.role:'author'})
  for(const child of state.durable?.children??[])await context(child.checkpoint,child.scope)
  for(const claim of state.durable?.dispatches??[]){if(this.requests.has(claim.id)||this.pending.some(row=>row.row.kind==='request'&&row.row.dispatchId===claim.id))continue;const used=claim.scope.execution===state.id?state:state.durable?.children.find(child=>canonicalJSON(child.scope)===canonicalJSON(claim.scope))?.checkpoint;const originals=used?[await context(used,claim.scope)]:[];await this.add(await prepareHistoryRequest(claim.scope,claim.id,claim.source,claim.tool,claim.request,this.active.get(canonicalJSON(claim.scope))??[],originals),signal)}
  }
  const rows=[...this.pending.filter(item=>item.row.kind==='genesis'),...this.pending.filter(item=>item.row.kind!=='genesis')],genesis=this.genesis,previous=this.head
  return {observations:rows.map(row=>({...row.ref})),...(genesis?{genesis:true}:{}),acknowledge:async(inputHead,input,executionRevision,signal)=>{
   const head=readHistoryHead(inputHead),checkpoint=input.checkpoint===null?previous?.checkpoint??null:{digest:historyBytesDigest(input.checkpoint),bytes:input.checkpoint.length}
   if(head.runId!==this.runId||head.executionRevision!==executionRevision||head.revision===previous?.revision||head.coverage!==(previous?.coverage??(genesis?'recording-incomplete':'legacy-incomplete'))||head.commitCount!==(previous?.commitCount??0)+1||head.eventCount!==(previous?.eventCount??0)+input.events.length||canonicalJSON(head.checkpoint)!==canonicalJSON(checkpoint))invalid()
   const page=copyHistoryEnvelope(await this.store.readHistoryPage(this.runId,head.revision,head.commitCount-1,1,signal)) as {revision:string;afterSequence:number;items:unknown[];nextSequence:number;hasMore:boolean;total:number}
   if(!page||Object.keys(page).sort().join(',')!=='afterSequence,hasMore,items,nextSequence,revision,total'||page.revision!==head.revision||page.afterSequence!==head.commitCount-1||page.nextSequence!==head.commitCount||page.hasMore!==false||page.total!==head.commitCount||!Array.isArray(page.items)||page.items.length!==1)invalid()
   const row=readHistoryEntry(page.items[0]);if(row.sequence!==head.commitCount||row.previousDigest!==(previous?.root??null)||row.eventStart!==(previous?.eventCount??0)+1||row.digest!==head.root||row.commitId!==input.commitId||row.executionRevision!==executionRevision||canonicalJSON(row.checkpoint)!==canonicalJSON(checkpoint)||canonicalJSON(row.context)!==canonicalJSON(head.context)||canonicalJSON(row.observations)!==canonicalJSON(rows.map(row=>row.ref))||canonicalJSON(row.events)!==canonicalJSON(input.events.map(bytes=>({digest:historyBytesDigest(bytes),bytes:bytes.length}))))invalid()
   for(const item of rows){const row=item.row;if(row.kind==='original'&&row.format==='context')this.contexts.set(canonicalJSON([row.scope,row.boundary,item.sourceDigest]),item.ref);else if(row.kind==='request')this.requests.add(row.dispatchId);else if(row.kind==='compaction'){this.replacements.set(canonicalJSON([row.scope,item.replacement]),item.ref);this.active.set(canonicalJSON(row.scope),[...(this.active.get(canonicalJSON(row.scope))??[]).filter(ref=>!row.prior.some(prior=>prior.digest===ref.digest)),item.ref])}}
   this.pending=[];this.genesis=false;this.head=head
  }}
 }
}
