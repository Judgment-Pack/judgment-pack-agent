import {ChatContext,type ChatScope,type ChatTurn} from './chat'
import {ContextTurnBindingInvalid,readChatContextSnapshot} from './chatSnapshot'
import {chunkHistoryValue,historyBytesDigest,readHistoryChunkManifest,HISTORY_OBJECT_BYTES,HISTORY_TOTAL_BYTES,HistoryInputInvalid,type HistoryObjectRef} from '../history/codec'
import type {HistoryRunStore,HistoryContextRef,ContextSnapshotUpdate,ContextSnapshotHead} from '../storage/history'
import {STORAGE_TRANSACTION_BYTES} from '../storage/common'
const ref=(v:HistoryObjectRef)=>({digest:v.digest,bytes:v.bytes})
/** One exact turn, used for both context.chat and runStore.context. The host supplies its bound
 * store/chat identity. Snapshot reads alone grant no authority: the writer's joint CAS must ack. */
export class StoredChatScope implements ChatScope{
 private turn?:ChatTurn
 private attached=false
 private closed=false
 private privateLedgerScope?:string
 private restoredPrivate=false
 private expectedRevision:string|null
 private bound?:HistoryContextRef
 private resume?:number
 private opening?:()=>ChatTurn
 constructor(readonly store:HistoryRunStore,readonly chatId:string,readonly context:ChatContext,head?:ContextSnapshotHead,resume?:number){
  if(store.historyVersion!==1||head&&head.chatId!==chatId)throw new ContextTurnBindingInvalid()
  this.expectedRevision=head?.revision??null;this.resume=resume
  if(resume!==undefined){const scope=context.resumeTurn(resume);this.opening=()=>scope.openTurn()}
 }
 get ledgerScope(){return this.privateLedgerScope}
 /** @internal Only a validated persisted private binding grants this once-only restore marker. */
 get restoredPrivatePhase(){return this.restoredPrivate}
 /** @internal Private restart attaches an invocation, never a replacement author turn. */
 attachPrivate(){if(!this.privateLedgerScope||!this.closed||this.attached)throw new ContextTurnBindingInvalid();this.attached=true}
 /** @internal Called after the real ledger isolation has released its original author turn. */
 enterPrivate(scope:string,turn?:ChatTurn){const number=this.turn?.number??this.resume;if(number===undefined||this.turn!==turn||this.context.hasActiveTurn(number)||scope===this.context.scope)throw new ContextTurnBindingInvalid();this.closed=true;this.privateLedgerScope=scope}
 openTurn():ChatTurn{if(this.attached||this.closed)throw new ContextTurnBindingInvalid();this.attached=true;return this.turn??=this.opening?this.opening():this.context.openTurn()}
 /** @internal The invocation ended; the paused logical turn remains, terminal turns are already released. */
 suspend(turn?:ChatTurn):boolean{if(this.turn!==turn||turn===undefined&&!this.privateLedgerScope)return false;this.attached=false;return !this.closed}
 /** @internal Validate the exact persisted binding before a resumed writer is exposed. */
 matches(saved:HistoryContextRef):boolean{return !!this.bound&&saved.chatId===this.bound.chatId&&saved.revision===this.bound.revision&&saved.turn===this.bound.turn&&saved.manifest.digest===this.bound.manifest.digest&&saved.manifest.bytes===this.bound.manifest.bytes&&saved.closed===this.bound.closed&&saved.privateScope===this.bound.privateScope}
 /** @internal Original run binding remains separate from the latest chat revision. */
 bindRestored(saved:HistoryContextRef){this.bound=structuredClone(saved)}
 /** @internal Validated decoded private binding only; admission matches it against the run head. */
 bindPrivateRestore(saved:HistoryContextRef){this.resume=saved.turn;this.closed=true;this.privateLedgerScope=saved.privateScope;this.restoredPrivate=true;this.bindRestored(saved)}
 /** @internal Snapshot before staging; CAS will reject concurrent publication. */
 async prepare(signal?:AbortSignal,terminal=false,checkpointScope?:string):Promise<ContextSnapshotUpdate>{
  if(checkpointScope!==undefined&&checkpointScope!==(this.privateLedgerScope??this.context.scope))throw new ContextTurnBindingInvalid()
  const turn=this.turn?.number??this.resume;if(turn===undefined)throw new ContextTurnBindingInvalid()
  if(terminal&&!this.closed){this.turn!.release();this.closed=true}
  const bytes=this.context.snapshot(),parts=await chunkHistoryValue(bytes),objects=[...parts.objects,parts.manifest]
  let batch:typeof objects=[],size=0
  for(const object of objects){if(size+object.bytes>STORAGE_TRANSACTION_BYTES){await this.store.stageHistoryObjects({kind:'chat',id:this.chatId},batch,signal);batch=[];size=0}batch.push(object);size+=object.bytes}
  if(batch.length)await this.store.stageHistoryObjects({kind:'chat',id:this.chatId},batch,signal)
  return {chatId:this.chatId,expectedRevision:this.expectedRevision,manifest:ref(parts.manifest),turn,...(this.closed?{closed:true as const}:{}),...(this.privateLedgerScope?{privateScope:this.privateLedgerScope}:{})}
 }
 /** After a direct host-owned invocation has closed, explicitly release its logical turn and
  * acknowledge the resulting complete chat snapshot. This does not terminalize a run checkpoint. */
 async close(signal?:AbortSignal):Promise<ContextSnapshotHead>{
  if(this.attached||!this.turn&&!this.privateLedgerScope)throw new ContextTurnBindingInvalid()
  const update=await this.prepare(signal,true),head=await this.store.commitContextSnapshot(update,crypto.randomUUID(),signal)
  if(head.chatId!==this.chatId||!head.revision||head.revision===update.expectedRevision||head.manifest.digest!==update.manifest.digest||head.manifest.bytes!==update.manifest.bytes)throw new HistoryInputInvalid()
  this.expectedRevision=head.revision;return head
 }
 /** @internal Called only after the writer validates the joint acknowledgement. */
 acknowledge(saved:HistoryContextRef){this.expectedRevision=saved.revision}
}
export async function readStoredChatSnapshot(store:HistoryRunStore,head:ContextSnapshotHead,signal?:AbortSignal):Promise<Uint8Array>{
 const read=async(ref:HistoryObjectRef)=>{const page=await store.readHistoryObject({kind:'chat',id:head.chatId},head.revision,ref.digest,0,HISTORY_OBJECT_BYTES,signal);if(page.digest!==ref.digest||page.total!==ref.bytes||page.offset!==0||page.nextOffset!==ref.bytes||page.hasMore||page.value.length!==ref.bytes||historyBytesDigest(page.value)!==ref.digest)throw new HistoryInputInvalid();return page.value}
 const manifest=readHistoryChunkManifest(await read(head.manifest));if(manifest.bytes>HISTORY_TOTAL_BYTES)throw new HistoryInputInvalid()
 const bytes=new Uint8Array(manifest.bytes);let offset=0
 for(const part of manifest.parts){bytes.set(await read(part),offset);offset+=part.bytes}
 if(offset!==manifest.bytes||historyBytesDigest(bytes)!==manifest.digest)throw new HistoryInputInvalid()
 readChatContextSnapshot(bytes);return bytes
}
/** Restore the latest complete chat view with the original exact run/turn binding; the writer verifies and claims its run/chat revisions before
 * dispatch. No other open turns are discarded and the restored turn counter never increments. */
export async function restoreStoredChatScope(store:HistoryRunStore,saved:HistoryContextRef,signal?:AbortSignal):Promise<StoredChatScope>{
 if(saved.turn===undefined)throw new ContextTurnBindingInvalid()
 const latest=await store.loadContextSnapshot(saved.chatId,signal);if(!latest||latest.chatId!==saved.chatId)throw new ContextTurnBindingInvalid()
 const context=ChatContext.restore(await readStoredChatSnapshot(store,latest,signal))
 if(saved.privateScope){if(!saved.closed||saved.privateScope===context.scope||context.hasActiveTurn(saved.turn))throw new ContextTurnBindingInvalid();const source=new StoredChatScope(store,saved.chatId,context,latest);source.bindPrivateRestore(saved);return source}
 if(saved.closed)throw new ContextTurnBindingInvalid()
 const source=new StoredChatScope(store,saved.chatId,context,latest,saved.turn);source.bindRestored(saved);return source
}
