import {PolicyRefusal} from '../policy'
import {failureError,failureEvent,toolFailureResult} from '../failures'
import {ExecutionPaused} from '../execution/state'
import type {AssistantEvent,EngineSession} from '../engine'
import {eventIterator,openRun} from '../engines/contract'
import {GateViolation} from '../toolGate'
import {approvalContinuation} from '../toolSources'
import {contextualSession,type SessionContext} from './session'
import {recordSession} from '../corpus'
const contexts=new WeakMap<object,SessionContext>()
export function contextOf(session:EngineSession):SessionContext {
 const context=contexts.get(session);if(!context)throw Error('AI execution requires the shared Desk context boundary.');return context
}
/** All adapters enter here, including direct tests, critics and delegated runs.
 * Derived context lives only as long as the execution, or, where the host hands
 * the run a chat-scoped context, as long as the chat (agent#8). Originals remain in
 * the owning chat/research journal. Rebuilding cannot resurrect deleted snapshots. */
export function managedContext<T extends EngineSession>(session:T,run:(session:T)=>AsyncIterable<AssistantEvent>,engine?:import('../host').AssistantEngine):AsyncIterable<AssistantEvent>{
 const recording=recordSession(session)
 const gate=openRun(recording.session,()=>{})
 return recording.stream(eventIterator({gate,open:()=>execute({...recording.session,signal:gate.signal},run,engine)}))
}
async function* execute<T extends EngineSession>(session:T,run:(session:T)=>AsyncIterable<AssistantEvent>,engine?:import('../host').AssistantEngine):AsyncGenerator<AssistantEvent>{
 let context:SessionContext|undefined
 let ended=false
 const tracked=!!session.context?.currentRequest||!!session.context?.execution
 let paused=false
 let failed=false
 // ADR-0008: the engine's results the boundary answered itself (a call refused while another waits
 // for the person), by call id, or by name where the events carry none.
 const held=new Set<string>()
 const heldByName=new Map<string,number>()
 const hold=(event:{callId?:string;name:string})=>{if(event.callId!==undefined)held.add(event.callId);else heldByName.set(event.name,(heldByName.get(event.name)??0)+1)}
 const release=(event:{callId?:string;name:string})=>{
  if(event.callId!==undefined)return held.delete(event.callId)
  const count=heldByName.get(event.name)??0
  if(count>0)heldByName.set(event.name,count-1)
  return count>0
 }
 /** The call id of the approval_requested event this run emitted, where it emitted one. */
 let requested:string|undefined
 try {
  session.signal.throwIfAborted()
  const ready=await contextualSession(session,engine);context=ready.context
  contexts.set(ready.session,context)
  yield {type:'policy_resolved',members:structuredClone(context.resolvedPolicy)}
  if(context.paceWarning)yield {type:'guardrail',tool:'pace',action:'refused',reason:'pace warning',detail:context.paceWarning}
  session.signal.throwIfAborted()
  if(tracked)yield {type:'execution_checkpoint',checkpoint:context.execution.snapshot()}
  // ADR-0008: a paused act call the person decided on. Approved: the decision is written and
  // acknowledged, the call runs once under its bounds, and its result is written and acknowledged
  // before anything else; refused, or restored with its result: written. Then the run's first request
  // carries the result as one data block (approvalContinuation).
  const step=context.execution.approvalStep
  if(step!==undefined){
   const write=async():Promise<AssistantEvent>=>{
    const checkpoint=context!.execution.snapshot()
    try{await session.context?.checkpoint?.(checkpoint)}catch{throw failureError('storage','storage','not-started')}
    return {type:'execution_checkpoint',durable:true,checkpoint}
   }
   if(step==='execute'){
    yield await write()
    const ran=await context.runApproved!(session.signal)
    context.execution.state.approval={...context.execution.state.approval!,state:'executed',result:ran.result}
    for(const notice of context.takeNotices())yield notice
    yield await write()
   }else if(step==='write'){
    yield await write()
    // The person refused the call: said once, after the refusal is written, with the refused result's
    // own sentence. The person's note is not in it; it reaches the model only inside the continuation's block.
    const refused=context.execution.state.approval!
    if(refused.state==='refused')yield {type:'guardrail',tool:refused.name,action:'refused',detail:refused.result?.content?.[0]?.text??'The person refused this call; it was not run and nothing was changed.',reason:'approval refused'}
   }
   ready.session.prompt=ready.session.prompt+'\n\n'+approvalContinuation(context.execution.state.approval!)
  }
  for await (let event of run(ready.session)){
   // agent#10: a refusal of the critic's call is the boundary's guardrail, put
   // on the stream before the result the engine reports for that call.
   for(const notice of context.takeNotices())yield notice
   // ADR-0008: an engine asks the host's approval policy where it hands an act call to the host
   // (SessionContext.approval) and delivers approval_requested in place of the call where it asks.
   // A call made while another waits is refused: said here with its call, its guardrail and its
   // refused result, whatever the engine does with the call after, and the engine's own result for
   // it is not delivered again.
   if(event.type==='approval_requested')requested=event.callId
   const pending=event.type==='tool_call'?context.sources?.pending(event):undefined
   if(event.type==='tool_call'&&pending!==undefined){
    const call=context.sources!.decorate(event)
    context.execution.observe(call)
    yield call
    yield {type:'guardrail',tool:event.name,action:'refused',detail:pending,reason:'approval pending'}
    const refused=toolFailureResult(new GateViolation(event.name,pending))
    const result=context.sources!.decorate({type:'tool_result',...(event.callId===undefined?{}:{callId:event.callId}),name:event.name,isError:true,text:refused.content[0].text,structured:refused.structuredContent})
    context.execution.observe(result)
    yield result
    hold(event)
    continue
   }
   if(event.type==='tool_result'&&release(event))continue
   // ADR-0008: the run ends once an act call waits for the person: the engine's own end, the
   // refused request's error, or a pause of its own all end it paused, with the checkpoint durable
   // and no error.
   if(context.execution.approvalAsked&&(event.type==='end'||event.type==='error'||event.type==='budget_paused')){
    const asked=context.execution.state.approval!
    if(requested!==asked.callId)yield {type:'approval_requested',callId:asked.callId,name:asked.name,tool:asked.tool,source:{...asked.source},args:structuredClone(asked.args)}
    paused=true;context.execution.state.status='paused'
    yield {type:'execution_checkpoint',durable:true,checkpoint:context.execution.snapshot('paused')}
    ended=true;yield {type:'end'};return
   }
   // ADR-0006: a tool with a source's call and result carry its source record.
   if(context.sources)event=context.sources.decorate(event)
   if(event.type==='error')failed=true
   context.execution.observe(event)
   if(event.type==='budget_paused'||event.type==='error'&&(event.failure?.code==='request-limit'||event.message===new ExecutionPaused().message)){
    paused=true;context.execution.state.status='paused'
    yield {type:'budget_paused',checkpoint:context.execution.snapshot('paused')};continue
   }
   if(event.type==='tool_result'&&context.execution.pauseRequested){yield event;context.execution.state.status='paused';yield {type:'budget_paused',checkpoint:context.execution.snapshot('paused')};yield {type:'end'};return}
   if(tracked&&(event.type==='tool_call'||event.type==='tool_result'||event.type==='user_question')){
    const tool=event.type==='user_question'?undefined:ready.session.hostTools.find(tool=>tool.name===event.name)
    const read=tool?.effect==='read'||tool?.delegation==='read'||tool?.delegation==='research'||event.type!=='user_question'&&['get_schema','get_authoring_instructions','list_context','read_context','list_examples','get_example'].includes(event.name)
    yield {type:'execution_checkpoint',durable:!read,checkpoint:context.execution.snapshot()}
   }
   // ADR-0010: the critic's decision is written durably: the checkpoint carrying it follows the event.
   if(event.type==='critic_decided'){yield event;if(tracked)yield {type:'execution_checkpoint',durable:true,checkpoint:context.execution.snapshot()};continue}
   if(event.type==='end'&&tracked&&!paused){context.execution.state.status=session.signal.aborted||failed?'stopped':'complete';yield {type:'execution_checkpoint',durable:true,checkpoint:context.execution.snapshot()}}
   if(session.context?.artifactProposals===false&&event.type==='proposal'){yield {type:'guardrail',tool:'proposal',action:'refused',detail:'Artifact proposals are unavailable in this conversation. Use its host tools or start a new artifact conversation.'};continue}
   if(session.context?.phase==='plan'&&event.type==='proposal'){yield {type:'guardrail',tool:'proposal',action:'refused',detail:'Artifact proposals are unavailable in Plan mode. Use propose_plan and wait for Start work.'};continue}
   if(event.type==='end')ended=true;yield event
  }
 } catch(error){
  if(ended)throw error
  if(error instanceof PolicyRefusal){
   yield error.event()
   if(error.checkpoint)yield {type:'execution_checkpoint',checkpoint:error.checkpoint}
  }
  if(!session.signal.aborted)yield failureEvent(error,'context')
  yield {type:'end'}
 } finally {context?.close()}
}
