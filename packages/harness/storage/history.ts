import {readCorpusCandidate,validateCandidateFeedback} from '../corpusCandidate'
import type {HistoryMetadata,HistoryMetadataEntry,HistoryMetadataHead,HistoryMetadataPage} from '../history/types'
import {historyObservationClosure} from '../history/closure'
import {readHistoryMetadata,readRunCapture} from '../history/metadata'
import {readExecution} from '../execution/state'
import {readStoredRunEvent} from './writer'
import type {RunCommit,RunReceipt,RunStore} from './run'
import {copyHistoryBytes,historyObject,validHistoryRef,readHistoryChunkManifest,historyJSONDigest,historyBytesDigest,HISTORY_MAX_ROWS,HISTORY_MAX_EVENTS,HISTORY_OBJECT_BYTES,HistoryInputInvalid,HistoryChanged,historyJSONBytes,historyText,readCanonicalHistoryJSON,type HistoryObjectRef,type HistoryObject} from '../history/codec'
import {canonicalJSON} from '../context/chat'
import {readChatContextSnapshot} from '../context/chatSnapshot'
import {requireId,requireLimit,StorageConflict,STORAGE_TRANSACTION_BYTES} from './common'
export type HistoryResource={kind:'run'|'chat';id:string}
export interface ContextSnapshotUpdate{chatId:string;expectedRevision:string|null;manifest:HistoryObjectRef;turn?:number;closed?:true;privateScope?:string}
export interface ContextSnapshotHead{chatId:string;revision:string;manifest:HistoryObjectRef}
export interface RunHistoryCommit{run:RunCommit;observations:HistoryObjectRef[];genesis?:true;context?:ContextSnapshotUpdate}
export interface HistoryContextRef{chatId:string;revision:string;manifest:HistoryObjectRef;turn?:number;closed?:true;privateScope?:string}
export interface RunHistoryEntry{version:1;sequence:number;eventStart:number;eventCount:number;commitId:string;executionRevision:string;recordedAt:string;previousDigest:string|null;checkpoint:HistoryObjectRef|null;events:HistoryObjectRef[];observations:HistoryObjectRef[];context?:HistoryContextRef;digest:string}
export interface RunHistoryHead{version:1;runId:string;revision:string;executionRevision:string;commitCount:number;eventCount:number;root:string;coverage:'legacy-incomplete'|'recording-incomplete';metadata?:HistoryMetadataHead;checkpoint:HistoryObjectRef|null;context?:HistoryContextRef}
export interface HistoryMetadataCommit{runId:string;expectedHistoryRevision:string;expectedMetadataRevision:string|null;commitId:string;record:HistoryMetadata}
export interface RunHistoryReceipt extends RunReceipt{history:RunHistoryHead}
export interface RunHistoryPage{revision:string;afterSequence:number;items:RunHistoryEntry[];nextSequence:number;hasMore:boolean;total:number}
export interface HistoryObjectPage{digest:string;total:number;offset:number;value:Uint8Array;nextOffset:number;hasMore:boolean}
/** Host-bound storage capability. Observation/metadata is not execution authority. */
export interface HistoryRunStore extends RunStore{
 readonly historyVersion:1
 stageHistoryObjects(resource:HistoryResource,objects:readonly HistoryObject[],signal?:AbortSignal):Promise<void>
 commitHistory(input:RunHistoryCommit,signal?:AbortSignal):Promise<RunHistoryReceipt>
 readHistoryHead(runId:string,signal?:AbortSignal):Promise<RunHistoryHead|undefined>
 readHistoryPage(runId:string,revision:string,afterSequence:number,limit:number,signal?:AbortSignal):Promise<RunHistoryPage>
 readHistoryObject(resource:HistoryResource,revision:string,digest:string,offset:number,limit:number,signal?:AbortSignal):Promise<HistoryObjectPage>
 loadContextSnapshot(chatId:string,signal?:AbortSignal):Promise<ContextSnapshotHead|undefined>
 commitContextSnapshot(input:ContextSnapshotUpdate,commitId:string,signal?:AbortSignal):Promise<ContextSnapshotHead>
 commitHistoryMetadata(input:HistoryMetadataCommit,signal?:AbortSignal):Promise<RunHistoryHead>
 readHistoryMetadataPage(runId:string,revision:string,after:number,limit:number,signal?:AbortSignal):Promise<HistoryMetadataPage>
}
export function freezeHistoryInput(input:Omit<RunHistoryCommit,'run'>):Omit<RunHistoryCommit,'run'>{
 if(!input||Object.keys(input).some(k=>!['observations','genesis','context'].includes(k))||!Array.isArray(input.observations)||input.observations.length>256||!input.observations.every(validHistoryRef)||input.genesis!==undefined&&input.genesis!==true)throw new HistoryInputInvalid()
 if(input.context!==undefined){const v=input.context;if(!v||Object.keys(v).some(k=>!['chatId','expectedRevision','manifest','turn','closed','privateScope'].includes(k))||!validHistoryRef(v.manifest)||v.closed!==undefined&&v.closed!==true||v.closed&&v.turn===undefined||v.privateScope!==undefined&&(!v.closed||typeof v.privateScope!=='string')||v.turn!==undefined&&(!Number.isSafeInteger(v.turn)||v.turn<1))throw new HistoryInputInvalid();requireId(v.chatId);if(v.privateScope!==undefined)requireId(v.privateScope);if(v.expectedRevision!==null)requireId(v.expectedRevision)}
 return structuredClone(input)
}
const resourceKey=(r:HistoryResource)=>{if(!r||Object.keys(r).some(k=>k!=='kind'&&k!=='id')||!['run','chat'].includes(r.kind))throw new HistoryInputInvalid();requireId(r.id);return canonicalJSON(r)}
type View={head:RunHistoryHead;length:number;publication:number;metadataLength:number}
type ContextState={scope:string;turns:number;disposed:boolean;open:number[]}
type ContextView={head:ContextSnapshotHead;sequence:number;state:ContextState}
type Prepared={resource:string;objects:HistoryObject[];entry:RunHistoryEntry;view:View;context?:{head:ContextSnapshotHead;sequence:number;refs:HistoryObjectRef[];state:ContextState}}
/** @internal Memory helper; the containing store serializes all publication with its original queue/lock. */
export class MemoryHistoryJournal{
 private objects=new Map<string,Map<string,{object:HistoryObject;published?:number}>>()
 private runs=new Map<string,{entries:RunHistoryEntry[];metadata:HistoryMetadataEntry[];views:Map<string,View>;head:View}>()
 private metadataCommits=new Map<string,{input:string;head:RunHistoryHead}>()
 private contexts=new Map<string,{head:ContextView;views:Map<string,ContextView>}>()
 private turnOwners=new Map<string,string>()
 private contextCommits=new Map<string,{input:string;head:ContextSnapshotHead}>()
 constructor(private now:()=>Date=()=>new Date()){}
 async stage(resource:HistoryResource,values:readonly HistoryObject[]){
  const key=resourceKey(resource)
  if(!Array.isArray(values)||values.length<1||values.length>256)throw new HistoryInputInvalid()
  const copied=values.map(v=>{if(!v||Object.keys(v).some(k=>!['digest','bytes','value'].includes(k))||!validHistoryRef({digest:v.digest,bytes:v.bytes}))throw new HistoryInputInvalid();return {...v,value:copyHistoryBytes(v.value)}})
  if(copied.reduce((sum,v)=>sum+v.value.length,0)>STORAGE_TRANSACTION_BYTES)throw new HistoryInputInvalid()
  for(const item of copied){const actual=await historyObject(item.value);if(actual.digest!==item.digest||actual.bytes!==item.bytes)throw new HistoryInputInvalid()}
  const held=this.objects.get(key)??new Map()
  for(const item of copied){const old=held.get(item.digest);if(old&&(old.object.bytes!==item.bytes||old.object.value.some((v:number,i:number)=>v!==item.value[i])))throw new HistoryInputInvalid()}
  for(const item of copied)if(!held.has(item.digest))held.set(item.digest,{object:item})
  this.objects.set(key,held)
 }
 private resolve(resource:string,ref:HistoryObjectRef):HistoryObject{
  const object=this.objects.get(resource)?.get(ref.digest)?.object
  if(!object||object.bytes!==ref.bytes)throw new HistoryInputInvalid()
  return object
 }
 private context(input:ContextSnapshotUpdate){
  const held=this.contexts.get(input.chatId)
  if((held?.head.head.revision??null)!==input.expectedRevision)throw new StorageConflict(input.expectedRevision,held?.head.head.revision??null)
  const key=resourceKey({kind:'chat',id:input.chatId}),object=this.resolve(key,input.manifest),manifest=readHistoryChunkManifest(object.value)
  const full=new Uint8Array(manifest.bytes);let offset=0
  for(const ref of manifest.parts){const part=this.resolve(key,ref);full.set(part.value,offset);offset+=part.bytes}
  if(historyBytesDigest(full)!==manifest.digest)throw new HistoryInputInvalid()
  const snapshot=readChatContextSnapshot(full)
  if(input.turn!==undefined&&(input.turn>snapshot.turns||snapshot.open.some(row=>row.number===input.turn)===!!input.closed))throw new HistoryInputInvalid()
  if(input.privateScope!==undefined&&(!input.closed||input.privateScope===snapshot.scope))throw new HistoryInputInvalid()
  const old=held?.head.state
  if(old&&(snapshot.scope!==old.scope||snapshot.turns<old.turns||old.disposed&&!snapshot.disposed||snapshot.open.some(row=>row.number<=old.turns&&!old.open.includes(row.number))))throw new HistoryInputInvalid()
  return {head:{chatId:input.chatId,revision:crypto.randomUUID(),manifest:structuredClone(input.manifest)},sequence:(held?.head.sequence??0)+1,refs:[input.manifest,...manifest.parts],state:{scope:snapshot.scope,turns:snapshot.turns,disposed:snapshot.disposed,open:snapshot.open.map(row=>row.number)}}
 }
 prepare(run:RunCommit,receipt:RunReceipt,checkpoint:Uint8Array|null,extra?:Omit<RunHistoryCommit,'run'>):Prepared{
  const held=this.runs.get(run.runId),sequence=(held?.entries.length??0)+1,eventCount=(held?.head.head.eventCount??0)+run.events.length
  if(extra&&(sequence>HISTORY_MAX_ROWS||eventCount>HISTORY_MAX_EVENTS)||extra?.genesis&&held)throw new HistoryInputInvalid()
  const objects:HistoryObject[]=[],ref=(bytes:Uint8Array|null):HistoryObjectRef|null=>{if(bytes===null)return null;const object={digest:historyBytesDigest(bytes),bytes:bytes.length,value:new Uint8Array(bytes)};objects.push(object);return {digest:object.digest,bytes:object.bytes}}
  const checkpointRef=ref(checkpoint),events=run.events.map(e=>ref(e)!),key=resourceKey({kind:'run',id:run.runId})
  const recorded=new Map<string,number>();for(const row of held?.entries??[])for(const ref of row.observations)if(!recorded.has(ref.digest))recorded.set(ref.digest,row.sequence);for(const ref of extra?.observations??[])if(!recorded.has(ref.digest))recorded.set(ref.digest,sequence)
  objects.push(...historyObservationClosure(extra?.observations??[],ref=>this.resolve(key,ref),recorded))
  if(extra?.context){const c=extra.context,owner=this.turnOwners.get(canonicalJSON([c.chatId,c.turn]));if(c.turn===undefined||owner!==undefined&&owner!==run.runId||held?.head.head.context&&(held.head.head.context.chatId!==c.chatId||held.head.head.context.turn!==c.turn||held.head.head.context.closed&&!c.closed||held.head.head.context.privateScope!==undefined&&c.privateScope===undefined))throw new HistoryInputInvalid()}
  const context=extra?.context?this.context(extra.context):undefined
  const contextRef=context?{...context.head,turn:extra!.context!.turn,...(extra!.context!.closed?{closed:true as const}:{}),...(extra!.context!.privateScope?{privateScope:extra!.context!.privateScope}:{})}:held?.head.head.context
  const recordedAt=this.now().toISOString()
  const body={version:1 as const,sequence,eventStart:eventCount-run.events.length+1,eventCount:run.events.length,commitId:run.commitId,executionRevision:receipt.revision,recordedAt,previousDigest:held?.head.head.root??null,checkpoint:checkpointRef,events,observations:extra?.observations??[],...(contextRef?{context:contextRef}:{})}
  const entry={...body,digest:historyJSONDigest(body)}
  const head:RunHistoryHead={version:1,runId:run.runId,revision:crypto.randomUUID(),executionRevision:receipt.revision,commitCount:sequence,eventCount,root:entry.digest,coverage:held?.head.head.coverage??(extra?.genesis?'recording-incomplete':'legacy-incomplete'),...(held?.head.head.metadata?{metadata:{...held.head.head.metadata,executionRevision:receipt.revision}}:{}),checkpoint:checkpointRef,...(contextRef?{context:contextRef}:{})}
  return {resource:key,objects,entry,view:{head,length:sequence,publication:(held?.head.publication??0)+1,metadataLength:held?.metadata.length??0},context}
 }
 publish(prepared:Prepared){
  const {entry,view,resource,objects,context}=prepared,held=this.runs.get(view.head.runId)
  const values=this.objects.get(resource)??new Map()
  for(const object of objects)if(!values.has(object.digest))values.set(object.digest,{object})
  for(const ref of [...objects,...entry.observations]){const item=values.get(ref.digest)!;item.published??=view.publication}
  this.objects.set(resource,values)
  const entries=held?.entries??[],views=held?.views??new Map();entries.push(entry);views.set(view.head.revision,view);this.runs.set(view.head.runId,{entries,metadata:held?.metadata??[],views,head:view})
  if(context){this.publishContext(context);this.turnOwners.set(canonicalJSON([context.head.chatId,entry.context!.turn]),view.head.runId)}
 }
 prepareMetadata(input:HistoryMetadataCommit):{replay:boolean;publish:()=>RunHistoryHead}{
  if(!input||Object.keys(input).some(k=>!['runId','expectedHistoryRevision','expectedMetadataRevision','commitId','record'].includes(k)))throw new HistoryInputInvalid()
  requireId(input.runId);requireId(input.expectedHistoryRevision);requireId(input.commitId);if(input.expectedMetadataRevision!==null)requireId(input.expectedMetadataRevision)
  const record=readHistoryMetadata(historyJSONBytes(input.record)),text=canonicalJSON({...input,record}),key=canonicalJSON([input.runId,input.commitId]),prior=this.metadataCommits.get(key)
  if(prior){if(prior.input!==text)throw new HistoryInputInvalid();return {replay:true,publish:()=>structuredClone(prior.head)}}
  const held=this.runs.get(input.runId);if(!held)throw new HistoryInputInvalid()
  const head=held.head.head,meta=head.metadata
  if(head.revision!==input.expectedHistoryRevision)throw new StorageConflict(input.expectedHistoryRevision,head.revision)
  if((meta?.revision??null)!==input.expectedMetadataRevision)throw new StorageConflict(input.expectedMetadataRevision,meta?.revision??null)
  if(held.entries.length+held.metadata.length>=HISTORY_MAX_ROWS)throw new HistoryInputInvalid()
  const resource=resourceKey({kind:'run',id:input.runId}),refs:HistoryObjectRef[]=[]
  if(record.kind==='feedback'){
   const feedback=record.feedback;if(feedback.runId!==input.runId)throw new HistoryInputInvalid()
   const previous=held.metadata.map(row=>row.record).filter((row):row is Extract<HistoryMetadata,{kind:'feedback'}>=>row.kind==='feedback'&&row.feedback.id===feedback.id).at(-1)?.feedback
   if(feedback.sequence!==(previous?.sequence??0)+1||feedback.previous!==(previous?historyJSONDigest(previous):null)||previous&&canonicalJSON(previous.target)!==canonicalJSON(feedback.target))throw new HistoryInputInvalid()
   if(record.candidate){const object=this.resolve(resource,record.candidate),candidate=readCorpusCandidate(object.value);validateCandidateFeedback(candidate,feedback);if(candidate.sourceView!==head.revision||!head.checkpoint)throw new HistoryInputInvalid();const checkpoint=readExecution(JSON.parse(historyText(this.resolve(resource,head.checkpoint).value))),report=candidate.review.report as {checkpointVersion:unknown;status:unknown};if(!checkpoint||!['complete','stopped'].includes(checkpoint.status)||report.checkpointVersion!==checkpoint.version||report.status!==checkpoint.status)throw new HistoryInputInvalid();refs.push(record.candidate)}
   if(feedback.target.kind==='assistant-message'){
    const target=feedback.target,commit=held.entries.find(row=>target.eventSequence>=row.eventStart&&target.eventSequence<row.eventStart+row.eventCount),ref=commit?.events[target.eventSequence-(commit?.eventStart??0)]
    if(!ref||ref.digest!==target.eventDigest)throw new HistoryInputInvalid()
    try{const event=readStoredRunEvent(this.resolve(resource,ref).value).event;if(event.type!=='message')throw new HistoryInputInvalid()}catch{throw new HistoryInputInvalid()}
   }
  }else{
   const capture=this.resolve(resource,record.capture);refs.push(record.capture);const decoded=readRunCapture(capture.value)
   if(decoded.runId!==input.runId||decoded.executionRevision!==head.executionRevision||canonicalJSON(decoded.checkpoint)!==canonicalJSON(head.checkpoint)||!head.checkpoint)throw new HistoryInputInvalid()
   const checkpoint=readExecution(JSON.parse(historyText(this.resolve(resource,head.checkpoint).value)));if(!checkpoint||!['complete','stopped'].includes(checkpoint.status))throw new HistoryInputInvalid()
   const identities=new Map<string,NonNullable<NonNullable<typeof checkpoint>['durable']>['dispatches'][number]>()
   for(const entry of held.entries){if(!entry.checkpoint)continue;const checkpoint=readExecution(JSON.parse(historyText(this.resolve(resource,entry.checkpoint).value)));if(!checkpoint)throw new HistoryInputInvalid();for(const row of checkpoint.durable?.dispatches??[])if(row.kind==='model'||row.kind==='agent')identities.set(row.id,row)}
   const requests=[...identities.values()]
   if(requests.length!==decoded.requests.length)throw new HistoryInputInvalid()
   for(const row of decoded.requests){const claim=requests.find(claim=>claim.id===row.dispatchId);if(!claim||claim.kind!==row.kind||claim.source!==row.source||claim.tool!==row.tool||claim.requestDigest!==row.requestDigest||canonicalJSON(claim.scope)!==canonicalJSON(row.scope)||claim.state!=='result')throw new HistoryInputInvalid()}
   let total=0
   for(const file of decoded.files){refs.push(file.manifest);const manifest=readHistoryChunkManifest(this.resolve(resource,file.manifest).value);total+=manifest.bytes;if(total>64*1024*1024)throw new HistoryInputInvalid();const bytes=new Uint8Array(manifest.bytes);let at=0;for(const part of manifest.parts){refs.push(part);const value=this.resolve(resource,part).value;bytes.set(value,at);at+=value.length}if(historyBytesDigest(bytes)!==manifest.digest)throw new HistoryInputInvalid();readCanonicalHistoryJSON(bytes,64*1024*1024,'finite')}
  }
  const sequence=held.metadata.length+1,recordedAt=this.now().toISOString(),body={version:1 as const,executionRevision:head.executionRevision,commitSequence:head.commitCount,sequence,recordedAt,previousDigest:meta?.root??null,record},entry={...body,digest:historyJSONDigest(body)},metadata:HistoryMetadataHead={version:1,runId:input.runId,revision:crypto.randomUUID(),executionRevision:head.executionRevision,count:sequence,root:entry.digest}
  const next={...head,revision:crypto.randomUUID(),metadata},view:View={head:next,length:held.entries.length,publication:held.head.publication+1,metadataLength:sequence}
  // One synchronous publication in the containing MemoryRunStore's original queue.
  return {replay:false,publish:()=>{
  for(const ref of refs)this.objects.get(resource)!.get(ref.digest)!.published??=view.publication
  held.metadata.push(entry);held.views.set(next.revision,view);held.head=view;this.metadataCommits.set(key,{input:text,head:next});return structuredClone(next)}}
 }
 metadataPage(runId:string,revision:string,after:number,limit:number):HistoryMetadataPage{
  requireId(runId);requireId(revision);requireLimit(limit);const held=this.runs.get(runId),view=held?.views.get(revision)
  if(!view)throw new HistoryChanged();if(!Number.isSafeInteger(after)||after<0||after>view.metadataLength)throw new HistoryInputInvalid()
  const items:HistoryMetadataEntry[]=[];let bytes=0
  for(const row of held!.metadata.slice(after,Math.min(view.metadataLength,after+limit))){const size=historyJSONBytes(row).length;if(bytes+size>STORAGE_TRANSACTION_BYTES)break;items.push(row);bytes+=size}
  return {revision,afterSequence:after,items:structuredClone(items),nextSequence:after+items.length,hasMore:after+items.length<view.metadataLength,total:view.metadataLength}
 }
 private publishContext(value:{head:ContextSnapshotHead;sequence:number;refs:HistoryObjectRef[];state:ContextState}){
  const key=resourceKey({kind:'chat',id:value.head.chatId}),objects=this.objects.get(key)!
  for(const ref of value.refs)objects.get(ref.digest)!.published??=value.sequence
  const views=this.contexts.get(value.head.chatId)?.views??new Map(),view={head:value.head,sequence:value.sequence,state:value.state};views.set(value.head.revision,view);this.contexts.set(value.head.chatId,{head:view,views})
 }
 commitContext(input:ContextSnapshotUpdate,commitId:string):ContextSnapshotHead{
  requireId(commitId);const frozen=freezeHistoryInput({observations:[],context:input}).context!,key=canonicalJSON([input.chatId,commitId]),text=canonicalJSON(frozen),prior=this.contextCommits.get(key)
  if(prior){if(prior.input!==text)throw new HistoryInputInvalid();return structuredClone(prior.head)}
  const prepared=this.context(frozen);this.publishContext(prepared);this.contextCommits.set(key,{input:text,head:prepared.head});return structuredClone(prepared.head)
 }
 head(runId:string){requireId(runId);return structuredClone(this.runs.get(runId)?.head.head)}
 contextHead(chatId:string){requireId(chatId);return structuredClone(this.contexts.get(chatId)?.head.head)}
 page(runId:string,revision:string,after:number,limit:number):RunHistoryPage{
  requireId(runId);requireId(revision);requireLimit(limit)
  const held=this.runs.get(runId),view=held?.views.get(revision)
  if(!view)throw new HistoryChanged()
  if(!Number.isSafeInteger(after)||after<0||after>view.length)throw new HistoryInputInvalid()
  const items:RunHistoryEntry[]=[];let bytes=0
  for(const row of held!.entries.slice(after,Math.min(view.length,after+limit))){const size=new TextEncoder().encode(canonicalJSON(row)).length;if(bytes+size>STORAGE_TRANSACTION_BYTES)break;items.push(row);bytes+=size}
  return {revision,afterSequence:after,items:structuredClone(items),nextSequence:after+items.length,hasMore:after+items.length<view.length,total:view.length}
 }
 object(resource:HistoryResource,revision:string,digest:string,offset:number,limit:number):HistoryObjectPage{
  const key=resourceKey(resource);requireId(revision)
  const bound=resource.kind==='run'?this.runs.get(resource.id)?.views.get(revision)?.publication:this.contexts.get(resource.id)?.views.get(revision)?.sequence
  if(bound===undefined)throw new HistoryChanged()
  const item=this.objects.get(key)?.get(digest)
  if(!item||item.published===undefined||item.published>bound||!Number.isSafeInteger(offset)||offset<0||offset>item.object.bytes||!Number.isSafeInteger(limit)||limit<1||limit>HISTORY_OBJECT_BYTES)throw new HistoryInputInvalid()
  const value=item.object.value.slice(offset,offset+limit),nextOffset=offset+value.length
  return {digest,total:item.object.bytes,offset,value,nextOffset,hasMore:nextOffset<item.object.bytes}
 }
}
