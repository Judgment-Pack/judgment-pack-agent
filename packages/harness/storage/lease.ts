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
 constructor(readonly reason:LeaseCapabilityReason,why=''){super({...failure('configuration','storage','not-started'),detail:`LeaseCapabilityRefused: ${reason}.${why&&' '+why}`});this.name='LeaseCapabilityRefused'}
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
 /** `ceiling` (#225, store-clock milliseconds): the latest the lease may expire, the run's deadline or an approval's
  * expiry; the caller refuses before it when the ceiling is not after the store's time. */
 acquire(request:LeaseAcquire,currentRevision:StorageRevision|null,ceiling?:number):RunLease{
  if(!object(request)||!exact(request,['resource','owner','expectedRevision','durationMs'])||!storageId(request.resource)||!storageId(request.owner)||request.expectedRevision!==null&&!storageId(request.expectedRevision))return invalid()
  const duration=requireDuration(request.durationMs),now=this.now()
  if(request.expectedRevision!==currentRevision)throw new LeaseRefused('revision-changed')
  const row=this.rows.get(request.resource)
  if(row&&row.state==='held'&&now<Date.parse(row.expiry))throw new LeaseRefused('held')
  const lease:RunLease={version:1,resource:request.resource,owner:request.owner,epoch:nextLeaseEpoch(row?.epoch),expiry:expiryAt(Math.min(now+duration,ceiling??Infinity)),state:'held'}
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
 /** #225: a renewal never passes `ceiling`: renewing a lease renews neither the run's deadline nor an approval. */
 renew(fence:LeaseFence,durationMs:number,ceiling?:number):RunLease{const duration=requireDuration(durationMs),row=this.check(fence);row.expiry=expiryAt(Math.min(this.now()+duration,ceiling??Infinity));return {...row}}
 /** #225: a held lease is shortened to a deadline set while it is held; it is never lengthened. */
 lower(resource:string,ceiling:number):void{const row=this.rows.get(resource);if(row&&row.state==='held'&&Date.parse(row.expiry)>ceiling)row.expiry=expiryAt(ceiling)}
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
/** #226: why an effect host refused an operation at its admission boundary before admitting it: the connection's
 * credentials expired or were revoked. A bounded reference to the connection, the SHA-256 of the principal identity the
 * credentials acted as, the scope labels they held and the ones the operation needs. Never a credential, a token or a
 * provider's error: anything else is refused by the reader. */
export interface CredentialRefusal {cause:'expired'|'revoked';connection:string;principal:string;permissions:string[];requires:string[]}
/** A reference effect host's credential check for one operation, before it admits it: undefined while the connection's
 * credentials are current, or the refusal. A check that throws admits nothing and proves nothing. */
export type CredentialCheck=(operation:EffectOperation,signal?:AbortSignal)=>Promise<CredentialRefusal|undefined>
const CONNECTION=/^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$/,LABEL=/^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$/
/** UTF-8 byte order, the order Go compares strings in. */
function byteOrder(a:string,b:string):number{const x=new TextEncoder().encode(a),y=new TextEncoder().encode(b);for(let i=0;i<Math.min(x.length,y.length);i++)if(x[i]!==y[i])return x[i]!-y[i]!;return x.length-y.length}
/** Scope labels: at most 64, each 1 to 128 characters, sorted in UTF-8 byte order without repeats. */
export function validScopeLabels(value:unknown):value is string[]{
 if(!Array.isArray(value)||value.length>64||value.some(v=>typeof v!=='string'||!LABEL.test(v)))return false
 for(let i=1;i<value.length;i++)if(byteOrder(value[i-1],value[i])>=0)return false
 return true
}
export const validConnectionReference=(value:unknown):value is string=>typeof value==='string'&&CONNECTION.test(value)
/** Reads a credential refusal; undefined for anything else (a member the record does not name included). */
export function readCredentialRefusal(value:unknown):CredentialRefusal|undefined{
 if(!object(value)||!exact(value,['cause','connection','principal','permissions','requires'])||value.cause!=='expired'&&value.cause!=='revoked'||!validConnectionReference(value.connection)||typeof value.principal!=='string'||!DIGEST.test(value.principal)||!validScopeLabels(value.permissions)||!validScopeLabels(value.requires))return undefined
 return {cause:value.cause,connection:value.connection,principal:value.principal,permissions:[...value.permissions],requires:[...value.requires]}
}
/** The trusted, invocation-bound disposition (contract/schemas/run-reauth.json): the exact admission an effect host
 * refused, never admitted, and why. */
export interface ReauthDisposition {version:1;resource:string;operation:EffectOperation;idempotencyKey:string;fence:LeaseFence;cause:'expired'|'revoked';connection:string;principal:string;permissions:string[];requires:string[]}
/** #226: an effect host's refusal of one admission because the connection's credentials expired or were revoked. Its
 * failure is typed (authentication, tool, not-started) and its detail fixed: it carries no credential, token or provider
 * error. It is a re-authorization proof only when the package trusts it (reauthorizationOf). Otherwise it is an ordinary
 * failure: the writer records and reports it, like any failure of that shape, with outcome unknown and no detail, so the
 * typed failure on a dispatch row always means a refusal the package trusted. */
export class ReauthorizationRequired extends AIFailureError {
 constructor(){super({...failure('authentication','tool','not-started'),detail:'ReauthorizationRequired: the connection needs re-authorization; the operation was not admitted.'});this.name='ReauthorizationRequired'}
}
/** #226: a reference effect host's credential check threw. Nothing was admitted and nothing is proven, so the failure
 * says nothing else: whatever the check threw, a host-authored detail included, stays with the host. */
export class CredentialCheckFailed extends AIFailureError {
 constructor(){super({...failure('unknown','tool','unknown'),detail:'CredentialCheckFailed: the effect host could not check the credentials.'});this.name='CredentialCheckFailed'}
}
/** The failure only a trusted refusal may record on a dispatch row. */
export const isNeverAdmittedFailure=(recorded:unknown):boolean=>object(recorded)&&recorded.code==='authentication'&&recorded.source==='tool'&&recorded.outcome==='not-started'
interface DispatchAttempt {live:boolean;performed:boolean;invalid:boolean;minted?:ReauthorizationRequired;refusal?:CredentialRefusal}
/** The admissions the package has in flight: each object fencedEffect handed an effect host, while its dispatch runs. */
const inflight=new WeakMap<object,DispatchAttempt>()
/** The refusals the package trusts, with their disposition and whether a park used them. */
const trusted=new WeakMap<ReauthorizationRequired,{disposition:ReauthDisposition;used:boolean}>()
/** The refusals just trusted, each until the writer of the dispatch that minted it takes it: a proof kept and thrown again
 * by a later dispatch, or passed on by a nested one, is not that dispatch's. */
const fresh=new WeakSet<ReauthorizationRequired>()
/** #226: an effect host refuses the exact admission it was handed, at its admission boundary and before it admits the
 * operation, because the connection's credentials expired or were revoked; it then throws what this returns and never
 * performs. The refusal is trusted only when it is minted once, for an admission the package has in flight, before
 * anything is performed and with a well-formed refusal, and the dispatch then rejects with it, from an effect host proven
 * for the run's store (admitUnattended). A copy of an admission, a second mint, a mint after the dispatch ended or after
 * the operation was performed, and a malformed refusal (one with a token or an error in it) give an untrusted refusal: an
 * ordinary failure whose operation keeps its unknown outcome. */
export function reauthorizationRequired(admission:EffectAdmission,refusal:CredentialRefusal):ReauthorizationRequired{
 const error=new ReauthorizationRequired(),attempt=inflight.get(admission),held=readCredentialRefusal(refusal)
 if(!attempt)return error
 if(!attempt.live||attempt.performed||attempt.minted!==undefined||!held){attempt.invalid=true;return error}
 attempt.minted=error;attempt.refusal=held
 return error
}
/** @internal Whether a refusal is one the package trusts, used or not. */
export const trustedReauthorization=(error:unknown):boolean=>error instanceof ReauthorizationRequired&&trusted.has(error)
/** @internal Whether the writer's dispatch ended in a refusal trusted for that very dispatch: the row then records the
 * typed failure and the run stops there. It answers once. */
export const takeFreshReauthorization=(error:unknown):boolean=>error instanceof ReauthorizationRequired&&fresh.delete(error)
/** @internal The trusted disposition of a refusal not yet used by a park, as a copy. */
export function reauthDispositionOf(error:unknown):ReauthDisposition|undefined{
 const held=error instanceof ReauthorizationRequired?trusted.get(error):undefined
 return held&&!held.used?structuredClone(held.disposition):undefined
}
/** @internal A park committed with this refusal: it never parks another. */
export function useReauthorization(error:ReauthorizationRequired):void{const held=trusted.get(error);if(held)held.used=true}
/** #226: the trusted re-authorization refusal the run's last non-read dispatch through this fenced view ended in, not yet
 * used by a park, or undefined. The host hands it to parkRun; it parks one wait. */
export function reauthorizationOf(store:RunStore):ReauthorizationRequired|undefined{
 const held=leased.get(store)?.reauthorization
 return held!==undefined&&trusted.has(held)&&!trusted.get(held)!.used?held:undefined
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
 * whatever the epoch: its outcome is unknown until its own host reconciles it, and a takeover never replays it.
 * #258: it is final. Constructing a subclass refuses effect-host-unfenced: its dispatch and declaration are sealed on the
 * instance, so an override would never run. A host with its own behavior delegates to one, and is certified. */
export class ReferenceEffectHost implements EffectHost {
 readonly effectFencing='enforced' as const
 // #253: true private fields; replacing a same-named property after admission changes nothing the host enforces.
 readonly #authority:LeaseAuthority
 readonly #admitted=new Set<string>()
 readonly #credentials?:CredentialCheck
 #tail:Promise<unknown>=Promise.resolve()
 /** Every refusal this host gave, as its versioned record, in order. */
 readonly refusals:{version:1;reason:LeaseRefusalReason}[]=[]
 /** #226: with `credentials`, the host asks the check at its admission boundary, after the fence and the idempotency
  * identity and before it admits the operation; a refusal is thrown as reauthorizationRequired gives it, nothing
  * admitted and nothing performed. */
 constructor(authority:LeaseAuthority,options?:{credentials?:CredentialCheck}){
  if(new.target!==ReferenceEffectHost)throw new LeaseCapabilityRefused('effect-host-unfenced','ReferenceEffectHost cannot be subclassed: delegate to one from a host of your own, and certify that host.')
  this.#authority=authority
  if(options?.credentials!==undefined)this.#credentials=options.credentials
  // #253: what admission relies on is sealed on the instance: its dispatch and declaration cannot be swapped afterwards.
  Object.defineProperty(this,'dispatch',{value:ReferenceEffectHost.prototype.dispatch,writable:false,configurable:false,enumerable:false})
  Object.defineProperty(this,'effectFencing',{value:'enforced',writable:false,configurable:false,enumerable:true})
  references.set(this,authority)
 }
 dispatch<T>(admission:EffectAdmission,perform:(signal?:AbortSignal)=>Promise<T>,signal?:AbortSignal):Promise<T>{
  const admitted=this.#tail.then(async()=>{
   try{
    signal?.throwIfAborted()
    const held=readEffectAdmission(admission)
    if(held.fence.resource!==held.operation.run)throw new LeaseRefused('resource-mismatch')
    await this.#authority.checkFence(held.fence,signal)
    if(this.#admitted.has(held.idempotencyKey))throw new LeaseRefused('already-admitted')
    // #226: the credential check is at the admission boundary: a refusal admits nothing and performs nothing. It can take
    // longer than the lease has left, so the abort and the fence are looked at again before anything is admitted.
    if(this.#credentials){
     let refused:CredentialRefusal|undefined
     try{refused=await this.#credentials(structuredClone(held.operation),signal)}catch{signal?.throwIfAborted();throw new CredentialCheckFailed()}
     signal?.throwIfAborted()
     await this.#authority.checkFence(held.fence,signal)
     if(refused!==undefined)throw reauthorizationRequired(admission,refused)
    }
    this.#admitted.add(held.idempotencyKey)
   }catch(error){if(error instanceof LeaseRefused)this.refusals.push(error.record());throw error}
  })
  this.#tail=admitted.catch(()=>undefined)
  return admitted.then(()=>perform(signal))
 }
}

/** #253: the evidence unattended admission accepts, bound to exact instances. A reference host is admitted with the
 * authority it was built over; a certified host is the package's sealed wrapper of the exact instance the kit exercised,
 * with the dispatch the kit checked and the authority it was certified for. A declaration of effectFencing alone is not
 * evidence, and evidence on one instance is not evidence on another. */
const references=new WeakMap<ReferenceEffectHost,LeaseAuthority>()
const certified=new WeakMap<EffectHost,{host:EffectHost;dispatch:EffectHost['dispatch'];authority:LeaseAuthority}>()
function provenAuthority(host:EffectHost):LeaseAuthority|undefined{
 if(host instanceof ReferenceEffectHost){const authority=references.get(host);return authority&&host.dispatch===ReferenceEffectHost.prototype.dispatch&&host.effectFencing==='enforced'?authority:undefined}
 const record=certified.get(host)
 return record&&record.host.dispatch===record.dispatch&&record.host.effectFencing==='enforced'?record.authority:undefined
}
interface KitAuthority extends LeaseAuthority {table:LeaseTable;failing:boolean;at:number}
/** #258: how many times in a row the kit fails the authority for one operation retried, and then for as many new ones,
 * at each point it fails it. No finite count proves a host never degrades; this one fails a breaker of the usual sizes. */
const AUTHORITY_FAILURES=5
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
  const admits=async(property:string,admission:EffectAdmission,count:number)=>{
   let answer:unknown
   try{answer=await host.dispatch(admission,perform)}catch{fail(property)}
   if(answer!=='performed'||performed!==count)fail(property)
  }
  const a=fenceOf(kit.table.acquire({resource:'host-run',owner:'worker-a',expectedRevision:null,durationMs:1000},null))
  // #258: unavailable ownership evidence closes dispatch every time, however often and whenever the authority errors.
  const unavailable=async(property:string,point:string)=>{
   kit.failing=true
   for(let n=0;n<AUTHORITY_FAILURES;n++)await refuses(property,effectAdmission(a,op(`${point}-retried`)),'any')
   for(let n=0;n<AUTHORITY_FAILURES;n++)await refuses(property,effectAdmission(a,op(`${point}-${n}`)),'any')
   kit.failing=false
  }
  await unavailable('host.authority-unavailable','before-any-dispatch')
  await admits('host.admit.current',effectAdmission(a,op('op-1')),1)
  await unavailable('host.authority-unavailable.after-success','after-success')
  await refuses('host.already-admitted',effectAdmission(a,op('op-1')),'already-admitted')
  await refuses('host.other-owner',effectAdmission({...a,owner:'worker-x'},op('op-2')),'other-owner')
  await refuses('host.forged-epoch',effectAdmission({...a,epoch:'2'},op('op-3')),'stale-epoch')
  await refuses('host.resource-mismatch',effectAdmission(a,op('op-4','other-run')),'resource-mismatch')
  await refuses('host.invalid',{...effectAdmission(a,op('op-5')),idempotencyKey:effectAdmission(a,op('op-6')).idempotencyKey},'invalid')
  await unavailable('host.authority-unavailable.between-dispatches','between-dispatches')
  await admits('host.admit.recovered',effectAdmission(a,op('op-10')),2)
  kit.at+=1000
  await refuses('host.expired',effectAdmission(a,op('op-7')),'expired')
  const b=fenceOf(kit.table.acquire({resource:'host-run',owner:'worker-b',expectedRevision:null,durationMs:1000},null))
  await refuses('host.stale-epoch',effectAdmission(a,op('op-8')),'stale-epoch')
  await refuses('host.takeover.already-admitted',effectAdmission(b,op('op-1')),'already-admitted')
  kit.table.release(b)
  await refuses('host.released',effectAdmission(b,op('op-9')),'released')
  if(performed!==2)fail('host.performed-once')
 }catch(error){if(error instanceof StorageConformanceFailure)throw error;throw new StorageConformanceFailure('host.operation')}
}
/** #223 review, #253, #258: the effect-host conformance. The kit owns the lease authority it hands the factory and moves
 * its clock; the host must refuse, without performing, every time the authority is unavailable, which the kit makes it
 * several times in a row before the first dispatch, after a success and between dispatches; admit a current fence once,
 * also once the authority is back; and refuse, without performing, an identity it admitted before (also under a
 * takeover's fence), another owner, a stale or forged epoch, a fence for another run than the operation's, a key that is
 * not its operation's, an expired lease and a released one. */
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
 // #253: the dispatch is read once; the kit runs through exactly that function, and only that function is admitted.
 const dispatch=host.dispatch
 if(typeof dispatch!=='function')throw new LeaseCapabilityRefused('effect-host-unfenced')
 const captured:EffectHost={effectFencing:host.effectFencing,dispatch:<T,>(admission:EffectAdmission,perform:(signal?:AbortSignal)=>Promise<T>,signal?:AbortSignal)=>dispatch.call(host,admission,perform,signal) as Promise<T>}
 await exerciseEffectHost(captured,kit)
 if(host.dispatch!==dispatch||host.effectFencing!=='enforced')throw new LeaseCapabilityRefused('effect-host-unfenced')
 live=true
 const wrapper:EffectHost=Object.freeze({effectFencing:'enforced' as const,dispatch<T>(admission:EffectAdmission,perform:(signal?:AbortSignal)=>Promise<T>,signal?:AbortSignal):Promise<T>{
  if(host.dispatch!==dispatch)return Promise.reject(new LeaseCapabilityRefused('effect-host-unfenced'))
  return dispatch.call(host,admission,perform,signal) as Promise<T>
 }})
 certified.set(wrapper,{host,dispatch,authority})
 return wrapper
}
/** #226: a resumed re-authorization wait's grant: the one never-admitted dispatch its continuation may send again. */
interface ReauthorizedGrant {operation:EffectOperation;used:boolean}
const leased=new WeakMap<object,{fence:LeaseFence;store:RunStore;effectHost?:EffectHost;reauthorization?:ReauthorizationRequired;grant?:ReauthorizedGrant}>()
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
 Object.freeze(view);leased.set(view,{fence:held,store,...(effectHost?{effectHost}:{})})
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
/** @internal The writer's admission of one non-read effect under the fence of the store it writes through. #226: the
 * admission handed to the effect host is in flight while its dispatch runs; a re-authorization refusal minted for it, once,
 * before anything was performed, that the dispatch rejects with, from an effect host proven for the run's store, is
 * trusted and kept on the view (reauthorizationOf). */
export function fencedEffect<T>(store:RunStore,operation:EffectOperation,perform:()=>Promise<T>,signal?:AbortSignal):Promise<T>{
 const lease=leased.get(store)
 if(!lease)return perform()
 if(!lease.effectHost)return Promise.reject(new LeaseCapabilityRefused('effect-host-unfenced'))
 let admission:EffectAdmission
 try{admission=effectAdmission(lease.fence,operation)}catch(error){return Promise.reject(error)}
 const attempt:DispatchAttempt={live:true,performed:false,invalid:false},host=lease.effectHost
 inflight.set(admission,attempt)
 return host.dispatch(admission,()=>{
  // #226: a perform after the dispatch ended never runs, and a refusal followed by one is no proof: the host that minted it
  // did not stand by it.
  if(!attempt.live){attempt.invalid=true;if(attempt.minted){trusted.delete(attempt.minted);fresh.delete(attempt.minted)}return Promise.reject(new LeaseRefused('invalid'))}
  attempt.performed=true;return perform()
 },signal).then(value=>{attempt.live=false;return value},error=>{
  attempt.live=false
  if(attempt.minted!==undefined&&error===attempt.minted&&!attempt.performed&&!attempt.invalid&&provenAuthority(host)===(lease.store as object)){
   const refusal=attempt.refusal!
   fresh.add(error)
   trusted.set(error,{disposition:{version:1,resource:admission.fence.resource,operation:structuredClone(admission.operation),idempotencyKey:admission.idempotencyKey,fence:structuredClone(admission.fence),cause:refusal.cause,connection:refusal.connection,principal:refusal.principal,permissions:[...refusal.permissions],requires:[...refusal.requires]},used:false})
   lease.reauthorization=error
  }
  throw error
 })
}
/** @internal #226: attach a resumed re-authorization wait's grant to the fenced view its continuation runs on: the view's
 * fence must be the claim's, and the operation this run's. */
export function attachReauthorizedDispatch(store:RunStore,operation:EffectOperation,fence:LeaseFence):void{
 const lease=leased.get(store),held=readOperation(operation),bound=readLeaseFence(fence)
 if(!lease||lease.grant||canonicalJSON(lease.fence)!==canonicalJSON(bound)||held.run!==bound.resource)return invalid()
 lease.grant={operation:held,used:false}
}
/** @internal #226: the writer asks before it sends a dispatch again that its run holds as unknown. Only the one operation
 * a resumed re-authorization wait granted on this view, whose recorded failure is the typed never-admitted refusal, which
 * only a trusted refusal records, and only once: anything else stays unknown and is never sent again. */
export function takeReauthorizedDispatch(store:RunStore,operation:EffectOperation,recorded:unknown):boolean{
 const grant=leased.get(store)?.grant
 if(!grant||grant.used||!isNeverAdmittedFailure(recorded)||canonicalJSON(grant.operation)!==canonicalJSON(operation))return false
 grant.used=true
 return true
}
