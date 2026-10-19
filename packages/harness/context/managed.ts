import {failureEvent} from '../failures'
import {ExecutionPaused} from '../execution/state'
import type {AssistantEvent,EngineSession} from '../engine'
import {eventIterator,openRun} from '../engines/contract'
import {contextualSession,type SessionContext} from './session'
const contexts=new WeakMap<object,SessionContext>()
export function contextOf(session:EngineSession):SessionContext {
 const context=contexts.get(session);if(!context)throw Error('AI execution requires the shared Desk context boundary.');return context
}
/** All adapters enter here, including direct tests, critics and delegated runs.
 * Derived context lives only as long as the execution, or, where the host hands
 * the run a chat-scoped context, as long as the chat (agent#8). Originals remain in
 * the owning chat/research journal. Rebuilding cannot resurrect deleted snapshots. */
export function managedContext<T extends EngineSession>(session:T,run:(session:T)=>AsyncIterable<AssistantEvent>):AsyncIterable<AssistantEvent>{
 const gate=openRun(session,()=>{})
 return eventIterator({gate,open:()=>execute({...session,signal:gate.signal},run)})
}
async function* execute<T extends EngineSession>(session:T,run:(session:T)=>AsyncIterable<AssistantEvent>):AsyncGenerator<AssistantEvent>{
 let context:SessionContext|undefined
 let ended=false
 const tracked=!!session.context?.currentRequest||!!session.context?.execution
 let paused=false
 let failed=false
 try {
  session.signal.throwIfAborted()
  const ready=await contextualSession(session);context=ready.context
  contexts.set(ready.session,context)
  session.signal.throwIfAborted()
  if(tracked)yield {type:'execution_checkpoint',checkpoint:context.execution.snapshot()}
  for await (const event of run(ready.session)){
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
   if(event.type==='end'&&tracked&&!paused){context.execution.state.status=session.signal.aborted||failed?'stopped':'complete';yield {type:'execution_checkpoint',durable:true,checkpoint:context.execution.snapshot()}}
   if(session.context?.artifactProposals===false&&event.type==='proposal'){yield {type:'guardrail',tool:'proposal',action:'refused',detail:'Artifact proposals are unavailable in this conversation. Use its host tools or start a new artifact conversation.'};continue}
   if(session.context?.phase==='plan'&&event.type==='proposal'){yield {type:'guardrail',tool:'proposal',action:'refused',detail:'Artifact proposals are unavailable in Plan mode. Use propose_plan and wait for Start work.'};continue}
   if(event.type==='end')ended=true;yield event
  }
 } catch(error){
  if(ended)throw error
  if(!session.signal.aborted)yield failureEvent(error,'context')
  yield {type:'end'}
 } finally {context?.close()}
}
