// @vitest-environment node
// ADR-0008 on the agent wire: an approved continuation that delegates goes on past its batch (#305), and a decision is
// taken by the response's first open only.
import {describe,it,expect,vi} from 'vitest'
import type {AgentAssistantSession,AssistantEvent,Engine,HostTool} from './engine'
import type {AgentRun} from './agent'
import {codex} from './engines/codex'
import {withDelegation} from './delegation'
import {MemoryRunStore,type RunStore} from './storage/run'
import {toolListDigest} from './toolSources'
import {approvalShownDigest,decisionForParentOpen} from './execution/state'

const ANSWER='THE-APPROVED-ANSWER'
const WRITE={name:'write_note',description:'write_note',inputSchema:{type:'object'},taxonomy:{operation:'act',effect:'act'} as const}

interface Scene{store:MemoryRunStore;asked:number;ran:number;children:number;prompts:string[];events:AssistantEvent[];extra?:HostTool[];seen?:(event:AssistantEvent)=>Promise<void>}
const scene=():Scene=>({store:new MemoryRunStore(),asked:0,ran:0,children:0,prompts:[],events:[]})
async function writeTool(s:Scene):Promise<HostTool>{
 return {...WRITE,effectClass:'act',source:{kind:'custom',id:'writes',trust:'person',version:'1',toolListDigest:await toolListDigest([WRITE] as never)},approve:()=>{s.asked++;return s.asked===1?'ask':'allow'},execute:async()=>{s.ran++;return {content:[{type:'text',text:ANSWER}]}}} as unknown as HostTool
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
 const session={prompt:'Write the note.',testPrompt:'',tools:[],callTool:vi.fn(async()=>({content:[]})),hostTools:[await writeTool(s),...(s.extra??[])],signal:abort.signal,allowConversation:true,interactive:true,critic:'off',agent:{model:'fixture',run},context:{currentRequest:{message:'Write the note.',input:'note'},runStore:{store:on,id:'run-c',...(resume?{resume:true}:{})},...(decision?{approval:decision}:{})}} as unknown as AgentAssistantSession
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

describe('the decision a parent open is given (#305, narrowed after #312\u2019s review, MEDIUM-1)',()=>{
 const act={callId:'act',approved:true},plan={callId:'plan',approved:true}
 const taken={approval:{callId:'act',state:'answered'}},asked={approval:{callId:'act',state:'asked'}}
 for(const [label,given,execution,opened,want] of [
  ['a later open, the act\u2019s decision taken',act,taken,true,undefined],
  ['the first open: refused there if stale',act,taken,false,act],
  ['a later open, a Plan decision',plan,taken,true,plan],
  ['a later open, the call still asked',act,asked,true,act],
  ['a later open, no approval record',act,{},true,act],
  ['a later open, no checkpoint',act,undefined,true,act],
  ['no decision',undefined,taken,true,undefined],
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
   const failed=s.events.filter(e=>e.type==='error').map(e=>(e as {failure?:{source:string;outcome:string}}).failure)
   expect([count(s,'approval_requested'),s.ran,failed.map(f=>[f?.source,f?.outcome])]).toEqual([0,0,[['storage','unknown']]])
   if(!lost)expect((await head(s.store)).saved.approval).toBeUndefined()
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
