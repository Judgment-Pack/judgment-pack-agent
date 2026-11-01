// @vitest-environment node
// ADR-0029 with #226: a run under a delegation that parks for re-authorization. A credential refusal is not a delegation
// refusal and counts nothing; a re-send must still pass the delegation's admission, and is counted once at its physical
// dispatch, RFC 0030 §2's "a freed attempt counts once per dispatch", through the real engine, writer and effect host.
import {describe,it,expect,vi} from 'vitest'
import chains from '../../contract/vectors/principal/chains.json'
import type {AssistantEvent,EngineSession,HostTool} from './engine'
import {vercel} from './engines/vercel'
import {withDelegation} from './delegation'
import {answerWith} from './testing/scenarioDriver'
import {toolListDigest} from './toolSources'
import {sha256} from './records'
import {MemoryRunStore,type RunStore} from './storage/run'
import {ReferenceEffectHost,fenceOf,reauthorizationOf,unattendedRunStore,type CredentialRefusal,type LeaseFence,type LeaseLifetime} from './storage/lease'
import {parkRun,reauthorizedRunStore,resumeParked,wakeKeyDigest,wakeRun,type WaitLifetime,type WakeEvidence} from './storage/wait'
import type {AdmissionAnswer,AdmissionRead,ChildLinkRequest,PrincipalBinding,RunPrincipal} from './principal'
import type {Transport} from '@modelcontextprotocol/sdk/shared/transport.js'
import type {JSONRPCMessage} from '@modelcontextprotocol/sdk/types.js'
import {openAssistantConnection} from './session'
import type {RuntimeExecutionOwner} from './runtimeActions'

/** The principals a delegated run's lease, fence and wait hold: the same for the root and for the chain that names source runtime. */
const BINDING={sub:chains.items[0]![0]!.sub,rootActor:chains.items[0]![0]!.act.agent,host:chains.items[0]![0]!.host} as PrincipalBinding
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
 const lease=await store.acquireLease({resource:'run-r',owner:'worker-a',expectedRevision:null,durationMs:HOUR,principal:BINDING})
 const view=unattendedRunStore({store,fence:fenceOf(lease),effectHost:host})
 const events=await drive(view,[await writeTool(execute)],principal(reads),provider([CALL,{say:'Could not.'}]).call)
 return {clock,store,state,host,execute,view,fence:fenceOf(lease),events}
}
async function parked(reads:AdmissionRead[]){
 const held=await refused(reads),{clock,store,fence,view}=held
 const {wait}=await parkRun(parkInput(store,fence,reauthorizationOf(view)!))
 clock.at=T0+3*MINUTE
 await wakeRun(store,wake)
 const {model,...rest}=wait.pins
 const current={version:1 as const,resource:'run-r',wait:'wait-r',...rest,model:{...model,status:'available' as const},policy:{digest:wait.pins.policy}}
 return {...held,wait,current,input:{store,resource:'run-r',wait:'wait-r',evidence:'refreshed-1',owner:'worker-b',durationMs:HOUR,reauthorization:action}}
}
async function claimed(reads:AdmissionRead[]){
 const held=await parked(reads),{store,current,input}=held
 const resumed=await resumeParked({...input,current})
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
   const lease=await store.acquireLease({resource:'run-r',owner:'worker-a',expectedRevision:null,durationMs:HOUR,principal:BINDING})
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

describe("a re-authorization wait's resume under a delegation",()=>{
 const resumeRefusal=(reason:string)=>expect.objectContaining({name:'ResumeRefused',reason})
 it('the wait pins the principals, and its proof keeps the version 2 fence it was parked under',async()=>{
  const {wait}=await parked([])
  expect(wait.pins.principal).toEqual(BINDING)
  expect(wait.reauthorization!.fence).toMatchObject({version:2,principal:BINDING})
 })
 it('evidence of other principals is principal-changed, of none is evidence-missing, and neither claims anything, whatever the refresh says',async()=>{
  const {store,current,input}=await parked([])
  await expect(resumeParked({...input,current:{...current,principal:{...BINDING,rootActor:'d1'.repeat(16)}}})).rejects.toEqual(resumeRefusal('principal-changed'))
  const {principal:_,...none}=current
  await expect(resumeParked({...input,current:none})).rejects.toEqual(resumeRefusal('evidence-missing'))
  expect(await store.readLease('run-r')).toMatchObject({state:'released',epoch:'1'})
  expect((await store.readWait('run-r','wait-r'))!.state).toBe('woken')
 })
 it("a changed credential identity stays #226's identity-changed: the delegation's principals are the same",async()=>{
  const {current,input}=await parked([])
  await expect(resumeParked({...input,current,reauthorization:{...action,action:'replacement'}})).rejects.toEqual(resumeRefusal('identity-changed'))
 })
 it("the delegation's refusal comes first, when both changed",async()=>{
  const {current,input}=await parked([])
  await expect(resumeParked({...input,current:{...current,principal:{...BINDING,rootActor:'d1'.repeat(16)}},reauthorization:{...action,action:'replacement'}})).rejects.toEqual(resumeRefusal('principal-changed'))
 })
 it("the claim's lease and fence keep the principals, and the re-send's view accepts them",async()=>{
  const {resumed,continued,execute}=await claimed([])
  expect(resumed.lease).toMatchObject({version:2,epoch:'2',principal:BINDING})
  expect(resumed.fence).toMatchObject({version:2,principal:BINDING})
  const reads:AdmissionRead[]=[]
  await drive(continued,[await writeTool(execute)],principal(reads),provider([CALL,{say:'Written.'}]).call,true)
  expect(execute).toHaveBeenCalledOnce()
 })
 for(const [label,given] of [['another subject',{chain:4}],['no delegation at all',undefined]] as const)it(`a continuation under ${label} sends nothing, though the claim was held: principal-changed`,async()=>{
  const reads:AdmissionRead[]=[]
  const {continued,execute}=await claimed(reads)
  const session={prompt:'Write the note.',testPrompt:'',tools:[],callTool:vi.fn(async()=>({content:[]})),hostTools:[await writeTool(execute)],signal:new AbortController().signal,allowConversation:true,critic:'off',context:{...(given?{principal:{...principal(reads),chain:JSON.stringify(chains.items[given.chain])}}:{}),currentRequest:{message:'Write the note.',input:'note'},runStore:{store:continued,id:'run-r',resume:true}},model:{family:'openai-compatible',model:'fixture',call:provider([CALL]).call}} as unknown as EngineSession
  const events:AssistantEvent[]=[];for await(const event of vercel.start(session))events.push(event)
  expect(execute).not.toHaveBeenCalled()
  expect(JSON.stringify(events.filter(e=>e.type==='error'))).toContain('principal-changed')
 })
})

describe('a child under a run that parked for re-authorization',()=>{
 async function tools(wrote:()=>void):Promise<HostTool[]>{
  return [await writeTool((async()=>{wrote();return {content:[{type:'text',text:'written'}]}}) as HostTool['execute'])]
 }
 const CHILD_CHAIN=JSON.stringify(chains.items[1])
 const session=(store:RunStore,hostTools:HostTool[],given:RunPrincipal,turns:Parameters<typeof answerWith>[0][],resume=false)=>({prompt:'Write the note.',testPrompt:'',tools:[],callTool:vi.fn(async()=>({content:[]})),hostTools,signal:new AbortController().signal,allowConversation:true,interactive:true,critic:'off',context:{principal:given,currentRequest:{message:'Write the note.',input:'note'},runStore:{store,id:'run-r',...(resume?{resume:true}:{})}},model:{family:'openai-compatible',model:'fixture',call:provider(turns).call}}) as unknown as EngineSession
 const collect=async(given:EngineSession)=>{const out:AssistantEvent[]=[];for await(const event of withDelegation(vercel).start(given))out.push(event);return out}
 const DELEGATE={call:{tool:'delegate_tasks',arguments:{tasks:[{title:'Check',profile:'review',objective:'Check the note.'}]}}}
 async function stoppedRun(reads:AdmissionRead[],asked:ChildLinkRequest[],ran:string[]){
  const clock={at:T0},store=new DurableWaitStore({now:()=>new Date(clock.at)}),state:{refusal:CredentialRefusal|undefined}={refusal:REFUSED}
  const host=new ReferenceEffectHost(store,{credentials:async()=>state.refusal})
  const lease=await store.acquireLease({resource:'run-r',owner:'worker-a',expectedRevision:null,durationMs:HOUR,principal:BINDING})
  const view=unattendedRunStore({store,fence:fenceOf(lease),effectHost:host})
  const list=await tools(()=>ran.push('write_note'))
  const delegated=(extra:Partial<RunPrincipal>={}):RunPrincipal=>({...principal(reads),child:request=>{asked.push(request);return {chain:CHILD_CHAIN}},...extra})
  await collect(session(view,list,delegated(),[CALL,{say:'Could not.'}]))
  return {clock,store,state,host,list,view,fence:fenceOf(lease),delegated}
 }
 async function resume(held:Awaited<ReturnType<typeof stoppedRun>>){
  const {clock,store,state,host,view,fence}=held
  const {wait}=await parkRun(parkInput(store,fence,reauthorizationOf(view)!))
  clock.at=T0+3*MINUTE
  await wakeRun(store,wake)
  const {model,...rest}=wait.pins
  const current={version:1 as const,resource:'run-r',wait:'wait-r',...rest,model:{...model,status:'available' as const},policy:{digest:wait.pins.policy}}
  const resumed=await resumeParked({store,resource:'run-r',wait:'wait-r',evidence:'refreshed-1',owner:'worker-b',durationMs:HOUR,current,reauthorization:action})
  state.refusal=undefined
  return reauthorizedRunStore({store,effectHost:host,resumed})
 }
 it('no child starts while it is parked: the run cannot be leased, and its proof is what stopped it',async()=>{
  const reads:AdmissionRead[]=[],asked:ChildLinkRequest[]=[],ran:string[]=[]
  const held=await stoppedRun(reads,asked,ran)
  const {wait}=await parkRun(parkInput(held.store,held.fence,reauthorizationOf(held.view)!))
  await expect(held.store.acquireLease({resource:'run-r',owner:'worker-x',expectedRevision:wait.checkpoint.revision,durationMs:HOUR,principal:BINDING})).rejects.toEqual(expect.objectContaining({name:'WaitRefused',reason:'wait-pending'}))
  expect(ran).toEqual([])
  expect(asked).toEqual([])
 })
 it("after the re-authorization a child runs under its own link, on the claim's view, and the parent's re-send is the grant's alone",async()=>{
  const reads:AdmissionRead[]=[],asked:ChildLinkRequest[]=[],ran:string[]=[]
  const held=await stoppedRun(reads,asked,ran)
  const before=reads.length
  const continued=await resume(held)
  const events=await collect(session(continued,held.list,held.delegated(),[DELEGATE,{say:'One finding.'},CALL,{say:'Written.'}],true))
  expect(events.filter(e=>e.type==='error')).toEqual([])
  expect(asked.length).toBe(1)
  expect(ran).toEqual(['write_note'])
  const last=[...events].reverse().find(e=>e.type==='delegated_task') as Extract<AssistantEvent,{type:'delegated_task'}>&{principal?:{chain:string[]}}
  expect(last.task.state).toBe('complete')
  expect(last.principal?.chain).toEqual([chains.digests.root,chains.digests.child])
  // A child's own calls are held by its chain (principalChild.test.ts). Its standing read counts nothing, and the one physical dispatch here, the grant's re-send, is counted once.
  expect(reads.slice(before).filter(r=>r.call.source==='local').map(r=>[r.call.tool,r.call.count])).toEqual([['delegate_tasks',false]])
  expect(reads.slice(before).filter(r=>r.call.count).map(r=>[r.call.tool,r.chain.length])).toEqual([['write_note',1]])
 })
 it('a delegation revoked meanwhile starts no child, by name, and the re-send is refused with it',async()=>{
  const reads:AdmissionRead[]=[],asked:ChildLinkRequest[]=[],ran:string[]=[]
  const held=await stoppedRun(reads,asked,ran)
  const continued=await resume(held)
  const events=await collect(session(continued,held.list,held.delegated({admission:read=>{reads.push(read);return REVOKED}}),[DELEGATE,CALL,{say:'Could not.'}],true))
  expect(ran).toEqual([])
  expect(asked).toEqual([])
  expect(delegationRefusals(events)).toContain('revoked')
  const last=[...events].reverse().find(e=>e.type==='delegated_task') as Extract<AssistantEvent,{type:'delegated_task'}>
  expect(last.task).toMatchObject({state:'failed',problem:expect.stringContaining('revoked')})
 })
})
