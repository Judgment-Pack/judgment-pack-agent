import {authoredFailure} from '../failures'
import {requireId,STORAGE_RECORD_BYTES} from '../storage/common'
import type {PlanReference,CurrentPlanReference,LegacyPlanReference} from './model'
export interface ResolvedPlanReference extends CurrentPlanReference {content?:Uint8Array}
/** Already bound to the authorized host resource scope. No filesystem/URL is inferred by Agent. */
export type LegacyPlanLocator={kind:'file';id:string;revision?:undefined}
export interface PlanResolver {
 resolve(reference:Pick<CurrentPlanReference,'kind'|'id'>,signal:AbortSignal):Promise<ResolvedPlanReference>
 legacy?(reference:LegacyPlanReference|LegacyPlanLocator,signal:AbortSignal):Promise<ResolvedPlanReference>
}
export class LegacyPlanReferenceUnresolved extends Error {readonly failureCode='stale-state';constructor(){super('LegacyPlanReferenceUnresolved: the host must resolve this historical reference.');this.name='LegacyPlanReferenceUnresolved'}}
export class PlanReferenceChanged extends Error {readonly failureCode='stale-state';constructor(){super('PlanReferenceChanged: a referenced object changed; review a new plan revision.');this.name='PlanReferenceChanged'}}
export async function resolvePlanReference(reference:PlanReference|LegacyPlanLocator|Pick<CurrentPlanReference,'kind'|'id'>,resolver:PlanResolver,signal:AbortSignal):Promise<CurrentPlanReference>{
 signal.throwIfAborted()
 const legacy=reference.kind==='file'||'revision' in reference
 if(legacy&&!resolver.legacy)throw new LegacyPlanReferenceUnresolved()
 const found=legacy?await resolver.legacy!(reference as LegacyPlanReference|LegacyPlanLocator,signal):await resolver.resolve(reference as Pick<CurrentPlanReference,'kind'|'id'>,signal)
 signal.throwIfAborted();requireId(found.id)
 if(!['artifact','dialect'].includes(found.kind)||!/^[a-f0-9]{64}$/.test(found.digest)||!legacy&&(found.id!==reference.id||found.kind!==reference.kind))throw authoredFailure('stale-state','PlanReferenceChanged: the host resolved a different object.','storage')
 if(found.content!==undefined){
  if(!ArrayBuffer.isView(found.content)||found.content.length>STORAGE_RECORD_BYTES)throw new PlanReferenceChanged()
  const sum=await crypto.subtle.digest('SHA-256',new Uint8Array(found.content))
  if([...new Uint8Array(sum)].map(n=>n.toString(16).padStart(2,'0')).join('')!==found.digest)throw new PlanReferenceChanged()
 }
 const pin='revision' in reference?reference.revision:'digest'in reference?reference.digest:undefined
 if(pin!==undefined&&pin!==found.digest)throw new PlanReferenceChanged()
 return {kind:found.kind,id:found.id,digest:found.digest}
}
