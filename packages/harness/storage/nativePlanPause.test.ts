import {it,expect,vi} from 'vitest'
import {codex} from '../engines/codex'
import {withDelegation} from '../delegation'
import {MemoryRunStore,type RunStore} from './run'
import type {AgentAssistantSession,AssistantEvent} from '../engine'
import type {AgentRun} from '../agent'
import {contextualSession} from '../context/session'
import {StorageOutcomeUnknown} from './writer'
import {composeProviderRelease,providerReleaseAttempt} from './release'

const plan={id:'original',revision:1,objective:'Work',steps:['Inspect','Report'],questions:[],references:[],status:'accepted' as const,createdAt:'2026-01-01T00:00:00.000Z',acceptedAt:'2026-01-01T00:00:00.000Z'}
const checkpoint=async(store:RunStore)=>JSON.parse(new TextDecoder().decode((await store.load('native-plan'))!.checkpoint!))
const basic=(store:MemoryRunStore):RunStore=>({durability:'memory',load:store.load.bind(store),commit:store.commit.bind(store),readEvents:store.readEvents.bind(store)})
const input=(store:RunStore,run:AgentRun,mode:string,resume=false,approval?:{callId:string;approved:boolean}):AgentAssistantSession=>({prompt:'Work',testPrompt:'',tools:[],hostTools:[],callTool:vi.fn(),agent:{model:'fixture',run},signal:new AbortController().signal,allowConversation:true,interactive:true,critic:'off',context:{taskSteps:true,...(mode==='standalone'?{taskStepEnvelope:{id:'host-original',revision:'sha256:'+'d'.repeat(64),unit:'provider-invocations' as const,limit:6}}:{}),currentRequest:{message:'Work',input:'facts'},plan:structuredClone(plan),runStore:{store,id:'native-plan',...(resume?{resume:true}:{})},...(approval?{approval}:{})}})
async function events(session:AgentAssistantSession,mode:string){const out:AssistantEvent[]=[];for await(const event of (mode==='coordinator'?withDelegation(codex):codex).start(session))out.push(event);return out}
for(const mode of ['standalone','coordinator'])for(const single of [true,false])for(const approved of [true,false])it(`#219 actual native ${mode} ${single?'first-departure':'allocated'} ${approved?'accept':'reject'} keeps released Plan pause unknown and starts a distinct invocation`,async()=>{
 const store=basic(new MemoryRunStore());let calls=0
 const run:AgentRun=async(request,cb)=>{
  calls++
  try{
   if(calls===1){const tool=request.tools.find(tool=>tool.description.startsWith('update_plan: '))!;expect(tool).toBeDefined()
    if(!single)await cb.tool({id:'allocate',name:tool.name,arguments:{explanation:'Allocate original work',plan:[{id:'step-1',step:'Inspect',status:'in_progress',budget:{requests:2}},{id:'step-2',step:'Report',status:'pending',budget:{requests:1}}]}},cb.signal)
    await cb.tool({id:'pause',name:tool.name,arguments:single?{explanation:'Remove accepted work',plan:[{id:'step-2',step:'Report',status:'pending',budget:{requests:1}}]}:{explanation:'Remove unstarted work',plan:[{id:'step-1',step:'Inspect',status:'in_progress',budget:{requests:2}}]}},cb.signal)
   }else{expect((await checkpoint(store)).status).toBe('running');await cb.event({type:'message',id:'done',text:'Done.',phase:'final'})}
  }finally{if(cb.providerRelease)expect(cb.providerRelease.acknowledge(cb.providerRelease.invocation)).toBe(true)}
 }
 const first=await events(input(store,run,mode),mode)
 expect(first.filter(event=>event.type==='error')).toEqual([])
 const before=await checkpoint(store),old=before.durable.dispatches.find((row:{kind:string})=>row.kind==='agent')
 expect(before.status).toBe('paused');expect(old.state).toBe('unknown');expect(old.planPause).toMatchObject({callId:before.planState.pending.callId,revision:before.planState.pending.revision,candidateRevision:before.planState.pending.candidate.planState.revision})
 const resumed=await events(input(store,run,mode,true,{callId:before.planState.pending.callId,approved}),mode)
 expect(resumed.filter(event=>event.type==='error')).toEqual([])
 const after=await checkpoint(store),physical=after.durable.dispatches.filter((row:{kind:string})=>row.kind==='agent')
 expect(calls).toBe(2);expect(physical).toHaveLength(2);expect(physical[0]).toEqual(old);expect(physical[1].id).not.toBe(old.id)
 expect(after.plan).toHaveLength(approved?1:2);expect(after.planState.decisions).toHaveLength(1);expect(after.planState.decisions[0].nativeInvocation).toEqual({dispatchId:old.id,requestDigest:old.requestDigest})
 expect(resumed.filter(event=>event.type==='plan_drift_decided')).toHaveLength(1)
})
for(const fault of ['missing','wrong','duplicate','late'])it(`#219 native ${fault} release never forgives the unknown claim`,async()=>{
 const store=basic(new MemoryRunStore());let calls=0,late:(()=>boolean)|undefined
 const run:AgentRun=async(request,cb)=>{calls++;try{const tool=request.tools.find(tool=>tool.description.startsWith('update_plan: '))!;await cb.tool({id:'pause',name:tool.name,arguments:{explanation:'Remove accepted work',plan:[{id:'step-2',step:'Report',status:'pending'}]}},cb.signal)}finally{
  if(cb.providerRelease){if(fault==='wrong')expect(cb.providerRelease.acknowledge({})).toBe(false);if(fault==='duplicate'){expect(cb.providerRelease.acknowledge(cb.providerRelease.invocation)).toBe(true);expect(cb.providerRelease.acknowledge(cb.providerRelease.invocation)).toBe(false)}if(fault==='late')late=()=>cb.providerRelease!.acknowledge(cb.providerRelease!.invocation)}
 }}
 await events(input(store,run,'standalone'),'standalone');const before=await checkpoint(store),old=before.durable.dispatches.find((row:{kind:string})=>row.kind==='agent');expect(old.state).toBe('unknown');expect(old.planPause).toBeUndefined()
 if(late)expect(late()).toBe(false)
 const resumed=await events(input(store,run,'standalone',true,{callId:before.planState.pending.callId,approved:true}),'standalone')
 expect(calls).toBe(1);expect(resumed.filter(event=>event.type==='error').map(event=>({code:event.failure?.code,outcome:event.failure?.outcome}))).toEqual([{code:'unknown',outcome:'unknown'}])
})
it('#219 composed release invalidates every owner on wrong or duplicate proof',()=>{
 for(const fault of ['wrong','duplicate']){const a=providerReleaseAttempt(),b=providerReleaseAttempt(),composed=composeProviderRelease(a.capability,b.capability)!
  if(fault==='wrong')expect(composed.acknowledge({})).toBe(false);else{expect(composed.acknowledge(composed.invocation)).toBe(true);expect(composed.acknowledge(composed.invocation)).toBe(false)}
  expect(a.finish()).toBe(false);expect(b.finish()).toBe(false)
 }
})

for(const disabled of ['no-plan','no-conversation'])it(`#219 ${disabled} native calls keep ordinary feature-free audit and need no release`,async()=>{
 const memory=new MemoryRunStore(),store=basic(memory);let calls=0;const requests:{prompt:string;tools:number}[]=[]
 const run:AgentRun=async(request,cb)=>{calls++;requests.push({prompt:request.prompt,tools:request.tools.length});expect(cb.providerRelease).toBeUndefined();await cb.event({type:'message',id:'done',text:'Done.',phase:'final'})}
 const raw=input(store,run,'legacy');delete raw.context!.taskSteps
 if(disabled==='no-plan')delete raw.context!.plan;else raw.allowConversation=false
 const out=await events(raw,'legacy');if(disabled==='no-plan')expect(out.filter(event=>event.type==='error')).toEqual([]);else expect(out.filter(event=>event.type==='error').every(event=>event.failure?.code==='invalid-response')).toBe(true);expect(calls).toBe(disabled==='no-plan'?1:2) // The ordinary no-conversation path includes its existing final-response repair.
 if(disabled==='no-conversation'){expect(requests[0]!.prompt).not.toContain('BEGIN PREVIOUS ANSWER');expect(requests[1]!.prompt).toContain('BEGIN PREVIOUS ANSWER\nDone.\nEND PREVIOUS ANSWER');expect(requests[1]!.tools).toBe(0)}
 const cp=await checkpoint(store);expect(cp.version).toBe(2);expect(cp.durable.dispatches[0].planPause).toBeUndefined()
 const page=await memory.readEvents('native-plan',null,100);for(const row of page.items){const event=JSON.parse(new TextDecoder().decode(row.bytes)).event;if(event.type==='execution_checkpoint')expect(event.version).toBe(1)}
})

for(const fault of ['truncated','foreign-scope','duplicate-cursor','expired','head-race'])it(`#219 retained ${fault} proof refuses before a new provider`,async()=>{
 const memory=new MemoryRunStore();let enabled=false,calls=0,raced=false
 const store:RunStore={...basic(memory),readEvents:async(id,after,limit,signal)=>{
  const page=await memory.readEvents(id,after,limit,signal);if(!enabled)return page
  if(fault==='head-race'&&!raced){raced=true;const head=(await memory.load(id))!;await memory.commit({runId:id,expectedRevision:head.revision,commitId:'proof-race',checkpoint:head.checkpoint,events:[new TextEncoder().encode(JSON.stringify({version:1,scope:{execution:id,role:'author'},event:{version:1,type:'guardrail',tool:'fixture',action:'refused',detail:'concurrent proof notice'}}))]});return page}
  const items=page.items.map(item=>({...item,bytes:new Uint8Array(item.bytes)}))
  const claim=items.findIndex(item=>{const event=JSON.parse(new TextDecoder().decode(item.bytes)).event;return event.type==='execution_checkpoint'&&event.checkpoint.durable?.dispatches.some((row:{kind:string;state:string})=>row.kind==='agent'&&row.state==='claimed')})
  const pause=items.findIndex(item=>{const event=JSON.parse(new TextDecoder().decode(item.bytes)).event;return event.type==='execution_checkpoint'&&event.checkpoint.durable?.dispatches.some((row:{planPause?:unknown})=>row.planPause)})
  if(fault==='truncated')return {...page,items:items.filter(item=>{const event=JSON.parse(new TextDecoder().decode(item.bytes)).event;return !(event.type==='execution_checkpoint'&&event.checkpoint.durable?.dispatches.some((row:{kind:string;state:string})=>row.kind==='agent'&&row.state==='claimed'))})}
  if(fault==='duplicate-cursor'&&claim>=0)items.splice(claim,0,items[claim]!)
  if(pause>=0&&(fault==='foreign-scope'||fault==='expired')){const envelope=JSON.parse(new TextDecoder().decode(items[pause]!.bytes));if(fault==='foreign-scope')envelope.scope.execution='another-run';else envelope.event.checkpoint.retainUntil='2000-01-01T00:00:00.000Z';items[pause]!.bytes=new TextEncoder().encode(JSON.stringify(envelope))}
  return {...page,items}
 }}
 const run:AgentRun=async(request,cb)=>{calls++;try{const tool=request.tools.find(tool=>tool.description.startsWith('update_plan: '))!;await cb.tool({id:'pause',name:tool.name,arguments:{explanation:'Remove accepted work',plan:[{id:'step-2',step:'Report',status:'pending'}]}},cb.signal)}finally{cb.providerRelease?.acknowledge(cb.providerRelease.invocation)}}
 await events(input(store,run,'standalone'),'standalone');const before=await checkpoint(store);expect(before.durable.dispatches[0].planPause).toBeDefined();enabled=true
 const resumed=await events(input(store,run,'standalone',true,{callId:before.planState.pending.callId,approved:true}),'standalone')
 expect(calls).toBe(1);expect(resumed.some(event=>event.type==='error')).toBe(true)
 expect((await checkpoint(store)).planState.pending).toBeDefined()
})
it('#219 unknown decision acknowledgement requires retained-read reconciliation and never replays',async()=>{
 const memory=new MemoryRunStore();let lose=true,calls=0
 const store:RunStore={...basic(memory),commit:async(input,signal)=>{const receipt=await memory.commit(input,signal);if(lose&&input.events.some(raw=>JSON.parse(new TextDecoder().decode(raw)).event.type==='plan_drift_decided')){lose=false;throw Error('fixture lost decision acknowledgement')}return receipt}}
 const run:AgentRun=async(request,cb)=>{calls++;try{if(calls===1){const tool=request.tools.find(tool=>tool.description.startsWith('update_plan: '))!;await cb.tool({id:'pause',name:tool.name,arguments:{explanation:'Remove accepted work',plan:[{id:'step-2',step:'Report',status:'pending'}]}},cb.signal)}else await cb.event({type:'message',id:'done',text:'Done.',phase:'final'})}finally{cb.providerRelease?.acknowledge(cb.providerRelease.invocation)}}
 await events(input(store,run,'standalone'),'standalone');const before=await checkpoint(store);expect(before.durable.dispatches[0].planPause).toBeDefined();const approval={callId:before.planState.pending.callId,approved:true}
 let unknown:unknown;try{await contextualSession(input(store,run,'standalone',true,approval),'codex')}catch(error){unknown=error};expect(calls).toBe(1);if(unknown&&!(unknown instanceof StorageOutcomeUnknown))throw unknown;expect(unknown).toBeInstanceOf(StorageOutcomeUnknown)
 const retained=await checkpoint(store);expect(retained.status).toBe('running');expect(retained.planState.decisions).toHaveLength(1)
 await events(input(store,run,'standalone',true,approval),'standalone');expect(calls).toBe(1)
 const reconciled=await events(input(store,run,'standalone',true),'standalone');expect(reconciled.filter(event=>event.type==='error')).toEqual([]);expect(calls).toBe(2)
})

it('#219 composed release invalidates live owners if another owner has already closed',()=>{
 const closed=providerReleaseAttempt(),live=providerReleaseAttempt();expect(closed.finish()).toBe(false)
 const joined=composeProviderRelease(closed.capability,live.capability)!
 expect(joined.acknowledge(joined.invocation)).toBe(false);expect(live.finish()).toBe(false)
})

it('#219 a released Plan decision never forgives the next unknown invocation',async()=>{
 const store=basic(new MemoryRunStore());let calls=0
 const run:AgentRun=async(request,cb)=>{calls++;try{if(calls===1){const tool=request.tools.find(tool=>tool.description.startsWith('update_plan: '))!;await cb.tool({id:'pause',name:tool.name,arguments:{explanation:'Remove accepted work',plan:[{id:'step-2',step:'Report',status:'pending'}]}},cb.signal)}else throw Error('unrelated next provider unknown')}finally{cb.providerRelease?.acknowledge(cb.providerRelease.invocation)}}
 await events(input(store,run,'standalone'),'standalone');const before=await checkpoint(store)
 await events(input(store,run,'standalone',true,{callId:before.planState.pending.callId,approved:true}),'standalone');expect(calls).toBe(2)
 const after=await checkpoint(store);expect(after.durable.dispatches.filter((row:{kind:string;state:string})=>row.kind==='agent'&&row.state==='unknown')).toHaveLength(2)
 const refused=await events(input(store,run,'standalone',true),'standalone');expect(calls).toBe(2);expect(refused.some(event=>event.type==='error')).toBe(true)
})
for(const fault of ['missing-pause','missing-decision','wrong-request'])it(`#219 decided retained ${fault} evidence cannot restore the private exemption`,async()=>{
 const memory=new MemoryRunStore();let corrupt=false,calls=0
 const store:RunStore={...basic(memory),readEvents:async(...args)=>{const page=await memory.readEvents(...args);if(!corrupt)return page;const items=page.items.filter(item=>{const event=JSON.parse(new TextDecoder().decode(item.bytes)).event;return !(fault==='missing-decision'&&event.type==='plan_drift_decided')&&!(fault==='missing-pause'&&event.type==='execution_checkpoint'&&event.checkpoint.durable?.dispatches.some((row:{planPause?:unknown})=>row.planPause))}).map(item=>{if(fault!=='wrong-request')return item;const envelope=JSON.parse(new TextDecoder().decode(item.bytes)),row=envelope.event.checkpoint?.durable?.dispatches.find((row:{planPause?:unknown})=>row.planPause);if(row)row.requestDigest='sha256:'+'a'.repeat(64);return {...item,bytes:new TextEncoder().encode(JSON.stringify(envelope))}});return {...page,items}}}
 const run:AgentRun=async(request,cb)=>{calls++;try{if(calls===1){const tool=request.tools.find(tool=>tool.description.startsWith('update_plan: '))!;await cb.tool({id:'pause',name:tool.name,arguments:{explanation:'Remove accepted work',plan:[{id:'step-2',step:'Report',status:'pending'}]}},cb.signal)}else await cb.event({type:'message',id:'done',text:'Done.',phase:'final'})}finally{cb.providerRelease?.acknowledge(cb.providerRelease.invocation)}}
 await events(input(store,run,'standalone'),'standalone');const before=await checkpoint(store)
 const resumed=await events(input(store,run,'standalone',true,{callId:before.planState.pending.callId,approved:true}),'standalone');expect(resumed.filter(event=>event.type==='error')).toEqual([]);expect(calls).toBe(2)
 corrupt=true;const refused=await events(input(store,run,'standalone',true),'standalone');expect(calls).toBe(2);expect(refused.some(event=>event.type==='error')).toBe(true)
})

for(const taskSteps of [false,true])it(`#219 interrupted terminal write never replays a later unknown native invocation with taskSteps=${taskSteps}`,async()=>{
 const memory=new MemoryRunStore();let refuseTerminal=false,terminalRefusals=0,calls=0
 const store:RunStore={...basic(memory),commit:async(value,signal)=>{
  if(refuseTerminal&&value.checkpoint&&JSON.parse(new TextDecoder().decode(value.checkpoint)).status==='stopped'){terminalRefusals++;throw Error('fixture interrupted terminal checkpoint')}
  return memory.commit(value,signal)
 }}
 const run:AgentRun=async(request,cb)=>{
  calls++
  try{if(calls===1){const tool=request.tools.find(tool=>tool.description.startsWith('update_plan: '))!;expect(tool).toBeDefined();await cb.tool({id:'pause',name:tool.name,arguments:{explanation:'Remove accepted work',plan:[{id:'step-2',step:'Report',status:'pending'}]}},cb.signal)}else throw Error('fixture later native outcome unknown')}
  finally{expect(cb.providerRelease?.acknowledge(cb.providerRelease.invocation)).toBe(true)}
 }
 const session=(resume=false,approval?:{callId:string;approved:boolean})=>{
  const value=input(store,run,'legacy',resume,approval)
  if(!taskSteps)delete value.context!.taskSteps
  return value
 }
 const first=await events(session(),'legacy'),before=await checkpoint(store),old=before.durable.dispatches[0]
 expect(first.filter(event=>event.type==='error')).toEqual([]);expect(calls).toBe(1)
 expect(before.status).toBe('paused');expect(old).toMatchObject({kind:'agent',state:'unknown',planPause:{callId:before.planState.pending.callId}})
 refuseTerminal=true
 try{await events(session(true,{callId:before.planState.pending.callId,approved:true}),'legacy')}catch(error){expect(error).toBeInstanceOf(StorageOutcomeUnknown)}
 expect(calls).toBe(2);expect(terminalRefusals).toBeGreaterThan(0)
 const retained=await checkpoint(store),physical=retained.durable.dispatches.filter((row:{kind:string})=>row.kind==='agent')
 expect(retained.status).toBe('running');expect(retained.planState.pending).toBeUndefined();expect(retained.plan).toHaveLength(1)
 expect(retained.planState.decisions).toHaveLength(1);expect(retained.planState.decisions[0]).toMatchObject({approved:true,nativeInvocation:{dispatchId:old.id,requestDigest:old.requestDigest}})
 expect(physical).toHaveLength(2);expect(physical[0]).toEqual(old);expect(physical[1]).toMatchObject({state:'unknown',scope:old.scope});expect(physical[1].id).not.toBe(old.id)
 const journal=await memory.readEvents('native-plan',null,100),decisions=journal.items.map(row=>JSON.parse(new TextDecoder().decode(row.bytes)).event).filter(event=>event.type==='plan_drift_decided')
 expect(decisions).toHaveLength(1);expect(decisions[0]).toMatchObject({approved:true,nativeInvocation:{dispatchId:old.id,requestDigest:old.requestDigest}})
 refuseTerminal=false
 const refused=await events(session(true),'legacy')
 expect(calls,'distinct later unknown must refuse before provider dispatch').toBe(2)
 expect(refused.filter(event=>event.type==='error').map(event=>({code:event.failure?.code,outcome:event.failure?.outcome}))).toEqual([{code:'unknown',outcome:'unknown'}])
 const final=await checkpoint(store);expect(final.durable.dispatches.filter((row:{kind:string})=>row.kind==='agent')).toEqual(physical)
})
