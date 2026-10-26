import {canonicalJSON} from '../context/chat'
import type {DurableScope} from '../storage/writer'
import type {RunHistoryEntry} from '../storage/history'
import type {HistoryObservation} from './types'
import {HistoryInputInvalid,HISTORY_OBJECT_BYTES,readCanonicalHistoryJSON,historyJSONBytes,historyJSONDigest,historyDigestPattern,validHistoryRef,type HistoryObjectRef} from './codec'
import {storageId} from '../storage/common'
export interface HistoryRelationEvidence{version:1;kind:'observed-relation';scope:DurableScope;dispatchId:string;checkpoint:HistoryObjectRef;observationId:string;requestDigest:string;resultDigest:string;target:{kind:'job'|'artifact'|'dialect';id:string}}
type Kind='checkpoint'|'observation'|'evidence'|'request-manifest'
const same=(a:unknown,b:unknown)=>canonicalJSON(a)===canonicalJSON(b)
const object=(v:unknown):v is Record<string,unknown>=>v!==null&&typeof v==='object'&&!Array.isArray(v)
/** A closed evidence record; reading it alone proves no association with an execution. */
export function readHistoryRelationEvidence(bytes:Uint8Array):HistoryRelationEvidence{
 const e=readCanonicalHistoryJSON(bytes,HISTORY_OBJECT_BYTES) as HistoryRelationEvidence
 const closed=(v:unknown,keys:string[],optional:string[]=[])=>object(v)&&keys.every(k=>Object.hasOwn(v,k))&&Object.keys(v).every(k=>keys.includes(k)||optional.includes(k))
 if(!closed(e,['version','kind','scope','dispatchId','checkpoint','observationId','requestDigest','resultDigest','target'])||e.version!==1||e.kind!=='observed-relation'||!closed(e.scope,['execution','role'],['task'])||!storageId(e.scope.execution)||!storageId(e.scope.role)||e.scope.task!==undefined&&!storageId(e.scope.task)||!storageId(e.dispatchId)||!storageId(e.observationId)||!validHistoryRef(e.checkpoint)||!historyDigestPattern.test(e.requestDigest)||!historyDigestPattern.test(e.resultDigest)||!closed(e.target,['kind','id'])||!['job','artifact','dialect'].includes(e.target.kind)||!storageId(e.target.id))throw new HistoryInputInvalid()
 return e
}
/** Host target assertions bind an actual retained request, result and observation. They do not prove external success.
 * In a derived display, source digests remain historical labels; sanitized result bytes never claim the original hash. */
export function validateHistoryRelations(commits:RunHistoryEntry[],read:(ref:HistoryObjectRef,kind:Kind)=>unknown,derived:(ref:HistoryObjectRef)=>boolean=()=>false):void{
 function invalid():never{throw new HistoryInputInvalid()}
 const observations:{sequence:number;value:HistoryObservation}[]=[]
 for(const commit of commits)for(const ref of commit.observations)observations.push({sequence:commit.sequence,value:read(ref,'observation') as HistoryObservation})
 for(const row of observations){const relation=row.value;if(relation.kind!=='relation')continue
  const e=readHistoryRelationEvidence(historyJSONBytes(read(relation.evidence,'evidence')))
  if(e.dispatchId!==relation.dispatchId||!same(e.scope,relation.scope)||!same(e.target,relation.target))invalid()
  if(!commits.some(c=>c.sequence<=row.sequence&&same(c.checkpoint,e.checkpoint)))invalid()
  const checkpoint=read(e.checkpoint,'checkpoint') as Record<string,unknown>,book=checkpoint.durable as {dispatches?:Record<string,unknown>[];observations?:Record<string,unknown>[]} |undefined,claim=book?.dispatches?.find(d=>d.id===e.dispatchId&&same(d.scope,e.scope))
  if(!claim||!['host','runtime'].includes(String(claim.kind))||claim.state!=='result'||!Object.hasOwn(claim,'result')||claim.requestDigest!==e.requestDigest||!derived(e.checkpoint)&&historyJSONDigest(claim.result)!==e.resultDigest)invalid()
  const observation=book?.observations?.find(o=>o.id===e.observationId&&o.kind==='operation'&&o.dispatchId===e.dispatchId&&o.source===claim.source&&same(o.scope,e.scope));if(!observation||!commits.some(c=>c.commitId===observation.commitId&&c.sequence<=row.sequence))invalid()
  const request=observations.find(r=>r.sequence<=row.sequence&&r.value.kind==='request'&&r.value.dispatchId===e.dispatchId&&r.value.source===claim.source&&r.value.tool===claim.tool&&same(r.value.scope,e.scope))?.value
  if(!request||request.kind!=='request'||(read(request.request,'request-manifest') as {digest?:unknown}).digest!==e.requestDigest)invalid()
 }
}
