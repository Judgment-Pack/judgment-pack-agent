import {describe,it,expect,vi} from 'vitest'
import checkpoints from '../../../contract/vectors/plan/extended-checkpoints.json'
import events from '../../../contract/vectors/plan/extended-events.json'
import {closePlan,changedDepartures,contentDigest} from './planActions'
import {revisePlan,validPlanState,type IdentifiedPlan} from './plan'
import {readExecution} from './state'
import {readRunEvent} from '../recordReader'
import {contextualSession} from '../context/session'
import {managedContext} from '../context/managed'
import type {AssistantEvent} from '../engine'
import {MemoryRunStore} from '../storage/run'
import type {DurableBook,DurableScope} from '../storage/writer'
import type {AssistantSession} from '../engine'
const rows=checkpoints.items
const held=(i:number)=>structuredClone({plan:rows[i]!.plan,planState:rows[i]!.planState}) as unknown as IdentifiedPlan
const scope:DurableScope={execution:'extended-plan',role:'author'}
const proof=()=>({scope,book:{dispatches:[{id:'dispatch-ok',scope,kind:'host',source:'host',tool:'inspect',fingerprint:'sha256:'+'a'.repeat(64),request:'{}',requestDigest:'sha256:'+'a'.repeat(64),state:'result',read:true,result:{content:[]}}],observations:[{id:'observed-operation',scope,kind:'operation',source:'host',dispatchId:'dispatch-ok',commitId:'commit-ok'},{id:'observed-artifact',scope,kind:'artifact-read',source:'host',dispatchId:'dispatch-ok',commitId:'commit-ok',artifact:{kind:'artifact',id:'artifact-1',digest:'sha256:'+'b'.repeat(64)}}],children:[]} as DurableBook})
const args=()=>({id:'step-1',expectedRevision:held(1).planState.revision,outcome:'completed',evidence:structuredClone(held(2).plan[0]!.evidence)})
const input=(store?:MemoryRunStore):AssistantSession=>({prompt:'Work',testPrompt:'',allowConversation:true,interactive:true,tools:[],hostTools:[],callTool:vi.fn(async()=>({content:[]})),model:{family:'openai-compatible',model:'fixture',call:vi.fn(async()=>new Response('{}'))},signal:new AbortController().signal,context:{currentRequest:{message:'Work',input:'Work'},...(store?{runStore:{store,id:'extended-plan'}}:{})}})
const accepted=()=>{const a=held(0).planState.accepted!;return {id:a.id,revision:a.sourceRevision,objective:a.objective,steps:structuredClone(a.steps),references:structuredClone(a.references),acceptedAt:a.acceptedAt,status:'accepted' as const,questions:[],createdAt:a.acceptedAt}}
const incoming=(value:IdentifiedPlan)=>value.plan.map(({id,step,status,detail})=>({id,step,status,...(detail===undefined?{}:{detail})}))
describe('active completion and accepted-plan drift',()=>{
 it('reads all handwritten extended vectors and preserves exact terminal evidence',()=>{
  for(const row of rows)expect(readExecution(row)).toEqual(row)
  for(const event of events.items)expect(readRunEvent(event)).toEqual(event)
  expect(closePlan(held(1),args(),proof()).next).toEqual(held(2))
  expect(closePlan(held(3),{id:'step-2',expectedRevision:held(3).planState.revision,outcome:'not-needed',reason:'The recorded inspection already includes the report.'},undefined).next).toEqual(held(4))
 })
 it('refuses unknown, failed, unacknowledged, wrong-role and wrong-child evidence atomically',()=>{
  const cases:[string,(p:ReturnType<typeof proof>)=>void][]=[['unknown-operation',p=>p.book.dispatches=[]],['unsuccessful-operation',p=>p.book.dispatches[0]!.state='unknown'],['unsuccessful-operation',p=>p.book.dispatches[0]!.result={isError:true}],['unsuccessful-operation',p=>p.book.dispatches[0]!.kind='model'],['unsuccessful-operation',p=>p.book.observations=[]],['wrong-scope',p=>p.book.dispatches[0]!.scope={...scope,role:'critic'}],['wrong-scope',p=>p.book.dispatches[0]!.scope={...scope,task:'other-child'}],['unobserved-revision',p=>p.book.observations[1]!.artifact!.id='same-digest-other-id'],['unobserved-revision',p=>p.book.observations[1]!.artifact!.digest='sha256:'+'c'.repeat(64)]]
  for(const [reason,mutate] of cases){const current=held(1),before=structuredClone(current),p=proof();mutate(p);expect(closePlan(current,args(),p).reason).toBe(reason);expect(current).toEqual(before);expect(current.plan[0]!.status).toBe('in_progress')}
  expect(closePlan(held(1),args(),undefined).reason).toBe('evidence-unavailable')
  const failed=proof();failed.book.dispatches[0]!.result={isError:true};expect(closePlan(held(1),{...args(),evidence:{operations:[],artifacts:args().evidence!.artifacts}},failed).reason).toBe('unobserved-revision')
 })
 it('rejects malformed/empty/oversized evidence, stale revision, wrong id and repeated closure',()=>{
  for(const evidence of [undefined,{},[],{operations:[''],artifacts:[]},{operations:Array.from({length:21},(_,i)=>'op'+i),artifacts:[]},{operations:['dispatch-ok','dispatch-ok'],artifacts:[]}])expect(closePlan(held(1),{...args(),evidence},proof()).reason).toBe('invalid-input')
  expect(closePlan(held(1),{...args(),evidence:{operations:[],artifacts:[]}},proof()).reason).toBe('missing-evidence')
  expect(closePlan(held(1),{...args(),expectedRevision:held(0).planState.revision},proof()).reason).toBe('revision-changed')
  expect(closePlan(held(1),{...args(),id:'step-2'},proof()).reason).toBe('active-step-required')
  expect(closePlan(held(2),{...args(),expectedRevision:held(2).planState.revision},proof()).reason).toBe('active-step-required')
  expect(closePlan(held(1),{...args(),reason:'Also a reason'},proof()).reason).toBe('invalid-input')
  expect(closePlan(held(1),{...args(),outcome:'not-needed',reason:'Unneeded'},proof()).reason).toBe('invalid-input')
 })
 it('carries several unchanged terminal rows and refuses terminal introduction or rewording with no unrelated edits',()=>{
  const current=held(4),before=structuredClone(current)
  expect(revisePlan(current,[...incoming(current),{step:'Follow up',status:'pending'}],'Extend').plan.slice(0,2)).toEqual(current.plan)
  for(const plan of [[{...incoming(held(1))[0]!,status:'completed' as const}],[{...incoming(current)[0]!,step:'Reword'},incoming(current)[1]!],[{...incoming(current)[0]!,status:'pending' as const},incoming(current)[1]!]])expect(()=>revisePlan(plan[0]?.status==='completed'&&plan.length===1?held(1):current,plan,'Refuse')).toThrow(/PlanCompletionActionRequired/)
  expect(current).toEqual(before)
 })
 it('rejects altered lineage, nested candidates, stale pending binding and mismatched candidate digest',()=>{
  for(const mutate of [(s:any)=>s.plan[0].origin.index=19,(s:any)=>s.planState.accepted.steps[0]='Changed',(s:any)=>s.planState.history[0].diff[0].before.origin.plan='other-accepted-plan',(s:any)=>s.planState.pending.revision=held(0).planState.revision,(s:any)=>s.planState.pending.candidate.planState.pending=s.planState.pending,(s:any)=>s.planState.pending.candidate.planState.revision=held(0).planState.revision,(s:any)=>s.planState.pending.candidate.planState.nextId=9,(s:any)=>s.planState.pending.candidate.planState.history.at(-1).explanation='Another explanation']){const value=structuredClone(rows[5]) as any;mutate(value);expect(()=>readExecution(value)).toThrow()}
  expect(changedDepartures(held(1),held(6),'Narrow the accepted work')).toEqual(rows[5]!.planState.pending!.departures)
  expect(contentDigest({objective:accepted().objective,steps:accepted().steps,questions:[],references:[]})).toBe(rows[0]!.planState.accepted.revision)
 })
 it('seeds accepted work, pauses removed work, consumes exact approve/reject once and persists decisions before dispatch',async()=>{
  for(const approved of [true,false]){
   const store=new MemoryRunStore(),session={...input(store),context:{...input(store).context,plan:accepted()}},ready=await contextualSession(session)
   const controller=ready.context.execution,original=structuredClone(controller.state.plan),update=ready.session.values.hostTools.find(t=>t.name==='update_plan')!
   expect(controller.state.planState!.accepted?.id).toBe('accepted-plan')
   await update.execute({explanation:'Remove accepted work',plan:[{id:original[1]!.id,step:'Report',status:'pending'}]},ready.session.signal)
   expect(controller.state.plan).toEqual(original);expect(controller.approvalAsked).toBe(true)
   const pending=controller.state.planState!.pending!;expect(pending.purpose).toBe('plan-drift')
   await expect(update.execute({explanation:'Another update',plan:[{step:'Other',status:'pending'}]},ready.session.signal)).rejects.toThrow(/PlanApprovalRequired/)
   ready.context.close()
   await expect(contextualSession({...session,context:{...session.context,runStore:{store,id:'extended-plan',resume:true},approval:{callId:'different',approved}}})).rejects.toThrow(/PlanApprovalRequired/)
   const continued=await contextualSession({...session,context:{...session.context,runStore:{store,id:'extended-plan',resume:true},approval:{callId:pending.callId,approved,note:'Decision'}}})
   expect(continued.context.execution.state.planState!.pending).toBeUndefined();expect(continued.context.execution.state.plan).toHaveLength(approved?1:2)
   expect(continued.context.execution.state.planState!.decisions?.[0]).toMatchObject({callId:pending.callId,approved});expect(continued.context.execution.requestState()).toContain(approved?'\"approved\":true':'\"approved\":false')
   expect(continued.context.takeNotices().filter(e=>e.type==='plan_drift_decided')).toHaveLength(1)
   const saved=(await store.load('extended-plan'))!;expect(JSON.parse(new TextDecoder().decode(saved.checkpoint!)).planState.decisions[0].approved).toBe(approved)
   continued.context.close()
   await expect(contextualSession({...session,context:{...session.context,runStore:{store,id:'extended-plan',resume:true},approval:{callId:pending.callId,approved}}})).rejects.toThrow(/decision|decided/)
  }
 })
 it('keeps no durable proof in callback mode and retains bounded refusal events through resume',async()=>{
  const ready=await contextualSession(input()),controller=ready.context.execution
  controller.mutate({...held(1)});controller.proof=undefined
  await expect(controller.completionTool().execute(args(),ready.session.signal)).rejects.toThrow(/PlanEvidenceUnavailable/)
  expect(controller.state.plan[0]!.status).toBe('in_progress');expect(controller.state.planState!.sequence).toBe(1)
  for(let i=0;i<35;i++)await expect(controller.completionTool().execute({},ready.session.signal)).rejects.toThrow(/invalid-input/)
  expect(controller.state.planState!.refusals).toHaveLength(32);expect(controller.state.planState!.refusalsDiscarded).toBe(4)
  const snapshot=controller.snapshot('stopped');expect(readExecution(snapshot)?.planState).toEqual(snapshot.planState);expect(validPlanState(snapshot.plan,snapshot.planState)).toBe(true)
  ready.context.close()
 })
 it('uses actual acknowledged host evidence, exposes exact scoped ids and preserves proof through resume',async()=>{
  const store=new MemoryRunStore(),raw=input(store)
  raw.hostTools=[{name:'inspect',effect:'read',description:'Inspect',inputSchema:{type:'object'},execute:async()=>({content:[{type:'text',text:'Inspected'}]}),artifacts:()=>[{kind:'artifact',id:'artifact-1',digest:'sha256:'+'b'.repeat(64)}]}]
  const ready=await contextualSession(raw),controller=ready.context.execution,signal=ready.session.signal
  await ready.session.values.hostTools.find(t=>t.name==='update_plan')!.execute({explanation:'Inspect',plan:[{step:'Inspect',status:'in_progress'}]},signal)
  await ready.session.values.hostTools.find(t=>t.name==='inspect')!.execute({},signal)
  const op=controller.proof!()!.book.dispatches.find(d=>d.tool==='inspect')!
  expect(controller.requestState()).toContain(op.id)
  await ready.session.values.hostTools.find(t=>t.name==='complete_plan_step')!.execute({id:controller.state.plan[0]!.id,expectedRevision:controller.state.planState!.revision,outcome:'completed',evidence:{operations:[op.id],artifacts:[{kind:'artifact',id:'artifact-1',digest:'sha256:'+'b'.repeat(64)}]}},signal)
  const evidence=structuredClone(controller.state.plan[0]!.evidence);expect(evidence?.operations).toEqual([op.id]);ready.context.close()
  const resumed=await contextualSession({...raw,context:{...raw.context,runStore:{store,id:'extended-plan',resume:true}}})
  expect(resumed.context.execution.state.plan[0]!.evidence).toEqual(evidence);resumed.context.close()
 })
 it('serializes competing closures against the same content revision',async()=>{
  const ready=await contextualSession(input()),controller=ready.context.execution;controller.mutate(held(1));controller.proof=proof
  const result=await Promise.allSettled([controller.completionTool().execute(args(),ready.session.signal),controller.completionTool().execute({...args(),outcome:'not-needed',evidence:undefined,reason:'No further work'},ready.session.signal)])
  expect(result.filter(r=>r.status==='fulfilled')).toHaveLength(1);expect(controller.state.plan[0]!.status).toBe('completed');expect(controller.state.planState!.sequence).toBe(2)
  expect(controller.state.planState!.refusals?.at(-1)?.reason).toBe('revision-changed');ready.context.close()
 })
 it('keeps the actual store CAS conflict from overwriting another plan commit',async()=>{
  const store=new MemoryRunStore(),raw=input(store),first=await contextualSession(raw)
  await first.context.execution.tool().execute({explanation:'Initial',plan:[{step:'Inspect',status:'in_progress'}]},first.session.signal);first.context.close()
  const resume={...raw,context:{...raw.context,runStore:{store,id:'extended-plan',resume:true}}},a=await contextualSession(resume),b=await contextualSession(resume)
  const plan=a.context.execution.state.plan.map(({id,step,status})=>({id,step,status}))
  const results=await Promise.allSettled([a.context.execution.tool().execute({explanation:'First editor',plan:[{...plan[0]!,detail:'A'}]},a.session.signal),b.context.execution.tool().execute({explanation:'Second editor',plan:[{...plan[0]!,detail:'B'}]},b.session.signal)])
  expect(results.filter(r=>r.status==='fulfilled')).toHaveLength(1)
  const saved=JSON.parse(new TextDecoder().decode((await store.load('extended-plan'))!.checkpoint!));expect(saved.planState.sequence).toBe(2)
  a.context.close();b.context.close()
 })

 it('validates pinned dependencies before initial work and every resume',async()=>{
  const store=new MemoryRunStore(),raw=input(store),plan={...accepted(),references:[{kind:'artifact' as const,id:'dependency',digest:'b'.repeat(64)}]}
  const resolve=vi.fn(async()=>({kind:'artifact' as const,id:'dependency',digest:'b'.repeat(64)}))
  await expect(contextualSession({...raw,context:{...raw.context,plan}})).rejects.toThrow(/PlanResolverRequired/)
  resolve.mockResolvedValueOnce({kind:'artifact',id:'dependency',digest:'c'.repeat(64)})
  await expect(contextualSession({...raw,context:{...raw.context,plan,planResolver:{resolve}}})).rejects.toThrow(/PlanReferenceChanged/)
  const ready=await contextualSession({...raw,context:{...raw.context,plan,planResolver:{resolve}}})
  await ready.context.execution.tool().execute({explanation:'Track accepted work',plan:incoming({plan:ready.context.execution.state.plan,planState:ready.context.execution.state.planState!})},ready.session.signal)
  expect(ready.context.execution.state.planState!.accepted!.validatedReferences).toEqual([{kind:'artifact',id:'dependency',digest:'b'.repeat(64)}])
  ready.context.close();resolve.mockResolvedValueOnce({kind:'artifact',id:'dependency',digest:'c'.repeat(64)})
  await expect(contextualSession({...raw,context:{...raw.context,plan,planResolver:{resolve},runStore:{store,id:'extended-plan',resume:true}}})).rejects.toThrow(/PlanReferenceChanged/)
 })
 it('offers mutually exclusive completed/evidence and not-needed/reason branches',async()=>{
  const ready=await contextualSession(input()),schema=ready.context.execution.completionTool().inputSchema
  expect(schema).toMatchObject({oneOf:[{required:['evidence'],not:{required:['reason']}},{required:['reason'],not:{required:['evidence']}}]});ready.context.close()
 })

 it('retains accepted content, origins and original pin checks beyond bounded history rollover and resume',async()=>{
  const store=new MemoryRunStore(),raw=input(store),plan={...accepted(),references:[{kind:'artifact' as const,id:'dependency',digest:'b'.repeat(64)}]},planResolver={resolve:async()=>({kind:'artifact' as const,id:'dependency',digest:'b'.repeat(64)})},ready=await contextualSession({...raw,context:{...raw.context,plan,planResolver}}),c=ready.context.execution
  const original=structuredClone(c.state.planState!.accepted)
  for(let i=0;i<35;i++)await c.tool().execute({explanation:'Track unchanged accepted work '+i,plan:incoming({plan:c.state.plan,planState:c.state.planState!})},ready.session.signal)
  expect(c.state.planState!.history).toHaveLength(32);expect(c.state.planState!.discarded).toBe(3);expect(c.state.planState!.accepted).toEqual(original)
  expect(c.state.plan.every((step,index)=>step.origin?.index===index&&step.step===original!.steps[index])).toBe(true);ready.context.close()
  const resumed=await contextualSession({...raw,context:{...raw.context,plan,planResolver,runStore:{store,id:'extended-plan',resume:true}}})
  expect(resumed.context.execution.state.planState!.accepted).toEqual(original);expect(resumed.context.execution.state.planState!.accepted!.validatedReferences).toEqual([{kind:'artifact',id:'dependency',digest:'b'.repeat(64)}]);resumed.context.close()
 })
 it('waits for the atomic plan checkpoint/event acknowledgement before returning',async()=>{
  const store=new MemoryRunStore(),commit=store.commit.bind(store);let release!:()=>void,entered!:()=>void
  const blocked=new Promise<void>(resolve=>{release=resolve}),seen=new Promise<void>(resolve=>{entered=resolve})
  store.commit=async transaction=>{if(transaction.events.some(bytes=>JSON.parse(new TextDecoder().decode(bytes)).event.type==='plan_revised')){entered();await blocked}return commit(transaction)}
  const ready=await contextualSession(input(store)),prior=await store.load('extended-plan');let returned=false
  const completion=ready.context.execution.tool().execute({explanation:'Track',plan:[{step:'Inspect',status:'pending'}]},ready.session.signal).then(()=>{returned=true})
  await seen;expect(returned).toBe(false);expect((await store.load('extended-plan'))?.revision).toBe(prior?.revision);release();await completion
  expect((await store.load('extended-plan'))!.cursor).not.toBeNull();ready.context.close()
 })

 it('delivers one purpose-specific plan approval through the real managed boundary and blocks later dispatch',async()=>{
  const store=new MemoryRunStore(),raw=input(store),delivered:AssistantEvent[]=[];raw.context={...raw.context,plan:accepted()}
  const effect=vi.fn(async()=>({content:[]}));raw.hostTools=[{name:'effect',description:'Effect',inputSchema:{type:'object'},execute:effect}]
  for await(const event of managedContext(raw,async function*(prepared){
   const c=prepared.values.hostTools.find(t=>t.name==='update_plan')!
   await c.execute({explanation:'Narrow accepted work',plan:[{id:'step-2',step:'Report',status:'pending'}]},prepared.signal)
   await expect(prepared.values.hostTools.find(t=>t.name==='effect')!.execute({},prepared.signal)).rejects.toThrow(/PlanApprovalRequired/)
   yield {type:'tool_result',name:'update_plan',isError:false,text:'approval-required'};yield {type:'end'}
  }))delivered.push(event)
  expect(effect).not.toHaveBeenCalled();expect(delivered.filter(e=>e.type==='approval_requested')).toHaveLength(1)
  expect(delivered.find(e=>e.type==='approval_requested')).toMatchObject({purpose:'plan-drift',plan:{execution:'extended-plan'}})
  const page=await store.readEvents('extended-plan',null,100);expect(page.items.map(row=>JSON.parse(new TextDecoder().decode(row.bytes)).event).filter(e=>e.type==='approval_requested')).toHaveLength(1)
 })

 it('records status-only and reorder behavior and asks again for another accepted text departure',async()=>{
  const defaultRun=await contextualSession({...input(),context:{...input().context,plan:accepted()}}),d=defaultRun.context.execution
  await d.tool().execute({explanation:'Start inspection',plan:[{id:'step-1',step:'Inspect',status:'in_progress'},{id:'step-2',step:'Report',status:'pending'}]},defaultRun.session.signal)
  expect(d.state.planState!.departures).toBeUndefined()
  await d.tool().execute({explanation:'Reorder work',plan:[{id:'step-2',step:'Report',status:'pending'},{id:'step-1',step:'Inspect',status:'in_progress'}]},defaultRun.session.signal)
  expect(d.approvalAsked).toBe(false);expect(d.state.planState!.departures?.[0]?.changes.map(row=>row.kind)).toEqual(['reordered','reordered']);defaultRun.context.close()
  const store=new MemoryRunStore(),raw=input(store),base={...raw,context:{...raw.context,plan:accepted(),planDrift:['changed' as const]}},first=await contextualSession(base)
  const edit=(text:string)=>({explanation:'Adjust accepted inspection',plan:[{id:'step-1',step:text,status:'pending'},{id:'step-2',step:'Report',status:'pending'}]})
  await first.context.execution.tool().execute(edit('Inspect revision A'),first.session.signal)
  const one=first.context.execution.state.planState!.pending!;expect(one).toBeDefined();first.context.close()
  const resumed=await contextualSession({...base,context:{...base.context,runStore:{store,id:'extended-plan',resume:true},approval:{callId:one.callId,approved:true}}})
  await resumed.context.execution.tool().execute(edit('Inspect revision B'),resumed.session.signal)
  const two=resumed.context.execution.state.planState!.pending!;expect(two.callId).not.toBe(one.callId);expect(two.departures).toEqual([{kind:'changed',step:'step-1',index:0}])
  expect(resumed.context.execution.state.plan[0]!.step).toBe('Inspect revision A');expect(two.candidate.plan[0]!.step).toBe('Inspect revision B');resumed.context.close()
 })

 it('checks accepted content independently of pending candidates and requires operation-only acknowledgements',()=>{
  const altered=structuredClone(rows[0]);altered.planState.accepted.steps[0]='Changed acceptance';expect(()=>readExecution(altered)).toThrow()
  for(const mutate of [(p:ReturnType<typeof proof>)=>{p.book.observations=[]},(p:ReturnType<typeof proof>)=>{p.book.dispatches[0]!.state='unknown'}]){const p=proof();mutate(p);expect(closePlan(held(1),{...args(),evidence:{operations:['dispatch-ok'],artifacts:[]}},p).reason).toBe('unsuccessful-operation')}
 })

})
