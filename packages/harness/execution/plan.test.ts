import {describe,it,expect,vi} from 'vitest'
import checkpoints from '../../../contract/vectors/plan/checkpoints.json'
import events from '../../../contract/vectors/plan/events.json'
import {identifyPlan,revisePlan,planDigest,validPlanState} from './plan'
import {readExecution,type TaskStep} from './state'
import {readRunEvent} from '../recordReader'
import {contextualSession} from '../context/session'
import {managedContext} from '../context/managed'
import {canonicalJSON} from '../context/chat'
import {MemoryRunStore} from '../storage/run'
import {durableOwner,durableSession,readStoredRunEvent} from '../storage/writer'
import type {AssistantSession,AssistantEvent} from '../engine'
const fixture=checkpoints.items
const input=(store?:MemoryRunStore,resume=false):AssistantSession=>({prompt:'Work',testPrompt:'',allowConversation:true,interactive:true,tools:[],hostTools:[],callTool:vi.fn(async()=>({content:[]})),model:{family:'openai-compatible',model:'fixture',call:vi.fn(async()=>new Response('{}'))},signal:new AbortController().signal,context:{currentRequest:{message:'Work',input:'Work'},...(store?{runStore:{store,id:'plan-execution',...(resume?{resume:true}:{})}}:{})}})
const steps=(at:number)=>structuredClone(fixture[at]!.plan) as TaskStep[]
const identified=(at:number)=>({plan:steps(at),planState:structuredClone(fixture[at]!.planState) as any})
describe('task plan identity and revision contract',()=>{
 it('reads every shared checkpoint/event and computes the handwritten canonical diffs',()=>{
  for(const row of fixture)expect(readExecution(row)).toEqual(row)
  for(const row of events.items)expect(readRunEvent(row)).toEqual(row)
  expect(identifyPlan([])).toEqual(identified(0))
  const first=revisePlan(identified(0),steps(1).map(({id,...s})=>s),'Track the work')
  expect(first).toEqual(identified(1))
  const second=revisePlan(first,[steps(2)[0]!,{step:'Report',status:'pending'}],'Narrow verification and report')
  expect(second).toEqual(identified(2))
  expect(revisePlan(second,steps(2),'Content unchanged')).toEqual(identified(3))
  expect(fixture[2]!.planState.revision).toBe(fixture[3]!.planState.revision)
 })
 it('rejects unknown, duplicate and removed ids without mutating any state',()=>{
  for(const plan of [[{...steps(1)[0]!,id:'step-99'}],[steps(1)[0]!,steps(1)[0]!],[{...steps(1)[0]!}]]){
   const current=identified(plan[0]!.id==='step-1'&&plan.length===1?2:1),before=canonicalJSON(current)
   expect(()=>revisePlan(current,plan,'Invalid identity')).toThrow(/PlanIdentityInvalid/)
   expect(canonicalJSON(current)).toBe(before)
  }
 })
 it('keeps the allocator high water and explicit discarded history through bounded no-op revisions',()=>{
  let state=identified(2)
  for(let i=0;i<35;i++)state=revisePlan(state,state.plan,'No content change')
  expect(state.planState).toMatchObject({sequence:37,nextId:4,discarded:5})
  expect(state.planState.history).toHaveLength(32);expect(validPlanState(state.plan,state.planState)).toBe(true)
  expect(state.planState.revision).toBe(fixture[2]!.planState.revision)
  const next=revisePlan(state,[{step:'New identity',status:'pending'}],'Replace')
  expect(next.plan[0]!.id).toBe('step-4')
 })
 it('refuses a diff overflow before mutation and retains forty-change revisions',()=>{
  const current=identifyPlan(Array.from({length:20},(_,i)=>({step:`Item ${i}`,status:'pending' as const})))
  const proposed=current.plan.map((s,i)=>({...s,step:'Changed '+i,status:'completed' as const})).reverse()
  const before=canonicalJSON(current)
  expect(()=>revisePlan(current,proposed,'Too many changes')).toThrow(/PlanRevisionTooLarge/)
  expect(canonicalJSON(current)).toBe(before)
  expect(revisePlan(current,Array.from({length:20},(_,i)=>({step:`New ${i}`,status:'pending' as const})),'Replace all').planState.history[0]!.diff).toHaveLength(40)
 })
 it('refuses malformed semantic history, digest, allocator and id states',()=>{
  for(const change of [(s:any)=>s.planState.revision='sha256:'+'b'.repeat(64),(s:any)=>s.planState.nextId=2,(s:any)=>s.planState.history[1].previous='sha256:'+'b'.repeat(64),(s:any)=>s.planState.discarded=1,(s:any)=>s.plan[0].id='step-3']){
   const held=structuredClone(fixture[2]);change(held);expect(()=>readExecution(held)).toThrow()
  }
 })
 it('keeps historical bytes on read and assigns deterministic ids at next controller write',async()=>{
  const first=await contextualSession(input()),saved=first.context.execution.snapshot('stopped')
  delete saved.planState;saved.plan=[{step:'Legacy',status:'completed'}];saved.version=1;delete saved.schema;delete saved.digest;delete saved.retainUntil;delete saved.records
  const before=canonicalJSON(saved);expect(readExecution(saved)).toEqual(saved)
  const next=await contextualSession({...input(),context:{...input().context,execution:saved}})
  expect(canonicalJSON(saved)).toBe(before)
  expect(next.context.execution.state.plan).toEqual([{id:'step-1',step:'Legacy',status:'completed'}])
  const snapshot=next.context.execution.snapshot('stopped'),again=await contextualSession({...input(),context:{...input().context,execution:snapshot}})
  expect(again.context.execution.state.planState).toEqual(snapshot.planState)
  first.context.close();next.context.close();again.context.close()
 })
 it('commits plan checkpoint and event before returning and consumes only its private delivery identity',async()=>{
  const store=new MemoryRunStore(),bound=await durableSession(input(store)),ready=await contextualSession(bound),tool=ready.session.values.hostTools.find(t=>t.name==='update_plan')!
  await tool.execute({explanation:'Track the work',plan:steps(1).map(({id,...s})=>s)},ready.session.signal)
  const head=(await store.load('plan-execution'))!,page=await store.readEvents('plan-execution',null,100),notice=ready.context.takeNotices().find(e=>e.type==='plan_revised')!
  expect(readExecution(JSON.parse(new TextDecoder().decode(head.checkpoint!)))!.planState!.sequence).toBe(1)
  expect(page.items.map(i=>readStoredRunEvent(i.bytes).event.type)).toContain('plan_revised')
  const binding=bound.context![durableOwner]!
  await binding.writer.event(binding,notice)
  expect((await store.load('plan-execution'))!.revision).toBe(head.revision)
  await binding.writer.event(binding,notice)
  expect((await store.load('plan-execution'))!.revision).not.toBe(head.revision)
  ready.context.close()
 })
 it('does not grant delivery authority to a copy or another invocation, and persists a fresh child checkpoint',async()=>{
  const store=new MemoryRunStore(),bound=await durableSession(input(store)),ready=await contextualSession(bound),binding=bound.context![durableOwner]!
  const event={...events.items[0],execution:'child-plan'} as AssistantEvent
  const child={...binding,scope:{execution:'child-plan',role:'author',task:'child-plan'},root:false}
  const state={...ready.context.execution.snapshot(),id:'child-plan',...identified(1)}
  const committed=await binding.writer.commitPlan(child,state,event as any)
  const head=(await store.load('plan-execution'))!,cp=JSON.parse(new TextDecoder().decode(head.checkpoint!))
  expect(cp.durable.children[0].checkpoint.planState.sequence).toBe(1)
  await binding.writer.event(child,structuredClone(committed));expect((await store.load('plan-execution'))!.revision).not.toBe(head.revision)
  const before=(await store.load('plan-execution'))!.revision
  await binding.writer.event(child,committed);expect((await store.load('plan-execution'))!.revision).toBe(before)
  ready.context.close()
 })
 it('resumes after receipt before delivery without regenerating the revision or request bytes',async()=>{
  const store=new MemoryRunStore(),first=await contextualSession(input(store)),tool=first.session.values.hostTools.find(t=>t.name==='update_plan')!
  await tool.execute({explanation:'Track the work',plan:steps(1).map(({id,...s})=>s)},first.session.signal)
  const state=first.context.execution.requestState(),saved=(await store.load('plan-execution'))!.checkpoint!
  first.context.close();const next=await contextualSession(input(store,true))
  expect(next.context.execution.state.planState!.sequence).toBe(1);expect(next.context.takeNotices().some(e=>e.type==='plan_revised')).toBe(false)
  expect(next.context.execution.state.plan).toEqual(JSON.parse(new TextDecoder().decode(saved)).plan)
  expect(next.context.execution.requestState()).toBe(state)
  next.context.close()
 })
 it('delivers one plan notice through the real managed boundary with no second log append',async()=>{
  const store=new MemoryRunStore(),delivered:AssistantEvent[]=[]
  for await(const event of managedContext(input(store),async function*(prepared){
   const tool=prepared.values.hostTools.find(t=>t.name==='update_plan')!
   await tool.execute({explanation:'Track the work',plan:steps(1).map(({id,...s})=>s)},prepared.signal)
   yield {type:'tool_result',name:'update_plan',isError:false,text:'recorded'};yield {type:'end'}
  }))delivered.push(event)
  expect(delivered.filter(e=>e.type==='plan_revised')).toHaveLength(1)
  const page=await store.readEvents('plan-execution',null,100)
  expect(page.items.map(i=>readStoredRunEvent(i.bytes).event).filter(e=>e.type==='plan_revised')).toHaveLength(1)
 })
})
