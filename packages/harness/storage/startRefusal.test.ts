// @vitest-environment node
// #293: a refusal at the start, before the run has a root to record against, reaches the host as what it is, through a run
// store as without one. Nothing was sent and nothing is committed.
import {describe,expect,it,vi} from 'vitest'
import type {AssistantEvent,EngineSession,HostTool} from '../engine'
import {vercel} from '../engines/vercel'
import {deepagents} from '../engines/deepagents'
import {MemoryRunStore} from './run'

const LOCAL={name:'write_note',description:'Write one note.',inputSchema:{type:'object'},execute:async()=>({content:[]})}
const REFUSED:[string,HostTool,string][]=[
 ['an act declared with approval none',{...LOCAL,taxonomy:{operation:'act',effect:'act'},approval:'none',source:{kind:'custom',id:'notes',trust:'first-party',version:'1'}} as HostTool,'cannot widen'],
 ['a snapshot on a custom source',{...LOCAL,name:'read_note',taxonomy:{operation:'read',effect:'read'},source:{kind:'custom',id:'notes',trust:'first-party',version:'1',toolListDigest:'sha256:'+'a'.repeat(64),descriptorSnapshot:new Uint8Array([123,125]),currentDescriptorSnapshot:async()=>new Uint8Array([123,125]),descriptorApproval:{}}} as unknown as HostTool,'not an MCP/Gateway snapshot'],
]
const run=async(engine:typeof vercel,tool:HostTool,store:MemoryRunStore|undefined,call:ReturnType<typeof vi.fn>)=>{
 const session={prompt:'Write it.',testPrompt:'',tools:[],callTool:vi.fn(async()=>({content:[]})),hostTools:[tool],signal:new AbortController().signal,allowConversation:true,critic:'off',context:{currentRequest:{message:'Write it.',input:'note'},...(store?{runStore:{store,id:'run-s'}}:{})},model:{family:'openai-compatible',model:'fixture',call}} as unknown as EngineSession
 const out:AssistantEvent[]=[];for await(const event of engine.start(session))out.push(event);return out
}
describe('a refusal at the start, under a run store',()=>{
 for(const [engineName,engine] of [['vercel',vercel],['deepagents',deepagents]] as const)for(const [name,tool,says] of REFUSED)it(`${engineName}: ${name} is reported as configuration, once, and ends`,async()=>{
  const store=new MemoryRunStore(),call=vi.fn()
  const events=await run(engine as typeof vercel,tool,store,call)
  const errors=events.filter(e=>e.type==='error') as Extract<AssistantEvent,{type:'error'}>[]
  expect(errors).toHaveLength(1)
  expect(errors[0]).toMatchObject({failure:{code:'configuration'}})
  expect(JSON.stringify(errors[0])).toContain(says)
  expect(events.at(-1)?.type).toBe('end')
  expect(events.filter(e=>e.type==='end')).toHaveLength(1)
  expect(call).not.toHaveBeenCalled()
  expect(await store.load('run-s')).toBeUndefined()
  // With no store the host hears the same.
  const plain=await run(engine as typeof vercel,tool,undefined,call)
  expect(plain.map(e=>e.type)).toEqual(events.map(e=>e.type))
 })
 it('a run that has a head still fails as a storage stop if it cannot record: only a run with no root passes through',async()=>{
  const store=new MemoryRunStore(),fine=vi.fn()
  await store.commit({runId:'run-s',expectedRevision:null,commitId:'first',checkpoint:new TextEncoder().encode('{"not":"a checkpoint"}'),events:[]})
  const session={prompt:'Write it.',testPrompt:'',tools:[],callTool:vi.fn(async()=>({content:[]})),hostTools:[],signal:new AbortController().signal,allowConversation:true,critic:'off',context:{currentRequest:{message:'Write it.',input:'note'},runStore:{store,id:'run-s',resume:true}},model:{family:'openai-compatible',model:'fixture',call:fine}} as unknown as EngineSession
  const out:AssistantEvent[]=[];for await(const event of vercel.start(session))out.push(event)
  expect(fine).not.toHaveBeenCalled()
  expect(out.some(e=>e.type==='error')).toBe(true);expect(out.at(-1)?.type).toBe('end')
 })
})
