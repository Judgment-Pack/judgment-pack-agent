import {ChatNotFound,ChatRevisionConflict,requireId,requireLimit,StorageCursorInvalid,StorageInputError,STORAGE_RECORD_BYTES,STORAGE_TRANSACTION_BYTES} from './common'
import type {StorageRevision,StorageCursor} from './common'
/** Complete JSON chat documents are retained; future plan payload fields are not projected away. */
export interface StoredChat {id:string;interaction?:'plan'|'work';plans?:unknown[];[key:string]:unknown}
export interface ChatEntry {chat:StoredChat;revision:StorageRevision;kind:'chat'|'draft'}
export interface ChatPatch {interaction?:'plan'|'work';plans?:unknown[]}
export interface ChatListRequest {limit:number;cursor?:StorageCursor;kind?:'chat'|'draft'}
export interface ChatPage {items:ChatEntry[];nextCursor?:StorageCursor;hasMore:boolean}
export interface ChatStore {
 readonly consistency?:'atomic'|'single-writer-legacy'
 get(id:string,signal?:AbortSignal):Promise<ChatEntry|undefined>
 list(request:ChatListRequest,signal?:AbortSignal):Promise<ChatPage>
 /** Atomic durable update; no separate checkpoint or rollback. Missing patch members are retained. */
 update(id:string,patch:ChatPatch,expectedRevision:StorageRevision,signal?:AbortSignal):Promise<ChatEntry>
}
function jsonCopy<T>(input:T):T {
 // Bound work/depth before stringification, which could invoke a lossy toJSON or overflow.
 const stack:{value:unknown;depth:number}[]=[{value:input,depth:0}]
 let work=0,minimumBytes=0
 while(stack.length){
  const {value,depth}=stack.pop()!
  if(++work>100_000||depth>128||minimumBytes>STORAGE_RECORD_BYTES)throw new StorageInputError()
  if(value===null||typeof value==='boolean'||typeof value==='number'&&Number.isFinite(value)){minimumBytes++;continue}
  if(typeof value==='string'){minimumBytes+=value.length;if(minimumBytes>STORAGE_RECORD_BYTES)throw new StorageInputError();continue}
  if(Array.isArray(value)){for(const item of value)stack.push({value:item,depth:depth+1});continue}
  if(value&&typeof value==='object'&&(Object.getPrototypeOf(value)===Object.prototype||Object.getPrototypeOf(value)===null)){
   for(const [key,item] of Object.entries(value)){minimumBytes+=key.length;stack.push({value:item,depth:depth+1})}continue
  }
  throw new StorageInputError()
 }
 let text:string
 try{text=JSON.stringify(input)}catch{throw new StorageInputError()}
 if(text===undefined||new TextEncoder().encode(text).length>STORAGE_RECORD_BYTES)throw new StorageInputError()
 return JSON.parse(text) as T
}
const copyEntry=(entry:ChatEntry):ChatEntry=>jsonCopy(entry)
/** Test kind only. Seeding is host setup, never an engine capability. Listings use snapshot cursors. */
export interface MemoryChatStoreOptions {beforeUpdate?:()=>void;afterUpdate?:()=>void}
export class MemoryChatStore implements ChatStore {
 readonly consistency='atomic' as const
 private chats=new Map<string,ChatEntry>()
 private pages=new Map<string,{entries:ChatEntry[];offset:number;kind?:'chat'|'draft'}>()
 constructor(seed:{chat:StoredChat;kind?:'chat'|'draft'}[]=[],private options:MemoryChatStoreOptions={}){
  for(const item of seed){requireId(item.chat.id);if(this.chats.has(item.chat.id)||item.kind!==undefined&&!['chat','draft'].includes(item.kind))throw new StorageInputError();const chat=jsonCopy(item.chat);this.chats.set(chat.id,{chat,kind:item.kind??'chat',revision:crypto.randomUUID()})}
 }
 async get(id:string,signal?:AbortSignal){signal?.throwIfAborted();requireId(id);const entry=this.chats.get(id);return entry?copyEntry(entry):undefined}
 async update(id:string,patch:ChatPatch,expectedRevision:StorageRevision,signal?:AbortSignal):Promise<ChatEntry>{
  signal?.throwIfAborted();requireId(id);requireId(expectedRevision)
  if(!patch||typeof patch!=='object'||Array.isArray(patch)||Object.keys(patch).length===0||Object.keys(patch).some(k=>!['interaction','plans'].includes(k))||patch.interaction!==undefined&&!['plan','work'].includes(patch.interaction)||patch.plans!==undefined&&!Array.isArray(patch.plans))throw new StorageInputError()
  const frozen=jsonCopy(patch),held=this.chats.get(id)
  if(!held)throw new ChatNotFound()
  if(held.revision!==expectedRevision)throw new ChatRevisionConflict(expectedRevision,held.revision)
  const entry={...held,chat:jsonCopy({...held.chat,...frozen}),revision:crypto.randomUUID()}
  this.options.beforeUpdate?.()
  this.chats.set(id,entry)
  this.options.afterUpdate?.()
  return copyEntry(entry)
 }
 async list(request:ChatListRequest,signal?:AbortSignal):Promise<ChatPage>{
  signal?.throwIfAborted();requireLimit(request.limit)
  if(request.kind!==undefined&&!['chat','draft'].includes(request.kind))throw new StorageInputError()
  let entries:ChatEntry[],offset:number
  if(request.cursor!==undefined){
   const saved=this.pages.get(request.cursor)
   if(!saved||saved.kind!==request.kind)throw new StorageCursorInvalid()
   entries=saved.entries;offset=saved.offset
  }else{entries=[...this.chats.values()].filter(e=>request.kind===undefined||e.kind===request.kind).sort((a,b)=>a.chat.id<b.chat.id?-1:a.chat.id>b.chat.id?1:0).map(copyEntry);offset=0}
  const items:ChatEntry[]=[];let pageBytes=0
  for(const entry of entries.slice(offset,offset+request.limit)){const bytes=new TextEncoder().encode(JSON.stringify(entry)).length;if(pageBytes+bytes>STORAGE_TRANSACTION_BYTES)break;items.push(copyEntry(entry));pageBytes+=bytes}
  const next=offset+items.length,hasMore=next<entries.length
  let nextCursor:StorageCursor|undefined
  if(hasMore){nextCursor=crypto.randomUUID();this.pages.set(nextCursor,{entries,offset:next,kind:request.kind})}
  return {items,hasMore,...(nextCursor===undefined?{}:{nextCursor})}
 }
}
