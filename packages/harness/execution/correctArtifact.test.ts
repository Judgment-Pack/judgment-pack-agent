import {expect,it,vi} from 'vitest'
import {correctArtifact,type CorrectionPorts} from './correctArtifact'
import {contextualSession} from '../context/session'
import {normalize} from '../thinking'
import type {AssistantSession} from '../engine'
import {ExecutionPaused} from './state'
const signal=()=>new AbortController().signal
it.each(['pack','dialect','future-artifact'])('checks and checkpoints %s before each correction, retaining exact typed inputs',async kind=>{
 const events:string[]=[],facts={decimal:'24',zero:0,flag:false,nothing:null},initial={kind,revision:1,facts}
 const result=await correctArtifact({initial,maxCorrections:2,fingerprint:c=>String(c.revision),check:async c=>{events.push('check'+c.revision);return c.revision===2},passed:check=>check,checkpoint:async(c)=>{events.push('saved'+c.revision)},correct:async c=>{events.push('correct'+c.revision);return {...c,revision:2}}},signal())
 expect(events).toEqual(['check1','saved1','correct1','check2','saved2'])
 expect(result).toMatchObject({state:'ready',corrections:1,candidate:{facts}})
 expect(initial).toEqual({kind,revision:1,facts})
})
it('stops cycles, bounded changes and policy explanations without saving or retrying',async()=>{
 const run=async(next:(c:number)=>number|null,max=5)=>{
  const check=vi.fn(async()=>false),correct=vi.fn(async(c:number)=>next(c))
  return {result:await correctArtifact({initial:1,maxCorrections:max,fingerprint:String,check,passed:check=>check,correct,checkpoint:async()=>{}},signal()),check,correct}
 }
 const cycle=await run(c=>c===1?2:1)
 expect(cycle.result.state).toBe('stalled');expect(cycle.check).toHaveBeenCalledTimes(2)
 const limited=await run(c=>c+1,2)
 expect(limited.result.state).toBe('limit');expect(limited.correct).toHaveBeenCalledTimes(2)
 const policy=await run(()=>null)
 expect(policy.result.state).toBe('answered');expect(policy.correct).toHaveBeenCalledTimes(1)
})
it('propagates storage, transport and cancellation before any next request',async()=>{
 const correct=vi.fn(async(c:number)=>c+1)
 const base:CorrectionPorts<number,boolean>={initial:1,maxCorrections:2,fingerprint:String,check:async()=>false,passed:c=>c,correct,checkpoint:async()=>{}}
 for(const phase of ['check','checkpoint'] as const){const failure=Error(phase);await expect(correctArtifact({...base,[phase]:async()=>{throw failure}},signal())).rejects.toBe(failure)}
 const controller=new AbortController()
 await expect(correctArtifact({...base,check:async()=>{controller.abort();return false}},controller.signal)).rejects.toMatchObject({name:'AbortError'})
 expect(correct).not.toHaveBeenCalled()
})
it.each(['gemini','openai-compatible','anthropic'] as const)('cannot reset the shared request budget while correcting through %s',async family=>{
 const original:AssistantSession={prompt:'Fix requested artifact',context:{currentRequest:{message:'Fix it',input:'Current artifact and saved expectations'}},allowConversation:true,interactive:true,testPrompt:'',tools:[],hostTools:[],callTool:vi.fn(async()=>({content:[]})),model:{family,model:'fixture',call:vi.fn(async()=>new Response('{}'))},thinking:normalize('off',family),signal:signal()}
 const first=await contextualSession(original)
 first.context.execution.state.used=19
 let held=first.context.execution.snapshot('complete')
 const checked:number[]=[]
 await expect(correctArtifact({initial:1,maxCorrections:5,fingerprint:String,check:async()=>false,passed:c=>c,checkpoint:async c=>{checked.push(c)},correct:async c=>{
  const resumed=await contextualSession({...original,context:{...original.context,execution:held}})
  await resumed.session.model.call('generate',{body:'{}'})
  held=resumed.context.execution.snapshot('complete')
  return c+1
 }},signal())).rejects.toBeInstanceOf(ExecutionPaused)
 expect(original.model.call).toHaveBeenCalledTimes(1)
 expect(held).toMatchObject({used:20,limit:20,grants:[]})
 expect(checked).toEqual([1,2])
})
