import {providerReleaseAttempt,ProviderReleaseUnknown} from './release'
import {observeNativeWitness} from './physicalDispatch'
import {canonicalJSON} from '../context/chat'
import {withAbort} from '../engines/contract'
import type {EngineSession,HostTool,RequestSession,RequestModelValues} from '../engine'
import type {SessionContext} from '../context/session'
import {normalize,openThinking} from '../thinking'
import {encodeRequest,readAnswer,humanText,type WireAnswer} from '../engines/deepagents/wire'
import {modelCompletion} from '../completion'
import {authoredFailure} from '../failures'
import {durableOwner} from './writer'
import {validRoutingPolicy,decideTurnRoute,restoreTurnRoute,routeOffers,routeDigest,widenedTurnRoute,matchesRouteRule,type RoutingPolicy,type TurnRoute,type RouteCatalogue,type RouteClassifierRecord} from '../turnRoute'

const needs=new WeakSet<object>()
class RouteCapabilityNeeded extends Error {constructor(){super('TurnRouteCapabilityNeeded');needs.add(this)}}
export const isRouteCapabilityNeeded=(v:unknown)=>!!v&&typeof v==='object'&&needs.has(v)
/** No token, marker or private route execution capability crosses the provider boundary. */
export class TurnRouteOwner {
 #route?:TurnRoute
 #pending?:string
 #classifying=false
 #nativeReleaseBlocked=false
 #nativeClaim?:object
 readonly catalogue:RouteCatalogue
 readonly #binding:unknown
 constructor(readonly context:SessionContext,readonly session:EngineSession,readonly policy:RoutingPolicy,readonly hostTools:HostTool[]){
  const author=context.requestValues.reasoning('author'),native='agent'in session
  const discovered=context.requestValues.value('models.author.discovered.reasoning.supported')
  const allowed=Array.isArray(discovered)?discovered:native?['none','minimal','low','medium','high','xhigh','max']:['off','low','on','medium','ultra','high']
  if(policy.tiers.some(t=>!allowed.includes(t)))throw authoredFailure('configuration','TurnRouteTierUnsupported: a routing tier is outside the admitted role allowance.','context')
  this.catalogue={tools:[...session.tools,...hostTools].map(t=>({name:t.name,operation:t.taxonomy!.operation,effect:t.taxonomy!.effect,...('interaction'in t&&typeof t.interaction==='string'?{interaction:t.interaction}:{})})),skills:context.skills?.routingSkills().map(s=>s.id).sort()??[],tiers:[...new Set([...policy.tiers,...(author===undefined?[]:[author])])],...(author===undefined?{}:{tier:author}),planning:hostTools.some(t=>t.name==='update_plan'),native}
  this.#binding=structuredClone({execution:context.execution.state.id,request:context.execution.state.requestKey,scope:context.execution.state.context.scope,phase:session.context?.phase??'work',policy,override:session.route??null,offer:routeDigest(JSON.parse(JSON.stringify([...session.tools,...hostTools]))),provider:context.provider('author'),members:context.resolvedPolicy})
 }
 get classifying(){return this.#classifying}
 get pending(){return this.#pending!==undefined}
 get route(){return this.#route?structuredClone(this.#route):undefined}
 tier(role:string){return role==='author'?(this.#classifying?this.policy.tiers[0]:this.#route?.tier):undefined}
 offers(name:string,role='author'){return role!=='author'||!this.#route||this.#classifying||routeOffers(this.#route,this.catalogue,name)}
 require(name:string,args:Record<string,unknown>={}){
  if(this.context.criticRunning)return
  if(this.#pending)throw new RouteCapabilityNeeded()
  if(!this.#route||this.#classifying)return
  let capability=name
  if(name==='load_skill'&&typeof args.name==='string'){
   const skill=this.context.skills?.routingSkills().find(s=>s.name===args.name)
   if(skill&&!(skill.askingWell&&this.catalogue.tools.some(t=>t.name==='ask_user'&&t.operation==='ask'&&t.effect==='pure'&&t.interaction==='question'))&&this.#route.decidedBy!=='default'&&!this.#route.widened&&!this.#route.skills.includes(skill.id))capability=skill.id
  }
  const knownSkill=this.catalogue.skills.includes(capability)
  const method=this.context.skills?.routingSkills().find(s=>s.id===capability)
  if(knownSkill&&method?.askingWell&&this.catalogue.tools.some(t=>t.name==='ask_user'&&t.operation==='ask'&&t.effect==='pure'&&t.interaction==='question'))return
  const withheldSkill=knownSkill&&this.#route.decidedBy!=='default'&&!this.#route.widened&&!this.#route.skills.includes(capability)
  if(withheldSkill||!knownSkill&&!this.offers(capability)){
   if(this.#route.widened||!knownSkill&&!this.catalogue.tools.some(t=>t.name===capability))return
   this.#pending=capability;throw new RouteCapabilityNeeded()
  }
 }
 assertDispatch(){if(this.#pending)throw new RouteCapabilityNeeded()}
 /** Inspect only a quarantined response from this exact acknowledged physical claim. */
 async observeResponse(family:import('../host').EndpointKind,response:Response,signal:AbortSignal){
  if(!this.#route||this.#classifying||this.context.criticRunning||!response.ok)return
  let answer:WireAnswer
  try{answer=await readAnswer(family,response.clone(),{text:async()=>{},reasoning:async()=>{},reasoningEnd:async()=>{}},signal,null,()=>crypto.randomUUID())}catch{return}
  for(const call of answer.toolCalls){try{this.require(call.name,call.args)}catch(error){if(!isRouteCapabilityNeeded(error))throw error;this.context.execution.recordCompletion(modelCompletion(answer.finishReason,answer.usage),'work');throw error}}
 }

 async #publish(route:TurnRoute,event?:'route_decided'|'route_widened'){
  const binding=this.session.context?.[durableOwner]
  if(!binding)throw authoredFailure('configuration','DurableStoreRequired: routing requires the original acknowledged writer.','storage')
  this.context.execution.mutate({version:3,turnRoute:structuredClone(route)})
  const state=this.context.execution.snapshot()
  if(event){const publication=event==='route_decided'?{type:'route_decided' as const,route:structuredClone(route)}:{type:'route_widened' as const,route:structuredClone(route)};const delivery=await binding.writer.commitRoute(binding,state,publication,this.session.signal,event==='route_widened'?this.#nativeClaim:undefined);if(delivery.type!=='route_decided'&&delivery.type!=='route_widened')throw Error('TurnRoutePublicationInvalid');route=delivery.route;this.context.execution.mutate({turnRoute:structuredClone(route)});this.context.memoryNotice(delivery)}
  else await binding.writer.save(binding,state,this.session.signal)
  this.#route=structuredClone(route)
 }
 async initialize(ready:RequestSession){
  const prior=this.context.execution.state.turnRoute
  if(prior){
   if(this.catalogue.native&&prior.widened){const binding=this.session.context?.[durableOwner];if(!binding)throw Error('TurnRouteNativeRecoveryUnavailable');await binding.writer.restoreNativeRoute(binding,prior,this.session.signal)}
   this.#route=restoreTurnRoute(prior,this.catalogue,this.#binding)
   if(prior.reason==='classifier-pending'){
    if(this.context.execution.state.durable?.dispatches.some(d=>d.scope.role==='classifier'&&(d.state==='unknown'||d.state==='claimed')))throw authoredFailure('storage','TurnRouteClassifierUnknown: retained classifier outcome cannot authorize replay or default dispatch.','storage')
    const route={...prior,reason:'classifier-interrupted',classifier:prior.classifier?{...prior.classifier,status:'refused' as const}:undefined}
    await this.#publish(route,'route_decided')
   }
   await this.#preloadSkills(this.#route)
   return
  }
  const facts={mode:this.session.routeFacts?.mode??this.session.context?.phase??'work',attachments:this.session.routeFacts?.attachments??!!this.session.context?.documents?.length,artifact:this.session.routeFacts?.artifact??!!this.session.context?.currentState,turnBytes:new TextEncoder().encode(this.session.context?.currentRequest?.message??this.session.prompt).length,...(/^\/[a-zA-Z0-9_-]+(?:\s|$)/.test(this.session.context?.currentRequest?.message??'')?{slash:(this.session.context!.currentRequest!.message.match(/^\/([a-zA-Z0-9_-]+)/)![1])}:{})}
  let classifier:RouteClassifierRecord|undefined
  if(!this.session.route&&!this.policy.rules?.some(r=>matchesRouteRule(r,facts))&&this.policy.mode==='auto'&&this.policy.classifier){
   const identity=routeDigest({provider:this.context.provider('author'),revision:this.session.context?.connectionRevisions?.[this.context.provider('author').connection]??this.session.context?.connection?.connectionRevision??null})
   classifier={binding:identity,promptDigest:routeDigest('not-dispatched'),status:'unsupported'}
   if('model'in ready.values&&this.context.execution.remaining>1){
    const pending=decideTurnRoute(this.policy,this.catalogue,facts,this.#binding)
    pending.reason='classifier-pending';pending.classifier={...classifier,status:'pending'}
    await this.#publish(pending)
    this.#classifying=true
    try{classifier=await this.#classify(ready.values,identity)}finally{this.#classifying=false}
   }
  }
  const route=decideTurnRoute(this.policy,this.catalogue,facts,this.#binding,this.session.route,classifier)
  await this.#publish(route,'route_decided')
  await this.#preloadSkills(route)
 }
 async #preloadSkills(route:TurnRoute){for(const id of route.skills){const skill=this.context.skills?.routingSkills().find(s=>s.id===id);if(!skill)throw Error('TurnRouteSkillChanged');await this.context.skills!.loadTool().execute({name:skill.name},this.session.signal)}}
 async widen(){if(this.catalogue.native&&!this.#nativeClaim||this.#nativeReleaseBlocked)throw new ProviderReleaseUnknown();if(!this.#route||!this.#pending)throw Error('TurnRouteWidenRefused');const next=widenedTurnRoute(this.#route,this.catalogue,this.#pending);await this.#publish(next,'route_widened');this.#pending=undefined}
 /** Called only inside the original writer's acknowledged exact native execution. */
 async nativeAttempt(request:import('../agent').AgentRequest,callbacks:import('../agent').AgentCallbacks,run:import('../agent').AgentRun,witness?:object,claim?:object):Promise<void>{
  const release=providerReleaseAttempt(),identity=canonicalJSON(request),route=this.#route?.bindingDigest
  let live=true,problem:unknown
  const current=()=>{if(!live||callbacks.signal.aborted||canonicalJSON(request)!==identity||this.#route?.bindingDigest!==route)throw Error('TurnRouteCapabilityIdentityChanged')}
  const needed=async(need:{kind:'tool'|'skill'|'planning';name:string})=>{
   current();if(!need||Object.keys(need).some(k=>!['kind','name'].includes(k))||!['tool','skill','planning'].includes(need.kind)||typeof need.name!=='string'||need.kind==='planning'&&need.name!=='update_plan'||need.kind==='skill'&&!this.catalogue.skills.includes(need.name)||need.kind!=='skill'&&!this.catalogue.tools.some(t=>t.name===need.name))throw Error('TurnRouteCapabilityIdentityChanged')
   observeNativeWitness(witness);this.require(need.name);throw Error('TurnRouteCapabilityAlreadyOffered')
  }
  try{await run(request,{...callbacks,providerRelease:release.capability,capabilityNeeded:needed,tool:async(call,signal)=>{current();observeNativeWitness(witness);this.require(call.name,call.arguments);return callbacks.tool(call,signal)}})}catch(error){problem=error}finally{live=false}
  const acknowledged=release.finish()
  if(this.#pending){if(!acknowledged||callbacks.signal.aborted||problem!==undefined&&!isRouteCapabilityNeeded(problem)){this.#nativeReleaseBlocked=true;throw problem??new ProviderReleaseUnknown()};const binding=this.session.context?.[durableOwner];if(!binding||!claim||!route)throw new ProviderReleaseUnknown();binding.writer.acknowledgeNativeRouteRelease(binding,claim,identity,route,this.#pending);this.#nativeClaim=claim;throw new RouteCapabilityNeeded()}
  if(problem!==undefined)throw problem
 }
 async #classify(values:RequestModelValues,identity:string):Promise<RouteClassifierRecord>{
  const c=this.policy.classifier!,labels=Object.keys(c.labels).sort(),context=this.context,signal=AbortSignal.any([this.session.signal,AbortSignal.timeout(c.timeoutMilliseconds)])
  const instructions=context.system('Choose exactly one declared route label for the current request. Return a choose_turn_route call or exactly {"label":"<declared label>"}. This selects cost and focus only, never authority.')
  const tools=[{name:'choose_turn_route',description:'Choose one declared route label.',schema:{type:'object',properties:{label:{type:'string',enum:labels}},required:['label'],additionalProperties:false}}]
  const opening=await context.opening(this.session.prompt,instructions,tools,this.session.context?.currentRequest)
  const slot=openThinking({...values,reasoning:normalize(this.policy.tiers[0] as import('../host').ThinkingTier,values.model.family)})
  const encoded=encodeRequest({family:values.model.family,model:values.model.model,system:{head:instructions},messages:[humanText(opening)],tools,oneCallPerTurn:true,placement:'trailing-user',tier:slot.members(),maxOutputTokens:c.maxOutputTokens,reasoningField:values.capabilities.streamingQuirks.reasoningField,wholeAnswer:true,images:{count:0,bytes:0}})
  const record:RouteClassifierRecord={binding:identity,promptDigest:routeDigest(encoded.body),status:'refused'}
  try{
   const response=await withAbort(()=>values.model.call(encoded.suffix,{method:'POST',body:encoded.body,headers:encoded.headers,signal}),signal)
   const last=context.execution.state.prompts?.at(-1);if(last)record.promptDigest=`sha256:${last.sha256}`
   if(!response.ok){context.execution.recordCompletion(modelCompletion('error',{}),'work');return record}
   const answer:WireAnswer=await readAnswer(values.model.family,response,{text:async()=>{},reasoning:async()=>{},reasoningEnd:async()=>{}},signal,values.capabilities.streamingQuirks.reasoningField,()=>crypto.randomUUID())
   const completion=modelCompletion(answer.finishReason,answer.usage);context.execution.recordCompletion(completion,'work')
   record.usage=Object.fromEntries(['inputTokens','outputTokens'].filter(k=>completion[k as keyof typeof completion]!==undefined).map(k=>[k,completion[k as keyof typeof completion]]))
   let output:unknown
   if(answer.toolCalls.length===1&&answer.toolCalls[0]!.name==='choose_turn_route')output=answer.toolCalls[0]!.args
   else if(answer.toolCalls.length===0&&answer.text.length<=512){try{output=JSON.parse(answer.text)}catch{}}
   const label=output&&typeof output==='object'&&!Array.isArray(output)&&Object.keys(output).length===1?(output as {label?:unknown}).label:undefined
   if(answer.finishReason!=='stop'&&answer.finishReason!=='tool-calls'||typeof label!=='string'||!Object.hasOwn(c.labels,label)){record.status='unknown-label';return record}
   record.status='label';record.label=label;return record
  }catch(error){
   // A selection fallback never bypasses original-writer uncertainty or cancellation.
   const binding=this.session.context?.[durableOwner];if(signal.aborted||this.session.signal.aborted||binding?.writer.failed||context.execution.state.usageBudget?.entries.some(r=>r.state==='unknown'))throw error
   record.status=signal.aborted?'timeout':'refused';return record
  }
 }
}
export function routingPolicyFromMembers(m:import('../policy').PolicyMembers):RoutingPolicy|undefined{
 const mode=m['routing.mode']?.value;if(mode===undefined||mode==='off')return
 const policy=Object.fromEntries(['mode','tiers','rules','classifier'].filter(k=>m[`routing.${k}`]!==undefined).map(k=>[k,m[`routing.${k}`]!.value]));if(!validRoutingPolicy(policy))throw authoredFailure('configuration','TurnRoutePolicyInvalid: routing requires an exact bounded declaration.','context');return policy
}
/** Ceiling is rechecked after every canonical request rewrite, before physical claim. */
export function enforceRouteClassifierCeiling(family:string,raw:string,ceiling:number):string{
 const body=JSON.parse(raw) as Record<string,unknown>
 if(!body||typeof body!=='object'||Array.isArray(body)||!Number.isInteger(ceiling)||ceiling<1||ceiling>64)throw Error('TurnRouteClassifierUnsupported')
 if(family==='openai-compatible'||family==='anthropic'){
  const fields=family==='anthropic'?['max_tokens']:['max_tokens','max_completion_tokens'];let found=false
  for(const field of fields)if(Object.hasOwn(body,field)){body[field]=ceiling;found=true}
  if(!found)body.max_tokens=ceiling
 }else if(family==='gemini'){
  const config=body.generationConfig;if(!config||typeof config!=='object'||Array.isArray(config))throw Error('TurnRouteClassifierUnsupported');(config as Record<string,unknown>).maxOutputTokens=ceiling
 }else throw Error('TurnRouteClassifierUnsupported')
 return JSON.stringify(body)
}
