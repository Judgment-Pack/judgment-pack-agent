// @vitest-environment node
// ADR-0008 on the agent wire: after the person's decision the approved call runs once, and the invocation that continues
// the response carries the continuation block (#309); a continuation that delegates goes on past its batch (#305).
import {describe,it,expect,vi} from 'vitest'
import type {AgentAssistantSession,AssistantEvent,Engine,HostTool} from './engine'
import type {AgentRun} from './agent'
import {codex} from './engines/codex'
import {withDelegation} from './delegation'
import {MemoryRunStore,type RunStore} from './storage/run'
import {toolListDigest,APPROVAL_CONTINUATION} from './toolSources'
import {approvalShownDigest} from './execution/state'

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

describe('the invocation that continues an approved response on the agent wire (#309)',()=>{
 for(const [label,engine] of [['plain',codex],['through the coordinator',withDelegation(codex)]] as const)it(`carries the approved result, and the act runs once: ${label}`,async()=>{
  const s=scene(),decision=await paused(s,engine),before=s.prompts.length
  await drive(s,engine,s.store,true,decision)
  const continued=s.prompts.slice(before)
  expect(errors(s)).toEqual([])
  expect(s.ran).toBe(1)
  expect(continued).toHaveLength(1)
  expect(continued[0]).toContain(APPROVAL_CONTINUATION)
  expect(continued[0]).toContain(ANSWER)
  expect(continued[0]).toContain(decision.callId)
  expect(finished(s)).toBe(true)
 },30_000)
})

describe('a lost continuing invocation',()=>{
 it('is sent again as recorded, and the act still runs once',async()=>{
  const s=scene(),decision=await paused(s,codex),abort=new AbortController()
  await drive(s,codex,s.store,true,decision,[async()=>{abort.abort();throw new DOMException('The operation was aborted','AbortError')}],abort).catch(()=>undefined)
  const lost=s.prompts.at(-1)!
  expect(lost).toContain(ANSWER)
  const before=s.prompts.length
  await drive(s,codex,s.store,true,undefined)
  expect(s.ran).toBe(1)
  expect(s.prompts.slice(before)).toEqual([lost])
 },30_000)
})

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
