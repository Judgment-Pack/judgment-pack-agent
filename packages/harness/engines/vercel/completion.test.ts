import {readFileSync} from 'node:fs'
import {expect,it,vi} from 'vitest'
import {vercel} from './index'
import {normalize} from '../../thinking'
import {readExecution,extendExecution} from '../../execution/state'
import type {AssistantEvent,AssistantSession} from '../../engine'

type Family='gemini'|'anthropic'|'openai-compatible'
const families:Family[]=['gemini','anthropic','openai-compatible']
type Step={reason?:'stop'|'length'|'content-filter'|'error';text?:string;tool?:string;args?:Record<string,unknown>;transportError?:boolean}
function wire(family:Family,step:Step):Response {
 if(step.transportError)return new Response('{"error":{"code":"server_error","message":"PRIVATE PROVIDER BODY"}}',{status:500,headers:{'content-type':'application/json'}})
 const reason=step.reason??'stop'
 let result:unknown
 if(family==='gemini')result={candidates:[{content:{role:'model',parts:step.tool?[{functionCall:{name:step.tool,args:step.args??{}}}]:step.text?[{text:step.text}]:[]},finishReason:{stop:'STOP',length:'MAX_TOKENS','content-filter':'SAFETY',error:'MALFORMED_FUNCTION_CALL'}[reason]}],usageMetadata:{promptTokenCount:100,candidatesTokenCount:0,thoughtsTokenCount:8192,totalTokenCount:8292}}
 else if(family==='anthropic')result={id:'m',type:'message',role:'assistant',model:'fixture',content:step.tool?[{type:'tool_use',id:'call',name:step.tool,input:step.args??{}}]:step.text?[{type:'text',text:step.text}]:[],stop_reason:step.tool?'tool_use':{stop:'end_turn',length:'max_tokens','content-filter':'refusal',error:'error'}[reason],stop_sequence:null,usage:{input_tokens:100,output_tokens:8192}}
 else result={id:'c',object:'chat.completion',created:0,model:'fixture',choices:[{index:0,message:{role:'assistant',content:step.text??null,...(step.tool?{tool_calls:[{id:'call',type:'function',function:{name:step.tool,arguments:JSON.stringify(step.args??{})}}]}:{})},finish_reason:step.tool?'tool_calls':{stop:'stop',length:'length','content-filter':'content_filter',error:'error'}[reason]}],usage:{prompt_tokens:100,completion_tokens:8192,total_tokens:8292,completion_tokens_details:{reasoning_tokens:8192}}}
 return new Response(JSON.stringify(result),{headers:{'content-type':'application/json'}})
}
function fixture(family:Family,steps:Step[]){
 const bodies:Record<string,any>[]=[],execute=vi.fn(async(_args?:unknown)=>({content:[{type:'text' as const,text:'VERIFIED CURRENT RESULT: zero=0, flag=false; action completed once.'}]}))
 const session:AssistantSession={prompt:'Apply the requested change and explain the result.',context:{currentRequest:{message:'Apply the requested change',input:'Current workspace object'}},testPrompt:'',tools:[],hostTools:[{name:'perform_task',effect:'workflow',description:'Perform the authorized task',inputSchema:{type:'object',properties:{revision:{type:'integer'}}},execute}],callTool:async()=>({}),model:{family,model:'fixture',call:async(_path,request)=>{bodies.push(JSON.parse(request.body));return wire(family,steps[bodies.length-1]??{transportError:true})}},thinking:normalize('off',family),signal:new AbortController().signal,allowConversation:true,interactive:true,adversarialReview:false}
 return {session,bodies,execute}
}
async function drain(session:AssistantSession){const events:AssistantEvent[]=[];for await(const event of vercel.start(session))events.push(event);return events}
const snapshot=(events:AssistantEvent[])=>events.filter(e=>e.type==='execution_checkpoint').at(-1)!

it.each(families.flatMap(family=>(['stop','length'] as const).map(reason=>({family,reason}))))('$family completes an empty $reason response once, with tools closed and completed results intact',async({family,reason})=>{
 const {session,bodies,execute}=fixture(family,[{tool:'perform_task'},{reason},{text:'The verified task is complete. No additional actions were taken.'}])
 const events=await drain(session)
 expect(events.filter(e=>e.type==='error'||e.type==='budget_paused')).toEqual([])
 expect(events.filter(e=>e.type==='response_recovery')).toHaveLength(1)
 expect(events.filter(e=>e.type==='message')).toEqual([{type:'message',text:'The verified task is complete. No additional actions were taken.'}])
 expect(execute).toHaveBeenCalledTimes(1);expect(bodies).toHaveLength(3)
 expect(bodies[2]!.tools??[]).toEqual([])
 expect(JSON.stringify(bodies[2])).toContain('VERIFIED CURRENT RESULT')
 expect(JSON.stringify(bodies[2])).toContain('Tools are unavailable')
 const saved=readExecution(JSON.parse(JSON.stringify(snapshot(events).checkpoint)))!
 expect(saved).toMatchObject({used:3,limit:20,grants:[],status:'complete'})
 expect(saved.completions?.map(c=>[c.request,c.phase,c.result.finishReason])).toEqual([[1,'work','tool-calls'],[2,'work',reason],[3,'synthesis','stop']])
 if(family==='gemini')expect(saved.completions?.[1]?.result.reasoningTokens).toBe(8192)
})
it.each(families)('%s never presents a length-truncated proposal as a complete proposal',async family=>{
 const partial='```json\n{"proposal":{"kind":"create","document":{"id":"truncated"},"unknowns":[]}}\n```'
 const {session,bodies}=fixture(family,[{text:partial,reason:'length'},{text:'The available results are insufficient to verify a complete change.'}])
 const events=await drain(session)
 expect(events.filter(e=>e.type==='proposal'||e.type==='error')).toEqual([])
 expect(events.filter(e=>e.type==='message')).toHaveLength(1)
 expect(JSON.stringify(bodies[1])).not.toContain('truncated')
})
it.each(families.flatMap(family=>(['stop','length'] as const).map(reason=>({family,reason}))))('$family bounds repeated empty $reason responses and persists a specific cause',async({family,reason})=>{
 const {session,bodies}=fixture(family,[{reason},{reason}]);const events=await drain(session)
 expect(bodies).toHaveLength(2)
 expect(events.filter(e=>e.type==='end')).toHaveLength(1)
 expect(events.find(e=>e.type==='error')).toMatchObject({failure:{code:reason==='length'?'output-limit':'invalid-response',completion:{finishReason:reason,outputTokens:8192}}})
 expect(snapshot(events).checkpoint).toMatchObject({used:2,limit:20,status:'stopped'})
})
it.each(families)('%s does not automatically retry transport failures',async family=>{
 const {session,bodies}=fixture(family,[{transportError:true}]);const events=await drain(session)
 expect(bodies).toHaveLength(1);expect(events.filter(e=>e.type==='response_recovery')).toEqual([])
 expect(events.find(e=>e.type==='error')).toMatchObject({failure:{code:'unavailable'}})
 expect(JSON.stringify(events)).not.toContain('PRIVATE PROVIDER BODY')
})
it.each(['gemini','openai-compatible'] as const)('%s reports content filtering without retrying or accepting partial output',async family=>{
 const {session,bodies}=fixture(family,[{reason:'content-filter',text:'Incomplete answer'}]);const events=await drain(session)
 expect(bodies).toHaveLength(1);expect(events.filter(e=>e.type==='message'||e.type==='proposal'||e.type==='response_recovery')).toEqual([])
 expect(events.find(e=>e.type==='error')).toMatchObject({failure:{code:'content-filter',completion:{finishReason:'content-filter'}}})
})
it('does not retry a malformed Gemini function call as if it were an empty successful answer',async()=>{
 const {session,bodies}=fixture('gemini',[{reason:'error'}]);const events=await drain(session)
 expect(bodies).toHaveLength(1);expect(events.find(e=>e.type==='error')).toMatchObject({failure:{code:'invalid-response',completion:{finishReason:'error'}}})
})
it.each(families)('%s refuses tool execution during the tools-disabled finishing request',async family=>{
 const {session,bodies,execute}=fixture(family,[{}, {tool:'perform_task'}]);const events=await drain(session)
 expect(bodies).toHaveLength(2);expect(execute).not.toHaveBeenCalled()
 expect(events.filter(e=>e.type==='message'||e.type==='proposal')).toEqual([])
 expect(events.find(e=>e.type==='error')).toBeDefined()
})
it('does not spend an additional request when the approved allowance is exhausted',async()=>{
 const {contextualSession}=await import('../../context/session')
 const {session,bodies}=fixture('gemini',[{reason:'length'}]);const ready=await contextualSession(session)
 const saved=ready.context.execution.snapshot('stopped');saved.used=19;session.context!.execution=saved
 const events=await drain(session)
 expect(bodies).toHaveLength(1)
 expect(events.find(e=>e.type==='error')).toMatchObject({failure:{code:'output-limit'}})
 expect(events.filter(e=>e.type==='response_recovery'||e.type==='budget_paused')).toEqual([])
 expect(snapshot(events).checkpoint.used).toBe(20)
})
it.runIf(!!process.env.JPS_CHAT_CONTEXT_STORE&&!!process.env.JPS_CHAT_CONTEXT_ID)('replays the saved failure continuation without modifying its journal or repeating its completed operations',async()=>{
 const path=process.env.JPS_CHAT_CONTEXT_STORE!,before=readFileSync(path,'utf8')
 const state=JSON.parse(before).chats.find((chat:any)=>chat.id===process.env.JPS_CHAT_CONTEXT_ID).checkpoint.state
 const latest=[...state.turns].reverse().find((turn:any)=>turn.role==='user')
 const {session,bodies,execute}=fixture('gemini',[{}, {text:'Completed checks are retained. The requested change still needs a verified proposal.'}])
 session.context={currentRequest:{message:latest.statement??latest.text,input:latest.input??latest.text},execution:structuredClone(state.execution)}
 if(session.context.execution!.used+2>session.context.execution!.limit)session.context.execution=extendExecution(session.context.execution!,true)
 session.prompt='Continue the requested change using retained results.'
 const events=await drain(session)
 expect(events.filter(e=>e.type==='error'||e.type==='budget_paused')).toEqual([])
 expect(execute).not.toHaveBeenCalled();expect(bodies).toHaveLength(2)
 expect(JSON.stringify(bodies[1])).toContain('resumeFromCompletedResults')
 expect(snapshot(events).checkpoint.used).toBe(state.execution.used+2)
 expect(readFileSync(path,'utf8')).toBe(before)
})

it.each(families)('%s lets a stalled model finish from retained results during its single recovery request',async family=>{
 const {session,bodies,execute}=fixture(family,[{tool:'perform_task'},{tool:'perform_task'},{tool:'perform_task'},{tool:'perform_task'},{text:'The retained results establish the current state; the proposed change has not been saved.'}])
 const events=await drain(session)
 expect(events.filter(e=>e.type==='error'||e.type==='budget_paused')).toEqual([])
 expect(execute).toHaveBeenCalledTimes(4)
 expect(bodies).toHaveLength(5);expect(bodies[4]!.tools?.length).toBeGreaterThan(0)
 expect(JSON.stringify(bodies[4])).toContain('returned unchanged results')
 expect(snapshot(events).checkpoint).toMatchObject({used:5,limit:20,status:'complete'})
})

it.each(families)('%s does not keep spending requests when a stalled model ignores tool closure',async family=>{
 const {session,bodies,execute}=fixture(family,Array.from({length:12},()=>({tool:'perform_task'})))
 const events=await drain(session)
 expect(execute).toHaveBeenCalledTimes(4);expect(bodies).toHaveLength(6)
 expect(bodies[5]!.tools??[]).toEqual([])
 expect(events.find(e=>e.type==='error')).toMatchObject({failure:{code:'repeated-work',source:'desk',completion:{finishReason:'tool-calls'}}})
 const saved=readExecution(JSON.parse(JSON.stringify(snapshot(events).checkpoint)))!
 expect(saved).toMatchObject({used:6,limit:20,grants:[],status:'stopped'})
 expect(saved.operations).toHaveLength(6)
 for(const refused of saved.operations.slice(4))expect(refused).toMatchObject({name:'perform_task',args:{},status:'failed',result:{structuredContent:{failure:{code:'repeated-work',source:'desk',outcome:'not-started'}}}})
})
it('projects exact working arguments and results before the next model request',async()=>{
 const {session,bodies}=fixture('gemini',[{tool:'perform_task'},{text:'Completed work is retained.'}])
 const events=await drain(session)
 expect(JSON.stringify(bodies[1])).toContain('resumeFromCompletedResults')
 expect(JSON.stringify(bodies[1])).toContain('/args')
 expect(JSON.stringify(bodies[1])).toContain('/result')
 const saved=snapshot(events).checkpoint
 expect(saved.context.entries.some(entry=>entry.id.includes(':operation-')&&JSON.stringify(entry.value).includes('VERIFIED CURRENT RESULT'))).toBe(true)
})

it.each(families)('%s allows a different useful action after repetition without increasing the allowance',async family=>{
 const {session,bodies,execute}=fixture(family,[...Array.from({length:4},()=>({tool:'perform_task'})),{tool:'perform_task',args:{revision:2}},{text:'The changed proposal was checked and is ready for review.'}])
 const events=await drain(session)
 expect(events.filter(e=>e.type==='error'||e.type==='budget_paused')).toEqual([])
 expect(execute).toHaveBeenCalledTimes(5)
 expect(execute.mock.calls.at(-1)?.[0]).toEqual({revision:2})
 expect(bodies).toHaveLength(6)
 expect(JSON.stringify(bodies[4])).toContain('one recovery request')
 expect(snapshot(events).checkpoint).toMatchObject({used:6,limit:20,grants:[],status:'complete'})
})
it.each(families)('%s journals unknown calls refused in finalization without dispatching them',async family=>{
 const {session,execute}=fixture(family,[...Array.from({length:4},()=>({tool:'perform_task'})),{tool:'perform_task'},{tool:'not_offered',args:{revision:3}}])
 session.callTool=vi.fn()
 const events=await drain(session)
 expect(execute).toHaveBeenCalledTimes(4);expect(session.callTool).not.toHaveBeenCalled()
 expect(snapshot(events).checkpoint.operations.at(-1)).toMatchObject({name:'not_offered',args:{revision:3},status:'failed',result:{structuredContent:{failure:{code:'repeated-work',outcome:'not-started'}}}})
})
it.each(families)('%s journals schema-invalid calls refused in finalization without executing them',async family=>{
 const {session,execute}=fixture(family,[...Array.from({length:4},()=>({tool:'perform_task'})),{tool:'perform_task'},{tool:'perform_task',args:{revision:'invalid'}}])
 const events=await drain(session)
 expect(execute).toHaveBeenCalledTimes(4)
 expect(snapshot(events).checkpoint.operations.at(-1)).toMatchObject({name:'perform_task',args:{revision:'invalid'},status:'failed',result:{structuredContent:{failure:{code:'repeated-work',outcome:'not-started'}}}})
})
it.each(['gemini','openai-compatible'] as const)('%s preserves a provider refusal instead of replacing it with the repetition cause',async family=>{
 const {session,bodies}=fixture(family,[...Array.from({length:4},()=>({tool:'perform_task'})),{reason:'content-filter'}])
 const events=await drain(session)
 expect(bodies).toHaveLength(5)
 expect(events.find(e=>e.type==='error')).toMatchObject({failure:{code:'content-filter',source:'model'}})
})
it('never extends the allowance automatically to recover repeated work',async()=>{
 const {contextualSession}=await import('../../context/session')
 const {session,bodies,execute}=fixture('gemini',Array.from({length:8},()=>({tool:'perform_task'})))
 const ready=await contextualSession(session),saved=ready.context.execution.snapshot('stopped')
 saved.used=15;session.context!.execution=saved
 const events=await drain(session)
 expect(execute).toHaveBeenCalledTimes(4);expect(bodies).toHaveLength(5)
 expect(bodies[4]!.tools??[]).toEqual([])
 expect(events.find(e=>e.type==='error')).toMatchObject({failure:{code:'repeated-work'}})
 expect(events.filter(e=>e.type==='budget_paused')).toEqual([])
 expect(snapshot(events).checkpoint).toMatchObject({used:20,limit:20,grants:[],status:'stopped'})
})
it('applies the same recovery boundary to runtime tools',async()=>{
 const {session,execute}=fixture('gemini',Array.from({length:8},()=>({tool:'read_decision'})))
 session.tools=[{name:'read_decision',description:'Read current decision',inputSchema:{type:'object',properties:{}}}]
 session.callTool=vi.fn(async()=>({structuredContent:{revision:1,document:{value:false}}}))
 const events=await drain(session)
 expect(execute).not.toHaveBeenCalled();expect(session.callTool).toHaveBeenCalledTimes(4)
 expect(snapshot(events).checkpoint.operations.slice(4)).toHaveLength(2)
 expect(events.find(e=>e.type==='error')).toMatchObject({failure:{code:'repeated-work'}})
})
it('continues fresh mutable observations without treating the same arguments as repeated work',async()=>{
 const {session,bodies,execute}=fixture('gemini',[...Array.from({length:7},()=>({tool:'perform_task'})),{text:'All changes have been observed.'}])
 let revision=0
 execute.mockImplementation(async()=>({content:[{type:'text',text:JSON.stringify({revision:++revision})}]}))
 const events=await drain(session)
 expect(execute).toHaveBeenCalledTimes(7);expect(bodies).toHaveLength(8)
 expect(events.filter(e=>e.type==='error'||e.type==='budget_paused')).toEqual([])
})

it.runIf(!!process.env.JPS_CHAT_CONTEXT_STORE&&!!process.env.JPS_CHAT_CONTEXT_ID)('recovers the saved retry trace with a different action and preserves the source journal',async()=>{
 const path=process.env.JPS_CHAT_CONTEXT_STORE!,before=readFileSync(path,'utf8')
 const state=JSON.parse(before).chats.find((chat:any)=>chat.id===process.env.JPS_CHAT_CONTEXT_ID).checkpoint.state
 const prior=state.execution.operations.filter((op:any)=>op.status==='complete').slice(-3)
 const {session,bodies}=fixture('gemini',[...prior.map((op:any)=>({tool:op.name,args:op.args})),{tool:'perform_task',args:{revision:2}},{text:'The different action completed using retained results.'}])
 const latest=[...state.turns].reverse().find((turn:any)=>turn.role==='user')
 session.context={currentRequest:{message:latest.statement??latest.text,input:latest.input??latest.text},execution:structuredClone(state.execution)}
 if(session.context.execution!.used+5>session.context.execution!.limit)session.context.execution=extendExecution(session.context.execution!,true)
 const retained=vi.fn(async(name:string)=>prior.find((op:any)=>op.name===name).result)
 session.tools=prior.map((op:any)=>({name:op.name,description:'Read the retained fixture observation',inputSchema:{type:'object',properties:{}}}))
 session.callTool=retained
 const events=await drain(session)
 expect(bodies,JSON.stringify(events.filter(e=>e.type==='error'))).toHaveLength(5)
 expect(JSON.stringify(bodies[3])).toContain('one recovery request')
 expect(events.filter(e=>e.type==='error'||e.type==='budget_paused')).toEqual([])
 expect(snapshot(events).checkpoint.used).toBe(state.execution.used+5)
 expect(readFileSync(path,'utf8')).toBe(before)
})

it.each(families)('%s does not start another finishing request after an empty stalled-loop final answer',async family=>{
 const {session,bodies}=fixture(family,[...Array.from({length:5},()=>({tool:'perform_task'})),{}])
 const events=await drain(session)
 expect(bodies).toHaveLength(6)
 expect(events.find(e=>e.type==='error')).toMatchObject({failure:{code:'repeated-work'}})
 expect(snapshot(events).checkpoint).toMatchObject({used:6,grants:[],status:'stopped'})
})
