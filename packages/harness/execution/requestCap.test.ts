import {describe,it,expect,vi} from 'vitest'
import {contextualSession} from '../context/session'
import {managedContext,contextOf} from '../context/managed'
import {withDelegation} from '../delegation'
import type {AssistantSession} from '../engine'
import {normalize} from '../thinking'
import {canExtendInterruptedExecution,extendExecution,readExecution,ExecutionPaused,type ExecutionCheckpoint} from './state'
import {RecordsRefused} from '../outputGuard'
import {failureEvent} from '../failures'
import {argumentProblem} from '../sourceArguments'
import schema from '../../../contract/schemas/checkpoint.json'
import eventSchema from '../../../contract/schemas/run-event.json'
import vectors from '../../../contract/vectors/checkpoint/request-cap.json'
import refusals from '../../../contract/vectors/checkpoint/request-cap-refusal.json'

const cappedSession=(requestCap?:number,execution?:ExecutionCheckpoint):AssistantSession=>({prompt:'One admitted operation.',context:{requestCap,execution,currentRequest:{message:'One operation',input:'Trusted operation identity'}},allowConversation:true,interactive:true,critic:'off',testPrompt:'',tools:[],hostTools:[],callTool:vi.fn(async()=>({content:[]})),model:{family:'openai-compatible',model:'fixture',call:vi.fn(async()=>new Response('{}'))},thinking:normalize('off','openai-compatible'),signal:new AbortController().signal})
describe('fixed request caps',()=>{
 it('reads shared legacy/capped vectors and rejects malformed schema members and records',()=>{
  for(const cp of vectors.items)expect(readExecution(cp)).toMatchObject(cp)
  for(const bad of [null,0,-1,21,1.5,'1',true,[],{}]){
   expect(argumentProblem(schema.properties.requestCap,bad)).toBeDefined()
   expect(()=>readExecution({...vectors.items[1],requestCap:bad})).toThrow(/invalid/)
  }
  expect(()=>readExecution({...vectors.items[1],requestCap:21,limit:21})).toThrow(/invalid/)
  for(const patch of [{unit:'provider-managed'},{used:2},{limit:2},{grants:[{from:1,to:2,at:'2026-10-24T00:00:00Z'}]}])expect(()=>readExecution({...vectors.items[1],...patch})).toThrow(/invalid/)
  expect(readExecution({...vectors.items[0],limit:21,grants:[{from:20,to:21,at:'2026-10-24T00:00:00Z'}]})?.limit).toBe(21)
 })
 it.each([1,20])('roundtrips cap%s, preserves hashes and refuses extension or changed identity',async cap=>{
  const input=cappedSession(cap),{context}=await contextualSession(input)
  if(cap===20)context.execution.claim()
  const saved=context.execution.snapshot('stopped')
  expect(saved.requestCap).toBe(cap);expect(readExecution(saved)?.requestCap).toBe(cap)
  const resumed=await contextualSession(cappedSession(cap,saved));expect(resumed.context.execution.snapshot('running').requestCap).toBe(cap);expect(resumed.context.execution.state.used).toBe(saved.used)
  for(const expected of [undefined,cap===1?2:1])await expect(contextualSession(cappedSession(expected,saved))).rejects.toMatchObject({failure:{code:'stale-state',source:'context',outcome:'not-started'}})
  if(cap===20){const omitted=structuredClone(saved);delete omitted.requestCap;await expect(contextualSession(cappedSession(cap,omitted))).rejects.toMatchObject({failure:{code:'stale-state'}})}
  const other=cap===1?2:1;await expect(contextualSession(cappedSession(cap,{...saved,requestCap:other,limit:other}))).rejects.toMatchObject({failure:{code:'stale-state'}})
  expect(()=>extendExecution({...saved,status:'paused'})).toThrow(/cannot receive/)
  const consumed={...saved,used:cap};expect(canExtendInterruptedExecution(consumed)).toBe(false)
  await expect(contextualSession(cappedSession(cap,consumed))).rejects.toBeInstanceOf(ExecutionPaused)
  const legacy=await contextualSession(cappedSession());expect(legacy.context.execution.state.requestKey).toBe(saved.requestKey)
  await expect(contextualSession(cappedSession(cap,legacy.context.execution.snapshot('stopped')))).rejects.toMatchObject({failure:{code:'stale-state'}})
  expect(input.model.call).not.toHaveBeenCalled()
 })
 it('captures host admission before asynchronous setup and blocks mutation widening',async()=>{
  const input=cappedSession(1),pending=contextualSession(input);input.context!.requestCap=20
  const {context}=await pending
  expect(context.execution.state.requestCap).toBe(1)
  for(const patch of [{requestCap:undefined,limit:20},{limit:20},{used:2},{unit:'provider-managed'},{grants:[{from:1,to:2,at:'2026-10-24T00:00:00Z'}]}])expect(()=>context.execution.mutate(patch as never)).toThrow()
  context.execution.claim();await expect(context.execution.initialize()).rejects.toMatchObject({failure:{code:'configuration'}});expect(()=>context.execution.mutate({used:0})).toThrow();expect(()=>context.execution.claim()).toThrow(ExecutionPaused)
 })
 it('keeps invalid and native input failures typed and schema-valid',async()=>{
  for(const cap of [null,0,-1,21,1.5,Infinity,NaN,'1',{},[]]){
   try{await contextualSession(cappedSession(cap as number));throw Error('admitted invalid cap')}
   catch(error){const event=failureEvent(error,'context');expect(event).toEqual(refusals.items[0]);expect(argumentProblem(eventSchema.$defs.error,event)).toBeUndefined()}
  }
  const base=cappedSession(1),{model,thinking,...rest}=base
  await expect(contextualSession({...rest,agent:{family:'codex',model:'fixture',run:vi.fn()}} as never)).rejects.toMatchObject({failure:{code:'configuration'}})
  const native=await contextualSession({...rest,context:{...rest.context,requestCap:undefined},agent:{family:'codex',model:'fixture',run:vi.fn()}} as never)
  expect(native.context.execution.snapshot('stopped')).toMatchObject({unit:'provider-managed',used:0,limit:1});expect(native.context.execution.state.requestCap).toBeUndefined()
 })
 it('accepts only identical fresh helper repeats and stays closed when a refusal is caught',async()=>{
  const fresh=await contextualSession(cappedSession());fresh.context.execution.limitTo(1);fresh.context.execution.limitTo(1);expect(readExecution(fresh.context.execution.snapshot('stopped'))?.requestCap).toBe(1)
  for(const kind of ['zero','fraction','negative','oversized','nonfinite','different','tighten','postclaim','granted','restored','native']){
   let input=cappedSession()
   if(kind==='restored')input=cappedSession(1,fresh.context.execution.snapshot('stopped'))
   if(kind==='native'){const {model,thinking,...rest}=input;input={...rest,agent:{family:'codex',model:'fixture',run:vi.fn()}} as never}
   const {context}=await contextualSession(input),c=context.execution
   if(kind==='different')c.limitTo(1)
   if(kind==='tighten')c.limitTo(20)
   if(kind==='postclaim')c.claim()
   if(kind==='granted')c.mutate({limit:30,grants:[{from:20,to:30,at:'2026-10-24T00:00:00Z'}]})
   expect(()=>c.limitTo(kind==='zero'?0:kind==='fraction'?1.5:kind==='negative'?-1:kind==='oversized'?21:kind==='nonfinite'?NaN:kind==='different'?2:1)).toThrow()
   expect(()=>c.claim()).toThrow();expect(()=>c.snapshot('running')).toThrow();expect(()=>c.limitTo(1)).toThrow()
  }
 fresh.context.execution.claim();await expect(fresh.context.execution.initialize()).rejects.toMatchObject({failure:{code:'configuration'}})
 })
 it('refuses capped handoff when the durable claim cannot be projected, preserving legacy behavior',async()=>{
  for(const cap of [1,undefined]){
   const input=cappedSession(cap);input.context!.checkpoint=vi.fn(async()=>{})
   const opened=await contextualSession(input)
   vi.spyOn(opened.context.execution,'snapshot').mockImplementation(()=>{throw new RecordsRefused('fixture')})
   const request=opened.session.values.model.call('chat/completions',{body:'{}'})
   if(cap===undefined){await request;expect(input.model.call).toHaveBeenCalledTimes(1)}
   else{await expect(request).rejects.toMatchObject({failure:{code:'storage',source:'storage',outcome:'not-started'}});expect(input.model.call).not.toHaveBeenCalled()}
  }
 })
 it('persists the claim before handoff and never replays it after interruption',async()=>{
  const input=cappedSession(1),saved:ExecutionCheckpoint[]=[];input.context!.checkpoint=async cp=>{saved.push(cp)}
  const opened=await contextualSession(input)
  await opened.session.values.model.call('chat/completions',{method:'POST',headers:{'content-type':'application/json'},body:'{}'})
  const cp=saved.at(-1)!;expect(cp).toMatchObject({used:1,limit:1,requestCap:1});expect(cp.prompts).toHaveLength(1)
  expect(readExecution(cp)?.requestCap).toBe(1)
  await expect(contextualSession(cappedSession(1,{...cp,status:'stopped'}))).rejects.toBeInstanceOf(ExecutionPaused)
  await expect(opened.session.values.model.call('chat/completions',{body:'{}'})).rejects.toBeInstanceOf(ExecutionPaused)
  expect(input.model.call).toHaveBeenCalledTimes(1)
 })
})

for(const delegated of [false,true])it(`captures host cap when the public ${delegated?'delegation':'managed'} iterator is created`,async()=>{
 const input=cappedSession(1),captured:number[]=[]
 const engine={id:'vercel' as const,start:(given:AssistantSession)=>managedContext(given,async function*(ready){captured.push(contextOf(ready).execution.state.requestCap!);yield {type:'end'}})}
 const output=delegated?withDelegation(engine).start(input):engine.start(input)
 input.context!.requestCap=20
 const events=[];for await(const event of output)events.push(event)
 expect(events.filter(event=>event.type==='error')).toEqual([]);expect(captured).toEqual([1])
})
