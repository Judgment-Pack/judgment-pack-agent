import {afterEach,describe,expect,it} from 'vitest'
import {configureHarness} from './host'
import {contextualSession} from './context/session'
import {normalize} from './thinking'
import {recallMemory,readMemoryLifecycle,nextMemoryRecall,commitMemoryRecall,type Memory,type MemoryRecall} from './memories'
import {CheckpointMutation} from './execution/mutation'
import {readExecution,type ExecutionCheckpoint} from './execution/state'
import {readFailure} from './failures'
import {readHostParts} from './prompt'
import {CONTEXT_DEFAULTS} from './context/policy'
import {vercelTurn,sseTurn,streamed} from './testing/scripted'
import answers from '../../contract/vectors/memory/lifecycle-answer.json'
import transitions from '../../contract/vectors/memory/lifecycle-transitions.json'
import aliases from '../../contract/vectors/compatibility/retired-aliases.json'
import {resolvePolicy} from './policy'

const identity=transitions.identity as Omit<MemoryRecall,'state'>
const record:Memory={...identity,kind:'preference',text:'Use metric units.',source:{run:'fixture-host',proposedBy:'host'},scope:'project',createdAt:'2026-10-01T00:00:00Z',state:'accepted'}
const row=(state:Memory['state']='accepted'):MemoryRecall=>({...identity,state})
const update={...identity,state:'revoked' as const}
afterEach(()=>configureHarness({memoryFor:()=>undefined,memoryLifecycle:()=>undefined}))
const session=(execution?:ExecutionCheckpoint,memory=true)=>({prompt:'Current request',testPrompt:'',tools:[],hostTools:[],callTool:async()=>({}),signal:new AbortController().signal,thinking:normalize('off','openai-compatible'),model:{family:'openai-compatible' as const,model:'fixture',call:async()=>new Response()},critic:'off' as const,context:{phase:'plan' as const,...(execution?{execution}:{}),memory}})
const store=(recalled:MemoryRecall[])=>new CheckpointMutation({memories:{proposed:[],recalled}} as unknown as ExecutionCheckpoint)
describe('metadata-only memory lifecycle',()=>{
 it('reads every shared closed answer and rejects malformed, missing, extra and oversized rows',()=>{
  for(const answer of answers.items)expect(readMemoryLifecycle(answer)).toEqual(answer)
  for(const refused of answers.refused)expect(()=>readMemoryLifecycle(refused.answer),refused.name).toThrow('memoryLifecycle')
  for(const value of [[Object.create(update)],[{...update,[Symbol('extra')]:true}],Array(1),Array.from({length:257},(_,i)=>({...update,id:String(i)})),false,{},[{...update,digest:12}]])expect(()=>readMemoryLifecycle(value)).toThrow('memoryLifecycle')
  expect(readMemoryLifecycle(null)).toEqual([]);expect(readMemoryLifecycle(undefined)).toEqual([])
 })
 it('consumes every shared transition through validated lifecycle answers',async()=>{
  for(const input of transitions.cases){
   const one=input as {name:string;prior:Memory['state'];state?:'revoked'|'expired';id?:string;digest?:string;kind?:Memory['kind'];full?:Memory['state'];fullKind?:Memory['kind'];fullText?:string;fullDigest?:string;expiresAt?:string;result:Memory['state']|null}
   const prior=[row(one.prior)]
   const recall=await recallMemory(one.full?[{...record,state:one.full,kind:one.fullKind??record.kind,text:one.fullText??record.text,digest:one.fullDigest??record.digest,...(one.expiresAt?{expiresAt:one.expiresAt}:{})}]:[],4000,'marker')
   const lifecycle=readMemoryLifecycle(one.state?[{...identity,state:one.state,...(one.id?{id:one.id}:{}),...(one.digest?{digest:one.digest}:{}),...(one.kind?{kind:one.kind}:{})}]:[])
   if(one.result===null)expect(()=>nextMemoryRecall(prior,recall,lifecycle),one.name).toThrow('memoryLifecycle')
   else expect(nextMemoryRecall(prior,recall,lifecycle),one.name).toEqual([row(one.result)])
  }
 })
 it('stages valid-first invalid-later input and cumulative overflow without writes or success notices',async()=>{
  const saved=store([row(),{...row(),id:'second'}]),before=saved.read(),revision=saved.revision
  const recall=await recallMemory([],4000,'marker')
  for(const last of [{...update,id:'unknown'},{...update,id:'second',digest:'sha256:'+'0'.repeat(64)},{...update,id:'second',kind:'fact' as const}]){
   expect(()=>{const next=nextMemoryRecall(before.memories!.recalled,recall,readMemoryLifecycle([update,last]));commitMemoryRecall(()=>saved.read(),patch=>{saved.apply(patch)},before.memories!.recalled,next)}).toThrow()
   expect(saved.read()).toEqual(before);expect(saved.revision).toBe(revision)
  }
  const full=store(Array.from({length:256},(_,i)=>({...row(),id:String(i)})))
  const prior=full.read().memories!.recalled
  const incoming=await recallMemory([{...record,id:'new'}],4000,'marker')
  expect(()=>commitMemoryRecall(()=>full.read(),patch=>{full.apply(patch)},prior,nextMemoryRecall(prior,incoming,[]))).toThrow('full memory report')
  expect(full.read().memories!.recalled).toHaveLength(256)
 })
 it('rejects same-answer identity creation, preserves current proposals and detects stale recalled state',async()=>{
  const recall=await recallMemory([record],4000,'marker')
  expect(()=>nextMemoryRecall([],recall,[update])).toThrow('unknown')
  const saved=store([row()]),prior=saved.read().memories!.recalled
  const next=nextMemoryRecall(prior,await recallMemory([],4000,'marker'),[update])
  const proposal={...record,id:'proposal',state:'proposed' as const}
  saved.apply({memories:{proposed:[proposal],recalled:prior}})
  commitMemoryRecall(()=>saved.read(),patch=>{saved.apply(patch)},prior,next)
  expect(saved.read().memories!.proposed).toEqual([proposal])
  const revision=saved.revision
  commitMemoryRecall(()=>saved.read(),patch=>{saved.apply(patch)},next,next)
  expect(saved.revision).toBe(revision)
  expect(()=>commitMemoryRecall(()=>saved.read(),patch=>{saved.apply(patch)},prior,next)).toThrow('stale')
 })
 it('resumes through the canonical host hook after text deletion, including disabled recall',async()=>{
  let textCalls=0
  configureHarness({memoryFor:()=>{textCalls++;return [record]}})
  const first=await contextualSession(session())
  const nativeShape=first.context.execution.snapshot('stopped');first.context.close()
  const checkpoint=readExecution(JSON.parse(JSON.stringify(nativeShape)))!
  expect(checkpoint.memories!.recalled).toEqual([row()])
  for(const enabled of [true,false]){
   let lifecycleCalls=0;let retained:typeof update[]=[]
   configureHarness({memoryFor:()=>{textCalls++;return []},memoryLifecycle:metadata=>{
    lifecycleCalls++;expect(metadata).toEqual([row()]);expect(JSON.stringify(metadata)).not.toContain(record.text)
    ;(metadata[0] as MemoryRecall).digest='sha256:'+'0'.repeat(64)
    retained=[{...update}];return retained
   }})
   const before=textCalls
   const resumed=await contextualSession(session(checkpoint,enabled))
   retained[0]!.state='expired' as 'revoked'
   expect(lifecycleCalls).toBe(1);expect(textCalls-before).toBe(enabled?1:0)
   expect(resumed.context.system('guidance')).not.toContain(record.text)
   expect(resumed.context.execution.state.memories!.recalled).toEqual([row('revoked')])
   expect(resumed.context.takeNotices().filter(e=>e.type==='memory_recalled')).toEqual([])
   const again=readExecution(JSON.parse(JSON.stringify(resumed.context.execution.snapshot('stopped'))))!;resumed.context.close()
   configureHarness({memoryLifecycle:()=>undefined,memoryFor:()=>[]})
   const reloaded=await contextualSession(session(again,false));expect(reloaded.context.execution.state.memories!.recalled).toEqual([row('revoked')]);reloaded.context.close()
  }
 })
 it('reports lifecycle even with text callbacks that must never run and keeps low-level disabled callback behavior',async()=>{
  let captured=0,life=0
  configureHarness({memoryFor:()=>{throw Error('disabled text read')},memoryLifecycle:()=>{life++;return []}})
  expect(await readHostParts('q',CONTEXT_DEFAULTS,false,'marker',()=>{captured++})).toEqual({})
  expect(captured).toBe(1);expect(life).toBe(0)
  const run=await contextualSession(session(undefined,false));expect(life).toBe(1);run.context.close()
 })
 it('emits canonical configuration failures with no request/success event for malformed lifecycle answers',async()=>{
  for(const answer of ['', [{}],Array.from({length:257},(_,i)=>({...update,id:String(i)})),[update]]){
   configureHarness({memoryLifecycle:()=>answer as never})
   const run=await vercelTurn({answer:()=>streamed(sseTurn({text:'Done.'}))})
   expect(run.requests).toEqual([])
   expect(run.events.filter(e=>e.type==='memory_recalled')).toEqual([])
   const error=run.events.find(e=>e.type==='error')!
   expect(error.type==='error'&&readFailure(error.failure)).toMatchObject({code:'configuration',outcome:'not-started',source:'context'})
  }
 })
 it('consumes shared alias refusals on presence, including false/empty, beside canonical spellings',async()=>{
  for(const input of aliases.cases){
   if(input.alias==='critic')await expect(contextualSession({...session(),adversarialReview:input.value as boolean})).rejects.toThrow('use critic')
   if(input.alias==='memory')await expect(recallMemory(input.value,4000,'marker')).rejects.toThrow(input.replacement)
   if(input.alias==='budget')expect(()=>resolvePolicy({}, {}, {readBytes:input.value,budget:{readBytes:512},models:{author:{model:'fixture'}}}, {connections:['fixture'],agent:false,requests:2})).toThrow(input.replacement)
  }
  for(const value of [null,undefined])await expect(recallMemory(value,4000,'marker')).resolves.toMatchObject({rows:[],bytes:0})
  for(const value of [null,undefined,''])await expect(contextualSession({...session(),adversarialReview:value as never})).rejects.toThrow('use critic')
 })
})
