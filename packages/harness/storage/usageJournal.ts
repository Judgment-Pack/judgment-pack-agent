import {readUsageJSON,usageJSONCount} from './usageJSON'
import {readDescriptorJSON} from '../descriptorSnapshot'
import {canonicalJSON} from '../context/chat'
import {sha256} from '../records'
import {storageId} from './common'
import {USAGE_DIMENSIONS,UsageBudgetCapacity,UsageBudgetExceeded,UsageBudgetIdentityChanged,UsageBudgetUnknown,UsageBudgetUnsupported,type UsageAmounts,type UsageBudgetReceipt,type UsageBudgetScope,type UsageBudgetStore,type UsageDimension,type UsageReservation} from './usageBudget'

export interface UsageBoundRequest {
 kind:'model'|'agent';family:string;connection:string;connectionRevision:string;model:string
 /** Detached final encoded provider body, after every tier/fallback rewrite. */
 request:Uint8Array;encodedOutputMaximum?:number
}
/** Host-owned conservative token bound capability; it is not a token usage estimate. */
export interface UsageBudgetInput {store:UsageBudgetStore;scope:UsageBudgetScope;bounder?:(request:UsageBoundRequest,signal?:AbortSignal)=>Promise<UsageAmounts>}
export interface UsageJournalEntry {reservation:UsageReservation;receipt?:UsageBudgetReceipt;settlement?:UsageBudgetReceipt;state:'reserved'|'claimed'|'reported'|'unknown'|'never-admitted';observed?:UsageAmounts}
export interface UsageBudgetRefusal {id:string;execution:string;role:string;task?:string;requestDigest:string;name:'UsageBudgetExceeded'|'UsageBudgetUnknown'|'UsageBudgetUnsupported'|'UsageBudgetIdentityChanged'|'UsageBudgetCapacity';dimension?:UsageDimension}
export interface UsageBudgetJournal {version:1;scope:UsageBudgetScope;entries:UsageJournalEntry[];refusals:UsageBudgetRefusal[]}
export interface UsageTotal {known:number;complete:boolean}
export type UsageTotals=Record<UsageDimension,UsageTotal>
const digest=(value:unknown)=>`sha256:${sha256(canonicalJSON(value))}`
const object=(v:unknown):v is Record<string,unknown>=>!!v&&typeof v==='object'&&!Array.isArray(v)
const exact=(v:Record<string,unknown>,keys:readonly string[])=>Object.keys(v).every(k=>keys.includes(k))
const validID=(v:unknown)=>storageId(v)&&new TextEncoder().encode(v).length<=100
const isDigest=(v:unknown)=>typeof v==='string'&&/^sha256:[a-f0-9]{64}$/.test(v)
const has=(o:object,k:PropertyKey)=>Object.hasOwn(o,k)
export function validUsageAmounts(v:unknown):v is UsageAmounts{return object(v)&&exact(v,USAGE_DIMENSIONS)&&Object.values(v).every(n=>Number.isSafeInteger(n)&&Number(n)>=0)}
export function validUsageScope(v:unknown):v is UsageBudgetScope{return object(v)&&exact(v,['resource','id','revision','limits'])&&['resource','id','revision'].every(k=>validID(v[k]))&&validUsageAmounts(v.limits)&&Object.keys(v.limits).length>0}
function validReservation(v:unknown,scope:UsageBudgetScope):v is UsageReservation {
 if(!object(v)||!exact(v,['scope','id','root','execution','role','task','kind','requestDigest','connection','connectionRevision','model','upper'])||canonicalJSON(v.scope)!==canonicalJSON(scope)||!['id','root','execution','role','connection','connectionRevision','model'].every(k=>validID(v[k]))||v.task!==undefined&&!validID(v.task)||!['model','agent'].includes(String(v.kind))||!isDigest(v.requestDigest)||!validUsageAmounts(v.upper)||Object.keys(scope.limits).some(k=>!has(v.upper as object,k)))return false
 const upper=v.upper
 return v.kind==='model'?(!has(upper,'modelRequests')||upper.modelRequests===1)&&(!has(upper,'providerInvocations')||upper.providerInvocations===0):(!has(upper,'providerInvocations')||upper.providerInvocations===1)&&!has(scope.limits,'modelRequests')
}
export function validUsageReceipt(v:unknown,reservation:UsageReservation):v is UsageBudgetReceipt {
 if(!object(v)||!exact(v,['resource','scope','reservation','reservationDigest','revision','state','observed'])||v.resource!==reservation.scope.resource||v.scope!==reservation.scope.id||v.reservation!==reservation.id||v.reservationDigest!==digest(reservation)||!validID(v.revision)||!['reserved','partial','settled','never-admitted'].includes(String(v.state))||!validUsageAmounts(v.observed))return false
 const keys=Object.keys(v.observed) as UsageDimension[]
 const observed=v.observed
 if(keys.some(k=>reservation.upper[k]===undefined||observed[k]!>reservation.upper[k]!))return false
 return v.state==='reserved'||v.state==='never-admitted'?keys.length===0:v.state==='settled'?Object.keys(reservation.upper).every(k=>has(v.observed as object,k)):keys.length>0&&Object.keys(reservation.upper).some(k=>!has(v.observed as object,k))
}
export function validUsageJournal(v:unknown):v is UsageBudgetJournal {
 if(!object(v)||!exact(v,['version','scope','entries','refusals'])||v.version!==1||!validUsageScope(v.scope)||!Array.isArray(v.entries)||v.entries.length>400||!Array.isArray(v.refusals)||v.refusals.length>100)return false
 const ids=new Set<string>()
 for(const entry of v.entries){
  if(!object(entry)||!exact(entry,['reservation','receipt','settlement','state','observed'])||!validReservation(entry.reservation,v.scope)||ids.has(entry.reservation.id)||!['reserved','claimed','reported','unknown','never-admitted'].includes(String(entry.state)))return false
  const reservation=entry.reservation
  ids.add(reservation.id)
  if(entry.receipt!==undefined&&(!validUsageReceipt(entry.receipt,entry.reservation)||entry.receipt.state!=='reserved'))return false
  if(entry.state!=='unknown'&&entry.receipt===undefined||entry.settlement!==undefined&&!validUsageReceipt(entry.settlement,entry.reservation)||entry.observed!==undefined&&!validUsageAmounts(entry.observed))return false
  if(entry.state==='reported'&&entry.observed===undefined||entry.observed!==undefined&&(!entry.receipt||!['reported','unknown'].includes(String(entry.state))||(entry.observed as UsageAmounts)[reservation.kind==='model'?'modelRequests':'providerInvocations']!==1))return false
  if(reservation.kind==='agent'&&entry.observed!==undefined&&has(entry.observed,'modelRequests'))return false
  if(entry.settlement!==undefined&&entry.settlement.state!=='never-admitted'&&(entry.observed===undefined||Object.entries(entry.settlement.observed).some(([k,n])=>(entry.observed as UsageAmounts)[k as UsageDimension]!==n)))return false
  if(entry.observed!==undefined&&Object.entries(entry.observed).some(([k,n])=>(has(reservation.scope.limits,k)||k==='modelRequests'||k==='providerInvocations')&&Number(n)>reservation.upper[k as UsageDimension]!))return false
  if(entry.state==='never-admitted'&&(entry.settlement===undefined||entry.settlement.state!=='never-admitted'||entry.observed!==undefined))return false
 }
 for(const row of v.refusals)if(!object(row)||!exact(row,['id','execution','role','task','requestDigest','name','dimension'])||!['id','execution','role'].every(k=>validID(row[k]))||row.task!==undefined&&!validID(row.task)||!isDigest(row.requestDigest)||!['UsageBudgetExceeded','UsageBudgetUnknown','UsageBudgetUnsupported','UsageBudgetIdentityChanged','UsageBudgetCapacity'].includes(String(row.name))||row.dimension!==undefined&&!USAGE_DIMENSIONS.includes(row.dimension as UsageDimension))return false
 return true
}
/** Root-only unique attempt facts: childUsage is deliberately not summed a second time. */
export function usageTotals(journal:UsageBudgetJournal):UsageTotals {
 if(!validUsageJournal(journal))throw new UsageBudgetIdentityChanged()
 const totals=Object.fromEntries(USAGE_DIMENSIONS.map(k=>[k,{known:0,complete:true}])) as UsageTotals
 for(const row of journal.entries){
  if(row.state==='never-admitted')continue
  for(const key of USAGE_DIMENSIONS){
   // A direct model attempt contains no provider-managed invocation. Opaque native physical requests and token omissions stay unknown.
   const absentKind=key==='providerInvocations'&&row.reservation.kind==='model'
   const n=row.observed?.[key]
   if(n===undefined){if(!absentKind)totals[key].complete=false;continue}
   if(!Number.isSafeInteger(totals[key].known+n))throw new UsageBudgetUnknown()
   totals[key].known+=n
  }
 }
 return totals
}
export type UsageDispatchFacts={execution:string;role:string;task?:string;source:string;tool:string;descriptor:string}&UsageBoundRequest
interface Ticket {entry:UsageJournalEntry;key:string}
/** Internal owner. All methods must run inside the original RunWriter queue/mutex. */
export class UsageJournalOwner {
 readonly #store:UsageBudgetStore
 readonly #scope:UsageBudgetScope
 readonly #bounder:UsageBudgetInput['bounder']
 readonly #tickets=new WeakMap<object,Ticket>()
 readonly #journal:UsageBudgetJournal
 constructor(input:UsageBudgetInput,readonly root:string,retained?:UsageBudgetJournal){
  if(!input?.store||!validUsageScope(input.scope)||input.store.resource!==input.scope.resource)throw new UsageBudgetIdentityChanged()
  this.#store=input.store;this.#scope=structuredClone(input.scope);this.#bounder=input.bounder
  if(retained&&!validUsageJournal(retained)||retained&&(canonicalJSON(retained.scope)!==canonicalJSON(this.#scope)||retained.entries.some(row=>row.reservation.root!==root)))throw new UsageBudgetIdentityChanged()
  this.#journal=retained?structuredClone(retained):{version:1,scope:structuredClone(this.#scope),entries:[],refusals:[]}
  for(const row of this.#journal.entries)if(row.state==='reserved'||row.state==='claimed')row.state='unknown'
 }
 async reconcile(signal?:AbortSignal):Promise<void>{
  for(const row of this.#journal.entries){
   const head=await this.#store.readReservation(this.#scope,row.reservation.id,signal)
   if(!head||canonicalJSON(head.request)!==canonicalJSON(row.reservation)||!validUsageReceipt(head.receipt,row.reservation)||head.receipt.state==='never-admitted'&&row.state!=='never-admitted')throw new UsageBudgetIdentityChanged()
   for(const [key,n] of Object.entries(row.settlement?.observed??{}))if(head.receipt.observed[key as UsageDimension]!==n)throw new UsageBudgetIdentityChanged()
   for(const [key,n] of Object.entries(head.receipt.observed))if(row.observed?.[key as UsageDimension]!==undefined&&row.observed[key as UsageDimension]!==n)throw new UsageBudgetIdentityChanged()
  }
 }
 matches(input:UsageBudgetInput):boolean{return input.store===this.#store&&canonicalJSON(input.scope)===canonicalJSON(this.#scope)}
 snapshot():UsageBudgetJournal{return structuredClone(this.#journal)}
 refusal(facts:UsageDispatchFacts,error:unknown){
  if(this.#journal.refusals.length>=100)throw new UsageBudgetCapacity()
  const name=error instanceof UsageBudgetExceeded?'UsageBudgetExceeded':error instanceof UsageBudgetUnsupported?'UsageBudgetUnsupported':error instanceof UsageBudgetIdentityChanged?'UsageBudgetIdentityChanged':error instanceof UsageBudgetCapacity?'UsageBudgetCapacity':'UsageBudgetUnknown'
  this.#journal.refusals.push({id:crypto.randomUUID(),execution:facts.execution,role:facts.role,...(facts.task?{task:facts.task}:{}),requestDigest:`sha256:${sha256(facts.descriptor)}`,name,...(error instanceof UsageBudgetExceeded?{dimension:error.dimension}:{})})
 }
 async reserve(id:string,facts:UsageDispatchFacts,signal?:AbortSignal):Promise<object>{
  if(!usageDescriptorMatches(facts))throw new UsageBudgetUnsupported()
  if(!validID(facts.connection)||!validID(facts.connectionRevision)||!validID(facts.model))throw new UsageBudgetUnsupported()
  if(this.#journal.entries.some(row=>row.state==='unknown'))throw new UsageBudgetUnknown()
  if(this.#journal.entries.length>=400)throw new UsageBudgetCapacity()
  const upper:UsageAmounts={modelRequests:facts.kind==='model'?1:0,providerInvocations:facts.kind==='agent'?1:0}
  if(facts.kind==='agent'&&(has(this.#scope.limits,'modelRequests')||has(this.#scope.limits,'inputTokens')||has(this.#scope.limits,'outputTokens')))throw new UsageBudgetUnsupported()
  if(has(this.#scope.limits,'inputTokens')||has(this.#scope.limits,'outputTokens')){
   if(!this.#bounder||!['openai-compatible','anthropic','gemini'].includes(facts.family))throw new UsageBudgetUnsupported()
   const maximum=has(this.#scope.limits,'outputTokens')?encodedUsageOutputMaximum(facts.family,facts.request):undefined
   if(maximum!==undefined&&facts.encodedOutputMaximum!==undefined&&maximum!==facts.encodedOutputMaximum)throw new UsageBudgetUnsupported()
   const bounds=structuredClone(await this.#bounder({kind:facts.kind,family:facts.family,connection:facts.connection,connectionRevision:facts.connectionRevision,model:facts.model,request:facts.request.slice(),...(maximum===undefined?{}:{encodedOutputMaximum:maximum})},signal))
   if(!validUsageAmounts(bounds))throw new UsageBudgetUnsupported()
   for(const key of ['inputTokens','outputTokens'] as const)if(has(this.#scope.limits,key)){if(bounds[key]===undefined||key==='outputTokens'&&bounds[key]!<maximum!)throw new UsageBudgetUnsupported();upper[key]=bounds[key]}
  }
  const reservation:UsageReservation={scope:structuredClone(this.#scope),id,root:this.root,execution:facts.execution,role:facts.role,...(facts.task?{task:facts.task}:{}),kind:facts.kind,requestDigest:`sha256:${sha256(facts.descriptor)}`,connection:facts.connection,connectionRevision:facts.connectionRevision,model:facts.model,upper}
  if(!validReservation(reservation,this.#scope))throw new UsageBudgetIdentityChanged()
  const entry:UsageJournalEntry={reservation,state:'unknown'};this.#journal.entries.push(entry)
  let result:Awaited<ReturnType<UsageBudgetStore['reserve']>>
  try{result=await this.#store.reserve(reservation,signal)}catch(error){
   // These specified refusals establish no mutation for this Reserve. Unknown failures retain the row.
   if(error instanceof UsageBudgetExceeded||error instanceof UsageBudgetUnsupported||error instanceof UsageBudgetIdentityChanged||error instanceof UsageBudgetCapacity)this.#journal.entries.pop()
   throw error
  }
  if(!result||result.created!==true||!validUsageReceipt(result.receipt,reservation)||result.receipt.state!=='reserved')throw new UsageBudgetUnknown()
  entry.receipt=structuredClone(result.receipt);entry.state='reserved'
  const token=Object.freeze({});this.#tickets.set(token,{entry,key:canonicalJSON({execution:facts.execution,role:facts.role,task:facts.task??null,source:facts.source,tool:facts.tool,descriptor:facts.descriptor,kind:facts.kind})});return token
 }
 async observe(id:string,observed:UsageAmounts,evidence:string,signal?:AbortSignal):Promise<void>{
  const row=this.#journal.entries.find(row=>row.reservation.id===id)
  if(!row||row.state!=='claimed'||!validUsageAmounts(observed)||!isDigest(evidence)||Object.entries(observed).some(([key,n])=>(has(row.reservation.scope.limits,key)||key==='modelRequests'||key==='providerInvocations')&&Number(n)>row.reservation.upper[key as UsageDimension]!))throw new UsageBudgetIdentityChanged()
  const count=row.reservation.kind==='model'?'modelRequests':'providerInvocations'
  if(observed[count]!==1)throw new UsageBudgetIdentityChanged()
  row.observed=structuredClone(observed);row.state='reported'
  const limited=Object.fromEntries(Object.entries(observed).filter(([key])=>has(row.reservation.upper,key))) as UsageAmounts
  try{
  const receipt=await this.#store.settle({scope:this.#scope,id,reservationDigest:row.receipt!.reservationDigest,commitId:crypto.randomUUID(),evidence,kind:'reported',observed:limited},signal)
  if(!validUsageReceipt(receipt,row.reservation)||receipt.state!=='partial'&&receipt.state!=='settled'||Object.entries(limited).some(([key,n])=>receipt.observed[key as UsageDimension]!==n))throw new UsageBudgetUnknown()
  row.settlement=structuredClone(receipt)
  }catch(error){row.state='unknown';throw error}
 }
 unknown(id:string){const row=this.#journal.entries.find(row=>row.reservation.id===id);if(!row||row.state!=='claimed')throw new UsageBudgetIdentityChanged();row.state='unknown'}
 consume(token:object,facts:Pick<UsageDispatchFacts,'execution'|'role'|'task'|'source'|'tool'|'descriptor'|'kind'>):string{
  const held=this.#tickets.get(token),key=canonicalJSON({...facts,task:facts.task??null})
  if(!held||held.key!==key||held.entry.state!=='reserved')throw new UsageBudgetIdentityChanged()
  this.#tickets.delete(token);held.entry.state='claimed';return held.entry.reservation.id
 }
}

/** Inspect the final actual body, including conflicting supported token fields. Never infer an unknown wire protocol. */
export function encodedUsageOutputMaximum(family:string,request:Uint8Array):number {
 let body:unknown
 try{body=readUsageJSON(new TextDecoder('utf-8',{fatal:true}).decode(request))}catch{throw new UsageBudgetUnsupported()}
 if(!object(body))throw new UsageBudgetUnsupported()
 let fields:unknown[]
 if(family==='openai-compatible')fields=['max_tokens','max_completion_tokens','max_output_tokens'].filter(k=>has(body,k)).map(k=>body[k])
 else if(family==='anthropic')fields=[body.max_tokens]
 else if(family==='gemini'&&object(body.generationConfig))fields=[body.generationConfig.maxOutputTokens]
 else throw new UsageBudgetUnsupported()
 try{const counts=fields.map(usageJSONCount);if(!counts.length||counts.some(n=>n<1))throw new UsageBudgetUnsupported();return Math.max(...counts)}catch{throw new UsageBudgetUnsupported()}
}

function usageDescriptorMatches(facts:UsageDispatchFacts):boolean {
 try{const request=new TextDecoder('utf-8',{fatal:true}).decode(facts.request);if(facts.kind==='agent')return request===facts.descriptor;const descriptor=readDescriptorJSON(facts.descriptor);return object(descriptor)&&typeof descriptor.body==='string'&&descriptor.body===request}catch{return false}
}
