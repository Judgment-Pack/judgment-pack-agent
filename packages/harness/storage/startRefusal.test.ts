// @vitest-environment node
// #293, #297: a refusal before the writer is bound, on a fresh run or at a resume with a head, reaches the host as what it is,
// through a run store as without one. Nothing is sent, and the head's bytes do not change; under a stored chat the resume's claim
// still moves the run's revision and the chat's context with the same bytes, and the run stays resumable.
import {describe,expect,it,vi} from 'vitest'
import type {AssistantEvent,EngineSession,HostTool} from '../engine'
import {vercel} from '../engines/vercel'
import {deepagents} from '../engines/deepagents'
import {codex} from '../engines/codex'
import {answerWith} from '../testing/scenarioDriver'
import {RECORD_DEFAULTS} from '../records'
import {MemoryRunStore} from './run'
import {ContinuationNotRecoverable,RunWriter,type DurableBinding} from './writer'

const LOCAL={name:'write_note',description:'Write one note.',inputSchema:{type:'object'},execute:async()=>({content:[]})}
const REFUSED:[string,Record<string,unknown>,string,string[]][]=[
 ['an act declared with approval none',{hostTools:[{...LOCAL,taxonomy:{operation:'act',effect:'act'},approval:'none',source:{kind:'custom',id:'notes',trust:'first-party',version:'1'}} as unknown as HostTool]},'cannot widen',['error']],
 ['a snapshot on a custom source',{hostTools:[{...LOCAL,name:'read_note',taxonomy:{operation:'read',effect:'read'},source:{kind:'custom',id:'notes',trust:'first-party',version:'1',toolListDigest:'sha256:'+'a'.repeat(64),descriptorSnapshot:new Uint8Array([123,125]),currentDescriptorSnapshot:async()=>new Uint8Array([123,125]),descriptorApproval:{}}} as unknown as HostTool]},'not an MCP/Gateway snapshot',['error']],
 ['a policy refusal',{policy:{models:{author:{reasoning:'none',discovered:{reasoning:{default:'high',supported:['low','medium','high']}}}}}},'policy refused',['guardrail','error']],
 ['a policy with a member this package cannot read',{policy:{notAMember:true}},'record member unsupported',['guardrail','error']],
]
const ENGINES=[['vercel',vercel],['deepagents',deepagents],['codex',codex]] as const
const named=(name:string)=>ENGINES.find(([held])=>held===name)![1] as typeof vercel
const engineCall=(engine:string,call:ReturnType<typeof vi.fn>)=>engine==='codex'?{agent:{model:'fixture',run:call}}:{model:{family:'openai-compatible',model:'fixture',call}}
const run=async(engine:string,extra:Record<string,unknown>,store:MemoryRunStore|undefined,call:ReturnType<typeof vi.fn>,resume=false)=>{
 const session={prompt:'Write it.',testPrompt:'',tools:[],callTool:vi.fn(async()=>({content:[]})),hostTools:[],signal:new AbortController().signal,allowConversation:true,critic:'off',context:{currentRequest:{message:'Write it.',input:'note'},...(store?{runStore:{store,id:'run-s',...(resume?{resume:true}:{})}}:{})},...engineCall(engine,call),...extra} as unknown as EngineSession
 const out:AssistantEvent[]=[];for await(const event of named(engine).start(session))out.push(event);return out
}
/** A real head: an ordinary run of that engine, finished and recorded. */
async function withHead(engine:string){
 const store=new MemoryRunStore()
 const fine=engine==='codex'
  ?vi.fn(async(_request:unknown,callbacks:{event:(event:unknown)=>Promise<void>;providerRelease?:{acknowledge:(invocation:unknown)=>void;invocation:unknown}})=>{try{await callbacks.event({type:'message',id:'reply',text:'Done.',phase:'final'})}finally{callbacks.providerRelease?.acknowledge(callbacks.providerRelease.invocation)}})
  :vi.fn(async()=>answerWith({say:'Done.'},'stream',0))
 // A head is engine-neutral: deepagents' own scripted endpoint is another protocol, and the refusal comes before any model.
 const events=await run(engine==='deepagents'?'vercel':engine,{},store,fine)
 expect(events.filter(e=>e.type==='error')).toEqual([])
 const head=await store.load('run-s')
 expect(head?.checkpoint).toBeDefined()
 return {store,revision:head!.revision}
}
const heard=(events:AssistantEvent[],says:string)=>{
 const errors=events.filter(e=>e.type==='error') as Extract<AssistantEvent,{type:'error'}>[]
 expect(errors).toHaveLength(1)
 expect(errors[0]).toMatchObject({failure:{code:'configuration'}})
 expect(JSON.stringify(events)).toContain(says)
 expect(events.at(-1)?.type).toBe('end')
 expect(events.filter(e=>e.type==='end')).toHaveLength(1)
}
describe('a refusal before the writer is bound, under a run store',()=>{
 for(const [engine] of ENGINES)for(const [name,extra,says,first] of REFUSED){
  it(`${engine}, a fresh run: ${name} is reported as configuration, once, and ends`,async()=>{
   const store=new MemoryRunStore(),call=vi.fn()
   const events=await run(engine,extra,store,call)
   heard(events,says)
   expect(events.map(e=>e.type).filter(type=>first.includes(type))).toEqual(first)
   expect(call).not.toHaveBeenCalled()
   expect(await store.load('run-s')).toBeUndefined()
   // With no store the host hears the same.
   expect((await run(engine,extra,undefined,call)).map(e=>e.type)).toEqual(events.map(e=>e.type))
  })
  it(`${engine}, a resume with a head: ${name} is reported as configuration, once, and the head does not move`,async()=>{
   const {store,revision}=await withHead(engine),call=vi.fn()
   const events=await run(engine,extra,store,call,true)
   heard(events,says)
   expect(events.map(e=>e.type).filter(type=>first.includes(type))).toEqual(first)
   expect(call).not.toHaveBeenCalled()
   expect((await store.load('run-s'))!.revision).toBe(revision)
  })
 }
 it('a head it cannot read stops before the writer, and still sends nothing',async()=>{
  const store=new MemoryRunStore(),fine=vi.fn()
  await store.commit({runId:'run-s',expectedRevision:null,commitId:'first',checkpoint:new TextEncoder().encode('{"not":"a checkpoint"}'),events:[]})
  const out=await run('vercel',{},store,fine,true)
  expect(fine).not.toHaveBeenCalled()
  expect(out.some(e=>e.type==='error')).toBe(true);expect(out.at(-1)?.type).toBe('end')
 })
})

describe("the writer's pass-through, at its own port (#297)",()=>{
 const scope={execution:'run-w',role:'author'}
 const checkpointOf=()=>({version:2,id:'run-w',requestKey:'a'.repeat(64),phase:'work',status:'running',unit:'model-requests',used:0,limit:20,grants:[],plan:[],explanation:'',operations:[],context:{scope:'fixture',entries:[]}}) as never
 const opened=()=>{const store=new MemoryRunStore(),writer=new RunWriter(store,'run-w',undefined),root:DurableBinding={writer,root:true,scope};return {store,writer,root}}
 const LISTED:AssistantEvent[]=[{type:'error',message:'Refused.'},{type:'usage',usage:{rows:[]}},{type:'end'},{type:'guardrail',tool:'models.author.reasoning',action:'refused',reason:'policy refused',detail:'Refused.'}] as AssistantEvent[]
 it.each(LISTED.map(event=>[event.type,event] as const))('before the bind, %s reaches the host unrecorded',async(_type,event)=>{
  const {store,writer,root}=opened()
  await expect(writer.event(root,event)).resolves.toMatchObject({type:event.type})
  expect(await store.load('run-w')).toBeUndefined()
 })
 it('before the bind, nothing else does: it has no root to record against',async()=>{
  const {store,writer,root}=opened()
  await expect(writer.event(root,{type:'message',text:'.'} as AssistantEvent)).rejects.toBeInstanceOf(ContinuationNotRecoverable)
  expect(await store.load('run-w')).toBeUndefined()
 })
 it('before the bind, a child scope gets no pass-through',async()=>{
  const {writer}=opened()
  await expect(writer.event({writer,root:false,scope:{execution:'child',role:'author',task:'child'}},{type:'error',message:'Refused.'} as AssistantEvent)).rejects.toBeInstanceOf(ContinuationNotRecoverable)
 })
 // #303 (from #301's review): the pass-through keys on the bind's policy, not on the root. A coordinator hands the root back
 // (useAcknowledgedRoot) and goes on: its last usage and end are recorded, never passed through, though the writer has no root.
 it.each([['usage',{type:'usage',usage:{rows:[]}}],['end',{type:'end'}]] as const)('after a coordinator handed the root back, its %s is still recorded',async(type,event)=>{
  const {store,writer,root}=opened()
  writer.bind(checkpointOf,RECORD_DEFAULTS,()=>undefined)
  await writer.event(root,{type:'message',text:'Working.'} as AssistantEvent)
  writer.useAcknowledgedRoot(root)
  await writer.event(root,event as AssistantEvent)
  const stored=(await store.readEvents('run-w',null,10)).items.map(item=>new TextDecoder().decode(item.bytes))
  expect(stored.join('\n')).toContain(`"type":"${type}"`)
 })
 it.each(LISTED.map(event=>[event.type,event] as const))('from the bind, before the first commit, %s is recorded',async(_type,event)=>{
  const {store,writer,root}=opened()
  writer.bind(checkpointOf,RECORD_DEFAULTS,()=>undefined)
  await writer.event(root,event)
  const head=await store.load('run-w')
  expect(head?.checkpoint).toBeDefined()
  const stored=(await store.readEvents('run-w',null,10)).items.map(item=>new TextDecoder().decode(item.bytes))
  expect(stored.join('\n')).toContain(`"type":"${event.type}"`)
 })
})
