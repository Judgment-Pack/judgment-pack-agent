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

interface Scene{store:MemoryRunStore;asked:number;ran:number;prompts:string[];events:AssistantEvent[]}
const scene=():Scene=>({store:new MemoryRunStore(),asked:0,ran:0,prompts:[],events:[]})
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
   if(parent){const at=parents++;s.prompts.push(request.prompt);if(steps[at])await steps[at]!(request,callbacks)}
   await callbacks.event({type:'message',id:'final',text:'Done.',phase:'final'})
  }finally{callbacks.providerRelease?.acknowledge(callbacks.providerRelease.invocation)}
 }
 const session={prompt:'Write the note.',testPrompt:'',tools:[],callTool:vi.fn(async()=>({content:[]})),hostTools:[await writeTool(s)],signal:abort.signal,allowConversation:true,interactive:true,critic:'off',agent:{model:'fixture',run},context:{currentRequest:{message:'Write the note.',input:'note'},runStore:{store:on,id:'run-c',...(resume?{resume:true}:{})},...(decision?{approval:decision}:{})}} as unknown as AgentAssistantSession
 for await(const event of engine.start(session))s.events.push(event)
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
  expect(errors(s)[0]).toContain('already decided')
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
