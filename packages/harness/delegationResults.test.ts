/**
 * What a delegated child returns, when it cannot finish, and what it cost (agent#16, agent#17,
 * agent#18), through the delegation coordinator on the real engines where the case allows, with
 * scripted model capabilities and agents.
 *
 * - agent#16: each profile's report reads in its shape; a child's question or proposal reaches the
 *   parent as an open question and the task is complete; one more batch is allowed after a batch
 *   that returned one; a child's prose streams under its task id.
 * - agent#17: a child stopped at its time limit returns what it wrote, marked partial; a 503
 *   overloaded_error before the child's first answer byte is sent once more, once, and never after a
 *   byte; a connection that serves three requests at once runs three children; an identical task of
 *   the same chat returns the earlier result with no model request.
 * - agent#18: each child's model, requests, tool calls, tokens, time to first byte and duration on
 *   its record and in the parent's checkpoint, with the batch's total and the parent's own, and the
 *   same figures after a resume from that checkpoint.
 */
import type { AgentRun } from './agent'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { DEFAULT_CHILD_CONCURRENCY, figuresTotal, fitReport, withDelegation } from './delegation'
import { DELEGATE_TOOL, MAX_BATCHES, REPORT_BOUNDS, TASK_PROFILES, readReport, reportInstructions, validDelegatedTask, type DelegatedTask, type ReportKind, type TaskProfile, type TaskReport } from './delegationContract'
import type { AssistantEvent, AssistantSession, Engine, EngineSession, McpToolResult } from './engine'
import { ChatContext } from './context/chat'
import { codex } from './engines/codex'
import { vercel } from './engines/vercel'
import { normalize } from './thinking'
import { configureHarness } from './host'
import { readExecution, validDelegationFigures, type ChildFigures, type ExecutionCheckpoint } from './execution/state'
import taskSchema from '../../contract/schemas/delegated-task.json'
import provenanceSchema from '../../contract/schemas/provenance.json'
import analyzeVector from '../../contract/vectors/delegation/report-analyze.json'
import reviewVector from '../../contract/vectors/delegation/report-review.json'
import researchVector from '../../contract/vectors/delegation/report-research.json'
import authorTestVector from '../../contract/vectors/delegation/report-author-test.json'
import figuresVector from '../../contract/vectors/delegation/figures.json'
import answerVector from '../../contract/vectors/delegation/answer.json'
import recordVector from '../../contract/vectors/delegation/record.json'
import resumedVector from '../../contract/vectors/delegation/record-resumed.json'
import connectionVector from '../../contract/vectors/delegation/connection-models.json'
import agentRunsVector from '../../contract/vectors/delegation/figures-agent-runs.json'
import answerResumedVector from '../../contract/vectors/delegation/answer-resumed.json'
import checkpointVector from '../../contract/vectors/delegation/checkpoint.json'

afterEach(()=>{vi.restoreAllMocks();vi.useRealTimers();configureHarness({modelsFor:()=>({})})})

const apiSession=(patch:Partial<AssistantSession>={}):AssistantSession=>({policy:{outputGuard:'off'},prompt:'PARENT PRIVATE HISTORY',testPrompt:'',tools:[],callTool:vi.fn(),hostTools:[],signal:new AbortController().signal,allowConversation:true,interactive:true,critic: 'off',model:{family:'openai-compatible',model:'fixture',call:vi.fn(async()=>new Response('{}'))},thinking:normalize('off','openai-compatible'),...patch})
const collect=async(engine:Engine,session:EngineSession)=>{const events:AssistantEvent[]=[];for await(const e of engine.start(session))events.push(e);return events}
/** The last state of each task the run published. */
const records=(events:AssistantEvent[])=>{const tasks=new Map<string,DelegatedTask>();for(const event of events)if(event.type==='delegated_task')tasks.set(event.task.id,structuredClone(event.task));return [...tasks.values()]}
const delegateIn=(s:EngineSession)=>s.hostTools.find(t=>t.name===DELEGATE_TOOL)
type AnsweredTask={id:string;state:string;report?:TaskReport;reportCut?:true;result?:string;partial?:true;reused?:string;problem?:string;more?:{id:string;cursor:string}}
const answered=(result:McpToolResult)=>result.structuredContent as {tasks:AnsweredTask[];note:string}

/** One OpenAI-compatible turn, streamed: text in the pieces given, or one tool call; usage on its last frame where given. */
function sse(step:{text?:string[];tool?:{name:string;args:unknown;id?:string};usage?:{prompt_tokens:number;completion_tokens:number}}):Response {
 const frame=(choices:unknown[],extra:Record<string,unknown>={})=>'data: '+JSON.stringify({id:'fixture',object:'chat.completion.chunk',model:'fixture',choices,...extra})+'\n\n'
 const body=[frame([{index:0,delta:{role:'assistant',content:null},finish_reason:null}])]
 if(step.tool)body.push(frame([{index:0,delta:{tool_calls:[{index:0,id:step.tool.id??'delegate',type:'function',function:{name:step.tool.name,arguments:JSON.stringify(step.tool.args)}}]},finish_reason:null}]))
 else for(const piece of step.text??[''])body.push(frame([{index:0,delta:{content:piece},finish_reason:null}]))
 body.push(frame([{index:0,delta:{},finish_reason:step.tool?'tool_calls':'stop'}],step.usage?{usage:step.usage}:{}))
 body.push('data: [DONE]\n\n')
 return new Response(body.join(''),{headers:{'Content-Type':'text/event-stream'}})
}
/** A refusal as a provider sends it. */
const overloaded=(status=503)=>new Response(JSON.stringify({error:{type:'overloaded_error',message:'The service is overloaded'}}),{status,headers:{'content-type':'application/json'}})

interface Sent {number:number;body:{model?:string;messages:{role:string;content?:unknown;tool_call_id?:string}[];tools?:{function:{name:string}}[]}}
/** A model capability that answers each request with `answer`, and keeps what it was sent. */
const scripted=(answer:(sent:Sent,signal?:AbortSignal)=>Response|Promise<Response>)=>{
 const sent:Sent[]=[]
 const call=vi.fn(async(_suffix:string,request:{body:string;signal?:AbortSignal})=>{const one={number:sent.length+1,body:JSON.parse(request.body) as Sent['body']};sent.push(one);return answer(one,request.signal)})
 return {sent,call}
}
const opening=(one:Sent)=>{const message=one.body.messages.find(m=>m.role==='user');return typeof message?.content==='string'?[message.content]:((message?.content as {text?:string}[]|undefined)??[]).map(part=>part.text??'')}
const promptOf=(one:Sent)=>opening(one).find(text=>text.includes('Complete this read-only task'))??''
const isChild=(one:Sent)=>promptOf(one)!==''
const objectiveOf=(one:Sent)=>{const text=promptOf(one);return (JSON.parse(text.slice(text.lastIndexOf('\n{"objective"')+1).split('\n')[0]!) as {objective:string}).objective}
const toolMessages=(one:Sent)=>one.body.messages.filter(m=>m.role==='tool').map(m=>String(m.content))
/** The structured result of the delegate call the parent was answered with, read back from its framed tool message. */
const framedAnswer=(text:string)=>(JSON.parse(text.slice(text.indexOf('\n')+1,text.lastIndexOf('\n'))) as {structuredContent:{tasks:AnsweredTask[]}}).structuredContent
const delegateCall=(tasks:{title:string;objective:string;profile:string;references?:string[]}[],id='call_batch')=>sse({tool:{name:DELEGATE_TOOL,args:{tasks},id}})
const fence=(report:unknown)=>'\n\n```json\n'+JSON.stringify({report})+'\n```'

const VECTORS:Record<ReportKind,{items:unknown[]}>={analyze:analyzeVector,review:reviewVector,research:researchVector,'author-test':authorTestVector}

describe('structured results (agent#16)',()=>{
 it('reads each profile’s report vector in its own shape, and refuses one that breaks the shape',()=>{
  for(const profile of TASK_PROFILES){
   const kind=profile.report!
   for(const item of VECTORS[kind].items)expect(readReport(kind,item)).toEqual({report:item})
  }
  const review=reviewVector.items[0]!
  // A review finding without its severity, a research finding citing nothing, an author-test finding naming no check.
  expect(readReport('review',{...review,findings:[{text:'A gap.'}]})).toEqual({problem:expect.stringContaining('severity')})
  expect(readReport('research',{...researchVector.items[0]!,findings:[{text:'The rate is 0.58.'}]})).toEqual({problem:expect.stringContaining('at least one source')})
  expect(readReport('author-test',{...authorTestVector.items[0]!,findings:[{text:'Valid.',status:'passed'}]})).toEqual({problem:expect.stringContaining('Runtime check')})
  // A finding citing an id the report does not list, and a member no shape names.
  expect(readReport('analyze',{...analyzeVector.items[0]!,findings:[{text:'x',citations:['elsewhere']}]})).toEqual({problem:expect.stringContaining('citations')})
  expect(readReport('analyze',{...analyzeVector.items[0]!,approved:true})).toEqual({problem:expect.stringContaining('approved')})
  // The shape the child is told is the one its reader holds.
  expect(reportInstructions('review')).toContain('"severity": "gap" | "inconsistency" | "unsupported"')
  expect(reportInstructions('author-test')).toContain('"check": the Runtime tool you ran')
  // The schema's report shapes are the reader's: the same members, the same bounds.
  for(const shape of ['analyzeReport','reviewReport','researchReport','authorTestReport'] as const){
   const def=taskSchema.$defs[shape]
   expect(Object.keys(def.properties).sort()).toEqual(['citations','confidence','findings','notDone','openQuestions','summary'])
   expect([def.properties.summary.maxLength,def.properties.findings.maxItems,def.properties.citations.maxItems,def.properties.openQuestions.maxItems,def.properties.notDone.maxItems]).toEqual([REPORT_BOUNDS.summary,REPORT_BOUNDS.findings,REPORT_BOUNDS.citations,REPORT_BOUNDS.openQuestions,REPORT_BOUNDS.notDone])
  }
 })

 it('gives each profile’s child its report shape, and returns the report it wrote, read in that shape, to the parent beside its text',async()=>{
  const kinds=TASK_PROFILES.map(p=>p.report!)
  const reportFor=(objective:string)=>VECTORS[kinds.find(kind=>objective.includes(`(${kind})`))!].items[0]
  const {sent,call}=scripted(one=>{
   if(isChild(one))return sse({text:[`Findings for ${objectiveOf(one)}.`,fence(reportFor(objectiveOf(one)))]})
   const done=toolMessages(one).length
   if(done===0)return delegateCall(TASK_PROFILES.slice(0,3).map(p=>({title:p.id,objective:`Check the draft (${p.report}).`,profile:p.id})),'call_one')
   if(done===1)return delegateCall([{title:'author-test',objective:'Check the draft (author-test).',profile:'author-test'}],'call_two')
   return sse({text:['Done.']})
  })
  const events=await collect(withDelegation(vercel),apiSession({model:{family:'openai-compatible',model:'fixture',call}}))
  const tasks=records(events)
  expect(tasks.map(t=>t.state)).toEqual(['complete','complete','complete','complete'])
  for(const task of tasks){
   const kind=TASK_PROFILES.find(p=>p.id===task.profile)!.report!
   // The child was told its own shape.
   expect(promptOf(sent.find(one=>isChild(one)&&objectiveOf(one)===task.objective)!)).toContain(reportInstructions(kind))
   expect(task.report).toEqual(reportFor(task.objective))
   expect(readReport(kind,task.report)).toEqual({report:task.report})
   // The text is the answer less its block.
   expect(task.result).toBe(`Findings for ${task.objective}.`)
   expect(validDelegatedTask(task)).toBe(true)
   for(const key of Object.keys(task))expect(Object.keys(taskSchema.properties)).toContain(key)
  }
  // The parent read each report in the call's result, whole, with no cut.
  const parent=sent.filter(one=>!isChild(one)).at(-1)!
  const first=framedAnswer(toolMessages(parent)[0]!).tasks
  expect(first.map(t=>t.report)).toEqual(tasks.slice(0,3).map(t=>t.report))
  expect(first.some(t=>t.reportCut)).toBe(false)
  for(const key of Object.keys(first[0]!))expect(Object.keys(taskSchema.$defs.answerTask.properties)).toContain(key)
 })

 it('writes the report itself where the child’s block breaks its shape, and says why',async()=>{
  // A review finding without its severity: the block is not read as the report.
  const broken={...reviewVector.items[0]!,findings:[{text:'A gap the draft leaves.'}]}
  const {call}=scripted(one=>isChild(one)?sse({text:['The draft leaves a gap.',fence(broken)]}):toolMessages(one).length===0?delegateCall([{title:'Review',objective:'Review the draft.',profile:'review'}]):sse({text:['Done.']}))
  const events=await collect(withDelegation(vercel),apiSession({model:{family:'openai-compatible',model:'fixture',call}}))
  const task=records(events)[0]!
  expect(task.state).toBe('complete')
  expect(task.report).toMatchObject({summary:'The draft leaves a gap.',findings:[],confidence:'unstated',notDone:[expect.stringContaining('severity')]})
  expect(readReport('review',task.report)).toEqual({report:task.report})
  expect(task.result).toBe('The draft leaves a gap.')
 })

 it('returns a child’s question to the parent as data: the task is complete, and its open question is in the result the parent reads',async()=>{
  let answer:{tasks:AnsweredTask[]}|undefined
  const engine:Engine={id:'vercel',async *start(s){
   if(delegateIn(s)){answer=answered(await delegateIn(s)!.execute({tasks:[{title:'Receipts',objective:'Check the receipt rule.',profile:'analyze'}]},s.signal));yield {type:'message',text:'I will ask the person.'};yield {type:'end'};return}
   yield {type:'message_progress',text:'No rule reads a receipt.'}
   yield {type:'user_question',id:'q1',question:{title:'Receipts',question:'Should a claim with no receipt be declined?',selection:'single',options:[{id:'a',label:'Decline',description:'Decline it.'},{id:'b',label:'Review',description:'Send it for review.'}],recommendedOptionId:null,recommendation:null}}
   yield {type:'end'}
  }}
  const events=await collect(withDelegation(engine),apiSession())
  const task=records(events)[0]!
  expect(task.state).toBe('complete')
  expect(task.failure).toBeUndefined()
  expect(task.report!.openQuestions).toEqual([{kind:'question',text:'Receipts: Should a claim with no receipt be declined? Options: Decline; Review.'}])
  expect(answer!.tasks[0]).toMatchObject({state:'complete',report:{openQuestions:[{kind:'question'}]}})
  // The question did not reach the person: no user_question left the coordinator.
  expect(events.filter(e=>e.type==='user_question')).toEqual([])
 })

 it('returns a document a child proposes as an open question with the document kept for the parent, never applied or shown for review',async()=>{
  const DOC={id:'from-child',title:'Child draft'}
  let readBack=''
  const {call}=scripted(one=>{
   if(isChild(one))return sse({text:['Here is a draft.\n\n```json\n'+JSON.stringify({proposal:{kind:'create',document:DOC,unknowns:[]}})+'\n```']})
   const done=toolMessages(one)
   if(done.length===0)return delegateCall([{title:'Draft',objective:'Draft the pack.',profile:'analyze'}])
   if(done.length===1){const ref=framedAnswer(done[0]!).tasks[0]!.report!.openQuestions[0]!;return sse({tool:{name:'read_window',args:{id:(ref as {ref:string}).ref},id:'call_read'}})}
   readBack=done[1]!;return sse({text:['I will look at the draft first.']})
  })
  const events=await collect(withDelegation(vercel),apiSession({model:{family:'openai-compatible',model:'fixture',call}}))
  const task=records(events)[0]!
  expect(task.state).toBe('complete')
  expect(task.report!.openQuestions).toEqual([{kind:'proposal',text:expect.stringContaining('not applied or shown for review'),ref:expect.stringContaining('task-proposal-')}])
  expect(events.filter(e=>e.type==='proposal')).toEqual([])
  expect(readBack).toContain('Child draft')
 })

 it('allows one more batch after a batch that returned an open question, past the response’s batches, and no other',async()=>{
  let ran=0,refused=''
  const engine:Engine={id:'vercel',async *start(s){
   if(delegateIn(s)){
    for(let i=0;i<MAX_BATCHES;i++){await delegateIn(s)!.execute({tasks:[{title:`Batch ${i}`,objective:`Check part ${i}${i===MAX_BATCHES-1?' (ask)':''}.`,profile:'analyze'}]},s.signal);ran++}
    // The last batch returned a question: one more is allowed, and then none.
    await delegateIn(s)!.execute({tasks:[{title:'Follow-up',objective:'Answer the question.',profile:'analyze'}]},s.signal);ran++
    await delegateIn(s)!.execute({tasks:[{title:'Another',objective:'One more.',profile:'analyze'}]},s.signal).catch((error:Error)=>{refused=error.message})
    yield {type:'message',text:'Done.'};yield {type:'end'};return
   }
   if(s.prompt.includes('(ask)'))yield {type:'message',text:'Part 3 needs a decision.'+fence({...analyzeVector.items[0]!})}
   else yield {type:'message',text:'Part checked.'}
   yield {type:'end'}
  }}
  const events=await collect(withDelegation(engine),apiSession())
  expect(ran).toBe(MAX_BATCHES+1)
  expect(refused).toContain(`its ${MAX_BATCHES+1} delegation batches`)
  expect(records(events)).toHaveLength(MAX_BATCHES+1)
 })

 it('streams a running child’s prose under its task id, between its records, and never as the parent’s answer',async()=>{
  const {call}=scripted(one=>isChild(one)?sse({text:['Trip 1:',' receipts match.']}):toolMessages(one).length===0?delegateCall([{title:'Receipts',objective:'Check the receipts.',profile:'analyze'}]):sse({text:['Parent ',' answer.']}))
  const events=await collect(withDelegation(vercel),apiSession({model:{family:'openai-compatible',model:'fixture',call}}))
  const task=records(events)[0]!
  const child=events.filter((e):e is Extract<AssistantEvent,{type:'message_progress'}>=>e.type==='message_progress'&&e.taskId!==undefined)
  // The child's engine clears its prose as each step starts, as it does for a parent; the coordinator clears it when the child ends.
  expect(child.map(e=>[e.taskId,e.text])).toEqual([[task.id,''],[task.id,'Trip 1:'],[task.id,'Trip 1: receipts match.'],[task.id,'']])
  // The child's prose comes after its running record and before its complete one.
  const at=(predicate:(e:AssistantEvent)=>boolean)=>events.findIndex(predicate)
  expect(at(e=>e.type==='delegated_task'&&e.task.state==='running')).toBeLessThan(at(e=>e===child[1]))
  expect(at(e=>e===child.at(-1))).toBeLessThan(at(e=>e.type==='delegated_task'&&e.task.state==='complete'))
  // The parent's own progress carries no task id.
  expect(events.filter(e=>e.type==='message_progress'&&e.taskId===undefined&&e.text!=='').map(e=>(e as {text:string}).text)).toEqual(['Parent','Parent  answer.'])
 })

 it('streams a Codex child’s prose under its task id the same way',async()=>{
  const run=vi.fn<AgentRun>(async(r,cb)=>{
   const delegate=r.tools.find(t=>t.description.startsWith('delegate_tasks:'))
   if(delegate&&!r.prompt.includes('DELEGATION RESULT')){await cb.tool({id:'call',name:delegate.name,arguments:{tasks:[{title:'Receipts',objective:'Check the receipts.',profile:'analyze'}]}},cb.signal);throw Error('Parent must yield')}
   if(!delegate){await cb.event({type:'text',id:'t',text:'Trip 1:'});await cb.event({type:'text',id:'t',text:' receipts match.'})}
   await cb.event({type:'message',id:'t',text:delegate?'Parent finished.':'Trip 1: receipts match.',phase:'final'})
  })
  const {model:_model,thinking:_thinking,...base}=apiSession()
  const events=await collect(withDelegation(codex),{...base,agent:{model:'fixture',run}})
  const task=records(events)[0]!
  expect(task.state).toBe('complete')
  expect(events.filter(e=>e.type==='message_progress'&&e.taskId!==undefined).map(e=>[(e as {taskId:string}).taskId,(e as {text:string}).text])).toEqual([[task.id,'Trip 1:'],[task.id,'Trip 1: receipts match.'],[task.id,'']])
 })

 it('holds a report to the task’s share by whole items, the whole kept on the record',()=>{
  const report:TaskReport={summary:'S'.repeat(200),findings:Array.from({length:12},(_,i)=>({text:`Finding ${i} `+'x'.repeat(300),citations:[`src-${i}`]})),citations:Array.from({length:12},(_,i)=>({id:`src-${i}`})),openQuestions:[{kind:'question',text:'Q?'}],confidence:'medium',notDone:['Not checked.']}
  const {shown,cut}=fitReport(report,1500)
  expect(cut).toBe(true)
  expect(JSON.stringify(shown).length).toBeLessThanOrEqual(1500)
  // The open question and the summary survive; notDone went first; what is shown still reads in its shape.
  expect(shown.openQuestions).toEqual(report.openQuestions)
  expect(shown.summary).toBe(report.summary)
  expect(shown.notDone).toEqual([])
  expect(readReport('analyze',shown)).toEqual({report:shown})
  expect(fitReport(report,100_000)).toEqual({shown:report,cut:false})
 })
})

describe('resilience and capacity (agent#17)',()=>{
 it('returns what a child had written when it reached its time limit, marked partial, to the parent',async()=>{
  // A profile with a short time limit; its child streams one line, then the answer stalls.
  const quick:TaskProfile={id:'quick',description:'A short check.',instructions:'Check quickly.',capabilities:[],report:'analyze',budget:{modelRequests:2,toolCalls:2,milliseconds:150}}
  const {call}=scripted((one,signal)=>{
   if(isChild(one)&&objectiveOf(one)==='Read the policy.')return sse({text:['The policy caps meals at 40.']})
   if(isChild(one))return new Response(new ReadableStream<Uint8Array>({start(controller){
    const frame=(delta:unknown)=>new TextEncoder().encode('data: '+JSON.stringify({id:'fixture',object:'chat.completion.chunk',model:'fixture',choices:[{index:0,delta,finish_reason:null}]})+'\n\n')
    controller.enqueue(frame({role:'assistant',content:null}));controller.enqueue(frame({content:'Trip 1: receipts match.'}))
    signal?.addEventListener('abort',()=>controller.error(new DOMException('Stopped','AbortError')),{once:true})
   }}),{headers:{'Content-Type':'text/event-stream'}})
   return toolMessages(one).length===0?delegateCall([{title:'Receipts',objective:'Check every trip.',profile:'quick'},{title:'Policy',objective:'Read the policy.',profile:'analyze'}]):sse({text:['Trip 1 matches; the rest was not checked.']})
  })
  const events=await collect(withDelegation(vercel,{profiles:[...TASK_PROFILES,quick]}),apiSession({model:{family:'openai-compatible',model:'fixture',call}}))
  const [task,sibling]=records(events)
  expect(task).toMatchObject({state:'complete',partial:true,result:'Trip 1: receipts match.',problem:'The task reached its time limit; its result is what it had written by then.'})
  expect(sibling).toMatchObject({state:'complete',result:'The policy caps meals at 40.'})
  expect(sibling!.partial).toBeUndefined()
  expect(task.report!.notDone).toContain('The task stopped before it finished.')
  const said=events.find(e=>e.type==='tool_result'&&e.name===DELEGATE_TOOL) as Extract<AssistantEvent,{type:'tool_result'}>
  expect((said.structured as {tasks:AnsweredTask[]}).tasks[0]).toMatchObject({state:'complete',partial:true,result:'Trip 1: receipts match.'})
  expect(events.filter(e=>e.type==='message').map(e=>(e as {text:string}).text)).toEqual(['Trip 1 matches; the rest was not checked.'])
 })

 it('sends a child’s first request again once where the endpoint answers 503 overloaded_error, and the task completes',async()=>{
  let childRequests=0
  const {call}=scripted(one=>{
   if(isChild(one)){childRequests++;return childRequests===1?overloaded():sse({text:['Checked.']})}
   return toolMessages(one).length===0?delegateCall([{title:'Check',objective:'Check the draft.',profile:'analyze'}]):sse({text:['Done.']})
  })
  const events=await collect(withDelegation(vercel),apiSession({model:{family:'openai-compatible',model:'fixture',call}}))
  expect(childRequests).toBe(2)
  const task=records(events)[0]!
  expect(task).toMatchObject({state:'complete',result:'Checked.',modelRequests:2,retried:{failure:{code:'unavailable',outcome:'unknown',status:503,source:'model'},waitMs:1000}})
  expect(validDelegatedTask(task)).toBe(true)
 })

 it('sends it again once and not twice: a second 503 fails the task',async()=>{
  let childRequests=0
  const {call}=scripted(one=>{
   if(isChild(one)){childRequests++;return overloaded()}
   return toolMessages(one).length===0?delegateCall([{title:'Check',objective:'Check the draft.',profile:'analyze'}]):sse({text:['The check could not run.']})
  })
  const events=await collect(withDelegation(vercel),apiSession({model:{family:'openai-compatible',model:'fixture',call}}))
  expect(childRequests).toBe(2)
  expect(records(events)[0]).toMatchObject({state:'failed',modelRequests:2,failure:{code:'unavailable',status:503}})
 })

 it('never sends a child’s request again after the first byte of its answers: a 503 on its second request fails it',async()=>{
  let childRequests=0
  const readTool={name:'read_page',taxonomy:{operation:'read',effect:'read'} as const,delegation:'read' as const,description:'Read a page.',inputSchema:{type:'object'},execute:vi.fn(async()=>({content:[{type:'text' as const,text:'Page.'}]}))}
  const {call}=scripted(one=>{
   if(isChild(one)){childRequests++;return childRequests===1?sse({tool:{name:'read_page',args:{},id:'call_page'}}):overloaded()}
   return toolMessages(one).length===0?delegateCall([{title:'Check',objective:'Check the page.',profile:'review'}]):sse({text:['The check could not finish.']})
  })
  const events=await collect(withDelegation(vercel),apiSession({hostTools:[readTool],model:{family:'openai-compatible',model:'fixture',call}}))
  expect(childRequests).toBe(2)
  const task=records(events)[0]!
  expect(task).toMatchObject({state:'failed',modelRequests:2,failure:{code:'unavailable'}})
  expect(task.retried).toBeUndefined()
 })

 it('leaves a 503 that carries no overload code unsent',async()=>{
  let childRequests=0
  const {call}=scripted(one=>{
   if(isChild(one)){childRequests++;return new Response(JSON.stringify({error:{type:'server_error'}}),{status:503,headers:{'content-type':'application/json'}})}
   return toolMessages(one).length===0?delegateCall([{title:'Check',objective:'Check the draft.',profile:'analyze'}]):sse({text:['No.']})
  })
  const events=await collect(withDelegation(vercel),apiSession({model:{family:'openai-compatible',model:'fixture',call}}))
  expect(childRequests).toBe(1)
  expect(records(events)[0]).toMatchObject({state:'failed',modelRequests:1})
 })

 it('leaves a 429 to the engine’s own rule: the engine sends it again, and the child adds no retry of its own',async()=>{
  let childRequests=0
  const {call}=scripted(one=>{
   if(isChild(one)){childRequests++;return childRequests===1?new Response(JSON.stringify({error:{type:'rate_limit_error'}}),{status:429,headers:{'content-type':'application/json','retry-after':'1'}}):sse({text:['Checked.']})}
   return toolMessages(one).length===0?delegateCall([{title:'Check',objective:'Check the draft.',profile:'analyze'}]):sse({text:['Done.']})
  })
  const events=await collect(withDelegation(vercel),apiSession({model:{family:'openai-compatible',model:'fixture',call}}))
  expect(childRequests).toBe(2)
  const task=records(events)[0]!
  expect(task).toMatchObject({state:'complete',result:'Checked.',modelRequests:2})
  expect(task.retried).toBeUndefined()
 })

 it('runs as many children at once as the connection serves requests: three where its modelsFor answer says three, two where it says none, one on an agent capability',async()=>{
  const peaks:number[]=[]
  for(const concurrentRequests of [3,undefined]){
   configureHarness({modelsFor:(asked)=>asked.connection?.connectionId==='wide'&&concurrentRequests!==undefined?{concurrentRequests}:{}})
   let active=0,peak=0
   const engine:Engine={id:'vercel',async *start(s){
    if(delegateIn(s)){await delegateIn(s)!.execute({tasks:['A','B','C'].map(t=>({title:t,objective:`Check part ${t}.`,profile:'analyze'}))},s.signal);yield {type:'end'};return}
    active++;peak=Math.max(peak,active);await new Promise(r=>setTimeout(r,20));active--
    yield {type:'message',text:'Checked.'};yield {type:'end'}
   }}
   await collect(withDelegation(engine),apiSession({context:{connection:{connectionId:'wide'}},model:{family:'openai-compatible',model:'fixture',call:vi.fn()}}))
   peaks.push(peak)
  }
  expect(peaks).toEqual([3,DEFAULT_CHILD_CONCURRENCY])
  // An agent capability runs one at a time whatever the connection says.
  configureHarness({modelsFor:()=>({concurrentRequests:3})})
  let active=0,peak=0
  const run=vi.fn<AgentRun>(async(r,cb)=>{
   const delegate=r.tools.find(t=>t.description.startsWith('delegate_tasks:'))
   if(delegate&&!r.prompt.includes('DELEGATION RESULT')){await cb.tool({id:'call',name:delegate.name,arguments:{tasks:['A','B','C'].map(t=>({title:t,objective:`Check part ${t}.`,profile:'analyze'}))}},cb.signal);throw Error('Parent must yield')}
   if(!delegate){active++;peak=Math.max(peak,active);await new Promise(r=>setTimeout(r,20));active--}
   await cb.event({type:'message',id:'m',text:delegate?'Parent finished.':'Checked.',phase:'final'})
  })
  const {model:_model,thinking:_thinking,...base}=apiSession()
  await collect(withDelegation(codex),{...base,agent:{model:'fixture',run}})
  expect(peak).toBe(1)
 })

 it('returns the earlier result for an identical task of the same chat with no model request, marked reused, and runs one whose profile differs',async()=>{
  const chat=new ChatContext()
  const run=async(profile:string)=>{
   const {sent,call}=scripted(one=>isChild(one)?sse({text:['The cap is 40 per day.',fence(analyzeVector.items[0])]}):toolMessages(one).length===0?delegateCall([{title:'Cap',objective:'Find the meal cap.',profile}]):sse({text:['40 per day.']}))
   const events=await collect(withDelegation(vercel),apiSession({context:{chat},model:{family:'openai-compatible',model:'fixture',call}}))
   return {children:sent.filter(isChild).length,task:records(events)[0]!,answer:framedAnswer(toolMessages(sent.filter(one=>!isChild(one)).at(-1)!)[0]!).tasks[0]!}
  }
  const first=await run('analyze')
  expect(first.children).toBe(1)
  const again=await run('analyze')
  expect(again.children).toBe(0)
  expect(again.task).toMatchObject({state:'complete',reused:first.task.id,modelRequests:0,toolCalls:0,result:first.task.result,report:first.task.report})
  expect(again.answer).toMatchObject({reused:first.task.id,result:first.task.result})
  expect(validDelegatedTask(again.task)).toBe(true)
  // The same objective under another profile is another task.
  const other=await run('review')
  expect(other.children).toBe(1)
  expect(other.task.reused).toBeUndefined()
 })
})

describe('cost and time (agent#18)',()=>{
 const REQUEST={message:'Check the draft',input:'The draft.'}
 const delayed=async(response:Response)=>{await new Promise(r=>setTimeout(r,25));return response}
 const endpoint=()=>scripted(one=>{
  if(isChild(one))return delayed(sse({text:[`Checked ${objectiveOf(one)}`],usage:{prompt_tokens:120,completion_tokens:30}}))
  return toolMessages(one).length===0?sse({tool:{name:DELEGATE_TOOL,args:{tasks:[{title:'A',objective:'part A.',profile:'analyze'},{title:'B',objective:'part B.',profile:'analyze'}]},id:'call_batch'},usage:{prompt_tokens:900,completion_tokens:40}}):sse({text:['Both parts check.'],usage:{prompt_tokens:1000,completion_tokens:20}})
 })
 const lastCheckpoint=(events:AssistantEvent[])=>(events.filter(e=>e.type==='execution_checkpoint').at(-1) as {checkpoint:ExecutionCheckpoint}).checkpoint

 it('records each child’s model, requests, tool calls, tokens, time to first byte and duration, and the batch’s total beside the parent’s own, and keeps them through a resume',async()=>{
  const chat=new ChatContext()
  const {call}=endpoint()
  const events=await collect(withDelegation(vercel),apiSession({context:{chat,currentRequest:REQUEST},model:{family:'openai-compatible',model:'fixture',call}}))
  const tasks=records(events)
  for(const task of tasks){
   // The provider's usage figures, as the completion rows record them.
   expect(task).toMatchObject({state:'complete',model:'fixture',modelRequests:1,toolCalls:0,tokens:{inputTokens:120,outputTokens:30}})
   expect(task.firstByteMs).toBeGreaterThanOrEqual(20)
   expect(task.durationMs).toBeGreaterThanOrEqual(task.firstByteMs!)
  }
  const done=lastCheckpoint(events)
  expect(done.status).toBe('complete')
  const figures=done.delegation!
  expect(validDelegationFigures(figures)).toBe(true)
  expect(readExecution(done)!.delegation).toEqual(figures)
  expect(figures.batches).toHaveLength(1)
  expect(figures.batches[0]!.children).toEqual(tasks.map(task=>({task:task.id,batch:1,profile:'analyze',model:'fixture',state:'complete',modelRequests:1,toolCalls:0,tokens:task.tokens,firstByteMs:task.firstByteMs,durationMs:task.durationMs})))
  expect(figures.batches[0]!.total).toMatchObject({modelRequests:2,toolCalls:0,tokens:{inputTokens:240,outputTokens:60}})
  expect(figures.batches[0]!.total.durationMs).toBeGreaterThanOrEqual(Math.max(...tasks.map(t=>t.durationMs!)))
  // The parent's own: its two requests (the response's four less its children's two) and its own tokens.
  expect(done.used).toBe(4)
  expect(figures.parent).toMatchObject({model:'fixture',modelRequests:2,tokens:{inputTokens:1900,outputTokens:60}})
  expect(Object.keys(figures.parent).sort()).toEqual(['model','modelRequests','tokens'])
  // Each member is one the contract names.
  for(const key of Object.keys(figures.batches[0]!.children[0]!))expect(Object.keys(provenanceSchema.$defs.childFigures.properties)).toContain(key)

  // A resume from the checkpoint saved after the batch: the figures are there again at its end.
  const afterBatch=events.filter(e=>e.type==='execution_checkpoint').map(e=>(e as {checkpoint:ExecutionCheckpoint}).checkpoint).find(c=>c.status==='running'&&c.delegation!==undefined&&c.operations.some(op=>op.name===DELEGATE_TOOL&&op.status==='complete'))!
  expect(afterBatch.delegation!.batches).toEqual(figures.batches)
  const resumed=scripted(()=>sse({text:['Both parts check, again.'],usage:{prompt_tokens:1100,completion_tokens:25}}))
  const again=await collect(withDelegation(vercel),apiSession({context:{chat,currentRequest:REQUEST,execution:afterBatch},model:{family:'openai-compatible',model:'fixture',call:resumed.call}}))
  expect(resumed.sent.some(isChild)).toBe(false)
  const end=lastCheckpoint(again)
  expect(end.status).toBe('complete')
  expect(end.delegation!.batches).toEqual(figures.batches)
  expect(end.delegation!.parent.modelRequests).toBe(end.used-2)
 })

 it('reads the figures vector, and refuses figures that do not read closed',()=>{
  for(const item of figuresVector.items)expect(validDelegationFigures(item)).toBe(true)
  const item=figuresVector.items[0]!
  expect(validDelegationFigures({...item,parent:{...item.parent,cost:1}})).toBe(false)
  expect(validDelegationFigures({...item,batches:[{...item.batches[0]!,children:[]}]})).toBe(false)
  // The answer vector's tasks are what batchAnswer writes: every member the schema names.
  for(const task of answerVector.items[1]!.tasks)for(const key of Object.keys(task))expect(Object.keys(taskSchema.$defs.answerTask.properties)).toContain(key)
 })

 // The vectors added for the schema members the release review found uncarried (#43).
 it('reads the resumed, connection and agent-run vectors as the harness’s own readers do',()=>{
  for(const task of [...resumedVector.items,...connectionVector.items]){
   expect(validDelegatedTask(task)).toBe(true)
   for(const key of Object.keys(task))expect(Object.keys(taskSchema.properties)).toContain(key)
  }
  expect(resumedVector.items[0]).toMatchObject({resumes:expect.any(String),summarized:true})
  expect(validDelegationFigures(agentRunsVector.items[0],'provider-managed')).toBe(true)
  expect(validDelegationFigures(agentRunsVector.items[0])).toBe(false)
  expect(validDelegationFigures(agentRunsVector.items[1])).toBe(true)
  expect((agentRunsVector.items[0]!.batches[0]!.total as {agentRuns?:number}).agentRuns).toBe(2)
  for(const task of answerResumedVector.items[0]!.tasks)for(const key of Object.keys(task))expect(Object.keys(taskSchema.$defs.answerTask.properties)).toContain(key)
 })

 it('reads the record vector’s tasks and the checkpoint vector’s figures as the harness’s own readers do (review row 6)',()=>{
  for(const task of recordVector.items){
   expect(validDelegatedTask(task)).toBe(true)
   for(const key of Object.keys(task))expect(Object.keys(taskSchema.properties)).toContain(key)
  }
  expect(validDelegatedTask({...recordVector.items[2]!,retried:{waitMs:1000}})).toBe(false)
  const checkpoint=readExecution(checkpointVector.items[0])!
  expect(checkpoint.delegation).toEqual(checkpointVector.items[0]!.delegation)
  expect(validDelegationFigures(checkpoint.delegation)).toBe(true)
  expect(()=>readExecution({...checkpointVector.items[0]!,delegation:{batches:[],parent:{model:'a-model'}}})).toThrow()
 })

 // agent#71: the agent capability's figures carry no modelRequests; the sum over both capabilities counts model requests only.
 it('reads an agent capability’s figures without modelRequests, holds a model capability’s to the member, and sums model requests only',()=>{
  const managed=checkpointVector.items[1]!
  expect(readExecution(managed)!.delegation).toEqual(managed.delegation)
  for(const held of [managed.delegation.parent,managed.delegation.batches[0]!.total,...managed.delegation.batches[0]!.children])expect(held).not.toHaveProperty('modelRequests')
  // Under the other unit the same figures are refused, and a member cut from a model execution's figures is.
  expect(()=>readExecution({...managed,unit:'model-requests',limit:20})).toThrow()
  const modeled=checkpointVector.items[0]!
  const cut=(figures:typeof modeled.delegation,at:'parent'|'child'|'total')=>{
   const held=structuredClone(figures) as {parent:Record<string,unknown>;batches:{children:Record<string,unknown>[];total:Record<string,unknown>}[]}
   delete (at==='parent'?held.parent:at==='child'?held.batches[0]!.children[0]!:held.batches[0]!.total).modelRequests
   return held
  }
  for(const at of ['parent','child','total'] as const)expect(()=>readExecution({...modeled,delegation:cut(modeled.delegation,at)})).toThrow()
  // One model child and one agent child: the total counts the model child's requests and the agent child adds none.
  const [model,agent]=[modeled.delegation.batches[0]!.children[0]!,managed.delegation.batches[0]!.children[0]!] as ChildFigures[]
  expect(figuresTotal([model,agent],50)).toEqual({modelRequests:model.modelRequests,toolCalls:model.toolCalls+agent.toolCalls,agentRuns:agent.agentRuns,...(model.tokens?{tokens:model.tokens}:{}),durationMs:50})
  expect(model.modelRequests).toBeGreaterThan(0)
  const alone=figuresTotal([agent,agent],50)
  expect(alone).toEqual({toolCalls:agent.toolCalls*2,agentRuns:2,durationMs:50})
  expect(alone).not.toHaveProperty('modelRequests')
 })
})

/**
 * The review of this batch (agent#39 on the pull request), row by row: each rule held by a test of
 * its own.
 */
describe('the review’s rules',()=>{
 const REQUEST={message:'Check the draft',input:'The draft.'}
 const delegateOnce=(tasks:{title:string;objective:string;profile:string;references?:string[];resume?:string}[],answer='Done.')=>(one:Sent)=>toolMessages(one).length===0?delegateCall(tasks):sse({text:[answer]})
 const recordsOf=(events:AssistantEvent[])=>records(events)
 const lastCheckpoint=(events:AssistantEvent[])=>(events.filter(e=>e.type==='execution_checkpoint').at(-1) as {checkpoint:ExecutionCheckpoint}).checkpoint

 // Row 1.
 it('does not reuse a result whose child read through a tool: the draft it read changed, and the same task runs again',async()=>{
  const chat=new ChatContext()
  let draft='v1'
  const readDraft={name:'read_draft',taxonomy:{operation:'read',effect:'read'} as const,delegation:'read' as const,description:'Read the draft.',inputSchema:{type:'object'},execute:vi.fn(async()=>({content:[{type:'text' as const,text:`The draft (${draft}).`}]}))}
  const run=async()=>{
   const {sent,call}=scripted(one=>{
    if(isChild(one))return toolMessages(one).length===0?sse({tool:{name:'read_draft',args:{},id:'call_read'}}):sse({text:[`The draft (${draft}) has no gap.`]})
    return delegateOnce([{title:'Review',objective:'Review the draft for gaps.',profile:'review'}])(one)
   })
   const events=await collect(withDelegation(vercel),apiSession({hostTools:[readDraft],context:{chat},model:{family:'openai-compatible',model:'fixture',call}}))
   return {children:sent.filter(isChild).length,task:recordsOf(events)[0]!}
  }
  const first=await run()
  expect(first.task).toMatchObject({state:'complete',toolCalls:1,result:'The draft (v1) has no gap.'})
  draft='v2'
  const again=await run()
  expect(again.children).toBe(2)
  expect(again.task.reused).toBeUndefined()
  expect(again.task.result).toBe('The draft (v2) has no gap.')
 })

 it('carries the model that wrote a reused result, and reuses nothing written on another model',async()=>{
  const chat=new ChatContext()
  const run=async()=>{
   const {sent,call}=scripted(one=>isChild(one)?sse({text:['The cap is 40.']}):delegateOnce([{title:'Cap',objective:'Find the meal cap.',profile:'analyze'}])(one))
   const events=await collect(withDelegation(vercel),apiSession({context:{chat},model:{family:'openai-compatible',model:'fixture',call}}))
   return {children:sent.filter(isChild).length,task:recordsOf(events)[0]!}
  }
  const first=await run()
  expect(first.task.model).toBe('fixture')
  const again=await run()
  expect(again).toMatchObject({children:0,task:{reused:first.task.id,model:'fixture'}})
  // The connection now names a child model: the earlier result was written on another one.
  configureHarness({modelsFor:()=>({child:'fixture-small'})})
  const other=await run()
  expect(other.children).toBe(1)
  expect(other.task).toMatchObject({model:'fixture-small'})
  expect(other.task.reused).toBeUndefined()
 })

 // Row 2, R1.
 it('never keeps a partial result for reuse: the same task after a time limit runs again',async()=>{
  const chat=new ChatContext()
  const quick:TaskProfile={id:'quick',description:'A short check.',instructions:'Check quickly.',capabilities:[],report:'analyze',budget:{modelRequests:2,toolCalls:2,milliseconds:150}}
  let stall=true,children=0
  const {call}=scripted((one,signal)=>{
   if(isChild(one)){children++
    if(!stall)return sse({text:['Every trip matches.']})
    return new Response(new ReadableStream<Uint8Array>({start(controller){
     controller.enqueue(new TextEncoder().encode('data: '+JSON.stringify({id:'f',object:'chat.completion.chunk',model:'fixture',choices:[{index:0,delta:{content:'Trip 1 matches.'},finish_reason:null}]})+'\n\n'))
     signal?.addEventListener('abort',()=>controller.error(new DOMException('Stopped','AbortError')),{once:true})
    }}),{headers:{'Content-Type':'text/event-stream'}})}
   return delegateOnce([{title:'Trips',objective:'Check every trip.',profile:'quick'}])(one)
  })
  const coordinator=withDelegation(vercel,{profiles:[...TASK_PROFILES,quick]})
  const first=recordsOf(await collect(coordinator,apiSession({context:{chat},model:{family:'openai-compatible',model:'fixture',call}})))[0]!
  expect(first).toMatchObject({state:'complete',partial:true})
  stall=false
  const again=recordsOf(await collect(coordinator,apiSession({context:{chat},model:{family:'openai-compatible',model:'fixture',call}})))[0]!
  expect(children).toBe(2)
  expect(again).toMatchObject({state:'complete',result:'Every trip matches.'})
  expect(again.reused).toBeUndefined()
 })

 // Row 2, R2.
 it('holds the references in the reuse key: the same objective on another document runs again, on the same one is reused',async()=>{
  const chat=new ChatContext()
  const documents=[{id:'policy-a',kind:'artifact' as const,label:'Policy A',value:'Meals are capped at 40.'},{id:'policy-b',kind:'artifact' as const,label:'Policy B',value:'Meals are capped at 55.'}]
  let children=0
  const run=async(reference:string)=>{
   const {call}=scripted(one=>{if(isChild(one)){children++;return sse({text:['The cap is stated.']})}return delegateOnce([{title:'Cap',objective:'Find the meal cap.',profile:'analyze',references:[reference]}])(one)})
   return recordsOf(await collect(withDelegation(vercel),apiSession({context:{chat,documents},model:{family:'openai-compatible',model:'fixture',call}})))[0]!
  }
  const a=await run('policy-a')
  const b=await run('policy-b')
  expect(children).toBe(2);expect(b.reused).toBeUndefined()
  const a2=await run('policy-a')
  expect(children).toBe(2);expect(a2.reused).toBe(a.id)
 })

 // Row 2, R3.
 it('never reuses a resumed task: a resume with the same objective runs, with its earlier work',async()=>{
  const chat=new ChatContext()
  const first=scripted(one=>isChild(one)?sse({text:['The cap is 40.']}):delegateOnce([{title:'Cap',objective:'Find the meal cap.',profile:'analyze'}])(one))
  const earlier=recordsOf(await collect(withDelegation(vercel),apiSession({context:{chat},model:{family:'openai-compatible',model:'fixture',call:first.call}})))[0]!
  const second=scripted(one=>isChild(one)?sse({text:['Still 40, per day.']}):delegateOnce([{title:'Cap',objective:'Find the meal cap.',profile:'analyze',resume:earlier.id}])(one))
  const resumed=recordsOf(await collect(withDelegation(vercel),apiSession({context:{chat},model:{family:'openai-compatible',model:'fixture',call:second.call}})))[0]!
  expect(second.sent.filter(isChild)).toHaveLength(1)
  expect(promptOf(second.sent.find(isChild)!)).toContain('earlierWork')
  expect(resumed).toMatchObject({state:'complete',resumes:earlier.id,result:'Still 40, per day.'})
  expect(resumed.reused).toBeUndefined()
 })

 // Row 2, R4.
 it('leaves tokens absent, never zero, where the provider reports no usage',async()=>{
  const {call}=scripted(one=>isChild(one)?sse({text:['Checked.']}):delegateOnce([{title:'A',objective:'Check A.',profile:'analyze'}])(one))
  const events=await collect(withDelegation(vercel),apiSession({context:{currentRequest:REQUEST},model:{family:'openai-compatible',model:'fixture',call}}))
  const task=recordsOf(events)[0]!
  expect(task.state).toBe('complete')
  expect(task.tokens).toBeUndefined()
  const figures=lastCheckpoint(events).delegation!
  expect(figures.batches[0]!.children[0]!.tokens).toBeUndefined()
  expect(figures.batches[0]!.total.tokens).toBeUndefined()
  expect(figures.parent.tokens).toBeUndefined()
 })

 // Row 2, R7.
 it('sends a child’s request again once per task: 503 overloaded, then 429, then 503 is three requests and the task fails',async()=>{
  let childRequests=0
  const {call}=scripted(one=>{
   if(isChild(one)){childRequests++;return childRequests===2?new Response(JSON.stringify({error:{type:'rate_limit_error'}}),{status:429,headers:{'content-type':'application/json','retry-after':'1'}}):overloaded()}
   return delegateOnce([{title:'Check',objective:'Check the draft.',profile:'analyze'}],'The check could not run.')(one)
  })
  const events=await collect(withDelegation(vercel),apiSession({model:{family:'openai-compatible',model:'fixture',call}}))
  expect(childRequests).toBe(3)
  expect(recordsOf(events)[0]).toMatchObject({state:'failed',modelRequests:3,retried:{failure:{status:503}}})
 })

 // Row 2, R18.
 it('does not wait out a Retry-After over thirty seconds: the refusal stands, and nothing is sent again',async()=>{
  let childRequests=0
  const {call}=scripted(one=>{
   if(isChild(one)){childRequests++;return new Response(JSON.stringify({error:{type:'overloaded_error'}}),{status:503,headers:{'content-type':'application/json','retry-after':'60'}})}
   return delegateOnce([{title:'Check',objective:'Check the draft.',profile:'analyze'}],'The check could not run.')(one)
  })
  const events=await collect(withDelegation(vercel),apiSession({model:{family:'openai-compatible',model:'fixture',call}}))
  expect(childRequests).toBe(1)
  const task=recordsOf(events)[0]!
  expect(task).toMatchObject({state:'failed',modelRequests:1})
  expect(task.retried).toBeUndefined()
 },5_000)

 // Row 2, R20.
 it('marks the first byte at the answer’s body, not at its headers',async()=>{
  const frames=['data: '+JSON.stringify({id:'f',object:'chat.completion.chunk',model:'fixture',choices:[{index:0,delta:{role:'assistant',content:'Checked.'},finish_reason:null}]})+'\n\n','data: '+JSON.stringify({id:'f',object:'chat.completion.chunk',model:'fixture',choices:[{index:0,delta:{},finish_reason:'stop'}]})+'\n\n','data: [DONE]\n\n']
  const {call}=scripted(one=>{
   if(isChild(one))return new Response(new ReadableStream<Uint8Array>({start(controller){setTimeout(()=>{for(const frame of frames)controller.enqueue(new TextEncoder().encode(frame));controller.close()},80)}}),{headers:{'Content-Type':'text/event-stream'}})
   return delegateOnce([{title:'Check',objective:'Check the draft.',profile:'analyze'}])(one)
  })
  const task=recordsOf(await collect(withDelegation(vercel),apiSession({model:{family:'openai-compatible',model:'fixture',call}})))[0]!
  expect(task.state).toBe('complete')
  expect(task.firstByteMs).toBeGreaterThanOrEqual(60)
 })

 // Row 2, R5b.
 it('makes no retry that would take the child’s last request: the 503 is the failure reported',async()=>{
  const single:TaskProfile={id:'single',description:'One request.',instructions:'Answer at once.',capabilities:[],report:'analyze',budget:{modelRequests:1,toolCalls:1,milliseconds:60_000}}
  let childRequests=0
  const {call}=scripted(one=>{
   if(isChild(one)){childRequests++;return overloaded()}
   return delegateOnce([{title:'Check',objective:'Check the draft.',profile:'single'}],'No.')(one)
  })
  const task=recordsOf(await collect(withDelegation(vercel,{profiles:[...TASK_PROFILES,single]}),apiSession({model:{family:'openai-compatible',model:'fixture',call}})))[0]!
  expect(childRequests).toBe(1)
  expect(task).toMatchObject({state:'failed',failure:{code:'unavailable',status:503}})
  expect(task.retried).toBeUndefined()
 })

 // Row 11.
 it.each([408,504])('sends a %s that carries the overload code no second time: the child retry is for a 5xx other than 504',async(status)=>{
  let childRequests=0
  const {call}=scripted(one=>{
   if(isChild(one)){childRequests++;return overloaded(status)}
   return delegateOnce([{title:'Check',objective:'Check the draft.',profile:'analyze'}],'No.')(one)
  })
  const task=recordsOf(await collect(withDelegation(vercel),apiSession({model:{family:'openai-compatible',model:'fixture',call}})))[0]!
  expect(childRequests).toBe(1)
  expect(task.state).toBe('failed')
  expect(task.retried).toBeUndefined()
 })

 // Row 3.
 it('writes the running batch into the checkpoint, so a resume counts the parent’s requests as its own and the interrupted child’s in its figure',async()=>{
  const chat=new ChatContext()
  let parentSent=0
  const first=scripted(one=>{
   if(isChild(one))return sse({text:['Part A checks.']})
   parentSent++
   return delegateOnce([{title:'A',objective:'Check part A.',profile:'analyze'}],'Part A checks.')(one)
  })
  const events=await collect(withDelegation(vercel),apiSession({context:{chat,currentRequest:REQUEST},model:{family:'openai-compatible',model:'fixture',call:first.call}}))
  // The durable checkpoint saved before the child's request: the batch running, its child in it.
  const mid=events.filter((e):e is Extract<AssistantEvent,{type:'execution_checkpoint'}>=>e.type==='execution_checkpoint'&&e.durable===true).map(e=>e.checkpoint).find(c=>c.delegation?.batches[0]?.children[0]?.state==='running')!
  expect(mid.delegation!.batches[0]!.children[0]).toMatchObject({state:'running',modelRequests:1})
  expect(mid.delegation!.parent.modelRequests).toBe(1)
  expect(readExecution(mid)!.delegation).toEqual(mid.delegation)
  const sentBefore=1
  // The resume: the parent runs a second batch and answers.
  let resumedParent=0
  const second=scripted(one=>{
   if(isChild(one))return sse({text:['Part B checks.']})
   resumedParent++
   return delegateOnce([{title:'B',objective:'Check part B.',profile:'analyze'}],'Both check.')(one)
  })
  const again=await collect(withDelegation(vercel),apiSession({context:{chat,currentRequest:REQUEST,execution:mid},model:{family:'openai-compatible',model:'fixture',call:second.call}}))
  const end=lastCheckpoint(again)
  expect(end.status).toBe('complete')
  expect(parentSent).toBe(2)
  expect(end.delegation!.parent.modelRequests).toBe(sentBefore+resumedParent)
  expect(end.delegation!.batches.map(batch=>batch.children.map(child=>[child.state,child.modelRequests]))).toEqual([[['interrupted',1]],[['complete',1]]])
  expect(end.used).toBe(end.delegation!.parent.modelRequests!+2)
 })

 // Row 4.
 it('counts a Codex child’s agent runs: two with its summarize run, one after a busy admission refusal',async()=>{
  const LONG=Array.from({length:600},(_,i)=>`Observation ${i+1}: nothing unusual.`).join('\n')
  const figuresOf=async(child:(r:{prompt:string},attempt:number)=>Promise<string>,request:{title:string;objective:string;profile:string;summarize?:boolean})=>{
   let childAttempts=0
   const run=vi.fn<AgentRun>(async(r,cb)=>{
    const delegate=r.tools.find(t=>t.description.startsWith('delegate_tasks:'))
    if(delegate&&!r.prompt.includes('DELEGATION RESULT')){await cb.tool({id:'call',name:delegate.name,arguments:{tasks:[request]}},cb.signal);throw Error('Parent must yield')}
    const text=delegate?'Parent finished.':await child(r,++childAttempts)
    await cb.event({type:'message',id:'m',text,phase:'final'})
   })
   const {model:_model,thinking:_thinking,...base}=apiSession({context:{currentRequest:REQUEST}})
   const events=await collect(withDelegation(codex),{...base,agent:{model:'fixture',run}})
   return lastCheckpoint(events).delegation!.batches[0]!.children[0]!
  }
  const summarized=await figuresOf(async r=>r.prompt.includes('Summarize the findings below')?'Summary: nothing unusual.':LONG,{title:'Scan',objective:'Scan everything.',profile:'analyze',summarize:true})
  expect(summarized.agentRuns).toBe(2)
  const busy=await figuresOf(async(_r,attempt)=>{if(attempt===1)throw Object.assign(Error('busy'),{code:'busy'});return 'Checked.'},{title:'Check',objective:'Check it.',profile:'analyze'})
  expect(busy).toMatchObject({state:'complete',agentRuns:1})
 })

 // v0.5.0 release review: a checkpoint written by v0.4.0 on an agent capability carries modelRequests: 0 on its children and totals.
 it('restores an agent capability’s v0.4.0 batches without their modelRequests of 0',async()=>{
  const agentRun=()=>{
   const run=vi.fn<AgentRun>(async(r,cb)=>{
    const delegate=r.tools.find(t=>t.description.startsWith('delegate_tasks:'))
    if(delegate&&!r.prompt.includes('DELEGATION RESULT')){await cb.tool({id:'call',name:delegate.name,arguments:{tasks:[{title:'Scan',objective:'Scan everything.',profile:'analyze'}]}},cb.signal);throw Error('Parent must yield')}
    await cb.event({type:'message',id:'m',text:delegate?'Parent finished.':'Checked.',phase:'final'})
   })
   const {model:_model,thinking:_thinking,...base}=apiSession({context:{currentRequest:REQUEST}})
   return {run,base}
  }
  const first=agentRun()
  const written=lastCheckpoint(await collect(withDelegation(codex),{...first.base,agent:{model:'fixture',run:first.run}}))
  expect(written.delegation!.batches[0]!.children[0]).not.toHaveProperty('modelRequests')
  // The v0.4.0 shape: the zero written on each child and on the batch's total.
  const old=structuredClone(written) as ExecutionCheckpoint
  for(const batch of old.delegation!.batches){batch.total.modelRequests=0;for(const child of batch.children)child.modelRequests=0}
  const second=agentRun()
  const restored=lastCheckpoint(await collect(withDelegation(codex),{...second.base,context:{currentRequest:REQUEST,execution:JSON.parse(JSON.stringify(old))},agent:{model:'fixture',run:second.run}}))
  expect(restored.delegation!.batches).toHaveLength(2)
  expect(restored.delegation!.batches[0]).toEqual(written.delegation!.batches[0])
  for(const batch of restored.delegation!.batches){expect(batch.total).not.toHaveProperty('modelRequests');for(const child of batch.children)expect(child).not.toHaveProperty('modelRequests')}
 })

 // Row 8.
 it('fails a run that reported an error, keeps its open questions on the record, and never keeps it for reuse',async()=>{
  const chat=new ChatContext()
  let children=0
  const engine:Engine={id:'vercel',async *start(s){
   if(delegateIn(s)){await delegateIn(s)!.execute({tasks:[{title:'Receipts',objective:'Check the receipt rule.',profile:'analyze'}]},s.signal);yield {type:'end'};return}
   children++
   yield {type:'user_question',id:'q',question:{title:'Receipts',question:'Decline a claim with no receipt?',selection:'single',options:[{id:'a',label:'Yes',description:'Decline.'},{id:'b',label:'No',description:'Review.'}],recommendedOptionId:null,recommendation:null}}
   yield {type:'error',message:'The connection was lost.',failure:{version:1,code:'connection-lost',source:'model',outcome:'unknown'}}
   yield {type:'end'}
  }}
  const task=recordsOf(await collect(withDelegation(engine),apiSession({context:{chat}})))[0]!
  expect(task).toMatchObject({state:'failed',problem:'The connection was lost.',report:{openQuestions:[{kind:'question',text:expect.stringContaining('Decline a claim with no receipt?')}]}})
  await collect(withDelegation(engine),apiSession({context:{chat}}))
  expect(children).toBe(2)
 })

 // Row 9.
 it('keeps no result of a stopped response for reuse, and runs afresh where the host says delegationReuse false',async()=>{
  const chat=new ChatContext()
  let children=0
  const engine:Engine={id:'vercel',async *start(s){
   if(delegateIn(s)){
    const tasks=s.prompt.includes('BOTH')?[{title:'A',objective:'Check part A.',profile:'analyze'},{title:'B',objective:'Check part B.',profile:'analyze'}]:[{title:'A',objective:'Check part A.',profile:'analyze'}]
    await delegateIn(s)!.execute({tasks},s.signal);yield {type:'message',text:'Done.'};yield {type:'end'};return
   }
   children++
   if(s.prompt.includes('part B'))await new Promise<void>(resolve=>s.signal.addEventListener('abort',()=>resolve(),{once:true}))
   else yield {type:'message',text:'Part A checks.'}
   yield {type:'end'}
  }}
  // A completes, B is still running, and the person stops the response.
  const stop=new AbortController()
  for await(const event of withDelegation(engine).start(apiSession({prompt:'BOTH',signal:stop.signal,context:{chat}})))if(event.type==='delegated_task'&&event.task.title==='A'&&event.task.state==='complete')stop.abort()
  expect(children).toBe(2)
  // The next response runs A again: nothing of the stopped one was kept.
  const again=recordsOf(await collect(withDelegation(engine),apiSession({context:{chat}})))[0]!
  expect(children).toBe(3);expect(again.reused).toBeUndefined()
  // Delivered, it is reused; unless the host asks for a fresh run.
  const reused=recordsOf(await collect(withDelegation(engine),apiSession({context:{chat}})))[0]!
  expect(children).toBe(3);expect(reused.reused).toBe(again.id)
  const fresh=recordsOf(await collect(withDelegation(engine),apiSession({context:{chat,delegationReuse:false}})))[0]!
  expect(children).toBe(4);expect(fresh.reused).toBeUndefined()
 })

 // Row 10.
 it('runs identical tasks of one batch once: the second is given the first’s result',async()=>{
  const {sent,call}=scripted(one=>isChild(one)?sse({text:['The cap is 40.']}):delegateOnce([{title:'Cap',objective:'Find the meal cap.',profile:'analyze'},{title:'Cap, again',objective:'Find the meal cap.',profile:'analyze'}])(one))
  const [first,second]=recordsOf(await collect(withDelegation(vercel),apiSession({model:{family:'openai-compatible',model:'fixture',call}})))
  expect(sent.filter(isChild)).toHaveLength(1)
  expect(first).toMatchObject({state:'complete',result:'The cap is 40.',modelRequests:1})
  expect(second).toMatchObject({state:'complete',result:'The cap is 40.',reused:first!.id,modelRequests:0})
 })

 // Row 13.
 it('says in a resumed task’s earlier work that a result was partial',async()=>{
  const chat=new ChatContext()
  const quick:TaskProfile={id:'quick',description:'A short check.',instructions:'Check quickly.',capabilities:[],report:'analyze',budget:{modelRequests:2,toolCalls:2,milliseconds:150}}
  const coordinator=withDelegation(vercel,{profiles:[...TASK_PROFILES,quick]})
  const first=scripted((one,signal)=>{
   if(isChild(one))return new Response(new ReadableStream<Uint8Array>({start(controller){
    controller.enqueue(new TextEncoder().encode('data: '+JSON.stringify({id:'f',object:'chat.completion.chunk',model:'fixture',choices:[{index:0,delta:{content:'Trip 1 matches.'},finish_reason:null}]})+'\n\n'))
    signal?.addEventListener('abort',()=>controller.error(new DOMException('Stopped','AbortError')),{once:true})
   }}),{headers:{'Content-Type':'text/event-stream'}})
   return delegateOnce([{title:'Trips',objective:'Check every trip.',profile:'quick'}])(one)
  })
  const partial=recordsOf(await collect(coordinator,apiSession({context:{chat},model:{family:'openai-compatible',model:'fixture',call:first.call}})))[0]!
  expect(partial.partial).toBe(true)
  const second=scripted(one=>isChild(one)?sse({text:['The rest match.']}):delegateOnce([{title:'Trips',objective:'Check the rest.',profile:'quick',resume:partial.id}])(one))
  await collect(coordinator,apiSession({context:{chat},model:{family:'openai-compatible',model:'fixture',call:second.call}}))
  const text=promptOf(second.sent.find(isChild)!)
  const brief=JSON.parse(text.slice(text.lastIndexOf('\n{"objective"')+1).split('\n')[0]!) as {earlierWork:{partial?:true}[]}
  expect(brief.earlierWork).toEqual([expect.objectContaining({objective:'Check every trip.',result:'Trip 1 matches.',partial:true})])
 })

 // Row 14.
 it('keeps what the run asked of the parent in the text at more, where a cut report leaves it out',async()=>{
  const chat=new ChatContext()
  const long='Finding. '.repeat(60)
  const report={summary:'S'.repeat(800),findings:Array.from({length:12},(_,i)=>({text:`${i} ${long}`.slice(0,600)})),citations:[],openQuestions:[],confidence:'medium',notDone:[]}
  let answer:{tasks:AnsweredTask[]}|undefined
  const engine:Engine={id:'vercel',async *start(s){
   // Long objectives leave each task the least share of the answer.
   if(delegateIn(s)){answer=answered(await delegateIn(s)!.execute({tasks:['A','B','C'].map(t=>({title:t,objective:`Check part ${t}. `+'Read every receipt and every rule. '.repeat(50),profile:'analyze'}))},s.signal));yield {type:'end'};return}
   for(let i=0;i<3;i++)yield {type:'user_question',id:`q${i}`,question:{title:'Receipts',question:`Question ${i}: `+'Should a claim with no receipt be declined or sent for review? '.repeat(5),selection:'single',options:[{id:'a',label:'Decline',description:'Decline it.'},{id:'b',label:'Review',description:'Send it for review.'}],recommendedOptionId:null,recommendation:null}}
   yield {type:'message',text:'Findings.'+fence(report)}
   yield {type:'end'}
  }}
  await collect(withDelegation(engine),apiSession({context:{chat}}))
  const cut=answer!.tasks.find(task=>task.reportCut&&task.report!.openQuestions.length<3)!
  expect(cut).toBeDefined()
  const kept=String(chat.entries.get(cut.more!.id)!.value)
  for(let i=0;i<3;i++)expect(kept).toContain(`Question ${i}:`)
 })
})
