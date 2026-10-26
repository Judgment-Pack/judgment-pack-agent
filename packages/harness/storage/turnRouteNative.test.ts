import {it,expect,vi} from 'vitest'
import {contextualSession} from '../context/session'
import {codex} from '../engines/codex'
import {MemoryRunStore} from './run'
import {MemoryUsageBudgetStore} from './usageBudget'
import {isRouteCapabilityNeeded} from './turnRouteOwner'
import skillVectors from '../../../contract/vectors/skills/declarations.json'
import type {AgentAssistantSession,AssistantEvent} from '../engine'
import type {AgentRun,AgentCallbacks} from '../agent'
const selection={route:'agent' as const,tier:'low',skills:[],tools:[],operations:'all' as const,planning:'withheld' as const}
function fixture(store:MemoryRunStore,run:AgentRun,resume=false):AgentAssistantSession{return {prompt:'Answer this request.',testPrompt:'',tools:[],hostTools:[{name:'lookup',description:'Read one value.',inputSchema:{type:'object',properties:{}},effect:'read',taxonomy:{operation:'read',effect:'read'},execute:vi.fn(async()=>({content:[{type:'text',text:'Value.'}]}))}],callTool:vi.fn(),allowConversation:true,interactive:false,critic:'off',signal:new AbortController().signal,policy:{version:2,routing:{mode:'rules',tiers:['low']}},route:structuredClone(selection),agent:{model:'native',run},context:{connection:{connectionId:'provider',connectionRevision:'one'},connectionRevisions:{provider:'one'},currentRequest:{message:'Question',input:'Fixed'},runStore:{store,id:'native-route',...(resume?{resume:true}:{})}}}}
async function consume(stream:AsyncIterable<AssistantEvent>){const events=[];for await(const event of stream)events.push(event);return events}
async function saved(store:MemoryRunStore){return JSON.parse(new TextDecoder().decode((await store.load('native-route'))!.checkpoint!))}
it('uses the actual acknowledged native route entry',async()=>{
 const store=new MemoryRunStore(),run=vi.fn<AgentRun>(async(_request,callbacks)=>{await callbacks.event({type:'message',id:'done',text:'Done.',phase:'final'})})
 const ready=await contextualSession(fixture(store,run),'codex')
 try {if(!('agent' in ready.session.values))throw Error('agent expected');await ready.session.values.agent.run({prompt:'Question',instructions:'Answer.',tools:[],phase:'author'},{signal:new AbortController().signal,event:async()=>{},tool:async()=>({text:'',isError:false})});expect(run).toHaveBeenCalledOnce()}finally{ready.context.close()}
})
it('records exact superseded claim, restores the full request and rejects late capability callbacks',async()=>{
 const store=new MemoryRunStore(),requests:string[][]=[];let prior:AgentCallbacks|undefined
 const run:AgentRun=async(request,callbacks)=>{requests.push(request.tools.map(t=>t.description.split(':')[0]!));if(requests.length===1){prior=callbacks;try {await callbacks.capabilityNeeded!({kind:'tool',name:'lookup'})}finally {expect(callbacks.providerRelease!.acknowledge(callbacks.providerRelease!.invocation)).toBe(true)}}else await callbacks.event({type:'message',id:'done',text:'Done.',phase:'final'})}
 const events=await consume(codex.start(fixture(store,run)))
 expect(events.filter(e=>e.type==='error')).toEqual([]);expect(requests).toHaveLength(2);expect(requests[0]).not.toContain('lookup');expect(requests[1]).toContain('lookup')
 const cp=await saved(store),marker=cp.turnRoute.widenedInvocation,row=cp.durable.dispatches.find((d:{id:string})=>d.id===marker.dispatchId)
 expect(row).toMatchObject({kind:'agent',state:'unknown',requestDigest:marker.requestDigest});expect(events.filter(e=>e.type==='route_widened')).toHaveLength(1)
 await expect(prior!.capabilityNeeded!({kind:'tool',name:'lookup'})).rejects.toThrow();expect(prior!.providerRelease!.acknowledge(prior!.providerRelease!.invocation)).toBe(false)
 const read=store.readHistoryPage.bind(store);let metadataChanged=false
 const probe=vi.spyOn(store,'readHistoryPage').mockImplementationOnce(async(...args)=>{const page=await read(...args);const head=(await store.readHistoryHead('native-route'))!
 await store.commitHistoryMetadata({runId:'native-route',expectedHistoryRevision:head.revision,expectedMetadataRevision:null,commitId:'feedback-1',record:{version:1,kind:'feedback',feedback:{version:1,id:'rating',runId:'native-route',sequence:1,previous:null,target:{kind:'run'},rating:'good',actor:'user',recordedAt:'2026-10-25T01:02:03.000Z'}}});metadataChanged=true;return page})
 const second=await contextualSession(fixture(store,run,true),'codex');expect(second.context.execution.state.turnRoute).toEqual(cp.turnRoute);second.context.close();expect(metadataChanged).toBe(true);probe.mockRestore()
})

const callbacks=():AgentCallbacks=>({signal:new AbortController().signal,event:async()=>{},tool:async()=>({text:'',isError:false})})
const request={prompt:'Question',instructions:'Answer.',tools:[],phase:'author' as const}
it('restores acknowledged widening with a fresh exact usage reservation and retains unknown audit',async()=>{
 const store=new MemoryRunStore(),budget=new MemoryUsageBudgetStore('route-budget');let sends=0
 const run:AgentRun=async(_request,cb)=>{sends++;if(sends===1){try{await cb.capabilityNeeded!({kind:'tool',name:'lookup'})}finally{cb.providerRelease!.acknowledge(cb.providerRelease!.invocation)}}else {expect(_request.tools.map(t=>t.name)).toContain('lookup');await cb.event({type:'message',id:'done',text:'Done.'})}}
 const input=(resume=false)=>{const value=fixture(store,run,resume);value.context!.usageBudget={store:budget,scope:{resource:'route-budget',id:'shared',revision:'one',limits:{providerInvocations:3}}};return value}
 const first=await contextualSession(input(),'codex');if(!('agent'in first.session.values))throw Error('agent expected')
 let needed:unknown;try{await first.session.values.agent.run(request,callbacks())}catch(error){needed=error};expect(isRouteCapabilityNeeded(needed)).toBe(true)
 await first.context.turnRoute!.widen();const prior=await saved(store);first.context.close()
 const resumed=await contextualSession(input(true),'codex');if(!('agent'in resumed.session.values))throw Error('agent expected')
 await resumed.session.values.agent.run({...request,tools:[{name:'lookup',description:'Read.',inputSchema:{type:'object'}}]},callbacks());resumed.context.close()
 const cp=await saved(store);expect(sends).toBe(2);expect(cp.durable.dispatches).toHaveLength(2);expect(cp.durable.dispatches[0]).toEqual(prior.durable.dispatches[0]);expect(cp.durable.dispatches[0].state).toBe('unknown');expect(cp.usageBudget.entries).toHaveLength(2)
 expect(cp.usageBudget.entries.map((row:any)=>row.reservation.id)).toEqual(cp.durable.dispatches.map((row:any)=>row.id));expect(cp.usageBudget.entries[0].reservation.id).not.toBe(cp.usageBudget.entries[1].reservation.id);expect(cp.usageBudget.entries.map((row:any)=>row.observed.providerInvocations)).toEqual([1,1])
})
for(const proof of ['missing','foreign','duplicate'] as const)it(`refuses widening after ${proof} release proof`,async()=>{
 const store=new MemoryRunStore();let sends=0
 const run:AgentRun=async(_request,cb)=>{sends++;try{await cb.capabilityNeeded!({kind:'tool',name:'lookup'})}finally{if(proof==='foreign')cb.providerRelease!.acknowledge({});if(proof==='duplicate'){cb.providerRelease!.acknowledge(cb.providerRelease!.invocation);cb.providerRelease!.acknowledge(cb.providerRelease!.invocation)}}}
 const ready=await contextualSession(fixture(store,run),'codex');if(!('agent'in ready.session.values))throw Error('agent expected')
 await expect(ready.session.values.agent.run(request,callbacks())).rejects.toThrow();await expect(ready.context.turnRoute!.widen()).rejects.toThrow();expect(sends).toBe(1);expect((await saved(store)).turnRoute.widened).toBe(false);ready.context.close()
})

it('refuses an unrelated retained uncertain native claim after exact widening',async()=>{
 const store=new MemoryRunStore();let sends=0
 const run:AgentRun=async(_request,cb)=>{sends++;if(sends===1)throw Error('fixture unrelated unknown native outcome');try{await cb.capabilityNeeded!({kind:'tool',name:'lookup'})}finally{cb.providerRelease!.acknowledge(cb.providerRelease!.invocation)}}
 const first=await contextualSession(fixture(store,run),'codex');if(!('agent'in first.session.values))throw Error('agent expected')
 await expect(first.session.values.agent.run(request,callbacks())).rejects.toThrow('fixture unrelated')
 await expect(first.session.values.agent.run(request,callbacks())).rejects.toThrow();await first.context.turnRoute!.widen();first.context.close()
 const resumed=await contextualSession(fixture(store,run,true),'codex');if(!('agent'in resumed.session.values))throw Error('agent expected')
 await expect(resumed.session.values.agent.run({...request,tools:[{name:'lookup',description:'Read.',inputSchema:{type:'object'}}]},callbacks())).rejects.toThrow();expect(sends).toBe(2);resumed.context.close()
})
it('refuses the active writer after a committed widening loses its acknowledgement',async()=>{
 const store=new MemoryRunStore();let sends=0
 const run:AgentRun=async(_request,cb)=>{sends++;try{await cb.capabilityNeeded!({kind:'tool',name:'lookup'})}finally{cb.providerRelease!.acknowledge(cb.providerRelease!.invocation)}}
 const ready=await contextualSession(fixture(store,run),'codex');if(!('agent'in ready.session.values))throw Error('agent expected')
 await expect(ready.session.values.agent.run(request,callbacks())).rejects.toThrow()
 const commit=store.commitHistory.bind(store),probe=vi.spyOn(store,'commitHistory').mockImplementationOnce(async(...args)=>{await commit(...args);throw Error('fixture lost widening ACK')})
 await expect(ready.context.turnRoute!.widen()).rejects.toThrow('fixture lost widening ACK');await expect(ready.session.values.agent.run(request,callbacks())).rejects.toThrow();expect(sends).toBe(1);probe.mockRestore();ready.context.close()
})

it('preloads the declared selected skill on fresh and interrupted startup without loading others',async()=>{
 const store=new MemoryRunStore(),run=vi.fn<AgentRun>(),skill=skillVectors.items[1]!,input=(resume=false)=>{const value=fixture(store,run,resume);value.skills=[skill as unknown as import('../skills').SkillDeclaration];value.route={...selection,skills:[skill.id]};return value}
 const first=await contextualSession(input(),'codex');expect(first.context.execution.state.skills?.map(s=>s.id)).toEqual([skill.id]);first.context.close()
 const second=await contextualSession(input(true),'codex');expect(second.context.execution.state.skills?.map(s=>s.id)).toEqual([skill.id]);expect(run).not.toHaveBeenCalled();second.context.close()
})

it('refuses a genuine widened checkpoint transplanted without its owning execution transaction',async()=>{
 const source=new MemoryRunStore(),run:AgentRun=async(_request,cb)=>{try{await cb.capabilityNeeded!({kind:'tool',name:'lookup'})}finally{cb.providerRelease!.acknowledge(cb.providerRelease!.invocation)}}
 const first=await contextualSession(fixture(source,run),'codex');if(!('agent'in first.session.values))throw Error('agent expected');await expect(first.session.values.agent.run(request,callbacks())).rejects.toThrow();await first.context.turnRoute!.widen();first.context.close()
 const checkpoint=(await source.load('native-route'))!.checkpoint!,foreign=new MemoryRunStore(),sent=vi.fn<AgentRun>()
 await foreign.commit({runId:'native-route',expectedRevision:null,commitId:'transplanted-checkpoint',checkpoint,events:[]})
 await expect(contextualSession(fixture(foreign,sent,true),'codex')).rejects.toThrow();expect(sent).not.toHaveBeenCalled()
})
