import type { AgentRun } from './agent'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { RESULT_LIMIT, findingsHead, withDelegation } from './delegation'
import { AUTHOR_TEST_TOOLS, DELEGATE_TOOL, DELEGATION_INSTRUCTIONS, MAX_BATCHES, MAX_TOOLS, TASK_BUDGET_CEILING, TASK_PROFILES, delegationTool, profileBudget, readDelegationRequest, readProfiles, validDelegatedTask, type DelegatedTask, type DelegationRequest } from './delegationContract'
import type { AssistantEvent, AssistantSession, Engine, EngineSession, HostTool, McpToolResult } from './engine'
import { ASSISTANT_TOOLS } from './host'
import { ChatContext } from './context/chat'
import { codex } from './engines/codex'
import { vercel } from './engines/vercel'
import { normalize } from './thinking'
import { gatedRuntime, runtimeAnswer, TEST_CASES_TOOL, TEST_CONFORMANCE_TOOL, type RuntimeCall } from './testing/scripted'
import { RECORDED_TOOLS } from './certification/scriptedServer'

const task=(title='Check evidence',profile='review')=>({title,profile,objective:'Check the supplied claims.'})
const request:DelegationRequest={tasks:[task()]}
const apiSession=(patch:Partial<AssistantSession>={}):AssistantSession=>({prompt:'PARENT PRIVATE HISTORY',testPrompt:'',tools:[],callTool:vi.fn(),hostTools:[],signal:new AbortController().signal,allowConversation:true,interactive:true,adversarialReview:false,model:{family:'openai-compatible',model:'fixture',call:vi.fn(async()=>new Response('{}'))},thinking:normalize('off','openai-compatible'),...patch})
const collect=async(engine:Engine,session:EngineSession)=>{const events:AssistantEvent[]=[];for await(const e of engine.start(session))events.push(e);return events}
// The delegated tasks a finished run left, as the host reads them: the last state of each task, and a
// task still queued or running read as interrupted (the Desk's chat history, chat/responseHistory.ts).
const results=(events:AssistantEvent[])=>{const tasks=new Map<string,DelegatedTask>();for(const event of events)if(event.type==='delegated_task'){const task=structuredClone(event.task);if(task.state==='running'||task.state==='queued'){task.state='interrupted';task.problem='This task was interrupted. Nothing restarted automatically.';task.steps=task.steps.map(step=>step.state==='running'||step.state==='queued'?{...step,state:'interrupted' as const}:step)}tasks.set(task.id,task)}return [...tasks.values()]}
const readTool:HostTool={name:'read_context',delegation:'read',description:'Read context',inputSchema:{type:'object'},execute:vi.fn(async()=>({content:[{type:'text',text:'Saved source'}]}))}
const writeTool:HostTool={name:'save_recipe',description:'Save',inputSchema:{type:'object'},execute:vi.fn()}
/** The delegation tool a parent run is offered, or undefined in a child's. */
const delegateIn=(s:EngineSession)=>s.hostTools.find(t=>t.name===DELEGATE_TOOL)
/** What a batch answered the parent: its tasks. */
type Answered={tasks:{id:string;state:string;result?:string;problem?:string;more?:{readWith:string;id:string;cursor:string;totalCharacters:number};summarized?:boolean;budget?:unknown;used:{modelRequests:number;toolCalls:number}}[];note:string}
const answered=(result:McpToolResult)=>result.structuredContent as Answered
afterEach(()=>{vi.restoreAllMocks();vi.unstubAllGlobals();vi.useRealTimers()})

// agent#14 rewrote this case: the parent is one run, and the findings are its delegate call's result
// rather than a continuation's prompt.
it('isolates child context and capabilities, then returns the findings to the parent as the call’s result',async()=>{
 const authorize=vi.fn(async()=>{})
 const sessions:EngineSession[]=[]
 const engine:Engine={id:'vercel',async *start(s){sessions.push(s)
  if(delegateIn(s)){const result=answered(await delegateIn(s)!.execute({...request},s.signal));expect(result.tasks[0]).toMatchObject({state:'complete',result:'One unsupported claim.'});yield {type:'message',text:'Review complete.'};yield {type:'end'};return}
  expect(s.context?.authorize).toBe(authorize);expect(s.context?.artifactProposals).toBe(false);expect(s.prompt).not.toContain('PARENT PRIVATE HISTORY');expect(s.hostTools.map(t=>t.name)).toEqual(['read_context']);expect(s.tools).toEqual([]);expect(s.adversarialReview).toBe(false);await s.hostTools[0]!.execute({},s.signal);yield {type:'message',text:'One unsupported claim.'};yield {type:'end'}
 }}
 const events=await collect(withDelegation(engine),apiSession({context:{authorize,artifactProposals:false},hostTools:[readTool,writeTool]}))
 expect(sessions.filter(delegateIn)).toHaveLength(1)
 expect(events.filter(e=>e.type==='end')).toHaveLength(1)
 expect(events.filter(e=>e.type==='message')).toEqual([{type:'message',text:'Review complete.'}])
 expect(results(events)[0]).toMatchObject({state:'complete',result:'One unsupported claim.',toolCalls:1,steps:[{name:'read_context',state:'complete'}]})
 expect(writeTool.execute).not.toHaveBeenCalled()
})

// agent#14: the failed sibling reaches the parent in the call's result.
it('limits independent API children to two concurrent workers and retains a failed sibling',async()=>{
 let active=0,peak=0,started=0
 const engine:Engine={id:'vercel',async *start(s){
  if(delegateIn(s)){const result=answered(await delegateIn(s)!.execute({tasks:[task('A'),task('B'),task('C')]},s.signal));expect(JSON.stringify(result)).toContain('Source unavailable');yield {type:'message',text:'Use the two completed findings.'}}
  else {active++;peak=Math.max(peak,active);const index=started++;await new Promise(r=>setTimeout(r,10));active--;if(index===1)yield {type:'error',message:'Source unavailable'};else yield {type:'message',text:'Retained finding'}}
  yield {type:'end'}
 }}
 const events=await collect(withDelegation(engine),apiSession())
 expect(peak).toBe(2);expect(results(events).map(t=>t.state)).toEqual(['complete','failed','complete'])
})

// agent#14: the parent's last request follows the children in the same run.
it('uses one model request budget across the parent and its children, and keeps the last request for the parent',async()=>{
 let dispatched=0
 const engine:Engine={id:'vercel',async *start(s){if(!('model' in s))throw Error('fixture')
  if(delegateIn(s)){for(let i=0;i<17;i++)await s.model.call('chat/completions',{body:'{}'});await delegateIn(s)!.execute({tasks:[task('A'),task('B'),task('C')]},s.signal);await s.model.call('chat/completions',{body:'{}'});yield {type:'message',text:'Available results only.'}}
  else {for(let i=0;i<6;i++)await s.model.call('chat/completions',{body:'{}'});yield {type:'message',text:'Done'}}
  yield {type:'end'}
 }}
 const events=await collect(withDelegation(engine),apiSession({model:{family:'openai-compatible',model:'m',call:async()=>{dispatched++;return new Response('{}')}}}))
 expect(dispatched).toBe(20);expect(results(events).every(t=>t.state==='failed')).toBe(true)
 expect(events.some(e=>e.type==='message'&&e.text==='Available results only.')).toBe(true)
})

// agent#15: the allowance is the review profile's budget, six tool calls.
it('stops at the child tool allowance rather than giving each child a fresh parent budget',async()=>{
 const execute=vi.fn(async()=>({content:[{type:'text' as const,text:'read'}]}))
 const engine:Engine={id:'vercel',async *start(s){
  if(delegateIn(s)){await delegateIn(s)!.execute({...request},s.signal);yield {type:'message',text:'Limit reached.'}}
  else {for(let i=0;i<10;i++)await s.hostTools[0]!.execute({},s.signal);yield {type:'message',text:'Unexpected'}}
  yield {type:'end'}
 }}
 const events=await collect(withDelegation(engine),apiSession({hostTools:[{...readTool,execute}]}))
 expect(execute).toHaveBeenCalledTimes(6);expect(results(events)[0]).toMatchObject({state:'failed',toolCalls:6,budget:profileBudget(TASK_PROFILES[1]!)})
})

// agent#14: the parent's call fails with the storage failure, and the response ends with it.
it('checkpoints before dispatch and refuses to run after persistence fails',async()=>{
 let children=0
 const engine:Engine={id:'vercel',async *start(s){if(delegateIn(s))await delegateIn(s)!.execute({...request},s.signal);else children++;yield {type:'end'}}}
 const events=await collect(withDelegation(engine,{checkpoint:async()=>{throw Error('Checkpoint unavailable')}}),apiSession())
 expect(children).toBe(0);expect(events.some(e=>e.type==='error'&&e.failure?.code==='storage')).toBe(true)
})

// agent#14: a stopped call returns no findings, so the parent run goes no further.
it('cancels active children, never starts queued children, and does not resume the parent',async()=>{
 const stop=new AbortController();let children=0,resumed=false
 const engine:Engine={id:'vercel',async *start(s){
  if(delegateIn(s)){await delegateIn(s)!.execute({tasks:[task('A'),task('B'),task('C')]},s.signal);resumed=true}
  else {children++;await new Promise<void>(resolve=>s.signal.addEventListener('abort',()=>resolve(),{once:true}))}
  yield {type:'end'}
 }}
 const events:AssistantEvent[]=[]
 for await(const e of withDelegation(engine,{checkpoint:async(_task,signal)=>{signal.throwIfAborted()}}).start(apiSession({signal:stop.signal}))){events.push(e);if(e.type==='delegated_task'&&e.task.state==='running')stop.abort()}
 expect(children).toBeLessThanOrEqual(2);expect(resumed).toBe(false)
 expect(results(events).every(t=>t.state==='interrupted')).toBe(true)
})

// agent#14: the hand-back reaches the parent in the call's result.
it('returns a research permission request to the parent without subsequent child source calls',async()=>{
 const execute=vi.fn(async()=>({structuredContent:{researchSuggestion:true}}))
 const engine:Engine={id:'vercel',async *start(s){
  if(delegateIn(s)){const result=answered(await delegateIn(s)!.execute({tasks:[task('Research','research')]},s.signal));expect(result.tasks[0]!.problem).toContain('parent’s research choice');yield {type:'message',text:'Research needs your choice.'}}
  else {await s.hostTools[0]!.execute({},s.signal);await s.hostTools[0]!.execute({},s.signal)}
  yield {type:'end'}
 }}
 const events=await collect(withDelegation(engine),apiSession({hostTools:[{...readTool,delegation:'research',execute}]}))
 expect(execute).toHaveBeenCalledTimes(1);expect(results(events)[0]?.state).toBe('failed')
})

// agent#14: Codex keeps the restart, and its next run is told the call's result and nothing else.
it('pauses the real Codex adapter and runs children serially before continuing',async()=>{
 let active=0,peak=0,calls=0
 const prompts:string[]=[]
 const run=vi.fn<AgentRun>(async(r,cb)=>{calls++;active++;peak=Math.max(peak,active);prompts.push(r.prompt)
  try {
   const delegate=r.tools.find((t:{description:string})=>t.description.startsWith('delegate_tasks:'))
   if(delegate&&!r.prompt.includes('DELEGATION RESULT')) {await cb.tool({id:'call',name:delegate.name,arguments:{tasks:[task('First'),task('Second')]}},cb.signal);throw Error('Parent must yield')}
   await cb.event({type:'message',id:'reply',text:r.prompt.includes('DELEGATION RESULT')?'Parent finished.':'Child finding.',phase:'final'})
  } finally {active--}
 })
 const {model:_model,thinking:_thinking,...base}=apiSession()
 const events=await collect(withDelegation(codex),{...base,agent:{model:'fixture',run}})
 expect(calls).toBe(4);expect(peak).toBe(1);expect(results(events)).toHaveLength(2)
 expect(events.filter(e=>e.type==='message')).toEqual([{type:'message',text:'Parent finished.'}])
 // The continuation carries the findings, as the call's result, and no copy of the parent's earlier work.
 const continuation=prompts.at(-1)!
 expect(continuation).toContain('Child finding.');expect(continuation).not.toContain('PARENT CONTINUATION');expect(continuation).not.toContain('priorParentWork')
})

it('retries only refused Codex admission, never an uncertain execution failure',async()=>{
 let parents=0,children=0
 const run=vi.fn<AgentRun>(async(r,cb)=>{
  const delegate=r.tools.find((t:{description:string})=>t.description.startsWith('delegate_tasks:'))
  if(delegate&&!r.prompt.includes('DELEGATION RESULT')){parents++;await cb.tool({id:'call',name:delegate.name,arguments:{...request}},cb.signal);return}
  if(!r.prompt.includes('DELEGATION RESULT')){children++;if(children===1)throw Object.assign(Error('busy'),{code:'busy'});throw Object.assign(Error('Lost connection'),{code:'run-failed'})}
  await cb.event({type:'message',id:'reply',text:'Child failed; no retry.',phase:'final'})
 })
 const {model:_model,thinking:_thinking,...base}=apiSession()
 const events=await collect(withDelegation(codex),{...base,agent:{model:'fixture',run}})
 expect(parents).toBe(1);expect(children).toBe(2);expect(results(events)[0]).toMatchObject({state:'failed',attempts:2,failure:{code:'unknown',source:'agent'},problem:expect.stringContaining('could not complete')})
})

function sse(text?:string,tool?:{name:string;args:unknown;id?:string}):Response {
 const frame=(delta:unknown,finish_reason:string|null=null)=>'data: '+JSON.stringify({id:'fixture',object:'chat.completion.chunk',model:'fixture',choices:[{index:0,delta,finish_reason}]})+'\n\n'
 return new Response(frame({role:'assistant',content:null})+(tool?frame({tool_calls:[{index:0,id:tool.id??'delegate',type:'function',function:{name:tool.name,arguments:JSON.stringify(tool.args)}}]}):frame({content:text}))+frame({},tool?'tool_calls':'stop')+'data: [DONE]\n\n',{headers:{'Content-Type':'text/event-stream'}})
}
// agent#14: the handoff is an awaited call now; the shape asserted is the same.
it('supports the real Vercel SDK delegation without emitting child prose into the main conversation',async()=>{
 let calls=0
 const call=vi.fn(async()=>{calls++;return calls===1?sse(undefined,{name:'delegate_tasks',args:request}):sse(calls===2?'Child finding.':'Parent answer.')})
 const events=await collect(withDelegation(vercel),apiSession({model:{family:'openai-compatible',model:'fixture',call}}))
 expect(calls).toBe(3);expect(results(events)[0]).toMatchObject({state:'complete',result:'Child finding.',modelRequests:1})
 expect(events.filter(e=>e.type==='message')).toEqual([{type:'message',text:'Parent answer.'}])
 expect(events.filter(e=>e.type==='end')).toHaveLength(1)
})

describe('retained task history',()=>{
 // agent#15 changed what is refused: a copied context block of any size, more than twenty references.
 it('validates bounded delegation before pausing or creating work',()=>{
  expect(()=>readDelegationRequest({tasks:Array.from({length:4},()=>task())})).toThrow()
  expect(()=>readDelegationRequest({tasks:[{...task(),context:'Provided company description.'}]})).toThrow()
  expect(()=>readDelegationRequest({tasks:[{...task(),references:Array.from({length:21},(_,i)=>`doc-${i}`)}]})).toThrow()
  expect(()=>readDelegationRequest({tasks:[{...task(),resume:'not-a-task-id'}]})).toThrow()
  expect(()=>readDelegationRequest({tasks:[{...task(),autoApprove:true}]})).toThrow()
  expect(readDelegationRequest({tasks:[{...task(),references:['policy'],summarize:true}]}).tasks[0]!.references).toEqual(['policy'])
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

/** One request as the scripted model received it. */
interface Sent {number:number;body:{model?:string;messages:{role:string;content?:unknown;tool_calls?:{id:string;function:{name:string;arguments:string}}[];tool_call_id?:string}[];tools?:{function:{name:string}}[]}}
/** A model capability that answers each request with `answer`, and keeps what it was sent. */
const scripted=(answer:(sent:Sent)=>Response|Promise<Response>)=>{
 const sent:Sent[]=[]
 const call=vi.fn(async(_suffix:string,request:{body:string})=>{const one={number:sent.length+1,body:JSON.parse(request.body) as Sent['body']};sent.push(one);return answer(one)})
 return {sent,call}
}
/** Every text part of a request's messages, in order. */
const texts=(one:Sent)=>one.body.messages.flatMap(m=>typeof m.content==='string'?[m.content]:Array.isArray(m.content)?(m.content as {text?:string}[]).map(part=>part.text??''):[])
/** The first text part of a request's first user message: the request text. */
const first=(one:Sent)=>{const message=one.body.messages.find(m=>m.role==='user');return typeof message?.content==='string'?message.content:String((message?.content as {text?:string}[]|undefined)?.[0]?.text??'')}
/** The text parts of a request's first user message. */
const opening=(one:Sent)=>{const message=one.body.messages.find(m=>m.role==='user');return typeof message?.content==='string'?[message.content]:((message?.content as {text?:string}[]|undefined)??[]).map(part=>part.text??'')}
/** A child's prompt: the part of its first message after its objective (agent#11) that carries its rules. */
const promptOf=(one:Sent)=>opening(one).find(text=>text.includes('Complete this read-only task'))??''
const isChild=(one:Sent)=>promptOf(one)!==''
/** The brief a child was sent, read back from the JSON line its prompt ends with. */
const briefOf=(one:Sent)=>{const text=promptOf(one);const at=text.lastIndexOf('\n{"objective"');return JSON.parse(text.slice(at+1).split('\n')[0]!) as {objective:string;references?:{id:string;label:string}[];earlierWork?:{objective:string;result:string}[]}}
const toolMessages=(one:Sent)=>one.body.messages.filter(m=>m.role==='tool').map(m=>String(m.content))
/** The value inside one framed tool message (framing.ts): the line after the opening, up to the closing line. */
const framed=(text:string)=>JSON.parse(text.slice(text.indexOf('\n')+1,text.lastIndexOf('\n'))) as {structuredContent:Answered}
/** How many requests of `sent` belong to the one parent run: its first message is the same text. */
const parentRequests=(sent:Sent[])=>sent.filter(one=>!isChild(one))

describe('one parent run (agent#14)',()=>{
 it('is one run that keeps its native tool messages: the findings come back as the delegate call’s tool result',async()=>{
  const {sent,call}=scripted(one=>isChild(one)?sse('Child finding: the claim has no source.'):toolMessages(one).length===0?sse(undefined,{name:DELEGATE_TOOL,args:request,id:'call_batch'}):sse('Parent answer.'))
  const starts:EngineSession[]=[]
  const counted:Engine={id:'vercel',start(s){starts.push(s);return vercel.start(s)}}
  const events=await collect(withDelegation(counted),apiSession({model:{family:'openai-compatible',model:'fixture',call}}))
  // One parent run and one child run: the parent was never started again.
  expect(starts.filter(delegateIn)).toHaveLength(1)
  expect(starts).toHaveLength(2)
  const parent=parentRequests(sent)
  expect(parent).toHaveLength(2)
  // The second request is the same run's next step: the same first message, the model's own call,
  // and the call's result as a native tool message carrying the findings.
  expect(first(parent[1]!)).toBe(first(parent[0]!))
  const assistant=parent[1]!.body.messages.find(m=>m.role==='assistant')!
  expect(assistant.tool_calls?.map(c=>[c.id,c.function.name])).toEqual([['call_batch',DELEGATE_TOOL]])
  const result=parent[1]!.body.messages.find(m=>m.role==='tool')!
  expect(result.tool_call_id).toBe('call_batch')
  expect(String(result.content)).toContain('BEGIN RETRIEVED DATA')
  expect(String(result.content)).toContain('Child finding: the claim has no source.')
  expect(JSON.stringify(sent)).not.toContain('PARENT CONTINUATION')
  // The stream records the call and its result like any other.
  expect(events.filter(e=>e.type==='tool_call').map(e=>(e as {name:string}).name)).toEqual([DELEGATE_TOOL])
  const said=events.find(e=>e.type==='tool_result'&&e.name===DELEGATE_TOOL) as Extract<AssistantEvent,{type:'tool_result'}>
  expect(said.isError).toBe(false)
  expect((said.structured as Answered).tasks[0]).toMatchObject({state:'complete',result:'Child finding: the claim has no source.'})
  expect(events.filter(e=>e.type==='message')).toEqual([{type:'message',text:'Parent answer.'}])
  expect(events.some(e=>e.type==='delegation_requested')).toBe(false)
 })

 it('runs a second batch in the same response',async()=>{
  const {sent,call}=scripted(one=>{
   if(isChild(one))return sse(briefOf(one).objective.includes('second')?'Second finding.':'First finding.')
   const done=toolMessages(one).length
   if(done===0)return sse(undefined,{name:DELEGATE_TOOL,args:{tasks:[{title:'One',objective:'Do the first check.',profile:'analyze'}]},id:'call_one'})
   if(done===1)return sse(undefined,{name:DELEGATE_TOOL,args:{tasks:[{title:'Two',objective:'Do the second check.',profile:'analyze'}]},id:'call_two'})
   return sse('Both checks are done.')
  })
  const events=await collect(withDelegation(vercel),apiSession({model:{family:'openai-compatible',model:'fixture',call}}))
  expect(results(events).map(t=>[t.title,t.state,t.result])).toEqual([['One','complete','First finding.'],['Two','complete','Second finding.']])
  const last=parentRequests(sent).at(-1)!
  expect(toolMessages(last)).toHaveLength(2)
  expect(toolMessages(last)[1]).toContain('Second finding.')
  expect(events.filter(e=>e.type==='message')).toEqual([{type:'message',text:'Both checks are done.'}])
  expect(events.filter(e=>e.type==='error')).toEqual([])
 })

 it('refuses a batch past the response’s last one, and the run goes on',async()=>{
  let batches=0
  const engine:Engine={id:'vercel',async *start(s){
   if(delegateIn(s)){
    for(let i=0;i<MAX_BATCHES;i++){await delegateIn(s)!.execute({tasks:[task(`Batch ${i}`,'analyze')]},s.signal);batches++}
    await expect(delegateIn(s)!.execute({tasks:[task('One more','analyze')]},s.signal)).rejects.toThrow(`its ${MAX_BATCHES} delegation batches`)
    yield {type:'message',text:'Done.'}
   }else yield {type:'message',text:'Finding.'}
   yield {type:'end'}
  }}
  const events=await collect(withDelegation(engine),apiSession())
  expect(batches).toBe(MAX_BATCHES);expect(results(events)).toHaveLength(MAX_BATCHES)
  expect(events.filter(e=>e.type==='message')).toEqual([{type:'message',text:'Done.'}])
 })

 it('resumes a finished child in a later response of the chat with its earlier objective and findings',async()=>{
  const chat=new ChatContext()
  // First response: one child answers.
  const one=scripted(sent=>isChild(sent)?sse('Finding A: the policy caps meals at 40.'):toolMessages(sent).length===0?sse(undefined,{name:DELEGATE_TOOL,args:{tasks:[{title:'Meals',objective:'Find the meal cap.',profile:'analyze'}]}}):sse('Noted.'))
  const firstEvents=await collect(withDelegation(vercel),apiSession({context:{chat},model:{family:'openai-compatible',model:'fixture',call:one.call}}))
  const earlier=results(firstEvents)[0]!
  expect(earlier.state).toBe('complete')
  // Second response: the parent resumes it with a follow-up.
  const two=scripted(sent=>isChild(sent)?sse('Following up on Finding A: the cap applies per day.'):toolMessages(sent).length===0?sse(undefined,{name:DELEGATE_TOOL,args:{tasks:[{title:'Meals, again',objective:'Is the cap per day or per trip?',profile:'analyze',resume:earlier.id}]}}):sse('Per day.'))
  const secondEvents=await collect(withDelegation(vercel),apiSession({context:{chat},model:{family:'openai-compatible',model:'fixture',call:two.call}}))
  const resumed=two.sent.find(isChild)!
  const brief=briefOf(resumed)
  expect(brief.objective).toBe('Is the cap per day or per trip?')
  expect(brief.earlierWork).toEqual([{objective:'Find the meal cap.',result:'Finding A: the policy caps meals at 40.'}])
  expect(promptOf(resumed)).toContain('You are continuing this task')
  // agent#11: the request text of the resumed child is its new objective.
  expect(first(resumed)).toBe('Is the cap per day or per trip?')
  expect(results(secondEvents)[0]).toMatchObject({state:'complete',resumes:earlier.id,result:'Following up on Finding A: the cap applies per day.'})
  // An id this chat never finished is refused before any task starts.
  const three=scripted(sent=>toolMessages(sent).length===0?sse(undefined,{name:DELEGATE_TOOL,args:{tasks:[{title:'Unknown',objective:'Go on.',profile:'analyze',resume:crypto.randomUUID()}]}}):sse('Could not resume.'))
  const thirdEvents=await collect(withDelegation(vercel),apiSession({context:{chat},model:{family:'openai-compatible',model:'fixture',call:three.call}}))
  expect(results(thirdEvents)).toEqual([])
  expect(toolMessages(three.sent.at(-1)!)[0]).toContain('no finished task')
 })
})

describe('context by reference and profiles (agent#15)',()=>{
 const POLICY='MILEAGE-RATE-0.58: mileage is paid at 0.58 per mile, with receipts for tolls.'
 it('gives a child a parent document by reference: the brief names it, the child reads it with read_window, and no copy is in the brief',async()=>{
  const chat=new ChatContext()
  let read=''
  const {sent,call}=scripted(one=>{
   if(isChild(one)){
    const done=toolMessages(one)
    if(done.length===0){const ref=briefOf(one).references![0]!;return sse(undefined,{name:'read_window',args:{id:ref.id},id:'call_read'})}
    read=done[0]!;return sse('The rate is 0.58 per mile.')
   }
   return toolMessages(one).length===0?sse(undefined,{name:DELEGATE_TOOL,args:{tasks:[{title:'Rate',objective:'Find the mileage rate.',profile:'analyze',references:['policy']}]}}):sse('0.58 per mile.')
  })
  const events=await collect(withDelegation(vercel),apiSession({context:{chat,documents:[{id:'policy',kind:'artifact',label:'Mileage policy',value:POLICY}]},model:{family:'openai-compatible',model:'fixture',call}}))
  const child=sent.find(isChild)!
  const brief=briefOf(child)
  expect(brief.references).toEqual([expect.objectContaining({id:expect.stringMatching(new RegExp(`^${chat.scope}:policy:[0-9a-f]{16}$`)),label:'Mileage policy'})])
  // The brief and the child's first request carry the id, not the text.
  expect(JSON.stringify(child.body)).not.toContain('MILEAGE-RATE-0.58')
  expect(read).toContain('MILEAGE-RATE-0.58')
  expect(results(events)[0]).toMatchObject({state:'complete',result:'The rate is 0.58 per mile.',references:[brief.references![0]!.id]})
 })

 it('refuses a reference the parent’s context does not hold, and one held by another chat',async()=>{
  const other=new ChatContext(),chat=new ChatContext()
  // The other chat holds a policy document; this one does not.
  const held=await collect(withDelegation(vercel),apiSession({context:{chat:other,documents:[{id:'policy',kind:'artifact',label:'Mileage policy',value:POLICY}]},model:{family:'openai-compatible',model:'fixture',call:scripted(()=>sse('Nothing to do.')).call}}))
  expect(held.filter(e=>e.type==='error')).toEqual([])
  const otherId=[...other.entries.keys()].find(id=>id.includes(':policy:'))!
  const {sent,call}=scripted(one=>toolMessages(one).length===0?sse(undefined,{name:DELEGATE_TOOL,args:{tasks:[{title:'Rate',objective:'Find the rate.',profile:'analyze',references:[otherId,'policy']}]}}):sse('I could not delegate that.'))
  const events=await collect(withDelegation(vercel),apiSession({context:{chat},model:{family:'openai-compatible',model:'fixture',call}}))
  expect(sent.some(isChild)).toBe(false);expect(results(events)).toEqual([])
  expect(toolMessages(sent.at(-1)!)[0]).toContain('Not in your context')
 })

 it('declares every profile’s model, budget and tools, and puts the budget on each task’s record',async()=>{
  expect(TASK_PROFILES.map(p=>[p.id,p.model,p.budget,p.capabilities,p.runtimeTools??[]])).toEqual([
   ['analyze','child',{modelRequests:6,toolCalls:6,milliseconds:120_000},[],[]],
   ['review','child',{modelRequests:6,toolCalls:6,milliseconds:120_000},['read'],[]],
   ['research','child',{modelRequests:10,toolCalls:12,milliseconds:360_000},['read','research'],[]],
   ['author-test','parent',{modelRequests:8,toolCalls:8,milliseconds:240_000},[],[...ASSISTANT_TOOLS]]
  ])
  expect(readProfiles(TASK_PROFILES)).toBe(TASK_PROFILES)
  // A feature adapter may add a profile, never a larger allowance or a tool off the gate's ceiling.
  expect(()=>readProfiles([...TASK_PROFILES,{id:'wide',description:'d',instructions:'i',capabilities:[],budget:{...TASK_BUDGET_CEILING,toolCalls:TASK_BUDGET_CEILING.toolCalls+1}}])).toThrow()
  expect(()=>readProfiles([...TASK_PROFILES,{id:'files',description:'d',instructions:'i',capabilities:[],runtimeTools:['get_pack']}])).toThrow()
  expect(()=>readProfiles([...TASK_PROFILES,{id:'mine',description:'d',instructions:'i',capabilities:['read']}])).not.toThrow()
  const engine:Engine={id:'vercel',async *start(s){
   if(delegateIn(s))await delegateIn(s)!.execute({tasks:TASK_PROFILES.slice(0,3).map(p=>task(p.id,p.id))},s.signal)
   else yield {type:'message',text:'Finding.'}
   if(delegateIn(s))await delegateIn(s)!.execute({tasks:[task('author-test','author-test')]},s.signal)
   yield {type:'end'}
  }}
  const events=await collect(withDelegation(engine),apiSession({model:{family:'openai-compatible',model:'fixture',call:vi.fn(),capabilities:{childModel:'fixture-small'}}}))
  const records=results(events)
  expect(records.map(t=>[t.profile,t.budget,t.model])).toEqual(TASK_PROFILES.map(p=>[p.id,profileBudget(p),p.model==='parent'?'fixture':'fixture-small']))
  for(const event of events)if(event.type==='delegated_task')expect(validDelegatedTask(event.task)).toBe(true)
  // The parent is told each profile's limits where it chooses one.
  const offered=JSON.stringify(delegationTool(TASK_PROFILES).inputSchema)
  expect(offered).toContain('research: Research one focused question using the parent’s permitted source tools. Up to 10 model requests, 12 tool calls, 360 seconds.')
 })

 it('runs a child on the connection’s child model, and an author-and-test child on the parent’s',async()=>{
  const {sent,call}=scripted(one=>isChild(one)?sse('Finding.'):toolMessages(one).length===0?sse(undefined,{name:DELEGATE_TOOL,args:{tasks:[task('Cheap','review'),task('Tests','author-test')]}}):sse('Done.'))
  await collect(withDelegation(vercel),apiSession({model:{family:'openai-compatible',model:'fixture',call,capabilities:{childModel:'fixture-small'}}}))
  const children=sent.filter(isChild).map(one=>[briefOf(one).objective,one.body.model])
  expect(children.map(([,model])=>model).sort()).toEqual(['fixture','fixture-small'])
  expect(parentRequests(sent).every(one=>one.body.model==='fixture')).toBe(true)
 })

 it('lets a research child finish six page reads within its own time limit and budget',async()=>{
  vi.useFakeTimers({toFake:['setTimeout','clearTimeout','Date']})
  // Each page read takes thirty seconds, as a read through the Gateway may: six are three minutes,
  // past the two minutes a child had before (agent#15).
  // The read moves the clock on by its thirty seconds, so every timer the run holds (the child's time
  // limit, the batch's, the call's deadline) sees the time the reads take, and nothing else moves it.
  const pages=vi.fn(async(args:Record<string,unknown>)=>{await vi.advanceTimersByTimeAsync(30_000);return {content:[{type:'text' as const,text:`Page ${String(args.url)}: the rate is 0.58.`}]}})
  const readPage:HostTool={name:'read_page',delegation:'research',description:'Read one page.',inputSchema:{type:'object',properties:{url:{type:'string'}},required:['url']},execute:pages}
  const {call}=scripted(one=>{
   if(isChild(one)){const done=toolMessages(one).length;return done<6?sse(undefined,{name:'read_page',args:{url:`https://example.test/${done}`},id:`call_page_${done}`}):sse('Six pages agree: 0.58 per mile.')}
   return toolMessages(one).length===0?sse(undefined,{name:DELEGATE_TOOL,args:{tasks:[{title:'Rate',objective:'Read the six pages.',profile:'research'}]}}):sse('0.58.')
  })
  const started=Date.now()
  const events=await collect(withDelegation(vercel),apiSession({hostTools:[readPage],model:{family:'openai-compatible',model:'fixture',call}}))
  expect(pages).toHaveBeenCalledTimes(6)
  expect(Date.now()-started).toBeGreaterThanOrEqual(180_000)
  expect(results(events)[0]).toMatchObject({state:'complete',result:'Six pages agree: 0.58 per mile.',modelRequests:7,toolCalls:6,budget:{modelRequests:10,toolCalls:12,milliseconds:360_000}})
 })

 it('offers an author-and-test child the gate’s Runtime tools and nothing else, runs the pack’s tests through the gate, and shares the parent’s read cache',async()=>{
  const gateEvents:AssistantEvent[]=[]
  const runtime=await gatedRuntime({tools:[...RECORDED_TOOLS,TEST_CASES_TOOL,TEST_CONFORMANCE_TOOL],allowed:[...ASSISTANT_TOOLS],
   handle:(c:RuntimeCall)=>c.name==='experimental_test_cases'?runtimeAnswer({status:'failed',cases:[{id:'c1',passed:false,why:'expected decided, got refused'}]}):runtimeAnswer({schema:'judgment-pack',version:'0.4'}),onEvent:e=>gateEvents.push(e)})
  const PACK=JSON.stringify({specVersion:'0.4',id:'expense'}),MATRIX=JSON.stringify({matrixVersion:'3',cases:[{id:'c1'}]})
  const readPack:HostTool={name:'read_pack',delegation:'read',description:'Read the saved pack.',inputSchema:{type:'object'},execute:vi.fn(async()=>({content:[{type:'text' as const,text:'saved'}]}))}
  try {
   const {sent,call}=scripted(one=>{
    const done=toolMessages(one).length
    if(isChild(one))return done===0?sse(undefined,{name:'get_schema',args:{},id:'call_child_schema'}):done===1?sse(undefined,{name:'experimental_test_cases',args:{pack:PACK,matrix:MATRIX},id:'call_cases'}):sse('Case c1 fails: expected decided, got refused.')
    return done===0?sse(undefined,{name:'get_schema',args:{},id:'call_schema'}):done===1?sse(undefined,{name:DELEGATE_TOOL,args:{tasks:[{title:'Run the tests',objective:'Run the draft’s test cases and say which fail.',profile:'author-test'}]}}):sse('One case fails.')
   })
   const events=await collect(withDelegation(vercel),apiSession({tools:runtime.tools,callTool:runtime.callTool,hostTools:[readPack],context:{chat:new ChatContext(),runtime:{identity:'sha256:runtime',version:'0.4.0'}},model:{family:'openai-compatible',model:'fixture',call}}))
   const child=sent.filter(isChild)
   const offered=(child[0]!.body.tools??[]).map(t=>t.function.name)
   // The gate's ceiling, as the runtime served it through the gate, beside the context tools every run has; no host tool.
   expect(offered.filter(name=>(ASSISTANT_TOOLS as readonly string[]).includes(name)).sort()).toEqual([...AUTHOR_TEST_TOOLS].sort())
   expect(offered.filter(name=>!(ASSISTANT_TOOLS as readonly string[]).includes(name)).sort()).toEqual(['list_context','read_context','read_window','request_budget_increase','search','update_plan'])
   // The test call went through the gate to the runtime; get_schema reached it once, for the parent: the child was served it from the chat's read cache.
   expect(runtime.seen.map(c=>c.name)).toEqual(['get_schema','experimental_test_cases'])
   expect(toolMessages(child.at(-1)!)[1]).toContain('expected decided, got refused')
   // The cached read was no call, so the task made one.
   expect(results(events)[0]).toMatchObject({state:'complete',profile:'author-test',toolCalls:1,result:'Case c1 fails: expected decided, got refused.'})
   expect(gateEvents.filter(e=>e.type==='guardrail')).toEqual([])
  } finally { await runtime.close() }
 })
})

describe('the rest of agent#13',()=>{
 it('keeps a long child result: the parent gets its head and a read_window cursor that reads on',async()=>{
  const LINES=Array.from({length:400},(_,i)=>`Finding line ${i+1}: the receipts for trip ${i+1} match the policy.`)
  const LONG=LINES.join('\n')
  let cursor:{id:string;cursor:string}|undefined,readOn=''
  const {sent,call}=scripted(one=>{
   if(isChild(one))return sse(LONG)
   const done=toolMessages(one)
   if(done.length===0)return sse(undefined,{name:DELEGATE_TOOL,args:{tasks:[{title:'Receipts',objective:'Check every trip.',profile:'analyze'}]}})
   if(done.length===1){const more=framed(done[0]!).structuredContent.tasks[0]!.more!;cursor=more;return sse(undefined,{name:'read_window',args:{id:more.id,cursor:more.cursor},id:'call_more'})}
   readOn=done[1]!;return sse('Every trip matches.')
  })
  const events=await collect(withDelegation(vercel),apiSession({model:{family:'openai-compatible',model:'fixture',call}}))
  const record=results(events)[0]!
  expect(record.state).toBe('complete')
  expect(LONG.startsWith(record.result!)).toBe(true)
  expect(record.result!.length).toBeLessThan(LONG.length)
  expect(record.more).toEqual({readWith:'read_window',id:cursor!.id,cursor:`line:${record.result!.split('\n').length}`,totalCharacters:LONG.length})
  // The parent read on from the cursor: the next line after the head.
  const next=LINES[record.result!.split('\n').length]!
  expect(readOn).toContain(next)
  expect(parentRequests(sent)).toHaveLength(3)
  expect(events.filter(e=>e.type==='message')).toEqual([{type:'message',text:'Every trip matches.'}])
 })

 it('cuts a head at whole lines, or inside a first line too long, with the cursor read_window takes',()=>{
  expect(findingsHead('a\nb\nc',JSON.stringify('a\nb').length)).toEqual({head:'a\nb',cursor:'line:2'})
  const long='x'.repeat(RESULT_LIMIT+50)
  expect(findingsHead(long,100_000)).toEqual({head:'x'.repeat(RESULT_LIMIT),cursor:`line:0+${RESULT_LIMIT}`})
 })

 it('asks a child once to summarize long findings where the task asks, within its budget',async()=>{
  const LONG=Array.from({length:600},(_,i)=>`Observation ${i+1}: nothing unusual.`).join('\n')
  const {sent,call}=scripted(one=>{
   if(isChild(one))return sse(promptOf(one).includes('Summarize the findings below')?'Summary: 600 observations, nothing unusual.':LONG)
   return toolMessages(one).length===0?sse(undefined,{name:DELEGATE_TOOL,args:{tasks:[{title:'Scan',objective:'Scan everything.',profile:'analyze',summarize:true}]}}):sse('Nothing unusual.')
  })
  const events=await collect(withDelegation(vercel),apiSession({model:{family:'openai-compatible',model:'fixture',call}}))
  expect(sent.filter(isChild)).toHaveLength(2)
  // The summary request is tool-free: one request, its last.
  expect(sent.filter(isChild)[1]!.body.tools).toBeUndefined()
  expect(results(events)[0]).toMatchObject({state:'complete',summarized:true,result:'Summary: 600 observations, nothing unusual.',modelRequests:2,more:{readWith:'read_window',cursor:'line:0',totalCharacters:LONG.length}})
 })

 it('states the response’s tool-call cap in the delegation instructions and in the execution state',async()=>{
  expect(DELEGATION_INSTRUCTIONS).toContain(`at most ${MAX_TOOLS} tool calls in all, yours and your children’s together`)
  const {sent,call}=scripted(one=>isChild(one)?sse('Finding.'):toolMessages(one).length===0?sse(undefined,{name:DELEGATE_TOOL,args:{tasks:[task('Check','analyze')]}}):sse('Done.'))
  await collect(withDelegation(vercel),apiSession({model:{family:'openai-compatible',model:'fixture',call}}))
  const stated=(one:Sent)=>JSON.parse(texts(one).join('\n').match(/"toolCalls":(\{[^}]*\})/)![1]!) as Record<string,unknown>
  const parent=parentRequests(sent)
  expect(texts(parent[0]!).join('\n')).toContain(`at most ${MAX_TOOLS} tool calls in all`)
  expect(stated(parent[0]!)).toMatchObject({unit:'tool-calls',used:0,limit:MAX_TOOLS,remaining:MAX_TOOLS,scope:'response'})
  // A child is told its own budget.
  expect(stated(sent.find(isChild)!)).toMatchObject({unit:'tool-calls',used:0,limit:6,remaining:6,scope:'task'})
 })
})
