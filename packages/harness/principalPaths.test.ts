// @vitest-environment node
// ADR-0029: every path that reaches a dispatch under a delegation. An acting Runtime call and a host tool with no
// source refuse past expiry and after revocation; a mapping belongs to a gateway source; a read served without a dispatch
// counts nothing; a resume names what it refuses; and a run resumed at its handoff starts no child outside its delegation.
import {describe,it,expect,beforeEach,vi} from 'vitest'
import chains from '../../contract/vectors/principal/chains.json'
import type {Transport} from '@modelcontextprotocol/sdk/shared/transport.js'
import type {JSONRPCMessage} from '@modelcontextprotocol/sdk/types.js'
import {openAssistantConnection} from './session'
import {managedContext} from './context/managed'
import {vercel} from './engines/vercel'
import {codex} from './engines/codex'
import {withDelegation} from './delegation'
import {MemoryRunStore,type RunStore} from './storage/run'
import type {RuntimeExecutionOwner,RuntimeActionPort} from './runtimeActions'
import type {AgentAssistantSession,AssistantEvent,AssistantSession,EngineSession,HostTool,McpTool} from './engine'
import type {AgentRun} from './agent'
import {answerWith} from './testing/scenarioDriver'
import {declareSources,toolListDigest} from './toolSources'
import {holdsDelegation,openPrincipal,requireOpenedPrincipal,type AdmissionAnswer,type AdmissionRead,type RunPrincipal} from './principal'
import {normalize} from './thinking'
import {configureHarness} from './host'
import {testHost} from './testing/host'

beforeEach(()=>configureHarness(testHost()))
const T='2026-10-30T01:00:00.000Z',LATE='2026-10-30T08:00:00.000Z'
const REVOKED:AdmissionAnswer={admission:'refuse',refusal:'revoked'},ADMIT:AdmissionAnswer={admission:'admit'}
const principal=(reads:AdmissionRead[],{now=T,answer=ADMIT,chain=0}:{now?:string;answer?:AdmissionAnswer|((read:AdmissionRead)=>AdmissionAnswer);chain?:number}={}):RunPrincipal=>({chain:JSON.stringify(chains.items[chain]),keys:chains.keys,software:chains.software,audience:chains.audience,clock:()=>Date.parse(now),admission:read=>{reads.push(read);return typeof answer==='function'?answer(read):answer}})

describe('an acting Runtime call under a delegation',()=>{
 const pin='sha256:'+'a'.repeat(64)
 const owner=():RuntimeExecutionOwner=>({artifactDigest:pin,processId:'launch-one',projectId:'project-one',projectLabel:'Example project',sessionId:'server-session-one',principalDigest:pin,principalLabel:'Project editor',scopeDigest:pin,scopeLabel:'Evaluate example project'})
 const evaluate=():McpTool=>({name:'experimental_evaluate',description:'Evaluate one unsaved draft',inputSchema:{type:'object',properties:{value:{type:'integer'},rehearsal:{type:'boolean'}}},_meta:{operation:'evaluate',effect:'act',experimental:true}})
 async function runtime(port?:RuntimeActionPort){
  const calls:{name:string;arguments:Record<string,unknown>}[]=[]
  const transport:Transport={start:async()=>{},close:async()=>{},send:async(message:JSONRPCMessage)=>{
   const m=message as {id?:string|number;method?:string;params?:{name:string;arguments:Record<string,unknown>}}
   if(m.id===undefined)return
   let result:unknown
   if(m.method==='initialize')result={protocolVersion:'2025-06-18',capabilities:{tools:{}},serverInfo:{name:'bounded-runtime',version:'fixture'}}
   else if(m.method==='tools/list')result={tools:[evaluate()]}
   else if(m.method==='tools/call'){calls.push(structuredClone(m.params!));result={content:[{type:'text',text:'evaluated draft'}],structuredContent:{status:'ok',auditAppended:false}}}
   else throw Error('unexpected method')
   queueMicrotask(()=>transport.onmessage?.({jsonrpc:'2.0',id:m.id!,result:result as Record<string,unknown>}))
  }}
  const connection=openAssistantConnection({allowed:['experimental_evaluate'],transport,onEvent:()=>{},runtimeActions:port})
  return {...await connection.ready,calls,close:connection.close}
 }
 const raw={value:7,rehearsal:false}
 function input(held:Awaited<ReturnType<typeof runtime>>,store:MemoryRunStore,given:RunPrincipal,resume=false,decision?:{callId:string;approved:boolean}):EngineSession{
  let requests=0
  return {prompt:'Evaluate the unsaved draft.',testPrompt:'',tools:held.tools,callTool:held.callTool,hostTools:[],signal:new AbortController().signal,allowConversation:true,critic:'off' as const,context:{principal:given,currentRequest:{message:'Evaluate the unsaved draft.',input:'draft'},runStore:{store,id:'runtime-action-run',...(resume?{resume:true}:{})},...(decision?{approval:decision}:{})},model:{family:'openai-compatible',model:'fixture',call:async()=>{requests++;if(requests>1&&!resume)return answerWith({say:'Evaluation completed.'},'stream',requests-1);return answerWith(resume?{say:'Evaluation completed.'}:{call:{tool:'experimental_evaluate',arguments:raw}},'stream',requests-1)}}} as EngineSession
 }
 const collect=async(session:EngineSession)=>{const events:AssistantEvent[]=[];for await(const event of vercel.start(session))events.push(event);return events}
 const refused=(events:AssistantEvent[])=>events.filter(e=>e.type==='guardrail'&&e.reason==='delegation refused') as Extract<AssistantEvent,{type:'guardrail'}>[]
 for(const [label,given,name] of [
  ['past its expiry',{now:LATE},'expired'],
  ['revoked',{answer:REVOKED},'revoked'],
 ] as const)it(`is refused when the host allows it, under a delegation ${label}`,async()=>{
  const current=owner(),held=await runtime({owner:current,current:async()=>current,approve:()=>'allow'}),store=new MemoryRunStore(),reads:AdmissionRead[]=[]
  try{
   const events=await collect(input(held,store,principal(reads,given)))
   expect(held.calls).toEqual([])
   expect(refused(events).map(e=>e.refusal?.name)).toEqual([name])
   expect(reads.filter(r=>r.call.count)).toEqual([])
  }finally{await held.close()}
 })
 it('is refused outside the scope, and not put to the person',async()=>{
  const current=owner(),held=await runtime({owner:current,current:async()=>current}),store=new MemoryRunStore(),reads:AdmissionRead[]=[]
  try{
   const events=await collect(input(held,store,principal(reads)))
   expect(held.calls).toEqual([])
   expect(events.some(e=>e.type==='approval_requested')).toBe(false)
   expect(refused(events).map(e=>e.refusal)).toEqual([{name:'out-of-scope',reason:'source'}])
  }finally{await held.close()}
 })
 it('reads the port under source runtime, effect act, uncounted before it is asked',async()=>{
  const current=owner(),held=await runtime({owner:current,current:async()=>current}),store=new MemoryRunStore(),reads:AdmissionRead[]=[]
  try{
   await collect(input(held,store,principal(reads)))
   expect(reads.map(r=>[r.audience,r.call.source,r.call.tool,r.call.effect,r.call.count])).toEqual([[{kind:'agent-host',name:'desk'},'runtime','experimental_evaluate','act',false]])
  }finally{await held.close()}
 })
 const scoped=chains.items.findIndex(chain=>(chain as {scope:{sources:{source:string}[]}}[]).at(-1)!.scope.sources.some(entry=>entry.source==='runtime'))
 it('runs once, counted once immediately before the physical call, under a chain that names it',async()=>{
  const current=owner(),held=await runtime({owner:current,current:async()=>current,approve:()=>'allow'}),store=new MemoryRunStore(),reads:AdmissionRead[]=[]
  try{
   const events=await collect(input(held,store,principal(reads,{chain:scoped})))
   expect(refused(events)).toEqual([])
   expect(held.calls.length).toBe(1)
   expect(reads.map(r=>r.call.count)).toEqual([false,true])
  }finally{await held.close()}
 })
 for(const [label,answer,name] of [
  ['revoked before the continuation',REVOKED,'revoked'],
  ['revoked as the continuation dispatches',(read:AdmissionRead)=>read.call.count?REVOKED:ADMIT,'revoked'],
 ] as const)it(`carries no approval past the delegation: ${label}`,async()=>{
  const current=owner(),held=await runtime({owner:current,current:async()=>current}),store=new MemoryRunStore(),reads:AdmissionRead[]=[]
  try{
   const first=await collect(input(held,store,principal(reads,{chain:scoped})))
   const asked=first.find(e=>e.type==='approval_requested') as Extract<AssistantEvent,{type:'approval_requested'}>
   expect(asked).toBeDefined()
   const second=await collect(input(held,store,principal(reads,{chain:scoped,answer}),true,{callId:asked.callId!,approved:true}))
   expect(held.calls).toEqual([])
   expect(refused(second).map(e=>e.refusal?.name)).toEqual([name])
   // Revoked before the continuation, the check before it prepares refuses it: nothing is counted.
   if(answer===REVOKED)expect(reads.at(-1)!.call.count).toBe(false)
  }finally{await held.close()}
 })
})

const session=(hostTools:HostTool[],given?:RunPrincipal,extra:Record<string,unknown>={}):AssistantSession=>({prompt:'Parent',testPrompt:'',tools:[],callTool:vi.fn(),hostTools,signal:new AbortController().signal,allowConversation:true,interactive:true,critic:'off',model:{family:'openai-compatible',model:'small',call:async()=>{throw Error('no model call here')}},thinking:normalize('off','openai-compatible'),context:{currentRequest:{message:'Parent',input:'Parent'},...(given?{principal:given}:{}),...extra}} as AssistantSession)
async function drive(given:AssistantSession,name:string,calls=1){
 const events:AssistantEvent[]=[]
 for await(const event of managedContext(given,async function*(ready){
  const tool=ready.values.hostTools.find(t=>t.name===name)!
  for(let at=0;at<calls;at++){
   const callId=`c${at}`
   yield {type:'tool_call' as const,callId,name,args:{}}
   let text='done',isError=false
   try{await tool.execute({},ready.signal,callId)}catch(error){text=(error as Error).message;isError=true}
   yield {type:'tool_result' as const,callId,name,isError,text}
  }
  yield {type:'end' as const}
 }))events.push(event)
 return events
}

describe('a host tool with no source',()=>{
 const local=(ran:string[],effect:'pure'|'read'|'propose')=>({name:'local_lookup',description:'local_lookup',inputSchema:{type:'object'},taxonomy:{operation:effect==='propose'?'propose':effect==='pure'?'transform':'read',effect},execute:async()=>{ran.push('local_lookup');return {content:[{type:'text',text:'looked up'}]}}}) as unknown as HostTool
 for(const effect of ['pure','read','propose'] as const){
  it(`runs under a standing delegation, effect ${effect}, read as source local and never counted`,async()=>{
   const ran:string[]=[],reads:AdmissionRead[]=[]
   await drive(session([local(ran,effect)],principal(reads)),'local_lookup')
   expect(ran).toEqual(['local_lookup'])
   expect(reads.map(r=>r.call)).toEqual([{source:'local',tool:'local_lookup',effect,count:false}])
  })
  for(const [label,given,name] of [['past its expiry',{now:LATE},'expired'],['revoked',{answer:REVOKED},'revoked']] as const)it(`is refused under a delegation ${label}, effect ${effect}`,async()=>{
   const ran:string[]=[],reads:AdmissionRead[]=[]
   const events=await drive(session([local(ran,effect)],principal(reads,given)),'local_lookup')
   expect(ran).toEqual([])
   expect(events.filter(e=>e.type==='guardrail'&&e.reason==='delegation refused').map(e=>(e as {refusal?:{name:string}}).refusal?.name)).toEqual([name])
  })
 }
 it('runs as before in a run with no delegation',async()=>{
  const ran:string[]=[]
  await drive(session([local(ran,'read')]),'local_lookup')
  expect(ran).toEqual(['local_lookup'])
 })
 it("refuses a question's or a delegation's host tool never: those are the package's own interactions",async()=>{
  const reads:AdmissionRead[]=[],asked:string[]=[]
  const tool={...local([],'read'),name:'ask_person',taxonomy:{operation:'ask',effect:'pure'},interaction:'question',execute:async()=>{asked.push('ask_person');return {content:[{type:'text',text:'asked'}]}}} as unknown as HostTool
  const events=await drive(session([tool],principal(reads,{answer:REVOKED})),'ask_person')
  expect(asked).toEqual(['ask_person'])
  expect(reads).toEqual([])
  expect(events.filter(e=>e.type==='guardrail'&&e.reason==='delegation refused')).toEqual([])
 })
})

describe('a gateway mapping',()=>{
 async function declared(source:Record<string,unknown>){
  const raw={name:'read_note',description:'read_note',inputSchema:{type:'object'},taxonomy:{operation:'read',effect:'read'} as const}
  return declareSources([{...raw,execute:async()=>({content:[]}),source:{trust:'first-party',toolListDigest:await toolListDigest([raw] as never),...source}} as unknown as HostTool],[])
 }
 it('is held to a source a gateway serves, at its declaration',async()=>{
  await expect(declared({kind:'custom',id:'notes',gatewayScope:{authority:'gateway:test',source:'tickets/live'}})).rejects.toThrow(/gatewayScope is for a source a gateway serves/)
 })
})

describe('a read served without a dispatch',()=>{
 async function immutable(ran:string[]):Promise<HostTool[]>{
  const t={name:'read_note',description:'read_note',inputSchema:{type:'object'},taxonomy:{operation:'read',effect:'read'} as const}
  return [{...t,contextPolicy:{immutable:true},execute:async()=>{ran.push('read_note');return {content:[{type:'text',text:'note'}]}},source:{kind:'custom',id:'notes',trust:'first-party',toolListDigest:await toolListDigest([t] as never)}} as unknown as HostTool]
 }
 for(const [label,extra] of [['with no store',()=>({})],['on a durable store',()=>({runStore:{store:new MemoryRunStore(),id:'count-run'}})]] as const)it(`counts nothing: one dispatch, counted once, ${label}`,async()=>{
  const ran:string[]=[],reads:AdmissionRead[]=[]
  await drive(session(await immutable(ran),principal(reads),extra()),'notes__read_note',3)
  expect(ran).toEqual(['read_note'])
  expect(reads.filter(r=>r.call.count).length).toBe(1)
  expect(reads.length).toBe(4)
  // The count is the final read before the dispatch: never the first, which the caches may yet answer.
  expect(reads.map(r=>r.call.count)).toEqual([false,true,false,false])
 })
 it('refuses a cached read after a revocation, though nothing would dispatch',async()=>{
  const ran:string[]=[],reads:AdmissionRead[]=[]
  let revoked=false
  const given=principal(reads,{answer:()=>revoked?REVOKED:ADMIT})
  const tools=await immutable(ran),outcomes:string[]=[]
  for await(const event of managedContext(session(tools,given),async function*(ready){
   const tool=ready.values.hostTools.find(t=>t.name==='notes__read_note')!
   await tool.execute({},ready.signal,'c0')
   revoked=true
   await tool.execute({},ready.signal,'c1').then(()=>outcomes.push('served'),()=>outcomes.push('refused'))
   yield {type:'end' as const}
  }))void event
  expect(ran).toEqual(['read_note'])
  expect(outcomes).toEqual(['refused'])
 })
 it('refuses at the dispatch itself when the counted read does not admit it, dispatching nothing',async()=>{
  const ran:string[]=[],reads:AdmissionRead[]=[]
  const events=await drive(session(await immutable(ran),principal(reads,{answer:read=>read.call.count?{admission:'refuse',refusal:'out-of-scope',reason:'limit'}:ADMIT})),'notes__read_note')
  expect(ran).toEqual([])
  expect(reads.map(r=>r.call.count)).toEqual([false,true])
  expect(events.filter(e=>e.type==='guardrail'&&e.reason==='delegation refused').map(e=>(e as {refusal?:unknown}).refusal)).toEqual([{name:'out-of-scope',reason:'limit'}])
 })
})

describe('a call made while another waits for the person',()=>{
 it('carries the principal on its refused result, as on its call',async()=>{
  const reads:AdmissionRead[]=[],ran:string[]=[]
  const listed=[{name:'read_note',description:'read_note',inputSchema:{type:'object'},taxonomy:{operation:'read',effect:'read'} as const},{name:'write_note',description:'write_note',inputSchema:{type:'object'},taxonomy:{operation:'act',effect:'act'} as const}]
  const digest=await toolListDigest(listed as never)
  const tools=listed.map(t=>({...t,...(t.name==='write_note'?{approve:()=>'ask'}:{}),execute:async()=>{ran.push(t.name);return {content:[{type:'text',text:'done'}]}},source:{kind:'custom',id:'notes',trust:'first-party',toolListDigest:digest}})) as unknown as HostTool[]
  const events:AssistantEvent[]=[]
  let context!:{approval(name:string,args:Record<string,unknown>,callId?:string):Promise<unknown>;sources:{pending(event:{callId?:string;name:string}):string|undefined}}
  const link={attach(given:typeof context){context=given}}
  for await(const event of managedContext(session(tools,principal(reads),{runStore:{store:new MemoryRunStore(),id:'pending'},link}),async function*(){
   await context.approval('notes__write_note',{text:'a'},'call-a')
   await context.approval('notes__write_note',{text:'b'},'call-b')
   expect(context.sources.pending({callId:'call-b',name:'notes__write_note'})).toBeDefined()
   yield {type:'tool_call' as const,callId:'call-b',name:'notes__write_note',args:{text:'b'}}
   yield {type:'end' as const}
  }))events.push(event)
  const result=events.find(e=>e.type==='tool_result') as Extract<AssistantEvent,{type:'tool_result'}>&{principal?:unknown}
  const call=events.find(e=>e.type==='tool_call') as Extract<AssistantEvent,{type:'tool_call'}>&{principal?:unknown}
  expect(call.principal).toBeDefined()
  expect(result.isError).toBe(true)
  expect(result.principal).toBeDefined()
  expect(ran).toEqual([])
 })
})

describe('a durable resume under other principals',()=>{
 async function notes(ran:string[]):Promise<HostTool[]>{
  const t={name:'read_note',description:'read_note',inputSchema:{type:'object'},taxonomy:{operation:'read',effect:'read'} as const}
  return [{...t,delegation:'read',execute:async()=>{ran.push('read_note');return {content:[{type:'text',text:'note'}]}},source:{kind:'custom',id:'notes',trust:'first-party',toolListDigest:await toolListDigest([t] as never)}} as unknown as HostTool]
 }
 for(const [label,next] of [['another subject',4],['another root actor',3],['no delegation at all',undefined]] as const)it(`names principal-changed: ${label}`,async()=>{
  const store=new MemoryRunStore(),ran:string[]=[],reads:AdmissionRead[]=[],tools=await notes(ran),id='durable-resume'
  await drive(session(tools,principal(reads),{runStore:{store,id}}),'notes__read_note')
  const before=ran.length
  const events=await drive(session(tools,next===undefined?undefined:principal(reads,{chain:next}),{runStore:{store,id,resume:true}}),'notes__read_note')
  expect(ran.length).toBe(before)
  const error=events.find(e=>e.type==='error') as Extract<AssistantEvent,{type:'error'}>
  expect(error.message).toContain('principal-changed')
  expect(error.failure?.code).toBe('stale-state')
 })
 it('continues under a chain re-issued for the same subject and root actor',async()=>{
  const store=new MemoryRunStore(),ran:string[]=[],reads:AdmissionRead[]=[],tools=await notes(ran),id='durable-reissued'
  await drive(session(tools,principal(reads),{runStore:{store,id}}),'notes__read_note')
  const events=await drive(session(tools,principal(reads,{chain:2}),{runStore:{store,id,resume:true}}),'notes__read_note')
  expect(events.filter(e=>e.type==='error'&&e.message.includes('principal-changed'))).toEqual([])
 })
})

describe('a run with a delegation it has not opened',()=>{
 const input=(reads:AdmissionRead[])=>principal(reads)
 it('holds a delegation given to it, or restored in its checkpoint, and starts no child of either',async()=>{
  const reads:AdmissionRead[]=[],opened=await openPrincipal(input(reads))
  expect(holdsDelegation(undefined)).toBe(false)
  expect(holdsDelegation({})).toBe(false)
  expect(holdsDelegation({principal:input(reads)})).toBe(true)
  expect(holdsDelegation({execution:{principal:opened.record}})).toBe(true)
  expect(()=>requireOpenedPrincipal(undefined,{principal:input(reads)})).toThrow(/delegation is not open/)
  expect(()=>requireOpenedPrincipal(undefined,{execution:{principal:opened.record}})).toThrow(/delegation is not open/)
  expect(()=>requireOpenedPrincipal(opened,{principal:input(reads)})).not.toThrow()
  expect(()=>requireOpenedPrincipal(undefined,{})).not.toThrow()
 })
})

describe('a run resumed at its handoff',()=>{
 const noteTool=async(ran:string[]):Promise<HostTool>=>{
  const tool={name:'read_note',description:'read_note',inputSchema:{type:'object'},taxonomy:{operation:'read',effect:'read'} as const}
  return {...tool,delegation:'read',execute:async()=>{ran.push('read_note');return {content:[{type:'text',text:'note'}]}},source:{kind:'custom',id:'notes',trust:'first-party',toolListDigest:await toolListDigest([tool] as never)}} as unknown as HostTool
 }
 const root=(store:RunStore|undefined,id:string,run:AgentRun,tools:HostTool[],given:RunPrincipal|undefined,resume=false,extra:Record<string,unknown>={}):AgentAssistantSession=>({prompt:'Review the supplied claims.',testPrompt:'',tools:[],hostTools:tools,callTool:vi.fn(),signal:new AbortController().signal,allowConversation:true,interactive:true,critic:'off',agent:{model:'fixture',run},context:{currentRequest:{message:'Review the supplied claims.',input:'Review the supplied claims.'},...(store?{runStore:{store,id,...(resume?{resume:true}:{})}}:{}),...(given?{principal:given}:{}),...extra}})
 const collect=async(given:AgentAssistantSession)=>{const events:AssistantEvent[]=[];for await(const event of withDelegation(codex).start(given))events.push(event);return events}
 async function stopped(id:string,state:'released'|'children',store:MemoryRunStore,given:RunPrincipal,tools:HostTool[],extra:Record<string,unknown>={}){
  let armed=true
  const faulting:RunStore={durability:'memory',load:store.load.bind(store),readEvents:store.readEvents.bind(store),commit:async input=>{
   const receipt=await store.commit(input),cp=JSON.parse(new TextDecoder().decode(input.checkpoint!))
   if(armed&&cp.handoff?.state===state){armed=false;throw Error('lost acknowledgement')}
   return receipt
  }}
  const first:AgentRun=async(request,callbacks)=>{
   try{
    const tool=request.tools.find(t=>t.description.startsWith('delegate_tasks:'))!
    await callbacks.tool({id:'delegate-wire',name:tool.name,arguments:{tasks:[{title:'Check',profile:'review',objective:'Check the note.'}]}},callbacks.signal)
   }finally{callbacks.providerRelease?.acknowledge(callbacks.providerRelease.invocation)}
  }
  await collect(root(faulting,id,first,tools,given,false,extra))
  return JSON.parse(new TextDecoder().decode((await store.load(id))!.checkpoint!))
 }
 const second=(seen:string[]):AgentRun=>async(request,callbacks)=>{
  try{
   const note=request.tools.find(t=>t.description.startsWith('notes__read_note:'))
   const parent=request.tools.some(t=>t.description.startsWith('delegate_tasks:'))
   if(note&&!parent){seen.push('child');await callbacks.tool({id:'child-read',name:note.name,arguments:{}},callbacks.signal).catch(()=>undefined)}
   await callbacks.event({type:'message',id:'reply',text:'Findings.',phase:'final'})
  }finally{callbacks.providerRelease?.acknowledge(callbacks.providerRelease.invocation)}
 }
 for(const state of ['released','children'] as const){
  describe(`state ${state}, task steps on a durable store`,()=>{
   const steps={taskSteps:true}
   it('dispatches nothing once the delegation is revoked',async()=>{
    const store=new MemoryRunStore(),ran:string[]=[],seen:string[]=[],reads:AdmissionRead[]=[],tools=[await noteTool(ran)],id=`steps-${state}`
    const saved=await stopped(id,state,store,principal(reads),tools,steps)
    expect(saved.handoff?.state).toBe(state)
    await collect(root(store,id,second(seen),tools,principal(reads,{answer:REVOKED}),true,steps))
    expect(ran).toEqual([])
   },30_000)
   for(const [label,next] of [['another subject',4],['no delegation at all',undefined]] as const)it(`starts no child under ${label}`,async()=>{
    const store=new MemoryRunStore(),ran:string[]=[],seen:string[]=[],reads:AdmissionRead[]=[],tools=[await noteTool(ran)],id=`steps-${state}-${next}`
    await stopped(id,state,store,principal(reads),tools,steps)
    const events=await collect(root(store,id,second(seen),tools,next===undefined?undefined:principal(reads,{chain:next}),true,steps))
    expect(ran).toEqual([])
    expect(seen).toEqual([])
    expect(JSON.stringify(events.filter(e=>e.type==='error'))).toContain('principal-changed')
   },30_000)
  })
  for(const carried of [false,true])describe(`state ${state}, ${carried?'a host-carried checkpoint':'a durable store'}`,()=>{
   const resume=async(id:string,store:MemoryRunStore,tools:HostTool[],seen:string[],next:RunPrincipal|undefined)=>{
    if(!carried)return collect(root(store,id,second(seen),tools,next,true))
    const saved=JSON.parse(new TextDecoder().decode((await store.load(id))!.checkpoint!))
    const {durable:_durable,...execution}=saved
    return collect(root(undefined,id,second(seen),tools,next,false,{execution}))
   }
   it('dispatches nothing once the delegation is revoked, by name',async()=>{
    const store=new MemoryRunStore(),ran:string[]=[],seen:string[]=[],reads:AdmissionRead[]=[],tools=[await noteTool(ran)],id=`revoked-${state}-${carried}`
    await stopped(id,state,store,principal(reads),tools)
    expect(ran).toEqual([])
    const before=reads.length
    const events=await resume(id,store,tools,seen,principal(reads,{answer:REVOKED}))
    expect(ran).toEqual([])
    expect(events.filter(e=>e.type==='error'&&/ContinuationNotRecoverable/.test(e.message))).toEqual([])
    expect(seen).toEqual(['child'])
    expect(reads.at(-1)?.chain).toEqual(reads[0]?.chain)
    expect(reads.length).toBeGreaterThan(before)
   },30_000)
   for(const [label,next] of [['another subject',4],['no delegation at all',undefined]] as const)it(`starts no child under ${label}, and names principal-changed`,async()=>{
    const store=new MemoryRunStore(),ran:string[]=[],seen:string[]=[],reads:AdmissionRead[]=[],tools=[await noteTool(ran)],id=`other-${state}-${carried}-${next}`
    await stopped(id,state,store,principal(reads),tools)
    const events=await resume(id,store,tools,seen,next===undefined?undefined:principal(reads,{chain:next}))
    expect(ran).toEqual([])
    expect(seen).toEqual([])
    expect(JSON.stringify(events.filter(e=>e.type==='error'))).toContain('principal-changed')
   },30_000)
  })
 }
})
