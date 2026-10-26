import type {DurableScope} from '../storage/writer'
import type {HistoryRunStore} from '../storage/history'
import type {HistoryObservation,HistoryCaptureIdentity} from './types'
import {readHistoryObservation} from './metadata'
import {HistoryInputInvalid,copyHistoryBytes,historyJSONBytes,historyObject,chunkHistoryValue,readHistorySourceJSON,readHistoryRequestJSON,HISTORY_TOTAL_BYTES,type HistoryObject,type HistoryObjectRef} from './codec'
export interface PreparedHistoryObservation{observation:HistoryObservation;ref:HistoryObjectRef;objects:HistoryObject[]}
async function prepared(observation:HistoryObservation,objects:HistoryObject[]=[]):Promise<PreparedHistoryObservation>{
 const value=historyJSONBytes(observation),decoded=readHistoryObservation(value);const object=await historyObject(value)
 return {observation:decoded,ref:{digest:object.digest,bytes:object.bytes},objects:[...objects,object]}
}
export function prepareHistoryGenesis(scope:DurableScope){return prepared({version:1,kind:'genesis',scope,profile:{version:1,profile:'full-history',genesis:true,originals:true,compactions:true,requests:true}})}
/** Internal public retained input only. Ordered source JSON remains authoritative, independently of parsed key order. */
export async function prepareHistoryOriginal(scope:DurableScope,boundary:string,sourceJSON:string,format:'messages'|'context',ordinal:number){
 readHistorySourceJSON(sourceJSON)
 const {manifest,objects}=await chunkHistoryValue(new TextEncoder().encode(sourceJSON))
 return prepared({version:1,kind:'original',scope,boundary,value:{digest:manifest.digest,bytes:manifest.bytes},format,ordinal},[...objects,manifest])
}
/** Call only after the original observation has been acknowledged, before losing its public inputs. */
export async function prepareHistoryCompaction(scope:DurableScope,boundary:string,originals:HistoryObjectRef[],replacementJSON:string,mode:'index'|'summary',prior:HistoryObjectRef[],legacyDigest:string){
 readHistorySourceJSON(replacementJSON)
 const {manifest,objects}=await chunkHistoryValue(new TextEncoder().encode(replacementJSON))
 return prepared({version:1,kind:'compaction',scope,boundary,originals,replacement:{digest:manifest.digest,bytes:manifest.bytes},mode,prior,legacyDigest},[...objects,manifest])
}
export async function prepareHistoryRequest(scope:DurableScope,dispatchId:string,source:string,tool:string,sourceJSON:string,compactions:HistoryObjectRef[],originals:HistoryObjectRef[]){
 // Prepared writer descriptors are canonical; preserve their exact supplied bytes.
 readHistoryRequestJSON(sourceJSON)
 const {manifest,objects}=await chunkHistoryValue(new TextEncoder().encode(sourceJSON))
 return prepared({version:1,kind:'request',scope,dispatchId,source,tool,request:{digest:manifest.digest,bytes:manifest.bytes},compactions,originals},[...objects,manifest])
}
export async function prepareCaptureExpectation(identity:Omit<HistoryCaptureIdentity,'captureIndex'>,bindings?:Record<string,string>){const {kind:captureKind,...fields}=identity;if(bindings!==undefined&&captureKind!=='agent')throw new HistoryInputInvalid();const source=bindings===undefined?undefined:await historyObject(historyJSONBytes(bindings));return prepared({version:1,kind:'capture-request',captureKind,...fields,...(source?{nativeBindings:{digest:source.digest,bytes:source.bytes}}:{})},source?[source]:[])}
/** Staging is private. The original writer must atomically publish the returned ref before dispatch or eviction. */
export async function stageHistoryObservation(store:HistoryRunStore,runId:string,value:PreparedHistoryObservation,signal?:AbortSignal){
 if(store.historyVersion!==1)throw new HistoryInputInvalid()
 const held=new Map<string,HistoryObject>();let total=0
 for(const object of value.objects){const bytes=copyHistoryBytes(object.value),copy=await historyObject(bytes);if(copy.digest!==object.digest||copy.bytes!==object.bytes)throw new HistoryInputInvalid();if(!held.has(copy.digest)){held.set(copy.digest,copy);total+=copy.bytes;if(total>HISTORY_TOTAL_BYTES)throw new HistoryInputInvalid()}}
 if(!held.has(value.ref.digest)||held.get(value.ref.digest)!.bytes!==value.ref.bytes)throw new HistoryInputInvalid()
 readHistoryObservation(held.get(value.ref.digest)!.value)
 let batch:HistoryObject[]=[],bytes=0
 for(const object of held.values()){if(batch.length>=256||bytes+object.bytes>8*1024*1024){await store.stageHistoryObjects({kind:'run',id:runId},batch,signal);batch=[];bytes=0}batch.push(object);bytes+=object.bytes}
 if(batch.length)await store.stageHistoryObjects({kind:'run',id:runId},batch,signal)
 return {...value.ref}
}
