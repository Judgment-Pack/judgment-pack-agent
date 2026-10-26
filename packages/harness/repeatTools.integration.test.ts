import {it,expect,vi} from 'vitest'
import {vercel} from './engines/vercel'
import {deepagents} from './engines/deepagents'
import {codex} from './engines/codex'
import {contextualSession} from './context/session'
import {MemoryRunStore,type RunStore} from './storage/run'
import {durableSession,durableOwner,scopeFor,childDurability,RunWriter,StorageOutcomeUnknown} from './storage/writer'
import {REPEAT_REMINDER} from './prompt'
import {RepeatedToolLimit} from './repeatTools'
import type {AssistantSession,AgentAssistantSession,AssistantEvent} from './engine'
const response=(name?:string,id='wire')=>new Response(JSON.stringify({id:'answer',object:'chat.completion',model:'fixture',choices:[{index:0,message:{role:'assistant',content:name?null:'Done.',...(name?{tool_calls:[{id,type:'function',function:{name,arguments:'{}'}}]}:{})},finish_reason:name?'tool_calls':'stop'}],usage:{prompt_tokens:2,completion_tokens:1}}),{headers:{'content-type':'application/json'}})
export const repeatInput=(store:RunStore,stopAt=2,resume=false):AssistantSession=>({prompt:'Read the value.',testPrompt:'',allowConversation:true,interactive:false,critic:'off',tools:[],hostTools:[{name:'lookup',description:'Read one value.',inputSchema:{type:'object',properties:{}},effect:'read',taxonomy:{operation:'read',effect:'read'},execute:vi.fn(async()=>({content:[{type:'text',text:'Value.'}]}))}],callTool:vi.fn(),model:{family:'openai-compatible',model:'fixture',call:vi.fn(async()=>response())},signal:new AbortController().signal,policy:{version:2,repeatTools:{mode:'on',stopAt}},context:{currentRequest:{message:'Read',input:'Fixed'},runStore:{store,id:'repeat-204',...(resume?{resume:true}:{})}}})
const saved=async(store:RunStore)=>JSON.parse(new TextDecoder().decode((await store.load('repeat-204'))!.checkpoint!))
const consume=async(stream:AsyncIterable<AssistantEvent>)=>{const events=[];for await(const event of stream)events.push(event);return events}
for(const engine of [vercel,deepagents])it(`#204 ${engine.id} reminds once and stops before extra physical dispatch`,async()=>{
 const store=new MemoryRunStore(),input=repeatInput(store),bodies:string[]=[]
 input.model.call=async(_suffix,request)=>{bodies.push(request.body);return response('lookup',`wire-${bodies.length}`)}
 const events=await consume(engine.start(input)),cp=await saved(store)
 expect(bodies).toHaveLength(3);expect(input.hostTools[0]!.execute).toHaveBeenCalledTimes(3)
 expect(bodies.filter(body=>body.includes(REPEAT_REMINDER))).toHaveLength(1)
 expect(bodies[2]).toContain('package repeat reminder');expect(events.filter(e=>e.type==='repeat_detected')).toHaveLength(2)
 expect(cp.repeatTools.executions[0]).toMatchObject({count:2,reminded:true,reminderDelivered:true,stopped:true})
 expect(cp).toMatchObject({version:3,status:'stopped',reason:'RepeatedToolLimit'})
 expect(cp.durable.dispatches.filter((row:any)=>row.kind==='host').map((row:any)=>row.result)).toEqual(Array(3).fill({content:[{type:'text',text:'Value.'}]}))
 const restored=repeatInput(store,2,true);await consume(engine.start(restored));expect(restored.model.call).not.toHaveBeenCalled();expect(restored.hostTools[0]!.execute).not.toHaveBeenCalled()
})
it('#204 codex original native invocation stops subsequent tool callbacks and next invocation',async()=>{
 const store=new MemoryRunStore(),input=repeatInput(store),runs=vi.fn(async(_request:any,callbacks:any)=>{for(let i=0;i<4;i++)await callbacks.tool({name:_request.tools.find((tool:any)=>tool.description.startsWith('lookup:')).name,arguments:{},id:`call-${i}`},callbacks.signal)})
 const {model,...base}=input;const native:AgentAssistantSession={...base,agent:{model:'fixture',run:runs},context:{...base.context,connection:{connectionId:'native',connectionRevision:'one'},connectionRevisions:{native:'one'}}}
 await consume(codex.start(native));const cp=await saved(store)
 expect(runs).toHaveBeenCalledOnce();expect(input.hostTools[0]!.execute).toHaveBeenCalledTimes(3);expect(cp.reason).toBe('RepeatedToolLimit')
})
it('#204 cumulative counts survive role changes, changed results, interleaving and children',async()=>{
 const store=new MemoryRunStore(),raw=await durableSession(repeatInput(store,5)),ready=await contextualSession(raw),binding=raw.context![durableOwner]!
 const invoke=(args:unknown,result:unknown,role='author',source='source',owner=binding)=>binding.writer.invoke(scopeFor(owner,role),'host',source,'lookup',JSON.stringify(args),true,async()=>result)
 try{
  await invoke({n:1},{value:1});await invoke({n:1},{value:2});await invoke({n:2},{value:1});await invoke({n:1},{value:1},'critic')
  expect(binding.writer.repeatState(binding)!.count).toBe(1)
  const child=childDurability(repeatInput(store,5),raw,'child-execution'),childReady=await contextualSession(child),childBinding=child.context![durableOwner]!
  try{await invoke({n:1},{value:1},'author','source',childBinding);await invoke({n:1},{value:1},'author','source',childBinding);expect(binding.writer.repeatState(childBinding)!.count).toBe(1)}finally{childReady.context.close()}
  expect(binding.writer.repeatState(binding)!.count).toBe(1)
  await invoke({n:2},{value:1});expect(binding.writer.repeatState(binding)!.count).toBe(2)
 }finally{ready.context.close()}
 const resumed=await contextualSession(repeatInput(store,5,true));try{expect(resumed.context.execution.requestState()).toContain(REPEAT_REMINDER)}finally{resumed.context.close()}
})
for(const phase of ['before','after'] as const)it(`#204 ${phase} result ACK loss does not recount or redispatch uncertain mutations`,async()=>{
 const memory=new MemoryRunStore();let armed=false
 const store:RunStore={durability:'memory',load:memory.load.bind(memory),readEvents:memory.readEvents.bind(memory),commit:async input=>{
  const cp=JSON.parse(new TextDecoder().decode(input.checkpoint!)),hit=armed&&cp.repeatTools?.executions[0].count===1
  if(hit)armed=false;if(hit&&phase==='before')throw Error('fixture before result ACK');const receipt=await memory.commit(input);if(hit)throw Error('fixture lost result ACK');return receipt
 }}
 const raw=await durableSession(repeatInput(store,5)),ready=await contextualSession(raw),binding=raw.context![durableOwner]!,execute=vi.fn(async()=>({value:1}))
 const first=()=>binding.writer.invoke(binding,'host','source','lookup','{}',true,execute)
 await first();armed=true;await expect(first()).rejects.toBeInstanceOf(StorageOutcomeUnknown);await expect(first()).rejects.toThrow();expect(execute).toHaveBeenCalledTimes(2);ready.context.close()
 const next=await durableSession(repeatInput(store,5,true)),resumed=await contextualSession(next),held=next.context![durableOwner]!
 try{
  expect(held.writer.repeatState(held)!.count).toBe(phase==='after'?1:0)
  const count=held.writer.repeatState(held)!.seen.length
  if(phase==='before')expect((await saved(memory)).durable.dispatches.some((row:any)=>row.state==='unknown'||row.state==='claimed')).toBe(true)
  // Non-reading uncertain operation reuse is covered by the original writer tests; detection never performs a replay.
  expect(execute).toHaveBeenCalledTimes(2);expect(held.writer.repeatState(held)!.seen).toHaveLength(count)
 }finally{resumed.context.close()}
})
it('#204 rejects retained missing observations and enforces stopped physical admissions for every kind',async()=>{
 const store=new MemoryRunStore(),raw=await durableSession(repeatInput(store,1)),ready=await contextualSession(raw),binding=raw.context![durableOwner]!,run=vi.fn(async()=>({value:1}))
 await binding.writer.invoke(binding,'host','source','lookup','{}',true,run);await binding.writer.invoke(binding,'host','source','lookup','{}',true,run)
 for(const kind of ['host','runtime','model','agent'] as const)await expect(binding.writer.invoke(scopeFor(binding,'critic'),kind,'source','lookup','{}',true,run)).rejects.toBeInstanceOf(RepeatedToolLimit)
 expect(run).toHaveBeenCalledTimes(2);ready.context.close()
 const cp=await saved(store);cp.repeatTools.executions[0]={...cp.repeatTools.executions[0],count:0,reminded:false,stopped:false,seen:[]}
 const head=(await store.load('repeat-204'))!;const {recordDigest}=await import('./records');cp.digest=recordDigest(cp)
 expect(()=>new RunWriter(store,'repeat-204',{...head,checkpoint:new TextEncoder().encode(JSON.stringify(cp))})).toThrow()
})
it('#204 retained reminder is assembled once after resume and acknowledged with the real model claim',async()=>{
 const store=new MemoryRunStore(),raw=await durableSession(repeatInput(store,5)),ready=await contextualSession(raw),binding=raw.context![durableOwner]!
 for(let i=0;i<2;i++)await binding.writer.invoke(binding,'host','source','lookup','{}',true,async()=>({value:1}))
 expect(ready.context.execution.requestState()).toContain(REPEAT_REMINDER);ready.context.close()
 const resumed=await contextualSession(repeatInput(store,5,true));try{
  const guidance=resumed.context.execution.requestState();expect(guidance).toContain('package repeat reminder')
  if(!('model'in resumed.session.values))throw Error('model expected')
  await resumed.session.values.model.call('',{method:'POST',body:JSON.stringify({model:'fixture',messages:[{role:'user',content:guidance}]})})
  expect((await saved(store)).repeatTools.executions[0]).toMatchObject({reminded:true,reminderDelivered:true,count:1})
  expect(resumed.context.execution.requestState()).not.toContain(REPEAT_REMINDER)
 }finally{resumed.context.close()}
 const again=await contextualSession(repeatInput(store,5,true));try{expect(again.context.execution.requestState()).not.toContain(REPEAT_REMINDER)}finally{again.context.close()}
})
it('#204 an accepted plan from the actual ChatStore cannot clear a durable repeat stop',async()=>{
 const {MemoryChatStore}=await import('./storage/chat'),{planTools,acceptPlan,currentChat}=await import('./planning/tools')
 const chats=new MemoryChatStore([{chat:{id:'repeat-chat',interaction:'plan',plans:[]}}]),signal=new AbortController().signal,resolver={resolve:async()=>{throw Error('No references in fixture')}}
 await planTools(chats,'repeat-chat',resolver)[0]!.execute({objective:'Read one source.',steps:['Inspect source'],questions:[],references:[]},signal)
 const chat=(await currentChat(chats,'repeat-chat'))!;await acceptPlan(chats,chat,chat.plans!.at(-1)!,signal,resolver)
 const accepted=(await currentChat(chats,'repeat-chat'))!.plans!.at(-1)! as import('./planning/model').ChatPlan,store=new MemoryRunStore(),input=repeatInput(store,1)
 input.context={...input.context,phase:'work',plan:accepted};const ready=await contextualSession(input)
 try{const tool=ready.session.values.hostTools.find(tool=>tool.name==='lookup')!;await tool.execute({},signal);await tool.execute({},signal);await expect(tool.execute({},signal)).rejects.toBeInstanceOf(RepeatedToolLimit);expect((await saved(store)).reason).toBe('RepeatedToolLimit');expect((await currentChat(chats,'repeat-chat'))!.plans!.at(-1)!.status).toBe('accepted')}finally{ready.context.close()}
})
it('#204 original writer retains exact changed result encodings across explicit resume',async()=>{
 const store=new MemoryRunStore(),raw=await durableSession(repeatInput(store,5)),ready=await contextualSession(raw),binding=raw.context![durableOwner]!
 for(const value of [{a:1,b:2},{b:2,a:1}])await binding.writer.invoke(binding,'host','source','lookup','{}',true,async()=>value)
 expect(binding.writer.repeatState(binding)!.count).toBe(0);const cp=await saved(store)
 expect(cp.durable.dispatches.filter((row:any)=>row.kind==='host').map((row:any)=>row.repeatResultJson)).toEqual(['{"a":1,"b":2}','{"b":2,"a":1}']);ready.context.close()
 const resumedInput=await durableSession(repeatInput(store,5,true)),resumed=await contextualSession(resumedInput),held=resumedInput.context![durableOwner]!
 try{await held.writer.invoke(held,'host','source','lookup','{}',true,async()=>({b:2,a:1}));expect(held.writer.repeatState(held)!.count).toBe(1)}finally{resumed.context.close()}
})
it('#204 empty enabled state is acknowledged before first dispatch and survives startup interruption',async()=>{
 const store=new MemoryRunStore(),input=repeatInput(store,5),first=await contextualSession(input)
 const cp=await saved(store);expect(cp.version).toBe(3);expect(cp.repeatTools.executions[0]).toMatchObject({count:0,seen:[],reminded:false,reminderDelivered:false,stopped:false})
 expect(input.model.call).not.toHaveBeenCalled();first.context.close()
 const resumed=await contextualSession(repeatInput(store,5,true));try{expect(resumed.context.execution.requestState()).not.toContain(REPEAT_REMINDER);const tool=resumed.session.values.hostTools.find(tool=>tool.name==='lookup')!;await tool.execute({},resumed.session.signal);await tool.execute({},resumed.session.signal);expect((await saved(store)).repeatTools.executions[0].count).toBe(1)}finally{resumed.context.close()}
})
