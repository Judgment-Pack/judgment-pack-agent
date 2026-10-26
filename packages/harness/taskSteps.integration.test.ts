import {vercel} from './engines/vercel'
import nativeDeclaration from '../../contract/testdata/task-steps/native-subagent.json'
import {codex} from './engines/codex'
import {managedContext,contextOf} from './context/managed'
import {withDelegation} from './delegation'
import type {Engine} from './engine'
import {MemoryChatStore} from './storage/chat'
import {planTools,acceptPlan,currentChat} from './planning/tools'
import type {ChatPlan} from './planning/model'
import {durableSession,durableOwner,normalizeTaskDelegation,childDurability} from './storage/writer'
import declaration from '../../contract/vectors/subagents/declaration.json'
import {subagentRecord,type SubagentDeclaration} from './subagents'
import type {DelegatedTask,DelegationRequest} from './delegationContract'
import {describe,it,expect,vi} from 'vitest'
import {contextualSession} from './context/session'
import {MemoryRunStore,type RunStore} from './storage/run'
import {readExecution,ExecutionPaused} from './execution/state'
import type {AssistantSession,AgentAssistantSession} from './engine'
import type {TaskStepEnvelope} from './taskSteps'
import type {AgentRun} from './agent'
const response=()=>new Response(JSON.stringify({id:'answer',object:'chat.completion',model:'fixture',choices:[{index:0,message:{role:'assistant',content:'Done.'},finish_reason:'stop'}],usage:{prompt_tokens:2,completion_tokens:1}}),{headers:{'content-type':'application/json'}})
const input=(store:RunStore,call:AssistantSession['model']['call'],resume=false):AssistantSession=>({prompt:'Work',testPrompt:'',allowConversation:true,interactive:true,critic:'off',tools:[],hostTools:[],callTool:vi.fn(),model:{family:'openai-compatible',model:'fixture',call},signal:new AbortController().signal,context:{taskSteps:true,requestCap:4,currentRequest:{message:'Work',input:'Work'},runStore:{store,id:'task-steps-183',...(resume?{resume:true}:{})}}})
const saved=async(store:MemoryRunStore)=>JSON.parse(new TextDecoder().decode((await store.load('task-steps-183'))!.checkpoint!))
const model=(ready:Awaited<ReturnType<typeof contextualSession>>)=>{if(!('model'in ready.session.values))throw Error('model expected');return ready.session.values.model}
const send=(ready:Awaited<ReturnType<typeof contextualSession>>)=>model(ready).call('',{method:'POST',body:JSON.stringify({model:'fixture',messages:[{role:'user',content:'Work'}]})})
const update=(ready:Awaited<ReturnType<typeof contextualSession>>,plan:unknown[])=>ready.session.values.hostTools.find(tool=>tool.name==='update_plan')!.execute({plan,explanation:'Work in order.'},ready.session.signal)
describe('#183 original writer task-step admission',()=>{
 it('refuses cycles and unready starts before changing the acknowledged plan',async()=>{
  const store=new MemoryRunStore(),call=vi.fn(async()=>response()),ready=await contextualSession(input(store,call))
  try{
   await update(ready,[{step:'First',status:'pending'},{step:'Next',status:'pending'}])
   const before=await saved(store),plan=ready.context.execution.state.plan
   await expect(update(ready,plan.map((step,i)=>({...step,dependsOn:[plan[1-i]!.id!]})))).rejects.toThrow('dependency-cycle')
   expect((await saved(store)).planState.revision).toBe(before.planState.revision)
   await expect(update(ready,[plan[0]!,{...plan[1]!,status:'in_progress',dependsOn:[plan[0]!.id!]}])).rejects.toThrow('dependencies-unready')
   expect(ready.context.execution.state.plan).toEqual(before.plan);expect(call).not.toHaveBeenCalled()
  }finally{ready.context.close()}
 })
 it('acknowledges root and step charge together, pauses at the share and retains it across mid-step resume',async()=>{
  const store=new MemoryRunStore(),call=vi.fn(async()=>response()),raw=input(store,call),ready=await contextualSession(raw)
  try{
   await update(ready,[{step:'First',status:'in_progress',budget:{requests:1}},{step:'Next',status:'pending',budget:{requests:2}}])
   await send(ready)
   const first=await saved(store),row=first.planState.taskSchedule.rows[0],dispatch=first.durable.dispatches.find((d:{kind:string})=>d.kind==='model')
   expect(first.used).toBe(1);expect(row.claims).toEqual([{id:dispatch.id,requestDigest:dispatch.requestDigest,state:'held'}]);expect(dispatch.state).toBe('result')
   await expect(send(ready)).rejects.toBeInstanceOf(ExecutionPaused)
   expect(call).toHaveBeenCalledOnce();expect((await saved(store)).used).toBe(1)
   expect(readExecution(first)?.planState?.taskSchedule).toEqual(first.planState.taskSchedule)
  }finally{ready.context.close()}
  const resumed=await contextualSession(input(store,call,true))
  try{expect(resumed.context.execution.state.planState!.taskSchedule!.rows[0]!.claims).toHaveLength(1);await expect(send(resumed)).rejects.toBeInstanceOf(ExecutionPaused);expect(call).toHaveBeenCalledOnce()}finally{resumed.context.close()}
 })
 it('keeps a lost claim acknowledgement held and never sends it on explicit resume',async()=>{
  const memory=new MemoryRunStore();let lose=true
  const store:RunStore={durability:'memory',load:memory.load.bind(memory),readEvents:memory.readEvents.bind(memory),commit:async(value,signal)=>{const receipt=await memory.commit(value,signal);const state=JSON.parse(new TextDecoder().decode(value.checkpoint!));if(lose&&state.durable?.dispatches.some((d:{kind:string;state:string})=>d.kind==='model'&&d.state==='claimed')){lose=false;throw Error('fixture lost claim acknowledgement')};return receipt}}
  const call=vi.fn(async()=>response()),ready=await contextualSession(input(store,call))
  try{await update(ready,[{step:'First',status:'in_progress',budget:{requests:1}}]);await expect(send(ready)).rejects.toThrow();expect(call).not.toHaveBeenCalled()}finally{ready.context.close()}
  const retained=await saved(memory);expect(retained.used).toBe(1);expect(retained.planState.taskSchedule.rows[0].claims).toHaveLength(1)
  const resumed=await contextualSession(input(store,call,true))
  try{await expect(send(resumed)).rejects.toThrow('HostOutcomeUnknown');expect(call).not.toHaveBeenCalled();expect((await saved(memory)).used).toBe(1)}finally{resumed.context.close()}
 })
})

const envelope:TaskStepEnvelope={id:'host-envelope',revision:'sha256:'+'a'.repeat(64),unit:'provider-invocations',limit:2}
const nativeInput=(store:RunStore,run:AgentRun,configuration?:TaskStepEnvelope,resume=false):AgentAssistantSession=>({prompt:'Work',testPrompt:'',allowConversation:true,interactive:true,critic:'off',tools:[],hostTools:[],callTool:vi.fn(),agent:{model:'native',run},signal:new AbortController().signal,context:{taskSteps:true,...(configuration?{taskStepEnvelope:configuration}:{}),currentRequest:{message:'Work',input:'Work'},runStore:{store,id:'task-steps-183',...(resume?{resume:true}:{})}}})
const nativeSend=async(ready:Awaited<ReturnType<typeof contextualSession>>)=>{if(!('agent'in ready.session.values))throw Error('native expected');return ready.session.values.agent.run({prompt:'Work',instructions:'Answer.',tools:[],phase:'author'},{signal:ready.session.signal,event:async()=>{},tool:async()=>({text:'',isError:false})})}
describe('#183 explicit standalone native host envelope',()=>{
 it('counts pre-plan and held physical identities once under an immutable host configuration',async()=>{
  const store=new MemoryRunStore(),run=vi.fn<AgentRun>(async()=>{}),ready=await contextualSession(nativeInput(store,run,envelope),'codex')
  try{
   await nativeSend(ready)
   const initial=await saved(store);expect(initial.limit).toBe(1);expect(initial.used).toBe(0);expect(initial.planState.taskEnvelope.configuration).toEqual(envelope)
   await update(ready,[{step:'Finish',status:'in_progress',budget:{requests:1}}]);await nativeSend(ready)
   await expect(nativeSend(ready)).rejects.toBeInstanceOf(ExecutionPaused)
   const state=await saved(store),claims=state.planState.taskSchedule.rows[0].claims,physical=state.durable.dispatches.filter((d:{kind:string})=>d.kind==='agent')
   expect(run).toHaveBeenCalledTimes(2);expect(physical).toHaveLength(2);expect(claims).toHaveLength(1);expect(claims[0].id).toBe(physical[1].id)
   expect(state.usage.rows.find((row:{source:string})=>row.source==='agent').invocations).toBe(2)
  }finally{ready.context.close()}
  for(const config of [undefined,{...envelope,limit:3},{...envelope,id:'foreign'},{...envelope,revision:'sha256:'+'b'.repeat(64)}])await expect(contextualSession(nativeInput(store,run,config,true),'codex')).rejects.toThrow('ContinuationIdentityChanged')
  const resumed=await contextualSession(nativeInput(store,run,envelope,true),'codex');try{await expect(nativeSend(resumed)).rejects.toBeInstanceOf(ExecutionPaused);expect(run).toHaveBeenCalledTimes(2)}finally{resumed.context.close()}
 })
 it('adds no native ceiling without an envelope and refuses optional budget configuration',async()=>{
  const store=new MemoryRunStore(),run=vi.fn<AgentRun>(async()=>{}),ready=await contextualSession(nativeInput(store,run),'codex')
  try{
   await expect(update(ready,[{step:'Funded step',status:'in_progress',budget:{requests:1}}])).rejects.toThrow()
   expect(ready.context.execution.state.plan).toEqual([])
   await update(ready,[{step:'Dependency work',status:'in_progress'}])
   for(let i=0;i<6;i++)await nativeSend(ready)
   expect(run).toHaveBeenCalledTimes(6);expect((await saved(store)).planState.taskEnvelope).toBeUndefined();expect((await saved(store)).planState.taskSchedule.rows[0].claims).toHaveLength(6)
  }finally{ready.context.close()}
 })
 for(const invalid of [{...envelope,limit:0},{...envelope,limit:101},{...envelope,limit:1.5},{...envelope,available:2}])it('refuses malformed host configuration before native work',async()=>{
  const run=vi.fn<AgentRun>(async()=>{});await expect(contextualSession(nativeInput(new MemoryRunStore(),run,invalid as TaskStepEnvelope),'codex')).rejects.toThrow();expect(run).not.toHaveBeenCalled()
 })
})

const packageInput=(store:RunStore,call:AssistantSession['model']['call'])=>({...input(store,call),subagents:declaration.items as SubagentDeclaration[],hostTools:[{name:'lookup',effect:'read' as const,taxonomy:{operation:'read' as const,effect:'read' as const},description:'Read supplied evidence.',inputSchema:{type:'object'},execute:async()=>({content:[{type:'text' as const,text:'Evidence.'}]})}]})
describe('#183 exact source-owned delegation action',()=>{
 it('validates the whole batch before binding actual child IDs in the original store',async()=>{
  const store=new MemoryRunStore(),call=vi.fn(async()=>response()),raw=await durableSession(packageInput(store,call)),binding=raw.context![durableOwner]!
  const started:DelegatedTask[][]=[]
  const action=binding.writer.taskDelegationTool(binding,async(request)=>{
   const tasks:DelegatedTask[]=request.tasks.map(task=>({...task,id:crypto.randomUUID(),parentRunId:crypto.randomUUID(),state:'queued',createdAt:new Date().toISOString(),modelRequests:0,toolCalls:0,attempts:0,steps:[]}))
   await binding.writer.bindTaskDelegation(binding,request,tasks,raw.signal);started.push(tasks)
   return {content:[{type:'text',text:'Children admitted.'}]}
  })
  const ready=await contextualSession({...raw,hostTools:[...raw.hostTools,action]})
  try{
   const pin=subagentRecord(ready.context.subagents[0]!)
   await update(ready,[{step:'Inspect first',status:'pending',delegation:pin,budget:{requests:1}},{step:'Inspect next',status:'pending',delegation:pin,budget:{requests:1},dependsOn:['step-1']}])
   const revision=ready.context.execution.state.planState!.revision,offered=ready.session.values.hostTools.find(tool=>tool.name==='delegate_plan_steps')!
   for(const args of [{planRevision:revision,steps:[]},{planRevision:revision,steps:['step-1','step-1']},{planRevision:'sha256:'+'f'.repeat(64),steps:['step-1']},{planRevision:revision,steps:['step-1','step-2']}])await expect(offered.execute(args,raw.signal)).rejects.toThrow()
   expect(started).toHaveLength(0);expect((await saved(store)).planState.taskSchedule.pending).toBeUndefined()
   await offered.execute({planRevision:revision,steps:['step-1']},raw.signal)
   const state=await saved(store),row=state.planState.taskSchedule.rows[0]
   expect(started).toHaveLength(1);expect(row.child).toEqual({task:started[0]![0]!.id,execution:started[0]![0]!.id,planRevision:revision,profile:pin});expect(state.planState.taskSchedule.pending).toBeUndefined();expect(row.claims).toEqual([]);expect(call).not.toHaveBeenCalled()
   expect(normalizeTaskDelegation({...action},binding,{planRevision:revision,steps:['step-2']})).toBeUndefined()
   await expect(offered.execute({planRevision:revision,steps:['step-1']},raw.signal)).rejects.toThrow()
  }finally{ready.context.close()}
 })
 it('retains an interrupted pending selection and refuses redispatch or graph reset on resume',async()=>{
  const store=new MemoryRunStore(),raw=await durableSession(packageInput(store,vi.fn(async()=>response()))),binding=raw.context![durableOwner]!
  const action=binding.writer.taskDelegationTool(binding,async()=>({content:[]})),ready=await contextualSession({...raw,hostTools:[...raw.hostTools,action]})
  let request:DelegationRequest
  try{
   await update(ready,[{step:'Inspect',status:'pending',delegation:subagentRecord(ready.context.subagents[0]!)}])
   request=await normalizeTaskDelegation(ready.session.values.hostTools.find(tool=>tool.name==='delegate_plan_steps')!,binding,{planRevision:ready.context.execution.state.planState!.revision,steps:['step-1']},raw.signal)!
   expect((await saved(store)).planState.taskSchedule.pending.steps).toEqual(['step-1'])
  }finally{ready.context.close()}
  const resumeInput=packageInput(store,vi.fn(async()=>response()));resumeInput.context!.runStore!.resume=true
  const rebound=await durableSession(resumeInput),resumed=await contextualSession(rebound)
  try{
   const owner=rebound.context![durableOwner]!
   const before=await saved(store)
   await expect(owner.writer.prepareTaskDelegation(owner,{planRevision:before.planState.revision,steps:['step-1']})).rejects.toThrow('delegation-outcome-unknown')
   await expect(update(resumed,resumed.context.execution.state.plan.map(step=>({...step,detail:'Changed while pending.'})))).rejects.toThrow('delegation-pending')
   expect((await saved(store)).planState.taskSchedule.pending).toEqual(before.planState.taskSchedule.pending)
   expect(request!.tasks[0]!.objective).toBe('Inspect')
  }finally{resumed.context.close()}
 })
})

describe('#183 coordinator and stored chat consumers',()=>{
 it('runs independent ready package children concurrently and restores exact acknowledged evidence for separate completion',async()=>{
  const store=new MemoryRunStore();let active=0,peak=0,calls=0,release:()=>void=()=>{}
  const together=new Promise<void>(resolve=>{release=resolve})
  const call=vi.fn(async()=>{calls++;active++;peak=Math.max(peak,active);let timer:ReturnType<typeof setTimeout>|undefined;try{if(calls>1){if(calls===3)release();await Promise.race([together,new Promise<never>((_,reject)=>{timer=setTimeout(()=>reject(Error('The second ready child was not scheduled under concurrency two.')),4000)})])}return response()}finally{if(timer)clearTimeout(timer);active--}})
  let answered:unknown
  const engine:Engine={id:'vercel',start:input=>managedContext(input,async function*(request){
   const context=contextOf(request),tools=request.values.hostTools
   if(!('model'in request.values))throw Error('model fixture expected')
   await request.values.model.call('',{method:'POST',body:JSON.stringify({model:'fixture',messages:[{role:'user',content:input.prompt}]})})
   if(input.context?.objective){await tools.find(tool=>tool.name==='update_plan')!.execute({explanation:'Track child work.',plan:[{step:'Inspect supplied evidence',status:'pending'}]},request.signal);yield {type:'message',text:'Evidence checked.'};yield {type:'end'};return}
   const pin=subagentRecord(context.subagents[0]!)
   await tools.find(tool=>tool.name==='update_plan')!.execute({explanation:'Inspect independent sources.',plan:[{step:'Inspect first source',status:'pending',budget:{requests:1},delegation:pin},{step:'Inspect second source',status:'pending',budget:{requests:1},delegation:pin}]},request.signal)
   answered=await tools.find(tool=>tool.name==='delegate_plan_steps')!.execute({planRevision:context.execution.state.planState!.revision,steps:['step-1','step-2']},request.signal)
   // The host interrupted after the acknowledged child results, before asking for completion.
  },'vercel')}
  const admitted={...packageInput(store,call),policy:{concurrency:{children:2,requests:2}}};const events=[];for await(const event of withDelegation(engine).start(admitted))events.push(event)
  expect(events.filter(event=>event.type==='error')).toEqual([])
  expect(answered).toBeDefined();expect(calls).toBe(3);expect(peak).toBe(2)
  const state=await saved(store),rows=state.planState.taskSchedule.rows
  expect(state.used).toBe(3);expect(state.plan.map((step:{status:string})=>step.status)).toEqual(['pending','pending'])
  expect(rows.every((row:{claims:unknown[];child?:{result?:unknown}})=>row.claims.length===1&&!!row.child?.result)).toBe(true)
  const raw={...packageInput(store,call),policy:{concurrency:{children:2,requests:2}}};raw.context!.runStore!.resume=true
  const resumed=await contextualSession(raw)
  try{
   const proof=resumed.context.execution.proof?.()?.children!;expect(proof).toHaveLength(2)
   const complete=resumed.session.values.hostTools.find(tool=>tool.name==='complete_plan_step')!
   const evidence=(index:number)=>{const {step:_step,planRevision:_revision,...child}=proof[index]!;return {operations:[],artifacts:[],child}}
   await expect(complete.execute({id:'step-1',expectedRevision:resumed.context.execution.state.planState!.revision,outcome:'completed',evidence:{...evidence(0),child:{...evidence(0).child,task:crypto.randomUUID()}}},resumed.session.signal)).rejects.toThrow('wrong-scope')
   await expect(complete.execute({id:'step-1',expectedRevision:'sha256:'+'f'.repeat(64),outcome:'completed',evidence:evidence(0)},resumed.session.signal)).rejects.toThrow('revision-changed')
   for(let i=0;i<2;i++)await complete.execute({id:'step-'+(i+1),expectedRevision:resumed.context.execution.state.planState!.revision,outcome:'completed',evidence:evidence(i)},resumed.session.signal)
   expect((await saved(store)).plan.map((step:{status:string})=>step.status)).toEqual(['completed','completed']);expect(calls).toBe(3)
  }finally{resumed.context.close()}
 })
 it('uses the accepted plan from the real ChatStore without letting plan acceptance grant dispatch',async()=>{
  const chatStore=new MemoryChatStore([{chat:{id:'chat-task183',interaction:'plan',plans:[]}}]),signal=new AbortController().signal
  const resolver={resolve:async()=>{throw Error('No references in fixture')}}
  await planTools(chatStore,'chat-task183',resolver)[0]!.execute({objective:'Inspect and summarize.',steps:['Inspect source','Summarize evidence'],questions:[],references:[]},signal)
  const chat=(await currentChat(chatStore,'chat-task183'))!,proposal=chat.plans!.at(-1)!
  expect(proposal.status).toBe('proposed')
  await acceptPlan(chatStore,chat,proposal,signal,resolver)
  const accepted=(await currentChat(chatStore,'chat-task183'))!.plans!.at(-1)! as ChatPlan
  const store=new MemoryRunStore(),call=vi.fn(async()=>response()),raw=input(store,call);raw.context={...raw.context,phase:'work',plan:accepted}
  const ready=await contextualSession(raw)
  try{
   expect(call).not.toHaveBeenCalled();expect(ready.context.execution.state.plan).toHaveLength(2)
   const plan=ready.context.execution.state.plan
   await update(ready,plan.map((step,index)=>({id:step.id,step:step.step,status:index===0?'in_progress':'pending',budget:{requests:1},...(index?{dependsOn:[plan[0]!.id!]}:{})})))
   await send(ready);await expect(send(ready)).rejects.toBeInstanceOf(ExecutionPaused)
   const stored=await currentChat(chatStore,'chat-task183');expect(stored!.plans!.at(-1)!.status).toBe('accepted');expect(call).toHaveBeenCalledOnce()
   expect((await saved(store)).planState.accepted.id).toBe(accepted.id)
  }finally{ready.context.close()}
 })
})

it('#183 actual native coordinator intersects the explicit host ceiling and completes only from exact child evidence',async()=>{
 const store=new MemoryRunStore(),definition=nativeDeclaration,pin={id:definition.id,digest:definition.pinnedDigest,source:definition.source}
 let calls=0,active=0,peak=0
 const run:AgentRun=async(request,callbacks)=>{
  calls++;active++;peak=Math.max(peak,active)
  const invoke=async(name:string,args:Record<string,unknown>)=>{const tool=request.tools.find(tool=>tool.description.startsWith(name+':'));if(!tool)throw Error('No offered '+name);return callbacks.tool({id:'task183-'+name,name:tool.name,arguments:args},callbacks.signal)}
  try{
   if(calls===1){
    const update=await invoke('update_plan',{explanation:'Inspect independent sources.',plan:[{step:'Inspect first source',status:'pending',budget:{requests:1},delegation:pin},{step:'Inspect second source',status:'pending',budget:{requests:1},delegation:pin}]});expect(update.isError).toBe(false)
    const state=await saved(store),revision=state.planState.revision
    expect((await invoke('delegate_plan_steps',{planRevision:revision,steps:['step-1','step-1']})).isError).toBe(true)
    await invoke('delegate_plan_steps',{planRevision:revision,steps:['step-1','step-2']});return
   }
   if(calls===4)for(let i=0;i<2;i++){
    const state=await saved(store),row=state.planState.taskSchedule.rows[i],child={task:row.child.task,...row.child.result}
    expect(state.plan[i].status).toBe('pending')
    const answer=await invoke('complete_plan_step',{id:row.id,expectedRevision:state.planState.revision,outcome:'completed',evidence:{operations:[],artifacts:[],child}});expect(answer.isError).toBe(false)
   }
   await callbacks.event({type:'message',id:'answer',text:'Evidence checked.',phase:'final'})
  }finally{active--;if(callbacks.providerRelease)callbacks.providerRelease.acknowledge(callbacks.providerRelease.invocation)}
 }
 const base=packageInput(store,vi.fn(async()=>response())),{model:_model,...common}=base
 const raw:AgentAssistantSession={...common,subagents:[nativeDeclaration as SubagentDeclaration],policy:{concurrency:{children:1,requests:1}},agent:{model:'fixture',run},context:{...common.context,requestCap:undefined,taskStepEnvelope:{...envelope,limit:4}}}
 const events=[];for await(const event of withDelegation(codex).start(raw))events.push(event)
 expect(events.filter(event=>event.type==='error')).toEqual([]);expect({calls,tasks:events.filter(event=>event.type==='delegated_task').map(event=>event.type==='delegated_task'?{id:event.task.id,state:event.task.state,problem:event.task.problem}:null),pauses:events.filter(event=>event.type==='budget_paused').length}).toMatchObject({calls:4});expect(peak).toBe(1)
 const state=await saved(store);expect(state.used).toBe(0);expect(state.limit).toBe(1);expect(state.plan.map((step:{status:string})=>step.status)).toEqual(['completed','completed']);expect(state.planState.taskSchedule.rows.every((row:{claims:unknown[]})=>row.claims.length===1)).toBe(true)
},60_000)

for(const evidenceMode of ['none','all-refused','unknown-plus-success'] as const)it('#183 a complete child checkpoint alone cannot establish successful delegated completion: '+evidenceMode,async()=>{
 const store=new MemoryRunStore(),raw=await durableSession(packageInput(store,vi.fn(async()=>response()))),binding=raw.context![durableOwner]!
 let task:DelegatedTask|undefined
 const action=binding.writer.taskDelegationTool(binding,async(request)=>{
  task={...request.tasks[0]!,id:crypto.randomUUID(),parentRunId:crypto.randomUUID(),state:'queued',createdAt:new Date().toISOString(),modelRequests:0,toolCalls:0,attempts:0,steps:[]}
  await binding.writer.bindTaskDelegation(binding,request,[task],raw.signal);return {content:[]}
 })
 const ready=await contextualSession({...raw,hostTools:[...raw.hostTools,action]})
 try{
  await update(ready,[{step:'Inspect',status:'pending',budget:{requests:evidenceMode==='unknown-plus-success'?2:1},delegation:subagentRecord(ready.context.subagents[0]!)}])
  await ready.session.values.hostTools.find(tool=>tool.name==='delegate_plan_steps')!.execute({planRevision:ready.context.execution.state.planState!.revision,steps:['step-1']},raw.signal)
  let childCalls=0
  const childCall=vi.fn(async()=>{childCalls++;if(evidenceMode==='all-refused')return new Response(JSON.stringify({error:{type:'rate_limit_error'}}),{status:429,headers:{'content-type':'application/json'}});if(childCalls===2)throw Error('fixture physical outcome unknown');return response()})
  const childRaw=childDurability<AssistantSession>({...input(store,childCall),context:{objective:'Inspect',currentRequest:{message:'Inspect',input:'Inspect'}}},raw,task!.id),child=await contextualSession(childRaw)
  try{if(evidenceMode!=='none')await send(child);await childRaw.context!.checkpoint!(child.context.execution.snapshot('complete'));if(evidenceMode==='unknown-plus-success')await expect(send(child)).rejects.toThrow()}finally{child.context.close()}
  task={...task!,state:'complete',result:'Claimed completion.',finishedAt:new Date().toISOString(),report:{summary:'Claimed completion.',findings:[],citations:[],openQuestions:[],confidence:'high',notDone:[]}}
  await binding.writer.commitTaskResult(binding,{type:'delegated_task',task},raw.signal)
  expect((await saved(store)).planState.taskSchedule.rows[0].child.result).toBeUndefined()
  expect(binding.writer.taskCompletionProof(binding)).toEqual([])
  await expect(ready.session.values.hostTools.find(tool=>tool.name==='complete_plan_step')!.execute({id:'step-1',expectedRevision:ready.context.execution.state.planState!.revision,outcome:'completed',evidence:{operations:[],artifacts:[],child:{task:task.id,checkpointDigest:'sha256:'+'c'.repeat(64),resultDigest:'sha256:'+'d'.repeat(64),eventDigest:'sha256:'+'e'.repeat(64),commitId:'caller-text'}}},raw.signal)).rejects.toThrow(evidenceMode==='unknown-plus-success'?undefined:'evidence-unavailable')
 }finally{ready.context.close()}
})

function task183WireResponse(name?:string,args?:unknown):Response{
 const delta=name?{tool_calls:[{index:0,id:crypto.randomUUID(),type:'function',function:{name,arguments:JSON.stringify(args)}}]}:{content:'Evidence checked.'}
 return new Response('data: '+JSON.stringify({id:'fixture',object:'chat.completion.chunk',model:'fixture',choices:[{index:0,delta,finish_reason:name?'tool_calls':'stop'}],usage:{prompt_tokens:2,completion_tokens:1}})+'\n\ndata: [DONE]\n\n',{headers:{'content-type':'text/event-stream'}})
}
it('#183 actual Vercel model path runs the registered action and exact child completion',async()=>{
 const store=new MemoryRunStore(),definition=nativeDeclaration,pin={id:definition.id,digest:definition.pinnedDigest,source:definition.source}
 let parent=0,children=0,active=0,peak=0,release:()=>void=()=>{}
 const together=new Promise<void>(resolve=>{release=resolve})
 const call:AssistantSession['model']['call']=async(_suffix,request)=>{
  const isParent=String(request.body).includes('"name":"delegate_plan_steps"');active++;peak=Math.max(peak,active)
  let timer:ReturnType<typeof setTimeout>|undefined
  try{
   if(!isParent){children++;if(children===2)release();await Promise.race([together,new Promise<never>((_,reject)=>{timer=setTimeout(()=>reject(Error('The second ready child was not scheduled.')),6000)})]);return task183WireResponse()}
   parent++
   if(parent===1)return task183WireResponse('update_plan',{explanation:'Inspect independent sources.',plan:[{step:'Inspect first source',status:'pending',budget:{requests:1},delegation:pin},{step:'Inspect second source',status:'pending',budget:{requests:1},delegation:pin}]})
   const state=await saved(store)
   if(parent===2)return task183WireResponse('delegate_plan_steps',{planRevision:state.planState.revision,steps:['step-1','step-2']})
   if(parent===3||parent===4){const row=state.planState.taskSchedule.rows[parent-3];return task183WireResponse('complete_plan_step',{id:row.id,expectedRevision:state.planState.revision,outcome:'completed',evidence:{operations:[],artifacts:[],child:{task:row.child.task,...row.child.result}}})}
   return task183WireResponse()
  }finally{if(timer)clearTimeout(timer);active--}
 }
 const base=packageInput(store,call),raw:AssistantSession={...base,subagents:[nativeDeclaration as SubagentDeclaration],policy:{concurrency:{children:2,requests:2}},context:{...base.context,requestCap:8}}
 const events=[];for await(const event of withDelegation(vercel).start(raw))events.push(event)
 expect(events.filter(event=>event.type==='error')).toEqual([]);expect(parent).toBe(5);expect(children).toBe(2);expect(peak).toBe(2)
 const state=await saved(store);expect(state.used).toBe(7);expect(state.plan.map((step:{status:string})=>step.status)).toEqual(['completed','completed'])
},60_000)
it('#183 an actual model tool cannot gain registration from a forged marker',async()=>{
 let calls=0,executions=0
 const forged={name:'forged_task_action',description:'Fixture forged marker.',taxonomy:{operation:'run' as const,effect:'read' as const},interaction:'delegation' as const,inputSchema:{type:'object',properties:{planRevision:{type:'string'},steps:{type:'array',items:{type:'string'}}},required:['planRevision','steps'],additionalProperties:false},taskStepRegistration:true,execute:async()=>{executions++;return {content:[]}}}
 const raw=input(new MemoryRunStore(),async()=>++calls===1?task183WireResponse('forged_task_action',{planRevision:'sha256:'+'a'.repeat(64),steps:['step-1']}):task183WireResponse());raw.hostTools=[forged]
 const events=[];for await(const event of vercel.start(raw))events.push(event)
 expect(calls).toBe(2);expect(executions).toBe(0);expect(events.some(event=>event.type==='tool_result'&&event.name==='forged_task_action'&&event.isError)).toBe(true)
})

for(const fault of ['binding','physical'] as const)it('#183 explicit native resume preserves queued IDs after '+fault+' acknowledgement loss',async()=>{
 const memory=new MemoryRunStore();let lose=true,calls=0,children=0
 const store:RunStore={durability:'memory',load:memory.load.bind(memory),readEvents:memory.readEvents.bind(memory),commit:async(value,signal)=>{
  const receipt=await memory.commit(value,signal),state=JSON.parse(new TextDecoder().decode(value.checkpoint!))
  const bound=state.planState?.taskSchedule?.rows.some((row:{child?:unknown})=>row.child)
  const physical=state.durable?.dispatches.some((row:{kind:string;state:string;scope:{task?:string}})=>row.kind==='agent'&&row.state==='claimed'&&row.scope.task)
  if(lose&&(fault==='binding'?bound:physical)){lose=false;throw Error('fixture '+fault+' acknowledgement lost')};return receipt
 }}
 const definition=nativeDeclaration,pin={id:definition.id,digest:definition.pinnedDigest,source:definition.source}
 const run:AgentRun=async(request,callbacks)=>{
  calls++
  const invoke=async(name:string,args:Record<string,unknown>)=>{const tool=request.tools.find(tool=>tool.description.startsWith(name+':'))!;return callbacks.tool({id:crypto.randomUUID(),name:tool.name,arguments:args},callbacks.signal)}
  try{
   if(calls===1){
    await invoke('update_plan',{explanation:'Inspect one source.',plan:[{step:'Inspect source',status:'pending',budget:{requests:1},delegation:pin}]})
    const state=await saved(memory);await invoke('delegate_plan_steps',{planRevision:state.planState.revision,steps:['step-1']});return
   }
   if(!request.tools.some(tool=>tool.description.startsWith('delegate_plan_steps:')))children++
   await callbacks.event({type:'message',id:crypto.randomUUID(),text:'Evidence checked.',phase:'final'})
  }finally{if(callbacks.providerRelease)callbacks.providerRelease.acknowledge(callbacks.providerRelease.invocation)}
 }
 const base=packageInput(store,vi.fn(async()=>response())),{model:_model,...common}=base
 const raw:AgentAssistantSession={...common,subagents:[nativeDeclaration as SubagentDeclaration],policy:{concurrency:{children:1,requests:1}},agent:{model:'fixture',run},context:{...common.context,requestCap:undefined,taskStepEnvelope:{...envelope,limit:4}}}
 for await(const _event of withDelegation(codex).start(raw)){}
 expect(lose).toBe(false);expect(children).toBe(0);expect(calls).toBe(1)
 const before=await saved(memory),id=before.planState.taskSchedule.rows[0].child.task
 expect(before.handoff.tasks.map((task:{id:string})=>task.id)).toEqual([id])
 raw.context!.runStore!.resume=true
 const resumedEvents=[];for await(const event of withDelegation(codex).start(raw))resumedEvents.push(event)
 const after=await saved(memory),row=after.planState.taskSchedule.rows[0]
 expect(row.child.task).toBe(id);expect(children,JSON.stringify({events:resumedEvents.filter(event=>event.type==='error'||event.type==='delegated_task'),handoff:after.handoff,delegation:after.delegation})).toBe(fault==='binding'?1:0)
 expect(after.durable.dispatches.filter((claim:{scope:{task?:string};kind:string})=>claim.scope.task===id&&claim.kind==='agent')).toHaveLength(1)
 expect(row.claims).toHaveLength(1)
 if(fault==='physical')expect(row.child.result).toBeUndefined()
},60_000)

it('#183 disabled mode preserves legacy request bytes and plan declarations',async()=>{
 const store=new MemoryRunStore(),body=JSON.stringify({model:'fixture',messages:[{role:'user',content:'Work'}]}),call=vi.fn(async(_suffix:string,request:{body?:unknown})=>{expect(request.body).toBe(body);return response()})
 const raw=input(store,call as AssistantSession['model']['call']);delete raw.context!.taskSteps
 const ready=await contextualSession(raw)
 try{
  const tools=ready.session.values.hostTools,plan=tools.find(tool=>tool.name==='update_plan')!,complete=tools.find(tool=>tool.name==='complete_plan_step')!
  expect(tools.some(tool=>tool.name==='delegate_plan_steps')).toBe(false)
  const schema=plan.inputSchema as {properties:{plan:{items:{properties:Record<string,unknown>}}}}
  expect(Object.keys(schema.properties.plan.items.properties)).toEqual(['id','step','status','detail'])
  expect(JSON.stringify(complete.inputSchema)).not.toContain('checkpointDigest')
  await update(ready,[{step:'First',status:'pending'}]);await send(ready)
  expect(call).toHaveBeenCalledOnce();const state=await saved(store)
  expect(state.version).toBe(2);expect(state.planState.taskSchedule).toBeUndefined();expect(state.planState.taskEnvelope).toBeUndefined()
  const events=await store.readEvents('task-steps-183',null,100)
  const revised=events.items.map(row=>JSON.parse(new TextDecoder().decode(row.bytes)).event).find(event=>event.type==='plan_revised')
  expect(revised.version).toBe(1)
 }finally{ready.context.close()}
})

it('#183 retains configured candidate members through accepted-plan drift approval',async()=>{
 const chatStore=new MemoryChatStore([{chat:{id:'task183-drift',interaction:'plan',plans:[]}}]),signal=new AbortController().signal,resolver={resolve:async()=>{throw Error('No references in fixture')}}
 await planTools(chatStore,'task183-drift',resolver)[0]!.execute({objective:'Inspect evidence.',steps:['Inspect source','Report evidence'],questions:[],references:[]},signal)
 const chat=(await currentChat(chatStore,'task183-drift'))!,proposal=chat.plans!.at(-1)!;await acceptPlan(chatStore,chat,proposal,signal,resolver)
 const accepted=(await currentChat(chatStore,'task183-drift'))!.plans!.at(-1)! as ChatPlan,store=new MemoryRunStore(),call=vi.fn(async()=>response()),raw=input(store,call)
 raw.context={...raw.context,phase:'work',plan:accepted,planDrift:['changed']}
 const ready=await contextualSession(raw)
 try{
  const original=ready.context.execution.state.plan[0]!
  await update(ready,[{id:original.id,step:'Inspect revised evidence',status:'pending',budget:{requests:1},dependsOn:[]}])
  const state=await saved(store);expect(state.status).toBe('paused');expect(state.plan[0].step).toBe('Inspect source');expect(state.planState.pending.candidate.plan[0].budget).toEqual({requests:1});expect(readExecution(state)).toBeDefined();expect(call).not.toHaveBeenCalled()
 }finally{ready.context.close()}
})

for(const status of [429,503])it('#183 original physical charges retain '+status+' response semantics',async()=>{
 const store=new MemoryRunStore(),definition=nativeDeclaration,pin={id:definition.id,digest:definition.pinnedDigest,source:definition.source};let parent=0,child=0
 const call:AssistantSession['model']['call']=async(_suffix,request)=>{
  if(!String(request.body).includes('"name":"delegate_plan_steps"')){child++;return child===1?new Response(JSON.stringify({error:{type:status===429?'rate_limit_error':'overloaded_error',message:'Busy fixture.'}}),{status,headers:{'content-type':'application/json'}}):task183WireResponse()}
  parent++;if(parent===1)return task183WireResponse('update_plan',{explanation:'Inspect source.',plan:[{step:'Inspect source',status:'pending',budget:{requests:2},delegation:pin}]})
  if(parent===2)return task183WireResponse('delegate_plan_steps',{planRevision:(await saved(store)).planState.revision,steps:['step-1']})
  return task183WireResponse()
 }
 const raw=packageInput(store,call);raw.subagents=[definition as SubagentDeclaration];raw.context!.requestCap=8
 const events=[];for await(const event of withDelegation(vercel).start(raw))events.push(event)
 const state=await saved(store),row=state.planState.taskSchedule.rows[0]
 expect(child).toBe(status===429?2:1);expect(parent).toBe(status===429?3:2);expect(state.used).toBe(status===429?5:3);expect(row.claims).toHaveLength(status===429?2:1);expect(new Set(row.claims.map((claim:{id:string})=>claim.id)).size).toBe(row.claims.length)
 if(status===429){expect(events.some(event=>event.type==='delegated_task'&&event.task.state==='complete'&&event.task.modelRequests===2)).toBe(true);expect(row.child.result).toBeDefined()}else expect(row.child.result).toBeUndefined()
},60_000)
