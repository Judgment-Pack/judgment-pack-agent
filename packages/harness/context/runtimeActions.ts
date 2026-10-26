/** Runtime action approvals use the actual connector and the original durable writer. */
import type {SessionContext} from './session'
import type {EngineSession,McpToolResult,AssistantEvent} from '../engine'
import {canonicalJSON} from './chat'
import {durableOwner,scopeFor,hostSucceeded} from '../storage/writer'
import {prepareRuntimeAttempt,claimRuntimeAttempt,executeRuntimeAttempt,runtimePolicy,runtimeJSON,type RuntimeAttempt} from '../storage/runtimeActionOwner'
import type {ApprovalRecord} from '../execution/state'
import {sha256} from '../records'
import type {RuntimeAdmission} from '../storage/runtimeActionOwner'
export interface RuntimeCall {
 readonly args:Record<string,unknown>
 readonly requested?:Extract<AssistantEvent,{type:'approval_requested'}>
 execute(signal:AbortSignal):Promise<McpToolResult>
}
export function installRuntimeActions(context:SessionContext,session:EngineSession,admission:()=>RuntimeAdmission){
 const acting=(name:string)=>session.tools.some(t=>t.name===name&&t._meta?.effect==='act')
 const check=(name:string,args:Record<string,unknown>)=>{
  context.admit(name,'runtime',args)
  if(context.storageRole!=='author'||session.context?.phase==='plan'||session.context?.objective||session.childDefinition)throw context.refuse(name,'This phase admits no Runtime action. Nothing was sent.','approval refused')
  if(!acting(name))throw context.refuseUnoffered(name,args)
 }
 const execute=async(attempt:RuntimeAttempt,callId:string,signal:AbortSignal)=>{
  claimRuntimeAttempt(attempt)
  const {name,args,source,runtime}=attempt.request
  check(name,args);signal.throwIfAborted()
  const binding=session.context?.[durableOwner]
  if(!binding)throw context.refuse(name,'Runtime actions require the original durable run store. Nothing was sent.','approval refused')
  const request=canonicalJSON({version:1,callId,name,args,source,runtime})
  const invoke=()=>binding.writer.invoke(scopeFor(binding,context.storageRole),'runtime','sha256:'+sha256(canonicalJSON({source,runtime})),name,request,false,async raw=>{
   if(raw!==request)throw context.refuse(name,'The prepared Runtime action changed. Nothing was sent.','approval stale')
   check(name,args)
   return executeRuntimeAttempt(attempt,admission,signal,async()=>{check(name,args);await session.context?.authorize?.(signal)})
  },hostSucceeded,()=>[],signal)
  context.claimTestCall(name)
  const call=()=>context.execution.wrapRead(name,invoke,{},false,{runtime:true,offered:true})(runtimeJSON(args))
  return context.sources?context.sources.runtime(call):call()
 }
 context.runtimeNeedsApproval=acting
 context.prepareRuntimeCall=async(name,args,callId)=>{
  if(!acting(name))return
  const release=context.beginApproval(name)
  try {
   check(name,args)
   const binding=session.context?.[durableOwner]
   if(!binding)throw context.refuse(name,'Runtime actions require the original durable run store. Nothing was sent.','approval refused')
   const clean=context.output.value(args,'tool argument',true)
   await session.context?.authorize?.(session.signal);session.signal.throwIfAborted()
   const attempt=await prepareRuntimeAttempt(session.callTool,name,clean,admission(),session.signal)
   const answer=runtimePolicy(attempt)
   if(answer==='refuse')throw context.refuse(name,"The host refused this Runtime action. Nothing was sent.",'approval refused')
   if(answer==='ask'){
    if(context.claimApprovalPause&&!context.claimApprovalPause())throw context.refuse(name,'The invocation did not claim this approval pause. Nothing was sent.','approval pending')
    const record:ApprovalRecord={...runtimeJSON(attempt.request),callId,state:'asked',marker:context.marker}
    context.execution.mutate({approval:record,version:3,status:'paused'})
    try{await binding.writer.save(binding,context.execution.snapshot('paused'),session.signal)}catch(error){context.execution.mutate({approval:undefined,status:'stopped'});throw error}
    session.signal.throwIfAborted()
    return {args:runtimeJSON(attempt.request.args),requested:{type:'approval_requested',...runtimeJSON(attempt.request),callId},execute:async()=>{throw context.refuse(name,'The Runtime action waits for the person. Nothing was sent.','approval pending')}}
   }
   return {args:runtimeJSON(attempt.request.args),execute:signal=>execute(attempt,callId,signal)}
  }finally{release()}
 }
 const record=context.execution.state.approval
 if(context.execution.approvalStep==='execute'&&record?.source.kind==='runtime')context.runApproved=async signal=>{
  if(!record.runtime)throw context.refuse(record.name,'The saved Runtime action has no current execution binding. Nothing was sent.','approval stale')
  check(record.name,record.args)
  const attempt=await prepareRuntimeAttempt(session.callTool,record.name,record.args,admission(),signal,record.runtime)
  if(canonicalJSON(attempt.request.source)!==canonicalJSON(record.source)||canonicalJSON(attempt.request.args)!==canonicalJSON(record.args))throw context.refuse(record.name,'The saved Runtime action changed. Nothing was sent.','approval stale')
  return {result:await execute(attempt,record.callId,signal),ran:true}
 }
}
