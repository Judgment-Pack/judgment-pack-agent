import defaults from '../contract/policy.json'
export type ContextPolicy = typeof defaults
export interface ModelContextBudget {contextWindowTokens:number;outputTokens:number}
export interface ContextConfiguration {version:1;projectBrief?:string;models?:Record<string,ModelContextBudget>}
export const CONTEXT_DEFAULTS:Readonly<ContextPolicy> = Object.freeze(defaults)
export function contextConfiguration(value:unknown):ContextConfiguration {
 if(!value||typeof value!=='object'||Array.isArray(value))throw Error('Invalid assistant context configuration')
 const c=value as Record<string,unknown>
 if(c.version!==1||Object.keys(c).some(k=>!['version','projectBrief','models'].includes(k)))throw Error('Invalid assistant context configuration')
 if(c.projectBrief!==undefined&&(typeof c.projectBrief!=='string'||!c.projectBrief||c.projectBrief.length>512||/[\\\0\r\n:]/.test(c.projectBrief)||c.projectBrief.split('/').some(p=>!p||p==='.'||p==='..')))throw Error('Project brief must be a relative project file')
 if(c.models!==undefined){if(!c.models||typeof c.models!=='object'||Array.isArray(c.models)||Object.keys(c.models).length>128)throw Error('Invalid model context budgets');for(const [name,value]of Object.entries(c.models)){
  const b=value as ModelContextBudget
  if(!name||name.trim()!==name||name.length>128||/[\r\n\0]/.test(name)||!b||Object.keys(b).some(k=>!['contextWindowTokens','outputTokens'].includes(k))||!Number.isInteger(b.contextWindowTokens)||b.contextWindowTokens<4096||b.contextWindowTokens>2000000||!Number.isInteger(b.outputTokens)||b.outputTokens<1024||b.outputTokens>b.contextWindowTokens/2)throw Error('Invalid model context budget')
 }}
 return value as ContextConfiguration
}
export function contextPolicy(config:ContextConfiguration|undefined,model:string):ContextPolicy {
 return {...CONTEXT_DEFAULTS,...(config?contextConfiguration(config).models?.[model]:undefined)}
}
export const utf8Bytes=(text:string)=>new TextEncoder().encode(text).length
export const inputBytes=(policy:ContextPolicy)=>Math.min(policy.maxInputBytes,Math.max(1024,(policy.contextWindowTokens-policy.outputTokens)*3))
export function prefix(text:string,bytes:number):string {
 if(utf8Bytes(text)<=bytes)return text
 const data=new TextEncoder().encode(text);let end=Math.max(0,Math.min(data.length,bytes));while(end>0&&(data[end]!&0xc0)===0x80)end--
 return new TextDecoder().decode(data.subarray(0,end))
}
/** What a reading tool's result carries beside its window: the reader's own
 * metadata (its item, offsets, cursor, a short header) and the result's JSON
 * wrapper. A reader whose metadata is longer cuts its window by the excess. */
export const READ_ENVELOPE_BYTES=1024
/** The largest window a reading tool may return: the result limit less the
 * envelope, counted in bytes of the window's JSON-encoded text (escapes
 * included). A read held to it is one result, never a ledger reference and a
 * preview that the model has to page back through `read_context`. Every
 * reading tool a host offers uses this, through `encodedPrefix`. */
export const readWindowBytes=(policy:Pick<ContextPolicy,'toolResultBytes'>)=>Math.max(256,policy.toolResultBytes-READ_ENVELOPE_BYTES)
/** The longest prefix of `text` whose JSON encoding is at most `bytes` bytes,
 * quotes and escapes included, never splitting a surrogate pair. */
export function encodedPrefix(text:string,bytes:number):string {
 let size=2,end=0
 while(end<text.length){
  const code=text.codePointAt(end)!,width=code>0xffff?2:1
  const cost=code===0x22||code===0x5c||code===0x08||code===0x09||code===0x0a||code===0x0c||code===0x0d?2:code<0x20||code>=0xd800&&code<=0xdfff?6:code<0x80?1:code<0x800?2:code<0x10000?3:4
  if(size+cost>bytes)break
  size+=cost;end+=width
 }
 return text.slice(0,end)
}
export class ContextBudgetError extends Error {readonly failureCode='context-limit';constructor(){super('The current AI context exceeds its configured budget. Saved work is retained; reduce the current request or read smaller context sections.')}}
