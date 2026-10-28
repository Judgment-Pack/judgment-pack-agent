import {it,expect} from 'vitest'
import vector from '../../contract/testdata/task-steps/scenarios.json'
import {validateTaskGraph,readyTaskSteps,createTaskSchedule,readTaskSchedule,reserveTaskStep,requireTaskProfiles,reconcileTaskSchedule,bindTaskChildren,type TaskStepDefinition,type TaskScheduleBinding,type TaskStepStatus} from './taskSteps'
const binding=vector.binding as TaskScheduleBinding,requestDigest='sha256:'+'c'.repeat(64)
for(const row of vector.cases)it(`task steps183 ${row.id}`,()=>{
 const run=()=>{validateTaskGraph(row.steps);const steps=row.steps as TaskStepDefinition[];const schedule=createTaskSchedule(binding,steps,row.available??4);expect(readyTaskSteps(steps,('statuses'in row?row.statuses:{}) as Record<string,TaskStepStatus>)).toEqual('ready'in row?row.ready:[]);expect(readTaskSchedule(JSON.parse(JSON.stringify(schedule)),binding,steps)).toEqual(schedule)}
 if('error'in row)expect(run).toThrow(row.error);else run()
})
it('retains unknown shares across restore without replay and protects the sibling reservation',()=>{
 const steps:TaskStepDefinition[]=[{id:'step-1',budget:{requests:1}},{id:'step-2',budget:{requests:2}}],statuses:Record<string,TaskStepStatus>={'step-1':'in_progress','step-2':'pending'}
 const initial=createTaskSchedule(binding,steps,3),next=reserveTaskStep(initial,steps,statuses,'step-1',{id:'exact-dispatch',requestDigest},3)
 expect(initial.rows[0]!.claims).toEqual([]);next.rows[0]!.claims[0]!.state='unknown'
 const saved=readTaskSchedule(JSON.parse(JSON.stringify(next)),binding,steps)
 expect(saved.rows[0]!.claims[0]!.state).toBe('unknown')
 expect(()=>reserveTaskStep(saved,steps,statuses,'step-1',{id:'exact-dispatch',requestDigest},3)).toThrow('reservation-already-held')
 expect(()=>reserveTaskStep(saved,steps,statuses,'step-1',{id:'new-dispatch',requestDigest},2)).toThrow('step-budget-pause')
 expect(()=>readTaskSchedule(saved,{...binding,scopeDigest:'sha256:'+'d'.repeat(64)},steps)).toThrow('identity-changed')
 expect(()=>readTaskSchedule(saved,binding,[{...steps[0]!,budget:{requests:2}},steps[1]!])).toThrow('identity-changed')
})
it('refuses starting an unready step even if status claims it is already active',()=>{
 const steps:TaskStepDefinition[]=[{id:'step-1'},{id:'step-2',dependsOn:['step-1']}],schedule=createTaskSchedule(binding,steps,3)
 expect(()=>reserveTaskStep(schedule,steps,{'step-1':'pending','step-2':'in_progress'},'step-2',{id:'d',requestDigest},3)).toThrow('dependencies-unready')
})
it('preserves committed sibling shares from unassigned work and requires exact admitted profile pins',()=>{
 const steps:TaskStepDefinition[]=[{id:'step-1'},{id:'step-2',budget:{requests:2}}],schedule=createTaskSchedule(binding,steps,3)
 expect(()=>reserveTaskStep(schedule,steps,{'step-1':'in_progress','step-2':'pending'},'step-1',{id:'d',requestDigest},2)).toThrow('step-budget-pause')
 expect(()=>validateTaskGraph([{id:'step-1',delegation:{id:'named-profile'}}])).toThrow('invalid-profile-pin')
 expect(()=>readTaskSchedule({...schedule,extra:true},binding,steps)).toThrow('identity-changed')
})

it('binds delegation to the full actual admitted profile pin',()=>{
 const pin={id:'research',digest:'sha256:'+'d'.repeat(64),source:{kind:'custom' as const,id:'project',trust:'person' as const,version:'one'}}
 const steps=[{id:'step-1',delegation:pin}]
 expect(()=>requireTaskProfiles(steps,[structuredClone(pin)])).not.toThrow()
 expect(()=>requireTaskProfiles(steps,[])).toThrow('profile-not-admitted')
 expect(()=>requireTaskProfiles(steps,[{...pin,digest:'sha256:'+'e'.repeat(64)}])).toThrow('profile-not-admitted')
})

it('preserves an unknown physical claim across a real plan revision and protects prior active definitions',()=>{
 const steps:TaskStepDefinition[]=[{id:'step-1',budget:{requests:1}},{id:'step-2',budget:{requests:2}}],before:Record<string,TaskStepStatus>={'step-1':'in_progress','step-2':'pending'},after:Record<string,TaskStepStatus>={'step-1':'completed','step-2':'pending'}
 const held=reserveTaskStep(createTaskSchedule(binding,steps,3),steps,before,'step-1',{id:'original-physical-claim',requestDigest},3);held.rows[0]!.claims[0]!.state='unknown'
 const revised={...binding,planRevision:'sha256:'+'e'.repeat(64)},next=reconcileTaskSchedule(held,steps,before,revised,steps,after,2)
 expect(next.rows[0]!.claims).toEqual([{id:'original-physical-claim',requestDigest,state:'unknown'}]);expect(next.binding.planRevision).toBe(revised.planRevision);expect(held.binding.planRevision).toBe(binding.planRevision)
 expect(()=>reconcileTaskSchedule(held,steps,before,revised,[{...steps[0]!,budget:{requests:2}},steps[1]!],{'step-1':'pending','step-2':'pending'},3)).toThrow('active-definition-changed')
 expect(()=>reconcileTaskSchedule(held,steps,before,revised,[steps[1]!],{'step-2':'pending'},3)).toThrow('active-definition-changed')
})
it('releases unused share only for explicitly terminal rows',()=>{
 const steps:TaskStepDefinition[]=[{id:'step-1',budget:{requests:1}},{id:'step-2',budget:{requests:2}}],schedule=createTaskSchedule(binding,steps,3)
 expect(()=>reserveTaskStep(schedule,steps,{'step-1':'in_progress','step-2':'pending'},'step-1',{id:'new-physical',requestDigest},1)).toThrow('step-budget-pause')
 expect(reserveTaskStep(schedule,steps,{'step-1':'in_progress','step-2':'not-needed'},'step-1',{id:'new-physical',requestDigest},1).rows[0]!.claims).toHaveLength(1)
})
it('validates an entire child association set before deriving any binding',()=>{
 const pin={id:'research',digest:'sha256:'+'d'.repeat(64),source:{kind:'custom' as const,id:'project',trust:'person' as const,version:'one'}},steps=[{id:'step-1',delegation:pin},{id:'step-2',delegation:pin}],statuses:Record<string,TaskStepStatus>={'step-1':'pending','step-2':'pending'},schedule=createTaskSchedule(binding,steps,4)
 const children=[{step:'step-1',task:'child-one',profile:pin},{step:'step-2',task:'child-two',profile:pin}],next=bindTaskChildren(schedule,steps,statuses,children)
 expect(schedule.rows.map(r=>r.child)).toEqual([undefined,undefined]);expect(next.rows.map(r=>r.child?.task)).toEqual(['child-one','child-two']);expect(next.rows[0]!.child).toEqual({task:'child-one',execution:'child-one',planRevision:binding.planRevision,profile:pin})
 for(const bad of [[children[0]!,children[0]!],[children[0]!,{...children[1]!,task:'child-one'}],[children[0]!,{...children[1]!,profile:{...pin,digest:'sha256:'+'f'.repeat(64)}}]]){expect(()=>bindTaskChildren(schedule,steps,statuses,bad)).toThrow();expect(schedule.rows.every(r=>r.child===undefined)).toBe(true)}
 const duplicate=structuredClone(next);duplicate.rows[1]!.child!.task='child-one';duplicate.rows[1]!.child!.execution='child-one';expect(()=>readTaskSchedule(duplicate,binding,steps)).toThrow('invalid-child-binding')
 expect(()=>bindTaskChildren(next,steps,statuses,[children[0]!])).toThrow('child-not-admitted')
 const claimed=structuredClone(schedule);claimed.rows[0]!.claims=[{id:'local-claim',requestDigest,state:'unknown'}];expect(()=>bindTaskChildren(claimed,steps,statuses,[children[0]!])).toThrow('child-not-admitted')
 next.rows[0]!.child!.result={checkpointDigest:'sha256:'+'a'.repeat(64),resultDigest:'sha256:'+'b'.repeat(64),eventDigest:'sha256:'+'c'.repeat(64),commitId:'actual-child-transaction'}
 expect(readTaskSchedule(next,binding,steps).rows[0]!.child!.result!.commitId).toBe('actual-child-transaction')
 next.rows[0]!.child!.result!.commitId='';expect(()=>readTaskSchedule(next,binding,steps)).toThrow('invalid-child-evidence')
})
