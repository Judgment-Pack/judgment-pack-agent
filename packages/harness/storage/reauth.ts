import {AIFailureError,failure} from '../failures'
import {canonicalJSON} from '../context/chat'
import {sha256} from '../records'
import {storageId} from './common'
import {ResumeRefused} from './resume'
import {effectIdempotencyKey,readLeaseFence,validConnectionReference,validScopeLabels,type ReauthDisposition} from './lease'
/* Re-authorization for unattended runs (ADR-0025 decision 4, #226; contract/schemas/run-reauth.json). The package
 * validates and records: only a trusted, invocation-bound host disposition (the run's proven effect host refused one exact
 * admission at its boundary, never admitting it, because the connection's credentials expired or were revoked) parks a
 * run as needs_reauthorization; an outcome that may have been admitted keeps its unknown outcome and is reconciled.
 * Re-authorization is never retry permission. The host owns credentials, their refresh and the person's action; no
 * credential, token or provider error enters a checkpoint, wait, prompt, report or export. The Go twin is
 * go/context/run_reauth.go. */
export const REAUTH_RECORD_VERSION=1 as const
export type ReauthRefusalReason='invalid'|'untrusted'|'used'|'stale'|'unbound'|'permission-insufficient'
export const REAUTH_REFUSAL_REASONS:readonly ReauthRefusalReason[]=['invalid','untrusted','used','stale','unbound','permission-insufficient']
export type {ReauthDisposition}
/** The explicit host or person action a re-authorization resume presents (run-reauth.json#/$defs/action). */
export interface ReauthAction {version:1;resource:string;wait:string;connection:string;action:'refresh'|'replacement';principal:string;permissions:string[];actor:string;at:string}
/** What a re-authorization wait's claim records (run-reauth.json#/$defs/reauthorized). */
export interface Reauthorized {actor:string;at:string;principal:string;permissions:string[];digest:string}

/** A named re-authorization refusal. Nothing was parked, claimed or dispatched. */
export class ReauthorizationRefused extends AIFailureError {
 constructor(readonly reason:ReauthRefusalReason){super({...failure('stale-state','storage','not-started'),detail:`ReauthorizationRefused: ${reason}.`});this.name='ReauthorizationRefused'}
 record():{version:1;reason:ReauthRefusalReason}{return {version:1,reason:this.reason}}
}

const TIME=/^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{3}Z$/,DIGEST=/^sha256:[a-f0-9]{64}$/
const object=(v:unknown):v is Record<string,unknown>=>v!==null&&typeof v==='object'&&!Array.isArray(v)&&(Object.getPrototypeOf(v)===Object.prototype||Object.getPrototypeOf(v)===null)
const invalid=():never=>{throw new ReauthorizationRefused('invalid')}
function shape(v:unknown,required:readonly string[]):Record<string,unknown>{
 if(!object(v)||Object.keys(v).length!==required.length||required.some(k=>!Object.hasOwn(v,k)))return invalid()
 return v
}
const id=(v:unknown):string=>storageId(v)?v:invalid()
const digest=(v:unknown):string=>typeof v==='string'&&DIGEST.test(v)?v:invalid()
const connection=(v:unknown):string=>validConnectionReference(v)?v:invalid()
/** The host-attested actor of an action: a bounded reference (the connection's pattern), never free text: no space and no
 * address, so a raw identity or a bearer string is refused. */
export const validActorReference=(v:unknown):v is string=>validConnectionReference(v)
const actor=(v:unknown):string=>validActorReference(v)?v:invalid()
const labels=(v:unknown):string[]=>validScopeLabels(v)?[...v]:invalid()
const validTime=(v:unknown):v is string=>{if(typeof v!=='string'||!TIME.test(v))return false;const ms=Date.parse(v);return Number.isSafeInteger(ms)&&new Date(ms).toISOString()===v}
const time=(v:unknown):string=>validTime(v)?v:invalid()
/** A lease reader's refusal inside a re-authorization record is the record's invalid. */
function within<T>(read:()=>T):T{try{return read()}catch(error){if(error instanceof ReauthorizationRefused)throw error;return invalid()}}

/** Reads a version 1 disposition and holds every rule the schema cannot state: one run, the operation's own key. */
export function readReauthDisposition(v:unknown):ReauthDisposition{
 const held=shape(v,['version','resource','operation','idempotencyKey','fence','cause','connection','principal','permissions','requires'])
 if(held.version!==1)return invalid()
 const operation=shape(held.operation,['run','operation','source','tool','requestDigest'])
 const disposition:ReauthDisposition={version:1,resource:id(held.resource),operation:{run:id(operation.run),operation:id(operation.operation),source:id(operation.source),tool:id(operation.tool),requestDigest:digest(operation.requestDigest)},
  idempotencyKey:digest(held.idempotencyKey),fence:within(()=>readLeaseFence(held.fence)),cause:held.cause==='expired'||held.cause==='revoked'?held.cause:invalid(),connection:connection(held.connection),principal:digest(held.principal),permissions:labels(held.permissions),requires:labels(held.requires)}
 if(disposition.operation.run!==disposition.resource||disposition.fence.resource!==disposition.resource||within(()=>effectIdempotencyKey(disposition.operation))!==disposition.idempotencyKey)return invalid()
 return disposition
}
export function readReauthAction(v:unknown):ReauthAction{
 const held=shape(v,['version','resource','wait','connection','action','principal','permissions','actor','at'])
 if(held.version!==1)return invalid()
 return {version:1,resource:id(held.resource),wait:id(held.wait),connection:connection(held.connection),action:held.action==='refresh'||held.action==='replacement'?held.action:invalid(),principal:digest(held.principal),permissions:labels(held.permissions),actor:actor(held.actor),at:time(held.at)}
}
export function readReauthorized(v:unknown):Reauthorized{
 const held=shape(v,['actor','at','principal','permissions','digest'])
 return {actor:actor(held.actor),at:time(held.at),principal:digest(held.principal),permissions:labels(held.permissions),digest:digest(held.digest)}
}
export function readReauthRefusal(v:unknown):{version:1;reason:ReauthRefusalReason}{
 const held=shape(v,['version','reason'])
 if(held.version!==1||!REAUTH_REFUSAL_REASONS.includes(held.reason as ReauthRefusalReason))return invalid()
 return {version:1,reason:held.reason as ReauthRefusalReason}
}
const digestOf=(value:unknown)=>`sha256:${sha256(canonicalJSON(value))}`
export const reauthDispositionDigest=(disposition:ReauthDisposition)=>digestOf(readReauthDisposition(disposition))
export const reauthActionDigest=(action:ReauthAction)=>digestOf(readReauthAction(action))
/** Every label `requires` names is one `permissions` holds. */
export const permitted=(requires:readonly string[],permissions:readonly string[])=>requires.every(label=>permissions.includes(label))

/** What re-authorization reads of a wait: its run, identity, park time and trusted disposition. */
export interface ReauthWait {resource:string;wait:string;parked:{at:string};pending:{kind:string;connection?:string};reauthorization?:ReauthDisposition}
/** #226: check the host's or the person's explicit action against a woken re-authorization wait, before anything of its
 * continuation is claimed. The action is this wait's and this connection's; a replacement, or a refresh whose principal
 * is not the one the credentials acted as, is a changed identity (ResumeRefused identity-changed: a new admitted run is
 * needed, ADR-0019), never relabelled as a refresh; an action from before the park is stale; current permissions that do
 * not cover what the operation requires make it inadmissible. Returns what the claim records. The store checks the
 * principal, the permissions and the action's time against its own clock again in the claim's transaction. */
export function reauthorize(wait:ReauthWait,input:unknown):Reauthorized{
 const action=readReauthAction(input),disposition=wait.reauthorization
 if(!disposition||wait.pending.kind!=='reauthorization')return invalid()
 if(action.resource!==wait.resource||action.wait!==wait.wait||action.connection!==disposition.connection||action.connection!==wait.pending.connection)return invalid()
 if(action.action!=='refresh'||action.principal!==disposition.principal)throw new ResumeRefused('identity-changed')
 if(Date.parse(action.at)<Date.parse(wait.parked.at))throw new ReauthorizationRefused('stale')
 if(!permitted(disposition.requires,action.permissions))throw new ReauthorizationRefused('permission-insufficient')
 return {actor:action.actor,at:action.at,principal:action.principal,permissions:[...action.permissions],digest:reauthActionDigest(action)}
}
