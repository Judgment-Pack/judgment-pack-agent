// @vitest-environment node
// ADR-0029 with #226: a run under a delegation that parks for re-authorization. A credential refusal is not a delegation
// refusal and counts nothing; a re-send must still pass the delegation's admission, and is counted once at its physical
// dispatch, RFC 0030 §2's "a freed attempt counts once per dispatch", through the real engine, writer and effect host.
import {describe,it,expect,vi} from 'vitest'
import chains from '../../contract/vectors/principal/chains.json'
import type {AssistantEvent,EngineSession,HostTool} from './engine'
import {vercel} from './engines/vercel'
import {answerWith} from './testing/scenarioDriver'
import {toolListDigest} from './toolSources'
import {sha256} from './records'
import {MemoryRunStore,type RunStore} from './storage/run'
import {ReferenceEffectHost,fenceOf,reauthorizationOf,unattendedRunStore,type CredentialRefusal,type LeaseFence,type LeaseLifetime} from './storage/lease'
import {parkRun,reauthorizedRunStore,resumeParked,wakeKeyDigest,wakeRun,type WaitLifetime,type WakeEvidence} from './storage/wait'
import type {AdmissionAnswer,AdmissionRead,RunPrincipal} from './principal'
import type {Transport} from '@modelcontextprotocol/sdk/shared/transport.js'
import type {JSONRPCMessage} from '@modelcontextprotocol/sdk/types.js'
import {openAssistantConnection} from './session'
import type {RuntimeExecutionOwner} from './runtimeActions'

const T='2026-10-30T01:00:00.000Z',T0=Date.parse(T),MINUTE=60_000,HOUR=60*MINUTE,LATE='2026-10-30T08:00:00.000Z'
const iso=(ms:number)=>new Date(ms).toISOString()
const KEY='wk.delegated.reauth.Q7z2Xm9LpV4cR8tNb3Yh6Df1G'
const ADMIT:AdmissionAnswer={admission:'admit'},REVOKED:AdmissionAnswer={admission:'refuse',refusal:'revoked'}
class DurableWaitStore extends MemoryRunStore {override readonly leaseLifetime:LeaseLifetime='durable';override readonly waitLifetime:WaitLifetime='durable'}
const credential=`sha256:${sha256('sentinel-person@example.invalid')}`
const REFUSED:CredentialRefusal={cause:'expired',connection:'notes-conn',principal:credential,permissions:['notes:write'],requires:['notes:write']}
const WRITE={name:'write_note',description:'Write one note.',inputSchema:{type:'object'},taxonomy:{operation:'act',effect:'act'} as const}
const CALL={call:{tool:'notes__write_note',arguments:{text:'Mileage'}}}
const principal=(reads:AdmissionRead[],{now=T,answer=ADMIT}:{now?:string;answer?:AdmissionAnswer|((read:AdmissionRead)=>AdmissionAnswer)}={}):RunPrincipal=>({chain:JSON.stringify(chains.items[0]),keys:chains.keys,software:chains.software,audience:chains.audience,clock:()=>Date.parse(now),admission:read=>{reads.push(read);return typeof answer==='function'?answer(read):answer}})
const provider=(turns:Parameters<typeof answerWith>[0][])=>{let requests=0;return {call:async()=>{const turn=turns[requests];requests++;if(!turn)throw Error('no scripted turn');return answerWith(turn,'stream',requests-1)}}}
async function writeTool(execute:HostTool['execute']):Promise<HostTool>{
 return {...WRITE,effectClass:'act',source:{kind:'custom',id:'notes',trust:'person',version:'1',toolListDigest:await toolListDigest([WRITE] as never)},approve:()=>'allow',execute} as HostTool
}
async function drive(store:RunStore,tools:HostTool[],given:RunPrincipal,call:(...args:never[])=>Promise<Response>,resume=false):Promise<AssistantEvent[]>{
 const session={prompt:'Write the note.',testPrompt:'',tools:[],callTool:vi.fn(async()=>({content:[]})),hostTools:tools,signal:new AbortController().signal,allowConversation:true,critic:'off',context:{principal:given,currentRequest:{message:'Write the note.',input:'note'},runStore:{store,id:'run-r',...(resume?{resume:true}:{})}},model:{family:'openai-compatible',model:'fixture',call}} as unknown as EngineSession
 const out:AssistantEvent[]=[];for await(const event of vercel.start(session))out.push(event);return out
}
const delegationRefusals=(events:AssistantEvent[])=>events.filter(e=>e.type==='guardrail'&&e.reason==='delegation refused').map(e=>(e as {refusal?:{name:string}}).refusal?.name)
const parkInput=(store:RunStore,fence:LeaseFence,proof:NonNullable<ReturnType<typeof reauthorizationOf>>)=>({store,fence,wait:'wait-r',continuation:'k-r',condition:{event:{kind:'reauthorized',subject:'notes-conn'}},model:{provider:'openai',model:'fixture'},wakeKeyDigest:wakeKeyDigest(KEY),release:[],reauthorization:proof})
const wake:WakeEvidence={version:1,resource:'run-r',wait:'wait-r',evidence:'refreshed-1',kind:'event',event:{kind:'reauthorized',subject:'notes-conn'},key:KEY}
const action={version:1,resource:'run-r',wait:'wait-r',connection:'notes-conn',action:'refresh',principal:credential,permissions:['notes:write'],actor:'person-owner',at:iso(T0+2*MINUTE)}

async function refused(reads:AdmissionRead[]){
 const clock={at:T0},store=new DurableWaitStore({now:()=>new Date(clock.at)}),state:{refusal:CredentialRefusal|undefined}={refusal:REFUSED}
 const host=new ReferenceEffectHost(store,{credentials:async()=>state.refusal})
 const execute=vi.fn(async()=>({content:[{type:'text' as const,text:'written'}]}))
 const lease=await store.acquireLease({resource:'run-r',owner:'worker-a',expectedRevision:null,durationMs:HOUR})
 const view=unattendedRunStore({store,fence:fenceOf(lease),effectHost:host})
 const events=await drive(view,[await writeTool(execute)],principal(reads),provider([CALL,{say:'Could not.'}]).call)
 return {clock,store,state,host,execute,view,fence:fenceOf(lease),events}
}
async function claimed(reads:AdmissionRead[]){
 const held=await refused(reads),{clock,store,fence,view}=held
 const {wait}=await parkRun(parkInput(store,fence,reauthorizationOf(view)!))
 clock.at=T0+3*MINUTE
 await wakeRun(store,wake)
 const {model,...rest}=wait.pins
 const current={version:1 as const,resource:'run-r',wait:'wait-r',...rest,model:{...model,status:'available' as const},policy:{digest:wait.pins.policy}}
 const resumed=await resumeParked({store,resource:'run-r',wait:'wait-r',evidence:'refreshed-1',owner:'worker-b',durationMs:HOUR,current,reauthorization:action})
 held.state.refusal=undefined
 return {...held,resumed,continued:await reauthorizedRunStore({store,effectHost:held.host,resumed})}
}

describe('a credential refusal under a delegation',()=>{
 it('is not a delegation refusal, and counts nothing: the effect host refused it before any dispatch',async()=>{
  const reads:AdmissionRead[]=[]
  const {execute,view,events}=await refused(reads)
  expect(execute).not.toHaveBeenCalled()
  expect(delegationRefusals(events)).toEqual([])
  const result=events.find(e=>e.type==='tool_result') as Extract<AssistantEvent,{type:'tool_result'}>&{principal?:unknown}
  expect(result).toMatchObject({isError:true,structured:{failure:{code:'authentication',source:'tool',outcome:'not-started'}}})
  expect(result.principal).toBeDefined()
  expect(reads.length).toBeGreaterThan(0)
  expect(reads.filter(r=>r.call.count)).toEqual([])
  expect(reauthorizationOf(view)).toBeDefined()
 })
})

describe('the re-send after a re-authorization, under a delegation',()=>{
 it('passes the admission, and is counted once, at its physical dispatch',async()=>{
  const reads:AdmissionRead[]=[]
  const {execute,continued}=await claimed(reads)
  const before=reads.length
  const events=await drive(continued,[await writeTool(execute)],principal(reads),provider([CALL,{say:'Written.'}]).call,true)
  expect(events.filter(e=>e.type==='error')).toEqual([])
  expect(execute).toHaveBeenCalledOnce()
  // The boundary's check before its decision, the gate's, and the one at the dispatch, which alone counts.
  expect(reads.slice(before).map(r=>r.call.count)).toEqual([false,false,true])
  expect(reads.filter(r=>r.call.count).length).toBe(1)
 })
 for(const [label,given,name] of [
  ['revoked meanwhile',{answer:REVOKED},'revoked'],
  ['expired meanwhile',{now:LATE},'expired'],
  ['revoked as it dispatches',{answer:(read:AdmissionRead)=>read.call.count?REVOKED:ADMIT},'revoked'],
 ] as const)it(`is refused by name when the delegation is ${label}, though the credentials were refreshed`,async()=>{
  const reads:AdmissionRead[]=[]
  const {execute,continued}=await claimed(reads)
  const events=await drive(continued,[await writeTool(execute)],principal(reads,given),provider([CALL,{say:'Could not.'}]).call,true)
  expect(execute).not.toHaveBeenCalled()
  expect(delegationRefusals(events)).toEqual([name])
 })
 it('refused again for its credentials, counts nothing',async()=>{
  const reads:AdmissionRead[]=[]
  const {execute,continued,state}=await claimed(reads)
  state.refusal=REFUSED
  await drive(continued,[await writeTool(execute)],principal(reads),provider([CALL,{say:'Could not.'}]).call,true)
  expect(execute).not.toHaveBeenCalled()
  expect(reads.filter(r=>r.call.count)).toEqual([])
 })
})

describe('an acting Runtime call refused for its credentials, under a delegation',()=>{
 const pin='sha256:'+'a'.repeat(64)
 const owner=():RuntimeExecutionOwner=>({artifactDigest:pin,processId:'launch-one',projectId:'project-one',projectLabel:'Example project',sessionId:'server-session-one',principalDigest:pin,principalLabel:'Project editor',scopeDigest:pin,scopeLabel:'Evaluate example project'})
 async function runtime(){
  const calls:unknown[]=[],current=owner()
  const transport:Transport={start:async()=>{},close:async()=>{},send:async(message:JSONRPCMessage)=>{
   const m=message as {id?:string|number;method?:string;params?:unknown}
   if(m.id===undefined)return
   let result:unknown
   if(m.method==='initialize')result={protocolVersion:'2025-06-18',capabilities:{tools:{}},serverInfo:{name:'bounded-runtime',version:'fixture'}}
   else if(m.method==='tools/list')result={tools:[{name:'experimental_evaluate',description:'Evaluate one unsaved draft',inputSchema:{type:'object',properties:{value:{type:'integer'},rehearsal:{type:'boolean'}}},_meta:{operation:'evaluate',effect:'act',experimental:true}}]}
   else if(m.method==='tools/call'){calls.push(m.params);result={content:[{type:'text',text:'evaluated draft'}],structuredContent:{status:'ok',auditAppended:false}}}
   else throw Error('unexpected method')
   queueMicrotask(()=>transport.onmessage?.({jsonrpc:'2.0',id:m.id!,result:result as Record<string,unknown>}))
  }}
  const connection=openAssistantConnection({allowed:['experimental_evaluate'],transport,onEvent:()=>{},runtimeActions:{owner:current,current:async()=>current,approve:()=>'allow'}})
  return {...await connection.ready,calls,close:connection.close}
 }
 const raw={value:7,rehearsal:false}
 const given=(reads:AdmissionRead[]):RunPrincipal=>({...principal(reads),chain:JSON.stringify(chains.items[5])})
 async function run(held:Awaited<ReturnType<typeof runtime>>,store:RunStore,delegation:RunPrincipal,resume:boolean){
  const script=provider(resume?[{call:{tool:'experimental_evaluate',arguments:raw}},{say:'Evaluated.'}]:[{call:{tool:'experimental_evaluate',arguments:raw}},{say:'Could not.'}])
  const session={prompt:'Evaluate the unsaved draft.',testPrompt:'',tools:held.tools,callTool:held.callTool,hostTools:[],signal:new AbortController().signal,allowConversation:true,critic:'off',context:{principal:delegation,currentRequest:{message:'Evaluate the unsaved draft.',input:'draft'},runStore:{store,id:'run-r',...(resume?{resume:true}:{})}},model:{family:'openai-compatible',model:'fixture',call:script.call}} as unknown as EngineSession
  const out:AssistantEvent[]=[];for await(const event of vercel.start(session))out.push(event);return out
 }
 it('counts nothing when refused, and once at the physical call of its re-send',async()=>{
  const reads:AdmissionRead[]=[],held=await runtime()
  const clock={at:T0},store=new DurableWaitStore({now:()=>new Date(clock.at)}),state:{refusal:CredentialRefusal|undefined}={refusal:REFUSED}
  const host=new ReferenceEffectHost(store,{credentials:async()=>state.refusal})
  try{
   const lease=await store.acquireLease({resource:'run-r',owner:'worker-a',expectedRevision:null,durationMs:HOUR})
   const view=unattendedRunStore({store,fence:fenceOf(lease),effectHost:host})
   const events=await run(held,view,given(reads),false)
   expect(held.calls).toEqual([])
   expect(delegationRefusals(events)).toEqual([])
   expect(reads.filter(r=>r.call.count)).toEqual([])
   const {wait}=await parkRun(parkInput(store,fenceOf(lease),reauthorizationOf(view)!))
   clock.at=T0+3*MINUTE
   await wakeRun(store,wake)
   const {model,...rest}=wait.pins
   const current={version:1 as const,resource:'run-r',wait:'wait-r',...rest,model:{...model,status:'available' as const},policy:{digest:wait.pins.policy}}
   const resumed=await resumeParked({store,resource:'run-r',wait:'wait-r',evidence:'refreshed-1',owner:'worker-b',durationMs:HOUR,current,reauthorization:action})
   state.refusal=undefined
   const second=await run(held,await reauthorizedRunStore({store,effectHost:host,resumed}),given(reads),true)
   expect(second.filter(e=>e.type==='error')).toEqual([])
   expect(held.calls.length).toBe(1)
   expect(reads.filter(r=>r.call.count).length).toBe(1)
   expect(reads.at(-1)!.call).toEqual({source:'runtime',tool:'experimental_evaluate',effect:'act',count:true})
  }finally{await held.close()}
 })
})
