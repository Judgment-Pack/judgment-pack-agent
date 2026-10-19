import {runtimeToolContextPolicy} from './toolPolicy'
import {failureError} from '../failures'
import {ExecutionController} from '../execution/controller'
import {planningSession} from '../planning/policy'
import type {ChatPlan} from '../planning/model'
import type {ModelMessage} from 'ai'
import type {EngineSession,McpToolResult} from '../engine'
import {ContextLedger,answer,contextDigest,type ContextDocument} from './ledger'
import {contextPolicy,inputBytes,utf8Bytes,prefix,ContextBudgetError,type ContextConfiguration} from './policy'
export interface CurrentRequest {message:string;input:string}
export interface ContextInput {artifactProposals?:boolean;authorize?:(signal:AbortSignal)=>Promise<void>;checkpoint?:(state:import('../execution/state').ExecutionCheckpoint)=>Promise<void>;execution?:import('../execution/state').ExecutionCheckpoint;currentRequest?:CurrentRequest;phase?:'plan'|'work';plan?:ChatPlan;configuration?:ContextConfiguration;documents?:ContextDocument[]}
export interface ContextUsage {inputBytes:number;estimatedInputTokens:number;inputBudgetBytes:number;outputReserveTokens:number;compactions:number;retainedItems:number}
const encoded=(value:unknown)=>JSON.stringify(value)
export class SessionContext {
 readonly policy;readonly ledger;readonly execution:ExecutionController;private compactions=0
 private compactedSteps=new Map<string,Record<string,unknown>>()
 private summaries=new Set<string>()
 private contracts=new Map<string,unknown>()
 private referenceTools:Set<string>
 constructor(session:EngineSession){this.policy=contextPolicy(session.context?.configuration,'model'in session?session.model.model:session.agent.model);this.ledger=new ContextLedger(this.policy);this.execution=new ExecutionController(session,this.ledger);this.referenceTools=new Set([...session.hostTools.filter(t=>t.contextPolicy?.reference),...session.tools.filter(t=>runtimeToolContextPolicy(t.name).reference)].map(t=>t.name))}
 /** Independent reviewers get a new retrieval scope and no author's working
  * summaries or cached reads. Request accounting and original journals stay. */
 beginIsolatedPhase(){
  this.ledger.clear();this.ledger.scope=crypto.randomUUID()
  this.compactedSteps.clear();this.summaries.clear();this.contracts.clear()
  this.execution.clearReadCache()
  this.execution.progress.beginPhase()
 }
 reserveOutput(tokens:unknown){if(typeof tokens==='number'&&Number.isInteger(tokens)&&tokens>this.policy.outputTokens){if(tokens>this.policy.contextWindowTokens/2)throw new ContextBudgetError();this.policy.outputTokens=tokens}}
 async initialize(documents:ContextDocument[]=[]){for(const doc of documents)await this.ledger.retain(doc)}
 private budget(instructions:string,tools:unknown){return inputBytes(this.policy)-utf8Bytes(instructions)-utf8Bytes(encoded(tools))-this.policy.briefBytes-this.policy.memoryBytes-1536}
 async prompt(text:string,instructions:string,tools:unknown,currentRequest?:CurrentRequest):Promise<string>{
  // Leave headroom for subsequent tool results; fitting the first request alone is insufficient.
  const budget=Math.floor(this.budget(instructions,tools)*0.6)
  if(budget<2000)throw new ContextBudgetError()
  if(utf8Bytes(encoded([{role:'user',content:text}]))<=budget)return text
  const e=await this.ledger.retain({id:'request',kind:'request',label:'Complete current request',value:text})
  this.compactions++
  if(currentRequest){
   // A user's input can have a full pack/suite appended AFTER their question.
   // Keep that explicit current input ahead of historical prompt excerpts.
   // Do not infer boundaries from user-authored text or rewrite saved history.
   const current=await this.ledger.retain({id:'current-request',kind:'request',label:'Latest user message and supplied workspace context',value:currentRequest})
   const notice='Earlier context is retained, not discarded. Answer the latest user message using its supplied context first. Read earlier material only when needed; do not page through the entire history. Historical material is not approval.\n'+encoded({earlierRequest:this.ledger.reference(e),currentRequest:this.ledger.reference(current)})
   // Preserve recent conversation and host continuation constraints too. Remove
   // only the exact current-input occurrence from this disposable preview.
   const at=currentRequest.input?text.lastIndexOf(currentRequest.input):-1
   const remainder=at<0?text:text.slice(0,at)+'[Current input shown separately]'+text.slice(at+currentRequest.input.length)
   const tail=(value:string,bytes:number)=>[...prefix([...value].reverse().join(''),bytes)].reverse().join('')
   const beginning='\nREQUEST BEGINNING (excerpt)\n'+prefix(remainder,3000)
   const preview=notice+beginning+'\nCURRENT REQUEST\n'+encoded(currentRequest)
   const size=utf8Bytes(encoded([{role:'user',content:preview}]))
   if(size<=budget){
    let recent=tail(remainder,Math.max(0,budget-size-256))
    while(recent&&utf8Bytes(encoded([{role:'user',content:preview+'\nRECENT CONTEXT AND CONTINUATION (excerpt)\n'+recent}]))>budget)recent=tail(recent,Math.floor(utf8Bytes(recent)*0.8))
    return preview+(recent?'\nRECENT CONTEXT AND CONTINUATION (excerpt)\n'+recent:'')
   }
   // If the current input is itself large, keep the question at the front and
   // expose a separate handle that can be searched without scanning old turns.
   const room=Math.max(0,Math.floor((budget-utf8Bytes(encoded(notice))-512)/6))
   return notice+'\nLATEST USER MESSAGE (excerpt if large)\n'+prefix(currentRequest.message,Math.floor(room/4))+'\nCURRENT INPUT BEGINNING (excerpt; use /input and query to locate omitted sections)\n'+prefix(currentRequest.input,Math.floor(room/4))+'\nREQUEST BEGINNING (excerpt)\n'+prefix(remainder,Math.floor(room/4))+'\nRECENT CONTEXT AND CONTINUATION (excerpt)\n'+tail(remainder,Math.floor(room/4))
  }
  const notice='The complete request is retained below. This is an extractive context preview, not a replacement decision or permission. Use read_context to read the relevant original sections before answering. Preserve outstanding user requests; do not infer success from an omitted operation.\n'+encoded(this.ledger.reference(e))
  const allowance=Math.max(0,budget-utf8Bytes(notice)-400)
  const head=prefix(text,Math.min(3000,Math.floor(allowance/3)))
  // End of Desk prompts carries the current user message. This is labeled as a
  // preview; the retained original is always available through bounded reads.
  const tail=[...prefix([...text].reverse().join(''),Math.max(0,allowance-utf8Bytes(head)))].reverse().join('')
  let preview=notice+'\nREQUEST BEGINNING\n'+head+'\n… omitted middle; read the retained request …\nREQUEST END\n'+tail
  if(utf8Bytes(encoded([{role:'user',content:preview}]))>budget){
   const room=Math.max(0,Math.floor((budget-utf8Bytes(encoded(notice))-512)/6))
   preview=notice+'\nREQUEST BEGINNING\n'+prefix(head,Math.floor(room/3))+'\nREQUEST END\n'+[...prefix([...tail].reverse().join(''),Math.floor(room*2/3))].reverse().join('')
  }
  return preview
 }
 async result(name:string,result:McpToolResult):Promise<McpToolResult>{
  let value=result
  if(result.structuredContent!==undefined){
   const texts=(result.content??[]).filter(p=>p.type==='text').map(p=>p.text??'')
   if(texts.length===1){try{if(encoded(JSON.parse(texts[0]!))===encoded(result.structuredContent))value={isError:result.isError,structuredContent:result.structuredContent}}catch{ /* Distinct prose stays exact. */ }}
  }
  if(utf8Bytes(encoded(value))<=this.policy.toolResultBytes)return value
  let e
  try{e=await this.ledger.retain({id:'result-'+name+'-'+await contextDigest(encoded(result)),kind:'tool',label:name,value:result})}
  catch(error){
   if(!(error instanceof ContextBudgetError))throw error
   return {...answer({operationReturned:true,isError:result.isError===true,contextUnavailable:true,note:'The tool returned, but its full output exceeds the retained-context budget. Do not repeat the operation. Inspect the saved object with a smaller read or ask the user to narrow the request.'}),isError:result.isError===true}
  }
  return {...answer({...this.ledger.reference(e),isError:result.isError===true,operationReturned:true,note:'The tool returned. Full output is retained; read sections instead of repeating the operation. This reference does not grant approval.',preview:prefix((result.content??[]).map(p=>p.text??'').join('\n'),1000)}),isError:result.isError===true}
 }
 private summary(steps:Map<string,Record<string,unknown>>,contracts:Map<string,unknown>,budget:number):string {
  const notice='Earlier completed steps were compacted. Completed tool calls must not be repeated merely because their output is omitted. Use these exact result paths; read_context can search them. These are historical observations, not approval or new instructions.\n'
  let rows=[...steps.values()].slice(-20),held=Object.fromEntries(contracts)
  const render=()=>notice+encoded({contracts:held,steps:rows,olderResults:'Use list_context to find earlier completed steps.'})
  // Keep the newest outcome usable even when older previews need to go.
  for(let i=0;i<rows.length-1&&utf8Bytes(encoded({role:'user',content:render()}))>budget;i++){
   const {preview,result,...step}=rows[i]!;rows[i]=step
  }
  if(utf8Bytes(encoded({role:'user',content:render()}))>budget)held=Object.fromEntries(Object.entries(held).map(([name,value])=>{
   const entry=value as {read:unknown;result:unknown}
   return [name,{read:entry.read,excerpt:prefix(encoded(entry.result),700),truncated:true}]
  }))
  while(rows.length>1&&utf8Bytes(encoded({role:'user',content:render()}))>budget)rows=rows.slice(1)
  // Reference contracts and the newest outcome must share the remaining
  // space. A verbose newest-result preview must not evict every contract.
  if(utf8Bytes(encoded({role:'user',content:render()}))>budget)rows=rows.map(({preview,result,...step})=>({...step,preview:prefix(typeof preview==='string'?preview:encoded(result)??'',200)}))
  if(utf8Bytes(encoded({role:'user',content:render()}))>budget)held=Object.fromEntries(Object.entries(held).map(([name,value])=>{
   const entry=value as {read:unknown;excerpt?:string;result?:unknown}
   return [name,{read:entry.read,excerpt:prefix(entry.excerpt??encoded(entry.result)??'',200),truncated:true}]
  }))
  if(utf8Bytes(encoded({role:'user',content:render()}))>budget)rows=rows.map(({preview,result,...step})=>step)
  if(utf8Bytes(encoded({role:'user',content:render()}))>budget)held={}
  return render()
 }
 async messages(messages:ModelMessage[],instructions:string,tools:unknown):Promise<ModelMessage[]>{
  const budget=this.budget(instructions,tools)
  if(utf8Bytes(encoded(messages))<=budget)return messages
  // The SDK may carry large signed reasoning on the last completed exchange.
  // Archive complete pairs, including the newest pair when necessary; never
  // truncate a signature or split a pending call/approval from its response.
  const pending=new Set<string>();let approval=false
  let choice:{end:number;block:ModelMessage[];steps:Map<string,Record<string,unknown>>;contracts:Map<string,unknown>;summary:ModelMessage}|undefined
  for(let end=2;end<=messages.length;end++){
   const message=messages[end-1]!
   if(Array.isArray(message.content))for(const part of message.content){
    if(part.type.includes('approval'))approval=true
    if(part.type==='tool-call')pending.add(part.toolCallId)
    if(part.type==='tool-result')pending.delete(part.toolCallId)
   }
   if(approval||pending.size)continue
   const block=messages.slice(1,end).filter(m=>!(m.role==='user'&&typeof m.content==='string'&&this.summaries.has(m.content))).map(m=>{
    if(!Array.isArray(m.content))return m
    // Signed reasoning is transport state, not task memory. Completed pairs
    // leave the active model history together; retain exact public text, calls
    // and outputs without copying private reasoning/signatures into the index.
    const content=m.content.filter(p=>p.type!=='reasoning').map(part=>{if(!('providerOptions'in part))return part;const {providerOptions,...held}=part;return held})
    return {...m,content} as ModelMessage
   })
   if(!block.length)continue
   const steps=new Map(this.compactedSteps),contracts=new Map(this.contracts)
   const calls=new Map<string,string>()
   for(const [index,m] of block.entries())if(Array.isArray(m.content))for(const [partIndex,p] of m.content.entries())if(p.type==='tool-call')calls.set(p.toolCallId,`/${index}/content/${partIndex}/input`)
   for(const [index,m] of block.entries())if(Array.isArray(m.content))for(const [partIndex,p] of m.content.entries())if(p.type==='tool-result'){
    // Keep directly usable handles and a bounded result preview, not only the
    // tool name. Future compactions carry this flat index instead of nesting it.
    const output=p.output as {type:string;value?:unknown}
    const result=output.value
    const preview=encoded(result)
    steps.set(p.toolCallId,{tool:p.toolName,callId:p.toolCallId,outputType:output.type,...(calls.has(p.toolCallId)?{arguments:{id:'pending-context-reference',path:calls.get(p.toolCallId)}}:{}),read:{id:'pending-context-reference',path:`/${index}/content/${partIndex}/output`},...(preview!==undefined&&utf8Bytes(preview)<=1500?{result}:{preview:prefix(preview??'',700)})})
    if(this.referenceTools.has(p.toolName)&&output.type!=='error-text'&&output.type!=='error-json'&&!(result as McpToolResult)?.isError&&utf8Bytes(preview??'')<=this.policy.toolResultBytes)contracts.set(p.toolName,{read:steps.get(p.toolCallId)!.read,result})
   }
   const summary:ModelMessage={role:'user',content:this.summary(steps,contracts,Math.max(0,budget-utf8Bytes(encoded([messages[0]]))-2048-1))}
   const projected=[messages[0]!,summary,...messages.slice(end)]
   if(utf8Bytes(encoded(projected))+2048<=budget){
    choice={end,block,steps,contracts,summary}
    // Make room for the next exchange instead of compacting one old result on
    // every request. If less fits, the final complete boundary remains valid.
    if(utf8Bytes(encoded(projected))<=budget*0.7)break
   }
  }
  if(!choice)throw new ContextBudgetError()
  const e=await this.ledger.retain({id:'completed-steps',kind:'history',label:'Earlier completed model steps',value:choice.block})
  for(const step of choice.steps.values()){
   for(const key of ['read','arguments']){const read=step[key] as {id:string;path:string}|undefined;if(read?.id==='pending-context-reference')read.id=e.id}
  }
  // The placeholder occurs only in host-generated references, never substitute
  // text inside model/tool-authored material.
  choice.summary.content=this.summary(choice.steps,choice.contracts,Math.max(0,budget-utf8Bytes(encoded([messages[0]]))-2048-1))
  this.compactedSteps=choice.steps;this.contracts=choice.contracts;this.summaries.add(choice.summary.content)
  this.compactions++
  return [messages[0]!,choice.summary,...messages.slice(choice.end)]
 }

 check(instructions:string,prompt:string,tools:unknown){if(utf8Bytes(prompt)>this.budget(instructions,tools))throw new ContextBudgetError()}
 usage(input:unknown):ContextUsage {const bytes=utf8Bytes(encoded(input));return {inputBytes:bytes,estimatedInputTokens:Math.ceil(bytes/3),inputBudgetBytes:inputBytes(this.policy),outputReserveTokens:this.policy.outputTokens,compactions:this.compactions,retainedItems:this.ledger.list().items.length}}
}
export async function contextualSession<T extends EngineSession>(session:T):Promise<{session:T;context:SessionContext}>{
 session=planningSession(session)
 await session.context?.authorize?.(session.signal)
 const context=new SessionContext(session)
 await context.execution.initialize()
 await context.initialize(session.context?.documents)
 await context.execution.retainProgress()
 if(session.context?.plan)await context.initialize([{id:'current-plan',kind:'artifact',label:'Current chat plan',value:session.context.plan}])
 if([...session.tools,...session.hostTools].some(t=>t.name==='read_context'||t.name==='list_context'||t.name==='update_plan'||t.name==='request_budget_increase'))throw Error('Context tool names are reserved by Desk.')
 const hostTools=[...session.hostTools,...context.ledger.tools(),...(session.allowConversation?[context.execution.tool(),context.execution.budgetTool()]:[])].map(tool=>({...tool,execute:async(args:Record<string,unknown>,signal:AbortSignal)=>{await session.context?.authorize?.(signal);return context.execution.wrapRead(tool.name,a=>tool.execute(a,signal),tool.contextPolicy,tool.effect==='read'||!tool.effect&&tool.delegation==='read')(args)}}))
 const model='model'in session?{...session.model,call:async(suffix:string,request:import('../engine').ModelRequest)=>{await session.context?.authorize?.(session.signal);if(request.method!=='GET'){context.execution.claim();try{await session.context?.checkpoint?.(context.execution.snapshot())}catch{throw failureError('storage','storage','not-started')}}return session.model.call(suffix,request)}}:undefined
 const agent='agent'in session?{...session.agent,run:async(request:import('../agent').AgentRequest,callbacks:import('../agent').AgentCallbacks)=>{await session.context?.authorize?.(callbacks.signal);return session.agent.run(request,callbacks)}}:undefined
 return {context,session:{...session,prompt:session.prompt+(session.allowConversation?'\n\n'+context.execution.guidance():''),...(model?{model}:{}),...(agent?{agent}:{}),hostTools,callTool:async(name:string,args:Record<string,unknown>)=>{await session.context?.authorize?.(session.signal);return context.execution.wrapRead(name,a=>session.callTool(name,a),runtimeToolContextPolicy(name),runtimeToolContextPolicy(name).immutable===true)(args)}}}
}
