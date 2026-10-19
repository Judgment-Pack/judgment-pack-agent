import defaults from '../contract/policy.json'
/**
 * The time bounds the harness adds to the Desk's table (budget.json): one turn's wall clock, after which the run
 * pauses as budget_paused does, and one tool call's, after which the model reads a failed result. The Desk's table
 * carries neither at the pinned commit; a table that does wins over these.
 */
export const TIME_DEFAULTS = Object.freeze({turnMilliseconds:15*60_000,toolCallMilliseconds:120_000})
/**
 * The bounds the harness puts on the Runtime's test tool, experimental_test_cases (agent#7), which the Desk's table
 * does not carry either: how many test calls one turn may make (each re-sends the whole pack, so a test, fix and
 * retest loop is held to a few rounds rather than to the request allowance), and the most bytes the pack text and the
 * matrix text may each be, which the tool gate holds on the wire. A table that carries one wins over these.
 */
export const TEST_DEFAULTS = Object.freeze({testCallsPerTurn:3,testPackBytes:65_536,testMatrixBytes:65_536})
export type TestBounds = {testCallsPerTurn:number;testPackBytes:number;testMatrixBytes:number}
export type ContextPolicy = typeof defaults & {turnMilliseconds:number;toolCallMilliseconds:number} & TestBounds
export interface ModelContextBudget {contextWindowTokens:number;outputTokens:number}
export interface ContextConfiguration {version:1;projectBrief?:string;models?:Record<string,ModelContextBudget>}
export const CONTEXT_DEFAULTS:Readonly<ContextPolicy> = Object.freeze({...TIME_DEFAULTS,...TEST_DEFAULTS,...defaults})
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
