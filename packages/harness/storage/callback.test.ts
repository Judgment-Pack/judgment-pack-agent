import {it,expect,vi} from 'vitest'
import type {AssistantSession} from '../engine'
import type {ExecutionCheckpoint} from '../execution/state'
import {contextualSession} from '../context/session'
import {durableSession,durableOwner,RunWriter,ContinuationNotRecoverable} from './writer'
import {MemoryRunStore,checkpointCallbackRunStore} from './run'
import vectors from '../../../contract/vectors/storage/checkpoints.json'
const input=(checkpoint:(cp:ExecutionCheckpoint)=>Promise<void>):AssistantSession=>({prompt:'Work',testPrompt:'',tools:[],hostTools:[],callTool:vi.fn(),critic:'off',signal:new AbortController().signal,model:{family:'openai-compatible',model:'fixture',call:vi.fn(async()=>new Response('{}'))},context:{currentRequest:{message:'Work',input:'Work'},checkpoint}})
it('keeps callback-only acknowledgement and secret-bearing input on the limited legacy path',async()=>{
 const saved:ExecutionCheckpoint[]=[],order:string[]=[],raw=input(async cp=>{saved.push(cp);order.push('saved-'+cp.used)})
 raw.model.call=vi.fn(async()=>{order.push('sent');return new Response('{}')})
 const profile=await durableSession(raw);expect(profile.context?.[durableOwner]).toBeUndefined()
 const opened=await contextualSession(profile)
 await opened.session.values.model.call('chat/completions',{body:JSON.stringify({messages:[{role:'tool',content:'AKIAABCDEFGHIJKLMNOP'}]})})
 expect(order).toEqual(['saved-0','saved-1','sent']);expect(saved.every(cp=>cp.durable===undefined)).toBe(true)
 opened.context.close()
})
for(const failed of [false,true])it(`requires legacy callback acknowledgement before dispatch (${failed?'failed':'blocked'})`,async()=>{
 let enter!:()=>void,release!:()=>void;const entered=new Promise<void>(done=>enter=done),held=new Promise<void>(done=>release=done)
 const raw=input(async cp=>{if(cp.used===1){enter();await held;if(failed)throw Error('private callback failure')}}),opened=await contextualSession(raw)
 const sent=opened.session.values.model.call('chat/completions',{body:'{}'});await entered;expect(raw.model.call).not.toHaveBeenCalled();release()
 if(failed)await expect(sent).rejects.toMatchObject({failure:{code:'storage',outcome:'not-started'}});else await sent
 expect(raw.model.call).toHaveBeenCalledTimes(failed?0:1);opened.context.close()
})
it('cannot adopt an explicit RunStore journal through either callback entry point',async()=>{
 const raw=input(vi.fn(async()=>{}));raw.context!.execution=structuredClone(vectors.items[0]) as ExecutionCheckpoint
 await expect(durableSession(raw)).rejects.toBeInstanceOf(ContinuationNotRecoverable)
 const store=checkpointCallbackRunStore(async()=>{},bytes=>JSON.parse(new TextDecoder().decode(bytes)))
 expect(()=>new RunWriter(store,'cannot-journal')).toThrow(ContinuationNotRecoverable)
 const configured=input(vi.fn());delete configured.context!.checkpoint;configured.context!.runStore={store,id:raw.context!.execution.id};configured.context!.execution=raw.context!.execution
 await expect(durableSession(configured)).rejects.toBeInstanceOf(ContinuationNotRecoverable)
})
it('keeps an ambiguous explicit RunStore model response stopped with its unknown claim',async()=>{
 const raw=input(vi.fn());delete raw.context!.checkpoint;const store=new MemoryRunStore();raw.context!.runStore={store,id:'strict-unknown'}
 raw.model.call=vi.fn(async()=>new Response('{}',{status:504}))
 const opened=await contextualSession(raw)
 await expect(opened.session.values.model.call('chat/completions',{body:'{}'})).rejects.toMatchObject({failure:{code:'timeout',outcome:'unknown'}})
 await expect(opened.session.values.model.call('chat/completions',{body:'{}'})).rejects.toThrow()
 expect(raw.model.call).toHaveBeenCalledOnce();const head=(await store.load('strict-unknown'))!,cp=JSON.parse(new TextDecoder().decode(head.checkpoint!))
 expect(cp.durable.dispatches).toHaveLength(1);expect(cp.durable.dispatches[0].state).toBe('unknown');opened.context.close()
})
