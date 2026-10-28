import {describe,it,expect,vi} from 'vitest'
import type {AssistantSession,AssistantEvent} from '../engine'
import {contextualSession} from '../context/session'
import {managedContext} from '../context/managed'
import {canonicalJSON} from '../context/chat'
import {MemoryRunStore,type RunStore} from './run'
import {HostOutcomeUnknown,readStoredRunEvent,durableSession,durableOwner,scopeFor,ContinuationNotRecoverable,ContinuationIdentityChanged,StorageAcknowledgementInvalid} from './writer'
import vectors from '../../../contract/vectors/storage/checkpoints.json'
import {readExecution} from '../execution/state'
import {checkRecord,writeRunEvent} from '../recordReader'
import {failureEvent} from '../failures'
import {withDelegation} from '../delegation'
const signal=()=>new AbortController().signal
const session=(store:RunStore,id:string,execute:AssistantSession['hostTools'][number]['execute'],resume=false):AssistantSession=>({prompt:'Work',testPrompt:'',tools:[],hostTools:[{name:'save_object',taxonomy:{operation:'own-state',effect:'pure'},effect:'workflow',description:'Save',inputSchema:{},execute,artifacts:()=>[{kind:'artifact',id:'opaque-artifact',digest:'sha256:'+'a'.repeat(64)}]}],callTool:vi.fn(async()=>({content:[]})),model:{family:'openai-compatible',model:'fixture',call:vi.fn(async()=>new Response('done'))},signal:signal(),context:{currentRequest:{message:'Work',input:'Work'},runStore:{store,id,...(resume?{resume:true}:{})}}})
async function run(store:RunStore,id:string,execute:AssistantSession['hostTools'][number]['execute'],resume=false){
 const events:AssistantEvent[]=[]
 for await(const event of managedContext(session(store,id,execute,resume),async function*(prepared){
  yield {type:'tool_call',callId:resume?'new-wire':'wire',name:'save_object',args:{value:1}}
  const result=await prepared.values.hostTools.find(t=>t.name==='save_object')!.execute({value:1},prepared.signal,'wire')
  yield {type:'tool_result',callId:resume?'new-wire':'wire',name:'save_object',isError:!!result.isError,text:result.content?.[0]?.text??''}
  yield {type:'end'}
 }))events.push(event)
 return events
}
describe('root RunWriter integration',()=>{
 it('commits real results and scoped artifact provenance, then prevents recovered host replay',async()=>{
  const store=new MemoryRunStore(),execute=vi.fn(async()=>({content:[{type:'text',text:'saved'}],opaque:{nested:[null,0]}}))
  const events=await run(store,'trusted-execution',execute)
  expect(events.some(e=>e.type==='error')).toBe(false);expect(execute).toHaveBeenCalledOnce()
  const head=(await store.load('trusted-execution'))!,saved=JSON.parse(new TextDecoder().decode(head.checkpoint!))
  expect(saved.status).toBe('complete');expect(saved.durable.observations.map((o:{kind:string})=>o.kind)).toEqual(['operation','artifact-produced'])
  expect(saved.durable.observations.every((o:{scope:unknown;commitId:string})=>canonicalJSON(o.scope)===canonicalJSON({execution:'trusted-execution',role:'author'})&&!!o.commitId)).toBe(true)
  await run(store,'trusted-execution',execute,true);expect(execute).toHaveBeenCalledOnce()
  await run(store,'new-host-authorized-run',execute);expect(execute).toHaveBeenCalledTimes(2)
  const page=await store.readEvents('trusted-execution',null,100)
  expect(page.items.every(item=>readStoredRunEvent(item.bytes).scope.execution==='trusted-execution')).toBe(true)
 })
 it('keeps an uncertain host outcome unknown and never runs it again',async()=>{
  const store=new MemoryRunStore(),execute=vi.fn(async()=>{throw new Error('uncertain')})
  const first=await contextualSession(session(store,'unknown-execution',execute)),tool=first.session.values.hostTools.find(t=>t.name==='save_object')!
  await expect(tool.execute({value:1},signal(),'wire')).rejects.toThrow();first.context.close()
  const next=await contextualSession(session(store,'unknown-execution',execute,true))
  await expect(next.session.values.hostTools.find(t=>t.name==='save_object')!.execute({value:1},signal(),'new-wire')).rejects.toBeInstanceOf(HostOutcomeUnknown)
  expect(execute).toHaveBeenCalledOnce();next.context.close()
 })
 it('interrupts before and after every commit and resumes only from the actual store',async()=>{
  let commits=0
  const base=new MemoryRunStore(),counting:RunStore={durability:'memory',load:base.load.bind(base),readEvents:base.readEvents.bind(base),commit:input=>{commits++;return base.commit(input)}}
  await run(counting,'enumerate',async()=>({content:[{type:'text',text:'saved'}]}))
  expect(commits).toBeGreaterThan(4)
  for(const phase of ['before','after'])for(let at=1;at<=commits;at++){
   const memory=new MemoryRunStore();let ordinal=0,armed=true
   const fault:RunStore={durability:'memory',load:memory.load.bind(memory),readEvents:memory.readEvents.bind(memory),commit:async input=>{
    const stop=armed&&++ordinal===at
    if(stop&&phase==='before'){armed=false;throw new Error('injected precommit')}
    const receipt=await memory.commit(input)
    if(stop&&phase==='after'){armed=false;throw new Error('lost acknowledgement')}
    return receipt
   }}
   const execute=vi.fn(async()=>({content:[{type:'text',text:'saved'}]}))
   await run(fault,'interrupted',execute)
   const head=await fault.load('interrupted');armed=false
   await run(fault,'interrupted',execute,!!head)
   expect(execute.mock.calls.length,`${phase} commit ${at}`).toBeLessThanOrEqual(1)
  }
 })
})

// Consumer cancellation must reach the gate before async-generator return can queue behind next.
for(const coordinator of [false,true])it(`cancels pending stream next while storage acknowledgement is unresolved (${coordinator?'coordinator':'managed'})`,async()=>{
 const memory=new MemoryRunStore();let release!:()=>void,entered!:()=>void
 const waiting=new Promise<void>(resolve=>entered=resolve),blocked=new Promise<void>(resolve=>release=resolve)
 const store:RunStore={durability:'memory',load:memory.load.bind(memory),readEvents:memory.readEvents.bind(memory),commit:async input=>{const receipt=await memory.commit(input);entered();await blocked;return receipt}}
 const execute=vi.fn(async()=>({content:[]})),input=session(store,'cancel-pending',execute)
 const underlying={id:'vercel' as const,start:(given:AssistantSession)=>managedContext(given,async function*(prepared){await prepared.values.hostTools[0]!.execute({},prepared.signal);yield {type:'end'}})}
 const events=(coordinator?withDelegation(underlying).start(input):underlying.start(input))[Symbol.asyncIterator]()
 const pending=events.next();await waiting
 const returned=events.return!()
 await expect(Promise.race([returned,new Promise((_,reject)=>setTimeout(()=>reject(Error('return remained queued')),100))])).resolves.toMatchObject({done:true})
 await expect(pending).resolves.toMatchObject({done:true})
 release();await new Promise(resolve=>setTimeout(resolve,0))
 expect(execute).not.toHaveBeenCalled()
 expect(await memory.load('cancel-pending')).not.toBeNull()
})

describe('storage recovery records and exact prepared requests',()=>{
 it('reads every shared current storage vector and bounds structural recursion without inspecting opaque payloads',()=>{
  for(const item of vectors.items)expect(readExecution(item)?.durable).toBeDefined()
  let nested:any=structuredClone(vectors.items[0]);delete nested.handoff
  for(let i=0;i<6;i++)nested={...structuredClone(vectors.items[0]),durable:{dispatches:[],observations:[],children:[{scope:{execution:nested.id,role:'author'},checkpoint:nested}]}}
  expect(()=>checkRecord('checkpoint',nested)).toThrow()
  expect(()=>checkRecord('event',writeRunEvent(failureEvent(new ContinuationNotRecoverable(),'storage')))).not.toThrow()
 })
 it('scopes recovered results by parent, role and sibling and admits explicit intentional repeats',async()=>{
  const store=new MemoryRunStore(),execute=vi.fn(async()=>({content:[{type:'text',text:'saved'}]}))
  const input=await durableSession(session(store,'isolation',execute)),first=await contextualSession(input),binding=input.context![durableOwner]!
  await binding.writer.invoke(binding,'host','source','write','{}',false,async()=>({value:'parent'}));first.context.close()
  const restored=await durableSession(session(store,'isolation',execute,true)),resumed=await contextualSession(restored),root=restored.context![durableOwner]!
  for(const scope of [{execution:'sibling-a',role:'author',task:'sibling-a'},{execution:'sibling-b',role:'author',task:'sibling-b'},{execution:'isolation',role:'critic'}]){
   const local={...root,scope,root:false},call=vi.fn(async()=>({value:scope.execution+scope.role}))
   expect(await root.writer.invoke(local,'host','source','write','{}',false,call)).toEqual({value:scope.execution+scope.role});expect(call).toHaveBeenCalledOnce()
  }
  const call=vi.fn(async()=>({value:'bad'}));expect(await root.writer.invoke(root,'host','source','write','{}',false,call)).toEqual({value:'parent'});expect(call).not.toHaveBeenCalled()
  resumed.context.close()
 })
 it('retains exact safe prepared bytes and refuses changed source identity or private reasoning before dispatch',async()=>{
  const store=new MemoryRunStore(),input=await durableSession(session(store,'prepared',vi.fn())),opened=await contextualSession(input),binding=input.context![durableOwner]!
  const exact=String.raw`{ "suffix": "/call", "method":"POST", "body":"{ \"value\": 1 }" }`
  await expect(binding.writer.invoke(binding,'model','source','fixture',exact,true,async()=>{throw Error('interrupted')})).rejects.toThrow();opened.context.close()
  const restored=await durableSession(session(store,'prepared',vi.fn(),true)),resumed=await contextualSession(restored),held=restored.context![durableOwner]!
  expect(held.writer.prepared(held,'model','source','fixture','{}')).toBe(exact)
  expect(()=>held.writer.prepared(held,'model','changed-source','fixture','{}')).toThrow(ContinuationIdentityChanged)
  const call=vi.fn();await expect(held.writer.invoke(scopeFor(held,'critic'),'model','source','fixture',JSON.stringify({body:JSON.stringify({type:'reasoning',text:'private'})}),true,call)).rejects.toThrow(ContinuationNotRecoverable);expect(call).not.toHaveBeenCalled();resumed.context.close()
 })
})

it('waits for the exact claim receipt, rejects malformed acknowledgement and rechecks current authority',async()=>{
 for(const malformed of [false,true]){
  const memory=new MemoryRunStore();let entered!:()=>void,release!:()=>void,armed=false
  const waiting=new Promise<void>(done=>entered=done),blocked=new Promise<void>(done=>release=done)
  const store:RunStore={durability:'host',load:memory.load.bind(memory),readEvents:memory.readEvents.bind(memory),commit:async input=>{const receipt=await memory.commit(input);if(armed){entered();await blocked;return malformed?{...receipt,commitId:'wrong-transaction'}:receipt};return receipt}}
  const execute=vi.fn(async()=>({content:[]})),input=await durableSession(session(store,'receipt-'+malformed,execute)),opened=await contextualSession(input)
  armed=true
  const call=opened.session.values.hostTools.find(t=>t.name==='save_object')!.execute({},input.signal,'wire')
  await waiting;expect(execute).not.toHaveBeenCalled();release()
  if(malformed)await expect(call).rejects.toBeInstanceOf(StorageAcknowledgementInvalid);else await call
  expect(execute.mock.calls.length).toBe(malformed?0:1);opened.context.close()
 }
 const memory=new MemoryRunStore(),execute=vi.fn(async()=>({content:[]}));let allowed=true
 const input=session(memory,'lease-loss',execute);input.context!.authorize=async()=>{if(!allowed)throw Error('lease lost')}
 const opened=await contextualSession(input);allowed=false
 await expect(opened.session.values.hostTools.find(t=>t.name==='save_object')!.execute({},input.signal)).rejects.toThrow();expect(execute).not.toHaveBeenCalled();opened.context.close()
})
it('refuses a second same-scope uncertain non-read call before refresh and isolates concurrent writers',async()=>{
 const memory=new MemoryRunStore(),execute=vi.fn(async()=>{throw Error('unknown')})
 const first=await contextualSession(session(memory,'live-unknown',execute)),tool=first.session.values.hostTools.find(t=>t.name==='save_object')!
 await expect(tool.execute({},signal(),'one')).rejects.toThrow();await expect(tool.execute({},signal(),'new-wire')).rejects.toBeInstanceOf(HostOutcomeUnknown);expect(execute).toHaveBeenCalledOnce();first.context.close()
 const store=new MemoryRunStore(),effect=vi.fn(async()=>({content:[]}))
 const seed=await contextualSession(session(store,'concurrent',effect));seed.context.close()
 const attempt=async()=>{const opened=await contextualSession(session(store,'concurrent',effect,true));try{return await opened.session.values.hostTools.find(t=>t.name==='save_object')!.execute({},signal())}finally{opened.context.close()}}
 const outcomes=await Promise.allSettled([attempt(),attempt()])
 expect(outcomes.filter(o=>o.status==='fulfilled')).toHaveLength(1);expect(effect).toHaveBeenCalledOnce()
})
