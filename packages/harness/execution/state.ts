import { validMemories, type Memories } from '../memories'
import {validSubagentRecord} from '../subagents'
import {readCompletion,type ModelCompletion} from '../completion'
import {failure,readFailure,type AIFailure} from '../failures'
import type {McpToolResult} from '../engine'
import {MAX_TURNS} from '../engines/contract'
import {utf8Bytes} from '../context/policy'
import {validCriticRecord,type CriticRecord} from '../criticSwitch'

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
/**
 * The digest of one request body the adapter sent (ADR-0005): which it was, counted from 1 in the
 * order sent (each model request, a retried attempt its own; on the agent wire each agent run), and
 * the SHA-256 of its text, lower-case hex. The contract's `provenance.json#/$defs/promptDigest`.
 */
export interface PromptDigest {request:number;sha256:string}
/** The most prompt digests a checkpoint keeps, the newest: three attempts at each request of the largest allowance. */
export const MAX_PROMPT_DIGESTS = 300
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
 /** ADR-0005: the digest of every request body the adapter sent, in order, carried as `used` is. */
 prompts?:PromptDigest[];
 /** ADR-0006: the run report's rows by source, the Runtime's first, carried as `used` is. */
 sources?:SourceFigures[];
 /** ADR-0008: the act call the run paused at for the person's decision, and what became of it. */
 approval?:ApprovalRecord;
 /** ADR-0009: the skills loaded, in load order, carried through a resume (`skills.ts`, SkillGate). */
 skills?:SkillLoad[];
 memories?:Memories;
 /**
  * ADR-0010: the critic's decision (`criticSwitch.ts`): written when the run decides, and before that,
  * under auto, when the model's request_review is taken; carried through a resume.
  */
 policy?:import('../policy').PolicyMembers;
 crossProvider?:import('../policy').CrossProvider[];
 critic?:CriticRecord;
}
/** One loaded skill as the checkpoint keeps it (ADR-0009; `provenance.json#/$defs/skillLoad`). */
export interface SkillLoad {id:string;digest:string;bytes:number;loadedAt:string}
/** The checkpoint's loaded skills, read closed (ADR-0009). */
export function validSkillLoads(value:unknown):value is SkillLoad[]{
 return Array.isArray(value)&&value.length<=64&&new Set(value.map(load=>object(load)?load.id:undefined)).size===value.length&&value.every(load=>object(load)&&exact(load,['id','digest','bytes','loadedAt'])&&typeof load.id==='string'&&/^[A-Za-z][A-Za-z0-9-]{0,23}$/.test(load.id)&&typeof load.digest==='string'&&/^sha256:[0-9a-f]{64}$/.test(load.digest)&&count(load.bytes)&&typeof load.loadedAt==='string'&&Number.isFinite(Date.parse(load.loadedAt)))
}
/**
 * One act call paused for the person's decision (ADR-0008; `checkpoint.json#/$defs/approval`): the
 * call as the model made it, and its state. `asked`: pending, the status paused; `approved`, written
 * and acknowledged before the call runs; `refused`, with the refused result; `executed`, with the
 * result, written and acknowledged before anything else; `answered` once the continuation request
 * carrying the result has been sent.
 */
export interface ApprovalRecord {callId:string;name:string;tool:string;source:import('../toolSources').SourceRecord;args:Record<string,unknown>;state:'asked'|'approved'|'refused'|'executed'|'answered';decidedAt?:string;note?:string;result?:McpToolResult;marker?:string}
/** The person's decision on a paused act call (ADR-0008; `checkpoint.json#/$defs/approvalDecision`), handed to the run as `context.approval`. */
export interface ApprovalDecision {callId:string;approved:boolean;note?:string}
const APPROVAL_STATES=['asked','approved','refused','executed','answered']
const WIRE_NAME=/^[A-Za-z0-9_-]{1,64}$/
/** A source record as an approval carries it, read closed (`tool-offer.json#/$defs/sourceRecord`). */
function validRecord(v:unknown):boolean{
 return object(v)&&exact(v,['kind','id','trust','version','toolListDigest','tool','receipt'])&&['runtime','gateway','mcp','custom','exchange'].includes(String(v.kind))&&typeof v.id==='string'&&/^[A-Za-z][A-Za-z0-9-]{0,23}$/.test(v.id)&&['first-party','connected','public','person'].includes(String(v.trust))&&(v.version===undefined||typeof v.version==='string'&&v.version.length>=1&&v.version.length<=64)&&(v.toolListDigest===undefined||typeof v.toolListDigest==='string'&&/^sha256:[0-9a-f]{64}$/.test(v.toolListDigest))&&(v.tool===undefined||text(v.tool,128))&&(v.receipt===undefined||text(v.receipt,128))
}
/** The checkpoint's approval, read closed (ADR-0008). */
export function validApproval(v:unknown):v is ApprovalRecord{
 return object(v)&&exact(v,['callId','name','tool','source','args','state','decidedAt','note','result','marker'])&&text(v.callId,256)&&v.callId.length>=1&&typeof v.name==='string'&&WIRE_NAME.test(v.name)&&text(v.tool,128)&&v.tool.length>=1&&validRecord(v.source)&&object(v.args)&&APPROVAL_STATES.includes(String(v.state))&&(v.decidedAt===undefined||typeof v.decidedAt==='string'&&Number.isFinite(Date.parse(v.decidedAt)))&&(v.note===undefined||text(v.note,2000))&&(v.result===undefined||object(v.result))&&(v.marker===undefined||typeof v.marker==='string'&&/^[0-9a-f]{32}$/.test(v.marker))
}
/** The person's decision, read closed (ADR-0008), or undefined where it is not one. */
export function readApprovalDecision(v:unknown):ApprovalDecision|undefined{
 if(!object(v)||!exact(v,['callId','approved','note'])||!text(v.callId,256)||v.callId.length<1||typeof v.approved!=='boolean'||v.note!==undefined&&!text(v.note,2000))return
 return {callId:v.callId,approved:v.approved,...(v.note===undefined?{}:{note:v.note as string})}
}
/** The refused result an act call the person refused carries (ADR-0008): never run. */
export function refusedResult(name:string):McpToolResult{
 const held={...failure('permission','tool','not-started'),detail:`The person refused this call of ${name}; it was not run and nothing was changed.`}
 return {isError:true,content:[{type:'text',text:held.detail}],structuredContent:{failure:held}}
}
/** What an approved call restored before its result was written carries (ADR-0008): it may have run, and it is not run again. */
export function unknownOutcome(name:string):McpToolResult{
 const held={...failure('unknown','tool','unknown'),detail:`This call of ${name} was approved, and the run stopped before its result was written, so it may have run. It was not run again: check its outcome before calling it again.`}
 return {isError:true,content:[{type:'text',text:held.detail}],structuredContent:{failure:held}}
}
/**
 * One source's row of the run report (ADR-0006, `toolSources.ts`): the contract's
 * `provenance.json#/$defs/sourceFigures`. The Runtime's row is source `runtime`, kind `runtime`.
 */
export interface SourceFigures {source:string;kind:'runtime'|'gateway'|'mcp'|'custom'|'exchange';calls:number;refused:number;proposed:number;bytes:number;milliseconds:number;/** ADR-0009: the source's skills the model loaded, each load counted; on a source that declares a skill. */skills?:number}
/** The run report's rows, read closed (`checkpoint.json`, sources). */
export function validSourceFigures(value:unknown):value is SourceFigures[]{
 return Array.isArray(value)&&value.length<=64&&value.every(row=>object(row)&&exact(row,['source','kind','calls','refused','proposed','bytes','milliseconds','skills'])&&typeof row.source==='string'&&/^[A-Za-z][A-Za-z0-9-]{0,23}$/.test(row.source)&&['runtime','gateway','mcp','custom','exchange'].includes(String(row.kind))&&count(row.calls)&&count(row.refused)&&count(row.proposed)&&count(row.bytes)&&count(row.milliseconds)&&(row.skills===undefined||count(row.skills)))
}
/** Token counts the provider reported, summed; a member is absent where no request reported it (agent#18). */
export interface TokenFigures {inputTokens?:number;outputTokens?:number;reasoningTokens?:number;cacheReadTokens?:number;cacheWriteTokens?:number}
const TOKEN_KEYS=['inputTokens','outputTokens','reasoningTokens','cacheReadTokens','cacheWriteTokens'] as const
/**
 * One delegated child's figures (agent#18): its model, its model requests (none on an agent capability,
 * where the provider runs its own loop; agentRuns counts its runs there), its tool calls, the provider's
 * token counts where reported, the time to its first answer byte and its whole duration.
 */
export interface ChildFigures {problem?:string;subagent?:import('../subagents').SubagentRecord;task:string;batch:number;profile:string;model?:string;state:'queued'|'running'|'complete'|'failed'|'interrupted';modelRequests?:number;toolCalls:number;agentRuns?:number;tokens?:TokenFigures;firstByteMs?:number;durationMs:number;partial?:true;reused?:true}
/** A batch's sums, and its wall time from its first child's start to its last child's end. modelRequests counts model requests only: absent on an agent capability (agent#71). */
export interface FiguresTotal {modelRequests?:number;toolCalls:number;agentRuns?:number;tokens?:TokenFigures;durationMs:number}
export interface BatchFigures {batch:number;children:ChildFigures[];total:FiguresTotal}
/** The parent's own: its model, its own model requests (the response's less its children's; none on an agent capability), and its own token counts. */
export interface ParentFigures {model:string;modelRequests?:number;agentRuns?:number;tokens?:TokenFigures}
export interface DelegationFigures {batches:BatchFigures[];parent:ParentFigures}
const count=(v:unknown,max=Number.MAX_SAFE_INTEGER)=>Number.isSafeInteger(v)&&Number(v)>=0&&Number(v)<=max
const exact=(v:Record<string,unknown>,names:readonly string[])=>Object.keys(v).every(k=>names.includes(k))
function validTokens(v:unknown):boolean{return object(v)&&exact(v,TOKEN_KEYS)&&TOKEN_KEYS.every(k=>v[k]===undefined||count(v[k]))}
/**
 * The checkpoint's delegation figures, read closed (agent#18). `unit` is the checkpoint's: where it counts
 * model requests each figure carries modelRequests; where the provider manages them (an agent capability)
 * the member is optional and the agent capability's figures omit it (agent#71).
 */
export function validDelegationFigures(v:unknown,unit:'model-requests'|'provider-managed'='model-requests'):v is DelegationFigures {
 const DAY=86400000
 const requests=(x:Record<string,unknown>,max=Number.MAX_SAFE_INTEGER)=>x.modelRequests===undefined?unit==='provider-managed':count(x.modelRequests,max)
 const tokens=(x:Record<string,unknown>)=>x.tokens===undefined||validTokens(x.tokens)
 const child=(c:unknown)=>object(c)&&exact(c,['task','batch','profile','model','state','modelRequests','toolCalls','agentRuns','tokens','firstByteMs','durationMs','partial','reused','subagent','problem'])&&(c.subagent===undefined||validSubagentRecord(c.subagent))&&text(c.task,36)&&/^[a-f0-9-]{36}$/.test(c.task)&&count(c.batch,100)&&Number(c.batch)>=1&&text(c.profile,48)&&(c.model===undefined||text(c.model,200))&&['queued','running','complete','failed','interrupted'].includes(String(c.state))&&requests(c,100)&&count(c.toolCalls,100)&&(c.agentRuns===undefined||count(c.agentRuns,100))&&tokens(c)&&(c.firstByteMs===undefined||count(c.firstByteMs,DAY))&&count(c.durationMs,DAY)&&(c.partial===undefined||c.partial===true)&&(c.reused===undefined||c.reused===true)
 const total=(t:unknown)=>object(t)&&exact(t,['modelRequests','toolCalls','agentRuns','tokens','durationMs'])&&requests(t)&&count(t.toolCalls)&&(t.agentRuns===undefined||count(t.agentRuns))&&tokens(t)&&count(t.durationMs,DAY)
 const batch=(b:unknown)=>object(b)&&exact(b,['batch','children','total'])&&count(b.batch,100)&&Number(b.batch)>=1&&Array.isArray(b.children)&&b.children.length>=1&&b.children.length<=3&&b.children.every(child)&&total(b.total)
 const parent=(p:unknown)=>object(p)&&exact(p,['model','modelRequests','agentRuns','tokens'])&&text(p.model,200)&&requests(p)&&(p.agentRuns===undefined||count(p.agentRuns))&&tokens(p)
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
 if(value.limit!==limit||!Array.isArray(value.plan)||(value.plan.length&&!validTaskSteps(value.plan))||!text(value.explanation,2000)||(value.reason!==undefined&&!text(value.reason,2000))||(value.testCalls!==undefined&&(!Number.isInteger(value.testCalls)||Number(value.testCalls)<0||Number(value.testCalls)>400))||(value.delegation!==undefined&&!validDelegationFigures(value.delegation,value.unit as 'model-requests'|'provider-managed')))return fail()
 if(value.completions!==undefined&&(!Array.isArray(value.completions)||value.completions.length>MAX_REQUEST_ALLOWANCE||value.completions.some(row=>!object(row)||!Number.isInteger(row.request)||Number(row.request)<1||Number(row.request)>Number(value.used)||!['work','synthesis','review'].includes(String(row.phase))||!readCompletion(row.result)||row.attempt!==undefined&&(!Number.isInteger(row.attempt)||Number(row.attempt)<1||Number(row.attempt)>3)||row.failure!==undefined&&!readFailure(row.failure)||row.waitMs!==undefined&&(!Number.isInteger(row.waitMs)||Number(row.waitMs)<0||Number(row.waitMs)>86400000))))return fail()
 if(value.prompts!==undefined&&(!Array.isArray(value.prompts)||value.prompts.length>MAX_PROMPT_DIGESTS||value.prompts.some((row,at,rows)=>!object(row)||!exact(row,['request','sha256'])||!Number.isSafeInteger(row.request)||Number(row.request)<1||at>0&&Number(row.request)<=Number((rows[at-1] as Record<string,unknown>).request)||typeof row.sha256!=='string'||!/^[a-f0-9]{64}$/.test(row.sha256))))return fail()
 if(value.sources!==undefined&&!validSourceFigures(value.sources))return fail()
 if(value.approval!==undefined&&!validApproval(value.approval))return fail()
 if(value.memories!==undefined&&!validMemories(value.memories))return fail()
 if(value.skills!==undefined&&!validSkillLoads(value.skills))return fail()
 if(value.critic!==undefined&&!validCriticRecord(value.critic))return fail()
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
