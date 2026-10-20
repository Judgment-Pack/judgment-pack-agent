import defaults from '../contract/policy.json'
/**
 * The time bounds the harness adds to the Desk's table (budget.json): one turn's wall clock, after which the run
 * pauses as budget_paused does, and one tool call's, after which the model reads a failed result. The Desk's table
 * carries neither at the pinned commit; a table that does wins over these.
 */
export const TIME_DEFAULTS = Object.freeze({turnMilliseconds:15*60_000,toolCallMilliseconds:120_000})
/**
 * How many images, and how many of their bytes decoded, one model request may carry (agent#5): the
 * newest are shown, and an older one past either bound is left out with a line saying so. Not in
 * the Desk's table; a table that carries them wins. Sixteen MiB is one image at its own bound.
 */
export const IMAGE_DEFAULTS = Object.freeze({imagesPerRequest:8,imageBytesPerRequest:16*1024*1024})
/**
 * The bounds the harness puts on the Runtime's test tool, experimental_test_cases (agent#7), which the Desk's table
 * does not carry either: how many test calls one turn may make (each re-sends the whole pack, so a test, fix and
 * retest loop is held to a few rounds rather than to the request allowance), and the most bytes the pack text and the
 * matrix text may each be, which the tool gate holds on the wire. A table that carries one wins over these.
 */
export const TEST_DEFAULTS = Object.freeze({testCallsPerTurn:3,testPackBytes:65_536,testMatrixBytes:65_536})
export type TestBounds = {testCallsPerTurn:number;testPackBytes:number;testMatrixBytes:number}
/**
 * The bounds on the index a run's first message carries of the results earlier runs in its chat
 * kept (agent#8): how many results it lists, and its most bytes, data block included. Not in the
 * Desk's table; a table that carries them wins.
 */
export const INDEX_DEFAULTS = Object.freeze({turnIndexEntries:20,turnIndexBytes:8000})
/**
 * The most UTF-8 bytes the project's own instructions may be (ADR-0005, `projectInstructions` in
 * host.ts): a longer answer is refused before the run's first request, as a brief over `briefBytes` and
 * memory over `memoryBytes` are. Not in the Desk's table; a table that carries it wins.
 */
export const PROMPT_DEFAULTS = Object.freeze({projectInstructionsBytes:4000})
/**
 * The bounds on tools with a source (ADR-0006, `toolSources.ts`): the most bytes of a description the
 * model is shown; how many such tools, and how many bytes of their names, descriptions and schemas,
 * one run offers before they become a catalogue that `load_tool` loads from; the largest result shown
 * whole; and per source, the calls one turn may make and one call's deadline. Not in the Desk's
 * table; a table that carries them wins.
 */
export const SOURCE_DEFAULTS = Object.freeze({toolDescriptionBytes:1024,offeredSchemas:16,offeredSchemaBytes:24_000,sourceResultBytes:8000,sourceCallsPerTurn:8,sourceCallMilliseconds:30_000})
export type ContextPolicy = typeof defaults & {turnMilliseconds:number;toolCallMilliseconds:number;imagesPerRequest:number;imageBytesPerRequest:number;turnIndexEntries:number;turnIndexBytes:number;projectInstructionsBytes:number} & TestBounds & {-readonly [K in keyof typeof SOURCE_DEFAULTS]:number}
export interface ModelContextBudget {contextWindowTokens:number;outputTokens:number}
export interface ContextConfiguration {version:1;projectBrief?:string;models?:Record<string,ModelContextBudget>}
export const CONTEXT_DEFAULTS:Readonly<ContextPolicy> = Object.freeze({...TIME_DEFAULTS,...IMAGE_DEFAULTS,...TEST_DEFAULTS,...INDEX_DEFAULTS,...PROMPT_DEFAULTS,...SOURCE_DEFAULTS,...defaults})
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
/** The budget one model runs under: the table's, then the window and output cap of the model's
 * capability record where it carries them (agent#4), then the assistant profile's own override for
 * the model, which a person wrote and which therefore stays the last word. `memory` (agent#11): the
 * host's word on whether the server adds recalled project memory to the run's requests; `false`
 * frees the `memoryBytes` reserve, and anything else keeps it. */
export function contextPolicy(config:ContextConfiguration|undefined,model:string,capabilities?:Partial<ModelContextBudget>,memory?:boolean):ContextPolicy {
 const record=capabilities?.contextWindowTokens!==undefined&&capabilities.outputTokens!==undefined?{contextWindowTokens:capabilities.contextWindowTokens,outputTokens:capabilities.outputTokens}:undefined
 return {...CONTEXT_DEFAULTS,...record,...(config?contextConfiguration(config).models?.[model]:undefined),...(memory===false?{memoryBytes:0}:{})}
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
