import {it,expect} from 'vitest'
import {vercel} from '../engines/vercel'
import {codex} from '../engines/codex'
import {withDelegation} from '../delegation'
import type {AssistantEvent,AssistantSession,AgentAssistantSession,Engine} from '../engine'
import type {AgentRun} from '../agent'
import {ChatContext} from '../context/chat'
import {normalize} from '../thinking'
import {ASSISTANT_TOOLS} from '../host'
import {gatedRuntime,runtimeAnswer,sseTurn} from '../testing/scripted'
import {currentContractReplayTools} from '../certification/scriptedServer'
import {MemoryRunStore,type RunStore} from './run'

// agent#248: handwritten offline doubles reached through the actual delegation coordinator, engines,
// gated Runtime connection and durable writer. The host store refuses any root checkpoint that leaves
// the retained chat's scope, the invariant a later turn on that chat reopens against.
const RUNTIME={identity:'sha256:'+'2'.repeat(64),version:'1.0.0'}
for(const engine of ['api','native'] as const)it(`${engine}: the delegate's final end keeps the retained chat scope; a second turn reuses immutable reads and reads mutable state fresh`,async()=>{
 const chat=new ChatContext(),memory=new MemoryRunStore(),refused:string[]=[]
 const store:RunStore={durability:'memory',load:memory.load.bind(memory),readEvents:memory.readEvents.bind(memory),commit:async input=>{
  const cp=input.checkpoint?JSON.parse(new TextDecoder().decode(input.checkpoint)):undefined
  if(cp&&cp.context?.scope!==chat.scope){refused.push(`${cp.status}:${cp.context?.scope}`);throw Error('fixture store: checkpoint left the retained chat scope')}
  return memory.commit(input)
 }}
 const physical=new Map<string,number>()
 const runtime=await gatedRuntime({tools:currentContractReplayTools(),allowed:[...ASSISTANT_TOOLS],onEvent:()=>{},handle:call=>{
  const count=(physical.get(call.name)??0)+1;physical.set(call.name,count)
  return runtimeAnswer(call.name==='validate'?{workspace:`revision ${count}`}:{schema:'judgment-pack',version:'0.2.0-draft'})
 }})
 const script=[{name:'get_schema',args:{spec_version:'0.2.0-draft'}},{name:'validate',args:{document:{id:'workspace'}}}]
 try{
  const turn=async(id:string)=>{
   let requests=0
   const context={chat,runtime:RUNTIME,currentRequest:{message:'Author the pack.',input:'Author the pack.'},runStore:{store,id}}
   const common={prompt:'Author the pack.',testPrompt:'',tools:runtime.tools,callTool:runtime.callTool,hostTools:[],signal:new AbortController().signal,allowConversation:true,interactive:true,critic:'off' as const,context}
   let wrapped:Engine,session:AssistantSession|AgentAssistantSession
   if(engine==='api'){
    wrapped=withDelegation(vercel)
    session={...common,thinking:normalize('off','openai-compatible'),model:{family:'openai-compatible',model:'fixture',call:async()=>{const step=script[requests++];return new Response(step?sseTurn({tool:step}):sseTurn({text:'Proposal ready.'}),{headers:{'content-type':'text/event-stream'}})}}}
   }else{
    const run:AgentRun=async(request,callbacks)=>{
     try{
      for(const step of script){requests++;const tool=request.tools.find(t=>t.description.startsWith(step.name+':'));if(!tool)throw Error(`fixture: ${step.name} was not offered`);await callbacks.tool({id:`native-${step.name}`,name:tool.name,arguments:step.args},callbacks.signal)}
      requests++;await callbacks.event({type:'message',id:'final',text:'Proposal ready.',phase:'final'})
     }finally{callbacks.providerRelease?.acknowledge(callbacks.providerRelease.invocation)}
    }
    wrapped=withDelegation(codex)
    session={...common,agent:{model:'fixture',run}}
   }
   const events:AssistantEvent[]=[];for await(const event of wrapped.start(session))events.push(event)
   expect(events.filter(event=>event.type==='error')).toEqual([])
   expect(requests).toBe(3)
   const saved=JSON.parse(new TextDecoder().decode((await memory.load(id))!.checkpoint!))
   expect({scope:saved.context.scope,status:saved.status,refused}).toEqual({scope:chat.scope,status:'complete',refused:[]})
   return JSON.stringify(events.filter(event=>event.type==='tool_result'&&event.name==='validate'))
  }
  const first=await turn(`${engine}-chat-turn-1`),second=await turn(`${engine}-chat-turn-2`)
  expect(Object.fromEntries(physical)).toEqual({get_schema:1,validate:2})
  expect(first).toContain('revision 1');expect(second).toContain('revision 2')
 }finally{await runtime.close()}
})
