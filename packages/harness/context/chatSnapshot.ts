import {canonicalJSON} from './chat'
import {sha256} from '../records'
import {readCanonicalHistoryJSON,readHistorySourceJSON,HISTORY_TOTAL_BYTES} from '../history/codec'
/**
 * The version a snapshot is written at. Version 2 has version 1's shape and says its cache rows were
 * keyed only from arguments their JSON text says exactly (#271). A version 1 row may hold the answer to
 * other arguments than its key names, and being a key and an entry it cannot be told apart, so version
 * 1 still loads and restores without its cache rows (`ChatContext.restore`).
 */
export const CHAT_SNAPSHOT_VERSION=2
export class ContextSnapshotInvalid extends Error{constructor(){super('ContextSnapshotInvalid: the chat context snapshot is invalid.');this.name='ContextSnapshotInvalid'}}
export class UnsupportedContextSnapshotVersion extends Error{constructor(){super('UnsupportedContextSnapshotVersion: the chat context snapshot version is unsupported.');this.name='UnsupportedContextSnapshotVersion'}}
export class ContextTurnBindingInvalid extends Error{constructor(){super('ContextTurnBindingInvalid: the active chat turn cannot be attached.');this.name='ContextTurnBindingInvalid'}}
export interface SnapshotEntry{id:string;kind:'history'|'artifact'|'request'|'tool';label:string;valueJSON:string;bytes:number;digest:string;revision?:string}
export interface SnapshotEviction{tool:string;arguments:string;bytes:number}
export interface ChatContextSnapshot{
 version:1|2;scope:string;turns:number;disposed:boolean;size:number;entries:SnapshotEntry[];ages:{id:string;turn:number}[]
 results:{tool:string;arguments:string;entry:string;bytes:number;firstLine:string;turn:number}[];cache:{key:string;entry:string}[];gone:string[]
 visibility:string[][];evictions:SnapshotEviction[];evicted:number[];threads:{profile:string;title:string;exchangesJSON:string;visible?:number}[]
 tasks:{id:string;thread:number}[];reusable:{key:string;value:{task:string;model:string;text:string;questionsJSON:string}}[]
 open:{number:number;referenced:string[];visible?:number;offered:number[]}[]
}
const invalid=():never=>{throw new ContextSnapshotInvalid()}
const object=(v:unknown):v is Record<string,unknown>=>!!v&&typeof v==='object'&&!Array.isArray(v)
const count=(v:unknown,min=0)=>Number.isSafeInteger(v)&&Number(v)>=min
const text=(v:unknown):v is string=>typeof v==='string'
const closed=(v:unknown,required:string[],optional:string[]=[]):v is Record<string,unknown>=>object(v)&&required.every(k=>Object.hasOwn(v,k))&&Object.keys(v).every(k=>required.includes(k)||optional.includes(k))
const unique=(rows:unknown[])=>new Set(rows).size===rows.length
const strings=(v:unknown):v is string[]=>Array.isArray(v)&&v.every(text)&&unique(v)
const sorted=(v:string[])=>v.every((item,i)=>i===0||v[i-1]!<item)
export function readChatContextSnapshot(bytes:Uint8Array):ChatContextSnapshot{
 let input:unknown;try{input=readCanonicalHistoryJSON(bytes,HISTORY_TOTAL_BYTES)}catch{return invalid()}
 if(object(input)&&count(input.version,CHAT_SNAPSHOT_VERSION+1))throw new UnsupportedContextSnapshotVersion()
 const required=['version','scope','turns','disposed','size','entries','ages','results','cache','gone','visibility','evictions','evicted','threads','tasks','reusable','open']
 if(!closed(input,required)||!count(input.version,1)||!text(input.scope)||!count(input.turns)||typeof input.disposed!=='boolean'||!count(input.size))return invalid()
 for(const key of required.slice(5))if(!Array.isArray(input[key]))return invalid()
 const v=input as unknown as ChatContextSnapshot,scoped=(id:unknown)=>text(id)&&id.startsWith(v.scope+':')
 let size=0
 for(const e of v.entries){
  if(!closed(e,['id','kind','label','valueJSON','bytes','digest'],['revision'])||!scoped(e.id)||!['history','artifact','request','tool'].includes(e.kind)||!text(e.label)||!text(e.valueJSON)||!count(e.bytes)||!text(e.digest)||e.revision!==undefined&&!text(e.revision))return invalid()
  let value:unknown;try{value=readHistorySourceJSON(e.valueJSON)}catch{return invalid()}
  if(JSON.stringify(value)!==e.valueJSON||new TextEncoder().encode(e.valueJSON).length!==e.bytes||sha256(e.valueJSON)!==e.digest)return invalid()
  size+=e.bytes
 }
 if(size!==v.size||!unique(v.entries.map(e=>e.id)))return invalid()
 if(v.ages.some(a=>!closed(a,['id','turn'])||!scoped(a.id)||!count(a.turn,1)||a.turn>v.turns)||!sorted(v.ages.map(a=>a.id)))return invalid()
 if(v.results.some(r=>!closed(r,['tool','arguments','entry','bytes','firstLine','turn'])||!text(r.tool)||!text(r.arguments)||!scoped(r.entry)||!count(r.bytes)||!text(r.firstLine)||!count(r.turn,1)||r.turn>v.turns)||!unique(v.results.map(r=>r.tool+'\n'+r.arguments)))return invalid()
 if(v.cache.some(r=>!closed(r,['key','entry'])||!text(r.key)||!scoped(r.entry))||!sorted(v.cache.map(r=>r.key))||!strings(v.gone)||v.gone.length>4096||!v.gone.every(scoped))return invalid()
 if(v.visibility.some(set=>!strings(set)||!sorted(set)||!set.every(scoped)))return invalid()
 if(v.evictions.some(e=>!closed(e,['tool','arguments','bytes'])||!text(e.tool)||!text(e.arguments)||!count(e.bytes)))return invalid()
 const refs=(rows:unknown,bound:number):rows is number[]=>Array.isArray(rows)&&rows.every(n=>count(n)&&Number(n)<bound)&&unique(rows)
 if(!refs(v.evicted,v.evictions.length))return invalid()
 const jsonArray=(source:unknown,test:(value:unknown)=>boolean)=>{if(!text(source))return false;try{const parsed:unknown=readHistorySourceJSON(source);return Array.isArray(parsed)&&parsed.every(test)&&JSON.stringify(parsed)===source}catch{return false}}
 if(v.threads.some(t=>!closed(t,['profile','title','exchangesJSON'],['visible'])||!text(t.profile)||!text(t.title)||!jsonArray(t.exchangesJSON,()=>true)||t.visible!==undefined&&(!count(t.visible)||t.visible>=v.visibility.length)))return invalid()
 if(v.tasks.length>64||v.tasks.some(t=>!closed(t,['id','thread'])||!text(t.id)||!count(t.thread)||t.thread>=v.threads.length)||!unique(v.tasks.map(t=>t.id)))return invalid()
 if(v.reusable.length>64||v.reusable.some(r=>!closed(r,['key','value'])||!text(r.key)||!closed(r.value,['task','model','text','questionsJSON'])||!text(r.value.task)||!text(r.value.model)||!text(r.value.text)||!jsonArray(r.value.questionsJSON,()=>true))||!unique(v.reusable.map(r=>r.key)))return invalid()
 if(v.open.some(t=>!closed(t,['number','referenced','offered'],['visible'])||!count(t.number,1)||t.number>v.turns||!strings(t.referenced)||!t.referenced.every(scoped)||!refs(t.offered,v.evictions.length)||t.visible!==undefined&&(!count(t.visible)||t.visible>=v.visibility.length))||!v.open.every((t,i)=>i===0||v.open[i-1]!.number<t.number))return invalid()
 const usedThreads=new Set(v.tasks.map(t=>t.thread)),usedVisibility=new Set([...v.threads.flatMap(t=>t.visible===undefined?[]:[t.visible]),...v.open.flatMap(t=>t.visible===undefined?[]:[t.visible])]),usedEvictions=new Set([...v.evicted,...v.open.flatMap(t=>t.offered)])
 if(usedThreads.size!==v.threads.length||usedVisibility.size!==v.visibility.length||usedEvictions.size!==v.evictions.length)return invalid()
 if(v.disposed&&(v.open.length||v.entries.length||v.ages.length||v.results.length||v.cache.length||v.gone.length||v.visibility.length||v.evictions.length||v.threads.length||v.tasks.length||v.reusable.length))return invalid()
 return v
}
/** Snapshot JSON is canonical externally; valueJSON companions preserve opaque source member order. */
export const canonicalChatContextSnapshot=(snapshot:ChatContextSnapshot)=>new TextEncoder().encode(canonicalJSON(snapshot))

/** Wire values preserve Go's existing arbitrary Value-array domain. A TS live restore must
 * satisfy its public row types; extra host fields remain preserved by structural typing. */
export function supportsTypeScriptChatSnapshot(saved:ChatContextSnapshot):boolean{
 const exchange=(e:unknown)=>object(e)&&text(e.objective)&&text(e.result)&&(e.partial===undefined||e.partial===true)&&(e.more===undefined||object(e.more)&&e.more.readWith==='read_window'&&text(e.more.id)&&text(e.more.cursor)&&count(e.more.totalCharacters))
 const question=(q:unknown)=>object(q)&&['question','proposal','delegation'].includes(String(q.kind))&&text(q.text)&&(q.ref===undefined||text(q.ref))
 return saved.threads.every(row=>row.visible!==undefined&&(JSON.parse(row.exchangesJSON) as unknown[]).every(exchange))&&saved.reusable.every(row=>(JSON.parse(row.value.questionsJSON) as unknown[]).every(question))
}
