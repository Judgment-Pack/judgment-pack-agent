import {storageId} from '../storage/common'
import type {DurableScope} from '../storage/writer'
import type {HistoryCaptureIdentity} from './types'
import {historyJSONBytes,historyBytesDigest,HISTORY_TOTAL_BYTES,HISTORY_OBJECT_BYTES,validHistoryRef,type HistoryObjectRef} from './codec'

export class ReplayUnavailable extends Error {
 constructor(){super('ReplayUnavailable: a complete supported capture could not be verified.');this.name='ReplayUnavailable'}
}
export interface CaptureAdmission {
 identity:Omit<HistoryCaptureIdentity,'captureIndex'>
 sequence:number;ordinal:number;commitId:string;expectation:HistoryObjectRef;request:string
}
export interface CaptureTerminal {runId:string;executionRevision:string;checkpoint:HistoryObjectRef;status:'complete'|'stopped'}
const tokenBrand:unique symbol=Symbol('private capture admission'),rootBrand:unique symbol=Symbol('private capture root'),sealBrand:unique symbol=Symbol('private capture seal')
export type CaptureToken={readonly [tokenBrand]:true}
export type CaptureRoot={readonly [rootBrand]:true}
export type CaptureSeal={readonly [sealBrand]:true}
interface Held {admission:CaptureAdmission;frame?:unknown;acknowledged:boolean}
interface Sealed {terminal:CaptureTerminal;rows:{admission:CaptureAdmission;frame:unknown}[]}
const seals=new WeakMap<object,{material:Sealed;store?:object;consumed:boolean}>()
const unavailable=():never=>{throw new ReplayUnavailable()}
const copy=<T>(v:T):T=>JSON.parse(new TextDecoder().decode(historyJSONBytes(v))) as T
/** Private issuance capability. Future original-writer hooks retain this instance; callers get no issuer.
 * admit/acknowledge are called only AFTER the writer's existing exact journal ACK checks succeed.
 * This module never turns a caller marker, snapshot or archive into acknowledgement authority. */
export class CaptureCollector {
 private readonly tokens=new WeakMap<object,Held>()
 private readonly root=Object.freeze({}) as CaptureRoot
 private readonly rows:Held[]=[]
 private readonly seen=new Set<string>()
 private failed=false
 private closed=false
 private bytes=0
 private store?:object
 constructor(private readonly owner:object,private readonly runId:string,scope:DurableScope){
  if(!owner||typeof owner!=='object'||!storageId(runId)||scope.execution!==runId||scope.role!=='author'||scope.task!==undefined)unavailable()
 }
 rootToken(owner:object){this.check(owner);return this.root}
 /** Enrollment uses the original store capability; snapshots/structural identifiers cannot bind it. */
 bindPublisher(owner:object,store:object){this.check(owner);if(!store||typeof store!=='object'||this.store||this.rows.length)return this.fail();this.store=store}

 private check(owner:object){if(owner!==this.owner||this.failed||this.closed){this.failed=true;unavailable()}}
 private fail():never{this.failed=true;return unavailable()}
 admit(owner:object,input:CaptureAdmission):CaptureToken {
  this.check(owner)
  const a=copy(input),i=a.identity,prior=this.rows.at(-1)?.admission
  if(!i||!storageId(i.dispatchId)||!storageId(i.source)||!storageId(i.tool)||!storageId(i.scope.execution)||!storageId(i.scope.role)||i.scope.task!==undefined&&!storageId(i.scope.task)||!['model','agent'].includes(i.kind)||i.surface!==(i.kind==='model'?'session-model':'native-aggregate')||!Number.isSafeInteger(a.sequence)||a.sequence<1||!Number.isSafeInteger(a.ordinal)||a.ordinal<0||!storageId(a.commitId)||!validHistoryRef(a.expectation)||typeof a.request!=='string'||new TextEncoder().encode(a.request).length>HISTORY_OBJECT_BYTES||historyBytesDigest(new TextEncoder().encode(a.request))!==i.requestDigest||this.rows.length>=400||prior&&(a.sequence<prior.sequence||a.sequence===prior.sequence&&a.ordinal<=prior.ordinal)||this.seen.has(i.dispatchId))return this.fail()
  if(i.scope.task===undefined?i.scope.execution!==this.runId:i.scope.execution!==i.scope.task)return this.fail()
  const token=Object.freeze({}) as CaptureToken,held:Held={admission:a,acknowledged:false}
  this.seen.add(i.dispatchId);this.rows.push(held);this.tokens.set(token,held);return token
 }
 record(owner:object,token:CaptureToken,frame:unknown){
  this.check(owner);const held=token&&this.tokens.get(token)
  if(!held||held.frame!==undefined)return this.fail()
  let value:unknown;try{value=copy(frame);this.bytes+=historyJSONBytes(value).length}catch{return this.fail()}
  if(this.bytes>HISTORY_TOTAL_BYTES)return this.fail();held.frame=value
 }
 acknowledge(owner:object,token:CaptureToken){this.check(owner);const held=token&&this.tokens.get(token);if(!held||held.acknowledged)return this.fail();held.acknowledged=true}
 seal(owner:object,root:CaptureRoot,terminal:CaptureTerminal):CaptureSeal {
  this.check(owner)
  if(root!==this.root||terminal.runId!==this.runId||!storageId(terminal.executionRevision)||!validHistoryRef(terminal.checkpoint)||!['complete','stopped'].includes(terminal.status)||this.rows.some(row=>!row.acknowledged||row.frame===undefined))return this.fail()
  const value={terminal:copy(terminal),rows:this.rows.map(row=>({admission:copy(row.admission),frame:copy(row.frame)}))},seal=Object.freeze({}) as CaptureSeal
  this.closed=true;seals.set(seal,{material:value,store:this.store,consumed:false});return seal
 }
}
/** Internal material accessor for the later publisher; a structurally identical object is refused. */
export function sealedCapture(seal:CaptureSeal){const value=seal&&seals.get(seal);if(!value)return unavailable();return copy(value.material)}

/** One private publication preparation per seal. A wrong-store attempt consumes no material. */
export function consumeSealedCapture(seal:CaptureSeal,store:object){const value=seal&&seals.get(seal);if(!value||value.consumed||!value.store||value.store!==store)return unavailable();value.consumed=true;return copy(value.material)}
