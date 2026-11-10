/**
 * #344: a critic, and a delegated author-test child, rehearse a saved case through a Runtime that serves
 * experimental_evaluate with effect act: sent as a rehearsal, no approval asked, nothing recorded as an
 * effect, and the case compared. Every other acting call keeps its approved path. The twin of
 * go/adapter/reauthtests/critic_rehearsal_test.go and go/gate/rehearsal_test.go.
 */
import {describe,it,expect,beforeEach} from 'vitest'
import chains from '../../contract/vectors/principal/chains.json'
import type {Transport} from '@modelcontextprotocol/sdk/shared/transport.js'
import type {JSONRPCMessage} from '@modelcontextprotocol/sdk/types.js'
import {openAssistantConnection} from './session'
import {vercel} from './engines/vercel'
import {codex} from './engines/codex'
import {withDelegation} from './delegation'
import {MemoryRunStore,type RunStore} from './storage/run'
import {contextualSession} from './context/session'
import {REFUTATION_MARKER,type CriticCase} from './refutation'
import {inspectOutboundFrame,rehearsalArguments,allowedTools,equalFold} from './toolGate'
import {runtimeRehearsal,registerRuntimeRehearsal,forwardRuntimeRehearsal} from './storage/runtimeActionOwner'
import {AIFailureError,authoredFailure} from './failures'
import {installRuntimeActions} from './context/runtimeActions'
import type {SessionContext} from './context/session'
import {ASSISTANT_TOOLS,configureHarness} from './host'
import {testHost} from './testing/host'
import type {AdmissionAnswer,AdmissionRead,RunPrincipal} from './principal'
import {normalize} from './thinking'
import type {AssistantEvent,EngineSession,McpTool,McpToolResult} from './engine'
import type {AgentRun} from './agent'
import type {RuntimeExecutionOwner} from './runtimeActions'

const pin='sha256:'+'a'.repeat(64)
const owner=():RuntimeExecutionOwner=>({artifactDigest:pin,processId:'launch-one',projectId:'project-one',projectLabel:'Example project',sessionId:'server-session-one',principalDigest:pin,principalLabel:'Project editor',scopeDigest:pin,scopeLabel:'Evaluate example project'})
const evaluate=():McpTool=>({name:'experimental_evaluate',description:'Evaluate one unsaved draft',inputSchema:{type:'object'},_meta:{operation:'evaluate',effect:'act',experimental:true}})
const DOCUMENT={specVersion:'0.2.0-draft',id:'https://example.com/expense',title:'Expense'}
const FACTS={claim:{amount:'120.00',receiptAttached:true}}
const PROPOSAL='Ready.\n\n```json\n'+JSON.stringify({proposal:{document:DOCUMENT,unknowns:[]}})+'\n```'
const ANSWER={status:'evaluated',rehearsal:true,disposition:{handoff:{state:'none'},kind:'outcome',outcomeId:'approve',reasons:[]}}
const CASE={source:'test',id:'receipted-120',facts:FACTS,expectedDisposition:{kind:'outcome',outcomeId:'approve'}} as CriticCase
/** The call as the model writes it: rehearsal false, which the gate rewrites. */
const CALL={pack:JSON.stringify(DOCUMENT),facts:JSON.stringify(FACTS),rehearsal:false}
const REHEARSED={name:'experimental_evaluate',arguments:{pack:JSON.stringify(DOCUMENT),facts:JSON.stringify(FACTS),rehearsal:true}}
const REWROTE='the call carried rehearsal: false; it was rewritten to an own rehearsal: true before the frame left the page, so the runtime appends no audit record (runtime ADR-0018, ADR-0028)'
const ACTING='Runtime action requires an exact approved attempt. Nothing was sent.'

/** A Runtime serving an acting evaluation behind the real gate, with the approval port; every tools/call frame is kept. */
async function runtime(){
 const calls:{name:string;arguments:Record<string,unknown>}[]=[],gate:AssistantEvent[]=[]
 const transport:Transport={
  start:async()=>{},close:async()=>{},
  send:async(message:JSONRPCMessage)=>{
   const m=message as {id?:string|number;method?:string;params?:{name:string;arguments:Record<string,unknown>}}
   if(m.id===undefined)return
   let result:unknown
   if(m.method==='initialize')result={protocolVersion:'2025-06-18',capabilities:{tools:{}},serverInfo:{name:'bounded-runtime',version:'fixture'}}
   else if(m.method==='tools/list')result={tools:[evaluate()]}
   else if(m.method==='tools/call'){calls.push(structuredClone(m.params!));result={content:[{type:'text',text:JSON.stringify(ANSWER)}],structuredContent:ANSWER}}
   else throw Error('unexpected method')
   queueMicrotask(()=>transport.onmessage?.({jsonrpc:'2.0',id:m.id!,result:result as Record<string,unknown>}))
  }
 }
 const current=owner()
 const connection=openAssistantConnection({allowed:[...ASSISTANT_TOOLS],transport,onEvent:e=>gate.push(e),runtimeActions:{owner:current,current:async()=>current}})
 const ready=await connection.ready
 return {...ready,calls,gate,close:connection.close}
}
type Runtime=Awaited<ReturnType<typeof runtime>>
function sse(text?:string,tool?:{name:string;args:unknown;id?:string}):Response {
 const frame=(delta:unknown,finish_reason:string|null=null)=>'data: '+JSON.stringify({id:'fixture',object:'chat.completion.chunk',model:'fixture',choices:[{index:0,delta,finish_reason}]})+'\n\n'
 return new Response(frame({role:'assistant',content:null})+(tool?frame({tool_calls:[{index:0,id:tool.id??'call',type:'function',function:{name:tool.name,arguments:JSON.stringify(tool.args)}}]}):frame({content:text}))+frame({},tool?'tool_calls':'stop')+'data: [DONE]\n\n',{headers:{'Content-Type':'text/event-stream'}})
}
const toolTexts=(body:{messages:{role:string;content?:unknown}[]})=>body.messages.filter(m=>m.role==='tool').map(m=>typeof m.content==='string'?m.content:JSON.stringify(m.content))
/** A native run acknowledges its provider release, as the host's agent does. */
const released=(c:Parameters<AgentRun>[1])=>{if(c.providerRelease)expect(c.providerRelease.acknowledge(c.providerRelease.invocation)).toBe(true)}
async function collect(engine:{start(s:EngineSession):AsyncIterable<AssistantEvent>},session:EngineSession){const events:AssistantEvent[]=[];for await(const e of engine.start(session))events.push(e);return events}
const saved=async(store:RunStore,id:string)=>JSON.parse(new TextDecoder().decode((await store.load(id))!.checkpoint!)) as {approval?:unknown;durable?:{dispatches?:{kind:string}[]}}

/** What every rehearsal here must show: the one frame on the wire, a rehearsal; no approval asked or held; no Runtime effect in the original writer; no refusal. */
async function rehearsed(r:Runtime,events:AssistantEvent[],store:RunStore,id:string,wire:unknown){
 expect(r.calls).toEqual([wire])
 expect(r.calls[0]!.arguments.rehearsal).toBe(true)
 expect(events.filter(e=>e.type==='approval_requested'||e.type==='error')).toEqual([])
 expect([...events,...r.gate].filter(e=>e.type==='guardrail'&&e.action==='refused')).toEqual([])
 const state=await saved(store,id)
 expect(state.approval??null).toBeNull()
 expect((state.durable?.dispatches??[]).filter(d=>d.kind==='runtime')).toEqual([])
}
/** The connection's own notices, as the gate wrote them (the event stream stamps its version on the same object later). */
const guardrails=(r:Runtime)=>r.gate.filter(e=>e.type==='guardrail').map(e=>{const {version:_,...rest}=e as {version?:number};return rest})
const compared=(events:AssistantEvent[])=>{
 const critique=events.find(e=>e.type==='critique') as Extract<AssistantEvent,{type:'critique'}>|undefined
 expect(critique).toMatchObject({cases:[{case:'receipted-120',agrees:true}],incomplete:[]})
 expect(critique!.text).not.toContain('not compared')
}

function criticSession(r:Runtime,store:RunStore,native:boolean,own:boolean,received:string[]):EngineSession{
 const common={prompt:'Check the expense pack.',testPrompt:'Test the pack with the runtime.',tools:r.tools,callTool:r.callTool,hostTools:[],signal:new AbortController().signal,allowConversation:true,critic:'on' as const,criticCases:[CASE],context:{currentRequest:{message:'Check the expense pack.',input:'The expense pack.'},runStore:{store,id:'critic-rehearsal'}}}
 if(native){
  const run:AgentRun=async(request,c)=>{try{
   if(request.phase!=='critic'){await c.event({type:'message',id:'author',text:PROPOSAL,phase:'final'});return}
   const tool=request.tools.find(t=>t.description.startsWith('experimental_evaluate:'))
   if(own&&tool)received.push((await c.tool({id:'critic-call',name:tool.name,arguments:CALL},c.signal)).text)
   await c.event({type:'message',id:'critic',text:'Done.',phase:'final'})
  }finally{released(c)}}
  return {...common,agent:{model:'fixture',run}}
 }
 const call=async(_suffix:string,request:{body:string})=>{
  const body=JSON.parse(request.body) as {messages:{role:string;content?:unknown}[]}
  if(!request.body.includes(REFUTATION_MARKER))return sse(PROPOSAL)
  const answered=toolTexts(body)
  if(own&&answered.length===0)return sse(undefined,{name:'experimental_evaluate',args:CALL,id:'critic-call'})
  received.push(...answered)
  return sse('Done.')
 }
 return {...common,thinking:normalize('off','openai-compatible'),model:{family:'openai-compatible',model:'fixture',call}} as EngineSession
}

for(const native of [false,true])describe(`#344 a critic rehearses a saved case on an acting evaluation (${native?'native':'model'} entry)`,()=>{
 it('the critic’s own call: the model reads the Runtime’s answer, the frame is a rehearsal, no approval, no effect, the case compared',async()=>{
  const r=await runtime(),store=new MemoryRunStore(),received:string[]=[]
  try{
   const events=await collect(native?codex:vercel,criticSession(r,store,native,true,received))
   await rehearsed(r,events,store,'critic-rehearsal',REHEARSED)
   expect(guardrails(r)).toEqual([{type:'guardrail',tool:'experimental_evaluate',action:'rewrote',detail:REWROTE}])
   expect(received).toHaveLength(1)
   expect(received[0]).toContain('"outcomeId":"approve"')
   compared(events)
  }finally{await r.close()}
 })
 it('the harness’s comparison of the case the critic left: a rehearsal, no approval, no effect, the case compared',async()=>{
  const r=await runtime(),store=new MemoryRunStore()
  try{
   const events=await collect(native?codex:vercel,criticSession(r,store,native,false,[]))
   expect(r.calls).toHaveLength(1)
   expect(r.calls[0]!.name).toBe('experimental_evaluate')
   expect(r.calls[0]!.arguments).toMatchObject({pack:JSON.stringify(DOCUMENT),facts:JSON.stringify(FACTS),rehearsal:true})
   await rehearsed(r,events,store,'critic-rehearsal',r.calls[0])
   expect(guardrails(r)).toEqual([])
   compared(events)
  }finally{await r.close()}
 })
})

const OBJECTIVE='Rehearse the saved case and say whether it holds.'
for(const native of [false,true])describe(`#344 an author-test child rehearses on an acting evaluation (${native?'native':'model'} entry)`,()=>{
 it('the child reads the Runtime’s answer; the frame is a rehearsal; no approval, no effect; the task completes',async()=>{
  const r=await runtime(),store=new MemoryRunStore(),received:string[]=[]
  const delegation={tasks:[{title:'Run the tests',objective:OBJECTIVE,profile:'author-test'}]}
  const common={prompt:'Check the expense pack.',testPrompt:'',tools:r.tools,callTool:r.callTool,hostTools:[],signal:new AbortController().signal,allowConversation:true,interactive:true,critic:'off' as const,context:{currentRequest:{message:'Check the expense pack.',input:'The expense pack.'},runStore:{store,id:'child-rehearsal'}}}
  let session:EngineSession
  if(native){
   const run:AgentRun=async(request,c)=>{try{
    const tool=(prefix:string)=>request.tools.find(t=>t.description.startsWith(prefix+':'))
    if(request.prompt.includes('DELEGATION RESULT')){await c.event({type:'message',id:'parent',text:'Done.',phase:'final'});return}
    const delegate=tool('delegate_tasks')
    if(delegate){await c.tool({id:'delegate',name:delegate.name,arguments:delegation},c.signal);throw Error('Parent must yield')}
    const evaluation=tool('experimental_evaluate')
    if(evaluation)received.push((await c.tool({id:'child-call',name:evaluation.name,arguments:CALL},c.signal)).text)
    await c.event({type:'message',id:'child',text:'The case holds.',phase:'final'})
   }finally{released(c)}}
   session={...common,agent:{model:'fixture',run}}
  }else{
   const call=async(_suffix:string,request:{body:string})=>{
    const body=JSON.parse(request.body) as {messages:{role:string;content?:unknown}[];tools?:{function:{name:string}}[]}
    const offered=(body.tools??[]).map(t=>t.function.name)
    const answered=toolTexts(body)
    if(request.body.includes(OBJECTIVE)&&!offered.includes('delegate_tasks')){
     if(answered.length===0)return sse(undefined,{name:'experimental_evaluate',args:CALL,id:'child-call'})
     received.push(...answered);return sse('The case holds.')
    }
    return answered.length===0?sse(undefined,{name:'delegate_tasks',args:delegation,id:'delegate'}):sse('Done.')
   }
   session={...common,thinking:normalize('off','openai-compatible'),model:{family:'openai-compatible',model:'fixture',call}} as EngineSession
  }
  try{
   const events=await collect(withDelegation(native?codex:vercel),session)
   await rehearsed(r,events,store,'child-rehearsal',REHEARSED)
   expect(guardrails(r)).toEqual([{type:'guardrail',tool:'experimental_evaluate',action:'rewrote',detail:REWROTE}])
   expect(received).toHaveLength(1)
   expect(received[0]).toContain('"outcomeId":"approve"')
   const tasks=events.filter(e=>e.type==='delegated_task') as Extract<AssistantEvent,{type:'delegated_task'}>[]
   expect(tasks.at(-1)?.task).toMatchObject({profile:'author-test',state:'complete'})
  }finally{await r.close()}
 })
})

describe('#344 every other acting call keeps the approved path',()=>{
 /** The context of a session on the acting evaluation, shaped as the row says. */
 const context=async(shape:{critic?:boolean;objective?:string;subagent?:boolean;phase?:'plan';reformat?:boolean;tool?:McpTool})=>{
  const tool=shape.tool??evaluate()
  const {context}=await contextualSession({prompt:'Check.',testPrompt:'',tools:[tool],hostTools:[],callTool:async()=>({content:[]}),signal:new AbortController().signal,thinking:normalize('off','openai-compatible'),model:{family:'openai-compatible',model:'fixture',call:async()=>new Response()},critic:'off',context:{...(shape.phase?{phase:shape.phase}:{}),...(shape.objective?{objective:shape.objective}:{})},...(shape.subagent?{childDefinition:{} as never}:{})} as EngineSession)
  if(shape.critic)context.beginCriticPhase()
  if(shape.reformat)context.beginReformatPhase()
  return context
 }
 it.each([
  ['the loop’s acting evaluation',{},true],
  ['the critic’s',{critic:true},false],
  ['a delegated child’s',{objective:'Run the tests.'},false],
  ['a declared sub-agent’s',{objective:'Run the tests.',subagent:true},true],
  ['the reformat run’s',{reformat:true},true],
  ['a pure evaluation',{tool:{...evaluate(),_meta:{operation:'evaluate',effect:'pure',experimental:true}} as McpTool},false]
 ])('%s needs an approved attempt: %s',async(_label,shape,approve)=>{
  const c=await context(shape)
  expect(c.runtimeNeedsApproval('experimental_evaluate')).toBe(approve)
 })

 it('Plan mode withholds the evaluation from a critic altogether: not offered, and refused in Plan’s own words',async()=>{
  const c=await context({critic:true,phase:'plan'})
  expect(c.runtimeNeedsApproval('experimental_evaluate')).toBe(false)
  await expect(c.runtimeCall!('experimental_evaluate',CALL)).rejects.toThrow('Plan mode does not permit this operation. Review the plan and choose Start work.')
 })

 it('the rehearsal predicate holds Plan mode, a sub-agent and every other acting tool out on its own',()=>{
  // Plan mode withholds the tool before this predicate is asked (above); the predicate does not lean on that.
  const shaped=(storageRole:'author'|'critic',sessionContext:Record<string,unknown>,subagent=false,name='experimental_evaluate')=>{
   const context={storageRole,execution:{state:{}}} as unknown as SessionContext
   installRuntimeActions(context,{tools:[{...evaluate(),name}],context:sessionContext,...(subagent?{childDefinition:{}}:{})} as unknown as EngineSession,()=>({}) as never)
   return context.runtimeNeedsApproval(name)
  }
  expect([shaped('critic',{}),shaped('critic',{phase:'plan'}),shaped('author',{objective:'Run the tests.'}),shaped('author',{objective:'Run the tests.',phase:'plan'}),shaped('author',{objective:'Run the tests.'},true),shaped('author',{})]).toEqual([false,true,false,true,true,true])
  // Another tool served with effect act is no rehearsal, for the critic or a child: it keeps its approved path.
  expect([shaped('critic',{},false,'other_action'),shaped('author',{objective:'Run the tests.'},false,'other_action')]).toEqual([true,true])
 })

 it('the loop’s acting evaluation is still asked, and nothing is sent before the person decides',async()=>{
  const r=await runtime(),store=new MemoryRunStore()
  try{
   const run:AgentRun=async(request,c)=>{try{
    const tool=request.tools.find(t=>t.description.startsWith('experimental_evaluate:'))!
    await c.tool({id:'loop-call',name:tool.name,arguments:CALL},c.signal)
   }finally{released(c)}}
   const events=await collect(codex,{prompt:'Evaluate.',testPrompt:'',tools:r.tools,callTool:r.callTool,hostTools:[],signal:new AbortController().signal,allowConversation:true,critic:'off',context:{currentRequest:{message:'Evaluate.',input:'draft'},runStore:{store,id:'loop'}},agent:{model:'fixture',run}})
   expect(events.filter(e=>e.type==='approval_requested')).toHaveLength(1)
   expect(r.calls).toEqual([])
  }finally{await r.close()}
 })

 it('a critic whose session has no rehearsal channel is refused with the act’s own words, and nothing is called',async()=>{
  const r=await runtime(),store=new MemoryRunStore(),received:string[]=[]
  const seen:string[]=[]
  // The connection's callTool behind a wrapper that registered no channel: the rewrite cannot be confirmed.
  const unconfirmed=async(name:string,args:Record<string,unknown>):Promise<McpToolResult>=>{seen.push(name);return r.callTool(name,args)}
  try{
   const events=await collect(codex,{...criticSession(r,store,true,true,received),callTool:unconfirmed})
   expect(seen).toEqual([])
   expect(r.calls).toEqual([])
   expect(events.filter(e=>e.type==='guardrail'&&e.action==='refused').map(e=>(e as {detail:string}).detail)).toEqual([ACTING,ACTING])
   expect(events.filter(e=>e.type==='approval_requested')).toEqual([])
  }finally{await r.close()}
 })

 it('the connection’s channel refuses a rewrite it cannot apply, and a frame whose rewrite it cannot confirm, before anything is sent',async()=>{
  const r=await runtime()
  try{
   const rehearse=runtimeRehearsal(r.callTool)!
   const cycle:Record<string,unknown>={};cycle.self=cycle
   await expect(rehearse('experimental_evaluate',cycle)).rejects.toMatchObject({name:'GateViolation',tool:'(a frame this gate could not read)'})
   // The same failure the connection throws for any acting call that reaches it unapproved.
   const refused=await rehearse('validate',{rehearsal:true}).catch(e=>e)
   expect(refused).toBeInstanceOf(AIFailureError)
   expect(refused.failure).toEqual(authoredFailure('permission',ACTING,'tool').failure)
   expect(refused.failure.detail).toBe(ACTING)
   expect(r.calls).toEqual([])
   expect(r.gate.filter(e=>e.type==='guardrail').map(e=>(e as {action:string}).action)).toEqual(['refused'])
  }finally{await r.close()}
 })

 it('rehearsalArguments confirms only an evaluation frame that carries an own rehearsal: true',()=>{
  const allowed=allowedTools([...ASSISTANT_TOOLS])
  const frame=(params:unknown)=>({verdict:'send' as const,notice:null,frame:{jsonrpc:'2.0',id:1,method:'tools/call',params} as unknown as JSONRPCMessage})
  expect(rehearsalArguments(inspectOutboundFrame({jsonrpc:'2.0',id:1,method:'tools/call',params:{name:'experimental_evaluate',arguments:{value:7}}},allowed))).toEqual({value:7,rehearsal:true})
  for(const verdict of [
   inspectOutboundFrame({jsonrpc:'2.0',id:1,method:'tools/call',params:{name:'experimental_evaluate',arguments:[7]}} as unknown as JSONRPCMessage,allowed),
   frame({name:'experimental_evaluate',arguments:{value:7}}),
   frame({name:'experimental_evaluate',arguments:{rehearsal:false}}),
   frame({name:'experimental_evaluate',arguments:{rehearsal:'true'}}),
   frame({name:'experimental_evaluate',arguments:Object.create({rehearsal:true})}),
   frame({name:'validate',arguments:{rehearsal:true}}),
   {verdict:'send' as const,notice:null,frame:{jsonrpc:'2.0',id:1,method:'tools/list',params:{name:'experimental_evaluate',arguments:{rehearsal:true}}} as unknown as JSONRPCMessage},
   frame({name:'experimental_evaluate',arguments:{rehearsal:true,Rehearsal:false}}),
   frame({name:'experimental_evaluate',arguments:{REHEARSAL:false,rehearsal:true}}),
   frame({name:'experimental_evaluate',arguments:{'rehearſal':false,rehearsal:true}})
  ])expect(rehearsalArguments(verdict)).toBeUndefined()
  // ʀ (U+0280) does not fold to r: no twin.
  expect(rehearsalArguments(frame({name:'experimental_evaluate',arguments:{'ʀehearsal':false,rehearsal:true}}))).toEqual({'ʀehearsal':false,rehearsal:true})
 })

 it('equalFold is Go’s strings.EqualFold on the names it is asked about',()=>{
  expect(['REHEARSAL','Rehearsal','rehearſal','rehearsal','ʀehearsal','rehearsa','rehearsals','reheaßal'].map(key=>equalFold(key,'rehearsal'))).toEqual([true,true,true,true,false,false,false,false])
 })

 it('the connection refuses a rehearsal whose arguments carry a case-folded twin of rehearsal, in the act’s words, and sends nothing',async()=>{
  const r=await runtime()
  try{
   const rehearse=runtimeRehearsal(r.callTool)!
   for(const args of [{value:7,Rehearsal:false},{REHEARSAL:false,rehearsal:true},{'rehearſal':false}]){
    const refused=await rehearse('experimental_evaluate',args).catch(e=>e)
    expect(refused).toBeInstanceOf(AIFailureError)
    expect(refused.failure).toEqual(authoredFailure('permission',ACTING,'tool').failure)
   }
   expect(r.calls).toEqual([])
   expect(guardrails(r)).toEqual([])
  }finally{await r.close()}
 })

 it('an evaluation the Runtime serves as a read is no rehearsal: the critic’s and a child’s go to the session as before',async()=>{
  for(const [storageRole,sessionContext] of [['critic',{}],['author',{objective:'Run the tests.'}]] as const){
   const sent:unknown[]=[]
   const context={storageRole,execution:{state:{}}} as unknown as SessionContext
   const callTool=async(name:string,args:Record<string,unknown>):Promise<McpToolResult>=>{sent.push([name,args]);return {content:[{type:'text',text:'evaluated'}]}}
   installRuntimeActions(context,{tools:[{...evaluate(),_meta:{operation:'evaluate',effect:'read',experimental:true}}],context:sessionContext,callTool} as unknown as EngineSession,()=>({}) as never)
   expect(context.runtimeNeedsApproval('experimental_evaluate')).toBe(false)
   expect(await context.runtimeCall!('experimental_evaluate',{value:7})).toEqual({content:[{type:'text',text:'evaluated'}]})
   expect(sent).toEqual([['experimental_evaluate',{value:7}]])
  }
 })
})

describe('#344 a critic’s rehearsal is held to the run’s delegation',()=>{
 beforeEach(()=>configureHarness(testHost()))
 const scoped=chains.items.findIndex(chain=>(chain as {scope:{sources:{source:string}[]}}[]).at(-1)!.scope.sources.some(entry=>entry.source==='runtime'))
 const principal=(reads:AdmissionRead[],answer:AdmissionAnswer):RunPrincipal=>({chain:JSON.stringify(chains.items[scoped]),keys:chains.keys,software:chains.software,audience:chains.audience,clock:()=>Date.parse('2026-10-30T01:00:00.000Z'),admission:read=>{reads.push(read);return answer}})
 const held=(r:Runtime,store:RunStore,given:RunPrincipal)=>{const s=criticSession(r,store,true,true,[]);return {...s,context:{...s.context,principal:given}}}
 it('admitted: read once under source runtime, effect act, counted, immediately before the one rehearsal',async()=>{
  const r=await runtime(),store=new MemoryRunStore(),reads:AdmissionRead[]=[]
  try{
   const events=await collect(codex,held(r,store,principal(reads,{admission:'admit'})))
   await rehearsed(r,events,store,'critic-rehearsal',REHEARSED)
   expect(reads.map(read=>[read.audience,read.call.source,read.call.tool,read.call.effect,read.call.count])).toEqual([[{kind:'agent-host',name:'desk'},'runtime','experimental_evaluate','act',true]])
  }finally{await r.close()}
 })
 it('revoked: nothing is sent, and the critic’s call and the harness’s comparison are each refused in the delegation’s words',async()=>{
  const r=await runtime(),store=new MemoryRunStore(),reads:AdmissionRead[]=[]
  try{
   const events=await collect(codex,held(r,store,principal(reads,{admission:'refuse',refusal:'revoked'})))
   expect(r.calls).toEqual([])
   const refused=events.filter(e=>e.type==='guardrail'&&e.reason==='delegation refused') as Extract<AssistantEvent,{type:'guardrail'}>[]
   expect(refused.map(e=>[e.tool,e.detail,e.refusal?.name])).toEqual([0,1].map(()=>['experimental_evaluate','experimental_evaluate was not run: the delegation this run acts under does not admit it (revoked). Nothing was changed. Do not retry it; say what could not be done.','revoked']))
   expect(events.filter(e=>e.type==='approval_requested')).toEqual([])
  }finally{await r.close()}
 })
})

describe('#344 a child’s rehearsal is held to its binding’s checks',()=>{
 it('counts against the task’s tool-call budget, as its other calls do: the ninth is not made',async()=>{
  const r=await runtime(),store=new MemoryRunStore(),answers:{text:string;isError:boolean}[]=[]
  const delegation={tasks:[{title:'Run the tests',objective:OBJECTIVE,profile:'author-test'}]}
  const run:AgentRun=async(request,c)=>{try{
   const tool=(prefix:string)=>request.tools.find(t=>t.description.startsWith(prefix+':'))
   if(request.prompt.includes('DELEGATION RESULT')){await c.event({type:'message',id:'parent',text:'Done.',phase:'final'});return}
   const delegate=tool('delegate_tasks')
   if(delegate){await c.tool({id:'delegate',name:delegate.name,arguments:delegation},c.signal);throw Error('Parent must yield')}
   const evaluation=tool('experimental_evaluate')!
   for(let i=1;i<=9;i++)answers.push(await c.tool({id:`child-call-${i}`,name:evaluation.name,arguments:{...CALL,facts:JSON.stringify({claim:{amount:`${i}.00`,receiptAttached:true}})}},c.signal))
   await c.event({type:'message',id:'child',text:'Eight cases ran.',phase:'final'})
  }finally{released(c)}}
  try{
   await collect(withDelegation(codex),{prompt:'Check the expense pack.',testPrompt:'',tools:r.tools,callTool:r.callTool,hostTools:[],signal:new AbortController().signal,allowConversation:true,interactive:true,critic:'off',context:{currentRequest:{message:'Check the expense pack.',input:'The expense pack.'},runStore:{store,id:'child-budget'}},agent:{model:'fixture',run}})
   expect(r.calls).toHaveLength(8)
   expect(r.calls.every(call=>call.arguments.rehearsal===true)).toBe(true)
   expect(answers.map(a=>a.isError)).toEqual([...Array(8).fill(false),true])
   const budget='This task has made its 8 tool calls, its profile’s budget; this call was not made. Return the findings you have to the parent.'
   expect(JSON.parse(answers[8]!.text)).toEqual({isError:true,content:[{type:'text',text:budget}],structuredContent:{failure:{version:1,code:'invalid-tool-input',source:'tool',outcome:'unknown',detail:budget}}})
  }finally{await r.close()}
 })

 it('a wrapper’s rehearsal passes its own check before the inner channel, and a wrapper of a callTool with none has none',async()=>{
  const sent:string[]=[],admitted:string[]=[]
  const inner=async():Promise<McpToolResult>=>({content:[]}),wrapper=async():Promise<McpToolResult>=>({content:[]}),bare=async():Promise<McpToolResult>=>({content:[]}),outer=async():Promise<McpToolResult>=>({content:[]})
  registerRuntimeRehearsal(inner,async name=>{sent.push(name);return {content:[]}})
  expect(()=>registerRuntimeRehearsal(inner,async()=>({content:[]}))).toThrow()
  forwardRuntimeRehearsal(wrapper,inner,name=>{admitted.push(name);if(name!=='experimental_evaluate')throw Error('not offered')})
  await runtimeRehearsal(wrapper)!('experimental_evaluate',{})
  await expect(runtimeRehearsal(wrapper)!('validate',{})).rejects.toThrow('not offered')
  expect([admitted,sent]).toEqual([['experimental_evaluate','validate'],['experimental_evaluate']])
  forwardRuntimeRehearsal(outer,bare,()=>{})
  expect(runtimeRehearsal(outer)).toBeUndefined()
 })
})
