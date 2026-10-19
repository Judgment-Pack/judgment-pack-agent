import {answer,deskFetch} from '../host'
export interface MemorySource {chatId:string;turnId:string}
export interface MemoryEntry {id:string;key:string;text:string;state:'active'|'pending'|'superseded';createdAt:string;updatedAt:string;source?:MemorySource;stale?:boolean;dependencies?:{path:string;sha256:string}[]}
export interface MemoryReply {sha256:string;content:{version:1;settings:{use:boolean;suggest:boolean};entries:MemoryEntry[]}}
export const MEMORY_KEY=['project-memory'] as const
export async function memoryRequest<T=MemoryReply>(action?:Record<string,unknown>,digest?:string,suffix='',signal?:AbortSignal):Promise<T>{return answer<T>(await deskFetch('/api/ai-memory'+suffix,{signal,...(action?{method:'POST',headers:{'Content-Type':'application/json',...(digest?{'If-Match':digest}:{})},body:JSON.stringify(action)}:{})}))}
