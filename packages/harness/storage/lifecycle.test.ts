import {it,expect,vi} from 'vitest'
import {RunWriter,StorageOutcomeUnknown,ContinuationIdentityChanged,type DurableBinding,readStoredRunEvent} from './writer'
import {MemoryRunStore,type RunStore} from './run'
import {RECORD_DEFAULTS} from '../records'
import type {ExecutionCheckpoint} from '../execution/state'
const checkpoint=(id:string):ExecutionCheckpoint=>({version:2,id,requestKey:'a'.repeat(64),phase:'work',status:'running',unit:'model-requests',used:0,limit:20,grants:[],plan:[],explanation:'',operations:[],context:{scope:'fixture',entries:[]}})
const setup=(id:string,store:RunStore=new MemoryRunStore())=>{
 const writer=new RunWriter(store,id),binding:DurableBinding={writer,root:true,scope:{execution:id,role:'author'}}
 writer.bind(()=>checkpoint(id),RECORD_DEFAULTS,()=>undefined)
 return {writer,binding,store}
}
for(const mode of ['dispatch-canceled','callback-canceled','explicit-stop','verify-canceled'])it(`finalizes cancellation through original writer: ${mode}`,async()=>{
 const memory=new MemoryRunStore();let stops=0
 const store:RunStore={durability:'memory',load:memory.load.bind(memory),readEvents:memory.readEvents.bind(memory),commit:async(input,signal)=>{
  const cp=JSON.parse(new TextDecoder().decode(input.checkpoint!));if(cp.status==='stopped'){stops++;expect(input.events).toHaveLength(2)}
  return memory.commit(input,signal)
 }}
 const {writer,binding}=setup('cancel-finalize',store),controller=new AbortController()
 const exact={content:[{type:'text',text:'earlier exact result'}],opaque:{nested:[null,0,false]}}
 const tool=vi.fn(async()=>exact)
 await writer.invoke(binding,'host','source','write','{"value":1}',false,tool)
 const before=writer.checkpoint!.durable!.dispatches[0]
 const cancel=new DOMException('The operation was aborted','AbortError')
 const model=vi.fn(async()=>{if(mode==='verify-canceled')return new Response('verified');if(mode==='explicit-stop')writer.closeDispatch(binding);if(mode!=='callback-canceled')controller.abort(cancel);throw cancel})
 const invocation=writer.invokeModel(binding,'source','fixture','{"request":"exact"}',model,async response=>{if(mode==='verify-canceled')controller.abort(cancel);return {response}},controller.signal)
 if(mode==='verify-canceled')await expect(invocation).rejects.toMatchObject({name:'AbortError'});else await expect(invocation).rejects.toBe(cancel)
 if(mode!=='callback-canceled')await expect(writer.event(binding,{type:'end'},controller.signal)).rejects.toThrow()
 await expect(writer.invokeModel(binding,'source','fixture','{}',model,async response=>({response}),new AbortController().signal)).rejects.toThrow()
 await expect(writer.event(binding,{type:'end'})).rejects.toThrow()
 await expect(writer.save(binding,checkpoint(writer.id))).rejects.toThrow()
 const events=await writer.finalizeStop(binding,new AbortController().signal)
 expect(events.map(e=>e.type)).toEqual(['execution_checkpoint','end']);expect(stops).toBe(1)
 expect(writer.checkpoint!.status).toBe('stopped');expect(writer.checkpoint!.durable!.dispatches[0]).toEqual(before);expect(writer.checkpoint!.durable!.dispatches[1]!.state).toBe('unknown')
 expect(model).toHaveBeenCalledOnce()
 const page=await memory.readEvents(writer.id,null,100)
 expect(page.items.map(item=>readStoredRunEvent(item.bytes).event.type)).toEqual(['execution_checkpoint','end'])
 const restored=new RunWriter(memory,writer.id,await memory.load(writer.id)),next={...binding,writer:restored}
 await expect(restored.invoke(next,'host','source','write','{"value":1}',false,tool)).rejects.toThrow();expect(tool).toHaveBeenCalledOnce()
 expect(restored.checkpoint!.durable).toEqual(writer.checkpoint!.durable)
})
for(const mode of ['wrong-writer','wrong-run','child','stale-revision','lost-ack','store-failure'])it(`refuses stop identity/storage failure: ${mode}`,async()=>{
 const memory=new MemoryRunStore();let armed=false,writes=0
 const store:RunStore={durability:'memory',load:memory.load.bind(memory),readEvents:memory.readEvents.bind(memory),commit:async(input,signal)=>{
  writes++;if(armed&&mode==='store-failure')throw Error('fixture storage outcome unavailable')
  const receipt=await memory.commit(input,signal);if(armed&&mode==='lost-ack')throw Error('fixture acknowledgement unavailable');return receipt
 }}
 const {writer,binding}=setup('stop-identity',store);await writer.save(binding,checkpoint(writer.id))
 const target={...binding,scope:{...binding.scope}}
 if(mode==='wrong-writer')target.writer=new RunWriter(memory,'other')
 if(mode==='wrong-run')target.scope.execution='other'
 if(mode==='child'){target.root=false;target.scope.task='child'}
 if(mode==='stale-revision'){const head=(await memory.load(writer.id))!;await memory.commit({runId:writer.id,expectedRevision:head.revision,commitId:'competing-commit',checkpoint:head.checkpoint,events:[]})}
 const before=writes;armed=true
 if(['wrong-writer','wrong-run','child'].includes(mode)){await expect(writer.finalizeStop(target,new AbortController().signal)).rejects.toBeInstanceOf(ContinuationIdentityChanged);expect(writes).toBe(before);return}
 let failed:unknown;try{await writer.finalizeStop(target,new AbortController().signal)}catch(error){failed=error}
 expect(failed).toBeDefined();await expect(writer.finalizeStop(binding,new AbortController().signal)).rejects.toBe(failed);expect(writes).toBe(before+1)
 if(mode!=='stale-revision')expect(failed).toBeInstanceOf(StorageOutcomeUnknown)
})
it('bounds finalization to five seconds and honors shorter host abort while closing dispatch immediately',async()=>{
 const memory=new MemoryRunStore();let armed=false
 const store:RunStore={durability:'memory',load:memory.load.bind(memory),readEvents:memory.readEvents.bind(memory),commit:async(input,signal)=>{
  if(armed){expect(signal).toBeDefined();await new Promise<void>((_,reject)=>{signal!.addEventListener('abort',()=>reject(signal!.reason),{once:true})})}
  return memory.commit(input,signal)
 }}
 const {writer,binding}=setup('bounded-stop',store);await writer.save(binding,checkpoint(writer.id));writer.closeDispatch(binding)
 const physical=vi.fn(async()=>1);await expect(writer.invoke(binding,'host','source','write','{}',false,physical)).rejects.toThrow();expect(physical).not.toHaveBeenCalled()
 armed=true
 await expect(writer.finalizeStop(binding,AbortSignal.timeout(20))).rejects.toBeInstanceOf(StorageOutcomeUnknown)
 expect(writer.checkpoint!.status).toBe('running')
})
it('keeps acknowledged root for child claims/results while a new root bind still owns live updates',async()=>{
 const {writer,binding}=setup('root-owner'),stale=checkpoint(writer.id);stale.status='complete'
 writer.bind(()=>stale,RECORD_DEFAULTS,()=>undefined);await writer.save(binding,checkpoint(writer.id))
 const child={writer,root:false,scope:{execution:'child',role:'author',task:'child'}}
 await writer.invoke(child,'host','source','read','{}',true,async()=>{expect(writer.checkpoint!.status,'child claim replaced acknowledged root').toBe('running');return 1})
 expect(writer.checkpoint!.status,'child result replaced acknowledged root').toBe('running')
 const next=checkpoint(writer.id);next.used=3;writer.bind(()=>next,RECORD_DEFAULTS,()=>undefined)
 await writer.event(binding,{type:'end'});expect(writer.checkpoint!.used).toBe(3)
})

it('bounds queue acquisition and prevents expired finalization from committing later',async()=>{
 const memory=new MemoryRunStore();let enter!:()=>void,release!:()=>void,writes=0
 const entered=new Promise<void>(resolve=>enter=resolve),blocked=new Promise<void>(resolve=>release=resolve)
 const store:RunStore={durability:'memory',load:memory.load.bind(memory),readEvents:memory.readEvents.bind(memory),commit:async(input,signal)=>{writes++;enter();await blocked;return memory.commit(input,signal)}}
 const {writer,binding}=setup('busy-stop',store),saving=writer.save(binding,checkpoint(writer.id))
 await entered
 writer.closeDispatch(binding)
 const stopping=writer.finalizeStop(binding,AbortSignal.timeout(20))
 const result=await Promise.race([stopping.then(()=> 'unexpected success',()=> 'bounded'),new Promise<string>(resolve=>setTimeout(()=>resolve('unbounded'),200))])
 release();await saving;await stopping.catch(()=>undefined);await new Promise(resolve=>setTimeout(resolve,0))
 expect(result,'Stop deadline did not bound writer queue').toBe('bounded');expect(writes).toBe(1);expect(writer.checkpoint!.status).toBe('running')
})
it('ignores late model success after acknowledged Stop',async()=>{
 const {writer,binding}=setup('late-completion'),handle=await writer.startModel(binding,'source','fixture','{}')
 await writer.finalizeStop(binding,new AbortController().signal);const before=writer.checkpoint
 await expect(writer.finishModel(binding,handle,{ok:true})).resolves.toBeUndefined()
 expect(writer.checkpoint).toEqual(before)
})
it('does not revive a stopped root or invent release proof from a pending handoff',async()=>{
 const {writer,binding}=setup('pending-stop')
 await writer.event(binding,{type:'delegation_requested',callId:'delegate-wire',request:{tasks:[{title:'Review',profile:'review',objective:'Check claims.'}]}})
 await writer.finalizeStop(binding,new AbortController().signal)
 expect(writer.checkpoint!.status).toBe('stopped');expect(writer.checkpoint!.durable!.pendingHandoff).toBeDefined()
})

it('caps a stalled attempted finalization independently at five seconds',async()=>{
 const memory=new MemoryRunStore();let armed=false,release!:()=>void
 const blocked=new Promise<void>(resolve=>release=resolve)
 const store:RunStore={durability:'memory',load:memory.load.bind(memory),readEvents:memory.readEvents.bind(memory),commit:async(input,signal)=>{if(armed){await blocked;return memory.commit(input)}return memory.commit(input,signal)}}
 const {writer,binding}=setup('independent-cap',store);await writer.save(binding,checkpoint(writer.id));armed=true
 const stopping=writer.finalizeStop(binding,new AbortController().signal)
 const result=await Promise.race([stopping.then(()=>null,error=>error),new Promise<null>(resolve=>setTimeout(()=>resolve(null),6000))])
 release();await stopping.catch(()=>undefined);await new Promise(resolve=>setTimeout(resolve,0))
 expect(result,'missing five-second persistence bound').toBeInstanceOf(StorageOutcomeUnknown)
 await expect(writer.finalizeStop(binding,new AbortController().signal)).rejects.toBe(result)
},7500)
