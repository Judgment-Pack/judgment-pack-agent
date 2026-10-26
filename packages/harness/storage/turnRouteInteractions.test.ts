import {describe,expect,it,vi} from 'vitest'
import {vercel} from '../engines/vercel'
import {deepagents} from '../engines/deepagents'
import {contextualSession} from '../context/session'
import {MemoryRunStore} from './run'
import {MemoryUsageBudgetStore} from './usageBudget'
import {toolListDigest} from '../toolSources'
import type {AssistantEvent,AssistantSession,Engine} from '../engine'
import {withDelegation} from '../delegation'
import profileDeclaration from '../../../contract/vectors/subagents/declaration.json'
import type {SubagentDeclaration} from '../subagents'
import type {RouteSelection} from '../turnRoute'

const direct:RouteSelection={route:'direct',tier:'off',skills:[],tools:[],operations:'all',planning:'withheld'}
const reply=(name?:string,args:Record<string,unknown>={})=>new Response(JSON.stringify({id:'fixture',object:'chat.completion',model:'fixture',choices:[{index:0,message:{role:'assistant',content:name?null:'Done.',...(name?{tool_calls:[{id:'fixed-call',type:'function',function:{name,arguments:JSON.stringify(args)}}]}:{})},finish_reason:name?'tool_calls':'stop'}],usage:{prompt_tokens:3,completion_tokens:2}}),{headers:{'content-type':'application/json'}})
function fixture(store:MemoryRunStore,call:AssistantSession['model']['call']):AssistantSession {
 return {prompt:'Inspect the evidence.',testPrompt:'',model:{family:'openai-compatible',model:'fixture',call},tools:[],hostTools:[{name:'lookup',description:'Read a value.',inputSchema:{type:'object',properties:{}},effect:'read',taxonomy:{operation:'read',effect:'read'},execute:vi.fn(async()=>({content:[{type:'text',text:'Value.'}]}))}],callTool:vi.fn(),allowConversation:true,interactive:false,critic:'off',signal:new AbortController().signal,route:structuredClone(direct),policy:{version:2,routing:{mode:'rules',tiers:['off','on']}},context:{connection:{connectionId:'provider',connectionRevision:'one'},connectionRevisions:{provider:'one'},currentRequest:{message:'Inspect evidence',input:'Fixed.'},runStore:{store,id:'route-interactions'}}}
}
async function consume(stream:AsyncIterable<AssistantEvent>){const events:AssistantEvent[]=[];for await(const event of stream)events.push(event);return events}
async function checkpoint(store:MemoryRunStore){return JSON.parse(new TextDecoder().decode((await store.load('route-interactions'))!.checkpoint!))}

for(const engine of [vercel,deepagents])describe(`${engine.id} route interaction acceptance`,()=>{
 it('escalates direct to agent once for the withheld planner, then admits a fresh full request',async()=>{
  const store=new MemoryRunStore(),budget=new MemoryUsageBudgetStore('route-interactions-budget'),bodies:string[]=[],plan={explanation:'Inspect in order.',plan:[{step:'Inspect supplied evidence',status:'pending'}]}
  const session=fixture(store,async(_suffix,request)=>{
   bodies.push(request.body)
   const cp=await checkpoint(store)
   if(bodies.length===1){expect(cp.plan??[]).toEqual([]);return reply('update_plan',plan)}
   expect(cp.turnRoute).toMatchObject({route:'agent',widened:true,widenedFor:'update_plan'})
   if(bodies.length===2){expect(cp.plan??[]).toEqual([]);return reply('update_plan',plan)}
   return reply()
  })
  session.context!.usageBudget={store:budget,scope:{resource:'route-interactions-budget',id:'shared',revision:'one',limits:{modelRequests:4}}}
  const events=await consume(engine.start(session))
  expect(events.filter(e=>e.type==='error')).toEqual([])
  const cp=await checkpoint(store)
  expect(bodies).toHaveLength(3);expect(JSON.parse(bodies[0]!).tools??[]).toEqual([]);expect(JSON.parse(bodies[1]!).tools.some((t:{function:{name:string}})=>t.function.name==='update_plan')).toBe(true)
  expect(events.filter(e=>e.type==='route_widened')).toHaveLength(1)
  expect(cp.plan).toMatchObject([{step:'Inspect supplied evidence',status:'pending'}])
  const rows=cp.usageBudget.entries
  expect(rows).toHaveLength(3);expect(new Set(rows.map((r:{reservation:{id:string}})=>r.reservation.id)).size).toBe(3)
  expect(rows.every((r:{reservation:{role:string}})=>r.reservation.role==='author')).toBe(true)
 })
 it('preserves Plan phase and source effect authority after widening a direct route',async()=>{
  const store=new MemoryRunStore(),bodies:string[]=[],session=fixture(store,async(_suffix,request)=>{
   bodies.push(request.body)
   if(bodies.length===1)return reply('lookup')
   expect((JSON.parse(request.body).tools??[]).some((t:{function:{name:string}})=>t.function.name.endsWith('external_write'))).toBe(false)
   if(bodies.length===2)return reply('route-actions__external_write')
   return reply()
  })
  const execute=vi.fn(async()=>({content:[]}))
  const action={name:'external_write',description:'Write an external value.',inputSchema:{type:'object'},taxonomy:{operation:'act' as const,effect:'act' as const},source:{kind:'gateway' as const,id:'route-actions',trust:'connected' as const,version:'one',identity:{kind:'connection' as const,label:'route-actions'},toolListDigest:''},execute}
  action.source.toolListDigest=await toolListDigest([action])
  session.hostTools.push(action)
  session.context!.phase='plan';session.critic='on';session.testPrompt='Test the artifact.'
  const events=await consume(engine.start(session))
  expect(events.filter(e=>e.type==='error')).toEqual([])
  const cp=await checkpoint(store)
  expect(execute).not.toHaveBeenCalled();expect(bodies).toHaveLength(3)
  expect(events.filter(e=>e.type==='route_widened')).toHaveLength(1)
  expect(cp.phase).toBe('plan');expect(cp.turnRoute.route).toBe('agent')
  expect(events.some(e=>e.type==='tool_result'&&e.name==='route-actions__external_write'&&e.isError)).toBe(true)
  expect(events.some(e=>e.type==='critique')).toBe(false)
 })
 it('refuses a child above its pinned profile context ceiling after direct-to-agent widening',async()=>{
  const store=new MemoryRunStore(),definition=profileDeclaration.items[0]!,bodies:string[]=[],children:AssistantSession[]=[]
  const session=fixture(store,async(_suffix,request)=>{
   bodies.push(request.body)
   if(bodies.length<=2)return reply('delegate_tasks',{tasks:[{title:'Evidence',objective:'Inspect one assigned excerpt.',profile:'auditor'}]})
   return reply()
  })
  session.subagents=[definition as SubagentDeclaration];session.context!.requestCap=6;session.interactive=true
  const observed:Engine={id:engine.id,start(input){if(input.context?.objective){expect('model' in input).toBe(true);children.push(input as AssistantSession)}return engine.start(input)}}
  const events=await consume(withDelegation(observed).start(session)),cp=await checkpoint(store)
  expect(events.filter(e=>e.type==='error')).toEqual([])
  expect(children,JSON.stringify(events.filter(e=>e.type==='tool_result'||e.type==='delegated_task'))).toHaveLength(1)
  expect(bodies).toHaveLength(3);expect(cp.used).toBe(3)
  expect(events.filter(e=>e.type==='route_widened')).toHaveLength(1)
  expect(cp.turnRoute).toMatchObject({route:'agent',widened:true,widenedFor:'delegate_tasks'})
  const child=children[0]!
  expect(child.childDefinition!.id).toBe('auditor')
  expect(child.childDefinition!.bounds).toEqual(definition.bounds)
  expect(child.context!.requestCap).toBeLessThanOrEqual(definition.bounds.modelRequests)
  expect(child.context!.requestCap).toBeGreaterThan(0)
  expect(child.critic).toBe('off')
  expect(events.filter(e=>e.type==='delegated_task').at(-1)).toMatchObject({type:'delegated_task',task:{profile:'auditor',state:'failed',modelRequests:0,toolCalls:0,budget:{modelRequests:3,toolCalls:2,milliseconds:30000},failure:{code:'context-limit'}}})
  expect(child.policy?.budget?.maxInputBytes).toBe(definition.bounds.bytes)
 })
 it('retains classifier cancellation uncertainty through explicit resume without another request',async()=>{
  const store=new MemoryRunStore(),budget=new MemoryUsageBudgetStore('cancel-budget'),controller=new AbortController();let sends=0
  const input=(resume=false)=>{
   const session=fixture(store,async(_suffix,request)=>{sends++;controller.abort(new DOMException('Fixture stopped','AbortError'));request.signal!.throwIfAborted();return reply()})
   delete session.route;session.signal=resume?new AbortController().signal:controller.signal
   session.policy={version:2,routing:{mode:'auto',tiers:['off'],classifier:{labels:{cheap:direct},maxOutputTokens:16,timeoutMilliseconds:1000}}}
   session.context!.usageBudget={store:budget,scope:{resource:'cancel-budget',id:'shared',revision:'one',limits:{modelRequests:4}}};session.context!.runStore!.resume=resume
   return session
  }
  const first=await consume(engine.start(input())),retained=await checkpoint(store)
  expect(sends).toBe(1);expect(first.some(e=>e.type==='route_decided')).toBe(false)
  expect(retained.turnRoute.reason).toBe('classifier-pending')
  const reservations=retained.usageBudget.entries.map((r:{reservation:{id:string}})=>r.reservation.id)
  const resumed=await consume(engine.start(input(true)))
  expect(sends).toBe(1);expect(resumed.some(e=>e.type==='route_decided')).toBe(false)
  expect((await checkpoint(store)).usageBudget.entries.map((r:{reservation:{id:string}})=>r.reservation.id)).toEqual(reservations)
 })
})

it('keeps critic role tier, critic tool ceiling and child request ceiling independent of the route',async()=>{
 const store=new MemoryRunStore(),session=fixture(store,vi.fn())
 session.critic='on';session.criticReasoning='on';session.testPrompt='Check the supplied artifact.'
 const ready=await contextualSession(session,'vercel')
 try{
  if(!('model' in ready.session.values))throw Error('model expected')
  expect(ready.session.values.reasoning?.tier).toBe('off')
  expect(ready.session.values.forRole('critic').reasoning?.tier).toBe('on')
  ready.context.beginCriticPhase()
  const lookup=ready.session.values.forRole('critic').hostTools.find(t=>t.name==='lookup')
  // The role view cannot expand the underlying critic admission boundary.
  expect(lookup).toBeDefined()
  expect(()=>ready.context.admit('lookup','host')).toThrow()
  expect(ready.context.turnRoute!.route).toMatchObject({route:'direct',widened:false})
 }finally{ready.context.close()}
 const childStore=new MemoryRunStore(),child=fixture(childStore,vi.fn())
 child.context!.objective='Read only the assigned evidence.';child.context!.requestCap=2;child.critic='on';child.testPrompt='Check artifact.'
 const scoped=await contextualSession(child,'vercel')
 try{
  expect(scoped.context.turnRoute).toBeUndefined()
  expect(scoped.context.execution.state.requestCap).toBe(2)
  expect(scoped.context.execution.remaining).toBe(2)
  expect(scoped.context.review.offered).toBe(false)
  expect(()=>scoped.context.admit('delegate_tasks','host')).toThrow('A child cannot delegate')
 }finally{scoped.context.close()}
})
