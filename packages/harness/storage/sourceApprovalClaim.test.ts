import {expect,it,vi} from 'vitest'
import {contextualSession} from '../context/session'
import {GateViolation} from '../toolGate'
import type {AssistantSession,HostTool} from '../engine'
it('retains a concurrent original approval claim as pending before the first pause is persisted',async()=>{
 let entered!:()=>void,release!:()=>void
 const observing=new Promise<void>(resolve=>entered=resolve),held=new Promise<void>(resolve=>release=resolve)
 const approve=vi.fn(()=> 'ask' as const),execute=vi.fn(async()=>({content:[]}))
 const tool:HostTool={name:'create_issue',description:'Create an issue.',inputSchema:{type:'object'},taxonomy:{operation:'act',effect:'act'},effectClass:'act',source:{kind:'custom',id:'tracker',trust:'person',version:'1',toolListDigest:'sha256:d6523ad27b67175e7423219dc07eb056332d4b640030068e8c09549e91ca4d82',identity:{kind:'connection',label:'tracker'},currentIdentity:async()=>{entered();await held;return {kind:'connection',label:'tracker'}}},approve,execute}
 const input:AssistantSession={prompt:'Current request',testPrompt:'',tools:[],hostTools:[tool],callTool:vi.fn(),signal:new AbortController().signal,critic:'off',model:{family:'openai-compatible',model:'fixture',call:async()=>{throw Error('unexpected model call')}}}
 const ready=await contextualSession(input),gate=ready.context.sources!,offered=ready.session.values.hostTools.find(tool=>tool.name==='tracker__create_issue')!
 const first=gate.decide(offered.name,{},'first-call')
 try{
  await observing;expect(ready.context.execution.state.approval).toBeUndefined()
  const second=await gate.decide(offered.name,{},'second-call');expect(second.answer).toBe('pending');expect(gate.pending({name:offered.name,callId:'second-call'})).toBe(second.detail)
  await expect(gate.call(offered,{},input.signal,execute,'second-call')).rejects.toBeInstanceOf(GateViolation)
  expect(gate.figures().find(row=>row.source==='tracker')).toMatchObject({calls:0,refused:1});expect(approve).not.toHaveBeenCalled();expect(execute).not.toHaveBeenCalled()
  release();expect((await first).answer).toBe('ask');expect(ready.context.execution.state.approval?.callId).toBe('first-call');expect(approve).toHaveBeenCalledTimes(1)
 }finally{release();await first;ready.context.close()}
})
