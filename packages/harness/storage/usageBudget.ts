import {canonicalJSON} from '../context/chat'
import {sha256} from '../records'
import {AIFailureError,failure} from '../failures'
import {StorageCommitMismatch,StorageInputError,storageId} from './common'

export const USAGE_DIMENSIONS=['modelRequests','providerInvocations','inputTokens','outputTokens'] as const
export type UsageDimension=typeof USAGE_DIMENSIONS[number]
/** Bounds/reservations are never provider-reported usage. Omitted figures are not zero. */
export type UsageAmounts=Partial<Record<UsageDimension,number>>
export interface UsageBudgetScope {resource:string;id:string;revision:string;limits:UsageAmounts}
export interface UsageReservation {
 scope:UsageBudgetScope;id:string;root:string;execution:string;role:string;task?:string
 kind:'model'|'agent';requestDigest:string;connection:string;connectionRevision:string;model:string
 upper:UsageAmounts
}
export type UsageSettlement={scope:UsageBudgetScope;id:string;reservationDigest:string;commitId:string;evidence:string}&(
 {kind:'reported';observed:UsageAmounts}|{kind:'never-admitted'}
)
export interface UsageBudgetReceipt {
 resource:string;scope:string;reservation:string;reservationDigest:string;revision:string
 state:'reserved'|'partial'|'settled'|'never-admitted';observed:UsageAmounts
}
export interface UsageReserveResult {receipt:UsageBudgetReceipt;/** Live operation metadata, not a serialized authority grant. */created:boolean}
export interface UsageReservationHead {request:UsageReservation;receipt:UsageBudgetReceipt}
/** Host owns one capability per budget resource, shared across all relevant runs/chats.
 * reserve is atomic against externally committed usage and ALL sibling reservations.
 * This transaction is separate from RunStore CAS. An unknown acknowledgment never refunds.
 */
export interface UsageBudgetStore {
 readonly resource:string
 reserve(input:UsageReservation,signal?:AbortSignal):Promise<UsageReserveResult>
 settle(input:UsageSettlement,signal?:AbortSignal):Promise<UsageBudgetReceipt>
 readReservation(scope:UsageBudgetScope,id:string,signal?:AbortSignal):Promise<UsageReservationHead|undefined>
}
export class UsageBudgetExceeded extends AIFailureError {
 constructor(readonly dimension:UsageDimension){super({...failure('request-limit','context','not-started'),detail:`UsageBudgetExceeded: the ${dimension} budget cannot admit this physical request.`});this.name='UsageBudgetExceeded'}
}
export class UsageBudgetUnknown extends AIFailureError {
 constructor(){super({...failure('storage','storage','unknown'),detail:'UsageBudgetUnknown: committed scope usage or a reservation acknowledgment is unknown; no automatic replay or refund.'});this.name='UsageBudgetUnknown'}
}
export class UsageBudgetUnsupported extends AIFailureError {
 constructor(){super({...failure('configuration','context','not-started'),detail:'UsageBudgetUnsupported: this route cannot prove the requested physical usage upper bound.'});this.name='UsageBudgetUnsupported'}
}
export class UsageBudgetIdentityChanged extends AIFailureError {
 constructor(){super({...failure('stale-state','context','not-started'),detail:'UsageBudgetIdentityChanged: the retained budget resource, policy or reservation differs.'});this.name='UsageBudgetIdentityChanged'}
}
export class UsageBudgetCapacity extends AIFailureError {
 constructor(){super({...failure('storage','storage','not-started'),detail:'UsageBudgetCapacity: unresolved or idempotent budget records reached the bounded resource capacity.'});this.name='UsageBudgetCapacity'}
}
export const USAGE_BUDGET_RECORD_BYTES=64*1024
export const USAGE_BUDGET_SCOPES=64
export const USAGE_BUDGET_RESERVATIONS=4096
const DIGEST=/^sha256:[a-f0-9]{64}$/
const owns=(value:object,key:PropertyKey)=>Object.prototype.hasOwnProperty.call(value,key)
const bytes=(value:unknown)=>new TextEncoder().encode(canonicalJSON(value)).length
const digest=(value:unknown)=>`sha256:${sha256(canonicalJSON(value))}`
function object(value:unknown,keys:readonly string[]):asserts value is Record<string,unknown>{
 if(!value||typeof value!=='object'||Array.isArray(value)||Object.keys(value).some(key=>!keys.includes(key)))throw new StorageInputError()
}
function id(value:unknown):asserts value is string {if(!storageId(value)||new TextEncoder().encode(value).length>100)throw new StorageInputError()}
function amounts(value:unknown,nonempty=false):asserts value is UsageAmounts {
 object(value,USAGE_DIMENSIONS)
 if(nonempty&&Object.keys(value).length===0)throw new StorageInputError()
 for(const count of Object.values(value))if(!Number.isSafeInteger(count)||Number(count)<0)throw new StorageInputError()
}
function scope(value:unknown,resource:string):asserts value is UsageBudgetScope {
 object(value,['resource','id','revision','limits']);id(value.resource);id(value.id);id(value.revision);amounts(value.limits,true)
 if(value.resource!==resource)throw new UsageBudgetIdentityChanged()
}
function request(value:unknown,resource:string):asserts value is UsageReservation {
 object(value,['scope','id','root','execution','role','task','kind','requestDigest','connection','connectionRevision','model','upper'])
 scope(value.scope,resource)
 for(const key of ['id','root','execution','role','connection','connectionRevision','model'])id(value[key])
 if(value.task!==undefined)id(value.task)
 if(value.kind!=='model'&&value.kind!=='agent'||typeof value.requestDigest!=='string'||!DIGEST.test(value.requestDigest))throw new StorageInputError()
 amounts(value.upper)
 for(const key of Object.keys((value.scope as unknown as UsageBudgetScope).limits) as UsageDimension[])if(!owns(value.upper,key))throw new UsageBudgetUnsupported()
 if(value.kind==='model'&&((value.upper.modelRequests??1)!==1||(value.upper.providerInvocations??0)!==0))throw new StorageInputError()
 if(value.kind==='agent'&&(value.upper.providerInvocations??1)!==1)throw new StorageInputError()
 if(value.kind==='agent'&&owns((value.scope as unknown as UsageBudgetScope).limits,'modelRequests'))throw new UsageBudgetUnsupported()
 if(bytes(value)>USAGE_BUDGET_RECORD_BYTES)throw new StorageInputError()
}
function settlement(value:unknown,resource:string):asserts value is UsageSettlement {
 object(value,['scope','id','reservationDigest','commitId','evidence','kind','observed']);scope(value.scope,resource);id(value.id);id(value.commitId)
 if(typeof value.reservationDigest!=='string'||!DIGEST.test(value.reservationDigest)||typeof value.evidence!=='string'||!DIGEST.test(value.evidence))throw new StorageInputError()
 if(value.kind==='reported')amounts(value.observed,true)
 else if(value.kind!=='never-admitted'||owns(value,'observed'))throw new StorageInputError()
 if(bytes(value)>USAGE_BUDGET_RECORD_BYTES)throw new StorageInputError()
}
interface HeldReservation {request:UsageReservation;reserved:UsageBudgetReceipt;receipt:UsageBudgetReceipt;settlements:Map<string,{input:string;receipt:UsageBudgetReceipt}>}
interface HeldScope {scope:UsageBudgetScope;baseline:UsageAmounts;reservations:Map<string,HeldReservation>}
export interface UsageBudgetSeed {scope:UsageBudgetScope;/** Missing configured dimensions remain UNKNOWN. */committed:UsageAmounts}
export interface MemoryUsageBudgetOptions {
 seeds?:readonly UsageBudgetSeed[]
 /** Fault injection only; never call this capability recursively. afterChange may lose an ack. */
 beforeChange?:()=>void|Promise<void>;afterChange?:()=>void|Promise<void>
}
/** Explicit host-owned ephemeral conformance capability; no hidden global owner registry.
 * A fresh unseeded scope begins at known zero. Seeded missing usage NEVER means zero.
 * Hosts must share this instance across a resource and supply externally committed seed usage.
 */
export class MemoryUsageBudgetStore implements UsageBudgetStore {
 private scopes=new Map<string,HeldScope>()
 private tail:Promise<unknown>=Promise.resolve()
 private count=0
 constructor(readonly resource:string,private options:MemoryUsageBudgetOptions={}){
  id(resource)
  if((options.seeds?.length??0)>USAGE_BUDGET_SCOPES)throw new UsageBudgetCapacity()
  for(const seed of options.seeds??[]){
   object(seed,['scope','committed']);scope(seed.scope,resource);amounts(seed.committed)
   if(this.scopes.has(seed.scope.id))throw new UsageBudgetIdentityChanged()
   this.scopes.set(seed.scope.id,{scope:structuredClone(seed.scope),baseline:structuredClone(seed.committed),reservations:new Map()})
  }
 }
 private held(input:UsageBudgetScope):HeldScope|undefined{
  const held=this.scopes.get(input.id)
  if(held&&canonicalJSON(held.scope)!==canonicalJSON(input))throw new UsageBudgetIdentityChanged()
  return held
 }
 private queue<T>(action:()=>Promise<T>):Promise<T>{const next=this.tail.then(action);this.tail=next.catch(()=>undefined);return next}
 reserve(input:UsageReservation,signal?:AbortSignal):Promise<UsageReserveResult>{
  let frozen:UsageReservation
  try{signal?.throwIfAborted();frozen=structuredClone(input);request(frozen,this.resource)}catch(error){return Promise.reject(error)}
  return this.queue(async()=>{
   signal?.throwIfAborted()
   let held=this.held(frozen.scope)
   const old=held?.reservations.get(frozen.id)
   if(old){if(canonicalJSON(old.request)!==canonicalJSON(frozen))throw new StorageCommitMismatch();return {receipt:structuredClone(old.reserved),created:false}}
   if(this.count>=USAGE_BUDGET_RESERVATIONS||!held&&this.scopes.size>=USAGE_BUDGET_SCOPES)throw new UsageBudgetCapacity()
   if(!held)held={scope:structuredClone(frozen.scope),baseline:Object.fromEntries(Object.keys(frozen.scope.limits).map(key=>[key,0])),reservations:new Map()}
   for(const dimension of USAGE_DIMENSIONS){
    if(!owns(frozen.scope.limits,dimension))continue
    const baseline=held.baseline[dimension]
    if(baseline===undefined)throw new UsageBudgetUnknown()
    let consumed=baseline
    for(const row of held.reservations.values()){
     if(row.receipt.state==='never-admitted')continue
     const amount=row.receipt.observed[dimension]??row.request.upper[dimension]
     if(amount===undefined||!Number.isSafeInteger(consumed+amount))throw new UsageBudgetUnknown()
     consumed+=amount
    }
    const upper=frozen.upper[dimension]!
    if(!Number.isSafeInteger(consumed+upper)||consumed+upper>frozen.scope.limits[dimension]!)throw new UsageBudgetExceeded(dimension)
   }
   await this.options.beforeChange?.();signal?.throwIfAborted()
   const receipt:UsageBudgetReceipt={resource:this.resource,scope:frozen.scope.id,reservation:frozen.id,reservationDigest:digest(frozen),revision:crypto.randomUUID(),state:'reserved',observed:{}}
   held.reservations.set(frozen.id,{request:frozen,reserved:structuredClone(receipt),receipt,settlements:new Map()});this.scopes.set(frozen.scope.id,held);this.count++
   await this.options.afterChange?.()
   return {receipt:structuredClone(receipt),created:true}
  })
 }
 settle(input:UsageSettlement,signal?:AbortSignal):Promise<UsageBudgetReceipt>{
  let frozen:UsageSettlement
  try{signal?.throwIfAborted();frozen=structuredClone(input);settlement(frozen,this.resource)}catch(error){return Promise.reject(error)}
  return this.queue(async()=>{
   signal?.throwIfAborted()
   const row=this.held(frozen.scope)?.reservations.get(frozen.id)
   if(!row||row.receipt.reservationDigest!==frozen.reservationDigest)throw new UsageBudgetIdentityChanged()
   const content=canonicalJSON(frozen),prior=row.settlements.get(frozen.commitId)
   if(prior){if(prior.input!==content)throw new StorageCommitMismatch();return structuredClone(prior.receipt)}
   if(row.settlements.size>=5)throw new UsageBudgetCapacity()
   if(row.receipt.state==='never-admitted')throw new UsageBudgetIdentityChanged()
   const observed=structuredClone(row.receipt.observed)
   let state:UsageBudgetReceipt['state']
   if(frozen.kind==='never-admitted'){
    if(Object.keys(observed).length!==0)throw new UsageBudgetIdentityChanged()
    state='never-admitted'
   }else{
    const counted=row.request.kind==='model'?'modelRequests':'providerInvocations'
    if(owns(frozen.observed,counted)&&frozen.observed[counted]!==1)throw new UsageBudgetIdentityChanged()
    for(const dimension of Object.keys(frozen.observed) as UsageDimension[]){
     const value=frozen.observed[dimension]!,upper=row.request.upper[dimension]
     if(upper===undefined||value>upper||observed[dimension]!==undefined&&observed[dimension]!==value)throw new UsageBudgetIdentityChanged()
     observed[dimension]=value
    }
    state=Object.keys(row.request.upper).every(key=>owns(observed,key))?'settled':'partial'
   }
   await this.options.beforeChange?.();signal?.throwIfAborted()
   const receipt:UsageBudgetReceipt={...row.receipt,revision:crypto.randomUUID(),state,observed}
   row.receipt=receipt;row.settlements.set(frozen.commitId,{input:content,receipt:structuredClone(receipt)})
   await this.options.afterChange?.()
   return structuredClone(receipt)
  })
 }
 async readReservation(input:UsageBudgetScope,reservation:string,signal?:AbortSignal):Promise<UsageReservationHead|undefined>{
  signal?.throwIfAborted();scope(input,this.resource);id(reservation)
  // Observational read; never resumes, reserves, settles or turns a past receipt into authority.
  const held=this.held(input)?.reservations.get(reservation)
  return held?structuredClone({request:held.request,receipt:held.receipt}):undefined
 }
}
