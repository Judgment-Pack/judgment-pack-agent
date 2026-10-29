import {AIFailureError,failure} from '../failures'
import {canonicalJSON} from '../context/chat'
import {sha256} from '../records'
import {storageId} from './common'
import {StorageConformanceFailure} from './conformance'
import type {StorageRevision} from './common'
import type {RunCommit,RunReceipt,RunStore} from './run'
import type {ChatEntry,ChatPatch,ChatStore} from './chat'
import type {ContextSnapshotHead,ContextSnapshotUpdate,HistoryRunStore,RunHistoryCommit,RunHistoryReceipt} from './history'
/* Run leases and fencing (ADR-0025 decision 1, #223; contract/schemas/run-lease.json).
 * The vocabulary is the ADR's: a lease has an owner, an authoritative expiry decided by the store clock and an exact,
 * monotonically increasing epoch. The fence ({resource, owner, epoch}) is the evidence presented at every checkpoint
 * commit and checked by the effect host at every admission. A lease grants neither tool permission nor approval. */
export const LEASE_RECORD_VERSION=1 as const
/** The largest epoch: 18 decimal digits, exact in a signed 64-bit integer and never carried as a JSON number. */
export const LEASE_EPOCH_MAX='999999999999999999'
/** A requested lease or renewal duration, in milliseconds of the store clock. */
export const LEASE_DURATION_MAX_MS=86_400_000
export type LeaseEpoch=string
export type LeaseLifetime='durable'|'process'
export interface RunLease {version:1;resource:string;owner:string;epoch:LeaseEpoch;expiry:string;state:'held'|'released'}
export interface LeaseFence {version:1;resource:string;owner:string;epoch:LeaseEpoch}
export interface LeaseAcquire {resource:string;owner:string;expectedRevision:StorageRevision|null;durationMs:number}
export type LeaseRefusalReason='invalid'|'no-lease'|'held'|'revision-changed'|'stale-epoch'|'other-owner'|'expired'|'released'|'epoch-exhausted'|'resource-mismatch'|'already-admitted'
export type LeaseCapabilityReason='store-unsupported'|'store-lifetime'|'effect-host-unfenced'
export const LEASE_REFUSAL_REASONS:readonly LeaseRefusalReason[]=['invalid','no-lease','held','revision-changed','stale-epoch','other-owner','expired','released','epoch-exhausted','resource-mismatch','already-admitted']
export const LEASE_CAPABILITY_REASONS:readonly LeaseCapabilityReason[]=['store-unsupported','store-lifetime','effect-host-unfenced']
/** A named lease or fence refusal. Nothing was committed or admitted, except already-admitted: an effect host admitted
 * that idempotency identity before, so its outcome is unknown and it is never performed again. */
export class LeaseRefused extends AIFailureError {
 constructor(readonly reason:LeaseRefusalReason){super({...(reason==='already-admitted'?failure('unknown','tool','unknown'):failure('stale-state','storage','not-started')),detail:`LeaseRefused: ${reason}.`});this.name='LeaseRefused'}
 record():{version:1;reason:LeaseRefusalReason}{return {version:1,reason:this.reason}}
}
/** A store or effect host that cannot give the guarantee refuses unattended admission by name; it never pretends. */
export class LeaseCapabilityRefused extends AIFailureError {
 constructor(readonly reason:LeaseCapabilityReason){super({...failure('configuration','storage','not-started'),detail:`LeaseCapabilityRefused: ${reason}.`});this.name='LeaseCapabilityRefused'}
 record():{version:1;reason:LeaseCapabilityReason}{return {version:1,reason:this.reason}}
}
const EPOCH=/^[1-9][0-9]{0,17}$/,EXPIRY=/^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{3}Z$/,DIGEST=/^sha256:[a-f0-9]{64}$/
const object=(v:unknown):v is Record<string,unknown>=>v!==null&&typeof v==='object'&&!Array.isArray(v)&&(Object.getPrototypeOf(v)===Object.prototype||Object.getPrototypeOf(v)===null)
const exact=(v:Record<string,unknown>,keys:readonly string[])=>Object.keys(v).length===keys.length&&keys.every(k=>Object.hasOwn(v,k))
const invalid=():never=>{throw new LeaseRefused('invalid')}
/** An exact epoch: a canonical base-10 string from 1 to LEASE_EPOCH_MAX. A JSON number, sign, fraction, exponent,
 * leading zero or space is not an epoch. */
export const validLeaseEpoch=(value:unknown):value is LeaseEpoch=>typeof value==='string'&&EPOCH.test(value)
/** The next epoch: 1 for a resource never leased, otherwise exactly one more. Never reused or decremented. */
export function nextLeaseEpoch(previous?:LeaseEpoch):LeaseEpoch{
 if(previous===undefined)return '1'
 if(!validLeaseEpoch(previous))return invalid()
 if(previous===LEASE_EPOCH_MAX)throw new LeaseRefused('epoch-exhausted')
 return (BigInt(previous)+1n).toString()
}
export function compareLeaseEpochs(a:LeaseEpoch,b:LeaseEpoch):-1|0|1{if(!validLeaseEpoch(a)||!validLeaseEpoch(b))return invalid();const x=BigInt(a),y=BigInt(b);return x<y?-1:x>y?1:0}
const validExpiry=(value:unknown):value is string=>{if(typeof value!=='string'||!EXPIRY.test(value))return false;const ms=Date.parse(value);return Number.isSafeInteger(ms)&&new Date(ms).toISOString()===value}
function expiryAt(ms:number):string{const text=Number.isSafeInteger(ms)?new Date(ms).toISOString():'';return validExpiry(text)?text:invalid()}
const requireDuration=(value:unknown):number=>Number.isSafeInteger(value)&&Number(value)>=1&&Number(value)<=LEASE_DURATION_MAX_MS?Number(value):invalid()
export function readLeaseFence(value:unknown):LeaseFence{
 if(!object(value)||!exact(value,['version','resource','owner','epoch'])||value.version!==1||!storageId(value.resource)||!storageId(value.owner)||!validLeaseEpoch(value.epoch))return invalid()
 return {version:1,resource:value.resource,owner:value.owner,epoch:value.epoch}
}
export function readRunLease(value:unknown):RunLease{
 if(!object(value)||!exact(value,['version','resource','owner','epoch','expiry','state'])||value.version!==1||!storageId(value.resource)||!storageId(value.owner)||!validLeaseEpoch(value.epoch)||!validExpiry(value.expiry)||value.state!=='held'&&value.state!=='released')return invalid()
 return {version:1,resource:value.resource,owner:value.owner,epoch:value.epoch,expiry:value.expiry,state:value.state}
}
export function readLeaseRefusal(value:unknown):{version:1;reason:LeaseRefusalReason}{
 if(!object(value)||!exact(value,['version','reason'])||value.version!==1||!LEASE_REFUSAL_REASONS.includes(value.reason as LeaseRefusalReason))return invalid()
 return {version:1,reason:value.reason as LeaseRefusalReason}
}
export function readLeaseCapabilityRefusal(value:unknown):{version:1;reason:LeaseCapabilityReason}{
 if(!object(value)||!exact(value,['version','reason'])||value.version!==1||!LEASE_CAPABILITY_REASONS.includes(value.reason as LeaseCapabilityReason))return invalid()
 return {version:1,reason:value.reason as LeaseCapabilityReason}
}
export const fenceOf=(lease:RunLease):LeaseFence=>({version:1,resource:lease.resource,owner:lease.owner,epoch:lease.epoch})

/** The lease port a store kind implements once per resource kind (desk #609). Every transition is atomic and checked
 * against the store's own clock; a caller's clock never extends a lease. */
export interface LeaseStore {
 readonly leaseVersion:1
 /** durable: leases and epochs survive the store's process. process: they end with it (memory test kinds). */
 readonly leaseLifetime:LeaseLifetime
 /** Compares expectedRevision with the resource's current revision. Refuses held while an unexpired lease is held. */
 acquireLease(request:LeaseAcquire,signal?:AbortSignal):Promise<RunLease>
 /** Current owner and epoch only, before expiry. An expired lease is never revived. */
 renewLease(fence:LeaseFence,durationMs:number,signal?:AbortSignal):Promise<RunLease>
 /** The epoch is retained, so the next acquisition is strictly greater. */
 releaseLease(fence:LeaseFence,signal?:AbortSignal):Promise<RunLease>
 readLease(resource:string,signal?:AbortSignal):Promise<RunLease|undefined>
 /** The authority an effect host checks a fence against; throws LeaseRefused with the named reason. */
 checkFence(fence:LeaseFence,signal?:AbortSignal):Promise<void>
}
export interface LeaseRunStore extends RunStore,LeaseStore {
 /** commit, checking the fence in the same transaction as the revision. */
 commitFenced(input:RunCommit,fence:LeaseFence,signal?:AbortSignal):Promise<RunReceipt>
 /** Required when the store also has the history capability. */
 commitHistoryFenced?(input:RunHistoryCommit,fence:LeaseFence,signal?:AbortSignal):Promise<RunHistoryReceipt>
 /** Required with the history capability: a leased run's chat snapshot commits only under its current fence (the fence
  * names the run; the snapshot names its chat). A driver that lost its lease cannot release the turn its takeover resumes. */
 commitContextSnapshotFenced?(input:ContextSnapshotUpdate,commitId:string,fence:LeaseFence,signal?:AbortSignal):Promise<ContextSnapshotHead>
}
export interface LeaseChatStore extends ChatStore,LeaseStore {
 updateFenced(id:string,patch:ChatPatch,expectedRevision:StorageRevision,fence:LeaseFence,signal?:AbortSignal):Promise<ChatEntry>
}

/** The lease transitions, held in memory. MemoryRunStore and MemoryChatStore call it inside their own serialized
 * transactions; a host store kind implements the same transitions against its own durable rows. */
export class LeaseTable {
 private rows=new Map<string,RunLease>()
 constructor(private readonly clock:()=>Date=()=>new Date()){}
 private now():number{const at=this.clock();const ms=at instanceof Date?at.getTime():NaN;return Number.isSafeInteger(ms)?ms:invalid()}
 read(resource:string):RunLease|undefined{if(!storageId(resource))return invalid();const row=this.rows.get(resource);return row?{...row}:undefined}
 acquire(request:LeaseAcquire,currentRevision:StorageRevision|null):RunLease{
  if(!object(request)||!exact(request,['resource','owner','expectedRevision','durationMs'])||!storageId(request.resource)||!storageId(request.owner)||request.expectedRevision!==null&&!storageId(request.expectedRevision))return invalid()
  const duration=requireDuration(request.durationMs),now=this.now()
  if(request.expectedRevision!==currentRevision)throw new LeaseRefused('revision-changed')
  const row=this.rows.get(request.resource)
  if(row&&row.state==='held'&&now<Date.parse(row.expiry))throw new LeaseRefused('held')
  const lease:RunLease={version:1,resource:request.resource,owner:request.owner,epoch:nextLeaseEpoch(row?.epoch),expiry:expiryAt(now+duration),state:'held'}
  this.rows.set(lease.resource,lease);return {...lease}
 }
 /** Throws the named reason unless the fence is current: this epoch, this owner, held and unexpired by the store clock.
  * An expiry equal to the store's time is expired. */
 check(fence:LeaseFence):RunLease{
  const held=readLeaseFence(fence),now=this.now(),row=this.rows.get(held.resource)
  if(!row)throw new LeaseRefused('no-lease')
  if(row.epoch!==held.epoch)throw new LeaseRefused('stale-epoch')
  if(row.owner!==held.owner)throw new LeaseRefused('other-owner')
  if(row.state==='released')throw new LeaseRefused('released')
  if(now>=Date.parse(row.expiry))throw new LeaseRefused('expired')
  return row
 }
 renew(fence:LeaseFence,durationMs:number):RunLease{const duration=requireDuration(durationMs),row=this.check(fence);row.expiry=expiryAt(this.now()+duration);return {...row}}
 release(fence:LeaseFence):RunLease{const row=this.check(fence);row.state='released';return {...row}}
}

/** The stable idempotency identity of one effect. It carries no epoch, so it is the same after a takeover. */
export interface EffectOperation {run:string;operation:string;source:string;tool:string;requestDigest:string}
export interface EffectAdmission {version:1;fence:LeaseFence;operation:EffectOperation;idempotencyKey:string}
function readOperation(value:unknown):EffectOperation{
 if(!object(value)||!exact(value,['run','operation','source','tool','requestDigest'])||!storageId(value.run)||!storageId(value.operation)||!storageId(value.source)||!storageId(value.tool)||typeof value.requestDigest!=='string'||!DIGEST.test(value.requestDigest))return invalid()
 return {run:value.run,operation:value.operation,source:value.source,tool:value.tool,requestDigest:value.requestDigest}
}
/** SHA-256 of the canonical JSON of the operation alone. */
export const effectIdempotencyKey=(operation:EffectOperation):string=>`sha256:${sha256(canonicalJSON(readOperation(operation)))}`
export function effectAdmission(fence:LeaseFence,operation:EffectOperation):EffectAdmission{const held=readOperation(operation);return {version:1,fence:readLeaseFence(fence),operation:held,idempotencyKey:effectIdempotencyKey(held)}}
export function readEffectAdmission(value:unknown):EffectAdmission{
 if(!object(value)||!exact(value,['version','fence','operation','idempotencyKey'])||value.version!==1)return invalid()
 const admission=effectAdmission(readLeaseFence(value.fence),readOperation(value.operation))
 return admission.idempotencyKey===value.idempotencyKey?admission:invalid()
}
/** The effect-host port. The host itself receives the fence and checks it at its own admission boundary, then performs
 * the effect: a client-side check or a store compare-and-set alone does not stop a duplicate external effect. */
export interface EffectHost {
 readonly effectFencing:'enforced'|'unsupported'
 dispatch<T>(admission:EffectAdmission,perform:(signal?:AbortSignal)=>Promise<T>,signal?:AbortSignal):Promise<T>
}
/** Where an effect host checks a fence: the store's own lease authority, or an enforcement service in front of it. */
export interface LeaseAuthority {checkFence(fence:LeaseFence,signal?:AbortSignal):Promise<void>}
/** The reference effect host. Admission is serialized: the fence is checked against the authority and the idempotency
 * identity is recorded before the effect is performed. An identity admitted before is refused as already-admitted
 * whatever the epoch: its outcome is unknown until its own host reconciles it, and a takeover never replays it. */
export class ReferenceEffectHost implements EffectHost {
 readonly effectFencing='enforced' as const
 private readonly admitted=new Set<string>()
 private tail:Promise<unknown>=Promise.resolve()
 /** Every refusal this host gave, as its versioned record, in order. */
 readonly refusals:{version:1;reason:LeaseRefusalReason}[]=[]
 constructor(private readonly authority:LeaseAuthority){
  // #253: what admission relies on is sealed on the instance: its dispatch and declaration cannot be swapped afterwards.
  Object.defineProperty(this,'dispatch',{value:ReferenceEffectHost.prototype.dispatch,writable:false,configurable:false,enumerable:false})
  Object.defineProperty(this,'effectFencing',{value:'enforced',writable:false,configurable:false,enumerable:true})
  if(Object.getPrototypeOf(this)===ReferenceEffectHost.prototype)references.set(this,authority)
 }
 dispatch<T>(admission:EffectAdmission,perform:(signal?:AbortSignal)=>Promise<T>,signal?:AbortSignal):Promise<T>{
  const admitted=this.tail.then(async()=>{
   try{
    signal?.throwIfAborted()
    const held=readEffectAdmission(admission)
    if(held.fence.resource!==held.operation.run)throw new LeaseRefused('resource-mismatch')
    await this.authority.checkFence(held.fence,signal)
    if(this.admitted.has(held.idempotencyKey))throw new LeaseRefused('already-admitted')
    this.admitted.add(held.idempotencyKey)
   }catch(error){if(error instanceof LeaseRefused)this.refusals.push(error.record());throw error}
  })
  this.tail=admitted.catch(()=>undefined)
  return admitted.then(()=>perform(signal))
 }
}

/** #253: the evidence unattended admission accepts, bound to exact instances. A reference host is admitted with the
 * authority it was built over; a certified host is the package's sealed wrapper of the exact instance the kit exercised,
 * with the dispatch the kit checked and the authority it was certified for. A declaration of effectFencing alone is not
 * evidence, and evidence on one instance is not evidence on another or on a subclass. */
const references=new WeakMap<ReferenceEffectHost,LeaseAuthority>()
const certified=new WeakMap<EffectHost,{host:EffectHost;dispatch:EffectHost['dispatch'];authority:LeaseAuthority}>()
function provenAuthority(host:EffectHost):LeaseAuthority|undefined{
 if(host instanceof ReferenceEffectHost){const authority=references.get(host);return authority&&host.dispatch===ReferenceEffectHost.prototype.dispatch&&host.effectFencing==='enforced'?authority:undefined}
 const record=certified.get(host)
 return record&&record.host.dispatch===record.dispatch&&record.host.effectFencing==='enforced'?record.authority:undefined
}
interface KitAuthority extends LeaseAuthority {table:LeaseTable;failing:boolean;at:number}
function kitAuthority():KitAuthority{
 const kit:KitAuthority={at:Date.parse('2026-01-01T00:00:00.000Z'),failing:false,table:undefined as never,checkFence:async fence=>{if(kit.failing)throw new Error('The lease authority is unavailable.');kit.table.check(fence)}}
 kit.table=new LeaseTable(()=>new Date(kit.at));return kit
}
/** The checks, on the exact host instance given, against the kit's authority. */
async function exerciseEffectHost(host:EffectHost,kit:KitAuthority):Promise<void>{
 const fail=(property:string):never=>{throw new StorageConformanceFailure(property)}
 try{
  if(host.effectFencing!=='enforced')fail('host.declared')
  const op=(operation:string,run='host-run'):EffectOperation=>({run,operation,source:'host',tool:'effect',requestDigest:'sha256:'+'b'.repeat(64)})
  let performed=0
  const perform=async()=>{performed++;return 'performed'}
  const refuses=async(property:string,admission:EffectAdmission,reason:LeaseRefusalReason|'any')=>{
   const before=performed
   try{await host.dispatch(admission,perform)}catch(error){if(reason!=='any'&&!(error instanceof LeaseRefused&&error.reason===reason)||performed!==before)fail(property);return}
   fail(property)
  }
  const a=fenceOf(kit.table.acquire({resource:'host-run',owner:'worker-a',expectedRevision:null,durationMs:1000},null))
  // Unavailable ownership evidence closes dispatch: an authority that errors is never a pass.
  kit.failing=true
  await refuses('host.authority-unavailable',effectAdmission(a,op('op-0')),'any')
  kit.failing=false
  if(await host.dispatch(effectAdmission(a,op('op-1')),perform)!=='performed'||performed!==1)fail('host.admit.current')
  await refuses('host.already-admitted',effectAdmission(a,op('op-1')),'already-admitted')
  await refuses('host.other-owner',effectAdmission({...a,owner:'worker-x'},op('op-2')),'other-owner')
  await refuses('host.forged-epoch',effectAdmission({...a,epoch:'2'},op('op-3')),'stale-epoch')
  await refuses('host.resource-mismatch',effectAdmission(a,op('op-4','other-run')),'resource-mismatch')
  await refuses('host.invalid',{...effectAdmission(a,op('op-5')),idempotencyKey:effectAdmission(a,op('op-6')).idempotencyKey},'invalid')
  kit.at+=1000
  await refuses('host.expired',effectAdmission(a,op('op-7')),'expired')
  const b=fenceOf(kit.table.acquire({resource:'host-run',owner:'worker-b',expectedRevision:null,durationMs:1000},null))
  await refuses('host.stale-epoch',effectAdmission(a,op('op-8')),'stale-epoch')
  await refuses('host.takeover.already-admitted',effectAdmission(b,op('op-1')),'already-admitted')
  kit.table.release(b)
  await refuses('host.released',effectAdmission(b,op('op-9')),'released')
  if(performed!==1)fail('host.performed-once')
 }catch(error){if(error instanceof StorageConformanceFailure)throw error;throw new StorageConformanceFailure('host.operation')}
}
/** #223 review, #253: the effect-host conformance. The kit owns the lease authority it hands the factory and moves its
 * clock; the host must refuse, without performing, while the authority is unavailable; admit a current fence once; and
 * refuse, without performing, an identity it admitted before (also under a takeover's fence), another owner, a stale or
 * forged epoch, a fence for another run than the operation's, a key that is not its operation's, an expired lease and a
 * released one. */
export async function effectHostConformance(create:(authority:LeaseAuthority)=>EffectHost|Promise<EffectHost>):Promise<void>{
 const kit=kitAuthority()
 let host:EffectHost
 try{host=await create(kit)}catch{throw new StorageConformanceFailure('host.operation')}
 await exerciseEffectHost(host,kit)
}
/** Certify an effect host for unattended admission over `authority` (for unattended admission, the run's own store).
 * The factory is called once, over an authority the kit controls until the checks pass and that then answers from
 * `authority` for good; the exact instance the kit exercised is the one admitted, through a sealed wrapper that refuses
 * once the instance's dispatch is not the one the kit checked. */
export async function certifyEffectHost(create:(authority:LeaseAuthority)=>EffectHost|Promise<EffectHost>,authority:LeaseAuthority):Promise<EffectHost>{
 const kit=kitAuthority()
 let live=false
 const bound:LeaseAuthority={checkFence:(fence,signal)=>live?authority.checkFence(fence,signal):kit.checkFence(fence,signal)}
 let host:EffectHost
 try{host=await create(bound)}catch{throw new StorageConformanceFailure('host.operation')}
 const dispatch=host.dispatch
 await exerciseEffectHost(host,kit)
 if(host.dispatch!==dispatch||host.effectFencing!=='enforced')throw new LeaseCapabilityRefused('effect-host-unfenced')
 live=true
 const wrapper:EffectHost=Object.freeze({effectFencing:'enforced' as const,dispatch<T>(admission:EffectAdmission,perform:(signal?:AbortSignal)=>Promise<T>,signal?:AbortSignal):Promise<T>{
  if(host.dispatch!==dispatch)return Promise.reject(new LeaseCapabilityRefused('effect-host-unfenced'))
  return dispatch.call(host,admission,perform,signal) as Promise<T>
 }})
 certified.set(wrapper,{host,dispatch,authority})
 return wrapper
}
const leased=new WeakMap<object,{fence:LeaseFence;effectHost?:EffectHost}>()
const bind=(store:object,name:string)=>{const method=(store as Record<string,unknown>)[name];return typeof method==='function'?method.bind(store):undefined}
const HISTORY_METHODS=['stageHistoryObjects','readHistoryHead','readHistoryPage','readHistoryObject','loadContextSnapshot','commitContextSnapshot','commitHistoryMetadata','readHistoryMetadataPage'] as const
/** A view of a lease store whose every checkpoint commit presents this fence, checked by the store in the same
 * transaction. A non-read host or runtime dispatch through a writer on this view is admitted by effectHost with the
 * same fence; without one it refuses effect-host-unfenced. Use it as the RunStore of the durable run (and as the store
 * of its stored chat scope). The store keeps its own durability; the view adds no lifetime it does not have. */
export function fencedRunStore(store:RunStore,fence:LeaseFence,effectHost?:EffectHost):RunStore{
 const lease=store as Partial<LeaseRunStore>&Partial<HistoryRunStore>
 if(lease.leaseVersion!==1||typeof lease.commitFenced!=='function'||store.durability==='single-writer-callback'||lease.historyVersion===1&&(typeof lease.commitHistoryFenced!=='function'||typeof lease.commitContextSnapshotFenced!=='function'))throw new LeaseCapabilityRefused('store-unsupported')
 if(effectHost!==undefined&&effectHost.effectFencing!=='enforced')throw new LeaseCapabilityRefused('effect-host-unfenced')
 const held=readLeaseFence(fence)
 const view:Record<string,unknown>={durability:store.durability,load:bind(store,'load'),readEvents:bind(store,'readEvents'),commit:(input:RunCommit,signal?:AbortSignal)=>lease.commitFenced!(input,held,signal)}
 if(lease.historyVersion===1){view.historyVersion=1;for(const name of HISTORY_METHODS)view[name]=bind(store,name);view.commitHistory=(input:RunHistoryCommit,signal?:AbortSignal)=>lease.commitHistoryFenced!(input,held,signal);view.commitContextSnapshot=(input:ContextSnapshotUpdate,commitId:string,signal?:AbortSignal)=>lease.commitContextSnapshotFenced!(input,commitId,held,signal)}
 Object.freeze(view);leased.set(view,{fence:held,...(effectHost?{effectHost}:{})})
 return view as unknown as RunStore
}
/** A view of a chat store whose every update presents this fence. */
export function fencedChatStore(store:ChatStore,fence:LeaseFence):ChatStore{
 const lease=store as Partial<LeaseChatStore>
 if(lease.leaseVersion!==1||typeof lease.updateFenced!=='function'||store.consistency==='single-writer-legacy')throw new LeaseCapabilityRefused('store-unsupported')
 const held=readLeaseFence(fence)
 return Object.freeze({consistency:store.consistency,get:store.get.bind(store),list:store.list.bind(store),update:(id:string,patch:ChatPatch,expectedRevision:StorageRevision,signal?:AbortSignal)=>lease.updateFenced!(id,patch,expectedRevision,held,signal)}) as ChatStore
}
/** Unattended admission: only a store whose leases outlive its process and an effect host proven to enforce the fence
 * against that same store: a reference host built over it, or a host certifyEffectHost certified over it.
 * Anything else refuses by name before a dispatch; in particular the memory and callback kinds never qualify. */
export function admitUnattended(input:{store:RunStore;effectHost?:EffectHost;chatStore?:ChatStore}):void{
 for(const store of [input.store,input.chatStore]){
  if(store===undefined)continue
  const lease=store as Partial<LeaseRunStore&LeaseChatStore>
  if(lease.leaseVersion!==1||(store===input.store?typeof lease.commitFenced!=='function'||(store as RunStore).durability==='single-writer-callback':typeof lease.updateFenced!=='function'||(store as ChatStore).consistency==='single-writer-legacy'))throw new LeaseCapabilityRefused('store-unsupported')
  if(lease.leaseLifetime!=='durable')throw new LeaseCapabilityRefused('store-lifetime')
 }
 // #253: the host's evidence must be its own, and its authority the run's own store.
 if(!input.effectHost||input.effectHost.effectFencing!=='enforced'||provenAuthority(input.effectHost)!==(input.store as object))throw new LeaseCapabilityRefused('effect-host-unfenced')
}
/** admitUnattended, then the fenced view the unattended run uses as its RunStore. */
export function unattendedRunStore(input:{store:RunStore;fence:LeaseFence;effectHost:EffectHost}):RunStore{admitUnattended(input);return fencedRunStore(input.store,input.fence,input.effectHost)}
/** @internal The writer's admission of one non-read effect under the fence of the store it writes through. */
export function fencedEffect<T>(store:RunStore,operation:EffectOperation,perform:()=>Promise<T>,signal?:AbortSignal):Promise<T>{
 const lease=leased.get(store)
 if(!lease)return perform()
 if(!lease.effectHost)return Promise.reject(new LeaseCapabilityRefused('effect-host-unfenced'))
 let admission:EffectAdmission
 try{admission=effectAdmission(lease.fence,operation)}catch(error){return Promise.reject(error)}
 return lease.effectHost.dispatch(admission,()=>perform(),signal)
}
