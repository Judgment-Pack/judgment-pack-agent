import {readCompletion,type ModelCompletion} from './completion'
import catalog from './contract/catalog.json'
import type {AssistantEvent} from './engine'
export type FailureCode=keyof typeof catalog.failures
export type FailureSource='model'|'agent'|'tool'|'context'|'storage'|'desk'
export interface AIFailure {version:1;code:FailureCode;source:FailureSource;outcome:'not-started'|'refused'|'unknown';status?:number;requestId?:string;retryAt?:string;detail?:string;completion?:ModelCompletion}
const object=(v:unknown):v is Record<string,unknown>=>!!v&&typeof v==='object'&&!Array.isArray(v)
const code=(v:unknown):v is FailureCode=>typeof v==='string'&&Object.hasOwn(catalog.failures,v)
const sources=['model','agent','tool','context','storage','desk']
export function readFailure(value:unknown):AIFailure|undefined {
 if(!object(value)||value.version!==1||!code(value.code)||!sources.includes(String(value.source))||!['not-started','refused','unknown'].includes(String(value.outcome)))return
 if(value.status!==undefined&&(!Number.isInteger(value.status)||Number(value.status)<100||Number(value.status)>599))return
 if(value.requestId!==undefined&&(typeof value.requestId!=='string'||!/^[-a-zA-Z0-9_]{1,128}$/.test(value.requestId)))return
 if(value.detail!==undefined&&(typeof value.detail!=='string'||value.detail.length>1000))return
 if(value.retryAt!==undefined&&(typeof value.retryAt!=='string'||value.retryAt.length>64||!Number.isFinite(Date.parse(value.retryAt))))return
 if(value.completion!==undefined&&!readCompletion(value.completion))return
 return {version:1,code:value.code,...(value.completion===undefined?{}:{completion:readCompletion(value.completion)}),source:value.source as FailureSource,outcome:value.outcome as AIFailure['outcome'],...(value.detail===undefined?{}:{detail:String(value.detail)}),...(value.status===undefined?{}:{status:Number(value.status)}),...(value.requestId===undefined?{}:{requestId:String(value.requestId)}),...(value.retryAt===undefined?{}:{retryAt:String(value.retryAt)})}
}
export const failureMessage=(failure:AIFailure)=>failure.detail??catalog.failures[failure.code].message
export const failureRecovery=(failure:AIFailure)=>catalog.failures[failure.code].recovery
export class AIFailureError extends Error {
 readonly failure:AIFailure
 constructor(failure:AIFailure){const safe=readFailure(failure);if(!safe)throw Error('Invalid AI failure');super(failureMessage(safe));this.name='AIFailureError';this.failure=safe}
}
export function failure(code:FailureCode,source:FailureSource='desk',outcome:AIFailure['outcome']='unknown'):AIFailure {return {version:1,code,source,outcome}}
export const failureError=(code:FailureCode,source:FailureSource='desk',outcome:AIFailure['outcome']='unknown')=>new AIFailureError(failure(code,source,outcome))
const alias=(v:unknown):FailureCode|undefined=>typeof v==='string'&&Object.hasOwn(catalog.aliases,v)?(catalog.aliases as Record<string,FailureCode>)[v]:undefined
function metadata(headers:unknown,now:number):Pick<AIFailure,'requestId'|'retryAt'> {
 const get=(key:string):unknown=>headers instanceof Headers?headers.get(key):object(headers)?Object.entries(headers).find(([name])=>name.toLowerCase()===key)?.[1]:undefined
 const id=get('request-id')??get('x-request-id');const after=get('retry-after')
 const delay=typeof after==='string'?( /^\d+(?:\.\d+)?$/.test(after)?Number(after)*1000:Date.parse(after)-now):NaN
 return {...(typeof id==='string'&&/^[-a-zA-Z0-9_]{1,128}$/.test(id)?{requestId:id}:{}),...(Number.isFinite(delay)&&delay>=0?{retryAt:new Date(now+Math.min(delay,86400000)).toISOString()}: {})}
}
/** Typed SDK/Desk codes and status only. Never retain provider prose, bodies, URLs, headers or stacks. */
export function normalizeFailure(cause:unknown,source:FailureSource='desk',now=Date.now()):AIFailure {
 const seen=new Set<unknown>()
 const classify=(value:unknown,depth:number):AIFailure=>{
  if(!object(value)||depth>4||seen.has(value))return failure('unknown',source)
  seen.add(value)
  const held=value instanceof AIFailureError?readFailure(value.failure):undefined;if(held)return held
  if(value.name==='AbortError'||value.name==='RunCancelled')return failure('cancelled',source)
  if(value.name==='NoSession')return failure('authentication','desk','not-started')
  const own=value instanceof Error&&code(value.failureCode)?value.failureCode:alias(value.code)??alias(value.type)??alias(value.status)
  let body:Record<string,unknown>|undefined
  if(typeof value.responseBody==='string'&&value.responseBody.length<=16384){try{const parsed:unknown=JSON.parse(value.responseBody);if(object(parsed))body=parsed}catch{/* Body is never a user diagnostic. */}}
  const upstream=object(body?.error)?body.error:body
  const fromBody=alias(upstream?.code)??alias(upstream?.type)??alias(body?.code)
  const status=[value.statusCode,value.status,typeof value.code==='number'?value.code:undefined].map(Number).find(n=>Number.isInteger(n)&&n>=100&&n<=599)??NaN
  const classified=own??fromBody??(status===401?'authentication':status===403?'permission':status===429?'rate-limited':status===408||status===504?'timeout':status===413?'context-limit':status===409?'stale-state':status>=500&&status<=599?'unavailable':status>=400&&status<=499?'invalid-request':undefined)
  if(classified){
   const outcome=value.name==='AI_StreamProviderError'?'unknown':classified==='busy'?'not-started':Number.isInteger(status)&&status>=400&&status<500&&status!==408?'refused':'unknown'
   return {...failure(classified,source,outcome),...(Number.isInteger(status)&&status>=100&&status<=599?{status}:{}),...metadata(value.responseHeaders,now)}
  }
  if(['AI_NoOutputGeneratedError','AI_NoContentGeneratedError','AI_EmptyResponseBodyError','AI_InvalidResponseDataError','AI_JSONParseError'].includes(String(value.name)))return failure('invalid-response',source)
  if(value.name==='TimeoutError')return failure('timeout',source)
  for(const nested of [value.lastError,value.cause,value.error])if(object(nested)){const found=classify(nested,depth+1);if(found.code!=='unknown')return found}
  return failure('unknown',source)
 }
 return classify(cause,0)
}
export function failureEvent(cause:unknown,source:FailureSource='desk'):Extract<AssistantEvent,{type:'error'}>{const value=normalizeFailure(cause,source);return {type:'error',message:failureMessage(value),failure:value}}
/** Automatic retry requires positive admission refusal; never infer replay safety from a 5xx or a timeout. */
export function canRetryAdmission(value:AIFailure,attempt:number,maxAttempts:number):boolean {return value.code==='busy'&&value.outcome==='not-started'&&attempt<maxAttempts}
/**
 * The refusals a model request is sent again after, by name (agent#2): the rate-limited class (a 429, or one of the
 * catalogue's rate-limit aliases: rate_limit_exceeded, rate_limit_error, RESOURCE_EXHAUSTED, rate-limited), and the
 * catalogue's overload alias, overloaded_error, on a 4xx. Nothing else: not a class whose recovery merely says to
 * retry (a 4xx carrying search-invalid-response is invalid-response, and is not sent again).
 */
export const RETRYABLE_REFUSAL_CLASSES:readonly FailureCode[]=Object.freeze(['rate-limited'])
export const RETRYABLE_OVERLOAD_CODES:readonly string[]=Object.freeze(['overloaded_error'])
/**
 * The code a refusal's body names that the catalogue aliases, read in the order `normalizeFailure` reads it
 * (`error.code`, `error.type`, then the top-level `code`), or nothing. Only a catalogue alias is returned, never
 * provider prose.
 */
export function refusalCode(responseBody:unknown):string|undefined {
 if(typeof responseBody!=='string'||responseBody.length>16384)return
 let body:unknown;try{body=JSON.parse(responseBody)}catch{return}
 if(!object(body))return
 const upstream=object(body.error)?body.error:body
 return [upstream.code,upstream.type,body.code].find((value):value is string=>alias(value)!==undefined)
}
/**
 * A model request the provider refused may be sent again: the outcome is `refused` (a 4xx other than 408, which did
 * nothing and was not charged), and the refusal is one of those named above. Never an `unknown` outcome (a 5xx, 408,
 * 504 or transport failure may have run), whatever its code.
 */
export function canRetryRefusal(value:AIFailure,code?:string):boolean {
 if(value.outcome!=='refused')return false
 return RETRYABLE_REFUSAL_CLASSES.includes(value.code)||code!==undefined&&RETRYABLE_OVERLOAD_CODES.includes(code)&&alias(code)===value.code
}
export function retryReady(value:AIFailure|undefined,now=Date.now()):boolean {return !value?.retryAt||Date.parse(value.retryAt)<=now}

/** Host-authored validation only; never pass provider prose or arbitrary caught messages. */
export function authoredFailure(code:FailureCode,detail:string,source:FailureSource='desk'):AIFailureError{return new AIFailureError({...failure(code,source),detail:detail.slice(0,1000)})}

/** A failed tool result is recoverable working data, not a failed assistant turn. */
export function toolFailureResult(cause:unknown){
 const classified=normalizeFailure(cause,'tool')
 const safe=classified.code==='unknown'?failure('tool-failed','tool'):classified
 return {isError:true as const,content:[{type:'text' as const,text:failureMessage(safe)}],structuredContent:{failure:safe}}
}

/** A host validation failure is actionable model feedback, not provider prose. */
export const toolInputFailure=(detail:string)=>authoredFailure('invalid-tool-input',detail,'tool')
