import {describe,it,expect,vi} from 'vitest'
import type {Engine,HostTool} from '../engine'
import {MemoryRunStore,type RunStore} from './run'
import {ReferenceEffectHost,fenceOf,unattendedRunStore,type LeaseLifetime} from './lease'
import {checkpointBytesDigest,type WaitLifetime} from './wait'
import type {JobLifetime} from './job'
import type {GoalCommand,GoalLifetime} from './goal'
import {goalCommand,readGoal} from './goalCommands'
import {managedContext,contextOf} from '../context/managed'
import {contextualSession} from '../context/session'
import {withDelegation} from '../delegation'
import {subagentRecord,type SubagentDeclaration} from '../subagents'
import declaration from '../../../contract/vectors/subagents/declaration.json'

/* #230 M16: a delegated required step re-verified by the writer's history check, through the package's delegation, its writer and
 * the memory store kind as a durable one. The Go twin is go/context/run_goal_task_test.go. */
const T0=Date.parse('2026-01-01T00:00:00.000Z'),HOUR=3_600_000
class DurableGoalStore extends MemoryRunStore {override readonly leaseLifetime:LeaseLifetime='durable';override readonly waitLifetime:WaitLifetime='durable';override readonly jobLifetime:JobLifetime='durable';override readonly goalLifetime:GoalLifetime='durable'}
const refusal=(reason:string)=>expect.objectContaining({name:'GoalRefused',reason})
const PLAN={id:'accepted-plan',revision:1,objective:'Inspect and report',steps:['Inspect','Report'],questions:[],references:[],status:'accepted' as const,createdAt:'2026-01-01T00:00:00.000Z',acceptedAt:'2026-01-01T00:00:00.000Z'}
const LOOKUP:HostTool={name:'lookup',effect:'read',taxonomy:{operation:'read',effect:'read'},description:'Read supplied evidence.',inputSchema:{type:'object'},execute:async()=>({content:[{type:'text',text:'Evidence.'}]})}
let ids=0
const lastEntry=async(store:RunStore,resource:string)=>(await readGoal(store,resource))!.journal.at(-1)!
vi.setConfig({testTimeout:120_000})
describe('M16: a delegated required step is re-verified by the writer\'s history check (#230)',()=>{
 const response=()=>new Response(JSON.stringify({id:'answer',object:'chat.completion',model:'fixture',choices:[{index:0,message:{role:'assistant',content:'Done.'},finish_reason:'stop'}],usage:{prompt_tokens:2,completion_tokens:1}}),{headers:{'content-type':'application/json'}})
 const RUN_T='task-goal-230'
 /** A run whose accepted Plan's two steps were delegated, whose children completed, and whose objective bound both steps
  * before the model closed them with the children's evidence. */
 async function delegated(){
  const clock={at:T0},store=new DurableGoalStore({now:()=>new Date(clock.at)}),host=new ReferenceEffectHost(store)
  const fence=fenceOf(await store.acquireLease({resource:RUN_T,owner:'worker-a',expectedRevision:null,durationMs:HOUR})),view=unattendedRunStore({store,fence,effectHost:host})
  const call=vi.fn(async()=>response())
  const base=(resume=false)=>({prompt:'Work',testPrompt:'',allowConversation:true,interactive:true,critic:'off' as const,tools:[],callTool:vi.fn(),model:{family:'openai-compatible' as const,model:'fixture',call},signal:new AbortController().signal,subagents:declaration.items as SubagentDeclaration[],hostTools:[LOOKUP],context:{taskSteps:true,requestCap:4,currentRequest:{message:'Work',input:'Work'},plan:{...PLAN,steps:['Inspect first source','Inspect second source']},runStore:{store:view,id:RUN_T,...(resume?{resume:true}:{})}}})
  const engine:Engine={id:'vercel',start:input=>managedContext(input,async function*(request){
   const context=contextOf(request),tools=request.values.hostTools
   if(!('model'in request.values))throw Error('model fixture expected')
   await request.values.model.call('',{method:'POST',body:JSON.stringify({model:'fixture',messages:[{role:'user',content:input.prompt}]})})
   if(input.context?.objective){await tools.find(tool=>tool.name==='update_plan')!.execute({explanation:'Track child work.',plan:[{step:'Inspect supplied evidence',status:'pending'}]},request.signal);yield {type:'message',text:'Evidence checked.'};yield {type:'end'};return}
   const pin=subagentRecord(context.subagents[0]!),plan=context.execution.state.plan
   await tools.find(tool=>tool.name==='update_plan')!.execute({explanation:'Inspect independent sources.',plan:plan.map(step=>({id:step.id,step:step.step,status:'pending',budget:{requests:1},delegation:pin}))},request.signal)
   await tools.find(tool=>tool.name==='delegate_plan_steps')!.execute({planRevision:context.execution.state.planState!.revision,steps:['step-1','step-2']},request.signal)
  },'vercel')}
  const events=[];for await(const event of withDelegation(engine).start({...base(),policy:{concurrency:{children:2,requests:2}}} as never))events.push(event)
  expect(events.filter(event=>event.type==='error')).toEqual([])
  const head=async()=>{const held=(await store.load(RUN_T))!;return {revision:held.revision,digest:checkpointBytesDigest(held.checkpoint!)}}
  const cmd=(kind:GoalCommand['kind'],expectedRevision:number|null,over:Partial<GoalCommand>={}):GoalCommand=>({version:1,resource:RUN_T,command:`m16-${++ids}`,actor:'person-1',expectedRevision,kind,...over})
  const created=await goalCommand({store,command:cmd('create',null,{objective:'objective-m16'})})
  const bound=await goalCommand({store,command:cmd('bind',created.goal.revision,{steps:['step-1','step-2'],head:await head()})})
  const resumed=await contextualSession({...base(true),policy:{concurrency:{children:2,requests:2}}} as unknown as Parameters<typeof contextualSession>[0])
  try{
   const proof=resumed.context.execution.proof?.()?.children!
   expect(proof).toHaveLength(2)
   const complete=resumed.session.values.hostTools.find(tool=>tool.name==='complete_plan_step')!
   for(const [i,row] of proof.entries()){const {step:_step,planRevision:_revision,...child}=row;await complete.execute({id:`step-${i+1}`,expectedRevision:resumed.context.execution.state.planState!.revision,outcome:'completed',evidence:{operations:[],artifacts:[],child}},resumed.session.signal)}
  }finally{resumed.context.close()}
  return {store,fence,head,cmd,bound}
 }
 it('a delegated step proven by its history transaction is achieved; without history, or with evidence the history does not hold, it is refused',async()=>{
  const held=await delegated(),revision=held.bound.goal.revision
  // A store that hides its history: the steps cannot be re-verified, and the store says so.
  const hidden=new Proxy(held.store,{get:(target,name)=>{if(name==='historyVersion')return undefined;const value=Reflect.get(target,name,target);return typeof value==='function'?value.bind(target):value}})
  await expect(goalCommand({store:hidden as never,command:held.cmd('achieve',revision,{head:await held.head()})})).rejects.toEqual(refusal('history-unsupported'))
  expect(await lastEntry(held.store,RUN_T)).toMatchObject({outcome:'history-unsupported',step:'step-1'})
  // A history that no longer holds what recorded the children: the step is not proven.
  const altered=new Proxy(held.store,{get:(target,name)=>{
   if(name==='readHistoryObject')return async(...args:Parameters<typeof target.readHistoryObject>)=>{const page=await target.readHistoryObject(...args);return {...page,value:new Uint8Array(page.value).map((byte,i)=>i===0?byte^1:byte)}}
   const value=Reflect.get(target,name,target);return typeof value==='function'?value.bind(target):value}})
  await expect(goalCommand({store:altered as never,command:held.cmd('achieve',revision,{head:await held.head()})})).rejects.toEqual(refusal('evidence-refused'))
  expect(await lastEntry(held.store,RUN_T)).toMatchObject({outcome:'evidence-refused',step:'step-1'})
  // The honest head: both steps are proven by the transactions that recorded their children.
  const done=await goalCommand({store:held.store,command:held.cmd('achieve',revision,{head:await held.head()})})
  expect(done.goal.objectives[0]!.state).toBe('achieved')
 })
})
