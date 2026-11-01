/** Internal connector provenance. This module is excluded from package exports. */
import type {McpTool, McpToolResult} from '../engine'
import type {RuntimeActionPort,RuntimeApprovalBinding,RuntimeApprovalRequest} from '../runtimeActions'
import {validRuntimeOwner} from '../runtimeActions'
import type {SourceRecord} from '../toolSources'
import {canonicalJSON} from '../context/chat'
import {sha256} from '../records'
import {authoredFailure} from '../failures'

type Call=(name:string,args:Record<string,unknown>)=>Promise<McpToolResult>
interface Connector {
 port:RuntimeActionPort
 initialTools:McpTool[]
 list:(signal:AbortSignal)=>Promise<McpTool[]>
 prepare:(name:string,args:Record<string,unknown>)=>Record<string,unknown>
 dispatch:Call
 open:()=>boolean
}
const connectors=new WeakMap<Call,Connector>()
const attempts=new WeakMap<object,{connector:Connector;name:string;args:Record<string,unknown>;source:SourceRecord;runtime:RuntimeApprovalBinding;offer:string;scope:string;claimed:boolean;used:boolean}>()
const fail=()=>authoredFailure('permission','Runtime action approval is missing or stale. Nothing was sent.','tool')
const digest=(value:unknown)=>'sha256:'+sha256(canonicalJSON(value))
/** A detached JSON value, rejecting unrepresentable or ambiguous approval values. */
export function runtimeJSON<T>(value:T):T {
 const text=JSON.stringify(value,(_key,v)=>{if(typeof v==='number'&&(!Number.isFinite(v)||Number.isInteger(v)&&!Number.isSafeInteger(v)))throw fail();return v})
 if(text===undefined)throw fail()
 const parsed=JSON.parse(text)
 const visit=(v:unknown):void=>{
  if(typeof v==='number'&&(!Number.isFinite(v)||Number.isInteger(v)&&!Number.isSafeInteger(v)))throw fail()
  if(v&&typeof v==='object')for(const item of Object.values(v))visit(item)
 }
 visit(parsed);return parsed
}
function projection(tools:McpTool[]){return tools.map(t=>({name:t.name,...(t.description===undefined?{}:{description:t.description}),inputSchema:t.inputSchema,taxonomy:t._meta}))}
/** Called only by the actual connection owner, never by an engine adapter. */
export function registerRuntimeConnector(call:Call,connector:Connector):void {
 if(connectors.has(call)||!validRuntimeOwner(connector.port.owner))throw fail()
 // Retain the observation function, and detach host-owned mutable declaration data.
 connectors.set(call,{...connector,initialTools:runtimeJSON(connector.initialTools),port:{...connector.port,owner:runtimeJSON(connector.port.owner)}})
}
export interface RuntimeAttempt {
 readonly request:RuntimeApprovalRequest
 /** The exact request descriptor passed to the original durable writer. */
 readonly descriptor:string
}
export interface RuntimeAdmission {execution:string;turn:number;role:string;task?:string;phase:string;effectivePermissions:unknown;finalRuntimeOffer:unknown}
export async function prepareRuntimeAttempt(call:Call,name:string,args:Record<string,unknown>,admission:RuntimeAdmission,signal:AbortSignal,restored?:RuntimeApprovalBinding):Promise<RuntimeAttempt> {
 const connector=connectors.get(call)
 if(!connector||!connector.open())throw fail()
 signal.throwIfAborted()
 const owner=runtimeJSON(await connector.port.current(signal))
 if(!validRuntimeOwner(owner)||canonicalJSON(owner)!==canonicalJSON(connector.port.owner))throw fail()
 const listed=runtimeJSON(await connector.list(signal)),offer=canonicalJSON(projection(listed))
 if(offer!==canonicalJSON(projection(connector.initialTools)))throw fail()
 const tool=listed.find(t=>t.name===name)
 if(!tool||tool._meta?.effect!=='act')throw fail()
 const admitted=runtimeJSON(admission.finalRuntimeOffer)
 if(!Array.isArray(admitted)||!admitted.some(t=>t?.name===name)||new Set(admitted.map(t=>t?.name)).size!==admitted.length||admitted.some(t=>!projection(listed).some(actual=>canonicalJSON(actual)===canonicalJSON(t))))throw fail()

 const clean=runtimeJSON(connector.prepare(name,runtimeJSON(args)))
 const identity={kind:'connection' as const,label:owner.principalLabel}
 const source:SourceRecord={kind:'runtime',id:'runtime',trust:'first-party',tool:name,toolListDigest:digest(projection(listed)),identity,identityDigest:digest(identity)}
 const scope=canonicalJSON(runtimeJSON({...admission,task:admission.task??null}))
 const runtime:RuntimeApprovalBinding={owner,admissionDigest:digest({version:1,owner,source,...JSON.parse(scope)})}
 if(restored&&canonicalJSON(restored)!==canonicalJSON(runtime))throw fail()
 signal.throwIfAborted()
 const request=runtimeJSON({name,tool:name,args:clean,source,runtime})
 const attempt=Object.freeze({request:deepFreeze(request),descriptor:canonicalJSON({version:1,name,args:clean,source,runtime})})
 attempts.set(attempt,{connector,name,args:clean,source,runtime,offer,scope,claimed:false,used:false})
 return attempt
}
function deepFreeze<T>(value:T):T {if(value&&typeof value==='object'){for(const v of Object.values(value))deepFreeze(v);Object.freeze(value)}return value}
export function runtimePolicy(attempt:RuntimeAttempt):'ask'|'allow'|'refuse' {
 const held=attempts.get(attempt)
 if(!held||held.used)throw fail()
 try {const answer=held.connector.port.approve===undefined?'ask':held.connector.port.approve(runtimeJSON(attempt.request));return ['ask','allow','refuse'].includes(answer)?answer:'refuse'}catch{return 'refuse'}
}
/** Reserve one Execute operation before its durable claim; copies share this state. */
export function claimRuntimeAttempt(attempt:RuntimeAttempt):void {
 const held=attempts.get(attempt)
 if(!held||held.claimed||held.used)throw fail()
 held.claimed=true
}
/** Rechecks the complete connector immediately before one physical call. No caller chooses args. */
export async function executeRuntimeAttempt(attempt:RuntimeAttempt,admission:()=>RuntimeAdmission,signal:AbortSignal,authorize:()=>Promise<void>,beforeDispatch?:()=>Promise<void>):Promise<McpToolResult> {
 const held=attempts.get(attempt)
 if(!held||!held.claimed||held.used||held.scope!==canonicalJSON(runtimeJSON({...admission(),task:admission().task??null})))throw fail()
 await authorize();signal.throwIfAborted()
 const c=held.connector
 if(!c.open())throw fail()
 const owner=runtimeJSON(await c.port.current(signal))
 const listed=runtimeJSON(await c.list(signal))
 if(canonicalJSON(owner)!==canonicalJSON(held.runtime.owner)||canonicalJSON(projection(listed))!==held.offer||canonicalJSON(c.prepare(held.name,runtimeJSON(held.args)))!==canonicalJSON(held.args))throw fail()
 await authorize();signal.throwIfAborted()
 await beforeDispatch?.();signal.throwIfAborted()
 if(held.used||!c.open()||held.scope!==canonicalJSON(runtimeJSON({...admission(),task:admission().task??null})))throw fail()
 // Synchronous one-use consumption. No mutable caller value is read below this point.
 held.used=true
 return c.dispatch(held.name,runtimeJSON(held.args))
}
