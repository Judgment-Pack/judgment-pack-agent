import {readCorpusCandidate,validateCandidateFeedback} from '../corpusCandidate'
import {canonicalJSON} from '../context/chat'
import {readChatContextSnapshot} from '../context/chatSnapshot'
import {readExecution} from '../execution/state'
import {readStoredRunEvent} from '../storage/writer'
import {storageId} from '../storage/common'
import type {HistoryRunStore,RunHistoryEntry,RunHistoryHead,HistoryResource} from '../storage/history'
import type {HistoryMetadataEntry} from './types'
import {validateHistoryRelations} from './relations'
import {historyObservationClosure} from './closure'
import {readHistoryMetadata,readHistoryObservation,readRunCapture} from './metadata'
import {HistoryChanged,HistoryInputInvalid,HISTORY_TOTAL_BYTES,HISTORY_OBJECT_BYTES,HISTORY_MAX_ROWS,HISTORY_MAX_EVENTS,copyHistoryBytes,historyJSONBytes,historyJSONDigest,historyBytesDigest,historyDigestPattern,validHistoryRef,readHistorySourceJSON,readHistoryRequestJSON,historyText,readCanonicalHistoryJSON,readHistoryChunkManifest,type HistoryObjectRef} from './codec'
const invalid=():never=>{throw new HistoryInputInvalid()}
const object=(v:unknown):v is Record<string,unknown>=>v!==null&&typeof v==='object'&&!Array.isArray(v)
const closed=(v:unknown,required:string[],optional:string[]=[]):v is Record<string,unknown>=>object(v)&&required.every(k=>Object.hasOwn(v,k))&&Object.keys(v).every(k=>required.includes(k)||optional.includes(k))
const count=(v:unknown,min=0)=>Number.isSafeInteger(v)&&Number(v)>=min
const digest=(v:unknown):v is string=>typeof v==='string'&&historyDigestPattern.test(v)
const time=(v:unknown)=>typeof v==='string'&&/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/.test(v)&&Number.isFinite(Date.parse(v))&&new Date(v).toISOString()===v
/** Detach unknown port data without evaluating getters, executable members, sparse arrays or prototypes. */
function copyEnvelope(input:unknown,max:number,finite:boolean):unknown{
 let budget=0,nodes=0;const seen=new Set<object>(),string=(v:string)=>{if(v.length>max||/[\uD800-\uDBFF](?![\uDC00-\uDFFF])|(?<![\uD800-\uDBFF])[\uDC00-\uDFFF]/u.test(v))invalid();budget+=new TextEncoder().encode(v).length;if(budget>max)invalid();return v}
 const visit=(value:unknown,depth:number):unknown=>{
  if(depth>64||++nodes>max)invalid();budget++;if(budget>max)invalid()
  if(value===null||typeof value==='boolean')return value
  if(typeof value==='string')return string(value)
  if(typeof value==='number'){if(finite?!Number.isFinite(value):!Number.isSafeInteger(value))invalid();return value}
  if(!value||typeof value!=='object'||seen.has(value)||Object.getOwnPropertySymbols(value).length)invalid()
  if(Object.getPrototypeOf(value)!==Object.prototype&&Object.getPrototypeOf(value)!==null&&!Array.isArray(value))invalid()
  seen.add(value as object);const descriptors=Object.getOwnPropertyDescriptors(value),out:Array<unknown>|Record<string,unknown>=Array.isArray(value)?[]:Object.create(null)
  if(Array.isArray(value)){if(value.length>max||Object.keys(descriptors).length!==value.length+1)invalid();for(let i=0;i<value.length;i++){const item=descriptors[String(i)];if(!item||!('value'in item)||!item.enumerable)invalid();(out as unknown[]).push(visit(item.value,depth+1))}}
  else for(const [key,item] of Object.entries(descriptors)){if(!('value'in item)||!item.enumerable)invalid();(out as Record<string,unknown>)[string(key)]=visit(item.value,depth+1)}
  seen.delete(value as object);return out
 }
 try{return visit(input,0)}catch{return invalid()}
}
/** Transport envelopes keep their exact integer-only grammar. */
export function copyHistoryEnvelope(input:unknown,max=8*1024*1024):unknown{return copyEnvelope(input,max,false)}
/** Corpus manifests contain resolved finite policy fractions. This creates data only. */
export function copyCorpusManifestEnvelope(input:unknown,max=8*1024*1024):unknown{return copyEnvelope(input,max,true)}
const context=(v:unknown)=>closed(v,['chatId','revision','manifest','turn'],['closed','privateScope'])&&storageId(v.chatId)&&storageId(v.revision)&&validHistoryRef(v.manifest)&&count(v.turn,1)&&(v.closed===undefined||v.closed===true)&&(v.privateScope===undefined||v.closed===true&&storageId(v.privateScope))
export function readHistoryHead(input:unknown):RunHistoryHead{
 const v=copyHistoryEnvelope(input)
 if(!closed(v,['version','runId','revision','executionRevision','commitCount','eventCount','root','coverage','checkpoint'],['context','metadata'])||v.version!==1||!storageId(v.runId)||!storageId(v.revision)||!storageId(v.executionRevision)||!count(v.commitCount,1)||Number(v.commitCount)>HISTORY_MAX_ROWS||!count(v.eventCount)||Number(v.eventCount)>HISTORY_MAX_EVENTS||!digest(v.root)||!['legacy-incomplete','recording-incomplete','complete'].includes(String(v.coverage))||v.checkpoint!==null&&!validHistoryRef(v.checkpoint)||v.context!==undefined&&!context(v.context))return invalid()
 if(v.metadata!==undefined){const m=v.metadata;if(!closed(m,['version','runId','revision','executionRevision','count','root'])||m.version!==1||m.runId!==v.runId||m.executionRevision!==v.executionRevision||!storageId(m.revision)||!count(m.count,1)||Number(m.count)+Number(v.commitCount)>HISTORY_MAX_ROWS||!digest(m.root))return invalid()}
 return v as unknown as RunHistoryHead
}
export function readHistoryEntry(input:unknown):RunHistoryEntry{
 const v=copyHistoryEnvelope(input)
 if(!closed(v,['version','sequence','eventStart','eventCount','commitId','executionRevision','recordedAt','previousDigest','checkpoint','events','observations','digest'],['context'])||v.version!==1||!count(v.sequence,1)||!count(v.eventStart,1)||!count(v.eventCount)||!storageId(v.commitId)||!storageId(v.executionRevision)||!time(v.recordedAt)||v.previousDigest!==null&&!digest(v.previousDigest)||v.checkpoint!==null&&!validHistoryRef(v.checkpoint)||!Array.isArray(v.events)||v.events.length>256||v.events.length!==v.eventCount||!v.events.every(validHistoryRef)||!Array.isArray(v.observations)||v.observations.length>256||!v.observations.every(validHistoryRef)||!digest(v.digest)||v.context!==undefined&&!context(v.context))return invalid()
 const {digest:sum,...body}=v;if(historyJSONDigest(body)!==sum)return invalid();return v as unknown as RunHistoryEntry
}
export function readMetadataEntry(input:unknown):HistoryMetadataEntry{
 const v=copyHistoryEnvelope(input)
 if(!closed(v,['version','executionRevision','commitSequence','sequence','recordedAt','previousDigest','record','digest'])||v.version!==1||!storageId(v.executionRevision)||!count(v.commitSequence,1)||!count(v.sequence,1)||!time(v.recordedAt)||v.previousDigest!==null&&!digest(v.previousDigest)||!digest(v.digest))return invalid()
 readHistoryMetadata(historyJSONBytes(v.record));const {digest:sum,...body}=v;if(historyJSONDigest(body)!==sum)return invalid();return v as unknown as HistoryMetadataEntry
}
const viewToken=Symbol('validated retained history')
/** Detached evidence with copied byte reads. It exposes no store, write, restore or resume capability. */
export class RetainedRunHistory{
 #head:RunHistoryHead;#commits:RunHistoryEntry[];#metadata:HistoryMetadataEntry[];#values:Map<string,Uint8Array>
 constructor(token:symbol,head:RunHistoryHead,commits:RunHistoryEntry[],metadata:HistoryMetadataEntry[],values:Map<string,Uint8Array>){
  if(token!==viewToken)throw new HistoryInputInvalid()
  this.#head=head;this.#commits=commits;this.#metadata=metadata;this.#values=values;Object.freeze(this)
 }
 get head(){return structuredClone(this.#head)}get commits(){return structuredClone(this.#commits)}get metadata(){return structuredClone(this.#metadata)}
 readObject(ref:HistoryObjectRef){if(!validHistoryRef(ref))return invalid();const value=this.#values.get(ref.digest);if(!value||value.length!==ref.bytes)return invalid();return new Uint8Array(value)}
}
/** One named composite view; any metadata or execution change during collection refuses. */
export async function readRetainedRunHistory(store:HistoryRunStore,runId:string,signal?:AbortSignal):Promise<RetainedRunHistory>{
 if(!storageId(runId)||store.historyVersion!==1)return invalid();const initial=await store.readHistoryHead(runId,signal);if(!initial)return invalid();const head=readHistoryHead(initial);if(head.runId!==runId)return invalid()
 const commits:RunHistoryEntry[]=[],metadata:HistoryMetadataEntry[]=[],values=new Map<string,Uint8Array>(),visible=new Set<string>();let total=0
 const load=async(ref:HistoryObjectRef,resource:HistoryResource={kind:'run',id:runId},revision=head.revision)=>{
  if(!validHistoryRef(ref))return invalid();const prior=values.get(ref.digest),key=canonicalJSON([resource,revision,ref.digest]);if(prior){if(prior.length!==ref.bytes)return invalid();if(visible.has(key))return prior}
  if(!prior&&(values.size>=50000||total+ref.bytes>HISTORY_TOTAL_BYTES))return invalid()
  const input=await store.readHistoryObject(resource,revision,ref.digest,0,HISTORY_OBJECT_BYTES,signal)
  if(!input||Object.getOwnPropertySymbols(input).length||![Object.prototype,null].includes(Object.getPrototypeOf(input)))return invalid()
  const fields=Object.getOwnPropertyDescriptors(input),keys=['digest','total','offset','nextOffset','hasMore','value'];if(Object.keys(fields).length!==keys.length||!keys.every(key=>fields[key]&&'value'in fields[key]&&fields[key].enumerable))return invalid()
  const page=Object.fromEntries(keys.map(key=>[key,fields[key]!.value]))
  if(!page||page.digest!==ref.digest||page.total!==ref.bytes||page.offset!==0||page.nextOffset!==ref.bytes||page.hasMore)return invalid()
  const bytes=copyHistoryBytes(page.value);if(bytes.length!==ref.bytes||historyBytesDigest(bytes)!==ref.digest)return invalid();visible.add(key);if(!prior){total+=bytes.length;values.set(ref.digest,bytes)}return prior??bytes
 }
 const loadChunk=async(ref:HistoryObjectRef)=>{const manifest=readHistoryChunkManifest(await load(ref)),value=new Uint8Array(manifest.bytes);let offset=0;for(const part of manifest.parts){const bytes=await load(part);value.set(bytes,offset);offset+=bytes.length}if(historyBytesDigest(value)!==manifest.digest)return invalid();return value}
 let events=0,previous:string|null=null
 while(commits.length<head.commitCount){const after=commits.length,p=copyHistoryEnvelope(await store.readHistoryPage(runId,head.revision,after,100,signal))
  if(!closed(p,['revision','afterSequence','items','nextSequence','hasMore','total'])||p.revision!==head.revision||p.afterSequence!==after||p.total!==head.commitCount||!Array.isArray(p.items)||p.items.length<1||p.items.length>100||p.nextSequence!==after+p.items.length||typeof p.hasMore!=='boolean'||p.hasMore!==(Number(p.nextSequence)<head.commitCount)||Number(p.nextSequence)>head.commitCount)return invalid()
  for(const input of p.items){const row=readHistoryEntry(input);if(row.sequence!==commits.length+1||row.previousDigest!==previous||row.eventStart!==events+1)return invalid();previous=row.digest;events+=row.eventCount;commits.push(row)
   if(row.checkpoint){const bytes=await load(row.checkpoint);try{const checkpoint=readExecution(readCanonicalHistoryJSON(bytes,HISTORY_OBJECT_BYTES,'finite'));if(!checkpoint||checkpoint.id!==runId)return invalid()}catch{return invalid()}}
   if(row.context){const source=row.context,manifest=readHistoryChunkManifest(await load(source.manifest,{kind:'chat',id:source.chatId},source.revision)),full=new Uint8Array(manifest.bytes);let offset=0
    for(const part of manifest.parts){const value=await load(part,{kind:'chat',id:source.chatId},source.revision);full.set(value,offset);offset+=value.length}
    if(historyBytesDigest(full)!==manifest.digest)return invalid();const saved=readChatContextSnapshot(full)
    if(source.turn===undefined||source.turn>saved.turns||saved.open.some(turn=>turn.number===source.turn)===!!source.closed||source.privateScope!==undefined&&(!source.closed||source.privateScope===saved.scope))return invalid()
   }
   for(const ref of row.events){const bytes=await load(ref);try{readCanonicalHistoryJSON(bytes,HISTORY_OBJECT_BYTES,'finite');const {scope,event}=readStoredRunEvent(bytes);if(scope.task===undefined?scope.execution!==runId:scope.execution!==scope.task)return invalid();if((event.type==='execution_checkpoint'||event.type==='budget_paused')&&event.checkpoint.id!==scope.execution)return invalid()}catch{return invalid()}}
   for(const ref of row.observations){const observation=readHistoryObservation(await load(ref));if(observation.kind==='original')readHistorySourceJSON(historyText(await loadChunk(observation.value)));else if(observation.kind==='compaction'){for(const original of observation.originals)await load(original);readHistorySourceJSON(historyText(await loadChunk(observation.replacement)));for(const prior of observation.prior)await load(prior)}else if(observation.kind==='request'){readHistoryRequestJSON(historyText(await loadChunk(observation.request)));for(const ref of [...observation.compactions,...observation.originals])await load(ref)}else if(observation.kind==='relation')await load(observation.evidence)}
  }
 }
 if(events!==head.eventCount||previous!==head.root||canonicalJSON(commits.at(-1)!.checkpoint)!==canonicalJSON(head.checkpoint)||commits.at(-1)!.executionRevision!==head.executionRevision||canonicalJSON(commits.at(-1)!.context)!==canonicalJSON(head.context))return invalid()
 const recorded=new Map<string,number>();for(const row of commits)for(const ref of row.observations)if(!recorded.has(ref.digest))recorded.set(ref.digest,row.sequence)
 historyObservationClosure(commits.flatMap(row=>row.observations),ref=>{const value=values.get(ref.digest);if(!value)return invalid();return {...ref,value}},recorded)
 const count=head.metadata?.count??0;previous=null
 while(metadata.length<count){const after=metadata.length,p=copyHistoryEnvelope(await store.readHistoryMetadataPage(runId,head.revision,after,100,signal))
  if(!closed(p,['revision','afterSequence','items','nextSequence','hasMore','total'])||p.revision!==head.revision||p.afterSequence!==after||p.total!==count||!Array.isArray(p.items)||p.items.length<1||p.items.length>100||p.nextSequence!==after+p.items.length||typeof p.hasMore!=='boolean'||p.hasMore!==(Number(p.nextSequence)<count)||Number(p.nextSequence)>count)return invalid()
  for(const input of p.items){const row=readMetadataEntry(input);if(row.sequence!==metadata.length+1||row.previousDigest!==previous||row.commitSequence>commits.length||commits[row.commitSequence-1]!.executionRevision!==row.executionRevision)return invalid();previous=row.digest;metadata.push(row);if(row.record.kind==='capture'){const capture=readRunCapture(await load(row.record.capture));for(const file of capture.files){const manifest=readHistoryChunkManifest(await load(file.manifest));for(const part of manifest.parts)await load(part)}}else if(row.record.candidate){const candidate=readCorpusCandidate(await load(row.record.candidate));validateCandidateFeedback(candidate,row.record.feedback)}}
 }
 if(previous!==(head.metadata?.root??null))return invalid()
 validateHistoryRelations(commits,ref=>readCanonicalHistoryJSON(values.get(ref.digest)??invalid(),HISTORY_OBJECT_BYTES,'finite'))
 const final=await store.readHistoryHead(runId,signal);if(!final||canonicalJSON(readHistoryHead(final))!==canonicalJSON(head))throw new HistoryChanged()
 return new RetainedRunHistory(viewToken,head,commits,metadata,values)
}
