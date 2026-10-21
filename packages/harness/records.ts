import type {RecordMarker} from './outputGuard'
import {canonicalJSON} from './context/chat'

export const CHECKPOINT_SCHEMA='https://github.com/Judgment-Pack/judgment-pack-agent/blob/main/contract/schemas/checkpoint.json'
export const PROVENANCE_SCHEMA='https://github.com/Judgment-Pack/judgment-pack-agent/blob/main/contract/schemas/provenance.json'
export const CRITIQUE_SCHEMA='https://github.com/Judgment-Pack/judgment-pack-agent/blob/main/contract/schemas/run-event.json#/$defs/critique'
export const EXPORT_SCHEMA='https://github.com/Judgment-Pack/judgment-pack-agent/blob/main/contract/schemas/export.json'
export const RECORD_SCHEMAS=[CHECKPOINT_SCHEMA,PROVENANCE_SCHEMA,CRITIQUE_SCHEMA] as const
export type RecordSchema=typeof RECORD_SCHEMAS[number]
export interface RecordsPolicy {retainDays:number;redactOnStore:boolean;exportFormat:'json'}
export interface RecordMembers<S extends RecordSchema=RecordSchema> {schema:S;version:1;digest:string;retainUntil:string;records?:RecordMarker}
export interface RecordGuard {declaredNames?:ReadonlySet<string>;value<T>(input:T,where:'message',record?:boolean,marker?:RecordMarker):T}
export interface ExportEntry extends RecordMembers {bytes:number}
export interface ExportManifest {
 schema:typeof EXPORT_SCHEMA;version:1;digest:string;retainUntil:string;createdAt:string;packageVersion:string
 policy:RecordsPolicy;records:ExportEntry[]
}
export interface RecordsExport {manifest:ExportManifest;records:Record<string,unknown>[]}
export interface ExportWritten {type:'export_written';records:number;bytes:number;digest:string}

export const RECORD_DEFAULTS:RecordsPolicy={retainDays:90,redactOnStore:true,exportFormat:'json'}
const DIGEST=/^sha256:[a-f0-9]{64}$/
const DAY=86_400_000

/** A small synchronous SHA-256 for record snapshots, whose existing checkpoint API is synchronous. */
export function sha256(text:string):string{
 const bytes=new TextEncoder().encode(text),bits=bytes.length*8,padded=new Uint8Array(((bytes.length+9+63)>>6)<<6)
 padded.set(bytes);padded[bytes.length]=0x80
 const view=new DataView(padded.buffer);view.setUint32(padded.length-4,bits>>>0);view.setUint32(padded.length-8,Math.floor(bits/0x1_0000_0000))
 const k=[0x428a2f98,0x71374491,0xb5c0fbcf,0xe9b5dba5,0x3956c25b,0x59f111f1,0x923f82a4,0xab1c5ed5,0xd807aa98,0x12835b01,0x243185be,0x550c7dc3,0x72be5d74,0x80deb1fe,0x9bdc06a7,0xc19bf174,0xe49b69c1,0xefbe4786,0x0fc19dc6,0x240ca1cc,0x2de92c6f,0x4a7484aa,0x5cb0a9dc,0x76f988da,0x983e5152,0xa831c66d,0xb00327c8,0xbf597fc7,0xc6e00bf3,0xd5a79147,0x06ca6351,0x14292967,0x27b70a85,0x2e1b2138,0x4d2c6dfc,0x53380d13,0x650a7354,0x766a0abb,0x81c2c92e,0x92722c85,0xa2bfe8a1,0xa81a664b,0xc24b8b70,0xc76c51a3,0xd192e819,0xd6990624,0xf40e3585,0x106aa070,0x19a4c116,0x1e376c08,0x2748774c,0x34b0bcb5,0x391c0cb3,0x4ed8aa4a,0x5b9cca4f,0x682e6ff3,0x748f82ee,0x78a5636f,0x84c87814,0x8cc70208,0x90befffa,0xa4506ceb,0xbef9a3f7,0xc67178f2]
 const h=[0x6a09e667,0xbb67ae85,0x3c6ef372,0xa54ff53a,0x510e527f,0x9b05688c,0x1f83d9ab,0x5be0cd19],w=new Uint32Array(64),rr=(x:number,n:number)=>(x>>>n)|(x<<(32-n))
 for(let offset=0;offset<padded.length;offset+=64){for(let i=0;i<16;i++)w[i]=view.getUint32(offset+i*4);for(let i=16;i<64;i++){const a=w[i-15]!,b=w[i-2]!,s0=rr(a,7)^rr(a,18)^(a>>>3),s1=rr(b,17)^rr(b,19)^(b>>>10);w[i]=(w[i-16]!+s0+w[i-7]!+s1)>>>0}let [a,b,c,d,e,f,g,z]=h;for(let i=0;i<64;i++){const s1=rr(e!,6)^rr(e!,11)^rr(e!,25),ch=(e!&f!)^(~e!&g!),t1=(z!+s1+ch+k[i]!+w[i]!)>>>0,s0=rr(a!,2)^rr(a!,13)^rr(a!,22),maj=(a!&b!)^(a!&c!)^(b!&c!),t2=(s0+maj)>>>0;z=g;g=f;f=e;e=(d!+t1)>>>0;d=c;c=b;b=a;a=(t1+t2)>>>0}for(let i=0;i<8;i++)h[i]=(h[i]!+[a,b,c,d,e,f,g,z][i]!)>>>0}
 return h.map(n=>n.toString(16).padStart(8,'0')).join('')
}

const withoutDigest=(record:object)=>Object.fromEntries(Object.entries(record).filter(([name])=>name!=='digest'))
export const recordDigest=(record:object)=>`sha256:${sha256(canonicalJSON(withoutDigest(record)))}`
export function retainUntil(policy:RecordsPolicy,now=new Date()):string{return new Date(now.getTime()+policy.retainDays*DAY).toISOString()}
export function recordsPolicy(members:Record<string,{value:unknown}>):RecordsPolicy{
 const retainDays=members['records.retainDays']?.value??RECORD_DEFAULTS.retainDays
 const redactOnStore=members['records.redactOnStore']?.value??RECORD_DEFAULTS.redactOnStore
 const exportFormat=members['records.exportFormat']?.value??RECORD_DEFAULTS.exportFormat
 if(!Number.isSafeInteger(retainDays)||Number(retainDays)<1||Number(retainDays)>36500||typeof redactOnStore!=='boolean'||exportFormat!=='json')throw Error('Invalid records policy.')
 return {retainDays:Number(retainDays),redactOnStore,exportFormat}
}

// Only model-derived members enter the store guard. Each selected member has its own budget.
const RECORD_TEXT_PATHS=[
 'text','explanation','reason','plan.*.step','plan.*.detail',
 'operations.*.name','operations.*.args','approval.name','approval.tool','approval.args',
 'memories.proposed.*.text','compaction.summaries.*.summary',
 'context.entries.*.value.@assistant.content.@text.text',
 'context.entries.*.value.@assistant.content.@call.toolName','context.entries.*.value.@assistant.content.@call.input',
 'context.entries.*.value.@assistant.@answer','context.entries.@draft.value.text','context.entries.@draft.value.unknowns',
] as const
function guardRecord(record:Record<string,unknown>,guard:RecordGuard,figures:RecordMarker):void{
 const scan=(node:unknown,path:readonly string[])=>{
  if(!node||typeof node!=='object')return
  const [key,...rest]=path
  if(key==='*'){if(Array.isArray(node))for(const item of node)scan(item,rest);return}
  if(key?.startsWith('@')&&key!=='@answer'){
   if(Array.isArray(node))for(const item of node){
    const matches=key==='@draft'?typeof item?.label==='string'&&item.label.startsWith('draft:'):key==='@assistant'?item?.role==='assistant':key==='@text'?item?.type==='text':item?.type==='tool-call'
    if(matches)scan(item,rest)
   }
   return
  }
  const object=node as Record<string,unknown>
  if(key==='@answer'){if(typeof object.content==='string')object.content=guard.value(object.content,'message',true,figures);return}
  if(key===undefined||!Object.hasOwn(object,key))return
  if(rest.length){scan(object[key],rest);return}
  if((key==='name'||key==='tool'||key==='toolName')&&typeof object[key]==='string'&&guard.declaredNames?.has(object[key] as string))return
  object[key]=guard.value(object[key],'message',true,figures)
 }
 for(const path of RECORD_TEXT_PATHS)scan(record,path.split('.'))
}

export function writeRecord<S extends RecordSchema,T extends Record<string,unknown>>(schema:S,value:T,policy:RecordsPolicy,guard?:RecordGuard,now=new Date(),keepRetainUntil?:string):T&RecordMembers<S>{
 let record={...structuredClone(value),schema,version:1 as const,retainUntil:keepRetainUntil??retainUntil(policy,now)} as T&Omit<RecordMembers<S>,'digest'>
 const figures:RecordMarker={redactions:{}}
 if(policy.redactOnStore&&guard)guardRecord(record,guard,figures)
 Object.assign(record,{records:figures})
 return {...record,digest:recordDigest(record)}
}
export function validRecord(value:unknown):value is Record<string,unknown>&RecordMembers{
 if(!value||typeof value!=='object'||Array.isArray(value))return false
 const record=value as Record<string,unknown>
 return RECORD_SCHEMAS.includes(record.schema as RecordSchema)&&record.version===1&&typeof record.digest==='string'&&DIGEST.test(record.digest)&&record.digest===recordDigest(record)&&typeof record.retainUntil==='string'&&record.retainUntil.length<=40&&Number.isFinite(Date.parse(record.retainUntil))
}
export function validRecordHeader(value:unknown,schema:RecordSchema):value is Record<string,unknown>&RecordMembers{
 if(!value||typeof value!=='object'||Array.isArray(value))return false
 const record=value as Record<string,unknown>,until=record.retainUntil
 return record.schema===schema&&record.version===1&&typeof record.digest==='string'&&DIGEST.test(record.digest)&&typeof until==='string'&&until.length<=40&&Number.isFinite(Date.parse(until))
}
export class RecordsExpired extends Error{
 constructor(){super('records expired')}
 event(){return {type:'guardrail' as const,tool:'resume',action:'refused' as const,reason:'records expired' as const,detail:'records expired'} }
}
export function refuseExpired(record:{retainUntil:string},now=new Date()):void{if(Date.parse(record.retainUntil)<=now.getTime())throw new RecordsExpired()}

export function exportRecords(records:unknown[],policy:RecordsPolicy,packageVersion:string,now=new Date()):{value:RecordsExport;text:string;event:ExportWritten}{
 if(!Array.isArray(records)||records.length>10000||records.some(record=>!validRecord(record)))throw Error('The host supplied an invalid record for export.')
 const copies=structuredClone(records) as (Record<string,unknown>&RecordMembers)[]
 const entries=copies.map(record=>({schema:record.schema,version:record.version,digest:record.digest,retainUntil:record.retainUntil,bytes:new TextEncoder().encode(canonicalJSON(record)).length}))
 const body={schema:EXPORT_SCHEMA as typeof EXPORT_SCHEMA,version:1 as const,retainUntil:retainUntil(policy,now),createdAt:now.toISOString(),packageVersion,policy:structuredClone(policy),records:entries}
 const manifest={...body,digest:recordDigest(body)}
 const value={manifest,records:copies},text=canonicalJSON(value),digest=`sha256:${sha256(text)}`
 return {value,text,event:{type:'export_written',records:copies.length,bytes:new TextEncoder().encode(text).length,digest}}
}
