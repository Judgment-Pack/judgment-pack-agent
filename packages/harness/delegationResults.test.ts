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
import { DEFAULT_CHILD_CONCURRENCY, fitReport, withDelegation } from './delegation'
import { DELEGATE_TOOL, MAX_BATCHES, REPORT_BOUNDS, TASK_PROFILES, readReport, reportInstructions, validDelegatedTask, type DelegatedTask, type ReportKind, type TaskProfile, type TaskReport } from './delegationContract'
import type { AssistantEvent, AssistantSession, Engine, EngineSession, McpToolResult } from './engine'
import { ChatContext } from './context/chat'
import { codex } from './engines/codex'
import { vercel } from './engines/vercel'
import { normalize } from './thinking'
import { readExecution, validDelegationFigures, type ExecutionCheckpoint } from './execution/state'
import taskSchema from '../../contract/schemas/delegated-task.json'
import provenanceSchema from '../../contract/schemas/provenance.json'
import analyzeVector from '../../contract/vectors/delegation/report-analyze.json'
import reviewVector from '../../contract/vectors/delegation/report-review.json'
import researchVector from '../../contract/vectors/delegation/report-research.json'
import authorTestVector from '../../contract/vectors/delegation/report-author-test.json'
import figuresVector from '../../contract/vectors/delegation/figures.json'
import answerVector from '../../contract/vectors/delegation/answer.json'

afterEach(()=>{vi.restoreAllMocks();vi.useRealTimers()})

const apiSession=(patch:Partial<AssistantSession>={}):AssistantSession=>({prompt:'PARENT PRIVATE HISTORY',testPrompt:'',tools:[],callTool:vi.fn(),hostTools:[],signal:new AbortController().signal,allowConversation:true,interactive:true,adversarialReview:false,model:{family:'openai-compatible',model:'fixture',call:vi.fn(async()=>new Response('{}'))},thinking:normalize('off','openai-compatible'),...patch})
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
  expect(task.report!.notDone).toContain('The task reached its time limit before it finished.')
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
  const readTool={name:'read_page',delegation:'read' as const,description:'Read a page.',inputSchema:{type:'object'},execute:vi.fn(async()=>({content:[{type:'text' as const,text:'Page.'}]}))}
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

 it('runs as many children at once as the connection serves requests: three where its record says three, two where it says none',async()=>{
  const peaks:number[]=[]
  for(const capabilities of [{concurrentRequests:3},{}]){
   let active=0,peak=0
   const engine:Engine={id:'vercel',async *start(s){
    if(delegateIn(s)){await delegateIn(s)!.execute({tasks:['A','B','C'].map(t=>({title:t,objective:`Check part ${t}.`,profile:'analyze'}))},s.signal);yield {type:'end'};return}
    active++;peak=Math.max(peak,active);await new Promise(r=>setTimeout(r,20));active--
    yield {type:'message',text:'Checked.'};yield {type:'end'}
   }}
   await collect(withDelegation(engine),apiSession({model:{family:'openai-compatible',model:'fixture',call:vi.fn(),capabilities}}))
   peaks.push(peak)
  }
  expect(peaks).toEqual([3,DEFAULT_CHILD_CONCURRENCY])
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
})
