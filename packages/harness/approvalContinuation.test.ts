// @vitest-environment node
// ADR-0008 on the agent wire: an approved continuation that delegates goes on past its batch (#305), and a decision is
// taken by the response's first open only.
import {describe,it,expect,vi} from 'vitest'
import type {AgentAssistantSession,AssistantEvent,Engine,EngineSession,HostTool,McpTool} from './engine'
import type {AgentRun} from './agent'
import {codex} from './engines/codex'
import {vercel} from './engines/vercel'
import {deepagents} from './engines/deepagents'
import {answerWith} from './testing/scenarioDriver'
import {withDelegation} from './delegation'
import {MemoryRunStore,type RunStore} from './storage/run'
import {APPROVAL_CONTINUATION,toolListDigest} from './toolSources'
import {approvalShownDigest,decisionForParentOpen} from './execution/state'
import {openAssistantConnection} from './session'
import type {ExecutionLink,SessionContext} from './context/session'
import type {RuntimeExecutionOwner} from './runtimeActions'
import type {Transport} from '@modelcontextprotocol/sdk/shared/transport.js'
import type {JSONRPCMessage} from '@modelcontextprotocol/sdk/types.js'

const ANSWER='THE-APPROVED-ANSWER'
const WRITE={name:'write_note',description:'write_note',inputSchema:{type:'object'},taxonomy:{operation:'act',effect:'act'} as const}

interface Scene{store:MemoryRunStore;asked:number;ran:number;children:number;prompts:string[];events:AssistantEvent[];extra?:HostTool[];seen?:(event:AssistantEvent)=>Promise<void>
 /** Runs as the person is first asked, before the ask is saved. */approving?:()=>void;link?:ExecutionLink;runtime?:{tools:McpTool[];callTool:AgentAssistantSession['callTool']}
 /** How many calls the person is asked about (1 where unset), and context members the run adds (#309, #317). */asks?:number;context?:Record<string,unknown>}
const scene=():Scene=>({store:new MemoryRunStore(),asked:0,ran:0,children:0,prompts:[],events:[]})
async function writeTool(s:Scene):Promise<HostTool>{
 return {...WRITE,effectClass:'act',source:{kind:'custom',id:'writes',trust:'person',version:'1',toolListDigest:await toolListDigest([WRITE] as never)},approve:()=>{s.asked++;if(s.asked===1)s.approving?.();return s.asked<=(s.asks??1)?'ask':'allow'},execute:async()=>{s.ran++;return {content:[{type:'text',text:ANSWER}]}}} as unknown as HostTool
}
type Step=(request:Parameters<AgentRun>[0],callbacks:Parameters<AgentRun>[1])=>Promise<void>
const callTool=(prefix:string,args:Record<string,unknown>={}):Step=>async(request,callbacks)=>{
 const tool=request.tools.find(t=>t.description.startsWith(prefix+':'))
 if(tool)await callbacks.tool({id:'wire-'+prefix,name:tool.name,arguments:args},callbacks.signal)
}
async function drive(s:Scene,engine:Engine,on:RunStore,resume:boolean,decision:unknown,steps:Step[]=[],abort=new AbortController()):Promise<void>{
 let parents=0
 const run:AgentRun=async(request,callbacks)=>{
  try{
   // A child's request is its objective; every other request is the parent's.
   const parent=!request.prompt.startsWith('Check the note.')
   if(parent){const at=parents++;s.prompts.push(request.prompt);if(steps[at])await steps[at]!(request,callbacks)}else s.children++
   await callbacks.event({type:'message',id:'final',text:'Done.',phase:'final'})
  }finally{callbacks.providerRelease?.acknowledge(callbacks.providerRelease.invocation)}
 }
 const session={prompt:'Write the note.',testPrompt:'',tools:s.runtime?.tools??[],callTool:s.runtime?.callTool??vi.fn(async()=>({content:[]})),hostTools:[await writeTool(s),...(s.extra??[])],signal:abort.signal,allowConversation:true,interactive:true,critic:'off',agent:{model:'fixture',run},context:{...(s.link?{link:s.link}:{}),currentRequest:{message:'Write the note.',input:'note'},runStore:{store:on,id:'run-c',...(resume?{resume:true}:{})},...(decision?{approval:decision}:{}),...s.context}} as unknown as AgentAssistantSession
 for await(const event of engine.start(session)){s.events.push(event);await s.seen?.(event)}
}
async function paused(s:Scene,engine:Engine){
 await drive(s,engine,s.store,false,undefined,[callTool('writes__write_note')])
 const saved=JSON.parse(new TextDecoder().decode((await s.store.load('run-c'))!.checkpoint!))
 expect([saved.approval?.state,s.ran]).toEqual(['asked',0])
 return {callId:saved.approval.callId,approved:true,shownDigest:await approvalShownDigest(saved.approval)}
}
const errors=(s:Scene)=>s.events.filter(e=>e.type==='error').map(e=>(e as {message?:string}).message)
const finished=(s:Scene)=>s.events.some(e=>e.type==='message'&&(e as {text?:string}).text==='Done.')

describe('an approved continuation that delegates (#305)',()=>{
 it('goes on past its batch: the decision is the first open’s, not the next parent’s',async()=>{
  const s=scene(),engine=withDelegation(codex),decision=await paused(s,engine)
  s.events.length=0
  await drive(s,engine,s.store,true,decision,[callTool('delegate_tasks',{tasks:[{title:'Check',profile:'review',objective:'Check the note.'}]})])
  expect(errors(s)).toEqual([])
  expect(s.ran).toBe(1)
  expect(s.events.some(e=>e.type==='delegated_task'&&['complete','failed'].includes((e as {task:{state:string}}).task.state))).toBe(true)
  expect(finished(s)).toBe(true)
 },30_000)
})

describe('a decision given again (#312\u2019s review, LOW-3)',()=>{
 for(const [label,engine] of [['plain',codex],['through the coordinator',withDelegation(codex)]] as const)it(`is refused for a call already decided: ${label}`,async()=>{
  const s=scene(),decision=await paused(s,engine)
  await drive(s,engine,s.store,true,decision)
  expect([s.ran,errors(s)]).toEqual([1,[]])
  s.events.length=0
  await drive(s,engine,s.store,true,decision)
  expect(s.ran).toBe(1)
  expect(errors(s)).toHaveLength(1)
  // #328: the decision's continuation finished the run, so the head is complete and the resume is refused RunComplete before
  // the decision is read: nothing runs again.
  expect(errors(s)[0]).toContain('RunComplete')
 },30_000)
})

describe('the decision a parent open is given (#305, narrowed after #312\u2019s review, MEDIUM-1; #317)',()=>{
 const act={callId:'act',approved:true},plan={callId:'plan',approved:true}
 const taken={approval:{callId:'act',state:'answered'}},asked={approval:{callId:'act',state:'asked'}}
 // #317: the Plan decision taken (planState.decisions holds its call id), and a later Plan pause of that call id.
 const planTaken={planState:{decisions:[{callId:'plan'}]}},planAgain={planState:{decisions:[{callId:'plan'}],pending:{callId:'plan'}}}
 for(const [label,given,execution,opened,want] of [
  ['a later open, the act\u2019s decision taken',act,taken,true,undefined],
  ['the first open: refused there if stale',act,taken,false,act],
  ['a later open, a Plan decision',plan,taken,true,plan],
  ['a later open, the call still asked',act,asked,true,act],
  ['a later open, no approval record',act,{},true,act],
  ['a later open, no checkpoint',act,undefined,true,act],
  ['no decision',undefined,taken,true,undefined],
  ['a later open, the Plan decision taken',plan,planTaken,true,undefined],
  ['the first open, the Plan decision taken: refused there if stale',plan,planTaken,false,plan],
  ['a later open, another call\u2019s Plan decision taken',act,planTaken,true,act],
  ['a later open, a Plan pause of that call id waits',plan,planAgain,true,plan],
 ] as const)it(label,()=>{expect(decisionForParentOpen(given as never,execution as never,opened)).toBe(want)})
})

// agent#342: an agent that answers the engine's own pause with its signal's reason, not the tool's error, still ends the run
// paused with the ask saved, and the ask is saved paused before it is shown. A storage failure and the caller's own abort end as
// they did.
const stoppedBySignal=(prefix:string,args:Record<string,unknown>={}):Step=>async(request,callbacks)=>{
 const tool=request.tools.find(t=>t.description.startsWith(prefix+':'))
 if(!tool)return
 try{await callbacks.tool({id:'wire-'+prefix,name:tool.name,arguments:args},callbacks.signal)}catch(error){
  if(!callbacks.signal.aborted)throw error
  throw callbacks.signal.reason
 }
}
const hosts=[['the tool’s error',callTool],['its signal’s reason',stoppedBySignal]] as const
const QUESTION={title:'Note',question:'Which note should be written?',selection:'single',options:[{id:'a',label:'The first',description:'One.'},{id:'b',label:'The second',description:'Two.'}]}
const askPerson={name:'ask_person',description:'ask_person',inputSchema:{type:'object'},taxonomy:{operation:'ask',effect:'pure'},interaction:'question',execute:async()=>({content:[{type:'text',text:'asked'}]})} as unknown as HostTool
const head=async(store:MemoryRunStore)=>{const saved=JSON.parse(new TextDecoder().decode((await store.load('run-c'))!.checkpoint!));return {saved,held:`${saved.status}/${saved.approval?.state??'none'}`}}
const count=(s:Scene,type:AssistantEvent['type'])=>s.events.filter(e=>e.type===type).length
const failures=(s:Scene)=>s.events.filter(e=>e.type==='error').map(e=>(e as {failure?:{code:string;source:string;outcome:string}}).failure).map(f=>[f?.code,f?.source,f?.outcome])
// A Runtime action's ask, as runtimeActions.test.ts serves one: a bounded Runtime over a transport, its owner current.
async function runtimeAction(){
 const pin='sha256:'+'a'.repeat(64)
 const owner:RuntimeExecutionOwner={artifactDigest:pin,processId:'launch-one',projectId:'project-one',projectLabel:'Example project',sessionId:'server-session-one',principalDigest:pin,principalLabel:'Project editor',scopeDigest:pin,scopeLabel:'Evaluate example project'}
 const tools:McpTool[]=[{name:'experimental_evaluate',description:'Evaluate one unsaved draft',inputSchema:{type:'object',properties:{value:{type:'integer'},rehearsal:{type:'boolean'}}},_meta:{operation:'evaluate',effect:'act',experimental:true}}]
 const calls:unknown[]=[]
 const transport:Transport={start:async()=>{},close:async()=>{},send:async(message:JSONRPCMessage)=>{
  const m=message as {id?:string|number;method?:string;params?:unknown}
  if(m.id===undefined)return
  const result=m.method==='initialize'?{protocolVersion:'2025-06-18',capabilities:{tools:{}},serverInfo:{name:'bounded-runtime',version:'fixture'}}:m.method==='tools/list'?{tools}:(calls.push(m.params),{content:[{type:'text',text:'evaluated draft'}],structuredContent:{status:'ok',auditAppended:false}})
  queueMicrotask(()=>transport.onmessage?.({jsonrpc:'2.0',id:m.id!,result}))
 }}
 const connection=openAssistantConnection({allowed:['experimental_evaluate'],transport,onEvent:()=>{},runtimeActions:{owner,current:async()=>owner}})
 return {...await connection.ready,calls,close:connection.close}
}
// Fails the commit of a checkpoint holding an asked approval: refused, nothing stored, or stored with its acknowledgement lost.
const faulty=(memory:MemoryRunStore,lost:boolean):RunStore=>({durability:'memory',load:memory.load.bind(memory),readEvents:memory.readEvents.bind(memory),commit:async request=>{
 const asked=JSON.parse(new TextDecoder().decode(request.checkpoint!)).approval?.state==='asked'
 if(asked&&!lost)throw Error('refused asked commit')
 const receipt=await memory.commit(request)
 if(asked)throw Error('lost pause acknowledgement')
 return receipt
}})

describe('an ask the engine’s own pause stopped (agent#342)',()=>{
 for(const [label,host] of hosts){
  it(`is saved paused before it is shown, and its decision runs the call once: ${label}`,async()=>{
   const s=scene();let shown=''
   s.seen=async event=>{if(event.type==='approval_requested')shown=(await head(s.store)).held}
   await drive(s,codex,s.store,false,undefined,[host('writes__write_note')])
   const {saved,held}=await head(s.store)
   expect([shown,held,count(s,'approval_requested'),errors(s),s.ran]).toEqual(['paused/asked','paused/asked',1,[],0])
   const decision={callId:saved.approval.callId,approved:true,shownDigest:await approvalShownDigest(saved.approval)}
   s.events.length=0;s.seen=undefined
   await drive(s,codex,s.store,true,decision)
   expect([s.ran,errors(s),finished(s)]).toEqual([1,[],true])
   await drive(s,codex,s.store,true,decision)
   expect(s.ran).toBe(1)
  },30_000)
  it(`a question ends the run asked, with its end stored: ${label}`,async()=>{
   const s=scene();s.extra=[askPerson]
   await drive(s,codex,s.store,false,undefined,[host('ask_person',QUESTION)])
   expect([(await head(s.store)).saved.status,count(s,'user_question'),errors(s),s.ran]).toEqual(['complete',1,[],0])
  },30_000)
  for(const lost of [false,true])it(`that could not be saved is not shown, and the run ends with the storage failure: ${label}, ${lost?'acknowledgement lost':'refused'}`,async()=>{
   const s=scene()
   await drive(s,codex,faulty(s.store,lost),false,undefined,[host('writes__write_note')])
   // unknown/storage/unknown, as a Runtime action's unsaved ask ends, in both languages.
   expect([count(s,'approval_requested'),s.ran,failures(s)]).toEqual([0,0,[['unknown','storage','unknown']]])
   if(!lost)expect((await head(s.store)).saved.approval).toBeUndefined()
  },30_000)
  for(const lost of [false,true])it(`a Runtime action’s ask that could not be saved is not shown, and the run ends with the storage failure: ${label}, ${lost?'acknowledgement lost':'refused'}`,async()=>{
   const s=scene(),runtime=await runtimeAction();s.runtime=runtime
   try{
    await drive(s,codex,faulty(s.store,lost),false,undefined,[host('experimental_evaluate',{value:7,rehearsal:false})])
    const {saved}=await head(s.store)
    expect([count(s,'approval_requested'),runtime.calls,failures(s),saved.status==='complete']).toEqual([0,[],[['unknown','storage','unknown']],false])
    if(!lost)expect(saved.approval).toBeUndefined()
   }finally{await runtime.close()}
  },30_000)
  it(`stopped by its host closing dispatch before the ask is saved is not shown: ${label}`,async()=>{
   // A host's stop closes dispatch before it aborts the run (RunWriter.closeDispatch): the ask comes in that window, the
   // caller's signal still live, and cannot be saved.
   const s=scene();let attached:SessionContext|undefined
   s.link={attach:context=>{attached=context}}
   s.approving=()=>{const binding=(attached as unknown as {storedBinding:{writer:{closeDispatch(binding:unknown):void}}}).storedBinding;binding.writer.closeDispatch(binding)}
   await drive(s,codex,s.store,false,undefined,[host('writes__write_note')])
   const {saved}=await head(s.store)
   expect([count(s,'approval_requested'),s.ran,saved.status==='complete',saved.approval]).toEqual([0,0,false,undefined])
  },30_000)
  for(const at of ['before the call','while the ask is saved','once the person is shown it'])it(`stopped by the caller’s own abort ${at} is not read as the pause: ${label}`,async()=>{
   const s=scene(),abort=new AbortController()
   const steps:Step[]=[at==='before the call'?async(request,callbacks)=>{abort.abort();await host('writes__write_note')(request,callbacks)}:host('writes__write_note')]
   // Ends the caller's run once it has stored the ask.
   const store:RunStore=at!=='while the ask is saved'?s.store:{durability:'memory',load:s.store.load.bind(s.store),readEvents:s.store.readEvents.bind(s.store),commit:async request=>{
    const receipt=await s.store.commit(request)
    if(JSON.parse(new TextDecoder().decode(request.checkpoint!)).approval?.state==='asked')abort.abort()
    return receipt
   }}
   if(at==='once the person is shown it')s.seen=async event=>{if(event.type==='approval_requested')abort.abort()}
   await drive(s,codex,store,false,undefined,steps,abort)
   const {saved}=await head(s.store)
   // Stopped, as before, never read as the pause: nothing ran, and an ask the caller stopped before it was shown is not shown.
   expect([s.ran,errors(s),saved.status==='complete',count(s,'end')]).toEqual([0,[],false,0])
   if(at!=='once the person is shown it')expect(count(s,'approval_requested')).toBe(0)
   if(at==='before the call')expect(saved.approval).toBeUndefined()
  },30_000)
 }
 it('a continuation that delegates, its agent answering the handoff with its signal’s reason, runs the child once (#312)',async()=>{
  const s=scene(),engine=withDelegation(codex)
  await drive(s,engine,s.store,false,undefined,[stoppedBySignal('writes__write_note')])
  const {saved,held}=await head(s.store)
  expect([held,s.ran]).toEqual(['paused/asked',0])
  const decision={callId:saved.approval.callId,approved:true,shownDigest:await approvalShownDigest(saved.approval)}
  s.events.length=0
  await drive(s,engine,s.store,true,decision,[stoppedBySignal('delegate_tasks',{tasks:[{title:'Check',profile:'review',objective:'Check the note.'}]})])
  const ended=s.events.filter(e=>e.type==='delegated_task'&&['complete','failed'].includes((e as {task:{state:string}}).task.state)).length
  expect([s.ran,s.children,ended,errors(s),finished(s)]).toEqual([1,1,1,[],true])
 },30_000)
})

// agent#349: the model engines, as the codex engine above. A Runtime action's ask whose save was refused, or whose acknowledgement
// was lost, is not shown, the Runtime is not called, no request follows, and the run ends unknown/storage/unknown: the writer
// cannot tell whether the save landed. Before, both modes reported storage/storage/not-started on vercel (the next request's
// checkpoint refused), and unknown/model/unknown on deepagents.
describe('a Runtime action\u2019s ask that could not be saved, on the model engines (agent#349)',()=>{
 for(const [label,engine] of [['vercel',vercel],['deepagents',deepagents]] as const)for(const lost of [false,true])it(`is not shown, and the run ends with the storage failure: ${label}, ${lost?'acknowledgement lost':'refused'}`,async()=>{
  const s=scene(),runtime=await runtimeAction();let requests=0
  const call=async()=>answerWith(requests++===0?{call:{tool:'experimental_evaluate',arguments:{value:7,rehearsal:false}}}:{say:'Evaluated.'},'stream',requests-1)
  const session={prompt:'Evaluate the draft.',testPrompt:'',tools:runtime.tools,callTool:runtime.callTool,hostTools:[],signal:new AbortController().signal,allowConversation:true,critic:'off',context:{currentRequest:{message:'Evaluate the draft.',input:'draft'},runStore:{store:faulty(s.store,lost),id:'run-c'}},model:{family:'openai-compatible',model:'fixture',call}} as unknown as EngineSession
  try{
   for await(const event of engine.start(session))s.events.push(event)
   const {saved,held}=await head(s.store)
   expect([count(s,'approval_requested'),runtime.calls,requests,failures(s),saved.status==='complete']).toEqual([0,[],1,[['unknown','storage','unknown']],false])
   // A refused save stored no ask; a lost acknowledgement's did land, and the run still ends with the failure.
   if(lost)expect(held).toBe('paused/asked');else expect(saved.approval).toBeUndefined()
  }finally{await runtime.close()}
 },30_000)
})

// #309 on the durable agent wire: the invocation that continues a decided approval tells the model the call's result, whether
// it is the first request after the decision, a lost one sent again, or a fresh one after a takeover; the act runs once; a
// call id that repeats is told apart; a delegated child is never run twice (#312's HIGH-1); and a Plan decision's
// continuation goes on (#317, #312's MEDIUM-B).
// A process that dies on the store's receipt of the first commit whose checkpoint holds what `at` says, or where `kill` is
// called: that commit is kept, nothing written after it lands, and the run's own signal stops it. `revive` is the next process.
function dying(memory:MemoryRunStore){
 const held={dead:false,at:undefined as ((saved:Record<string,any>)=>boolean)|undefined,abort:undefined as AbortController|undefined}
 const kill=()=>{held.dead=true;held.abort?.abort()}
 const store:RunStore={durability:'memory',load:memory.load.bind(memory),readEvents:memory.readEvents.bind(memory),commit:async request=>{
  if(held.dead)throw Error('the process died here')
  const receipt=await memory.commit(request)
  if(held.at?.(JSON.parse(new TextDecoder().decode(request.checkpoint!)))){kill();throw Error('the process died here')}
  return receipt
 }}
 return {store,held,kill,revive:()=>{held.dead=false;held.at=undefined;held.abort=undefined}}
}
const told=(prompt:string|undefined,approval:{decidedAt?:string}|undefined)=>!!prompt&&!!approval?.decidedAt&&prompt.includes(APPROVAL_CONTINUATION)&&prompt.includes(ANSWER)&&prompt.includes(`"decidedAt":"${approval.decidedAt}"`)
const envelope={id:'host',revision:'sha256:'+'d'.repeat(64),unit:'provider-invocations',limit:6}
const forms=[
 ['plain',codex,{}],['through the coordinator',withDelegation(codex),{}],
 ['plain, no current request',codex,{currentRequest:undefined}],['through the coordinator, no current request',withDelegation(codex),{currentRequest:undefined}],
 ['plain, task steps',codex,{taskSteps:true,taskStepEnvelope:envelope}],['through the coordinator, task steps',withDelegation(codex),{taskSteps:true}],
] as const
const delegateOnce=callTool('delegate_tasks',{tasks:[{title:'Check',profile:'review',objective:'Check the note.'}]})

describe('the approved call\u2019s result reaches the model (#309)',()=>{
 for(const [label,engine,context] of forms)it(`in the continuation\u2019s first request: ${label}`,async()=>{
  const s=scene();s.context={...context}
  const decision=await paused(s,engine)
  s.events.length=0
  await drive(s,engine,s.store,true,decision)
  const {saved}=await head(s.store)
  expect([s.ran,errors(s),finished(s),s.prompts.length,told(s.prompts[1],saved.approval)]).toEqual([1,[],true,2,true])
 },30_000)
 for(const [label,engine,context] of forms)it(`a continuation lost after it was sent is sent again as recorded, or refused under task steps: ${label}`,async()=>{
  const s=scene();s.context={...context}
  const decision=await paused(s,engine),dead=dying(s.store),abort=new AbortController();dead.held.abort=abort
  s.events.length=0
  await drive(s,engine,dead.store,true,decision,[async()=>{dead.kill();throw Error('the process died here')}],abort).catch(()=>undefined)
  expect([dead.held.dead,s.ran,finished(s),s.prompts.length]).toEqual([true,1,false,2])
  dead.revive();s.events.length=0
  await drive(s,engine,dead.store,true,undefined).catch(()=>undefined)
  if('taskSteps' in context){
   expect([s.ran,finished(s),s.prompts.length]).toEqual([1,false,2])
   expect(errors(s)).toHaveLength(1)
   expect(errors(s)[0]).toContain('HostOutcomeUnknown')
   return
  }
  expect([s.ran,errors(s),finished(s),s.prompts.length,s.prompts[2]===s.prompts[1]]).toEqual([1,[],true,3,true])
 },30_000)
 for(const [label,engine,context] of forms.slice(0,4))it(`a takeover once the approval is answered still tells the model (#312\u2019s MEDIUM-2): ${label}`,async()=>{
  const s=scene();s.context={...context}
  const decision=await paused(s,engine),dead=dying(s.store),abort=new AbortController();dead.held.abort=abort
  dead.held.at=saved=>saved.approval?.state==='answered'
  s.events.length=0
  await drive(s,engine,dead.store,true,decision,[],abort).catch(()=>undefined)
  expect([dead.held.dead,s.ran]).toEqual([true,1])
  const sent=s.prompts.length
  dead.revive();s.events.length=0
  await drive(s,engine,dead.store,true,undefined)
  const {saved}=await head(s.store)
  expect([s.ran,errors(s),finished(s),s.prompts.length,told(s.prompts[sent],saved.approval)]).toEqual([1,[],true,sent+1,true])
 },30_000)
 for(const [label,engine] of forms.slice(0,2))it(`a call id that repeats is told apart (#312\u2019s LOW-1): ${label}`,async()=>{
  const s=scene();s.asks=2
  const first=await paused(s,engine)
  await drive(s,engine,s.store,true,first,[callTool('writes__write_note',{again:true})])
  const again=(await head(s.store)).saved.approval
  expect([again?.state,again?.callId,s.ran]).toEqual(['asked',first.callId,1])
  // Decisions are dated to the millisecond: the second is taken in a later one.
  await new Promise(resolve=>setTimeout(resolve,5))
  s.events.length=0
  await drive(s,engine,s.store,true,{callId:again.callId,approved:true,shownDigest:await approvalShownDigest(again)})
  const {saved}=await head(s.store)
  expect([s.ran,errors(s),finished(s),s.prompts.length,s.prompts[2]!==s.prompts[1],told(s.prompts[2],saved.approval)]).toEqual([2,[],true,3,true,true])
 },30_000)
 it('a takeover after the batch\u2019s answer runs the child once (#312\u2019s HIGH-1)',async()=>{
  const s=scene(),engine=withDelegation(codex)
  const decision=await paused(s,engine),dead=dying(s.store),abort=new AbortController();dead.held.abort=abort
  dead.held.at=saved=>saved.handoff?.state==='answered'
  s.events.length=0
  await drive(s,engine,dead.store,true,decision,[delegateOnce],abort).catch(()=>undefined)
  expect([dead.held.dead,s.ran,s.children]).toEqual([true,1,1])
  const sent=s.prompts.length
  dead.revive();s.events.length=0
  await drive(s,engine,dead.store,true,undefined)
  expect([s.ran,s.children,errors(s),finished(s),s.prompts.length,s.prompts[sent]?.includes(APPROVAL_CONTINUATION),s.prompts[sent]?.includes('Done.')]).toEqual([1,1,[],true,sent+1,false,true])
 },30_000)
})

const accepted={id:'original',revision:1,objective:'Work',steps:['Inspect','Report'],questions:[],references:[],status:'accepted',createdAt:'2026-01-01T00:00:00.000Z',acceptedAt:'2026-01-01T00:00:00.000Z'}
const planPause=(budget:boolean):Step=>callTool('update_plan',{explanation:'Remove accepted work',plan:[{id:'step-2',step:'Report',status:'pending',...(budget?{budget:{requests:1}}:{})}]})

describe('a Plan decision\u2019s continuation goes on (#317, #312\u2019s MEDIUM-B)',()=>{
 for(const takeover of [false,true])it(`one that delegates, through the coordinator under task steps${takeover?', taken over once the batch\u2019s answer is durable':''}`,async()=>{
  const s=scene(),engine=withDelegation(codex);s.context={taskSteps:true,plan:structuredClone(accepted)}
  await drive(s,engine,s.store,false,undefined,[planPause(true)])
  const pending=(await head(s.store)).saved.planState?.pending
  expect(pending?.callId).toBeDefined()
  const dead=dying(s.store),abort=new AbortController();dead.held.abort=abort
  if(takeover)dead.held.at=saved=>saved.handoff?.state==='answered'
  s.events.length=0
  await drive(s,engine,dead.store,true,{callId:pending.callId,approved:true},[delegateOnce],abort).catch(()=>undefined)
  if(takeover){
   expect([dead.held.dead,s.children]).toEqual([true,1])
   dead.revive();s.events.length=0
   await drive(s,engine,dead.store,true,undefined)
  }
  expect([s.children,errors(s),finished(s)]).toEqual([1,[],true])
 },30_000)
 for(const [label,engine] of forms.slice(0,2))it(`one after an approval earlier in the response: ${label}`,async()=>{
  const s=scene();s.context={taskSteps:true,...(label==='plain'?{taskStepEnvelope:envelope}:{}),plan:structuredClone(accepted)}
  const decision=await paused(s,engine)
  s.events.length=0
  await drive(s,engine,s.store,true,decision,[planPause(true)])
  const pending=(await head(s.store)).saved.planState?.pending
  expect([s.ran,s.prompts.length,pending?.callId===undefined]).toEqual([1,2,false])
  s.events.length=0
  await drive(s,engine,s.store,true,{callId:pending.callId,approved:true})
  // The invocation that ended at the Plan pause carried the approval's block; the Plan continuation is a fresh request,
  // and the decision is still the response's newest continuation, so that request carries its block again (#312's MEDIUM-2).
  const {saved}=await head(s.store)
  expect([s.ran,errors(s),finished(s),s.prompts.length,told(s.prompts[2],saved.approval)]).toEqual([1,[],true,3,true])
 },30_000) // The safety side of the native Plan exemption: the Plan continuation lost after it was sent carries the response's newest
 // continuation (the batch's answer, or the approval's block again), so its effect is unknown. The takeover refuses
 // HostOutcomeUnknown and sends nothing, rather than passing over it and running the continuation twice.
 it('one lost after it was sent, after a handoff earlier in the response, is refused and nothing is sent',async()=>{
  const s=scene(),engine=withDelegation(codex);s.context={taskSteps:true,plan:structuredClone(accepted)}
  await drive(s,engine,s.store,false,undefined,[delegateOnce,planPause(true)])
  const pending=(await head(s.store)).saved.planState?.pending
  expect([s.children,pending?.callId!==undefined,errors(s)]).toEqual([1,true,[]])
  const dead=dying(s.store),abort=new AbortController();dead.held.abort=abort
  s.events.length=0
  const before=s.prompts.length
  await drive(s,engine,dead.store,true,{callId:pending.callId,approved:true},[async()=>{dead.kill();throw Error('the process died here')}],abort).catch(()=>undefined)
  expect([dead.held.dead,s.prompts.length]).toEqual([true,before+1])
  dead.revive();s.events.length=0
  const sent=s.prompts.length
  await drive(s,engine,dead.store,true,undefined).catch(()=>undefined)
  expect([s.prompts.length-sent,errors(s).length,String(errors(s)[0]).includes('HostOutcomeUnknown'),s.children]).toEqual([0,1,true,1])
 },60_000)
 for(const [label,engine,context] of [['plain, task steps',codex,{taskSteps:true,taskStepEnvelope:envelope}],['through the coordinator, task steps',withDelegation(codex),{taskSteps:true}]] as const)it(`one lost after it was sent, after an approval earlier in the response, is refused and nothing is sent: ${label}`,async()=>{
  const s=scene();s.context={...context,plan:structuredClone(accepted)}
  const decision=await paused(s,engine)
  s.events.length=0
  await drive(s,engine,s.store,true,decision,[planPause(true)])
  const pending=(await head(s.store)).saved.planState?.pending
  expect([s.ran,pending?.callId!==undefined]).toEqual([1,true])
  const dead=dying(s.store),abort=new AbortController();dead.held.abort=abort
  s.events.length=0
  const before=s.prompts.length
  await drive(s,engine,dead.store,true,{callId:pending.callId,approved:true},[async()=>{dead.kill();throw Error('the process died here')}],abort).catch(()=>undefined)
  const {saved}=await head(s.store)
  expect([dead.held.dead,s.prompts.length,told(s.prompts[before],saved.approval)]).toEqual([true,before+1,true])
  dead.revive();s.events.length=0
  const sent=s.prompts.length
  await drive(s,engine,dead.store,true,undefined).catch(()=>undefined)
  expect([s.prompts.length-sent,errors(s).length,String(errors(s)[0]).includes('HostOutcomeUnknown'),s.ran]).toEqual([0,1,true,1])
 },60_000)
})
