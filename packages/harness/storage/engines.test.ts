import {it,expect,vi} from 'vitest'
import {codex} from '../engines/codex'
import {withDelegation} from '../delegation'
import type {AgentAssistantSession,AssistantEvent} from '../engine'
import type {AgentRun} from '../agent'
import {MemoryRunStore,type RunStore} from './run'
import {readStoredRunEvent} from './writer'
import {providerReleaseAttempt} from './release'
const root=(store:RunStore,id:string,run:AgentRun,resume=false):AgentAssistantSession=>({prompt:'Review the supplied claims.',testPrompt:'',tools:[],hostTools:[],callTool:vi.fn(),signal:new AbortController().signal,allowConversation:true,interactive:true,critic:'off',agent:{model:'fixture',run},context:{currentRequest:{message:'Review the supplied claims.',input:'Review the supplied claims.'},runStore:{store,id,...(resume?{resume:true}:{})}}})
const collect=async(session:AgentAssistantSession)=>{const events:AssistantEvent[]=[];for await(const event of withDelegation(codex).start(session))events.push(event);return events}
it('the actual agent/coordinator persists zero-batch figures and all handoff transitions after provider release',async()=>{
 const memory=new MemoryRunStore(),states:string[]=[],requests:string[]=[];let active=0,invocations=0
 const store:RunStore={durability:'memory',load:memory.load.bind(memory),readEvents:memory.readEvents.bind(memory),commit:async input=>{const cp=JSON.parse(new TextDecoder().decode(input.checkpoint!));if(cp.handoff)states.push(cp.handoff.state);return memory.commit(input)}}
 const run:AgentRun=async(request,callbacks)=>{
  expect(active).toBe(0);active++;invocations++;requests.push(JSON.stringify(request))
  try{
   if(invocations===1){const tool=request.tools.find(t=>t.description.startsWith('delegate_tasks:'))!;expect(tool).toBeDefined();await callbacks.tool({id:'delegate-wire',name:tool.name,arguments:{tasks:[{title:'Review',profile:'review',objective:'Check the supplied claims.'}]}},callbacks.signal);return}
   const text=request.prompt.includes('Complete this read-only task')?'The supplied claims agree.':'Parent finished.'
   await callbacks.event({type:'message',id:'reply',text,phase:'final'})
  }finally{await Promise.resolve();active--;callbacks.providerRelease?.acknowledge(callbacks.providerRelease.invocation)}
 }
 const events=await collect(root(store,'handoff-root',run))
 expect(events.filter(e=>e.type==='error')).toEqual([]);expect(invocations).toBe(3)
 for(const state of ['released','children','answered','continued'])expect(states).toContain(state)
 const saved=JSON.parse(new TextDecoder().decode((await memory.load('handoff-root'))!.checkpoint!))
 expect(saved.status).toBe('complete');expect(saved.handoff.continuation).toBeTruthy();expect(requests[2]).toContain(JSON.stringify(saved.handoff.continuation).slice(1,-1))
 expect(saved.delegation.parent.agentRuns).toBe(2)
 const page=await memory.readEvents('handoff-root',null,100),logged=page.items.map(item=>readStoredRunEvent(item.bytes).event)
 expect(logged.filter(e=>e.type==='delegated_task').length).toBeGreaterThan(0)
 const empty=new MemoryRunStore();await collect(root(empty,'no-batches',async(_request,callbacks)=>{await callbacks.event({type:'message',id:'done',text:'Done.',phase:'final'});callbacks.providerRelease?.acknowledge(callbacks.providerRelease.invocation)}))
 const noBatches=JSON.parse(new TextDecoder().decode((await empty.load('no-batches'))!.checkpoint!))
 expect(noBatches.delegation.batches).toEqual([]);expect(noBatches.delegation.parent.agentRuns).toBe(1)
})
it('the actual agent uses the same prepared bytes after every predispatch acknowledgement fault',async()=>{
 const memory=new MemoryRunStore();let armed=true,first:string|undefined
 const store:RunStore={durability:'memory',load:memory.load.bind(memory),readEvents:memory.readEvents.bind(memory),commit:async input=>{
  const receipt=await memory.commit(input),cp=JSON.parse(new TextDecoder().decode(input.checkpoint!)),claim=cp.durable?.dispatches.find((d:{kind:string;state:string})=>d.kind==='agent'&&d.state==='claimed')
  if(armed&&claim){armed=false;first=claim.request;throw Error('lost dispatch acknowledgement')};return receipt
 }}
 const actual:string[]=[],run:AgentRun=async(request,callbacks)=>{actual.push(JSON.stringify(request));await callbacks.event({type:'message',id:'done',text:'Done.',phase:'final'});callbacks.providerRelease?.acknowledge(callbacks.providerRelease.invocation)}
 await collect(root(store,'agent-prepared',run));expect(actual).toEqual([])
 const events=await collect(root(store,'agent-prepared',run,true));expect(events.filter(e=>e.type==='error')).toEqual([])
 expect(actual).toEqual([first]);expect(JSON.parse(new TextDecoder().decode((await memory.load('agent-prepared'))!.checkpoint!)).delegation.parent.agentRuns).toBe(1)
})

for(const mode of ['missing','wrong','late'] as const)it(`refuses unknown provider release (${mode}) without dispatching a child or minting resume authority`,async()=>{
 const store=new MemoryRunStore();let calls=0,late:(()=>boolean)|undefined
 const run:AgentRun=async(request,callbacks)=>{
  calls++;const capability=callbacks.providerRelease!
  try{const tool=request.tools.find(t=>t.description.startsWith('delegate_tasks:'))!;await callbacks.tool({id:'delegate-wire',name:tool.name,arguments:{tasks:[{title:'Review',profile:'review',objective:'Check claims.'}]}},callbacks.signal)}finally{
   if(mode==='wrong')expect(capability.acknowledge({})).toBe(false)
   if(mode==='late')late=()=>capability.acknowledge(capability.invocation)
  }
 }
 const events=await collect(root(store,'release-'+mode,run));expect(calls).toBe(1)
 expect(events.some(e=>e.type==='error'&&e.failure?.detail?.startsWith('ProviderReleaseUnknown:'))).toBe(true)
 if(late)expect(late()).toBe(false)
 const saved=JSON.parse(new TextDecoder().decode((await store.load('release-'+mode))!.checkpoint!))
 expect(saved.durable.dispatches.some((d:{failure?:{detail?:string}})=>d.failure?.detail?.startsWith('ProviderReleaseUnknown:'))).toBe(true)
 const resumed=await collect(root(store,'release-'+mode,run,true));expect(calls).toBe(1);expect(resumed.some(e=>e.type==='error'&&e.failure?.detail?.startsWith('ProviderReleaseUnknown:'))).toBe(true)
})
it('a release acknowledgement cannot cross invocation identity or survive its completion',()=>{
 const first=providerReleaseAttempt(),second=providerReleaseAttempt()
 expect(second.capability.acknowledge(first.capability.invocation)).toBe(false);expect(second.finish()).toBe(false)
 expect(first.capability.acknowledge(first.capability.invocation)).toBe(true);expect(first.finish()).toBe(true)
 expect(first.capability.acknowledge(first.capability.invocation)).toBe(false)
})
