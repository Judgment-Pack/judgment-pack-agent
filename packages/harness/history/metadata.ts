import {readCanonicalHistoryJSON,HISTORY_OBJECT_BYTES,HistoryInputInvalid,historyDigestPattern,validHistoryRef} from './codec'
import {storageId} from '../storage/common'
import type {HistoryObservation,RunCapture,RunFeedback,HistoryMetadata} from './types'
const fail=():never=>{throw new HistoryInputInvalid()}
const object=(v:unknown):v is Record<string,unknown>=>v!==null&&typeof v==='object'&&!Array.isArray(v)
const closed=(v:unknown,required:string[],optional:string[]=[]):v is Record<string,unknown>=>object(v)&&required.every(k=>Object.hasOwn(v,k))&&Object.keys(v).every(k=>required.includes(k)||optional.includes(k))
const id=storageId
const count=(v:unknown,min=0)=>Number.isSafeInteger(v)&&Number(v)>=min
const digest=(v:unknown):v is string=>typeof v==='string'&&historyDigestPattern.test(v)
const scope=(v:unknown)=>closed(v,['execution','role'],['task'])&&id(v.execution)&&id(v.role)&&(v.task===undefined||id(v.task))
const refs=(v:unknown,max=10000)=>Array.isArray(v)&&v.length<=max&&v.every(validHistoryRef)
const timestamp=(v:unknown)=>typeof v==='string'&&/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/.test(v)&&Number.isFinite(Date.parse(v))&&new Date(v).toISOString()===v
export function validRunFeedback(v:unknown):v is RunFeedback{
 if(!closed(v,['version','id','runId','sequence','previous','target','rating','actor','recordedAt'],['remark'])||v.version!==1||!id(v.id)||!id(v.runId)||!count(v.sequence,1)||v.previous!==null&&!digest(v.previous)||!['good','bad'].includes(String(v.rating))||!id(v.actor)||!timestamp(v.recordedAt)||v.remark!==undefined&&typeof v.remark!=='string')return false
 return closed(v.target,['kind'])&&v.target.kind==='run'||closed(v.target,['kind','eventSequence','eventDigest'])&&v.target.kind==='assistant-message'&&count(v.target.eventSequence,1)&&digest(v.target.eventDigest)
}
export function readRunFeedback(bytes:Uint8Array):RunFeedback{const value=readCanonicalHistoryJSON(bytes,HISTORY_OBJECT_BYTES);if(!validRunFeedback(value))return fail();return value}
export function readHistoryMetadata(bytes:Uint8Array):HistoryMetadata{
 const value=readCanonicalHistoryJSON(bytes,HISTORY_OBJECT_BYTES)
 if(closed(value,['version','kind','capture'])&&value.version===1&&value.kind==='capture'&&validHistoryRef(value.capture))return value as unknown as HistoryMetadata
 if(closed(value,['version','kind','feedback'],['candidate','candidateRequested'])&&value.version===1&&value.kind==='feedback'&&validRunFeedback(value.feedback)&&(value.candidate===undefined||value.candidateRequested===true&&validHistoryRef(value.candidate))&&(value.candidateRequested===undefined||value.candidateRequested===true))return value as unknown as HistoryMetadata
 return fail()
}
export function readRunCapture(bytes:Uint8Array):RunCapture{
 const value=readCanonicalHistoryJSON(bytes,HISTORY_OBJECT_BYTES)
 if(!closed(value,['version','runId','executionRevision','checkpoint','requests','files','redacted'],['eventProjection'])||value.eventProjection!==undefined&&value.eventProjection!=='transcript-v1'||value.version!==1||!id(value.runId)||!id(value.executionRevision)||!validHistoryRef(value.checkpoint)||value.redacted!==true||!Array.isArray(value.requests)||value.requests.length>10000||!Array.isArray(value.files))return fail()
 const names=['checkpoint.json','events.json','expected.json','manifest.json','requests.json','tool-results.json']
 if(value.files.length!==names.length||value.files.some((file,i)=>!closed(file,['name','manifest'])||file.name!==names[i]||!validHistoryRef(file.manifest)))return fail()
 const seen=new Set<string>()
 for(const [index,row] of value.requests.entries()){
  if(!closed(row,['scope','dispatchId','kind','source','tool','requestDigest','surface','captureIndex'])||!scope(row.scope)||!id(row.dispatchId)||!['model','agent'].includes(String(row.kind))||!id(row.source)||!id(row.tool)||!digest(row.requestDigest)||row.surface!==(row.kind==='model'?'session-model':'native-aggregate')||row.captureIndex!==index)return fail()
  if(seen.has(row.dispatchId))return fail();seen.add(row.dispatchId)
 }
 return value as unknown as RunCapture
}
export function readHistoryObservation(bytes:Uint8Array):HistoryObservation{
 const value=readCanonicalHistoryJSON(bytes,HISTORY_OBJECT_BYTES)
 if(!object(value)||value.version!==1||!scope(value.scope))return fail()
 if(value.kind==='genesis'&&closed(value,['version','kind','scope','profile'])&&closed(value.profile,['version','profile','genesis','originals','compactions','requests'])&&value.profile.version===1&&value.profile.profile==='full-history'&&['genesis','originals','compactions','requests'].every(key=>(value.profile as Record<string,unknown>)[key]===true))return value as unknown as HistoryObservation
 if(value.kind==='capture-request'&&closed(value,['version','kind','scope','dispatchId','captureKind','source','tool','requestDigest','surface'])&&id(value.dispatchId)&&['model','agent'].includes(String(value.captureKind))&&id(value.source)&&id(value.tool)&&digest(value.requestDigest)&&value.surface===(value.captureKind==='model'?'session-model':'native-aggregate'))return value as unknown as HistoryObservation
 if(value.kind==='original'&&closed(value,['version','kind','scope','boundary','value','format','ordinal'])&&id(value.boundary)&&validHistoryRef(value.value)&&['messages','context'].includes(String(value.format))&&count(value.ordinal))return value as unknown as HistoryObservation
 if(value.kind==='compaction'&&closed(value,['version','kind','scope','boundary','mode','originals','replacement','prior','legacyDigest'])&&id(value.boundary)&&['index','summary'].includes(String(value.mode))&&refs(value.originals)&&validHistoryRef(value.replacement)&&refs(value.prior)&&id(value.legacyDigest))return value as unknown as HistoryObservation
 if(value.kind==='request'&&closed(value,['version','kind','scope','dispatchId','source','tool','request','compactions','originals'])&&id(value.dispatchId)&&id(value.source)&&id(value.tool)&&validHistoryRef(value.request)&&refs(value.compactions)&&refs(value.originals))return value as unknown as HistoryObservation
 if(value.kind==='relation'&&closed(value,['version','kind','scope','dispatchId','target','evidence'])&&id(value.dispatchId)&&closed(value.target,['kind','id'])&&['job','artifact','dialect'].includes(String(value.target.kind))&&id(value.target.id)&&validHistoryRef(value.evidence))return value as unknown as HistoryObservation
 return fail()
}
