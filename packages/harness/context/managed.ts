import {durableOwner,durableSession} from '../storage/writer'
import {guardConnectionNotices} from '../session'
import {OutputRefused,RecordsRefused,outputWhere} from '../outputGuard'
import {PolicyRefusal} from '../policy'
import {RecordsExpired} from '../records'
import {versionEvents,writeRunEvent,UnsupportedRecordVersion,UnknownRecordMember} from '../recordReader'
import {authoredFailure,failureError,failureEvent,toolFailureResult} from '../failures'
import {ExecutionPaused,RequestCapRefusal} from '../execution/state'
import type {AssistantEvent,EngineSession,RequestSession,RequestSessionFor} from '../engine'
import {eventIterator,openRun,withAbort} from '../engines/contract'
import {GateViolation} from '../toolGate'
import {approvalContinuation} from '../toolSources'
import {contextualSession,type SessionContext} from './session'
import {recordSession} from '../corpus'
import {canonicalJSON} from './chat'
import {contextDigest} from './ledger'
const contexts=new WeakMap<object,SessionContext>()
export function contextOf(session:RequestSession):SessionContext {
 const context=contexts.get(session);if(!context)throw Error('AI execution requires the shared Desk context boundary.');return context
}
/** All adapters enter here, including direct tests, critics and delegated runs.
 * Derived context lives only as long as the execution, or, where the host hands
 * the run a chat-scoped context, as long as the chat (agent#8). Originals remain in
 * the owning chat/research journal. Rebuilding cannot resurrect deleted snapshots. */
export function managedContext<T extends EngineSession>(session:T,run:(session:RequestSessionFor<T>)=>AsyncIterable<AssistantEvent>,engine?:import('../host').AssistantEngine):AsyncIterable<AssistantEvent>{
 session={...session,context:session.context?{...session.context}:undefined}
 const recording=recordSession(session)
 const gate=openRun(recording.session,()=>{})
 return versionEvents(recording.stream(versionEvents(eventIterator({gate,open:()=>persistedExecute({...recording.session,signal:gate.signal} as T,run,engine)}))))
}
/** Notices carry model-derived identifiers and refusal details, independently of tool results.
 * Guard-generated labels are trusted metadata and must not be scanned again. */
function guardNotice(notice:AssistantEvent,context:SessionContext):AssistantEvent {
 if(notice.type==='edit_refused'){const [document,reason]=context.output.value([notice.document,notice.reason] as const,'draft edit');if(document!==undefined&&[...document].length>512)context.output.refuse('draft edit','invalid-output');return {...notice,...(document===undefined?{}:{document}),reason}}
 if(notice.type==='edit_applied'){const document=context.output.value(notice.document,'draft edit');if([...document].length>512)context.output.refuse('draft edit','invalid-output');return {...notice,document}}
 if(notice.type==='guardrail'&&notice.reason!=='output refused'){const [tool,detail]=context.output.value([notice.tool,notice.detail] as const,'message');return {...notice,tool,detail}}
 return notice
}
function takeNotices(context:SessionContext):AssistantEvent[]{
 const out:AssistantEvent[]=[]
 for(const notice of context.takeNotices()){
  try{out.push(guardNotice(notice,context))}catch(error){if(!(error instanceof OutputRefused))throw error}
 }
 // These are the labels just produced by guarding notices, never tool results.
 return [...out,...context.takeNotices()]
}
async function* persistedExecute<T extends EngineSession>(session:T,run:(session:RequestSessionFor<T>)=>AsyncIterable<AssistantEvent>,engine?:import('../host').AssistantEngine):AsyncGenerator<AssistantEvent>{
 let events:AsyncGenerator<AssistantEvent>|undefined
 try{
  session=await withAbort(()=>durableSession(session),session.signal)
  events=execute(session,run,engine)
  for(;;){
   const step=await withAbort(()=>events!.next(),session.signal)
   if(step.done)return
   const event=step.value,binding=session.context?.[durableOwner]
   yield binding&&!binding.writer.failed?await withAbort(()=>binding.writer.event(binding,event,session.signal),session.signal):writeRunEvent(event)
  }
 }catch(error){if(!session.signal.aborted){if(error instanceof UnsupportedRecordVersion||error instanceof UnknownRecordMember||error instanceof RecordsExpired)yield writeRunEvent(error.event());yield writeRunEvent(failureEvent(error,'storage'));yield writeRunEvent({type:'end'})}}
 finally{void events?.return(undefined).catch(()=>undefined)}
}
async function* execute<T extends EngineSession>(session:T,run:(session:RequestSessionFor<T>)=>AsyncIterable<AssistantEvent>,engine?:import('../host').AssistantEngine):AsyncGenerator<AssistantEvent>{
 let releaseNotices:(()=>void)|undefined
 let context:SessionContext|undefined
 let ended=false
 const tracked=!!session.context?.[durableOwner]||!!session.context?.currentRequest||!!session.context?.execution
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
  releaseNotices=guardConnectionNotices(session.callTool,notice=>{
   try{return writeRunEvent(guardNotice(notice,ready.context))}catch(error){if(!(error instanceof OutputRefused))throw error;return undefined}
  })
  // Only names declared by the host, Runtime or reserved harness offer are trusted.
  // Keep dispatch on the original name: an unknown call still earns the gate's refusal.
  const declaredNames=new Set([...ready.session.values.tools,...ready.session.values.hostTools].map(tool=>tool.name))
  const deliveredNames=new Map<string,string>()
  const guardName=(name:string,call=false):string=>{
   if(declaredNames.has(name))return name
   if(!call&&deliveredNames.has(name))return deliveredNames.get(name)!
   let clean:string
   try{clean=context!.output.value(name,'message');if(context!.output.mode!=='off'&&[...clean].length>150)context!.output.refuse('message','invalid-output')}
   catch(error){if(!(error instanceof OutputRefused))throw error;clean=error.message}
   deliveredNames.set(name,clean);return clean
  }
  contexts.set(ready.session,context)
  yield {type:'policy_resolved',members:structuredClone(context.resolvedPolicy)}
  function* checkpoint(status=context!.execution.state.status,durable?:boolean,paused=false):Generator<AssistantEvent>{
   try{const saved=context!.execution.snapshot(status);yield paused?{type:'budget_paused',checkpoint:saved}:{type:'execution_checkpoint',...(durable===undefined?{}:{durable}),checkpoint:saved}}
   catch(error){if(!(error instanceof RecordsRefused))throw error;for(const notice of takeNotices(context!))yield notice}
  }
  if(context.paceWarning)yield {type:'guardrail',tool:'pace',action:'refused',reason:'pace warning',detail:context.paceWarning}
  session.signal.throwIfAborted()
  if(tracked)yield* checkpoint()
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
    if(!session.context?.checkpoint)throw authoredFailure('configuration','DurableStoreRequired: an approved act requires acknowledged persistence.','storage')
    yield await write()
    const ran=await context.runApproved!(session.signal)
    context.execution.mutate({approval:{...context.execution.state.approval!,state:'executed',result:ran.result}})
    for(const notice of takeNotices(context))yield notice
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
   // Output is held before host delivery; dispatch independently guards tool arguments.
   if(context.output.mode!=='off'&&(event.type==='message_progress'||event.type==='reasoning'&&!event.done))continue
   if(event.type==='reasoning')event={...event,text:context.output.value(event.text,'message')}
   if(event.type==='message')event={...event,text:context.output.value(event.text,'message')}
   if(event.type==='tool_call')event={...event,name:guardName(event.name,true),args:context.output.value(event.args,outputWhere(event.name),context.output.mode==='refuse')}
   if(event.type==='tool_result')event={...event,name:guardName(event.name)}
   if(event.type==='user_question')event={...event,question:context.output.value(event.question,'tool argument')}
   if(event.type==='delegation_requested')event={...event,request:context.output.value(event.request,'tool argument')}
   if(event.type==='proposal'){
    const clean=context.output.value(event,'proposal')
    if(JSON.stringify(clean)!==JSON.stringify(event)){
     let checked:import('../engine').McpToolResult={isError:true}
     try{checked=await ready.session.callTool('validate',{content:JSON.stringify(clean.document)})}catch{session.signal.throwIfAborted();context.output.refuse('proposal','proposal-validation')}
     let result=checked.structuredContent as {status?:string}|undefined
     if(result===undefined){try{result=JSON.parse((checked.content??[]).filter(p=>p.type==='text').map(p=>p.text??'').join('\n'))}catch{}}
     if(checked.isError||result?.status!=='valid')context.output.refuse('proposal','proposal-validation')
    }
    const {digest:_,...proposal}=clean
    event={...clean,digest:`sha256:${await contextDigest(canonicalJSON(proposal))}`}
   }
   event=guardNotice(event,context)
   // agent#10: a refusal of the critic's call is the boundary's guardrail, put
   // on the stream before the result the engine reports for that call.
   for(const notice of takeNotices(context))yield notice
   const checkpoints=event.type==='tool_call'||event.type==='tool_result'||event.type==='user_question'||event.type==='budget_paused'||event.type==='error'&&(event.failure?.code==='request-limit'||event.message===new ExecutionPaused().message)
   if(context.execution.approvalAsked||checkpoints)for(const notice of context.execution.queueSteers(ready.session.input,context.policy))yield notice
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
    paused=true;context.execution.mutate({status:'paused'})
    context.execution.mutate({usage:context.execution.usage()})
    yield* checkpoint('paused',true)
    yield {type:'usage',usage:structuredClone(context.execution.state.usage!)}
    ended=true;yield {type:'end'};return
   }
   // ADR-0006: a tool with a source's call and result carry its source record.
   if(context.sources)event=context.sources.decorate(event)
   if(event.type==='critique'){try{event=context.execution.recordCritique(event)}catch(error){if(!(error instanceof RecordsRefused))throw error;for(const notice of takeNotices(context))yield notice;continue}}
   if(event.type==='error')failed=true
   context.execution.observe(event)
   if(event.type==='budget_paused'||event.type==='error'&&(event.failure?.code==='request-limit'||event.message===new ExecutionPaused().message)){
    paused=true;context.execution.mutate({status:'paused'})
    context.execution.mutate({usage:context.execution.usage()})
    yield* checkpoint('paused',undefined,true);continue
   }
   if(event.type==='tool_result'&&context.execution.pauseRequested){yield event;context.execution.mutate({status:'paused'});context.execution.mutate({usage:context.execution.usage()});yield* checkpoint('paused',undefined,true);yield {type:'usage',usage:structuredClone(context.execution.state.usage!)};yield {type:'end'};return}
   if(tracked&&(event.type==='tool_call'||event.type==='tool_result'||event.type==='user_question')){
    const tool=event.type==='user_question'?undefined:ready.session.values.hostTools.find(tool=>tool.name===event.name)
    const read=tool?.effect==='read'||tool?.delegation==='read'||tool?.delegation==='research'||event.type!=='user_question'&&['get_schema','get_authoring_instructions','list_context','read_context','list_examples','get_example'].includes(event.name)
    yield* checkpoint(context.execution.state.status,!read)
   }
   // ADR-0010: the critic's decision is written durably: the checkpoint carrying it follows the event.
   if(event.type==='critic_decided'){yield event;if(tracked)yield* checkpoint(context.execution.state.status,true);continue}
   if(event.type==='end'&&!paused)context.execution.mutate({usage:context.execution.usage()})
   if(event.type==='end'&&tracked&&!paused){context.execution.mutate({status:session.signal.aborted||failed?'stopped':'complete'});yield* checkpoint(context.execution.state.status,true)}
   if(session.context?.artifactProposals===false&&event.type==='proposal'){yield {type:'guardrail',tool:'proposal',action:'refused',detail:'Artifact proposals are unavailable in this conversation. Use its host tools or start a new artifact conversation.'};continue}
   if(session.context?.phase==='plan'&&event.type==='proposal'){yield {type:'guardrail',tool:'proposal',action:'refused',detail:'Artifact proposals are unavailable in Plan mode. Use propose_plan and wait for Start work.'};continue}
   if(event.type==='end'){yield {type:'usage',usage:structuredClone(context.execution.state.usage!)};ended=true}yield event
  }
 } catch(error){
  if(ended)throw error
  if(error instanceof RequestCapRefusal){
   if(!session.signal.aborted)yield failureEvent(error,'context')
   yield {type:'usage',usage:context?.execution.state.usage??{rows:[]}}
   yield {type:'end'};return
  }
  if(error instanceof OutputRefused){
   const notices=context?takeNotices(context):[]
   for(const notice of notices)yield notice
   if(!notices.some(e=>e.type==='guardrail'&&e.reason==='output refused'))yield {type:'guardrail',tool:'output',action:'refused',reason:'output refused',detail:error.message}
   let usage=context?context.execution.usage():{rows:[]}
   if(context)context.execution.mutate({usage:usage})
   if(context&&tracked){try{yield {type:'execution_checkpoint',checkpoint:context.execution.snapshot('stopped')}}catch(held){if(!(held instanceof RecordsRefused))throw held;for(const notice of takeNotices(context))yield notice}}
   if(context)usage=context.execution.state.usage??usage
   yield {type:'usage',usage}
   yield {type:'end'};return
  }
  if(error instanceof PolicyRefusal){
   yield error.event()
   if(error.checkpoint)yield {type:'execution_checkpoint',checkpoint:error.checkpoint}
  }
  if(error instanceof RecordsExpired||error instanceof UnsupportedRecordVersion||error instanceof UnknownRecordMember)yield error.event()
  if(!session.signal.aborted)yield failureEvent(error,'context')
  const usage=context?.execution.state?context.execution.usage():{rows:[]}
  if(context?.execution.state)context.execution.mutate({usage:usage})
  yield {type:'usage',usage}
  yield {type:'end'}
 } finally {releaseNotices?.();context?.close()}
}
