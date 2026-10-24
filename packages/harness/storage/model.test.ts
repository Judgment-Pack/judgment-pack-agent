import {describe,it,expect,vi} from 'vitest'
import fixtures from '../../../contract/vectors/storage/model-terminals.json'
import type {EndpointKind} from '../host'
import {quarantineModelResponse,verifyModelTerminal,DURABLE_MODEL_BYTES} from './model'
import {MemoryRunStore,type RunStore} from './run'
import {durableSession,durableOwner,scopeFor,ContinuationIdentityChanged,HostOutcomeUnknown,StorageOutcomeUnknown} from './writer'
import {contextualSession} from '../context/session'
import type {AssistantSession} from '../engine'
import {AIFailureError,failure} from '../failures'
const response=(body:string,streamed:boolean)=>new Response(body,{headers:{'content-type':streamed?'text/event-stream':'application/json'}})
for(const row of fixtures.cases)it(`terminal evidence: ${row.name}`,async()=>{
 const verify=()=>verifyModelTerminal(row.family as EndpointKind,row.streamed,row.body)
 if(!row.accepted){expect(verify).toThrow();await expect(quarantineModelResponse(row.family as EndpointKind,response(row.body,row.streamed),new AbortController().signal)).rejects.toThrow()}
 else{expect(verify).not.toThrow();const value=await quarantineModelResponse(row.family as EndpointKind,response(row.body,row.streamed),new AbortController().signal);expect(await value.response.text()).toBe(row.body)}
})
it('bounds oversized bodies and cancels never-ending bodies without completion proof',async()=>{
 await expect(quarantineModelResponse('openai-compatible',response('x'.repeat(DURABLE_MODEL_BYTES+1),true),new AbortController().signal)).rejects.toThrow()
 const aborted=new AbortController(),cancel=vi.fn(()=>new Promise<void>(()=>{}))
 const pending=quarantineModelResponse('openai-compatible',new Response(new ReadableStream({cancel})),aborted.signal)
 aborted.abort()
 await expect(pending).rejects.toThrow();expect(cancel).toHaveBeenCalled()
})
const opened=async(id:string,store:RunStore=new MemoryRunStore(),resume=false,call:AssistantSession['model']['call']=vi.fn())=>{
 const root=new AbortController()
 const raw:AssistantSession={prompt:'Work',testPrompt:'',tools:[],hostTools:[],callTool:vi.fn(),model:{family:'openai-compatible',model:'fixture',call},signal:root.signal,context:{currentRequest:{message:'Work',input:'Work'},runStore:{store,id,resume}}}
 const input=await durableSession(raw),session=await contextualSession(input)
 return {store,session,binding:input.context![durableOwner]!,root}
}
describe('private model completion handles',()=>{
 for(const mode of ['wrong-writer','wrong-scope','forged-copy','duplicate','stale-resume'])it(`refuses ${mode}`,async()=>{
  const first=await opened('handle'),{binding}=first
  const token=await binding.writer.startModel(binding,'source','fixture','{}')
  expect(binding.writer.checkpoint!.durable!.dispatches[0]!.state).toBe('claimed')
  let target=binding,handle:object=token,other:Awaited<ReturnType<typeof opened>>|undefined
  if(mode==='wrong-writer'){other=await opened('other');target=other.binding}
  if(mode==='wrong-scope')target=scopeFor(binding,'critic')
  if(mode==='forged-copy')handle={...token}
  if(mode==='duplicate')await binding.writer.finishModel(binding,token,{ok:true})
  if(mode==='stale-resume'){other=await opened('handle',first.store,true);target=other.binding}
  // Spread retains symbols, but pointer identity still cannot recreate the capability.
  await expect(target.writer.finishModel(target,handle,{ok:true})).rejects.toBeInstanceOf(ContinuationIdentityChanged)
  expect(target.writer.failed).toBe(true);first.session.context.close();other?.session.context.close()
 })
 it('keeps an unresolved non-read host claim unknown across model cancellation and resume',async()=>{
  const first=await opened('host-barrier'),{binding}=first,calls=vi.fn(async()=>{throw Error('unknown host fixture')})
  await expect(binding.writer.invoke(binding,'host','source','write','{}',false,calls)).rejects.toThrow()
  const handle=await binding.writer.startModel(binding,'source','fixture','{}')
  await binding.writer.finishModel(binding,handle,{ok:false,error:Error('incomplete model fixture')});first.session.context.close()
  const next=await opened('host-barrier',first.store,true)
  await expect(next.binding.writer.invoke(next.binding,'host','source','write','{}',false,calls)).rejects.toBeInstanceOf(HostOutcomeUnknown)
  expect(calls).toHaveBeenCalledOnce();next.session.context.close()
 })
})

for(const mode of ['verified-refusal','pending','unverified-refusal','malformed-refusal'] as const)it(`restore preserves model identity except acknowledged verified refusal: ${mode}`,async()=>{
 const first=await opened('restore-'+mode),{binding}=first,original='{"request":"old"}',later='{"request":"later"}'
 if(mode==='verified-refusal'||mode==='malformed-refusal'){
  const invoke=binding.writer.invokeModel(binding,'source','fixture',original,async()=>new Response(mode==='malformed-refusal'?' {"error":':'{"error":{"code":"rate_limit_exceeded"}}',{status:429}),response=>quarantineModelResponse('openai-compatible',response,first.session.session.signal),first.session.session.signal)
  if(mode==='verified-refusal')expect((await invoke).status).toBe(429);else await expect(invoke).rejects.toThrow()
 }else{
  const handle=await binding.writer.startModel(binding,'source','fixture',original)
  if(mode==='unverified-refusal')await binding.writer.finishModel(binding,handle,{ok:false,error:new AIFailureError({...failure('rate-limited','model','refused'),status:429})})
 }
 first.session.context.close()
 const next=await opened('restore-'+mode,first.store,true),row=next.binding.writer.checkpoint!.durable!.dispatches[0]!
 expect(next.binding.writer.prepared(next.binding,'model','source','fixture',later)).toBe(mode==='verified-refusal'?later:original)
 expect(next.binding.writer.recovering(next.binding,'model')).toBe(mode!=='verified-refusal')
 if(mode==='verified-refusal'){expect(row.state).toBe('result');expect(row.failure?.outcome).toBe('refused')}
 next.session.context.close()
})

it('never replays an unresolved persisted non-read model claim',async()=>{
 const first=await opened('model-nonread'),{binding}=first
 await expect(binding.writer.invoke(binding,'model','source','fixture','{}',false,async()=>{throw Error('unresolved non-read model')})).rejects.toThrow()
 first.session.context.close();const next=await opened('model-nonread',first.store,true),dispatch=vi.fn()
 await expect(next.binding.writer.invokeModel(next.binding,'source','fixture','{}',dispatch,async response=>({response}),next.session.session.signal)).rejects.toBeInstanceOf(HostOutcomeUnknown)
 expect(dispatch).not.toHaveBeenCalled();next.session.context.close()
})

for(const mode of ['missing','stale','wrong','lost'] as const)it(`verified refusal remains quarantined after ${mode} acknowledgement`,async()=>{
 const memory=new MemoryRunStore();let armed=true
 const store:RunStore={durability:'memory',load:memory.load.bind(memory),readEvents:memory.readEvents.bind(memory),commit:async(transaction,signal)=>{
  const receipt=await memory.commit(transaction,signal),saved=JSON.parse(new TextDecoder().decode(transaction.checkpoint!))
  if(armed&&saved.durable.dispatches[0]?.state==='result'){
   armed=false
   if(mode==='lost')throw Error('private acknowledgement detail')
   if(mode==='missing')return undefined as never
   return {...receipt,...(mode==='stale'?{revision:transaction.expectedRevision!}:{commitId:'other-commit'})}
  }
  return receipt
 }}
 const first=await opened('refusal-ack-'+mode,store),{binding}=first,signal=first.session.session.signal
 const execute=vi.fn(async()=>new Response('{"error":{"code":"rate_limit_exceeded"}}',{status:429})),verify=(response:Response)=>quarantineModelResponse('openai-compatible',response,signal)
 await expect(binding.writer.invokeModel(binding,'source','fixture','{}',execute,verify,signal)).rejects.toBeInstanceOf(StorageOutcomeUnknown)
 expect(binding.writer.failed).toBe(true);expect(binding.writer.checkpoint!.durable!.dispatches[0]!.state).toBe('claimed')
 await expect(binding.writer.invokeModel(binding,'source','fixture','{}',execute,verify,signal)).rejects.toBeInstanceOf(StorageOutcomeUnknown)
 expect(execute).toHaveBeenCalledOnce();first.session.context.close()
})

for(const mode of ['headers','iterator-return'])it(`ordinary ${mode} is not model terminal authority`,async()=>{
 const first=await opened('generic-'+mode),{binding}=first
 const execute=async()=>{if(mode==='headers')return new Response(null);return (async function*(){return 'local return'})().next()}
 await expect(binding.writer.invoke(binding,'model','source','fixture','{}',true,execute)).rejects.toThrow()
 expect(binding.writer.checkpoint!.durable!.dispatches[0]!.state).toBe('unknown');first.session.context.close()
})

it('forwards root cancellation into the actual provider call before response headers',async()=>{
 let started!:()=>void,forwarded:AbortSignal|undefined
 const reached=new Promise<void>(resolve=>{started=resolve}),independent=new AbortController()
 const call=vi.fn((_suffix:string,request:Parameters<AssistantSession['model']['call']>[1])=>new Promise<Response>((_resolve,reject)=>{forwarded=request.signal;started();request.signal!.addEventListener('abort',()=>reject(request.signal!.reason),{once:true})}))
 const first=await opened('root-before-headers',new MemoryRunStore(),false,call)
 const pending=first.session.session.values.model.call('chat/completions',{method:'POST',body:'{"model":"fixture","messages":[]}',signal:independent.signal})
 try{
  await reached;first.root.abort();expect(forwarded?.aborted).toBe(true);expect(independent.signal.aborted).toBe(false)
  await expect(pending).rejects.toThrow();expect(call).toHaveBeenCalledOnce()
  expect(first.binding.writer.checkpoint!.durable!.dispatches[0]!.state).toBe('claimed')
 }finally{independent.abort();first.root.abort();await pending.catch(()=>undefined);first.session.context.close()}
})
