import {canonicalJSON} from '../context/chat'
import {validateHistoryRelations} from './relations'
import {projectHistoryRunReport} from './report'
import {readCorpusCandidate} from '../corpusCandidate'
import {readChatContextSnapshot} from '../context/chatSnapshot'
import {readExecution} from '../execution/state'
import {OutputGuard} from '../outputGuard'
import type {PolicyMembers} from '../policy'
import {checkRecord} from '../recordReader'
import {RUN_REPORT_SOURCES,projectRepeatTools} from '../runReport'
import {readRunReport,readRunReportDisplay} from '../runReportReader'
import {readStoredRunEvent,type DurableScope} from '../storage/writer'
import type {HistoryRunStore,RunHistoryEntry,RunHistoryHead} from '../storage/history'
import {usageTotals,type UsageBudgetJournal} from '../storage/usageJournal'
import {storageId} from '../storage/common'
import {readHistoryObservation,readRunCapture} from './metadata'
import {readRetainedRunHistory,readHistoryHead,readHistoryEntry,readMetadataEntry} from './view'
import type {HistoryMetadataEntry} from './types'
import {HistoryInputInvalid,HistoryChanged,HISTORY_OBJECT_BYTES,HISTORY_TOTAL_BYTES,historyJSONBytes,historyJSONDigest,historyBytesDigest,historyDigestPattern,validHistoryRef,readCanonicalHistoryJSON,readHistorySourceJSON,readHistoryRequestJSON,readHistoryChunkManifest,historyText,copyHistoryBytes,type HistoryObjectRef} from './codec'

export type ArchiveKind='checkpoint'|'event'|'observation'|'report'|'metadata'|'candidate'|'capture'|'evidence'|'source-manifest'|'source-chunk'|'request-manifest'|'request-chunk'|'chat-manifest'|'chat-chunk'|'corpus-manifest'|'corpus-chunk'
export type ArchiveRepresentation={kind:'exact'}|{kind:'redacted-view';source:HistoryObjectRef;redactions:string[]}
export interface ArchiveMapping{source:HistoryObjectRef;exported:HistoryObjectRef;kind:ArchiveKind;representation:ArchiveRepresentation}
export interface ArchiveMember extends HistoryObjectRef{source:HistoryObjectRef;kind:ArchiveKind;representation:ArchiveRepresentation}
export interface RunArchiveManifest{version:1;runId:string;source:RunHistoryHead;coverage:RunHistoryHead['coverage'];commits:RunHistoryEntry[];metadata:HistoryObjectRef[];mappings:ArchiveMapping[];members:ArchiveMember[];report:HistoryObjectRef;digest:string}
export interface RunArchive{version:1;manifest:RunArchiveManifest;objects:{digest:string;bytes:string}[]}
export interface RedactedArchiveView{version:1;sourceKind:ArchiveKind;sourceVersion:number;source:HistoryObjectRef;redactions:string[];document:unknown}
export class HistoryIncomplete extends Error{constructor(){super('HistoryIncomplete: complete retained history is required.');this.name='HistoryIncomplete'}}
const base64=(bytes:Uint8Array)=>{let text='';for(let i=0;i<bytes.length;i+=32768)text+=String.fromCharCode(...bytes.subarray(i,i+32768));return btoa(text)}
const unbase64=(text:string)=>Uint8Array.from(atob(text),c=>c.charCodeAt(0))
function invalid():never{throw new HistoryInputInvalid()}
const object=(v:unknown):v is Record<string,unknown>=>v!==null&&typeof v==='object'&&!Array.isArray(v)
const closed=(v:unknown,keys:string[]):v is Record<string,unknown>=>object(v)&&Object.keys(v).length===keys.length&&keys.every(k=>Object.hasOwn(v,k))
const same=(a:unknown,b:unknown)=>canonicalJSON(a)===canonicalJSON(b)
const kinds:ArchiveKind[]=['checkpoint','event','observation','report','metadata','candidate','capture','evidence','source-manifest','source-chunk','request-manifest','request-chunk','chat-manifest','chat-chunk','corpus-manifest','corpus-chunk']
const escape=(key:string)=>key.replace(/~/g,'~0').replace(/\//g,'~1')
const protectedKey=/^(?:id|.*Id|scope|execution|task|role|source|sourceView|tool|name|boundary|revision|.*Revision|digest|.*Digest|sha256|requestKey|type|kind|version|schema|retainUntil|recordedAt|previous|previousDigest|eventSequence|sequence|commitSequence)$/
/** Only native checkpoint/report/event paths add task identity locks; arbitrary source JSON is unaffected. */
function taskIdentity(kind:ArchiveKind,value:unknown){
 const out:Record<string,unknown>={}
 const dependency=(step:unknown,path:string)=>{if(object(step))out[path+'/dependsOn']=step.dependsOn??null}
 const steps=(rows:unknown,path:string)=>{if(Array.isArray(rows))rows.forEach((step,i)=>dependency(step,`${path}/${i}`))}
 const checkpoint=(v:unknown,path:string):void=>{if(!object(v))return;steps(v.plan,path+'/plan');const state=v.planState
  if(object(state)){out[path+'/planState/taskSchedule']=state.taskSchedule??null;out[path+'/planState/taskEnvelope']=state.taskEnvelope??null
   if(object(state.pending)&&object(state.pending.candidate))checkpoint(state.pending.candidate,path+'/planState/pending/candidate')
   if(Array.isArray(state.history))state.history.forEach((row,i)=>{if(object(row)&&Array.isArray(row.diff))row.diff.forEach((change,j)=>{if(object(change))for(const side of ['before','after']){const step=change[side];dependency(step,`${path}/planState/history/${i}/diff/${j}/${side}`)}})})
  }
  if(object(v.durable)&&Array.isArray(v.durable.children))v.durable.children.forEach((child,i)=>{if(object(child))checkpoint(child.checkpoint,`${path}/durable/children/${i}/checkpoint`)})
 }
 if(kind==='checkpoint'||kind==='report')checkpoint(value,'')
 else if(kind==='event'&&object(value)&&object(value.event))checkpoint(value.event.checkpoint,'/event/checkpoint')
 return out
}
function taskIdentityEqual(kind:ArchiveKind,a:unknown,b:unknown){if(!same(taskIdentity(kind,a),taskIdentity(kind,b)))invalid()}
function taskRedactionsAllowed(kind:ArchiveKind,value:unknown,paths:string[]){return !paths.some(path=>Object.keys(taskIdentity(kind,value)).some(identity=>path===identity||path.startsWith(identity+'/')))}
function differences(a:unknown,b:unknown,path='',locked=false):string[]{
 if(same(a,b))return []
 if(typeof a==='string'&&typeof b==='string'&&!locked&&/\[redacted: [a-z][a-z0-9-]{0,63}\]/.test(b))return [path]
 if(Array.isArray(a)&&Array.isArray(b)&&a.length===b.length)return a.flatMap((v,i)=>differences(v,b[i],`${path}/${i}`,locked))
 if(object(a)&&object(b)&&same(Object.keys(a).sort(),Object.keys(b).sort()))return Object.keys(a).sort().flatMap(key=>differences(a[key],b[key],`${path}/${escape(key)}`,locked||protectedKey.test(key)))
 return invalid()
}
/** Internal display relationship comparison shared with capture validation. */
export function archiveDisplayMatches(a:unknown,b:unknown):boolean {try{differences(a,b);return true}catch{return false}}
export function archiveCheckpointMatches(a:unknown,b:unknown):boolean {return archiveDisplayMatches(a,b)&&same(taskIdentity('checkpoint',a),taskIdentity('checkpoint',b))}
function declarations(value:unknown,paths:unknown):paths is string[]{
 if(!Array.isArray(paths)||paths.length<1||paths.length>10000||!paths.every(p=>typeof p==='string'&&p.length<=2000)||!same([...paths].sort(),paths)||new Set(paths).size!==paths.length)return false
 for(const path of paths){let v=value;for(const bit of path.split('/').slice(1)){if(/~(?![01])/.test(bit))return false;const key=bit.replace(/~1/g,'/').replace(/~0/g,'~');if(protectedKey.test(key)||!(Array.isArray(v)||object(v))||!Object.hasOwn(v,key))return false;v=(v as Record<string,unknown>)[key]}if(path!==''&&!path.startsWith('/')||typeof v!=='string'||!/\[redacted: [a-z][a-z0-9-]{0,63}\]/.test(v))return false}
 return true
}
function scopeValid(v:unknown):v is DurableScope{return object(v)&&Object.keys(v).every(k=>['execution','role','task'].includes(k))&&storageId(v.execution)&&storageId(v.role)&&(v.task===undefined||storageId(v.task))}
function validateDocument(kind:ArchiveKind,document:unknown,derived:boolean){
 const bytes=historyJSONBytes(document)
 if(kind==='checkpoint'){if(derived){checkRecord('checkpoint',document);if(!object(document)||!storageId(document.id)||![1,2,3].includes(Number(document.version)))invalid()}else if(!readExecution(document))invalid()}
 else if(kind==='event'){if(!closed(document,['version','scope','event'])||document.version!==1||!scopeValid(document.scope))invalid();if(derived)checkRecord('event',document.event);else readStoredRunEvent(bytes)}
 else if(kind==='report'){if(derived)readRunReportDisplay(document);else readRunReport(document)}
 else if(kind==='metadata'){if(derived){if(!object(document))invalid();const {digest:_,...body}=document;readMetadataEntry({...body,digest:historyJSONDigest(body)})}else readMetadataEntry(document)}
 else if(kind==='observation')readHistoryObservation(bytes)
 else if(kind==='candidate')readCorpusCandidate(bytes)
 else if(kind==='capture')readRunCapture(bytes)
 else if(kind.endsWith('-manifest'))readHistoryChunkManifest(bytes)
 else if(kind.endsWith('-chunk')){if(typeof document!=='string')invalid()}
 else if(kind!=='evidence')invalid()
}
function sourceVersion(kind:ArchiveKind,v:unknown){return ['checkpoint','event','report'].includes(kind)&&object(v)&&Number.isSafeInteger(v.version)?Number(v.version):1}
function unwrap(mapping:ArchiveMapping,bytes:Uint8Array){
 if(mapping.representation.kind==='exact')return mapping.kind.endsWith('-chunk')?base64(bytes):readCanonicalHistoryJSON(bytes,HISTORY_OBJECT_BYTES,'finite')
 const v=readCanonicalHistoryJSON(bytes,HISTORY_OBJECT_BYTES,'finite')
 if(!closed(v,['version','sourceKind','sourceVersion','source','redactions','document'])||v.version!==1||v.sourceKind!==mapping.kind||!Number.isSafeInteger(v.sourceVersion)||Number(v.sourceVersion)<0||!same(v.source,mapping.source)||!same(v.redactions,mapping.representation.redactions)||!(mapping.kind.endsWith('-chunk')?same(v.redactions,[''])&&typeof v.document==='string':declarations(v.document,v.redactions))||v.sourceVersion!==sourceVersion(mapping.kind,v.document)||!taskRedactionsAllowed(mapping.kind,v.document,v.redactions as string[]))invalid()
 return v.document
}
const roleKey=(digest:string,kind:ArchiveKind)=>digest+'|'+kind
const token=Symbol('validated archive')
/** Detached display data only. Source digests label historical bytes; derived bytes have their own digest. */
export class ImportedRunHistory{
 #manifest:RunArchiveManifest;#objects:Map<string,Uint8Array>;#mappings:Map<string,ArchiveMapping>;#bytes:Uint8Array
 constructor(key:symbol,manifest:RunArchiveManifest,objects:Map<string,Uint8Array>,mappings:Map<string,ArchiveMapping>,bytes:Uint8Array){if(key!==token)invalid();this.#manifest=manifest;this.#objects=objects;this.#mappings=mappings;this.#bytes=bytes;Object.freeze(this)}
 get head(){return structuredClone(this.#manifest.source)}get commits(){return structuredClone(this.#manifest.commits)}get manifest(){return structuredClone(this.#manifest)}
 canonicalBytes(){return new Uint8Array(this.#bytes)}
 readDisplay(source:HistoryObjectRef,kind?:ArchiveKind){const matches=kind===undefined?[...this.#mappings.values()].filter(m=>m.source.digest===source.digest):[];const mapping=kind===undefined?matches.length===1?matches[0]:undefined:this.#mappings.get(roleKey(source.digest,kind));if(!mapping||!same(mapping.source,source))return invalid();return {document:structuredClone(unwrap(mapping,this.#objects.get(mapping.exported.digest)!)),representation:structuredClone(mapping.representation),source:{...source},exported:{...mapping.exported}}}
 readEventDisplay(source:HistoryObjectRef){const value=this.readDisplay(source,'event');return {...value,...value.document as {scope:DurableScope;event:Record<string,unknown>}}}
}
/** Always constructs a detached guard. A caller cannot pass the live run's guard or counters. */
export async function exportRunArchive(store:HistoryRunStore,runId:string,options:{members?:PolicyMembers;requireComplete?:boolean;signal?:AbortSignal}={}):Promise<{bytes:Uint8Array;digest:string;manifest:RunArchiveManifest}>{
 const view=await readRetainedRunHistory(store,runId,options.signal),head=view.head
 if(options.requireComplete&&String(head.coverage)!=='complete')throw new HistoryIncomplete()
 const guard=new OutputGuard({...options.members,outputGuard:{value:'redact'} as never},()=>({}),()=>{}),sources=new Map<string,{source:HistoryObjectRef;kind:ArchiveKind;bytes:Uint8Array;document:unknown;clean?:unknown}>()
 const add=(source:HistoryObjectRef,kind:ArchiveKind,bytes=view.readObject(source),document:unknown=kind.endsWith('-chunk')?base64(bytes):readCanonicalHistoryJSON(bytes,HISTORY_OBJECT_BYTES,'finite'))=>{
  if(!validHistoryRef(source)||historyBytesDigest(bytes)!==source.digest||bytes.length!==source.bytes)invalid();validateDocument(kind,document,false)
  const prior=sources.get(roleKey(source.digest,kind));if(prior){if(!same(prior.source,source)||!same(prior.document,document))invalid();return prior}const row:{source:HistoryObjectRef;kind:ArchiveKind;bytes:Uint8Array;document:unknown;clean?:unknown}={source:{...source},kind,bytes,document};sources.set(roleKey(source.digest,kind),row);return row
 }
 const payload=(ref:HistoryObjectRef,prefix:'source'|'request'|'chat'|'corpus')=>{
  const manifest=readHistoryChunkManifest(view.readObject(ref));add(ref,`${prefix}-manifest`)
  const parts=manifest.parts.map(part=>view.readObject(part)),full=new Uint8Array(manifest.bytes);let at=0;for(const part of parts){full.set(part,at);at+=part.length}if(historyBytesDigest(full)!==manifest.digest)invalid()
  const text=historyText(full),document=prefix==='request'?readHistoryRequestJSON(text):prefix==='source'?readHistorySourceJSON(text):readCanonicalHistoryJSON(full,HISTORY_TOTAL_BYTES,'finite'),clean=guard.value(document,'tool argument',false),changed=!same(document,clean)
  if(!changed){for(const [i,part]of manifest.parts.entries())add(part,`${prefix}-chunk`,parts[i]!,base64(parts[i]!)).clean=base64(parts[i]!);return}
  differences(document,clean);const cleanText=canonicalJSON(clean),segments:string[]=[];let rest=cleanText
  for(let i=0;i<manifest.parts.length;i++){let low=0,high=rest.length;while(low<high){const mid=Math.ceil((low+high)/2),size=historyJSONBytes(rest.slice(0,mid)).length;if(size<=HISTORY_OBJECT_BYTES-1024)low=mid;else high=mid-1}let end=low;if(end>0&&/[\uD800-\uDBFF]/.test(rest[end-1]!))end--;segments.push(rest.slice(0,end));rest=rest.slice(end)}if(rest.length)invalid()
  for(const [i,part]of manifest.parts.entries()){const row=add(part,`${prefix}-chunk`,parts[i]!,base64(parts[i]!));if(row.clean!==undefined&&!same(row.clean,segments[i]))invalid();row.clean=segments[i]}
 }
 for(const row of view.commits){if(row.checkpoint)add(row.checkpoint,'checkpoint');if(row.context)payload(row.context.manifest,'chat');for(const ref of row.events)add(ref,'event');for(const ref of row.observations){const o=readHistoryObservation(view.readObject(ref));add(ref,'observation');if(o.kind==='original')payload(o.value,'source');else if(o.kind==='compaction')payload(o.replacement,'source');else if(o.kind==='request')payload(o.request,'request');else if(o.kind==='relation')add(o.evidence,'evidence')}}
 const metadata:HistoryObjectRef[]=[]
 for(const row of view.metadata){const bytes=historyJSONBytes(row),ref={digest:historyBytesDigest(bytes),bytes:bytes.length};add(ref,'metadata',bytes);metadata.push(ref);if(row.record.kind==='feedback'&&row.record.candidate)add(row.record.candidate,'candidate');if(row.record.kind==='capture'){const capture=readRunCapture(view.readObject(row.record.capture));add(row.record.capture,'capture');for(const file of capture.files)payload(file.manifest,'corpus')}}
 if(!head.checkpoint)invalid();const cp=readExecution(readCanonicalHistoryJSON(view.readObject(head.checkpoint),HISTORY_OBJECT_BYTES,'finite'));if(!cp)invalid();const report=historyJSONBytes(projectHistoryRunReport(view)),reportRef={digest:historyBytesDigest(report),bytes:report.length};add(reportRef,'report',report)
 // Headers contain only protected identities. A sensitive match here cannot be sanitized meaningfully.
 if(!same(guard.value({head,commits:view.commits},'tool argument',false),{head,commits:view.commits}))invalid()
 const mappings:ArchiveMapping[]=[],members:ArchiveMember[]=[],objects=new Map<string,Uint8Array>()
 for(const row of [...sources.values()].sort((a,b)=>roleKey(a.source.digest,a.kind).localeCompare(roleKey(b.source.digest,b.kind)))){const clean=row.clean??guard.value(row.document,'tool argument',false),redactions=(row.kind.endsWith('-chunk')?!same(row.document,clean)?['']:[]:differences(row.document,clean)).sort(),representation:ArchiveRepresentation=redactions.length?{kind:'redacted-view',source:row.source,redactions}:{kind:'exact'}
  taskIdentityEqual(row.kind,row.document,clean);
  const bytes=redactions.length?historyJSONBytes({version:1,sourceKind:row.kind,sourceVersion:sourceVersion(row.kind,row.document),source:row.source,redactions,document:clean}):row.bytes
  if(bytes.length>HISTORY_OBJECT_BYTES)invalid();const exported={digest:historyBytesDigest(bytes),bytes:bytes.length};mappings.push({source:row.source,exported,kind:row.kind,representation});const prior=objects.get(exported.digest);if(prior&&!same(Array.from(prior),Array.from(bytes)))invalid();objects.set(exported.digest,bytes);members.push({...exported,source:row.source,kind:row.kind,representation})
 }
 members.sort((a,b)=>roleKey(a.source.digest,a.kind).localeCompare(roleKey(b.source.digest,b.kind)));const body={version:1 as const,runId,source:head,coverage:head.coverage,commits:view.commits,metadata,mappings,members,report:reportRef},manifest={...body,digest:historyJSONDigest(body)},archive:RunArchive={version:1,manifest,objects:[...objects].sort(([a],[b])=>a.localeCompare(b)).map(([digest,bytes])=>({digest,bytes:base64(bytes)}))},bytes=historyJSONBytes(archive)
 if(bytes.length>HISTORY_TOTAL_BYTES)invalid();readRunArchive(bytes)
 const final=await store.readHistoryHead(runId,options.signal);if(!final||!same(readHistoryHead(final),head))throw new HistoryChanged()
 return {bytes,digest:historyBytesDigest(bytes),manifest:structuredClone(manifest)}
}
/** Strict canonical container reader. Unsigned self-consistency does not establish host authenticity. */
export function readRunArchive(input:Uint8Array,expectedDigest?:string):ImportedRunHistory{
 try{
 const bytes=copyHistoryBytes(input,HISTORY_TOTAL_BYTES)
 if(expectedDigest!==undefined&&(!historyDigestPattern.test(expectedDigest)||historyBytesDigest(bytes)!==expectedDigest))invalid()
 const archive=readCanonicalHistoryJSON(bytes,HISTORY_TOTAL_BYTES,'finite')
 if(!closed(archive,['version','manifest','objects'])||archive.version!==1||!closed(archive.manifest,['version','runId','source','coverage','commits','metadata','mappings','members','report','digest'])||archive.manifest.version!==1)return invalid()
 const m=archive.manifest as unknown as RunArchiveManifest,{digest,...body}=m
 if(!historyDigestPattern.test(digest)||historyJSONDigest(body)!==digest||!storageId(m.runId)||!same(readHistoryHead(m.source),m.source)||m.source.runId!==m.runId||m.coverage!==m.source.coverage||!Array.isArray(m.commits)||m.commits.length!==m.source.commitCount||!Array.isArray(m.metadata)||m.metadata.length!==(m.source.metadata?.count??0)||!Array.isArray(m.mappings)||m.mappings.length<1||m.mappings.length>50000||!Array.isArray(m.members)||m.members.length!==m.mappings.length||!Array.isArray(archive.objects)||archive.objects.length<1||archive.objects.length>m.members.length||!validHistoryRef(m.report))return invalid()
 const objects=new Map<string,Uint8Array>(),mappings=new Map<string,ArchiveMapping>(),used=new Set<string>(),documents=new Map<string,unknown>();let previous='',total=0
 for(const row of archive.objects){if(!closed(row,['digest','bytes'])||typeof row.digest!=='string'||row.digest<=previous||!historyDigestPattern.test(row.digest)||typeof row.bytes!=='string'||row.bytes.length>Math.ceil(HISTORY_OBJECT_BYTES/3)*4||!/^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/.test(row.bytes))invalid();const value=unbase64(row.bytes);if(value.length<1||value.length>HISTORY_OBJECT_BYTES||base64(value)!==row.bytes||historyBytesDigest(value)!==row.digest||total+value.length>HISTORY_TOTAL_BYTES)invalid();objects.set(row.digest,value);previous=row.digest;total+=value.length}
 const rep=(value:unknown,source?:HistoryObjectRef):value is ArchiveRepresentation=>closed(value,['kind'])&&value.kind==='exact'||closed(value,['kind','source','redactions'])&&value.kind==='redacted-view'&&validHistoryRef(value.source)&&(source===undefined||same(value.source,source))&&Array.isArray(value.redactions)
 const sourceSizes=new Map<string,number>()
 previous='';for(const row of m.mappings){const key=roleKey(row.source?.digest,row.kind);if(!closed(row,['source','exported','kind','representation'])||!validHistoryRef(row.source)||!validHistoryRef(row.exported)||key<=previous||!kinds.includes(row.kind)||!rep(row.representation,row.source)||row.representation.kind==='exact'&&!same(row.source,row.exported)||sourceSizes.has(row.source.digest)&&sourceSizes.get(row.source.digest)!==row.source.bytes||objects.has(row.source.digest)&&objects.get(row.source.digest)!.length!==row.source.bytes)invalid();sourceSizes.set(row.source.digest,row.source.bytes);mappings.set(key,row);previous=key}
 previous='';const declared=new Set<string>();for(const row of m.members){const key=roleKey(row.source?.digest,row.kind),mapping=mappings.get(key);if(!closed(row,['digest','bytes','source','kind','representation'])||!validHistoryRef({digest:row.digest,bytes:row.bytes})||!validHistoryRef(row.source)||key<=previous||!kinds.includes(row.kind)||!rep(row.representation,row.source)||objects.get(row.digest)?.length!==row.bytes||!mapping||!same(mapping.source,row.source)||!same(mapping.exported,{digest:row.digest,bytes:row.bytes})||!same(mapping.representation,row.representation))invalid();previous=key;declared.add(row.digest)}
 if(declared.size!==objects.size)invalid()
 const get=(ref:HistoryObjectRef,kind:ArchiveKind):unknown=>{if(!validHistoryRef(ref))return invalid();const key=roleKey(ref.digest,kind),row=mappings.get(key);if(!row||!same(row.source,ref))return invalid();const value=objects.get(row.exported.digest);if(!value||value.length!==row.exported.bytes)return invalid();used.add(key);if(documents.has(key))return documents.get(key);const document=unwrap(row,value);validateDocument(kind,document,row.representation.kind==='redacted-view');documents.set(key,document);return document}
 const payloads=new Map<string,unknown>(),logical=new Map<string,unknown>();
 const payload=(ref:HistoryObjectRef,prefix:'source'|'request'|'chat'|'corpus')=>{const cacheKey=prefix+ref.digest;if(payloads.has(cacheKey))return payloads.get(cacheKey);const manifest=get(ref,`${prefix}-manifest`) as ReturnType<typeof readHistoryChunkManifest>;let changed=false;const parts=manifest.parts.map(part=>{get(part,`${prefix}-chunk`);const row=mappings.get(roleKey(part.digest,`${prefix}-chunk`))!;if(row.representation.kind==='exact')return objects.get(row.exported.digest)!;changed=true;return new TextEncoder().encode(unwrap(row,objects.get(row.exported.digest)!) as string)});const length=parts.reduce((n,p)=>n+p.length,0);if(length<1||length>HISTORY_TOTAL_BYTES)invalid();const full=new Uint8Array(length);let offset=0;for(const part of parts){full.set(part,offset);offset+=part.length}if(!changed&&(length!==manifest.bytes||historyBytesDigest(full)!==manifest.digest))invalid();const text=historyText(full);if(changed&&!/\[redacted: [a-z][a-z0-9-]{0,63}\]/.test(text))invalid();const value=prefix==='source'?readHistorySourceJSON(text):prefix==='request'?readHistoryRequestJSON(text):readCanonicalHistoryJSON(full,HISTORY_TOTAL_BYTES,'finite');if(prefix==='chat'&&!changed)readChatContextSnapshot(full);
 if(changed&&manifest.parts.every(part=>objects.has(part.digest))){const original=new Uint8Array(manifest.bytes);let at=0;for(const part of manifest.parts){const raw=objects.get(part.digest)!;if(at+raw.length>original.length)invalid();original.set(raw,at);at+=raw.length}if(at!==manifest.bytes||historyBytesDigest(original)!==manifest.digest)invalid();const text=historyText(original),source=prefix==='source'?readHistorySourceJSON(text):prefix==='request'?readHistoryRequestJSON(text):readCanonicalHistoryJSON(original,HISTORY_TOTAL_BYTES,'finite');differences(source,value);for(const kind of ['checkpoint','report','event'] as const)if(mappings.has(roleKey(manifest.digest,kind)))taskIdentityEqual(kind,source,value)}
 if(logical.has(manifest.digest)){const other=logical.get(manifest.digest);try{differences(other,value)}catch{differences(value,other)}}else logical.set(manifest.digest,value)
 payloads.set(cacheKey,value);return value}
 let eventCount=0,prior:string|null=null
 const observations=new Map<string,{value:ReturnType<typeof readHistoryObservation>;sequence:number}>()
 for(const [i,input]of m.commits.entries()){const row=readHistoryEntry(input);if(row.sequence!==i+1||row.eventStart!==eventCount+1||row.previousDigest!==prior)invalid();eventCount+=row.eventCount;prior=row.digest
  if(row.checkpoint){const cp=get(row.checkpoint,'checkpoint') as Record<string,unknown>;if(cp.id!==m.runId)invalid()}
  if(row.context){const saved=payload(row.context.manifest,'chat') as Record<string,unknown>;if(!object(saved)||!Number.isSafeInteger(saved.turns)||row.context.turn===undefined||row.context.turn>Number(saved.turns)||!Array.isArray(saved.open)||saved.open.some(t=>object(t)&&t.number===row.context!.turn)===!!row.context.closed||row.context.privateScope!==undefined&&(!row.context.closed||saved.scope===row.context.privateScope))invalid()}
  for(const ref of row.events){const {scope,event}=get(ref,'event') as {scope:DurableScope;event:Record<string,unknown>};if(scope.task===undefined?scope.execution!==m.runId:scope.execution!==scope.task)invalid();if(['execution_checkpoint','budget_paused'].includes(String(event.type))&&(!object(event.checkpoint)||event.checkpoint.id!==scope.execution))invalid()}
  for(const ref of row.observations){const value=get(ref,'observation') as ReturnType<typeof readHistoryObservation>;if(!observations.has(ref.digest))observations.set(ref.digest,{value,sequence:row.sequence})}
 }
 const last=m.commits.at(-1)!;if(eventCount!==m.source.eventCount||prior!==m.source.root||last.executionRevision!==m.source.executionRevision||!same(last.checkpoint,m.source.checkpoint)||!same(last.context,m.source.context))invalid()
 const active=new Set<string>(),done=new Set<string>()
 const visit=(digest:string,depth=0,kind?:string,scope?:DurableScope,before?:number)=>{const row=observations.get(digest);if(!row||depth>64||active.has(digest)||kind!==undefined&&row.value.kind!==kind||scope!==undefined&&!same(row.value.scope,scope)||before!==undefined&&row.sequence>=before)invalid();if(done.has(digest))return;active.add(digest);const o=row.value
  if(o.kind==='original')payload(o.value,'source')
  else if(o.kind==='compaction'){if(o.originals.length<1)invalid();for(const ref of o.originals){get(ref,'observation');visit(ref.digest,depth+1,'original',o.scope,row.sequence)}payload(o.replacement,'source');for(const ref of o.prior){get(ref,'observation');visit(ref.digest,depth+1,'compaction',o.scope,row.sequence)}}
  else if(o.kind==='request'){payload(o.request,'request');for(const ref of o.compactions){get(ref,'observation');visit(ref.digest,depth+1,'compaction',o.scope,row.sequence+1)};for(const ref of o.originals){get(ref,'observation');visit(ref.digest,depth+1,'original',o.scope,row.sequence+1)}}
  else if(o.kind==='relation')get(o.evidence,'evidence')
  active.delete(digest);done.add(digest)
 };for(const digest of observations.keys())visit(digest)
 const report=get(m.report,'report') as Record<string,unknown>,feedbackViews=(report.feedback??[]) as {sourceDigest:string;record:import('./types').RunFeedback}[];let feedbackPosition=0
 let metadataPrior:string|null=null;const feedback=new Map<string,{sourceDigest:string;record:import('./types').RunFeedback}>(),eventRefs=m.commits.flatMap(row=>row.events)
 for(const [i,ref]of m.metadata.entries()){const row=get(ref,'metadata') as HistoryMetadataEntry;if(row.sequence!==i+1||row.previousDigest!==metadataPrior||row.commitSequence<1||row.commitSequence>m.commits.length||m.commits[row.commitSequence-1]!.executionRevision!==row.executionRevision)invalid();metadataPrior=row.digest
  if(row.record.kind==='capture'){const capture=get(row.record.capture,'capture') as ReturnType<typeof readRunCapture>;if(capture.runId!==m.runId||capture.executionRevision!==row.executionRevision)invalid();const files=new Map(capture.files.map(file=>[file.name,payload(file.manifest,'corpus')]));differences(get(capture.checkpoint,'checkpoint'),files.get('checkpoint.json'));taskIdentityEqual('checkpoint',get(capture.checkpoint,'checkpoint'),files.get('checkpoint.json'))}
  else{const f=row.record.feedback,old=feedback.get(f.id),display=feedbackViews[feedbackPosition++];if(!display||!same(display.record,f)||!historyDigestPattern.test(display.sourceDigest)||mappings.get(roleKey(ref.digest,'metadata'))!.representation.kind==='exact'&&display.sourceDigest!==historyJSONDigest(f)||f.runId!==m.runId||f.sequence!==(old?old.record.sequence+1:1)||f.previous!==(old?.sourceDigest??null)||old&&!same(old.record.target,f.target))invalid();if(row.record.candidate){const c=get(row.record.candidate,'candidate') as import('../corpusCandidate').CorpusCandidate,{remark:_,...a}=c.feedback,{remark:__,...b}=f;if(c.feedbackDigest!==display.sourceDigest||c.runId!==m.runId||!same(a,b)||Object.hasOwn(c.feedback,'remark')!==Object.hasOwn(f,'remark'))invalid()}if(f.target.kind==='assistant-message'){const target=eventRefs[f.target.eventSequence-1];if(!target||target.digest!==f.target.eventDigest||(get(target,'event') as {event:{type:string}}).event.type!=='message')invalid()}feedback.set(f.id,display)}
 }
 if(metadataPrior!==(m.source.metadata?.root??null))invalid()
 if(feedbackPosition!==feedbackViews.length)invalid();if(!m.source.checkpoint)invalid();const checkpoint=get(m.source.checkpoint,'checkpoint') as Record<string,unknown>;if(report.id!==m.runId||report.status!==checkpoint.status||!['complete','stopped'].includes(String(checkpoint.status))||report.checkpointVersion!==checkpoint.version)invalid();for(const name of RUN_REPORT_SOURCES)if(!same(report[name],checkpoint[name]))invalid();if(!same(report.repeatTools,projectRepeatTools(checkpoint as never)))invalid();if(report.version!==(checkpoint.version===3||feedbackViews.length>0?2:1)||!same(report.route,checkpoint.version===3?checkpoint.turnRoute:undefined)||!same(report.usageTotals,checkpoint.version===3&&checkpoint.usageBudget!==undefined?usageTotals(checkpoint.usageBudget as UsageBudgetJournal):undefined))invalid()
 if(!['legacy-incomplete','recording-incomplete'].includes(String(m.coverage)))invalid()
 validateHistoryRelations(m.commits,(ref,kind)=>get(ref,kind),ref=>mappings.get(roleKey(ref.digest,'checkpoint'))?.representation.kind==='redacted-view')
 if(used.size!==mappings.size)invalid()
 return new ImportedRunHistory(token,m,objects,mappings,bytes)
 }catch{return invalid()}
}
