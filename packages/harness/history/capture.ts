import {nativeBindings,nativeBindingsHeader} from './nativeBindings'
import {ImportedRunHistory,archiveDisplayMatches,archiveCheckpointMatches,type ArchiveKind} from './archive'
import {corpusCheckpointView,type CorpusCheckpointView} from './corpusCheckpointView'
import {canonicalJSON} from '../context/chat'
import {expectedFromEvents,type CorpusEntry,type RecordedRequest,type RecordedToolResult} from '../corpus'
import {argumentProblem} from '../sourceArguments'
import manifestSchema from '../contract/schemas/corpus-entry.json'
import {readExecution} from '../execution/state'
import {checkRecord,readRunEvent} from '../recordReader'
import type {DurableDispatch,DurableScope} from '../storage/writer'
import type {HistoryMetadataEntry} from './types'
import {readHistoryHead,readHistoryEntry,readMetadataEntry,readRetainedRunHistory,copyHistoryEnvelope,copyCorpusManifestEnvelope} from './view'
import {readHistoryObservation,readRunCapture} from './metadata'
import type {RunHistoryEntry,RunHistoryHead} from '../storage/history'
import {ReplayUnavailable,consumeSealedCapture,type CaptureSeal} from './captureCollector'
import {storageId,StorageConflict} from '../storage/common'
import type {HistoryRunStore,HistoryMetadataCommit} from '../storage/history'
import type {PolicyMembers} from '../policy'
import {OutputGuard} from '../outputGuard'
import {serializeCorpus,type CorpusManifest,type CorpusFiles,type CorpusSink} from '../corpus'
import {HistoryChanged,HistoryInputInvalid,historyText,HISTORY_TOTAL_BYTES,HISTORY_OBJECT_BYTES,chunkHistoryValue,historyObject,historyJSONDigest,historyJSONBytes,historyBytesDigest,readCanonicalHistoryJSON,readHistoryChunkManifest,readHistoryRequestJSON,validHistoryRef,type HistoryObjectRef} from './codec'
export {ReplayUnavailable} from './captureCollector'
/** Read-only evidence. It never enrolls a writer, acknowledges a claim or grants replay execution. */
export interface CaptureReadView {readonly head:RunHistoryHead;readonly commits:RunHistoryEntry[];readonly metadata:HistoryMetadataEntry[];readObject(ref:HistoryObjectRef,kind:ArchiveKind):Uint8Array}
const fail:()=>never=()=>{throw new ReplayUnavailable()}
const same=(a:unknown,b:unknown)=>canonicalJSON(a)===canonicalJSON(b)
const object=(v:unknown):v is Record<string,unknown>=>v!==null&&typeof v==='object'&&!Array.isArray(v)
const stringMap=(value:unknown):value is Record<string,string>=>object(value)&&Object.values(value).every(item=>typeof item==='string')
const closed=(v:unknown,keys:string[],optional:string[]=[]):v is Record<string,unknown>=>object(v)&&keys.every(k=>Object.hasOwn(v,k))&&Object.keys(v).every(k=>keys.includes(k)||optional.includes(k))
const displayMatches=archiveDisplayMatches
const captureCheckpointMatches=archiveCheckpointMatches
function agentBody(body:string){
 const value=readHistoryRequestJSON(body)
 if(!closed(value,['steps'])||!Array.isArray(value.steps))return fail()
 for(const step of value.steps){if(closed(step,['event'])){const e=step.event;if(!(closed(e,['type','id','text'],['phase'])&&['text','message'].includes(String(e.type))&&typeof e.id==='string'&&typeof e.text==='string'&&(e.phase===undefined||['commentary','final'].includes(String(e.phase)))||closed(e,['type','attempt'])&&e.type==='retry'&&Number.isSafeInteger(e.attempt)&&Number(e.attempt)>0))fail()}else if(closed(step,['tool'])){const t=step.tool;if(!closed(t,['id','name','arguments'])||typeof t.id!=='string'||typeof t.name!=='string'||!object(t.arguments))fail()}else fail()}
 return value.steps.filter((s:unknown)=>closed(s,['tool'])).map((s:any)=>s.tool as {id:string;name:string;arguments:Record<string,unknown>})
}
/** Validate actual portable corpus files against a pinned native journal. Missing evidence is unavailable.
 * Same-writer live capture issuance and automatic root finalization are deliberately stage2 hooks. */
export function readCapturedCorpus(view:CaptureReadView):CorpusEntry {return capturedCorpus(view,false)}
interface CaptureDisplayPort {read(ref:HistoryObjectRef,kind:ArchiveKind):{bytes:Uint8Array;exact:boolean};metadata:HistoryMetadataEntry[]}
function capturedCorpus(view:CaptureReadView,original:boolean,display=false,port?:CaptureDisplayPort):CorpusEntry {
 try{
  const matches=original?same:displayMatches
  const head=readHistoryHead(view.head),commits=view.commits.map(readHistoryEntry),metadata=port?structuredClone(port.metadata):view.metadata.map(readMetadataEntry)
  let bytes=0;const cache=new Map<string,Uint8Array>(),counted=new Set<string>()
  const changed=new Set<string>()
  const get=(ref:HistoryObjectRef,kind:ArchiveKind)=>{if(!validHistoryRef(ref))return fail();const key=ref.digest+kind,prior=cache.get(key);if(prior){if(!changed.has(key)&&prior.length!==ref.bytes)return fail();return prior}if(!counted.has(ref.digest)&&(bytes+=ref.bytes)>HISTORY_TOTAL_BYTES)return fail();const supplied=port?.read(ref,kind),b=new Uint8Array(supplied?.bytes??view.readObject(ref,kind));if(supplied&&!supplied.exact)changed.add(key);if(b.length>HISTORY_OBJECT_BYTES||!changed.has(key)&&(b.length!==ref.bytes||historyBytesDigest(b)!==ref.digest))return fail();counted.add(ref.digest);cache.set(key,b);return b}
  const json=(ref:HistoryObjectRef,kind:ArchiveKind='checkpoint')=>readCanonicalHistoryJSON(get(ref,kind),HISTORY_OBJECT_BYTES,'finite')
  const payload=(ref:HistoryObjectRef,prefix:'request'|'corpus'='corpus')=>{const m=readHistoryChunkManifest(get(ref,`${prefix}-manifest`)),parts=m.parts.map(part=>get(part,`${prefix}-chunk`)),size=parts.reduce((n,b)=>n+b.length,0);if(size>HISTORY_TOTAL_BYTES)fail();const b=new Uint8Array(size);let at=0;for(const raw of parts){b.set(raw,at);at+=raw.length}if(!m.parts.some(part=>changed.has(part.digest+`${prefix}-chunk`))&&(at!==m.bytes||historyBytesDigest(b)!==m.digest))fail();return prefix==='request'?readHistoryRequestJSON(historyText(b)):readCanonicalHistoryJSON(b,HISTORY_TOTAL_BYTES,'finite')}
  const checkpointDocument=(v:unknown,derived=false)=>{if(!display||!derived)return readExecution(v);checkRecord('checkpoint',v);return v as NonNullable<ReturnType<typeof readExecution>>}
  if(!head.checkpoint||commits.length!==head.commitCount)return fail()
  const checkpoint=checkpointDocument(json(head.checkpoint),changed.has(head.checkpoint.digest+'checkpoint'));if(!checkpoint||checkpoint.id!==head.runId||!['complete','stopped'].includes(checkpoint.status))return fail()
  let previous:string|null=null,eventAt=0
  const claims=new Map<string,{claim:DurableDispatch;first:number}>(),expectations:{identity:Omit<import('./types').HistoryCaptureIdentity,'captureIndex'>;sequence:number;nativeBindings?:HistoryObjectRef}[]=[],requests:{scope:DurableScope;dispatchId:string;digest:string;source:string;tool:string;sequence:number}[]=[],events:{scope:DurableScope;event:import('../engine').AssistantEvent;sequence:number}[]=[]
  for(const [index,c] of commits.entries()){if(c.sequence!==index+1||c.previousDigest!==previous||c.eventStart!==eventAt+1)fail();previous=c.digest;eventAt+=c.eventCount
   if(c.checkpoint){const cp=checkpointDocument(json(c.checkpoint),changed.has(c.checkpoint.digest+'checkpoint'));if(!cp||cp.id!==head.runId)fail();for(const claim of cp.durable?.dispatches??[]){const old=claims.get(claim.id);if(old&&!same([old.claim.scope,old.claim.kind,old.claim.source,old.claim.tool,old.claim.request,old.claim.requestDigest],[claim.scope,claim.kind,claim.source,claim.tool,claim.request,claim.requestDigest]))fail();claims.set(claim.id,{claim,first:old?.first??c.sequence})}}
   for(const ref of c.observations){const o=readHistoryObservation(get(ref,'observation'));if(o.kind==='capture-request'){const {version:_,kind:__,captureKind,nativeBindings,...identity}=o;expectations.push({identity:{...identity,kind:captureKind},sequence:c.sequence,nativeBindings})}else if(o.kind==='request'){const m=readHistoryChunkManifest(get(o.request,'request-manifest'));payload(o.request,'request');requests.push({scope:o.scope,dispatchId:o.dispatchId,digest:m.digest,source:o.source,tool:o.tool,sequence:c.sequence})}}
   for(const ref of c.events){const v=json(ref,'event');if(!closed(v,['version','scope','event'])||v.version!==1)fail();events.push({scope:v.scope as DurableScope,event:readRunEvent(v.event),sequence:c.sequence})}
  }
  const last=commits.at(-1);if(!last||previous!==head.root||eventAt!==head.eventCount||last.executionRevision!==head.executionRevision||!same(last.checkpoint,head.checkpoint))fail()
  if(metadata.length!==(head.metadata?.count??0))fail();let priorMetadata:string|null=null
  for(const [index,row]of metadata.entries()){if(row.sequence!==index+1||row.previousDigest!==priorMetadata||row.commitSequence>commits.length||commits[row.commitSequence-1]!.executionRevision!==row.executionRevision)fail();priorMetadata=row.digest}
  if(priorMetadata!==(head.metadata?.root??null))fail()
  const row=metadata.filter(r=>r.record.kind==='capture').at(-1);if(!row||row.record.kind!=='capture'||row.executionRevision!==head.executionRevision)fail()
  const capture=readRunCapture(get(row.record.capture,'capture'));if(capture.runId!==head.runId||capture.executionRevision!==head.executionRevision||!same(capture.checkpoint,head.checkpoint))fail()
  const eligible=[...claims.values()].filter(v=>v.claim.kind==='model'||v.claim.kind==='agent'),seen=new Set<string>()
  if(capture.requests.length!==eligible.length||expectations.length!==eligible.length)fail()
  for(const [i,e]of expectations.entries()){const id=e.identity,held=claims.get(id.dispatchId),r=capture.requests[i];if(!held||seen.has(id.dispatchId)||held.first!==e.sequence||held.claim.state!=='result'||!held.claim.read||!same(r,{...id,captureIndex:i})||!same([id.scope,id.kind,id.source,id.tool,id.requestDigest],[held.claim.scope,held.claim.kind,held.claim.source,held.claim.tool,held.claim.requestDigest])||requests.filter(q=>q.dispatchId===id.dispatchId&&same(q.scope,id.scope)&&q.digest===id.requestDigest&&q.source===id.source&&q.tool===id.tool&&q.sequence===e.sequence).length!==1)fail();seen.add(id.dispatchId)}
  const files=new Map(capture.files.map(f=>[f.name,payload(f.manifest)])),manifest=files.get('manifest.json'),rawRequests=files.get('requests.json'),rawTools=files.get('tool-results.json'),rawEvents=files.get('events.json'),rawCheckpoint=files.get('checkpoint.json'),expected=files.get('expected.json')
  if(argumentProblem(manifestSchema,manifest)!==undefined||!Array.isArray(rawRequests)||rawRequests.length!==expectations.length||!Array.isArray(rawTools)||!Array.isArray(rawEvents)||!checkpointDocument(rawCheckpoint,display)||!(original?same(checkpoint,rawCheckpoint):captureCheckpointMatches(checkpoint,rawCheckpoint)))fail()
  const nativeTools=new Map<string,{name:string;arguments:Record<string,unknown>}>()
  for(const [i,r]of rawRequests.entries()){if(!closed(r,['suffix','request','response'])||typeof r.suffix!=='string'||!object(r.request)||!closed(r.response,['status','headers','body'])||!Number.isSafeInteger(r.response.status)||Number(r.response.status)<100||Number(r.response.status)>599||!stringMap(r.response.headers)||typeof r.response.body!=='string')fail();const c=claims.get(expectations[i]!.identity.dispatchId)!.claim,request=JSON.parse(c.request);if(c.kind==='agent'){if(r.suffix!=='agent.run'||!matches(request,r.request))fail();const aliases=nativeBindings(request,r.response.headers),source=expectations[i]!.nativeBindings;if(!source||!aliases||changed.has(source.digest+'evidence')||!same(aliases,json(source,'evidence')))fail();for(const tool of agentBody(r.response.body)){const key=canonicalJSON([expectations[i]!.identity.scope,tool.id]);if(nativeTools.has(key))fail();if(!aliases&&/^desk_(host|runtime)_/.test(tool.name))fail();if(aliases&&!Object.hasOwn(aliases,tool.name))fail();nativeTools.set(key,{...tool,name:aliases?.[tool.name]??tool.name})}}else{if(Object.hasOwn(r.response.headers,nativeBindingsHeader))fail();if(!closed(r.request,['body'],['method','headers'])||typeof r.request.body!=='string'||r.request.method!==undefined&&!['GET','POST'].includes(String(r.request.method))||r.request.headers!==undefined&&(!object(r.request.headers)||Object.values(r.request.headers).some(v=>typeof v!=='string'))||!matches(request,{suffix:r.suffix,...(r.request.method===undefined?{}:{method:r.request.method}),body:r.request.body}))fail()}}
  const rootEvents=events.filter(v=>v.scope.execution===head.runId&&v.scope.task===undefined&&v.scope.role==='author'&&!['end','recorded'].includes(v.event.type)&&(capture.eventProjection!=='transcript-v1'||v.event.type!=='execution_checkpoint')).map(v=>v.event)
  const cleanEvents=rawEvents.map(readRunEvent);if(!matches(rootEvents,cleanEvents)||!same(expected,expectedFromEvents(cleanEvents)))fail()
  const pending=new Map<string,Extract<import('../engine').AssistantEvent,{type:'tool_call'}>>(),tools:RecordedToolResult[]=[]
  for(const {scope,event}of events){if(event.type==='tool_call'){if(!event.callId)fail();const k=canonicalJSON([scope,event.callId]);if(pending.has(k)||!object(event.args))fail();const native=nativeTools.get(k);if(native){if(native.name!==event.name||!matches(native.arguments,event.args))fail();nativeTools.delete(k)}pending.set(k,event)}else if(event.type==='tool_result'){if(!event.callId)fail();const k=canonicalJSON([scope,event.callId]),call=pending.get(k);if(!call||call.name!==event.name)fail();pending.delete(k);tools.push({name:event.name,args:call.args as Record<string,unknown>,result:{content:[{type:'text',text:event.text}],isError:event.isError,...(event.structured===undefined?{}:{structuredContent:event.structured})}})}}
  if(pending.size||nativeTools.size||!matches(tools,rawTools))fail()
  return {manifest:manifest as CorpusEntry['manifest'],requests:rawRequests as unknown as RecordedRequest[],toolResults:rawTools as unknown as RecordedToolResult[],events:cleanEvents,checkpoint:rawCheckpoint,expected:expected as CorpusEntry['expected']}
 }catch{return fail()}
}
/** The view remains a display-only archive. Derived corpus bytes cannot be asserted to hash native source refs. */
export function readArchiveCapturedCorpus(view:import('./archive').ImportedRunHistory):CorpusEntry {
 try{const m=view.manifest,get=(ref:HistoryObjectRef,kind:ArchiveKind)=>{const d=view.readDisplay(ref,kind);if(d.representation.kind!=='exact')return fail();if(typeof d.document==='string')return Uint8Array.from(atob(d.document),c=>c.charCodeAt(0));return historyJSONBytes(d.document)};return readCapturedCorpus({head:view.head,commits:view.commits,metadata:m.metadata.map(ref=>readMetadataEntry(readCanonicalHistoryJSON(get(ref,'metadata'),HISTORY_OBJECT_BYTES,'finite'))),readObject:get})}catch{return fail()}
}

/** Explicit display projection for fresh corpus replay; source labels are not execution authenticity. */
export interface CorpusReplayView {checkpoint:CorpusCheckpointView;entry:CorpusEntry}
function replayView(entry:CorpusEntry,source:HistoryObjectRef):CorpusReplayView {
 const checkpoint=corpusCheckpointView(source,entry.checkpoint)
 return {checkpoint,entry:{...entry,checkpoint:structuredClone(checkpoint)}}
}
export function readCapturedCorpusView(view:CaptureReadView):CorpusReplayView {
 const entry=capturedCorpus(view,false,true);if(!view.head.checkpoint)return fail()
 return replayView(entry,view.head.checkpoint)
}
export function readArchiveCapturedCorpusView(view:ImportedRunHistory):CorpusReplayView {
 try{
 if(!(view instanceof ImportedRunHistory))return fail()
 const m=view.manifest,read=(ref:HistoryObjectRef,kind:ArchiveKind)=>{const d=view.readDisplay(ref,kind);return {bytes:kind.endsWith('-chunk')?(d.representation.kind==='exact'?Uint8Array.from(atob(d.document as string),c=>c.charCodeAt(0)):new TextEncoder().encode(d.document as string)):historyJSONBytes(d.document),exact:d.representation.kind==='exact'}}
 const metadata=m.metadata.map(ref=>view.readDisplay(ref,'metadata').document as HistoryMetadataEntry)
 const port:CaptureDisplayPort={read,metadata},entry=capturedCorpus({head:view.head,commits:view.commits,metadata,readObject:(ref,kind)=>read(ref,kind).bytes},false,true,port)
 if(!m.source.checkpoint)return fail();return replayView(entry,m.source.checkpoint)
 }catch{return fail()}
}
/** Stage2 publication must use the original metadata CAS; this is only its host outcome vocabulary. */
export type CapturePublicationResult={capture:'committed';delivery:'not-requested'|'delivered'|'unknown'}|{capture:'unknown';delivery:'not-attempted'}

/** An opaque preparation, never a caller-authored checkpoint/commit descriptor. */
const publicationBrand:unique symbol=Symbol('private capture publication')
export type PreparedCapturePublication={readonly [publicationBrand]:true}
interface CapturePublication {store:HistoryRunStore;source:RunHistoryHead;commit:HistoryMetadataCommit;capture:HistoryObjectRef;objects:Map<string,Uint8Array>;files:CorpusFiles;id:string;delivery?:Promise<'delivered'|'unknown'>}
const publications=new WeakMap<object,CapturePublication>()
export interface CapturePublicationOptions {commitId:string;manifest:CorpusManifest;members?:PolicyMembers;signal?:AbortSignal}
export type PublishedCapture=CapturePublicationResult&{head?:RunHistoryHead;captureRef?:HistoryObjectRef}
function captureEvents(view:CaptureReadView){return view.commits.flatMap(c=>c.events.map(ref=>{const value=readCanonicalHistoryJSON(view.readObject(ref,'event'),HISTORY_OBJECT_BYTES,'finite');if(!closed(value,['version','scope','event'])||value.version!==1)return fail();return {scope:value.scope as DurableScope,event:readRunEvent(value.event)}}))}
function captureTools(events:ReturnType<typeof captureEvents>):RecordedToolResult[]{
 const calls=new Map<string,Extract<import('../engine').AssistantEvent,{type:'tool_call'}>>(),out:RecordedToolResult[]=[]
 for(const {scope,event}of events){if(event.type==='tool_call'){const key=canonicalJSON([scope,event.callId]);if(!event.callId||calls.has(key)||!object(event.args))return fail();calls.set(key,event)}else if(event.type==='tool_result'){const key=canonicalJSON([scope,event.callId]),call=calls.get(key);if(!call||call.name!==event.name)return fail();calls.delete(key);out.push({name:event.name,args:call.args as Record<string,unknown>,result:{content:[{type:'text',text:event.text}],isError:event.isError,...(event.structured===undefined?{}:{structuredContent:event.structured})}})}}
 if(calls.size)return fail();return out
}
async function capturePublicationFiles(view:import('./view').RetainedRunHistory,files:CorpusFiles,identities:import('./types').RunCapture['requests'],original:boolean){
 const source=view.head,objects=new Map<string,Uint8Array>(),fileRefs:import('./types').RunCapture['files']=[]
 for(const name of Object.keys(files).sort() as (keyof CorpusFiles)[]){const {manifest,objects:parts}=await chunkHistoryValue(new TextEncoder().encode(files[name]));for(const o of [...parts,manifest])objects.set(o.digest,o.value);fileRefs.push({name,manifest:{digest:manifest.digest,bytes:manifest.bytes}})}
 const capture=readRunCapture(historyJSONBytes({version:1,eventProjection:'transcript-v1',runId:source.runId,executionRevision:source.executionRevision,checkpoint:source.checkpoint,requests:identities,files:fileRefs,redacted:true})),captureObject=await historyObject(historyJSONBytes(capture)),captureRef={digest:captureObject.digest,bytes:captureObject.bytes};objects.set(captureObject.digest,captureObject.value)
 // Synthetic metadata is private preflight material only; the store issues the real receipt later.
 const count=source.metadata?.count??0,body={version:1 as const,executionRevision:source.executionRevision,commitSequence:source.commitCount,sequence:count+1,recordedAt:new Date().toISOString(),previousDigest:source.metadata?.root??null,record:{version:1 as const,kind:'capture' as const,capture:captureRef}},row={...body,digest:historyJSONDigest(body)},head={...source,revision:'capture-preflight',metadata:{version:1 as const,runId:source.runId,revision:'capture-preflight',executionRevision:source.executionRevision,count:count+1,root:row.digest}}
 const checked=capturedCorpus({head,commits:view.commits,metadata:[...view.metadata,row],readObject:(ref,_kind)=>objects.get(ref.digest)?.slice()??view.readObject(ref)},original,!original)
 if(!same(serializeCorpus(checked),files))return fail()
 return {objects,captureRef,checked}
}
/** Private publisher entry: only an enrolled same-store opaque seal can authorize preparation. */
export async function prepareCapturePublication(store:HistoryRunStore,seal:CaptureSeal,options:CapturePublicationOptions):Promise<PreparedCapturePublication>{
 try{
 const material=consumeSealedCapture(seal,store)
 if(!storageId(options.commitId))return fail()
 const view=await readRetainedRunHistory(store,material.terminal.runId,options.signal),source=view.head
 if(!source.checkpoint||source.executionRevision!==material.terminal.executionRevision||!same(source.checkpoint,material.terminal.checkpoint)||view.metadata.some(row=>row.record.kind==='capture'&&row.executionRevision===source.executionRevision))return fail()
 const checkpoint=readExecution(readCanonicalHistoryJSON(view.readObject(source.checkpoint),HISTORY_OBJECT_BYTES,'finite'))
 if(!checkpoint||checkpoint.status!==material.terminal.status||!['complete','stopped'].includes(checkpoint.status))return fail()
 for(const {admission:a}of material.rows){const commit=view.commits[a.sequence-1];if(!commit||commit.commitId!==a.commitId||!same(commit.observations[a.ordinal],a.expectation))return fail();const observation=readHistoryObservation(view.readObject(a.expectation));if(observation.kind!=='capture-request')return fail();const {version:_,kind:__,captureKind,nativeBindings:_nativeBindings,...identity}=observation;if(!same({...identity,kind:captureKind},a.identity))return fail();const cp=commit.checkpoint&&readExecution(readCanonicalHistoryJSON(view.readObject(commit.checkpoint),HISTORY_OBJECT_BYTES,'finite')),claim=cp?.durable?.dispatches.find(claim=>claim.id===a.identity.dispatchId);if(!claim||claim.request!==a.request)return fail()}
 const events=captureEvents(view),visible=events.filter(v=>v.scope.execution===source.runId&&v.scope.task===undefined&&v.scope.role==='author'&&!['end','recorded','execution_checkpoint'].includes(v.event.type)).map(v=>v.event)
 const raw:CorpusEntry={manifest:copyCorpusManifestEnvelope(options.manifest) as unknown as CorpusManifest,requests:material.rows.map(row=>row.frame) as RecordedRequest[],toolResults:captureTools(events),events:visible,checkpoint,expected:expectedFromEvents(visible)}
 const identities=material.rows.map((row,captureIndex)=>({...row.admission.identity,captureIndex}))
 await capturePublicationFiles(view,serializeCorpus(raw),identities,true)
 const guard=new OutputGuard({...options.members,outputGuard:{value:'redact'} as never},()=>({}),()=>{}),clean=guard.value(raw,'message',false)
 if(!displayMatches(raw,clean)||!captureCheckpointMatches(checkpoint,clean.checkpoint))return fail()
 const files=serializeCorpus(clean),{objects,captureRef,checked}=await capturePublicationFiles(view,files,identities,false)
 const latest=await store.readHistoryHead(source.runId,options.signal);if(!latest||!same(readHistoryHead(latest),source))throw new HistoryChanged()
 let batch:import('./codec').HistoryObject[]=[],bytes=0,total=0
 for(const [digest,value]of objects){total+=value.length;if(total>HISTORY_TOTAL_BYTES)return fail();if(batch.length>=256||bytes+value.length>8*1024*1024){await store.stageHistoryObjects({kind:'run',id:source.runId},batch,options.signal);batch=[];bytes=0}batch.push({digest,bytes:value.length,value:value.slice()});bytes+=value.length}
 if(batch.length)await store.stageHistoryObjects({kind:'run',id:source.runId},batch,options.signal)
 const prepared=Object.freeze({}) as PreparedCapturePublication
 publications.set(prepared,{store,source,commit:{runId:source.runId,expectedHistoryRevision:source.revision,expectedMetadataRevision:source.metadata?.revision??null,commitId:options.commitId,record:{version:1,kind:'capture',capture:captureRef}},capture:captureRef,objects,files,id:checked.manifest.id});return prepared
 }catch(error){if(error instanceof HistoryChanged)throw error;return fail()}
}
/** Explicit re-use of the same private preparation relies on the store's existing idempotent CAS.
 * Unknown acknowledgements never trigger an automatic retry or delivery. A sink is attempted once. */
export async function commitCapturePublication(store:HistoryRunStore,prepared:PreparedCapturePublication,options:{sink?:CorpusSink;signal?:AbortSignal}={}):Promise<PublishedCapture>{
 const held=prepared&&publications.get(prepared);if(!held||held.store!==store)return fail()
 const unknown=():PublishedCapture=>({capture:'unknown',delivery:'not-attempted'})
 let receipt:unknown
 try{receipt=await store.commitHistoryMetadata(copyHistoryEnvelope(held.commit) as HistoryMetadataCommit,options.signal)}catch(error){if(error instanceof StorageConflict||error instanceof HistoryInputInvalid)throw error;return unknown()}
 let head:RunHistoryHead
 try{
 head=readHistoryHead(receipt);const {revision:_,metadata:__,...before}=held.source,{revision:___,metadata:____,...after}=head,count=held.source.metadata?.count??0
 if(!same(before,after)||head.revision===held.source.revision||!head.metadata||head.metadata.count!==count+1||head.metadata.revision===held.source.metadata?.revision)return unknown()
 const page=copyHistoryEnvelope(await store.readHistoryMetadataPage(head.runId,head.revision,count,1,options.signal)) as import('./types').HistoryMetadataPage
 if(Object.keys(page).sort().join(',')!=='afterSequence,hasMore,items,nextSequence,revision,total'||page.revision!==head.revision||page.afterSequence!==count||page.items.length!==1||page.nextSequence!==count+1||page.hasMore||page.total!==head.metadata.count)return unknown()
 const row=readMetadataEntry(page.items[0]);if(row.sequence!==count+1||row.previousDigest!==(held.source.metadata?.root??null)||row.digest!==head.metadata.root||row.executionRevision!==held.source.executionRevision||row.commitSequence!==held.source.commitCount||!same(row.record,held.commit.record))return unknown()
 for(const [digest,value]of held.objects){const object=await store.readHistoryObject({kind:'run',id:head.runId},head.revision,digest,0,HISTORY_OBJECT_BYTES,options.signal);if(Object.keys(object).sort().join(',')!=='digest,hasMore,nextOffset,offset,total,value'||object.digest!==digest||object.total!==value.length||object.offset!==0||object.nextOffset!==value.length||object.hasMore||object.value.length!==value.length||historyBytesDigest(object.value)!==digest||!same(Array.from(object.value),Array.from(value)))return unknown()}
 }catch{return unknown()}
 let delivery:'not-requested'|'delivered'|'unknown'='not-requested'
 if(options.sink){held.delivery??=Promise.resolve().then(async()=>{try{await options.sink!.write(held.id,{...held.files});return 'delivered' as const}catch{return 'unknown' as const}});delivery=await held.delivery}
 return {capture:'committed',delivery,head,captureRef:{...held.capture}}
}
