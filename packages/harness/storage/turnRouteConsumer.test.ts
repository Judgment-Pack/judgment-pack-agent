import {it,expect,vi,describe} from 'vitest'
import {vercel} from '../engines/vercel'
import {deepagents} from '../engines/deepagents'
import {codex} from '../engines/codex'
import {contextualSession} from '../context/session'
import {MemoryRunStore,type RunStore} from './run'
import {MemoryUsageBudgetStore} from './usageBudget'
import type {AssistantSession,AssistantEvent} from '../engine'
import type {RouteSelection} from '../turnRoute'
const selection:RouteSelection={route:'agent',tier:'off',skills:[],tools:[],operations:'all',planning:'withheld'}
const response=(text='Done.',name?:string)=>new Response(JSON.stringify({id:'one',object:'chat.completion',model:'fixture',choices:[{index:0,message:{role:'assistant',content:name?null:text,...(name?{tool_calls:[{id:'call-one',type:'function',function:{name,arguments:'{}'}}]}:{})},finish_reason:name?'tool_calls':'stop'}],usage:{prompt_tokens:3,completion_tokens:2}}),{headers:{'content-type':'application/json'}})
function fixture(store:MemoryRunStore,call:AssistantSession['model']['call'],id='route'):AssistantSession{return {prompt:'Answer this request.',testPrompt:'',model:{family:'openai-compatible',model:'fixture',call},tools:[],hostTools:[{name:'lookup',description:'Read one value.',inputSchema:{type:'object',properties:{}},effect:'read',taxonomy:{operation:'read',effect:'read'},execute:vi.fn(async()=>({content:[{type:'text',text:'Value.'}]}))}],callTool:vi.fn(),allowConversation:true,interactive:false,critic:'off',signal:new AbortController().signal,policy:{version:2,routing:{mode:'rules',tiers:['off','on']}},route:structuredClone(selection),context:{connection:{connectionId:'provider',connectionRevision:'one'},connectionRevisions:{provider:'one'},currentRequest:{message:'Question',input:'Fixed'},runStore:{store,id}}}}
const consume=async(stream:AsyncIterable<AssistantEvent>)=>{const events=[];for await(const e of stream)events.push(e);return events}
const saved=async(store:MemoryRunStore,id='route')=>JSON.parse(new TextDecoder().decode((await store.load(id))!.checkpoint!))
for(const engine of [vercel,deepagents])describe(`${engine.id} actual routed consumer`,()=>{
 it('uses only the resolved tier and narrowed offer, with durable event',async()=>{const store=new MemoryRunStore(),bodies:string[]=[],session=fixture(store,async(_suffix,r)=>{bodies.push(r.body);return response()});const events=await consume(engine.start(session));expect(events.filter(e=>e.type==='error')).toEqual([]);expect(bodies).toHaveLength(1);expect(bodies[0]).not.toContain('lookup');const cp=await saved(store);expect(cp.turnRoute).toMatchObject({tier:'off',decidedBy:'host',tools:[],widened:false});expect(cp.version).toBe(3);expect(events.filter(e=>e.type==='route_decided')).toHaveLength(1)})
 it('widens once for an exact admitted withheld tool before it can execute',async()=>{const store=new MemoryRunStore();let sends=0;const bodies:string[]=[];const session=fixture(store,async(_suffix,r)=>{bodies.push(r.body);return ++sends===1?response('', 'lookup'):response()});const execute=session.hostTools[0]!.execute;const events=await consume(engine.start(session));expect(events.filter(e=>e.type==='error')).toEqual([]);expect(sends).toBe(2);expect(execute).not.toHaveBeenCalled();expect(bodies[0]).not.toContain('lookup');expect(bodies[1]).toContain('lookup');expect((await saved(store)).turnRoute.widened).toBe(true);expect(events.filter(e=>e.type==='route_widened')).toHaveLength(1)})
})
it('pinned admitted route restores without classifier or provider dispatch during bootstrap',async()=>{const store=new MemoryRunStore(),call=vi.fn(async()=>response()),session=fixture(store,call);const first=await contextualSession(session,'vercel');const retained=await saved(store);const firstTools=first.session.values.hostTools.map(t=>t.name);first.context.close();const resumed=fixture(store,call);resumed.context!.runStore!.resume=true;const second=await contextualSession(resumed,'vercel');expect(second.context.execution.state.turnRoute).toEqual(retained.turnRoute);expect(second.session.values.hostTools.map(t=>t.name)).toEqual(firstTools);expect(call).not.toHaveBeenCalled();second.context.close()})
it('opaque native direct route records refusal and uses eligible agent default',async()=>{const store=new MemoryRunStore(),base=fixture(store,vi.fn()),{model:_,...session}=base;const run=vi.fn(async(_request,callbacks)=>{await callbacks.event({type:'message',id:'done',text:'Done.',phase:'final'})});const events=await consume(codex.start({...session,route:{...selection,route:'direct',tier:'low'},policy:{version:2,routing:{mode:'rules',tiers:['low']}},agent:{model:'native',run}}));expect(events.filter(e=>e.type==='error')).toEqual([]);expect(run).toHaveBeenCalledOnce();expect((await saved(store)).turnRoute).toMatchObject({decidedBy:'default',route:'agent',refusals:[{stage:'host',reason:'native-direct-unsupported'}]})})
for(const engine of [vercel,deepagents])describe(`${engine.id} actual classifier stage`,()=>{
 for(const outcome of ['label','unknown','refusal'] as const)it(`uses one bounded physical classification then ${outcome} selection`,async()=>{
  const store=new MemoryRunStore(),bodies:string[]=[]
  const session=fixture(store,async(_suffix,r)=>{bodies.push(r.body);if(bodies.length===1){if(outcome==='refusal')return new Response(JSON.stringify({error:{message:'Rate limited',type:'rate_limit_error'}}),{status:429,headers:{'content-type':'application/json'}});return response(JSON.stringify({label:outcome==='label'?'cheap':'not_declared'}))}return response()})
  delete session.route
  session.policy={version:2,routing:{mode:'auto',tiers:['off','on'],classifier:{labels:{cheap:selection},maxOutputTokens:32,timeoutMilliseconds:1000}}}
  const events=await consume(engine.start(session));expect(events.filter(e=>e.type==='error')).toEqual([]);expect(bodies).toHaveLength(2)
  const request=JSON.parse(bodies[0]!);expect(request.max_tokens??request.max_completion_tokens).toBe(32);expect(bodies[0]).toContain('choose_turn_route');expect(bodies[0]).toContain('cheap')
  const cp=await saved(store);expect(cp.used).toBe(2);expect(cp.turnRoute.decidedBy).toBe(outcome==='label'?'classifier':'default');expect(cp.turnRoute.classifier.promptDigest).toBe('sha256:'+cp.prompts[0].sha256)
  const line=cp.usage.rows.find((row:{role:string})=>row.role==='classifier');expect(line.requests).toBe(1);if(outcome!=='refusal'){expect(line.tokens.inputTokens).toBe(3);expect(line.tokens.outputTokens).toBe(2)}
  expect(cp.completions[0].role).toBe('classifier');expect(cp.turnRoute.classifier.status).toBe(outcome==='label'?'label':outcome==='unknown'?'unknown-label':'refused')
 })
 it('preserves the final author allowance instead of buying a classifier bypass',async()=>{const store=new MemoryRunStore(),call=vi.fn<AssistantSession['model']['call']>(async()=>response()),session=fixture(store,call);delete session.route;session.context!.requestCap=1;session.policy={version:2,routing:{mode:'auto',tiers:['off'],classifier:{labels:{cheap:selection},maxOutputTokens:16,timeoutMilliseconds:1000}}};const events=await consume(engine.start(session));expect(events.filter(e=>e.type==='error')).toEqual([]);expect(call).toHaveBeenCalledOnce();expect(call.mock.calls[0]![1].body).not.toContain('choose_turn_route');expect((await saved(store)).turnRoute.classifier.status).toBe('unsupported')})
})

for(const engine of [vercel,deepagents])describe(`${engine.id} classifier acknowledgement barriers`,()=>{
 for(const fault of ['reserve','journal','outcome','transport','timeout'] as const)it(`does not dispatch a default after ${fault} uncertainty, including explicit resume`,async()=>{
  // Hold only the declared classifier deadline until the real provider callback starts.
  const deadline=new AbortController(),timeouts:number[]=[]
  const originalTimeout=AbortSignal.timeout.bind(AbortSignal)
  const timer=fault==='timeout'?vi.spyOn(AbortSignal,'timeout').mockImplementation(milliseconds=>{if(milliseconds!==10)return originalTimeout(milliseconds);timeouts.push(milliseconds);return deadline.signal}):undefined
  try {
  const memory=new MemoryRunStore();let armed=true,sends=0
  const budget=new MemoryUsageBudgetStore('classifier-budget',{afterChange(){if(fault==='reserve'&&armed){armed=false;throw Error('fixture lost classifier Reserve ack')}}})
  const store:RunStore=['journal','outcome'].includes(fault)?{durability:'memory',load:memory.load.bind(memory),readEvents:memory.readEvents.bind(memory),commit:async(value,signal)=>{
   const receipt=await memory.commit(value,signal),cp=JSON.parse(new TextDecoder().decode(value.checkpoint!))
   if(armed&&(fault==='journal'&&cp.usageBudget?.entries.length||fault==='outcome'&&cp.durable?.dispatches.some((d:{scope:{role:string};state:string})=>d.scope.role==='classifier'&&d.state==='result'))){armed=false;throw Error('fixture lost classifier '+fault+' ack')}
   return receipt
  }}:memory
  const call:AssistantSession['model']['call']=async(_suffix,request)=>{sends++;if(fault==='transport')throw Error('fixture unknown classifier transport');if(fault==='timeout')return new Promise((_,reject)=>{request.signal!.addEventListener('abort',()=>reject(request.signal!.reason),{once:true});deadline.abort(new DOMException('Fixture classifier deadline','TimeoutError'))});return response(JSON.stringify({label:'cheap'}))}
  const input=()=>{const session=fixture(memory,call);delete session.route;session.context!.runStore!.store=store;session.policy={version:2,routing:{mode:'auto',tiers:['off'],classifier:{labels:{cheap:selection},maxOutputTokens:16,timeoutMilliseconds:fault==='timeout'?10:1000}}};session.context!.usageBudget={store:budget,scope:{resource:'classifier-budget',id:'shared',revision:'one',limits:{modelRequests:4}}};return session}
  const events=await consume(engine.start(input()));expect(events.some(e=>e.type==='error')).toBe(true);expect(events.some(e=>e.type==='route_decided')).toBe(false);const physical=fault==='reserve'||fault==='journal'?0:1;expect(sends).toBe(physical)
  if(fault==='timeout')expect(timeouts).toEqual([10])
  const retained=await saved(memory);expect(retained.turnRoute.reason).toBe('classifier-pending')
  const resumed=input();resumed.context!.runStore!.resume=true;const resumedEvents=await consume(engine.start(resumed));expect(sends).toBe(physical)
  if(fault==='timeout'){
   expect(resumedEvents.some(e=>e.type==='error')).toBe(true)
   expect(resumedEvents.some(e=>e.type==='route_decided')).toBe(false)
   expect((await saved(memory)).turnRoute.reason).toBe('classifier-pending')
   expect(timeouts).toEqual([10])
  }
  } finally {timer?.mockRestore()}
 })
 it('fails a classifier deadline before dispatch with zero physical calls and no default',async()=>{
  const deadline=new AbortController(),timeouts:number[]=[],originalTimeout=AbortSignal.timeout.bind(AbortSignal)
  deadline.abort(new DOMException('Fixture classifier deadline','TimeoutError'))
  const timer=vi.spyOn(AbortSignal,'timeout').mockImplementation(milliseconds=>{if(milliseconds!==10)return originalTimeout(milliseconds);timeouts.push(milliseconds);return deadline.signal})
  try {
   const memory=new MemoryRunStore(),call=vi.fn<AssistantSession['model']['call']>(async()=>response(JSON.stringify({label:'cheap'}))),session=fixture(memory,call)
   delete session.route
   session.policy={version:2,routing:{mode:'auto',tiers:['off'],classifier:{labels:{cheap:selection},maxOutputTokens:16,timeoutMilliseconds:10}}}
   const events=await consume(engine.start(session))
   expect(timeouts).toEqual([10]);expect(call).not.toHaveBeenCalled()
   expect(events.some(e=>e.type==='error')).toBe(true);expect(events.some(e=>e.type==='route_decided')).toBe(false)
   expect((await saved(memory)).turnRoute.reason).toBe('classifier-pending')
  } finally {timer.mockRestore()}
 })
})
for(const engine of [vercel,deepagents])it(`${engine.id} leaves routing-off physical bytes and historical producer versions unchanged`,async()=>{
 let nextId=0;const fixed=vi.spyOn(crypto,'randomUUID').mockImplementation(()=>`00000000-0000-4000-8000-${String(++nextId).padStart(12,'0')}`)
 try{const bodies:string[]=[];for(const mode of ['absent','off']){nextId=0;const memory=new MemoryRunStore(),session=fixture(memory,async(_suffix,r)=>{bodies.push(r.body);return response()});delete session.route;session.policy=mode==='off'?{version:2,routing:{mode:'off',tiers:['off']}}:{version:1};const events=await consume(engine.start(session));expect(events.filter(e=>e.type==='error')).toEqual([]);const cp=await saved(memory);expect(cp.version).toBe(2);expect(cp.turnRoute).toBeUndefined();expect(events.some(e=>e.type==='route_decided'||e.type==='route_widened')).toBe(false)}expect(bodies).toHaveLength(2);expect(bodies[1]).toBe(bodies[0])}finally{fixed.mockRestore()}
})

it('uses an actual narrowed native request without claiming opaque direct/classifier support',async()=>{
 const store=new MemoryRunStore(),base=fixture(store,vi.fn()),{model:_,...session}=base,tools:string[][]=[]
 const run=vi.fn(async(request,callbacks)=>{tools.push(request.tools.map((t:{name:string})=>t.name));await callbacks.event({type:'message',id:'done',text:'Done.',phase:'final'})})
 const events=await consume(codex.start({...session,route:{...selection,tier:'low'},policy:{version:2,routing:{mode:'rules',tiers:['low']}},agent:{model:'native',run}}))
 expect(events.filter(e=>e.type==='error')).toEqual([]);expect(run).toHaveBeenCalledOnce();expect(tools[0]).not.toContain('lookup');expect((await saved(store)).turnRoute).toMatchObject({decidedBy:'host',route:'agent',tools:[]})
})
