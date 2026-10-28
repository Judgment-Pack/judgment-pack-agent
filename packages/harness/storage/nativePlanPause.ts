import type {SessionContext} from '../context/session'
import type {AgentCallbacks} from '../agent'
import type {ExecutionCheckpoint} from '../execution/state'
import {readExecution} from '../execution/state'
import type {NativePlanPause,NativePlanInvocation,PlanDecision} from '../execution/plan'
import type {DurableBinding,DurableDispatch} from './writer'
import {ContinuationIdentityChanged,readStoredRunEvent} from './writer'
import type {RunStore,RunHead} from './run'
import {canonicalJSON} from '../context/chat'
import {validRecord,refuseExpired} from '../records'
import {storageId,STORAGE_PAGE_LIMIT,STORAGE_RECORD_BYTES} from './common'
import {providerReleaseAttempt,composeProviderRelease} from './release'
import {readDescriptorJSON} from '../descriptorSnapshot'

/** Live package-owned slot. It never crosses the provider callback boundary. */
export interface NativePlanAttempt {binding:DurableBinding;context:SessionContext;live:boolean;invalid:boolean;released:boolean;pause?:NativePlanPause;claim?:DurableDispatch}
const slots=new WeakSet<object>(),active=new WeakMap<SessionContext,NativePlanAttempt>()
export function nativePlanAttempt(context:SessionContext,binding:DurableBinding):NativePlanAttempt|undefined {
 if(!binding.root||binding.scope.role!=='author'||binding.scope.task!==undefined||!context.execution.state.planState?.accepted)return
 const slot:NativePlanAttempt={binding,context,live:true,invalid:false,released:false};slots.add(slot);return slot
}
export function validNativePlanAttempt(slot:NativePlanAttempt,binding:DurableBinding){return slots.has(slot)&&slot.live&&!slot.invalid&&slot.binding.writer===binding.writer&&slot.binding.root===binding.root&&canonicalJSON(slot.binding.scope)===canonicalJSON(binding.scope)}
/** Called only by the package native adapter's actual Plan-pause branch. */
export function markNativePlanPause(context:SessionContext):void {
 const slot=active.get(context),pending=context.execution.state.planState?.pending
 if(!slot)return
 if(!slot.live||!pending||pending.purpose!=='plan-drift'||context.execution.state.status!=='paused')throw new ContinuationIdentityChanged()
 slot.pause={callId:pending.callId,revision:pending.revision,candidateRevision:pending.candidate.planState.revision}
}
export async function runNativePlanAttempt(slot:NativePlanAttempt|undefined,callbacks:AgentCallbacks,run:(callbacks:AgentCallbacks)=>Promise<void>):Promise<void>{
 if(!slot)return run(callbacks)
 const release=providerReleaseAttempt(),pending=new Set<Promise<unknown>>()
 let live=true,problem:unknown
 const track=<T>(operation:()=>Promise<T>):Promise<T>=>{if(!live){slot.invalid=true;return Promise.reject(new ContinuationIdentityChanged())}const work=operation();pending.add(work);void work.then(()=>pending.delete(work),()=>pending.delete(work));return work}
 active.set(slot.context,slot)
 try{await run({...callbacks,providerRelease:composeProviderRelease(callbacks.providerRelease,release.capability),event:event=>track(()=>callbacks.event(event)),tool:(call,signal)=>track(()=>callbacks.tool(call,signal))})}catch(error){problem=error}finally{live=false;await Promise.allSettled([...pending]);slot.released=release.finish();slot.live=false;if(active.get(slot.context)===slot)active.delete(slot.context)}
 if(problem!==undefined)throw problem
}
const same=(a:unknown,b:unknown)=>canonicalJSON(a)===canonicalJSON(b)
const tuple=(checkpoint:ExecutionCheckpoint):NativePlanPause|undefined=>{const p=checkpoint.planState?.pending;return p?{callId:p.callId,revision:p.revision,candidateRevision:p.candidate.planState.revision}:undefined}
const identity=(row:DurableDispatch)=>({id:row.id,scope:row.scope,kind:row.kind,source:row.source,tool:row.tool,fingerprint:row.fingerprint,request:row.request,requestDigest:row.requestDigest,read:row.read})
const rowOf=(cp:ExecutionCheckpoint,id:string)=>cp.durable?.dispatches.find(row=>row.id===id)
const fail=():never=>{throw new ContinuationIdentityChanged()}
function depth(value:unknown,level=0):void {if(level>64)fail();if(value&&typeof value==='object')for(const child of Object.values(value))depth(child,level+1)}
export function nativePlanReceiptMatches(slot:NativePlanAttempt|undefined,binding:DurableBinding,claim:DurableDispatch,checkpoint:ExecutionCheckpoint):boolean {
 return !!slot&&slots.has(slot)&&!slot.live&&!slot.invalid&&slot.released&&!!slot.pause&&!!slot.claim&&same(slot.binding.scope,binding.scope)&&slot.binding.writer===binding.writer&&same(identity(slot.claim),identity(claim))&&checkpoint.status==='paused'&&same(slot.pause,tuple(checkpoint))&&claim.kind==='agent'&&claim.read&&binding.root&&binding.scope.role==='author'&&binding.scope.task===undefined
}
/** Trusted-resource consistency proof over immutable event records, not cryptographic authenticity.
 * Missing/pruned evidence closes continuation. The original outcome and audit row remain unknown. */
export async function verifyNativePlanEvidence(store:RunStore,head:RunHead,current:ExecutionCheckpoint,old:DurableDispatch,decision:PlanDecision|undefined,requireDecision:boolean,signal?:AbortSignal):Promise<NativePlanInvocation>{
 if(!head.checkpoint||!head.cursor||head.runId!==current.id||!old.planPause||old.kind!=='agent'||old.state!=='unknown'||!old.read||old.scope.execution!==current.id||old.scope.role!=='author'||old.scope.task!==undefined)fail()
 const invocation={dispatchId:old.id,requestDigest:old.requestDigest},pause=old.planPause
 if(decision&&(!same(decision.nativeInvocation,invocation)||!same(pause,{callId:decision.callId,revision:decision.revision,candidateRevision:decision.candidateRevision})))fail()
 if(requireDecision&&!decision)fail()
 if(!requireDecision&&(current.status!=='paused'||!same(tuple(current),pause)))fail()
 const seen=new Set<string>();let cursor:string|null=null,count=0,bytes=0,claimed=false,paused=false,decided=false,ended=false
 while(!ended){
  const page=await store.readEvents(current.id,cursor,STORAGE_PAGE_LIMIT,signal)
  if(!page||!Array.isArray(page.items)||page.items.length===0||page.items.length>STORAGE_PAGE_LIMIT||page.nextCursor!==page.items.at(-1)!.cursor)fail()
  for(const item of page.items){
   if(!storageId(item.cursor)||seen.has(item.cursor)||++count>4000||item.bytes.length>STORAGE_RECORD_BYTES||(bytes+=item.bytes.length)>32*1024*1024)fail()
   seen.add(item.cursor);depth(readDescriptorJSON(new TextDecoder('utf-8',{fatal:true}).decode(item.bytes)))
   const envelope=readStoredRunEvent(item.bytes),event=envelope.event
   if(event.type==='execution_checkpoint'){
    const checkpoint=readExecution(event.checkpoint)!
    if(checkpoint.id!==envelope.scope.execution)fail()
    const row=rowOf(checkpoint,old.id)
    if(row){
     if(!same(identity(row),identity(old))||!same(envelope.scope,old.scope)||!validRecord(checkpoint))fail()
     refuseExpired(checkpoint as {retainUntil:string})
     if(row.state==='claimed'){if(paused||decided||row.planPause)fail();claimed=true}
     if(row.planPause){
      if(!claimed||row.state!=='unknown'||!same(row,old)||!same(row.planPause,pause))fail()
      if(checkpoint.status==='paused'&&same(tuple(checkpoint),pause))paused=true
     }
    }
   }
   if(event.type==='plan_drift_decided'&&event.nativeInvocation?.dispatchId===old.id){
    if(!claimed||!paused||decided||!same(envelope.scope,old.scope)||event.execution!==current.id||!decision||!same(event,{type:'plan_drift_decided',version:2,execution:current.id,...decision}))fail()
    decided=true
   }
   cursor=item.cursor
   if(cursor===head.cursor){if(page.hasMore||item!==page.items.at(-1))fail();ended=true;break}
  }
  if(!ended&&!page.hasMore)fail()
 }
 if(!claimed||!paused||requireDecision&&!decided||!requireDecision&&decided)fail()
 const again=await store.load(current.id,signal)
 if(!again||again.runId!==head.runId||again.revision!==head.revision||again.cursor!==head.cursor||again.commitId!==head.commitId||!again.checkpoint||again.checkpoint.byteLength!==head.checkpoint!.byteLength||!again.checkpoint.every((value,index)=>value===head.checkpoint![index]))fail()
 return invocation
}
