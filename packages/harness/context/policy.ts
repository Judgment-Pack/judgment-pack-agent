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
export class ContextBudgetError extends Error {readonly failureCode='context-limit';constructor(){super('The current AI context exceeds its configured budget. Saved work is retained; reduce the current request or read smaller context sections.')}}
