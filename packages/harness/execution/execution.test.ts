import {describe,it,expect,vi} from 'vitest'
import {contextualSession} from '../context/session'
import {normalize} from '../thinking'
import type {AssistantSession} from '../engine'
import {extendExecution,readExecution,ExecutionPaused} from './state'
import usageCheckpoint from '../../../contract/vectors/failover/usage-checkpoint.json'
const base=():AssistantSession=>({prompt:'Fix the missing-size exception and verify unchanged expectations',context:{currentRequest:{message:'Fix it',input:'current pack and test facts'}},allowConversation:true,interactive:true,testPrompt:'',tools:[],hostTools:[],callTool:vi.fn(async()=>({content:[]})),model:{family:'gemini',model:'fixture',call:vi.fn(async()=>new Response('{}'))},thinking:normalize('off','gemini'),signal:new AbortController().signal})
describe('shared execution continuity',()=>{
 it('restores every usage ledger row from an interrupted run',()=>{
  const saved=usageCheckpoint.items[0]
  expect(readExecution(JSON.parse(JSON.stringify(saved)))?.usage).toEqual(saved.usage)
 })
 it('refuses malformed switch and child usage checkpoint members',()=>{
  for(const change of [
   (s:any)=>{s.failover[0].consumed=2},
   (s:any)=>{s.failover[0].history[0].reason='permission'},
   (s:any)=>{delete s.failover[0].connection},
   (s:any)=>{s.childUsage[0].id=''},
   (s:any)=>{s.usage.rows[0].model='x'.repeat(201)}
  ]){const saved=structuredClone(usageCheckpoint.items[0]);change(saved);expect(()=>readExecution(saved)).toThrow(/invalid/)}
 })
 it('child usage survives resume and repeated snapshots without double counting',async()=>{
  const input=base(),ready=await contextualSession(input)
  const usage=usageCheckpoint.items[0]!.childUsage[0]!.usage
  ready.context.execution.mergeChildUsage('child-run',usage,'child.analyze')
  ready.context.execution.mergeChildUsage('child-run',usage,'child.analyze')
  const saved=ready.context.execution.snapshot('stopped')
  const resumed=await contextualSession({...input,context:{...input.context,execution:saved}})
  resumed.context.execution.mergeChildUsage('child-run',usage,'child.analyze')
  expect(resumed.context.execution.usage().rows.filter(r=>r.role==='child.analyze')).toEqual(usage.rows)
  expect(resumed.context.execution.snapshot().childUsage).toEqual(saved.childUsage)
 })
 it('attributes source deltas to the connection and role active when they ran',async()=>{
  const ready=await contextualSession(base()),execution=ready.context.execution
  execution.state.sources=[{source:'runtime',kind:'runtime',calls:1,refused:0,proposed:0,bytes:30,milliseconds:4}]
  execution.useConnection('backup','critic')
  execution.state.sources[0]={...execution.state.sources[0]!,calls:2,bytes:50,milliseconds:7}
  const rows=execution.usage().rows.filter(row=>row.source==='runtime')
  expect(rows).toEqual([
   {source:'runtime',connection:'fixture',role:'author',requests:0,invocations:1,milliseconds:4,bytes:30},
   {source:'runtime',connection:'backup',role:'critic',requests:0,invocations:1,milliseconds:3,bytes:20}
  ])
 })
 it('refuses unapproved, changed-phase and changed-request continuation',async()=>{
  const input=base(),ready=await contextualSession(input)
  const saved=ready.context.execution.snapshot('paused')
  await expect(contextualSession({...input,context:{...input.context,execution:saved}})).rejects.toBeInstanceOf(ExecutionPaused)
  await expect(contextualSession({...input,context:{...input.context,phase:'plan',execution:extendExecution(saved)}})).rejects.toThrow(/phase changed/)
  await expect(contextualSession({...input,context:{currentRequest:{message:'Different',input:'different'},execution:extendExecution(saved)}})).rejects.toThrow(/request.*changed/)
  expect(input.model.call).not.toHaveBeenCalled()
 })
 it('detects modified retained values before dispatch and rejects malformed grants',async()=>{
  const input=base(),ready=await contextualSession(input)
  await ready.context.ledger.retain({id:'source',kind:'tool',label:'Source',value:{flag:false}})
  const saved=extendExecution(ready.context.execution.snapshot('paused'))
  saved.context.entries[0]!.value={flag:true}
  await expect(contextualSession({...input,context:{...input.context,execution:saved}})).rejects.toThrow(/digest/)
  expect(()=>readExecution({...saved,limit:100})).toThrow(/invalid/)
  expect(input.model.call).not.toHaveBeenCalled()
 })
 it('caches unchanged session contracts but never workflow operations or failed reads',async()=>{
  const input=base();input.tools=[{name:'get_schema'}]
  input.callTool=vi.fn(async(name)=>({content:[{type:'text',text:name}]}))
  const {session}=await contextualSession(input)
  await session.callTool('get_schema',{});await session.callTool('get_schema',{})
  await session.callTool('save_pack',{id:'one'});await session.callTool('save_pack',{id:'one'})
  expect(input.callTool).toHaveBeenCalledTimes(3)
 })
 it('does not let progress updates grant work permissions or extra requests',async()=>{
  const input=base();input.context={...input.context,phase:'plan'}
  const {session,context}=await contextualSession(input)
  const tool=session.hostTools.find(t=>t.name==='update_plan')!
  await tool.execute({explanation:'Prepare review',plan:[{step:'Inspect',status:'completed'}],limit:999},input.signal)
  expect(context.execution.state.limit).toBe(20)
  await expect(session.callTool('save_pack',{})).rejects.toThrow(/Plan mode/)
  await expect(tool.execute({explanation:'',plan:[{step:'a',status:'in_progress'},{step:'b',status:'in_progress'}]},input.signal)).rejects.toThrow(/at most one/)
 })
})

it('requests an extension only near the ceiling and records a concrete reason without granting it',async()=>{
 const input=base(),{session,context}=await contextualSession(input)
 const request=session.hostTools.find(t=>t.name==='request_budget_increase')!
 const args={reason:'The proposed guard needs two boundary checks.',nextStep:'Evaluate both boundaries with unchanged expectations.'}
 expect((await request.execute(args,input.signal)).structuredContent).toMatchObject({status:'within-allowance'})
 expect(context.execution.pauseRequested).toBe(false)
 context.execution.state.used=18
 expect((await request.execute(args,input.signal)).structuredContent).toMatchObject({status:'approval-required'})
 expect(context.execution.state).toMatchObject({used:18,limit:20,grants:[]})
 expect(context.execution.pauseRequested).toBe(true)
 expect(context.execution.state.reason).toContain('two boundary checks')
})

it('persists request usage before dispatch and sends nothing when that checkpoint fails',async()=>{
 const input=base(),order:string[]=[]
 input.context={...input.context,checkpoint:async state=>{order.push('saved-'+state.used)}}
 input.model.call=vi.fn(async()=>{order.push('sent');return new Response('{}')})
 const ready=await contextualSession(input)
 await ready.session.model.call('generate',{body:'{}'})
 expect(order).toEqual(['saved-0','saved-1','sent'])
 const unavailable=base();unavailable.context={...unavailable.context,checkpoint:async()=>{throw Error('Storage unavailable')}}
 await expect(contextualSession(unavailable)).rejects.toMatchObject({failure:{code:'storage',outcome:'not-started'}})
 expect(unavailable.model.call).not.toHaveBeenCalled()
})

it('uses declared immutable read semantics for future tools and never caches writes or mutable references',async()=>{
 const input=base(),immutable=vi.fn(async()=>({content:[{type:'text',text:'stable'}]})),mutable=vi.fn(async()=>({content:[{type:'text',text:'current'}]})),write=vi.fn(async()=>({content:[]}))
 input.hostTools=[
  {name:'future_contract',effect:'read',contextPolicy:{immutable:true,reference:true},description:'Contract',inputSchema:{},execute:immutable},
  {name:'future_current_reference',effect:'read',contextPolicy:{reference:true},description:'Current reference',inputSchema:{},execute:mutable},
  {name:'future_write',effect:'workflow',delegation:'read',contextPolicy:{immutable:true},description:'Write',inputSchema:{},execute:write}
 ]
 const {session}=await contextualSession(input)
 for(const name of ['future_contract','future_current_reference','future_write']){
  const tool=session.hostTools.find(t=>t.name===name)!
  await tool.execute({},input.signal);await tool.execute({},input.signal)
 }
 expect(immutable).toHaveBeenCalledTimes(1);expect(mutable).toHaveBeenCalledTimes(2);expect(write).toHaveBeenCalledTimes(2)
 // Changing arguments still performs a distinct read; failures never poison cache.
 await session.hostTools.find(t=>t.name==='future_contract')!.execute({revision:'next'},input.signal)
 expect(immutable).toHaveBeenCalledTimes(2)
})

it('requires an explicit interrupted grant for an exhausted failed request and preserves completed operations',async()=>{
 const ready=await contextualSession(base()),task=ready.context.execution
 task.state.used=20
 task.observe({type:'tool_call',name:'read_contract',args:{revision:'retained'}})
 task.observe({type:'tool_result',name:'read_contract',isError:false,text:'Already read'})
 const stopped=task.snapshot('stopped')
 expect(()=>extendExecution(stopped)).toThrow(/cannot receive/)
 const extended=extendExecution(stopped,true)
 expect(extended).toMatchObject({used:20,limit:30,status:'running',operations:stopped.operations,grants:[{from:20,to:30}]})
 expect(stopped).toMatchObject({used:20,limit:20,status:'stopped',grants:[]})
 expect(()=>extendExecution({...stopped,status:'complete'},true)).toThrow(/cannot receive/)
 expect(()=>extendExecution({...stopped,used:19},true)).toThrow(/cannot receive/)
})
