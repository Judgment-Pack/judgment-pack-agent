import {it,expect,vi} from 'vitest'
import {vercel} from '../engines/vercel'
import {deepagents} from '../engines/deepagents'
import type {AssistantSession,AssistantEvent} from '../engine'
import type {EndpointKind} from '../host'
import {MemoryRunStore,type RunStore} from './run'

const toolBody='data: {"id":"fixture","object":"chat.completion.chunk","model":"fixture","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-fixture","type":"function","function":{"name":"read_fixture","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}\n\ndata: [DONE]\n\n'
const finalBody='data: {"id":"fixture","object":"chat.completion.chunk","model":"fixture","choices":[{"index":0,"delta":{"content":"Done."},"finish_reason":"stop"}]}\n\ndata: [DONE]\n\n'
const latch=()=>{let resolve!:()=>void;return {promise:new Promise<void>(done=>{resolve=done}),resolve:()=>resolve()}}
const checkpoint=async(store:MemoryRunStore,id:string)=>JSON.parse(new TextDecoder().decode((await store.load(id))!.checkpoint!))
const input=(store:RunStore,id:string,family:EndpointKind,call:AssistantSession['model']['call'],signal:AbortSignal,execute=vi.fn(async()=>({content:[{type:'text',text:'fixture result'}]}))):AssistantSession=>({
 prompt:'Read the fixture then answer.',testPrompt:'',tools:[],hostTools:[{name:'read_fixture',description:'Read fixture',inputSchema:{type:'object',properties:{}},effect:'read',execute}],callTool:vi.fn(),
 model:{family,model:'fixture',call},signal,allowConversation:true,interactive:false,critic:'off',context:{currentRequest:{message:'Read fixture',input:'Read fixture'},runStore:{store,id}}
})
const consume=async(iterator:AsyncIterator<AssistantEvent>)=>{const events:AssistantEvent[]=[];for(;;){const next=await iterator.next();if(next.done)return events;events.push(next.value)}}
const beforeEnd=async<T>(barrier:Promise<T>,done:Promise<unknown>)=>Promise.race([barrier,done.then(()=>{throw Error('engine ended before fixture barrier')})])

for(const engine of [vercel,deepagents]){
 for(const mode of ['acknowledged','lost-ack','missing-terminal','cancel-body','iterator-return'] as const)it(`${engine.id}: quarantines original body and awaits result acknowledgement (${mode})`,async()=>{
  const memory=new MemoryRunStore(),headers=latch(),reading=latch(),ack=latch(),release=latch(),abort=new AbortController(),execute=vi.fn(async()=>({content:[{type:'text',text:'fixture result'}]}))
  let armed=true,controller!:ReadableStreamDefaultController<Uint8Array>
  const store:RunStore={durability:'memory',load:memory.load.bind(memory),readEvents:memory.readEvents.bind(memory),commit:async(transaction,signal)=>{
   const receipt=await memory.commit(transaction,signal),saved=JSON.parse(new TextDecoder().decode(transaction.checkpoint!)),first=saved.durable?.dispatches[0]
   if(armed&&['acknowledged','lost-ack'].includes(mode)&&first?.kind==='model'&&first.state==='result'){
    armed=false;ack.resolve()
    if(mode==='lost-ack')throw Error('private lost acknowledgement fixture')
    await release.promise
   }
   return receipt
  }}
  const call=vi.fn(async()=>{
   if(call.mock.calls.length===1){const stream=new ReadableStream<Uint8Array>({start(given){controller=given},pull(){reading.resolve()}},{highWaterMark:0});headers.resolve();return new Response(stream,{headers:{'content-type':'text/event-stream'}})}
   return new Response(finalBody,{headers:{'content-type':'text/event-stream'}})
  })
  const iterator=engine.start(input(store,'engine-'+mode,'openai-compatible',call,abort.signal,execute))[Symbol.asyncIterator]()
  const done=consume(iterator)
  try{
   await beforeEnd(headers.promise,done)
   expect(await beforeEnd(Promise.race([reading.promise.then(()=>true),ack.promise.then(()=>false)]),done),'headers promoted a claim before the original body was read').toBe(true)
   const saved=await checkpoint(memory,'engine-'+mode)
   expect(saved.durable.dispatches[0].state).toBe('claimed');expect(call).toHaveBeenCalledOnce();expect(execute).not.toHaveBeenCalled()
   if(mode==='cancel-body')abort.abort()
   else if(mode==='iterator-return')await iterator.return!()
   else{
    controller.enqueue(new TextEncoder().encode(mode==='missing-terminal'?toolBody.replace('data: [DONE]\n\n',''):toolBody));controller.close()
    if(mode==='acknowledged'||mode==='lost-ack'){
     await beforeEnd(ack.promise,done)
     expect(call).toHaveBeenCalledOnce();expect(execute).not.toHaveBeenCalled();release.resolve()
    }
   }
   const events=await done
   if(mode==='acknowledged'){
    expect(events.filter(e=>e.type==='error')).toEqual([]);expect(call).toHaveBeenCalledTimes(2);expect(execute).toHaveBeenCalledOnce()
    expect((await checkpoint(memory,'engine-'+mode)).durable.dispatches.filter((row:{kind:string})=>row.kind==='model').every((row:{state:string})=>row.state==='result')).toBe(true)
   }else{
    expect(call).toHaveBeenCalledOnce();expect(execute).not.toHaveBeenCalled()
    if(mode!=='lost-ack')expect((await checkpoint(memory,'engine-'+mode)).durable.dispatches[0].state).not.toBe('result')
    if(mode==='lost-ack')expect(events.some(e=>e.type==='error'&&e.failure?.detail?.startsWith('StorageOutcomeUnknown:'))).toBe(true)
   }
  }finally{release.resolve();abort.abort();await done.catch(()=>undefined)}
 })
 for(const family of ['openai-compatible','anthropic','gemini'] as const)it(`${engine.id}: accepts positively terminated original whole ${family} response`,async()=>{
  const bodies={
   'openai-compatible':'{"id":"fixture","object":"chat.completion","model":"fixture","choices":[{"index":0,"message":{"role":"assistant","content":"Done."},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}',
   anthropic:'{"id":"fixture","type":"message","role":"assistant","model":"fixture","content":[{"type":"text","text":"Done."}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}',
   gemini:'{"candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"Done."}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}'
  }
  const memory=new MemoryRunStore(),abort=new AbortController(),call=vi.fn(async()=>new Response(bodies[family],{headers:{'content-type':'application/json'}}))
  const events=await consume(engine.start(input(memory,'whole-'+family,family,call,abort.signal))[Symbol.asyncIterator]())
  expect(events.filter(e=>e.type==='error')).toEqual([]);expect(call).toHaveBeenCalledOnce()
  const saved=await checkpoint(memory,'whole-'+family);expect(saved.durable.dispatches[0].state).toBe('result')
 })
}

for(const engine of [vercel,deepagents])it(`${engine.id}: retries only after fully verified acknowledged refusal`,async()=>{
 const memory=new MemoryRunStore(),abort=new AbortController()
 const call=vi.fn(async()=>call.mock.calls.length===1?new Response('{"error":{"code":"rate_limit_exceeded"}}',{status:429,headers:{'content-type':'application/json'}}):new Response(finalBody,{headers:{'content-type':'text/event-stream'}}))
 const events=await consume(engine.start(input(memory,'verified-refusal','openai-compatible',call,abort.signal))[Symbol.asyncIterator]())
 expect(events.filter(event=>event.type==='error')).toEqual([]);expect(call).toHaveBeenCalledTimes(2)
 const models=(await checkpoint(memory,'verified-refusal')).durable.dispatches.filter((row:{kind:string})=>row.kind==='model')
 expect(models).toHaveLength(2);expect(models[0].state).toBe('result');expect(models[0].failure.outcome).toBe('refused');expect(models[1].state).toBe('result')
})
