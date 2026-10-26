import {AIFailureError,failure} from '../failures'
import {withDelegation} from '../delegation'
import {describe,it,expect,vi} from 'vitest'
import {vercel} from '../engines/vercel'
import {deepagents} from '../engines/deepagents'
import {codex} from '../engines/codex'
import type {AssistantSession,AgentAssistantSession,AssistantEvent,EngineSession} from '../engine'
import {MemoryRunStore,type RunStore} from './run'
import {MemoryUsageBudgetStore,type UsageBudgetScope,type UsageBudgetStore} from './usageBudget'
import {projectRunReport} from '../runReport'
import {readExecution} from '../execution/state'
const scope=(limits:UsageBudgetScope['limits']):UsageBudgetScope=>({resource:'usage-project',id:'shared-scope',revision:'policy-r1',limits})
const final=()=>new Response(JSON.stringify({id:'fixture',object:'chat.completion',model:'fixture',choices:[{index:0,message:{role:'assistant',content:'Done.'},finish_reason:'stop'}],usage:{prompt_tokens:4,completion_tokens:2}}),{headers:{'content-type':'application/json'}})
const tool=()=>new Response(JSON.stringify({id:'fixture',object:'chat.completion',model:'fixture',choices:[{index:0,message:{role:'assistant',content:null,tool_calls:[{id:'read-1',type:'function',function:{name:'read_fixture',arguments:'{}'}}]},finish_reason:'tool_calls'}],usage:{prompt_tokens:4}}),{headers:{'content-type':'application/json'}})
const saved=async(store:MemoryRunStore,id:string)=>JSON.parse(new TextDecoder().decode((await store.load(id))!.checkpoint!))
const input=(store:RunStore,id:string,budget:UsageBudgetStore,limits:UsageBudgetScope['limits'],call:AssistantSession['model']['call'],resume=false):AssistantSession=>({prompt:'Answer.',testPrompt:'',tools:[],hostTools:[],callTool:vi.fn(),model:{family:'openai-compatible',model:'fixture',call},signal:new AbortController().signal,allowConversation:true,interactive:false,critic:'off',context:{currentRequest:{message:'Answer',input:'Answer'},connection:{connectionId:'provider-1',connectionRevision:'revision-1'},connectionRevisions:{'provider-1':'revision-1'},runStore:{store,id,resume},usageBudget:{store:budget,scope:scope(limits),bounder:async request=>({inputTokens:10,outputTokens:request.encodedOutputMaximum!})}}})
const consume=async(stream:AsyncIterable<AssistantEvent>)=>{const events:AssistantEvent[]=[];for await(const event of stream)events.push(event);return events}
for(const engine of [vercel,deepagents])describe(`${engine.id} actual physical usage admission`,()=>{
 it('admits one exact request, records actual token facts, and produces report2',async()=>{
  const store=new MemoryRunStore(),budget=new MemoryUsageBudgetStore('usage-project'),call=vi.fn(async()=>final())
  const events=await consume(engine.start(input(store,'one',budget,{modelRequests:1,inputTokens:10,outputTokens:100000},call)))
  expect(events.filter(e=>e.type==='error')).toEqual([]);expect(call).toHaveBeenCalledOnce()
  const checkpoint=await saved(store,'one');expect(checkpoint.version).toBe(3)
  expect(checkpoint.usageBudget.entries).toHaveLength(1)
  expect(checkpoint.usageBudget.entries[0].reservation.id).toBe(checkpoint.durable.dispatches[0].id)
  const report=projectRunReport(readExecution(checkpoint)!)
  expect(report.version).toBe(2);if(report.version!==2)throw Error('missing feature report')
  expect(report.usageTotals!.inputTokens).toEqual({known:4,complete:true});expect(report.usageTotals!.outputTokens).toEqual({known:2,complete:true})
 })
 it('arbitrates one shared scope across simultaneous actual root runs',async()=>{
  const store=new MemoryRunStore(),budget=new MemoryUsageBudgetStore('usage-project'),call=vi.fn(async()=>final())
  await Promise.all(['sibling-a','sibling-b'].map(id=>consume(engine.start(input(store,id,budget,{modelRequests:1},call)))))
  expect(call).toHaveBeenCalledOnce()
  const heads=await Promise.all(['sibling-a','sibling-b'].map(id=>saved(store,id)))
  expect(heads.reduce((n,cp)=>n+cp.usageBudget.entries.length,0)).toBe(1)
  expect(heads.flatMap(cp=>cp.usageBudget.refusals).map(row=>row.name)).toEqual(['UsageBudgetExceeded'])
 })
 it('retains missing output bound and refuses the next actual tool-loop request',async()=>{
  const store=new MemoryRunStore(),budget=new MemoryUsageBudgetStore('usage-project'),read=vi.fn(async()=>({content:[{type:'text' as const,text:'Read.'}]})),call=vi.fn(async()=>tool())
  const session=input(store,'partial',budget,{modelRequests:2,outputTokens:100000},call)
  // Scope is fixed before admission;8192 is the supported SDK's declared output reserve.
  session.context!.usageBudget!.scope.limits.outputTokens=8192
  session.context!.usageBudget!.bounder=async request=>({outputTokens:request.encodedOutputMaximum!})
  session.hostTools=[{name:'read_fixture',description:'Read',inputSchema:{type:'object',properties:{}},effect:'read',taxonomy:{operation:'read',effect:'read'},execute:read}]
  await consume(engine.start(session))
  expect(call).toHaveBeenCalledOnce();expect(read).toHaveBeenCalledOnce()
  const checkpoint=await saved(store,'partial');expect(checkpoint.usageBudget.refusals.at(-1).name).toBe('UsageBudgetExceeded')
  expect(checkpoint.usageBudget.entries[0].observed.outputTokens).toBeUndefined()
 })
 for(const fault of ['reserve','journal'] as const)it(`refuses physical dispatch after lost ${fault} ack and explicit resume`,async()=>{
  const memory=new MemoryRunStore(),call=vi.fn(async()=>final());let armed=true
  const budget=new MemoryUsageBudgetStore('usage-project',{afterChange(){if(fault==='reserve'&&armed){armed=false;throw Error('fixture lost Reserve ack')}}})
  const store:RunStore=fault==='journal'?{durability:'memory',load:memory.load.bind(memory),readEvents:memory.readEvents.bind(memory),commit:async(input,signal)=>{const receipt=await memory.commit(input,signal);if(armed&&JSON.parse(new TextDecoder().decode(input.checkpoint!)).usageBudget?.entries.length){armed=false;throw Error('fixture lost journal ack')}return receipt}}:memory
  await consume(engine.start(input(store,'lost-'+fault,budget,{modelRequests:1},call)))
  expect(call).not.toHaveBeenCalled()
  const checkpoint=await saved(memory,'lost-'+fault);expect(checkpoint.usageBudget.entries).toHaveLength(1)
  await consume(engine.start(input(store,'lost-'+fault,budget,{modelRequests:1},call,true)))
  expect(call).not.toHaveBeenCalled();expect((await saved(memory,'lost-'+fault)).usageBudget.refusals.at(-1).name).toBe('UsageBudgetUnknown')
 })
})
describe('actual native usage boundary',()=>{
 it('counts a provider invocation separately and refuses opaque physical token guarantees',async()=>{
  for(const limits of [{providerInvocations:1},{modelRequests:1},{inputTokens:10}]){
   const memory=new MemoryRunStore(),budget=new MemoryUsageBudgetStore('usage-project'),run=vi.fn(async(_request,callbacks)=>{await callbacks.event({type:'text',id:'final',text:'Done.',phase:'final'})})
   const model=input(memory,'native',budget,limits,vi.fn()),{model:_model,...base}=model
   const session={...base,agent:{model:'native-model',run}} as AgentAssistantSession
   await consume(codex.start(session))
   if('providerInvocations'in limits){expect(run).toHaveBeenCalledOnce();const cp=await saved(memory,'native');expect(cp.usageBudget.entries[0].observed).toEqual({providerInvocations:1});const report=projectRunReport(readExecution(cp)!);if(report.version!==2)throw Error('missing native feature report');expect(report.usageTotals!.modelRequests).toEqual({known:0,complete:false})}
   else {expect(run).not.toHaveBeenCalled();expect((await saved(memory,'native')).usageBudget.refusals.at(-1).name).toBe('UsageBudgetUnsupported')}
  }
 })
})

describe('public delegation physical retry accounting',()=>{
 for(const mode of ['complete','exhausted','reserve-ack','journal-ack'] as const)it(`owns each actual child retry: ${mode}`,async()=>{
  const memory=new MemoryRunStore();let sends=0,reserves=0,armed=true
  const actualBudget=new MemoryUsageBudgetStore('usage-project')
  const budget:UsageBudgetStore={resource:actualBudget.resource,reserve:async(value,signal)=>{const receipt=await actualBudget.reserve(value,signal);reserves++;if(mode==='reserve-ack'&&reserves===3)throw Error('fixture lost retry Reserve ack');return receipt},settle:actualBudget.settle.bind(actualBudget),readReservation:actualBudget.readReservation.bind(actualBudget)}
  const store:RunStore=mode==='journal-ack'?{durability:'memory',load:memory.load.bind(memory),readEvents:memory.readEvents.bind(memory),commit:async(value,signal)=>{const receipt=await memory.commit(value,signal);const cp=JSON.parse(new TextDecoder().decode(value.checkpoint!));if(armed&&cp.usageBudget?.entries.length===3&&cp.usageBudget.entries[2].state==='reserved'){armed=false;throw Error('fixture lost retry journal ack')}return receipt}}:memory
  const call=vi.fn(async(_suffix,request)=>{
   expect(Object.getOwnPropertySymbols(request)).toEqual([])
   expect(Object.keys(request).every(key=>['body','method','signal','headers'].includes(key))).toBe(true)
   sends++
   if(sends===1)return new Response(JSON.stringify({id:'parent',object:'chat.completion',model:'fixture',choices:[{index:0,message:{role:'assistant',content:null,tool_calls:[{id:'delegate-1',type:'function',function:{name:'delegate_tasks',arguments:JSON.stringify({tasks:[{title:'Check evidence',objective:'Check supplied evidence',profile:'review'}]})}}]},finish_reason:'tool_calls'}],usage:{prompt_tokens:4,completion_tokens:2}}),{headers:{'content-type':'application/json'}})
   if(sends===2)return new Response('{"error":{"code":"rate_limit_exceeded","message":"fixture retry"}}',{status:429,headers:{'content-type':'application/json','retry-after':'0'}})
   return final()
  })
  const session=input(store,'delegation-'+mode,budget,{modelRequests:mode==='exhausted'?2:8},call);session.interactive=true
  let captured:EngineSession|undefined
  let probed=false
  const wrapped=withDelegation({id:vercel.id,start(given){return (async function*(){if(given.context?.objective){captured=given;if(mode==='complete'&&!probed){probed=true;const fresh=input(memory,'transplanted-live',budget,{modelRequests:8},call),before=sends;await consume(vercel.start({...given,context:fresh.context}));expect(sends).toBe(before)}}yield*vercel.start(given)})()}})
  const events=await consume(wrapped.start(session))
  const cp=await saved(memory,'delegation-'+mode)
  if(mode==='complete'){
   expect(sends).toBe(4);expect(cp.usageBudget.entries).toHaveLength(4)
   expect(cp.usageBudget.entries.map((row:any)=>row.observed.modelRequests)).toEqual([1,1,1,1])
   expect(new Set(cp.usageBudget.entries.map((row:any)=>row.reservation.id)).size).toBe(4)
   expect(cp.usageBudget.entries.slice(1,3).map((row:any)=>row.reservation.task)).toEqual([cp.usageBudget.entries[1].reservation.task,cp.usageBudget.entries[1].reservation.task])
   expect(cp.usageBudget.entries[1].reservation.task).toBeTruthy()
   const report=projectRunReport(readExecution(cp)!);expect(report.version).toBe(2);if(report.version===2)expect(report.usageTotals!.modelRequests).toEqual({known:4,complete:true})
   expect(events.filter(event=>event.type==='error')).toEqual([])
   expect(captured).toBeTruthy();const fresh=input(memory,'transplanted',budget,{modelRequests:8},call);await consume(vercel.start({...captured!,context:fresh.context}));expect(sends).toBe(4)
  }else{
   expect(sends).toBe(2)
   if(mode==='journal-ack'){expect(cp.usageBudget.entries).toHaveLength(3);expect(cp.usageBudget.entries[2].state).toBe('reserved')}
   else expect(cp.usageBudget.refusals.some((row:any)=>row.name===(mode==='exhausted'?'UsageBudgetExceeded':'UsageBudgetUnknown'))).toBe(true)
   if(mode!=='exhausted'){const resumed=input(store,'delegation-'+mode,budget,{modelRequests:8},call,true);resumed.interactive=true;await consume(withDelegation(vercel).start(resumed));expect(sends).toBe(2)}
  }
 })
})

it('shares admission across actual concurrently delegated children without extra allowance',async()=>{
 const memory=new MemoryRunStore(),budget=new MemoryUsageBudgetStore('usage-project');let sends=0
 const call=vi.fn(async()=>{
  sends++;if(sends!==1)return final()
  return new Response(JSON.stringify({id:'parent',object:'chat.completion',model:'fixture',choices:[{index:0,message:{role:'assistant',content:null,tool_calls:[{id:'delegate-pair',type:'function',function:{name:'delegate_tasks',arguments:JSON.stringify({tasks:['First','Second'].map(title=>({title,objective:title+' evidence',profile:'review'}))})}}]},finish_reason:'tool_calls'}],usage:{prompt_tokens:4,completion_tokens:2}}),{headers:{'content-type':'application/json'}})
 })
 const session=input(memory,'parallel-children',budget,{modelRequests:2},call);session.interactive=true
 await consume(withDelegation(vercel).start(session))
 expect(sends).toBeGreaterThanOrEqual(1);expect(sends).toBeLessThanOrEqual(2)
 const cp=await saved(memory,'parallel-children');expect(cp.usageBudget.entries).toHaveLength(2)
 expect(cp.usageBudget.entries[1].reservation.task).toBeTruthy()
 expect(cp.usageBudget.refusals.some((row:any)=>row.name==='UsageBudgetExceeded'&&row.task)).toBe(true)
})
for(const engine of [vercel,deepagents])it(`${engine.id} keeps feature-free producers at checkpoint2/event1/report1`,async()=>{
 const memory=new MemoryRunStore(),budget=new MemoryUsageBudgetStore('usage-project'),session=input(memory,'plain',budget,{modelRequests:1},async()=>final())
 delete session.context!.usageBudget
 const events=await consume(engine.start(session)),cp=await saved(memory,'plain')
 expect(cp.version).toBe(2);expect(Object.hasOwn(cp,'usageBudget')).toBe(false)
 expect(events.every(event=>event.version===undefined||event.version===1)).toBe(true)
 expect(projectRunReport(readExecution(cp)!).version).toBe(1)
})

for(const engine of [vercel,deepagents])it(`${engine.id} refuses absent or mutable insufficient token bounds before send`,async()=>{
 for(const mode of ['absent','below-ceiling','mutated-ceiling','missing-revision']){
  const memory=new MemoryRunStore(),budget=new MemoryUsageBudgetStore('usage-project'),call=vi.fn(async()=>final()),session=input(memory,'bound-'+mode,budget,{outputTokens:100000},call)
  if(mode==='absent')delete session.context!.usageBudget!.bounder
  if(mode==='below-ceiling')session.context!.usageBudget!.bounder=async()=>({outputTokens:1})
  if(mode==='mutated-ceiling')session.context!.usageBudget!.bounder=async request=>{request.encodedOutputMaximum=0;return {outputTokens:0}}
  if(mode==='missing-revision'){session.context!.connectionRevisions={};session.context!.connection!.connectionRevision=undefined}
  await consume(engine.start(session));expect(call).not.toHaveBeenCalled();expect((await saved(memory,'bound-'+mode)).usageBudget.refusals[0].name).toBe('UsageBudgetUnsupported')
 }
})

describe('actual budgeted native handoff and callback-only counts',()=>{
 for(const mode of ['complete','no-callback','startup-only','outcome-ack','settle-ack','missing-release'] as const)it(mode,async()=>{
  const memory=new MemoryRunStore(),actual=new MemoryUsageBudgetStore('usage-project');let calls=0,armed=true,settlements=0
  const budget:UsageBudgetStore={resource:actual.resource,reserve:actual.reserve.bind(actual),readReservation:actual.readReservation.bind(actual),settle:async(value,signal)=>{settlements++;const receipt=await actual.settle(value,signal);if(mode==='settle-ack'&&armed){armed=false;throw Error('fixture lost native settlement ack')}return receipt}}
  const store:RunStore=mode==='outcome-ack'?{durability:'memory',load:memory.load.bind(memory),readEvents:memory.readEvents.bind(memory),commit:async(value,signal)=>{const receipt=await memory.commit(value,signal),cp=JSON.parse(new TextDecoder().decode(value.checkpoint!));if(armed&&cp.durable?.dispatches.some((row:any)=>row.kind==='agent'&&row.state==='unknown')){armed=false;throw Error('fixture lost native outcome ack')}return receipt}}:memory
  const {model:_model,...base}=input(store,'native-handoff-'+mode,budget,{providerInvocations:5},vi.fn())
  const run:AgentAssistantSession['agent']['run']=async(request,callbacks)=>{
   calls++
   try{
    expect(Object.getOwnPropertySymbols(request)).toEqual([])
    if(mode==='no-callback'||mode==='startup-only'){if(mode==='startup-only')await callbacks.event({type:'retry',attempt:1});throw new AIFailureError(failure('busy','agent','not-started'))}
    const delegate=request.tools.find(tool=>tool.description.startsWith('delegate_tasks:'))
    if(calls===1){expect(delegate).toBeTruthy();await callbacks.tool({id:'handoff',name:delegate!.name,arguments:{tasks:[{title:'Native child',objective:'Check evidence',profile:'review'}]}},callbacks.signal);throw Error('parent must yield')}
    await callbacks.event({type:'message',id:'answer',text:'Done.',phase:'final'})
   }finally{if(!(mode==='missing-release'&&calls===2))callbacks.providerRelease?.acknowledge(callbacks.providerRelease.invocation)}
  }
  const session:AgentAssistantSession={...base,interactive:true,agent:{model:'fixture',run}}
  await consume(withDelegation(codex).start(session))
  const cp=await saved(memory,'native-handoff-'+mode),entries=cp.usageBudget.entries
  if(mode==='complete'){
   expect(calls).toBe(3);expect(entries).toHaveLength(3);expect(entries.map((row:any)=>row.observed.providerInvocations)).toEqual([1,1,1])
   expect(cp.durable.dispatches.find((row:any)=>row.kind==='agent').state).toBe('unknown')
   expect(entries[0].state).toBe('reported')
  }else if(mode==='missing-release'){
   expect(calls).toBe(2);const child=entries.find((row:any)=>row.reservation.task);expect(child).toBeTruthy()
   expect(cp.durable.dispatches.find((row:any)=>row.id===child.reservation.id).state,'native result acknowledged before release validation').toBe('unknown')
  }else{
   expect(calls).toBe(1);if(mode==='outcome-ack')expect(settlements,'native settlement preceded original outcome acknowledgement').toBe(0);expect(entries).toHaveLength(1);expect(['unknown','claimed']).toContain(entries[0].state)
   if(mode==='settle-ack')expect(entries[0].observed).toEqual({providerInvocations:1})
   if(mode==='no-callback'||mode==='startup-only')expect(entries[0].observed).toBeUndefined()
   const resumed={...session,context:{...session.context,runStore:{store,id:'native-handoff-'+mode,resume:true}}}
   await consume(withDelegation(codex).start(resumed));expect(calls).toBe(1)
  }
 },20_000)
})

for(const engine of [vercel,deepagents])it(`${engine.id} retains actual usage but blocks after lost settlement acknowledgement`,async()=>{
 const memory=new MemoryRunStore(),actual=new MemoryUsageBudgetStore('usage-project');let armed=true
 const budget:UsageBudgetStore={resource:actual.resource,reserve:actual.reserve.bind(actual),readReservation:actual.readReservation.bind(actual),settle:async(value,signal)=>{const receipt=await actual.settle(value,signal);if(armed){armed=false;throw Error('fixture lost model settlement ack')}return receipt}}
 const call=vi.fn(async()=>final());await consume(engine.start(input(memory,'model-settle',budget,{modelRequests:3},call)))
 expect(call).toHaveBeenCalledOnce();const cp=await saved(memory,'model-settle');expect(cp.usageBudget.entries[0].state).toBe('unknown');expect(cp.usageBudget.entries[0].observed.modelRequests).toBe(1)
 await consume(engine.start(input(memory,'model-settle',budget,{modelRequests:3},call,true)));expect(call).toHaveBeenCalledOnce()
})
