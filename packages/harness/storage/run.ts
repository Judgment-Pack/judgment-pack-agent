import {MemoryHistoryJournal,freezeHistoryInput,type RunHistoryCommit,type RunHistoryReceipt,type HistoryResource,type ContextSnapshotUpdate,type HistoryObjectPage} from './history'
import type {HistoryObject} from '../history/codec'
import {canonicalJSON} from '../context/chat'
import {bytesCopy,requireId,requireLimit,sameBytes,storageId,StorageCommitMismatch,StorageConflict,StorageCursorInvalid,StorageInputError,STORAGE_EVENT_LIMIT,STORAGE_TRANSACTION_BYTES} from './common'
import type {StorageRevision,StorageCursor} from './common'
import {LeaseRefused,LeaseTable,readLeaseFence,type LeaseAcquire,type LeaseFence,type LeaseLifetime,type LeaseRunStore,type RunLease} from './lease'
import {WaitTable,type AttentionDelivery,type WaitClaim,type WaitLifetime,type WaitPark,type WaitRunStore,type WakeEvidence} from './wait'
import type {RunLimit} from './resume'
export type RunStoreDurability = 'host'|'memory'|'single-writer-callback'
export interface RunCommit {
 runId:string;expectedRevision:StorageRevision|null;commitId:string
 /** Exact canonical record bytes, already validated/guarded by the Agent writer. null leaves it unchanged. */
 checkpoint:Uint8Array|null;events:Uint8Array[]
}
export interface RunReceipt {revision:StorageRevision;cursor:StorageCursor|null;commitId:string}
export interface RunHead extends RunReceipt {runId:string;checkpoint:Uint8Array|null}
export interface StoredRunEvent {cursor:StorageCursor;bytes:Uint8Array}
export interface RunEventPage {items:StoredRunEvent[];nextCursor:StorageCursor|null;hasMore:boolean}
/** One injected store is already bound to an authorized host resource scope. load/read never resume.
 * A commit atomically publishes checkpoint + ordered events + idempotent receipt. A receipt proves
 * a past storage transaction, not current authority or external success. An uncertain acknowledgement
 * grants no dispatch. Repeat commit ids require identical bytes AND the original expected revision.
 */
export interface RunStore {
 readonly durability:RunStoreDurability
 load(runId:string,signal?:AbortSignal):Promise<RunHead|undefined>
 commit(input:RunCommit,signal?:AbortSignal):Promise<RunReceipt>
 readEvents(runId:string,after:StorageCursor|null,limit:number,signal?:AbortSignal):Promise<RunEventPage>
}
interface HeldRun {head:RunHead;events:StoredRunEvent[];commits:Map<string,{input:RunCommit;receipt:RunReceipt;historyInput?:string;historyReceipt?:RunHistoryReceipt}>}
export interface MemoryRunStoreOptions {
 /** Fault injection only. Hooks must not call this store recursively. */
 beforeCommit?:()=>void|Promise<void>;afterCommit?:()=>void|Promise<void>
 /** Storage observation clock; idempotent commits retain their first value. */
 now?:()=>Date
}
function copyCommit(input:RunCommit):RunCommit {
 if(!input||typeof input!=='object'||Array.isArray(input)||Object.keys(input).some(k=>!['runId','expectedRevision','commitId','checkpoint','events'].includes(k)))throw new StorageInputError()
 requireId(input.runId);requireId(input.commitId)
 if(input.expectedRevision!==null)requireId(input.expectedRevision)
 if(!Array.isArray(input.events)||input.events.length>STORAGE_EVENT_LIMIT||input.checkpoint===null&&input.events.length===0)throw new StorageInputError()
 const checkpoint=input.checkpoint===null?null:bytesCopy(input.checkpoint),events:Uint8Array[]=[]
 let total=checkpoint?.length??0
 for(const raw of input.events){const event=bytesCopy(raw);total+=event.length;if(total>STORAGE_TRANSACTION_BYTES)throw new StorageInputError();events.push(event)}
 return {...input,checkpoint,events}
}
const identical=(a:RunCommit,b:RunCommit)=>a.expectedRevision===b.expectedRevision&&sameBytes(a.checkpoint,b.checkpoint)&&a.events.length===b.events.length&&a.events.every((e,i)=>sameBytes(e,b.events[i]!))
const copyHead=(head:RunHead):RunHead=>({...head,checkpoint:head.checkpoint===null?null:new Uint8Array(head.checkpoint)})
/** Conforms to the atomic port in one process; all data is ephemeral. Tokens are unique per instance.
 * Its leases follow the lease transitions within the same process only (leaseLifetime 'process'): it never qualifies for
 * unattended admission, and a new instance starts with no leases. Its waits (#224) likewise end with its process
 * (waitLifetime 'process'), so a park on it refuses store-lifetime. */
export class MemoryRunStore implements RunStore,LeaseRunStore,WaitRunStore {
 readonly durability:RunStoreDurability='memory'
 readonly historyVersion=1 as const
 readonly leaseVersion=1 as const
 readonly leaseLifetime:LeaseLifetime='process'
 readonly waitVersion=1 as const
 readonly waitLifetime:WaitLifetime='process'
 private readonly leases:LeaseTable
 private readonly waits:WaitTable
 private readonly journal:MemoryHistoryJournal
 private runs=new Map<string,HeldRun>()
 private tail:Promise<unknown>=Promise.resolve()
 constructor(private options:MemoryRunStoreOptions={},private acknowledge?:(checkpoint:Uint8Array,signal?:AbortSignal)=>Promise<void>){this.journal=new MemoryHistoryJournal(options.now);this.leases=new LeaseTable(()=>this.options.now?.()??new Date());this.waits=new WaitTable(()=>this.options.now?.()??new Date())}
 private serial<T>(signal:AbortSignal|undefined,run:()=>T):Promise<T>{
  try{signal?.throwIfAborted()}catch(error){return Promise.reject(error)}
  const result=this.tail.then(()=>{signal?.throwIfAborted();return run()})
  this.tail=result.catch(()=>undefined);return result
 }
 // #265 M2 and #225: a run a wait holds is leased only by its resume's claim, and no lease outlives the run's deadline.
 acquireLease(request:LeaseAcquire,signal?:AbortSignal):Promise<RunLease>{return this.serial(signal,()=>{
  const resource=request&&typeof request==='object'&&storageId(request.resource)?request.resource:undefined
  if(resource!==undefined)this.waits.admitAcquire(resource)
  return this.leases.acquire(request,resource===undefined?null:this.runs.get(resource)?.head.revision??null,resource===undefined?undefined:this.waits.ceiling(resource))
 })}
 renewLease(fence:LeaseFence,durationMs:number,signal?:AbortSignal):Promise<RunLease>{return this.serial(signal,()=>this.leases.renew(fence,durationMs,fence&&typeof fence==='object'?this.waits.ceiling(fence.resource,fence.epoch):undefined))}
 releaseLease(fence:LeaseFence,signal?:AbortSignal):Promise<RunLease>{return this.serial(signal,()=>this.leases.release(fence))}
 readLease(resource:string,signal?:AbortSignal):Promise<RunLease|undefined>{return this.serial(signal,()=>this.leases.read(resource))}
 checkFence(fence:LeaseFence,signal?:AbortSignal):Promise<void>{return this.serial(signal,()=>{this.leases.check(fence)})}
 // #224: every wait transition runs in the same serialized transaction order as the commits, against the same head.
 private head(resource:string){return this.runs.get(resource)?.head}
 parkWait(park:WaitPark,fence:LeaseFence,signal?:AbortSignal){return this.serial(signal,()=>this.waits.park(park,fence,park&&typeof park==='object'?this.head(park.resource):undefined,this.leases))}
 wakeWait(evidence:WakeEvidence,signal?:AbortSignal){return this.serial(signal,()=>this.waits.wake(evidence))}
 resumeWait(claim:WaitClaim,signal?:AbortSignal){return this.serial(signal,()=>this.waits.resume(claim,claim&&typeof claim==='object'&&storageId(claim.resource)?this.head(claim.resource):undefined,this.leases))}
 limitRun(limit:RunLimit,signal?:AbortSignal){return this.serial(signal,()=>{const held=this.waits.limit(limit);this.leases.lower(held.resource,Date.parse(held.run));return held})}
 readWait(resource:string,wait:string,signal?:AbortSignal){return this.serial(signal,()=>this.waits.read(resource,wait))}
 exportWaits(resource:string,signal?:AbortSignal){return this.serial(signal,()=>this.waits.export(resource))}
 recordDelivery(delivery:Omit<AttentionDelivery,'at'|'version'>,signal?:AbortSignal){return this.serial(signal,()=>this.waits.deliver(delivery))}
 redactWait(resource:string,wait:string,signal?:AbortSignal){return this.serial(signal,()=>this.waits.redact(resource,wait))}
 commitFenced(input:RunCommit,fence:LeaseFence,signal?:AbortSignal):Promise<RunReceipt>{return this.commitTransaction(input,signal,undefined,fence).then(value=>value.receipt)}
 commitHistoryFenced(input:RunHistoryCommit,fence:LeaseFence,signal?:AbortSignal):Promise<RunHistoryReceipt>{
  if(!input||typeof input!=='object')return Promise.reject(new StorageInputError())
  const {run,...extra}=input
  let frozen:ReturnType<typeof freezeHistoryInput>;try{frozen=freezeHistoryInput(extra)}catch(error){return Promise.reject(error)}
  return this.commitTransaction(run,signal,frozen,fence).then(value=>value.historyReceipt!)
 }
 async load(runId:string,signal?:AbortSignal){signal?.throwIfAborted();requireId(runId);const held=this.runs.get(runId);return held?copyHead(held.head):undefined}
 commit(input:RunCommit,signal?:AbortSignal):Promise<RunReceipt> {return this.commitTransaction(input,signal).then(value=>value.receipt)}
 commitHistory(input:RunHistoryCommit,signal?:AbortSignal):Promise<RunHistoryReceipt>{
  if(!input||typeof input!=='object')return Promise.reject(new StorageInputError())
  const {run,...extra}=input
  let frozen:ReturnType<typeof freezeHistoryInput>;try{frozen=freezeHistoryInput(extra)}catch(error){return Promise.reject(error)}
  return this.commitTransaction(run,signal,frozen).then(value=>value.historyReceipt!)
 }
 private commitTransaction(input:RunCommit,signal?:AbortSignal,extra?:Omit<RunHistoryCommit,'run'>,fence?:LeaseFence):Promise<{receipt:RunReceipt;historyReceipt:RunHistoryReceipt}> {
  // Detach before the first asynchronous boundary, including while another commit owns the queue.
  let frozen:RunCommit
  try{signal?.throwIfAborted();frozen=copyCommit(input)}catch(error){return Promise.reject(error)}
  const result=this.tail.then(async()=>{
   signal?.throwIfAborted()
   // #223: the fence is checked in the same serialized transaction as the revision, and again at publication.
   const fenced=()=>{if(fence){const held=readLeaseFence(fence);if(held.resource!==frozen.runId)throw new LeaseRefused('resource-mismatch');this.leases.check(held)}}
   fenced()
   const held=this.runs.get(frozen.runId),prior=held?.commits.get(frozen.commitId)
   if(prior){if(!identical(prior.input,frozen)||prior.historyInput!==canonicalJSON(extra))throw new StorageCommitMismatch();return {receipt:{...prior.receipt},historyReceipt:structuredClone(prior.historyReceipt!)}}
   if((held?.head.revision??null)!==frozen.expectedRevision)throw new StorageConflict(frozen.expectedRevision,held?.head.revision??null)
   const preliminary={revision:crypto.randomUUID(),cursor:null,commitId:frozen.commitId}
   const prepared=this.journal.prepare(frozen,preliminary,frozen.checkpoint??held?.head.checkpoint??null,extra)
   await this.options.beforeCommit?.()
   signal?.throwIfAborted()
   if(frozen.checkpoint!==null)await this.acknowledge?.(new Uint8Array(frozen.checkpoint),signal)
   signal?.throwIfAborted()
   const events=frozen.events.map(bytes=>({cursor:crypto.randomUUID(),bytes})),cursor=events.at(-1)?.cursor??held?.head.cursor??null
   const receipt:RunReceipt={...preliminary,cursor}
   const historyReceipt:RunHistoryReceipt={...receipt,history:prepared.view.head}
   const head:RunHead={...receipt,runId:frozen.runId,checkpoint:frozen.checkpoint??held?.head.checkpoint??null}
   const commits=new Map(held?.commits);commits.set(frozen.commitId,{input:frozen,receipt,historyInput:canonicalJSON(extra),historyReceipt})
   fenced()
   // Publish only after all validation/acknowledgement. No externally visible partial state.
   this.runs.set(frozen.runId,{head,events:[...(held?.events??[]),...events],commits})
   this.journal.publish(prepared)
   await this.options.afterCommit?.()
   return {receipt:{...receipt},historyReceipt:structuredClone(historyReceipt)}
  })
  this.tail=result.catch(()=>undefined)
  return result
 }
 async stageHistoryObjects(resource:HistoryResource,objects:readonly HistoryObject[],signal?:AbortSignal){signal?.throwIfAborted();await this.journal.stage(resource,objects);signal?.throwIfAborted()}
 async readHistoryHead(runId:string,signal?:AbortSignal){signal?.throwIfAborted();return this.journal.head(runId)}
 async readHistoryPage(runId:string,revision:string,after:number,limit:number,signal?:AbortSignal){signal?.throwIfAborted();return this.journal.page(runId,revision,after,limit)}
 async readHistoryObject(resource:HistoryResource,revision:string,digest:string,offset:number,limit:number,signal?:AbortSignal):Promise<HistoryObjectPage>{signal?.throwIfAborted();return this.journal.object(resource,revision,digest,offset,limit)}
 async loadContextSnapshot(chatId:string,signal?:AbortSignal){signal?.throwIfAborted();return this.journal.contextHead(chatId)}
 async commitContextSnapshot(input:ContextSnapshotUpdate,commitId:string,signal?:AbortSignal,fence?:LeaseFence){
  if(!input)throw new StorageInputError()
  const frozen=freezeHistoryInput({observations:[],context:input}).context!
  // #223: a leased run's chat snapshot commits only under its current fence, checked in the same transaction.
  const result=this.tail.then(()=>{signal?.throwIfAborted();if(fence)this.leases.check(readLeaseFence(fence));return this.journal.commitContext(frozen,commitId)})
  this.tail=result.catch(()=>undefined);return result
 }
 commitContextSnapshotFenced(input:ContextSnapshotUpdate,commitId:string,fence:LeaseFence,signal?:AbortSignal){return this.commitContextSnapshot(input,commitId,signal,fence)}
 async commitHistoryMetadata(input:import('./history').HistoryMetadataCommit,signal?:AbortSignal){
  const frozen=structuredClone(input),result=this.tail.then(async()=>{signal?.throwIfAborted();const prepared=this.journal.prepareMetadata(frozen);if(prepared.replay)return prepared.publish();await this.options.beforeCommit?.();signal?.throwIfAborted();const result=prepared.publish();await this.options.afterCommit?.();return result});this.tail=result.catch(()=>undefined);return result
 }
 async readHistoryMetadataPage(runId:string,revision:string,after:number,limit:number,signal?:AbortSignal){signal?.throwIfAborted();return this.journal.metadataPage(runId,revision,after,limit)}
 async readEvents(runId:string,after:StorageCursor|null,limit:number,signal?:AbortSignal):Promise<RunEventPage>{
  signal?.throwIfAborted();requireId(runId);requireLimit(limit)
  if(after!==null){try{requireId(after)}catch{throw new StorageCursorInvalid()}}
  const events=this.runs.get(runId)?.events??[]
  const at=after===null?-1:events.findIndex(e=>e.cursor===after)
  if(after!==null&&at<0)throw new StorageCursorInvalid()
  const items:StoredRunEvent[]=[];let bytes=0,index=at+1
  for(;index<events.length&&items.length<limit;index++){
   const item=events[index]!
   if(bytes+item.bytes.length>STORAGE_TRANSACTION_BYTES)break
   items.push({cursor:item.cursor,bytes:new Uint8Array(item.bytes)});bytes+=item.bytes.length
  }
  return {items,nextCursor:items.at(-1)?.cursor??after,hasMore:index<events.length}
 }
}
/** Compatibility only: callback persistence has NO cross-process CAS or durable event log.
 * The decoder is explicit so a legacy codec, version or object projection is never guessed.
 * Restart/import is an explicit null-revision commit into a fresh adapter, not recovered log state.
 */
export function checkpointCallbackRunStore<T>(callback:(state:T)=>Promise<void>,decode:(bytes:Uint8Array)=>T):RunStore {
 const store=new MemoryRunStore({},async bytes=>callback(decode(bytes)))
 return {durability:'single-writer-callback',load:store.load.bind(store),commit:store.commit.bind(store),readEvents:store.readEvents.bind(store)}
}
