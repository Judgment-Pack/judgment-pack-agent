import {canonicalJSON} from './context/chat'
import type {AssistantEvent,ModelRequest,McpToolResult} from './engine'

export const CORPUS_FILES={requests:'requests.json',toolResults:'tool-results.json',events:'events.json',checkpoint:'checkpoint.json',expected:'expected.json',manifest:'manifest.json'} as const
export type CorpusFileName=typeof CORPUS_FILES[keyof typeof CORPUS_FILES]
export interface CorpusManifest {id:string;packageVersion:string;policy:Record<string,unknown>;files:{requests:'requests.json';toolResults:'tool-results.json';events:'events.json';checkpoint:'checkpoint.json'};expected:{file:'expected.json'};tolerance:string[]}
export interface RecordedRequest {suffix:string;request:unknown;response:{status:number;headers:Record<string,string>;body:string}}
export interface RecordedToolResult {name:string;args:Record<string,unknown>;result:McpToolResult}
export interface CorpusExpected {gateDecisions:unknown[];outcomes:unknown[];proposalDigest?:string;critic?:{verdict:unknown;reasons:unknown};refusals:unknown[];timings?:unknown;drawnMarkers?:unknown}
export interface CorpusEntry {manifest:CorpusManifest;requests:RecordedRequest[];toolResults:RecordedToolResult[];events:AssistantEvent[];checkpoint:unknown;expected:CorpusExpected}
export type CorpusFiles=Record<CorpusFileName,string>
export interface CorpusSink {write(id:string,files:CorpusFiles):Promise<void>|void}
export interface CorpusRedactor {value<T>(input:T,where:'message',record:true):T}

const bytes=(text:string)=>new TextEncoder().encode(text).length
const clone=<T>(value:T):T=>structuredClone(value)
const digestFromProposal=(event:Record<string,unknown>)=>typeof event.digest==='string'?event.digest:undefined

/** Projection compared by replay. It deliberately excludes message, message_progress, reasoning and critique text. */
export function expectedFromEvents(events:AssistantEvent[]):CorpusExpected{
 const gateDecisions:unknown[]=[],outcomes:unknown[]=[],refusals:unknown[]=[]
 let proposalDigest:string|undefined,critic:CorpusExpected['critic']
 for(const event of events){
  if(event.type==='guardrail'){
   const row={tool:event.tool,action:event.action,...(event.reason===undefined?{}:{reason:event.reason})}
   gateDecisions.push(row);if(event.action==='refused')refusals.push(row)
  }else if(event.type==='tool_result')outcomes.push({type:event.type,name:event.name,isError:event.isError})
  else if(event.type==='error')outcomes.push({type:event.type,code:event.failure?.code??null,outcome:event.failure?.outcome??null})
  else if(event.type==='proposal'){
   proposalDigest=digestFromProposal(event as unknown as Record<string,unknown>)
   const p=event as unknown as Record<string,unknown>,held=p.critique
   if(held&&typeof held==='object'){const c=held as Record<string,unknown>;critic={verdict:c.refuted,reasons:clone(c.checks??[])}}
   outcomes.push({type:event.type})
  }
 }
 return {gateDecisions,outcomes,...(proposalDigest===undefined?{}:{proposalDigest}),...(critic===undefined?{}:{critic}),refusals}
}

export function serializeCorpus(entry:CorpusEntry):CorpusFiles{
 const values:Record<CorpusFileName,unknown>={
  [CORPUS_FILES.manifest]:entry.manifest,[CORPUS_FILES.requests]:entry.requests,
  [CORPUS_FILES.toolResults]:entry.toolResults,[CORPUS_FILES.events]:entry.events,
  [CORPUS_FILES.checkpoint]:entry.checkpoint,[CORPUS_FILES.expected]:entry.expected
 }
 return Object.fromEntries(Object.entries(values).map(([name,value])=>[name,canonicalJSON(value)])) as CorpusFiles
}

/** Write a fully redacted entry. A redaction failure writes nothing and returns the required refusal. */
export async function writeCorpusEntry(input:Omit<CorpusEntry,'expected'>,guard:CorpusRedactor,sink:CorpusSink):Promise<{event:{type:'recorded';id:string;bytes:number}}|{event:{type:'guardrail';tool:'record';action:'refused';reason:'record refused';detail:string}}>{
 try{
  const raw={...clone(input),expected:expectedFromEvents(input.events)}
  const clean=guard.value(raw,'message',true)
  const files=serializeCorpus(clean)
  const total=Object.values(files).reduce((sum,text)=>sum+bytes(text),0)
  await sink.write(clean.manifest.id,files)
  return {event:{type:'recorded',id:clean.manifest.id,bytes:total}}
 }catch{
  return {event:{type:'guardrail',tool:'record',action:'refused',reason:'record refused',detail:'The output guard could not redact the corpus entry; no recording was written.'}}
 }
}

const pointerParts=(pointer:string)=>pointer.slice(1).split('/').map(part=>part.replace(/~1/g,'/').replace(/~0/g,'~'))
function omitPointer(value:unknown,pointer:string):void{
 if(!pointer.startsWith('/'))return
 const parts=pointerParts(pointer);let at=value as Record<string,unknown>|unknown[]
 for(let i=0;i<parts.length-1;i++){if(at===null||typeof at!=='object')return;at=(at as Record<string,unknown>)[parts[i]!] as typeof at}
 if(at!==null&&typeof at==='object')delete (at as Record<string,unknown>)[parts.at(-1)!]
}
export interface CorpusDifference {member:string;expected:unknown;actual:unknown}
function firstDifference(expected:unknown,actual:unknown,path=''):CorpusDifference|undefined{
 if(Object.is(expected,actual))return
 if(Array.isArray(expected)&&Array.isArray(actual)){
  if(expected.length!==actual.length)return {member:`${path}/length`,expected:expected.length,actual:actual.length}
  for(let i=0;i<expected.length;i++){const found=firstDifference(expected[i],actual[i],`${path}/${i}`);if(found)return found}return
 }
 if(expected&&actual&&typeof expected==='object'&&typeof actual==='object'){
  const keys=[...new Set([...Object.keys(expected),...Object.keys(actual)])].sort()
  for(const key of keys){const found=firstDifference((expected as Record<string,unknown>)[key],(actual as Record<string,unknown>)[key],`${path}/${key}`);if(found)return found}return
 }
 return {member:path||'/',expected,actual}
}
export function compareCorpus(expected:CorpusExpected,actual:CorpusExpected,tolerance:string[]):CorpusDifference|undefined{
 const left=clone(expected),right=clone(actual)
 for(const pointer of tolerance){omitPointer(left,pointer);omitPointer(right,pointer)}
 return firstDifference(left,right)
}

/** A replay transport backed only by the entry. Exhaustion or a request mismatch is a named failure; it never calls a live provider. */
export class ScriptedCorpusServer{
 private requestAt=0;private toolAt=0
 constructor(private readonly entry:CorpusEntry){}
 async model(suffix:string,request:ModelRequest):Promise<Response>{
  const row=this.entry.requests[this.requestAt++]
  const {signal:_,...held}=request
  const actual={suffix,request:held}
  if(!row||canonicalJSON({suffix:row.suffix,request:row.request})!==canonicalJSON(actual))throw Error(`corpus request ${this.requestAt-1} differs`)
  return new Response(row.response.body,{status:row.response.status,headers:row.response.headers})
 }
 async tool(name:string,args:Record<string,unknown>):Promise<McpToolResult>{
  const row=this.entry.toolResults[this.toolAt++]
  if(!row||canonicalJSON({name:row.name,args:row.args})!==canonicalJSON({name,args}))throw Error(`corpus tool result ${this.toolAt-1} differs`)
  return clone(row.result)
 }
 complete(){if(this.requestAt!==this.entry.requests.length)throw Error(`corpus requests: used ${this.requestAt}, recorded ${this.entry.requests.length}`);if(this.toolAt!==this.entry.toolResults.length)throw Error(`corpus tool results: used ${this.toolAt}, recorded ${this.entry.toolResults.length}`)}
}

export async function replayCorpus(entry:CorpusEntry,run:(server:ScriptedCorpusServer)=>Promise<AssistantEvent[]>):Promise<void>{
 const server=new ScriptedCorpusServer(entry),events=await run(server);server.complete()
 const difference=compareCorpus(entry.expected,expectedFromEvents(events),entry.manifest.tolerance)
 if(difference)throw Error(`corpus ${entry.manifest.id}: ${difference.member} differs`)
}

/** Wrap a session before its engine starts, then write its complete host-visible stream before end. */
export function recordSession<T extends import('./engine').EngineSession>(session:T):{session:T;stream:(events:AsyncIterable<AssistantEvent>)=>AsyncIterable<AssistantEvent>}{
 if(session.record!==true||session.corpus===undefined)return {session,stream:events=>events}
 const requests:RecordedRequest[]=[],toolResults:RecordedToolResult[]=[],calls=new Map<string,Record<string,unknown>[]>(),configuration=session.corpus
 const held={...session} as T
 if('model'in held){
  const call=held.model.call
  held.model={...held.model,call:async(suffix,request)=>{const {signal:_,...plain}=request,response=await call(suffix,request),copy=response.clone(),headers=Object.fromEntries(copy.headers.entries()),body=await copy.text();requests.push({suffix,request:plain,response:{status:copy.status,headers,body}});return response}}
 }else{
  const run=held.agent.run
  held.agent={...held.agent,run:async(request,callbacks)=>{await run(request,callbacks);requests.push({suffix:'agent.run',request:structuredClone(request),response:{status:200,headers:{},body:''}})}}
 }
 const stream=async function*(events:AsyncIterable<AssistantEvent>):AsyncGenerator<AssistantEvent>{
  const recorded:AssistantEvent[]=[];let checkpoint:unknown={}
  for await(const event of events){
   if(event.type==='end'){
    const result=await writeCorpusEntry({manifest:{...configuration.manifest,policy:(recorded.find(row=>row.type==='policy_resolved') as Extract<AssistantEvent,{type:'policy_resolved'}>|undefined)?.members??configuration.manifest.policy},requests,toolResults,events:recorded,checkpoint},configuration.guard,configuration.sink)
    yield result.event;yield event;return
   }
   recorded.push(clone(event))
   if(event.type==='execution_checkpoint')checkpoint=clone(event.checkpoint)
   if(event.type==='tool_call'){
    const key=event.callId??event.name,list=calls.get(key)??[]
    if(event.args&&typeof event.args==='object'&&!Array.isArray(event.args))list.push(clone(event.args as Record<string,unknown>));else list.push({})
    calls.set(key,list)
   }
   if(event.type==='tool_result'){
    const key=event.callId??event.name,list=calls.get(key)??[],args=list.shift()??{}
    toolResults.push({name:event.name,args,result:{content:[{type:'text',text:event.text}],isError:event.isError,...(event.structured===undefined?{}:{structuredContent:clone(event.structured)})}})
   }
   yield event
  }
 }
 return {session:held,stream}
}
