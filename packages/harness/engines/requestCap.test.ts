import {afterEach,describe,it,expect,vi} from 'vitest'
import {vercel} from './vercel'
import {deepagents} from './deepagents'
import {codex} from './codex'
import {configureHarness} from '../host'
import {normalize} from '../thinking'
import {readExecution,type ExecutionCheckpoint} from '../execution/state'
import type {AssistantEvent,AssistantSession,Engine,EngineSession} from '../engine'

const json=(text:string,reason='stop',tool=false)=>({id:'cap',object:'chat.completion',created:0,model:'fixture',choices:[{index:0,message:{role:'assistant',content:text,...(tool?{tool_calls:[{id:'one',type:'function',function:{name:'read_policy',arguments:'{}'}}]}:{})},finish_reason:tool?'tool_calls':reason}],usage:{prompt_tokens:1,completion_tokens:1,total_tokens:2}})
async function drain(engine:Engine,input:EngineSession){const events:AssistantEvent[]=[];for await(const event of engine.start(input))events.push(event);return events}
afterEach(()=>configureHarness({endpointCapabilities:()=>({})}))
for(const engine of [vercel,deepagents])describe(`${engine.id} fixed request cap`,()=>{
 const fixture=(kind:string,cap=1)=>{
  let attached:import('../context/session').SessionContext|undefined
  const bodies:Record<string,any>[]=[],saved:ExecutionCheckpoint[]=[],execute=vi.fn(async()=>({content:[{type:'text' as const,text:'Observed policy.'}]}))
  const session:AssistantSession={prompt:'Return the result of the admitted operation.',context:{requestCap:cap,link:{attach(context){attached=context}},currentRequest:{message:'One admitted operation',input:'Trusted reservation'},checkpoint:async cp=>{saved.push(cp);if(kind==='storage'&&cp.used===1)throw Error('durable handoff interrupted')}},allowConversation:kind!=='reformat',interactive:true,critic:kind==='critic'?'on':'off',testPrompt:'',tools:[],hostTools:[{name:'read_policy',effect:'read',description:'Read policy',inputSchema:{type:'object',properties:{}},execute}],callTool:async()=>({content:[]}),model:{family:'openai-compatible',model:'fixture',call:vi.fn(async(_path,request)=>{
   bodies.push(JSON.parse(request.body));expect(saved.at(-1)?.used).toBe(bodies.length);expect(saved.at(-1)?.requestCap).toBe(cap)
   if(kind==='retry')return new Response('{"error":{"code":"server_error"}}',{status:503,headers:{'content-type':'application/json','retry-after':'0'}})
   if(kind==='unknown')throw Error('delivery unknown')
   if(kind==='late-helper'){try{attached!.execution.limitTo(1)}catch{ /* Permanent refusal must survive a caught helper error. */ }}
   const tool=kind==='tool'&&(cap===1||bodies.length===1)
   const text=kind==='empty'?'':kind==='text-tool'?'```json\n{"tool":"read_policy","arguments":{}}\n```':kind==='reformat'?'not a proposal':'Observed answer.'
   return new Response(JSON.stringify(json(text,kind==='truncated'?'length':'stop',tool)),{headers:{'content-type':'application/json'}})
  })},thinking:normalize('off','openai-compatible'),signal:new AbortController().signal}
  return {session,bodies,saved,execute}
 }
 it.each(['normal','empty','truncated','tool','text-tool','retry','reformat','critic','late-helper'])('%s never gets a second attempt or executes final tools',async kind=>{
  if(kind==='text-tool')configureHarness({endpointCapabilities:()=>({nativeToolCalls:false})})
  const f=fixture(kind),pending=drain(engine,f.session)
  await expect(pending).resolves.toBeDefined()
  const events=await pending
  expect(f.bodies).toHaveLength(1);expect(f.bodies[0]!.tools??[]).toHaveLength(0);expect(f.execute).not.toHaveBeenCalled()
  expect(events.some(e=>e.type==='end')).toBe(true)
  if(kind==='late-helper')expect(events.some(e=>e.type==='error'&&e.failure?.code==='configuration')).toBe(true)
  for(const cp of f.saved)expect(readExecution(cp)?.requestCap).toBe(1)
  expect(f.saved.at(-1)).toMatchObject({used:1,limit:1,requestCap:1})
 })
 it('allows the same offered tool before the final request in a two-request reservation',async()=>{
  const f=fixture('tool',2);await drain(engine,f.session)
  expect(f.bodies).toHaveLength(2);expect(f.bodies[0]!.tools.length).toBeGreaterThan(0);expect(f.bodies[1]!.tools??[]).toHaveLength(0);expect(f.execute).toHaveBeenCalledTimes(1)
 })
 it.each(['storage','unknown'])('never replays a durable claim interrupted at %s',async kind=>{
  const f=fixture(kind);await drain(engine,f.session)
  expect(f.bodies).toHaveLength(kind==='storage'?0:1)
  const claimed=f.saved.find(cp=>cp.used===1)!;expect(readExecution(claimed)?.requestCap).toBe(1)
  const resumed=fixture('normal');resumed.session.context!.execution={...claimed,status:'stopped'}
  await drain(engine,resumed.session);expect(resumed.bodies).toHaveLength(0)
 })
})

it('native helper failure is closed without storage',async()=>{
 for(const refused of [false,true]){
  let calls=0
  const input:import('../engine').AgentAssistantSession={prompt:'Return a native answer.',testPrompt:'',allowConversation:true,interactive:true,critic:'off',tools:[],hostTools:[],callTool:async()=>({content:[]}),signal:new AbortController().signal,context:{link:{attach(context){if(refused){try{context.execution.limitTo(1)}catch{ /* The owner must remain closed if the host catches it. */ }}}}},agent:{family:'codex',model:'fixture',run:async(_request,callbacks)=>{calls++;await callbacks.event({type:'message',id:'native',text:'Native answer.',phase:'final'})}}}
  const events=await drain(codex,input)
  expect(calls).toBe(refused?0:1)
  if(refused)expect(events.some(event=>event.type==='error'&&event.failure?.code==='configuration')).toBe(true)
 }
})
