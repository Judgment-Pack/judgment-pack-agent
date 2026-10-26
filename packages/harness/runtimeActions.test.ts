import {contextualSession} from './context/session'
import canonicalVectors from '../../contract/testdata/runtime-actions/canonical.json'
import pauseVectors from '../../contract/vectors/approval/runtime-pause.json'
import requestVectors from '../../contract/vectors/approval/runtime-request.json'
import {sha256} from './records'
import {runtimeJSON} from './storage/runtimeActionOwner'
import {projectRunReport,readRunReport} from './runReport'
import {describe,it,expect} from 'vitest'
import type {Transport} from '@modelcontextprotocol/sdk/shared/transport.js'
import type {JSONRPCMessage} from '@modelcontextprotocol/sdk/types.js'
import {openAssistantConnection} from './session'
import {vercel} from './engines/vercel'
import {codex} from './engines/codex'
import {MemoryRunStore,type RunStore} from './storage/run'
import type {RuntimeExecutionOwner,RuntimeActionPort} from './runtimeActions'
import {validRuntimeOwner,validRuntimeBinding} from './runtimeActions'
import type {AssistantEvent,EngineSession,McpTool} from './engine'
import {answerWith} from './testing/scenarioDriver'
import type {AgentRun} from './agent'
import {readExecution} from './execution/state'
import {readRunEvent} from './recordReader'
import {canonicalJSON} from './context/chat'

const pin='sha256:'+'a'.repeat(64)
const owner=():RuntimeExecutionOwner=>({artifactDigest:pin,processId:'launch-one',projectId:'project-one',projectLabel:'Example project',sessionId:'server-session-one',principalDigest:pin,principalLabel:'Project editor',scopeDigest:pin,scopeLabel:'Evaluate example project'})
const evaluate=():McpTool=>({name:'experimental_evaluate',description:'Evaluate one unsaved draft',inputSchema:{type:'object',properties:{value:{type:'integer'},rehearsal:{type:'boolean'}}},_meta:{operation:'evaluate',effect:'act',experimental:true}})
const raw={value:7,rehearsal:false},expected={value:7,rehearsal:true}
async function fixture(port?:RuntimeActionPort){
 let tools=[evaluate()],closed=false
 const calls:{name:string;arguments:Record<string,unknown>}[]=[]
 const transport:Transport={
  start:async()=>{},close:async()=>{closed=true},
  send:async(message:JSONRPCMessage)=>{
   const m=message as {id?:string|number;method?:string;params?:{name:string;arguments:Record<string,unknown>}}
   if(m.id===undefined)return
   let result:unknown
   if(m.method==='initialize')result={protocolVersion:'2025-06-18',capabilities:{tools:{}},serverInfo:{name:'bounded-runtime',version:'fixture'}}
   else if(m.method==='tools/list')result={tools}
   else if(m.method==='tools/call'){calls.push(structuredClone(m.params!));result={content:[{type:'text',text:'evaluated draft'}],structuredContent:{status:'ok',auditAppended:false}}}
   else throw Error('unexpected method')
   queueMicrotask(()=>transport.onmessage?.({jsonrpc:'2.0',id:m.id!,result:result as Record<string,unknown>}))
  }
 }
 const connection=openAssistantConnection({allowed:['experimental_evaluate'],transport,onEvent:()=>{},runtimeActions:port})
 const ready=await connection.ready
 return {...ready,calls,close:connection.close,setTools:(next:McpTool[])=>{tools=next},closed:()=>closed}
}
async function collect(session:EngineSession,native:boolean){const events:AssistantEvent[]=[];for await(const event of (native?codex:vercel).start(session))events.push(event);return events}
function input(runtime:Awaited<ReturnType<typeof fixture>>,store:RunStore,native:boolean,resume=false,decision?:{callId:string;approved:boolean},signal=new AbortController().signal):EngineSession {
 let requests=0
 const common={prompt:'Evaluate the unsaved draft.',testPrompt:'',tools:runtime.tools,callTool:runtime.callTool,hostTools:[],signal,allowConversation:true,critic:'off' as const,context:{currentRequest:{message:'Evaluate the unsaved draft.',input:'draft'},runStore:{store,id:'runtime-action-run',...(resume?{resume:true}:{})},...(decision?{approval:decision}:{})}}
 const run:AgentRun=async(request,c)=>{try{requests++;if(!resume){const tool=request.tools.find(t=>t.description.startsWith('experimental_evaluate:'))!;await c.tool({id:'native-call',name:tool.name,arguments:raw},c.signal);await c.event({type:'message',id:'final',text:'Evaluation completed.',phase:'final'})}else{await c.event({type:'message',id:'final',text:'Evaluation completed.',phase:'final'})}}finally{if(c.providerRelease)expect(c.providerRelease.acknowledge(c.providerRelease.invocation)).toBe(true)}}
 return native?{...common,agent:{model:'fixture',run}}:{...common,model:{family:'openai-compatible',model:'fixture',call:async()=>{requests++;if(requests>1&&!resume){const head=await store.load('runtime-action-run');if(head?.checkpoint&&JSON.parse(new TextDecoder().decode(head.checkpoint)).approval?.state==='asked')throw Error('follow-up sent while held');return answerWith({say:'Evaluation completed.'},'stream',requests-1)}return answerWith(resume?{say:'Evaluation completed.'}:{call:{tool:'experimental_evaluate',arguments:raw}},'stream',requests-1)}}}
}
const state=async(store:RunStore)=>readExecution(JSON.parse(new TextDecoder().decode((await store.load('runtime-action-run'))!.checkpoint!)))!
for(const native of [false,true])describe(`RuntimeAction actual exported ${native?'native':'model'} entry`,()=>{
 it('asks, acknowledges pause, takes a separate person decision, and dispatches one exact call',async()=>{
  const current=owner(),runtime=await fixture({owner:current,current:async()=>current}),store=new MemoryRunStore()
  try{
   const first=await collect(input(runtime,store,native),native)
   expect(first.filter(e=>e.type==='error')).toEqual([])
   const asked=first.find(e=>e.type==='approval_requested'&&'runtime'in e) as Extract<AssistantEvent,{type:'approval_requested';name:string}>
   expect(asked).toMatchObject({name:'experimental_evaluate',args:expected,runtime:{owner:current}})
   expect(first.some(e=>e.type==='tool_call')).toBe(false)
   expect(runtime.calls).toEqual([])
   const saved=await state(store)
   expect(saved.version).toBe(3);expect(saved.status).toBe('paused');expect(saved.approval).toMatchObject({state:'asked',args:expected,runtime:asked.runtime})
   expect(()=>readRunEvent(asked)).not.toThrow()
   const second=await collect(input(runtime,store,native,true,{callId:asked.callId,approved:true}),native)
   expect(second.filter(e=>e.type==='error')).toEqual([])
   expect(runtime.calls).toEqual([{name:'experimental_evaluate',arguments:expected}])
   const done=await state(store)
   expect(done.approval?.state).toBe('answered')
   const report=projectRunReport(done);expect(report.version).toBe(2);expect(report).not.toHaveProperty('usageTotals');expect(()=>readRunReport(report)).not.toThrow()
   expect(done.durable?.dispatches.filter(d=>d.kind==='runtime')).toMatchObject([{read:false,state:'result',request:expect.stringContaining('"rehearsal":true')}])
   await collect(input(runtime,store,native,true),native)
   expect(runtime.calls).toHaveLength(1)
  }finally{await runtime.close()}
 })
 it('refuses changed owner, full list or canonical call on approved resume',async()=>{
  for(const change of ['owner','sibling','args']){
   let current=owner();const runtime=await fixture({owner:current,current:async()=>current}),store=new MemoryRunStore()
   try{
    const first=await collect(input(runtime,store,native),native),asked=first.find(e=>e.type==='approval_requested')!
    expect(asked).toHaveProperty('callId')
    if(change==='owner')current={...current,scopeDigest:'sha256:'+'b'.repeat(64)}
    if(change==='sibling')runtime.setTools([evaluate(),{name:'get_schema',inputSchema:{type:'object'},_meta:{operation:'read',effect:'read'}}])
    // A caller replacement checkpoint is deliberately ignored by the original RunStore.
    const resumed=input(runtime,store,native,true,{callId:(asked as {callId:string}).callId,approved:true})
    if(change==='args')resumed.context!.execution={...(await state(store)),approval:{...(await state(store)).approval!,args:{value:99,rehearsal:true}}}
    const events=await collect(resumed,native)
    expect(runtime.calls).toEqual([]);expect(events.some(e=>e.type==='error'||e.type==='guardrail')).toBe(true)
   }finally{await runtime.close()}
  }
 })
 it('does not expose an actionable ask when the original writer loses acknowledgement',async()=>{
  const current=owner(),runtime=await fixture({owner:current,current:async()=>current}),memory=new MemoryRunStore()
  const store:RunStore={durability:'memory',load:memory.load.bind(memory),readEvents:memory.readEvents.bind(memory),commit:async request=>{
   const receipt=await memory.commit(request),checkpoint=JSON.parse(new TextDecoder().decode(request.checkpoint!))
   if(checkpoint.approval?.state==='asked')throw Error('lost pause acknowledgement')
   return receipt
  }}
  try{const events=await collect(input(runtime,store,native),native);expect(events.some(e=>e.type==='approval_requested')).toBe(false);expect(runtime.calls).toEqual([])}finally{await runtime.close()}
 })
 it('RuntimeAction admitted descriptor must match the actual served declaration',async()=>{
  const current=owner(),runtime=await fixture({owner:current,current:async()=>current}),store=new MemoryRunStore()
  try{const session=input(runtime,store,native);session.tools=[{...session.tools[0]!,description:'A stale declaration'}];const events=await collect(session,native);expect(events.some(e=>e.type==='approval_requested')).toBe(false);expect(runtime.calls).toEqual([])}finally{await runtime.close()}
 })
 it('RuntimeAction final admission check refuses permissions changed during observation',async()=>{
  let attached:import('./context/session').SessionContext|undefined,observations=0;const current=owner()
  const runtime=await fixture({owner:current,current:async()=>{if(++observations===2)attached!.resolvedPolicy['budget.sourceCallsPerTurn']={...attached!.resolvedPolicy['budget.sourceCallsPerTurn']!,value:1};return current},approve:()=> 'allow'})
  try{const session=input(runtime,new MemoryRunStore(),native);session.context!.link={attach:context=>{attached=context}};await collect(session,native);expect(observations).toBeGreaterThanOrEqual(2);expect(runtime.calls).toEqual([])}finally{await runtime.close()}
 })
 it('RuntimeAction final owner check refuses a change after policy preparation',async()=>{
  const initial=owner();let observations=0
  const runtime=await fixture({owner:initial,current:async()=>++observations===1?initial:{...initial,scopeDigest:'sha256:'+'b'.repeat(64)},approve:()=> 'allow'}),store=new MemoryRunStore()
  try{await collect(input(runtime,store,native),native);expect(observations).toBeGreaterThanOrEqual(2);expect(runtime.calls).toEqual([])}finally{await runtime.close()}
 })
 it('refuses absent owner authority, malformed policies and cancellation before transport',async()=>{
  for(const mode of ['missing','refuse','throw','undefined','cancel']){
   const current=owner(),controller=new AbortController();let observations=0
   const runtime=await fixture(mode==='missing'?undefined:{owner:current,current:async()=>{if(++observations===2&&mode==='cancel')controller.abort();return current},approve:()=>{if(mode==='throw')throw Error('policy refused');if(mode==='undefined')return undefined as never;return mode==='cancel'?'allow':'refuse'}})
   try{const events=await collect(input(runtime,new MemoryRunStore(),native,false,undefined,controller.signal),native);expect(runtime.calls,mode).toEqual([]);expect(events.some(e=>e.type==='approval_requested'),mode).toBe(false)}finally{await runtime.close()}
  }
 })
 it('does not let policy mutation retarget the exact canonical call',async()=>{
  const current=owner(),runtime=await fixture({owner:current,current:async()=>current,approve:request=>{request.args.value=99;request.runtime.owner.scopeLabel='Other scope';return 'allow'}})
  try{await collect(input(runtime,new MemoryRunStore(),native),native);expect(runtime.calls).toEqual([{name:'experimental_evaluate',arguments:expected}])}finally{await runtime.close()}
 })
 it('keeps unknown dispatched outcomes from automatic replay',async()=>{
  const current=owner(),runtime=await fixture({owner:current,current:async()=>current}),memory=new MemoryRunStore();let armed=false
  const store:RunStore={durability:'memory',load:memory.load.bind(memory),readEvents:memory.readEvents.bind(memory),commit:async request=>{
   const checkpoint=JSON.parse(new TextDecoder().decode(request.checkpoint!))
   if(armed&&checkpoint.durable?.dispatches.some((d:{kind:string;state:string})=>d.kind==='runtime'&&d.state==='result'))throw Error('result commit interrupted')
   return memory.commit(request)
  }}
  try{
   const first=await collect(input(runtime,store,native),native),asked=first.find(e=>e.type==='approval_requested') as {callId:string}
   armed=true;await collect(input(runtime,store,native,true,{callId:asked.callId,approved:true}),native)
   expect(runtime.calls).toHaveLength(1)
   armed=false;await collect(input(runtime,store,native,true),native)
   expect(runtime.calls).toHaveLength(1)
  }finally{await runtime.close()}
 })
})
describe('RuntimeAction owner boundary',()=>{
 it('reads paired current Runtime records and portable canonical preimages',()=>{
  for(const row of canonicalVectors.rows){const text=canonicalJSON(runtimeJSON(JSON.parse(row.input)));expect(text,row.name).toBe(row.canonical);expect('sha256:'+sha256(text)).toBe(row.digest)}
  expect(canonicalJSON(runtimeJSON({missing:undefined,kept:[undefined,null]}))).toBe('{"kept":[null,null]}')
  for(const cp of pauseVectors.items){expect(readExecution(cp)?.approval?.runtime).toBeDefined();expect(()=>readExecution({...cp,version:2})).toThrow()}
  for(const event of requestVectors.items){expect(()=>readRunEvent(event)).not.toThrow();expect(()=>readRunEvent({...event,version:1})).toThrow()}
  for(const value of [NaN,Infinity,9007199254740992])expect(()=>runtimeJSON({value})).toThrow()
 })
 it('requires complete bounded closed execution labels and pins',()=>{
  expect(validRuntimeOwner(owner())).toBe(true)
  for(const bad of [{...owner(),principalLabel:''},{...owner(),sessionId:'x'.repeat(129)},{...owner(),extra:true},{...owner(),projectLabel:'line\nbreak'}])expect(validRuntimeOwner(bad)).toBe(false)
  expect(validRuntimeBinding({owner:owner(),admissionDigest:pin})).toBe(true)
  expect(validRuntimeBinding({owner:owner(),admissionDigest:pin,approved:true})).toBe(false)
 })
})

it('RuntimeAction exact attempt is single use even after a completed durable result',async()=>{
 const current=owner(),runtime=await fixture({owner:current,current:async()=>current,approve:()=> 'allow'}),store=new MemoryRunStore()
 try{
  const opened=await contextualSession(input(runtime,store,false))
  try{const call=(await opened.context.prepareRuntimeCall('experimental_evaluate',raw,'one-use-call'))!;await call.execute(new AbortController().signal);await expect(({...call}).execute(new AbortController().signal)).rejects.toThrow();expect(runtime.calls).toHaveLength(1)}finally{opened.context.close()}
 }finally{await runtime.close()}
})
