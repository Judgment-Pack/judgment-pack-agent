import {readCompletion,type ModelCompletion} from '../completion'
import {readFailure,type AIFailure} from '../failures'
import type {McpToolResult} from '../engine'
import {MAX_TURNS} from '../engines/contract'
import {utf8Bytes} from '../context/policy'

export const REQUEST_INCREMENT = 10
export const MAX_REQUEST_ALLOWANCE = 100
export const MAX_CHECKPOINT_BYTES = 4 * 1024 * 1024
export interface TaskStep {step:string;status:'pending'|'in_progress'|'completed';detail?:string}
export interface Operation {id:string;name:string;args:unknown;status:'running'|'complete'|'failed'|'interrupted';result?:McpToolResult}
export interface RetainedEntry {id:string;kind:'history'|'artifact'|'request'|'tool';label:string;value:unknown;revision?:string;bytes:number;digest:string}
/**
 * One model request's row. `attempt`, `failure` and `waitMs` record a request the provider refused and that was
 * retried (engines/vercel/retry.ts): the refused attempt's row carries its failure and the wait before the next, and
 * the attempt that answered carries its number.
 */
export interface CompletionRecord {request:number;phase:'work'|'synthesis'|'review';result:ModelCompletion;attempt?:number;failure?:AIFailure;waitMs?:number}
/** A local execution record, never permissions, evidence approval or private reasoning. */
export interface ExecutionCheckpoint {
 version:1;id:string;requestKey:string;phase:'plan'|'work';status:'running'|'paused'|'complete'|'stopped';
 unit:'model-requests'|'provider-managed';used:number;limit:number;grants:{from:number;to:number;at:string}[];
 plan:TaskStep[];explanation:string;operations:Operation[];
 completions?:CompletionRecord[];
 context:{scope:string;entries:RetainedEntry[]};reason?:string;
 /** agent#7: the turn's experimental_test_cases calls so far, carried as `used` is. */
 testCalls?:number;
 /** agent#18: each delegated child's cost and time, each batch's total and the parent's own, carried as `used` is. */
 delegation?:DelegationFigures;
}
/** Token counts the provider reported, summed; a member is absent where no request reported it (agent#18). */
export interface TokenFigures {inputTokens?:number;outputTokens?:number;reasoningTokens?:number;cacheReadTokens?:number;cacheWriteTokens?:number}
const TOKEN_KEYS=['inputTokens','outputTokens','reasoningTokens','cacheReadTokens','cacheWriteTokens'] as const
/**
 * One delegated child's figures (agent#18): its model, its model requests (none on an agent capability,
 * where the provider runs its own loop; agentRuns counts its runs there), its tool calls, the provider's
 * token counts where reported, the time to its first answer byte and its whole duration.
 */
export interface ChildFigures {task:string;batch:number;profile:string;model?:string;state:'queued'|'running'|'complete'|'failed'|'interrupted';modelRequests:number;toolCalls:number;agentRuns?:number;tokens?:TokenFigures;firstByteMs?:number;durationMs:number;partial?:true;reused?:true}
/** A batch's sums, and its wall time from its first child's start to its last child's end. */
export interface FiguresTotal {modelRequests:number;toolCalls:number;agentRuns?:number;tokens?:TokenFigures;durationMs:number}
export interface BatchFigures {batch:number;children:ChildFigures[];total:FiguresTotal}
/** The parent's own: its model, its own model requests (the response's less its children's), and its own token counts. */
export interface ParentFigures {model:string;modelRequests:number;agentRuns?:number;tokens?:TokenFigures}
export interface DelegationFigures {batches:BatchFigures[];parent:ParentFigures}
const count=(v:unknown,max=Number.MAX_SAFE_INTEGER)=>Number.isSafeInteger(v)&&Number(v)>=0&&Number(v)<=max
const exact=(v:Record<string,unknown>,names:readonly string[])=>Object.keys(v).every(k=>names.includes(k))
function validTokens(v:unknown):boolean{return object(v)&&exact(v,TOKEN_KEYS)&&TOKEN_KEYS.every(k=>v[k]===undefined||count(v[k]))}
/** The checkpoint's delegation figures, read closed (agent#18). */
export function validDelegationFigures(v:unknown):v is DelegationFigures {
 const DAY=86400000
 const tokens=(x:Record<string,unknown>)=>x.tokens===undefined||validTokens(x.tokens)
 const child=(c:unknown)=>object(c)&&exact(c,['task','batch','profile','model','state','modelRequests','toolCalls','agentRuns','tokens','firstByteMs','durationMs','partial','reused'])&&text(c.task,36)&&/^[a-f0-9-]{36}$/.test(c.task)&&count(c.batch,100)&&Number(c.batch)>=1&&text(c.profile,48)&&(c.model===undefined||text(c.model,200))&&['queued','running','complete','failed','interrupted'].includes(String(c.state))&&count(c.modelRequests,100)&&count(c.toolCalls,100)&&(c.agentRuns===undefined||count(c.agentRuns,100))&&tokens(c)&&(c.firstByteMs===undefined||count(c.firstByteMs,DAY))&&count(c.durationMs,DAY)&&(c.partial===undefined||c.partial===true)&&(c.reused===undefined||c.reused===true)
 const total=(t:unknown)=>object(t)&&exact(t,['modelRequests','toolCalls','agentRuns','tokens','durationMs'])&&count(t.modelRequests)&&count(t.toolCalls)&&(t.agentRuns===undefined||count(t.agentRuns))&&tokens(t)&&count(t.durationMs,DAY)
 const batch=(b:unknown)=>object(b)&&exact(b,['batch','children','total'])&&count(b.batch,100)&&Number(b.batch)>=1&&Array.isArray(b.children)&&b.children.length>=1&&b.children.length<=3&&b.children.every(child)&&total(b.total)
 const parent=(p:unknown)=>object(p)&&exact(p,['model','modelRequests','agentRuns','tokens'])&&text(p.model,200)&&count(p.modelRequests)&&(p.agentRuns===undefined||count(p.agentRuns))&&tokens(p)
 return object(v)&&exact(v,['batches','parent'])&&Array.isArray(v.batches)&&v.batches.length<=20&&v.batches.every(batch)&&parent(v.parent)
}
const object=(v:unknown):v is Record<string,unknown>=>!!v&&typeof v==='object'&&!Array.isArray(v)
const text=(v:unknown,max:number):v is string=>typeof v==='string'&&v.length<=max
export function validTaskSteps(value:unknown):value is TaskStep[] {
 return Array.isArray(value)&&value.length>0&&value.length<=20&&value.every(s=>object(s)&&text(s.step,1000)&&!!s.step.trim()&&['pending','in_progress','completed'].includes(String(s.status))&&(s.detail===undefined||text(s.detail,1500)))&&value.filter(s=>s.status==='in_progress').length<=1
}
export function readExecution(value:unknown):ExecutionCheckpoint|undefined {
 if(value===undefined)return
 const fail=():never=>{throw Error('Saved execution progress is invalid. History was left untouched.')}
 if(!object(value)||utf8Bytes(JSON.stringify(value))>MAX_CHECKPOINT_BYTES||value.version!==1||!text(value.id,100)||!value.id||!text(value.requestKey,64)||!/^[a-f0-9]{64}$/.test(value.requestKey)||!['plan','work'].includes(String(value.phase))||!['running','paused','complete','stopped'].includes(String(value.status))||!['model-requests','provider-managed'].includes(String(value.unit)))return fail()
 if(!Number.isInteger(value.used)||Number(value.used)<0||!Number.isInteger(value.limit)||Number(value.limit)<1||Number(value.limit)>MAX_REQUEST_ALLOWANCE||Number(value.used)>Number(value.limit)||!Array.isArray(value.grants)||value.grants.length>8)return fail()
 let limit=value.unit==='model-requests'?MAX_TURNS:1
 for(const grant of value.grants){if(!object(grant)||grant.from!==limit||!Number.isInteger(grant.to)||Number(grant.to)<=limit||Number(grant.to)>MAX_REQUEST_ALLOWANCE||typeof grant.at!=='string'||!Number.isFinite(Date.parse(grant.at)))return fail();limit=Number(grant.to)}
 if(value.limit!==limit||!Array.isArray(value.plan)||(value.plan.length&&!validTaskSteps(value.plan))||!text(value.explanation,2000)||(value.reason!==undefined&&!text(value.reason,2000))||(value.testCalls!==undefined&&(!Number.isInteger(value.testCalls)||Number(value.testCalls)<0||Number(value.testCalls)>400))||(value.delegation!==undefined&&!validDelegationFigures(value.delegation)))return fail()
 if(value.completions!==undefined&&(!Array.isArray(value.completions)||value.completions.length>MAX_REQUEST_ALLOWANCE||value.completions.some(row=>!object(row)||!Number.isInteger(row.request)||Number(row.request)<1||Number(row.request)>Number(value.used)||!['work','synthesis','review'].includes(String(row.phase))||!readCompletion(row.result)||row.attempt!==undefined&&(!Number.isInteger(row.attempt)||Number(row.attempt)<1||Number(row.attempt)>3)||row.failure!==undefined&&!readFailure(row.failure)||row.waitMs!==undefined&&(!Number.isInteger(row.waitMs)||Number(row.waitMs)<0||Number(row.waitMs)>86400000))))return fail()
 if(!Array.isArray(value.operations)||value.operations.length>400||new Set(value.operations.map(o=>o.id)).size!==value.operations.length)return fail()
 for(const op of value.operations){if(!object(op)||!text(op.id,100)||!text(op.name,150)||!['running','complete','failed','interrupted'].includes(String(op.status))||op.result!==undefined&&(!object(op.result)||(op.result.content!==undefined&&(!Array.isArray(op.result.content)||op.result.content.some(p=>!object(p)||p.type!==undefined&&typeof p.type!=='string'||p.text!==undefined&&typeof p.text!=='string')))))return fail()}
 if(!object(value.context)||!text(value.context.scope,100)||!Array.isArray(value.context.entries)||value.context.entries.length>800||new Set(value.context.entries.map(e=>e.id)).size!==value.context.entries.length)return fail()
 for(const e of value.context.entries){if(!object(e)||!text(e.id,300)||!e.id.startsWith(value.context.scope+':')||!['history','artifact','request','tool'].includes(String(e.kind))||!text(e.label,1000)||typeof e.digest!=='string'||!/^[a-f0-9]{64}$/.test(e.digest)||e.revision!==undefined&&!text(e.revision,256)||!Number.isInteger(e.bytes)||Number(e.bytes)<0||!Object.hasOwn(e,'value'))return fail()}
 const result=structuredClone(value) as unknown as ExecutionCheckpoint
 if(result.completions)result.completions=result.completions.map(({request,phase,result,attempt,failure,waitMs})=>({request,phase,result:readCompletion(result)!,...(attempt===undefined?{}:{attempt}),...(failure===undefined?{}:{failure:readFailure(failure)!}),...(waitMs===undefined?{}:{waitMs})}))
 return result
}
/** A stopped API request can fail exactly at the ceiling; recovery still needs an explicit grant. */
export function canExtendInterruptedExecution(saved:ExecutionCheckpoint|undefined):boolean {
 return !!saved&&saved.unit==='model-requests'&&saved.status==='stopped'&&saved.used===saved.limit&&saved.limit<MAX_REQUEST_ALLOWANCE
}
/** Explicit UI action only. Increasing compute does not alter tool/action consent. */
export function extendExecution(checkpoint:ExecutionCheckpoint,interrupted=false):ExecutionCheckpoint {
 const saved=readExecution(checkpoint)!
 if((saved.status!=='paused'&&!(interrupted&&canExtendInterruptedExecution(saved)))||saved.unit!=='model-requests'||saved.limit>=MAX_REQUEST_ALLOWANCE)throw Error('This execution cannot receive another request allowance.')
 const limit=Math.min(MAX_REQUEST_ALLOWANCE,saved.limit+REQUEST_INCREMENT)
 return {...saved,limit,status:'running',reason:undefined,grants:[...saved.grants,{from:saved.limit,to:limit,at:new Date().toISOString()}]}
}
export class ExecutionPaused extends Error {readonly failureCode='request-limit';constructor(){super('Work paused at the approved request limit. Continue with an additional allowance, or keep the completed work.')}}
