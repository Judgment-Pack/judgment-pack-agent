import type { AgentRun } from './agent'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { withDelegation } from './delegation'
import { delegationTool, readDelegationRequest, type DelegatedTask, type DelegationRequest } from './delegationContract'
import type { AssistantEvent, AssistantSession, Engine, EngineSession, HostTool } from './engine'
import { codex } from './engines/codex'
import { vercel } from './engines/vercel'
import { normalize } from './thinking'

const task=(title='Check evidence',profile='review')=>({title,profile,objective:'Check the supplied claims.',context:'Provided company description.'})
const request:DelegationRequest={tasks:[task()]}
const apiSession=(patch:Partial<AssistantSession>={}):AssistantSession=>({prompt:'PARENT PRIVATE HISTORY',testPrompt:'',tools:[],callTool:vi.fn(),hostTools:[],signal:new AbortController().signal,allowConversation:true,interactive:true,adversarialReview:false,model:{family:'openai-compatible',model:'fixture',call:vi.fn(async()=>new Response('{}'))},thinking:normalize('off','openai-compatible'),...patch})
const collect=async(engine:Engine,session:EngineSession)=>{const events:AssistantEvent[]=[];for await(const e of engine.start(session))events.push(e);return events}
// The delegated tasks a finished run left, as the host reads them: the last state of each task, and a
// task still queued or running read as interrupted (the Desk's chat history, chat/responseHistory.ts).
const results=(events:AssistantEvent[])=>{const tasks=new Map<string,DelegatedTask>();for(const event of events)if(event.type==='delegated_task'){const task=structuredClone(event.task);if(task.state==='running'||task.state==='queued'){task.state='interrupted';task.problem='This task was interrupted. Nothing restarted automatically.';task.steps=task.steps.map(step=>step.state==='running'||step.state==='queued'?{...step,state:'interrupted' as const}:step)}tasks.set(task.id,task)}return [...tasks.values()]}
const readTool:HostTool={name:'read_context',delegation:'read',description:'Read context',inputSchema:{type:'object'},execute:vi.fn(async()=>({content:[{type:'text',text:'Saved source'}]}))}
const writeTool:HostTool={name:'save_recipe',description:'Save',inputSchema:{type:'object'},execute:vi.fn()}
afterEach(()=>vi.restoreAllMocks())

it('isolates child context and capabilities, then resumes the parent with findings',async()=>{
 const authorize=vi.fn(async()=>{})
 const sessions:EngineSession[]=[]
 const engine:Engine={id:'vercel',async *start(s){sessions.push(s)
  if(sessions.length===1){expect(s.hostTools.some(t=>t.name==='delegate_tasks')).toBe(true);yield {type:'delegation_requested',request};yield {type:'end'};return}
  if(sessions.length===2){expect(s.context?.authorize).toBe(authorize);expect(s.context?.artifactProposals).toBe(false);expect(s.prompt).not.toContain('PARENT PRIVATE HISTORY');expect(s.hostTools.map(t=>t.name)).toEqual(['read_context']);expect(s.tools).toEqual([]);expect(s.adversarialReview).toBe(false);await s.hostTools[0]!.execute({},s.signal);yield {type:'message',text:'One unsupported claim.'};yield {type:'end'};return}
  expect(s.prompt).toContain('One unsupported claim.');expect(s.prompt).toContain('PARENT PRIVATE HISTORY');expect(s.hostTools.some(t=>t.name==='delegate_tasks')).toBe(false);yield {type:'message',text:'Review complete.'};yield {type:'end'}
 }}
 const events=await collect(withDelegation(engine),apiSession({context:{authorize,artifactProposals:false},hostTools:[readTool,writeTool]}))
 expect(events.filter(e=>e.type==='end')).toHaveLength(1)
 expect(events.filter(e=>e.type==='message')).toEqual([{type:'message',text:'Review complete.'}])
 expect(results(events)[0]).toMatchObject({state:'complete',result:'One unsupported claim.',toolCalls:1,steps:[{name:'read_context',state:'complete'}]})
 expect(writeTool.execute).not.toHaveBeenCalled()
})

it('limits independent API children to two concurrent workers and retains a failed sibling',async()=>{
 let active=0,peak=0,started=0
 const engine:Engine={id:'vercel',async *start(s){
  if(s.hostTools.some(t=>t.name==='delegate_tasks')){yield {type:'delegation_requested',request:{tasks:[task('A'),task('B'),task('C')]}}}
  else if(s.prompt.includes('PARENT CONTINUATION')){expect(s.prompt).toContain('Source unavailable');yield {type:'message',text:'Use the two completed findings.'}}
  else {active++;peak=Math.max(peak,active);const index=started++;await new Promise(r=>setTimeout(r,10));active--;if(index===1)yield {type:'error',message:'Source unavailable'};else yield {type:'message',text:'Retained finding'}}
  yield {type:'end'}
 }}
 const events=await collect(withDelegation(engine),apiSession())
 expect(peak).toBe(2);expect(results(events).map(t=>t.state)).toEqual(['complete','failed','complete'])
})

it('uses one model request budget across parent, children and continuation',async()=>{
 let dispatched=0
 const engine:Engine={id:'vercel',async *start(s){if(!('model' in s))throw Error('fixture')
  if(s.hostTools.some(t=>t.name==='delegate_tasks')){for(let i=0;i<17;i++)await s.model.call('chat/completions',{body:'{}'});yield {type:'delegation_requested',request:{tasks:[task('A'),task('B'),task('C')]}}}
  else if(s.prompt.includes('PARENT CONTINUATION')){await s.model.call('chat/completions',{body:'{}'});yield {type:'message',text:'Available results only.'}}
  else {for(let i=0;i<6;i++)await s.model.call('chat/completions',{body:'{}'});yield {type:'message',text:'Done'}}
  yield {type:'end'}
 }}
 const events=await collect(withDelegation(engine),apiSession({model:{family:'openai-compatible',model:'m',call:async()=>{dispatched++;return new Response('{}')}}}))
 expect(dispatched).toBe(20);expect(results(events).every(t=>t.state==='failed')).toBe(true)
 expect(events.some(e=>e.type==='message'&&e.text==='Available results only.')).toBe(true)
})

it('stops at the child tool allowance rather than giving each child a fresh parent budget',async()=>{
 const execute=vi.fn(async()=>({content:[{type:'text' as const,text:'read'}]}))
 const engine:Engine={id:'vercel',async *start(s){
  if(s.hostTools.some(t=>t.name==='delegate_tasks'))yield {type:'delegation_requested',request}
  else if(s.prompt.includes('PARENT CONTINUATION'))yield {type:'message',text:'Limit reached.'}
  else {for(let i=0;i<10;i++)await s.hostTools[0]!.execute({},s.signal);yield {type:'message',text:'Unexpected'}}
  yield {type:'end'}
 }}
 const events=await collect(withDelegation(engine),apiSession({hostTools:[{...readTool,execute}]}))
 expect(execute).toHaveBeenCalledTimes(6);expect(results(events)[0]).toMatchObject({state:'failed',toolCalls:6})
})

it('checkpoints before dispatch and refuses to run after persistence fails',async()=>{
 let children=0
 const engine:Engine={id:'vercel',async *start(s){if(s.hostTools.some(t=>t.name==='delegate_tasks'))yield {type:'delegation_requested',request};else children++;yield {type:'end'}}}
 const events=await collect(withDelegation(engine,{checkpoint:async()=>{throw Error('Checkpoint unavailable')}}),apiSession())
 expect(children).toBe(0);expect(events.some(e=>e.type==='error'&&e.failure?.code==='storage')).toBe(true)
})

it('cancels active children, never starts queued children, and does not resume the parent',async()=>{
 const stop=new AbortController();let children=0,resumed=false
 const engine:Engine={id:'vercel',async *start(s){
  if(s.hostTools.some(t=>t.name==='delegate_tasks'))yield {type:'delegation_requested',request:{tasks:[task('A'),task('B'),task('C')]}}
  else if(s.prompt.includes('PARENT CONTINUATION'))resumed=true
  else {children++;await new Promise<void>(resolve=>s.signal.addEventListener('abort',()=>resolve(),{once:true}))}
  yield {type:'end'}
 }}
 const events:AssistantEvent[]=[]
 for await(const e of withDelegation(engine,{checkpoint:async(_task,signal)=>{signal.throwIfAborted()}}).start(apiSession({signal:stop.signal}))){events.push(e);if(e.type==='delegated_task'&&e.task.state==='running')stop.abort()}
 expect(children).toBeLessThanOrEqual(2);expect(resumed).toBe(false)
 expect(results(events).every(t=>t.state==='interrupted')).toBe(true)
})

it('returns a research permission request to the parent without subsequent child source calls',async()=>{
 const execute=vi.fn(async()=>({structuredContent:{researchSuggestion:true}}))
 const engine:Engine={id:'vercel',async *start(s){
  if(s.hostTools.some(t=>t.name==='delegate_tasks'))yield {type:'delegation_requested',request:{tasks:[task('Research','research')]}}
  else if(s.prompt.includes('PARENT CONTINUATION')){expect(s.prompt).toContain('parent’s research choice');yield {type:'message',text:'Research needs your choice.'}}
  else {await s.hostTools[0]!.execute({},s.signal);await s.hostTools[0]!.execute({},s.signal)}
  yield {type:'end'}
 }}
 const events=await collect(withDelegation(engine),apiSession({hostTools:[{...readTool,delegation:'research',execute}]}))
 expect(execute).toHaveBeenCalledTimes(1);expect(results(events)[0]?.state).toBe('failed')
})

it('pauses the real Codex adapter and runs children serially before continuing',async()=>{
 let active=0,peak=0,calls=0
 const run=vi.fn<AgentRun>(async(r,cb)=>{calls++;active++;peak=Math.max(peak,active)
  try {
   const delegate=r.tools.find((t:{description:string})=>t.description.startsWith('delegate_tasks:'))
   if(delegate) {await cb.tool({id:'call',name:delegate.name,arguments:{tasks:[task('First'),task('Second')]}},cb.signal);throw Error('Parent must yield')}
   await cb.event({type:'message',id:'reply',text:r.prompt.includes('PARENT CONTINUATION')?'Parent finished.':'Child finding.',phase:'final'})
  } finally {active--}
 })
 const {model:_model,thinking:_thinking,...base}=apiSession()
 const events=await collect(withDelegation(codex),{...base,agent:{model:'fixture',run}})
 expect(calls).toBe(4);expect(peak).toBe(1);expect(results(events)).toHaveLength(2)
 expect(events.filter(e=>e.type==='message')).toEqual([{type:'message',text:'Parent finished.'}])
})

it('retries only refused Codex admission, never an uncertain execution failure',async()=>{
 let parents=0,children=0
 const run=vi.fn<AgentRun>(async(r,cb)=>{
  const delegate=r.tools.find((t:{description:string})=>t.description.startsWith('delegate_tasks:'))
  if(delegate){parents++;await cb.tool({id:'call',name:delegate.name,arguments:{...request}},cb.signal);return}
  if(!r.prompt.includes('PARENT CONTINUATION')){children++;if(children===1)throw Object.assign(Error('busy'),{code:'busy'});throw Object.assign(Error('Lost connection'),{code:'run-failed'})}
  await cb.event({type:'message',id:'reply',text:'Child failed; no retry.',phase:'final'})
 })
 const {model:_model,thinking:_thinking,...base}=apiSession()
 const events=await collect(withDelegation(codex),{...base,agent:{model:'fixture',run}})
 expect(parents).toBe(1);expect(children).toBe(2);expect(results(events)[0]).toMatchObject({state:'failed',attempts:2,failure:{code:'unknown',source:'agent'},problem:expect.stringContaining('could not complete')})
})

function sse(text?:string,tool?:{name:string;args:unknown}):Response {
 const frame=(delta:unknown,finish_reason:string|null=null)=>'data: '+JSON.stringify({id:'fixture',object:'chat.completion.chunk',model:'fixture',choices:[{index:0,delta,finish_reason}]})+'\n\n'
 return new Response(frame({role:'assistant',content:null})+(tool?frame({tool_calls:[{index:0,id:'delegate',type:'function',function:{name:tool.name,arguments:JSON.stringify(tool.args)}}]}):frame({content:text}))+frame({},tool?'tool_calls':'stop')+'data: [DONE]\n\n',{headers:{'Content-Type':'text/event-stream'}})
}
it('supports the real Vercel SDK handoff without emitting child prose into the main conversation',async()=>{
 let calls=0
 const call=vi.fn(async()=>{calls++;return calls===1?sse(undefined,{name:'delegate_tasks',args:request}):sse(calls===2?'Child finding.':'Parent answer.')})
 const events=await collect(withDelegation(vercel),apiSession({model:{family:'openai-compatible',model:'fixture',call}}))
 expect(calls).toBe(3);expect(results(events)[0]).toMatchObject({state:'complete',result:'Child finding.',modelRequests:1})
 expect(events.filter(e=>e.type==='message')).toEqual([{type:'message',text:'Parent answer.'}])
 expect(events.filter(e=>e.type==='end')).toHaveLength(1)
})

describe('retained task history',()=>{
 it('validates bounded delegation before pausing or creating work',()=>{
  expect(()=>readDelegationRequest({tasks:Array.from({length:4},()=>task())})).toThrow()
  expect(()=>readDelegationRequest({tasks:[{...task(),context:'a'.repeat(16001)}]})).toThrow()
  expect(()=>readDelegationRequest({tasks:[{...task(),autoApprove:true}]})).toThrow()
  expect(delegationTool([]).interaction).toBe('delegation')
 })
})

it('persists the combined parent and child request count before each provider dispatch',async()=>{
 let calls=0,saved=0
 const call=vi.fn(async()=>{
  calls++;expect(saved).toBe(calls)
  return calls===1?sse(undefined,{name:'delegate_tasks',args:request}):sse(calls===2?'Child finding.':'Parent answer.')
 })
 const context={currentRequest:{message:'Review this',input:'Supplied evidence'},checkpoint:async(checkpoint:import('./execution/state').ExecutionCheckpoint)=>{saved=checkpoint.used}}
 const events:AssistantEvent[]=[]
 for await(const event of withDelegation(vercel).start(apiSession({context,model:{family:'openai-compatible',model:'fixture',call}}))){
  events.push(event)
  if(event.type==='execution_checkpoint'&&event.durable)saved=event.checkpoint.used
 }
 expect(calls).toBe(3);expect(saved).toBe(3)
 expect(events.filter(e=>e.type==='error')).toEqual([])
 expect(events.filter(e=>e.type==='execution_checkpoint').at(-1)).toMatchObject({checkpoint:{used:3,status:'complete'}})
})

it('does not dispatch a child when saving the shared request usage fails',async()=>{
 let calls=0
 const call=vi.fn(async()=>{calls++;return calls===1?sse(undefined,{name:'delegate_tasks',args:request}):sse('Unexpected child')})
 const input=apiSession({context:{currentRequest:{message:'Review this',input:'Supplied evidence'}},model:{family:'openai-compatible',model:'fixture',call}})
 await expect((async()=>{
  for await(const event of withDelegation(vercel).start(input)){
   if(event.type==='execution_checkpoint'&&event.durable&&event.checkpoint.used===2)throw Error('Checkpoint unavailable')
  }
 })()).rejects.toThrow('Checkpoint unavailable')
 expect(calls).toBe(1)
})
