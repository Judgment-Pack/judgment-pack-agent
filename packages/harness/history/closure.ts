import {canonicalJSON} from '../context/chat'
import {readHistoryObservation} from './metadata'
import type {HistoryObservation} from './types'
import {HistoryInputInvalid,HISTORY_TOTAL_BYTES,HISTORY_OBJECT_BYTES,historyBytesDigest,historyText,readHistoryChunkManifest,readHistorySourceJSON,readHistoryRequestJSON,readCanonicalHistoryJSON,validHistoryRef,type HistoryObjectRef,type HistoryObject} from './codec'
/** Validate and collect all typed dependencies before the containing owner publishes anything.
 * `recorded` contains actual journal positions; staged bytes alone never establish an earlier original. */
export function historyObservationClosure(roots:HistoryObjectRef[],resolve:(ref:HistoryObjectRef)=>HistoryObject,recorded:ReadonlyMap<string,number>):HistoryObject[]{
 const objects=new Map<string,HistoryObject>(),observations=new Map<string,HistoryObservation>(),active=new Set<string>();let total=0
 const invalid=():never=>{throw new HistoryInputInvalid()}
 const object=(ref:HistoryObjectRef)=>{if(!validHistoryRef(ref))return invalid();const prior=objects.get(ref.digest);if(prior){if(prior.bytes!==ref.bytes)return invalid();return prior}const value=resolve(ref);if(value.bytes!==ref.bytes||value.value.length!==ref.bytes||historyBytesDigest(value.value)!==ref.digest||objects.size>=50000||total+ref.bytes>HISTORY_TOTAL_BYTES)return invalid();total+=ref.bytes;objects.set(ref.digest,value);return value}
 const payload=(ref:HistoryObjectRef,canonical=false)=>{const manifest=readHistoryChunkManifest(object(ref).value),value=new Uint8Array(manifest.bytes);let offset=0;for(const part of manifest.parts){const bytes=object(part).value;value.set(bytes,offset);offset+=bytes.length}if(historyBytesDigest(value)!==manifest.digest)return invalid();if(canonical)readHistoryRequestJSON(historyText(value));else readHistorySourceJSON(historyText(value))}
 const visit=(ref:HistoryObjectRef,depth:number,expected?:HistoryObservation['kind'],scope?:unknown,before?:number):void=>{
  const sequence=recorded.get(ref.digest);if(depth>64||sequence===undefined||before!==undefined&&sequence>=before||active.has(ref.digest))return invalid()
  const value=object(ref),row=observations.get(ref.digest)??readHistoryObservation(value.value)
  if(expected!==undefined&&row.kind!==expected||scope!==undefined&&canonicalJSON(row.scope)!==canonicalJSON(scope))return invalid()
  if(observations.has(ref.digest))return
  active.add(ref.digest)
  if(row.kind==='original')payload(row.value)
  else if(row.kind==='compaction'){if(row.originals.length<1)return invalid();for(const original of row.originals)visit(original,depth+1,'original',row.scope,sequence);payload(row.replacement);for(const prior of row.prior)visit(prior,depth+1,'compaction',row.scope,sequence)}
  else if(row.kind==='request'){payload(row.request,true);for(const compaction of row.compactions)visit(compaction,depth+1,'compaction',row.scope,sequence+1);for(const original of row.originals)visit(original,depth+1,'original',row.scope,sequence+1)}
  else if(row.kind==='capture-request'&&row.nativeBindings)readCanonicalHistoryJSON(object(row.nativeBindings).value,HISTORY_OBJECT_BYTES,'finite')
  else if(row.kind==='relation')readCanonicalHistoryJSON(object(row.evidence).value,HISTORY_OBJECT_BYTES,'finite')
  active.delete(ref.digest);observations.set(ref.digest,row)
 }
 for(const root of roots)visit(root,0)
 return [...objects.values()]
}
