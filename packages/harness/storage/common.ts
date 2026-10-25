import {AIFailureError,failure} from '../failures'
/** Storage tokens are opaque, scoped host values; they are never record digests or authority. */
export type StorageRevision = string
export type StorageCursor = string
export const STORAGE_PAGE_LIMIT = 100
export const STORAGE_RECORD_BYTES = 4 * 1024 * 1024
export const STORAGE_TRANSACTION_BYTES = 8 * 1024 * 1024
export const STORAGE_EVENT_LIMIT = 256

export class StorageInputError extends Error {
 constructor(){super('The storage request is invalid or exceeds its bounds.');this.name='StorageInputError'}
}
export class StorageConflict extends AIFailureError {
 constructor(readonly expectedRevision:StorageRevision|null,readonly actualRevision:StorageRevision|null){super({...failure('stale-state','storage','not-started'),detail:'StorageConflict: The run changed; no storage write was applied.'});this.name='StorageConflict'}
}
export class StorageCommitMismatch extends AIFailureError {
 constructor(){super({...failure('storage','storage','not-started'),detail:'StorageCommitMismatch: This commit identity already names different content.'});this.name='StorageCommitMismatch'}
}
export class StorageCursorInvalid extends Error {
 constructor(){super('The storage cursor is invalid for this resource.');this.name='StorageCursorInvalid'}
}
export class ChatRevisionConflict extends AIFailureError {
 constructor(readonly expectedRevision:StorageRevision,readonly actualRevision:StorageRevision){super({...failure('stale-state','storage','not-started'),detail:'ChatRevisionConflict: The chat changed; no update was applied.'});this.name='ChatRevisionConflict'}
}
export class ChatNotFound extends Error {
 constructor(){super('The chat does not exist in this store.');this.name='ChatNotFound'}
}
export const storageId=(value:unknown):value is string=>typeof value==='string'&&value.length>0&&value.length<=256&&new TextEncoder().encode(value).length<=256&&!/[\u0000-\u001f\u007f-\u009f\ud800-\udfff]/u.test(value)
export function requireId(value:unknown):asserts value is string {if(!storageId(value))throw new StorageInputError()}
export function requireLimit(value:unknown):asserts value is number {if(!Number.isInteger(value)||Number(value)<1||Number(value)>STORAGE_PAGE_LIMIT)throw new StorageInputError()}
export function bytesCopy(value:Uint8Array):Uint8Array {
 if((!ArrayBuffer.isView(value)||Object.prototype.toString.call(value)!=='[object Uint8Array]')||value.byteLength===0||value.byteLength>STORAGE_RECORD_BYTES)throw new StorageInputError()
 const out=new Uint8Array(value)
 try{new TextDecoder('utf-8',{fatal:true}).decode(out)}catch{throw new StorageInputError()}
 return out
}
export const sameBytes=(a:Uint8Array|null,b:Uint8Array|null)=>a===null?b===null:b!==null&&a.length===b.length&&a.every((v,i)=>v===b[i])
