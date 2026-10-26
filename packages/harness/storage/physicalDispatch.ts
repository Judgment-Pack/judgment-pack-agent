import type {DurableBinding} from './writer'
import {UsageBudgetIdentityChanged} from './usageBudget'
import type {ModelCall,ModelRequest} from '../engine'
import type {AgentCallbacks,AgentRequest,AgentRun} from '../agent'
/** Package-owned wrapper identities only. No marker or execution capability is sent to a host.
 * The contextual caller supplies a fresh admission closure for each actual attempt. */
export type PhysicalModelAttempt=(send:ModelCall)=>Promise<Response>
export type PhysicalAgentAttempt=(send:AgentRun)=>Promise<void>
const singles=new WeakMap<ModelCall,ModelCall>()
const models=new WeakMap<ModelCall,(suffix:string,request:ModelRequest,attempt:PhysicalModelAttempt)=>Promise<Response>>()
const bindings=new WeakMap<object,DurableBinding>()
function exact(call:object,binding:DurableBinding){const held=bindings.get(call);if(!held||held.writer!==binding.writer||held.root!==binding.root||held.scope.execution!==binding.scope.execution||held.scope.task!==binding.scope.task)throw new UsageBudgetIdentityChanged()}
const agents=new WeakMap<AgentRun,(request:AgentRequest,callbacks:AgentCallbacks,attempt:PhysicalAgentAttempt)=>Promise<void>>()
export function registerPhysicalModel(call:ModelCall,run:(suffix:string,request:ModelRequest,attempt:PhysicalModelAttempt)=>Promise<Response>,binding?:DurableBinding,single?:ModelCall):void{models.set(call,run);if(single)singles.set(call,single);if(binding)bindings.set(call,{...binding,scope:{...binding.scope}})}
export function registerPhysicalAgent(call:AgentRun,run:(request:AgentRequest,callbacks:AgentCallbacks,attempt:PhysicalAgentAttempt)=>Promise<void>,binding?:DurableBinding):void{agents.set(call,run);if(binding)bindings.set(call,{...binding,scope:{...binding.scope}})}
export async function dispatchPhysicalModel(call:ModelCall,suffix:string,request:ModelRequest,attempt:PhysicalModelAttempt,binding:DurableBinding):Promise<Response>{const wrapped=models.get(call);if(!wrapped)return attempt(call);exact(call,binding);let live=true,busy=false;try{return await wrapped(suffix,request,async send=>{if(!live||busy)throw Error('Expired physical model dispatcher');busy=true;try{return await attempt(send)}finally{busy=false}})}finally{live=false}}
/** One exact canonical attempt bypasses only package-owned retry wrappers with retained binding. */
export async function dispatchSinglePhysicalModel(call:ModelCall,attempt:PhysicalModelAttempt,binding:DurableBinding):Promise<Response>{
 const seen=new Set<ModelCall>();let selected=call
 while(models.has(selected)){if(seen.has(selected)||seen.size>=8)throw new UsageBudgetIdentityChanged();seen.add(selected);exact(selected,binding);const raw=singles.get(selected);if(!raw)throw new UsageBudgetIdentityChanged();selected=raw}
 return attempt(selected)
}
export async function dispatchPhysicalAgent(call:AgentRun,request:AgentRequest,callbacks:AgentCallbacks,attempt:PhysicalAgentAttempt,binding:DurableBinding):Promise<void>{const wrapped=agents.get(call);if(!wrapped)return attempt(call);exact(call,binding);let live=true,busy=false;try{return await wrapped(request,callbacks,async send=>{if(!live||busy)throw Error('Expired physical agent dispatcher');busy=true;try{return await attempt(send)}finally{busy=false}})}finally{live=false}}

const nativeWitnesses=new WeakMap<object,{binding:DurableBinding;ticket:object;live:boolean;admitted:boolean}>()
/** Issued only after the original writer acknowledged this exact agent claim. Never sent to a host. */
export function openNativeWitness(binding:DurableBinding,ticket:object):object{const token=Object.freeze({});nativeWitnesses.set(token,{binding:{...binding,scope:{...binding.scope}},ticket,live:true,admitted:false});return token}
export function observeNativeWitness(token:object|undefined):void{if(token){const held=nativeWitnesses.get(token);if(held?.live)held.admitted=true}}
export function closeNativeWitness(token:object|undefined):void{if(token){const held=nativeWitnesses.get(token);if(held)held.live=false}}
export function admittedNativeWitness(token:object|undefined,binding:DurableBinding,ticket:object|undefined):boolean{const held=token?nativeWitnesses.get(token):undefined;return !!held&&!held.live&&held.admitted&&held.ticket===ticket&&held.binding.writer===binding.writer&&held.binding.root===binding.root&&held.binding.scope.execution===binding.scope.execution&&held.binding.scope.role===binding.scope.role&&held.binding.scope.task===binding.scope.task}

const captureAliases=new WeakMap<AgentRequest,{tools:string;bindings:Record<string,string>}>()
/** The original native engine alone supplies its private route table. No description parsing. */
export function nativeCaptureRequest(request:AgentRequest,routes:ReadonlyMap<string,string>):AgentRequest{
 const bindings:Record<string,string>=Object.create(null),names=new Set<string>()
 if(routes.size!==request.tools.length)throw Error('Invalid native capture routes')
 for(const tool of request.tools){const name=routes.get(tool.name);if(typeof name!=='string'||!name||Object.hasOwn(bindings,tool.name)||names.has(name))throw Error('Invalid native capture routes');bindings[tool.name]=name;names.add(name)}
 captureAliases.set(request,{tools:JSON.stringify(request.tools),bindings});return request
}
export function originalNativeCaptureAliases(request:AgentRequest):Record<string,string>|undefined{const held=captureAliases.get(request);return held&&held.tools===JSON.stringify(request.tools)?structuredClone(held.bindings):undefined}
