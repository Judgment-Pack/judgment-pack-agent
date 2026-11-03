import {describe,it,expect} from 'vitest'
import {canonicalJSON} from '../context/chat'
import {readExecution} from '../execution/state'
import {checkRecord,readRunEvent} from '../recordReader'
import {readRunReport} from '../runReportReader'
import {GOAL_CAPABILITY_REASONS,GOAL_REFUSAL_REASONS,GoalCapabilityRefused,GoalRefused,GoalTable,goalCommandDigest,goalDeliveryDigest,goalReportFromRecord,readGoalCapabilityRefusal,readGoalCommand,readGoalDeliveries,readGoalInspect,readGoalRefusal,readGoalReport,readGoalTransition,readRunGoal,type GoalContext,type GoalTransition} from './goal'
import {readRunJob,type JobDelivery} from './job'
import {readEffectAdmission,readLeaseFence,readRunLease,type RunLease} from './lease'
import {readRunWait,readWaitExport,type RunWait} from './wait'
import goalVector from '../../../contract/vectors/goal/goal.json'
import commandVector from '../../../contract/vectors/goal/command.json'
import refusalVector from '../../../contract/vectors/goal/refusal.json'
import reportVector from '../../../contract/vectors/goal/report.json'
import transitionVector from '../../../contract/vectors/goal/transitions.json'
import jobVector from '../../../contract/vectors/job/job.json'
import goalSchema from '../../../contract/schemas/run-goal.json'

/* #230: the shared goal vectors, read by this file, by go/context/run_goal_test.go and by tests/test_contract.py. */
describe('goal records: shared vectors (#230)',()=>{
 it('reads every record and refuses every record the vector refuses',()=>{
  goalVector.items.forEach((record,i)=>expect(canonicalJSON(readRunGoal(record)),goalVector.names[i]).toBe(canonicalJSON(record)))
  for(const row of [...goalVector.refused,...goalVector.readerRefused])expect(()=>readRunGoal(row.record),row.problem).toThrow(GoalRefused)
 })
 it('no other reader takes a goal record, and the goal reader takes no job record',()=>{
  for(const record of goalVector.items){
   // A reader refuses it by throwing, or, as readExecution may, by reading nothing.
   for(const read of [readRunJob,readRunWait,readWaitExport,readRunLease,readLeaseFence,readEffectAdmission,readRunReport,readRunEvent,(v:unknown)=>checkRecord('checkpoint',v),(v:unknown)=>{if(readExecution(v)===undefined)throw Error('read nothing')}])expect(()=>read(structuredClone(record))).toThrow()
  }
  for(const record of jobVector.items)expect(()=>readRunGoal(record)).toThrow(GoalRefused)
 })
 it('commands, their digests, transitions, inspections and deliveries are the vector\'s',()=>{
  commandVector.items.forEach((command,i)=>{expect(readGoalCommand(command)).toEqual(command);expect(goalCommandDigest(command as never)).toBe(commandVector.digests[i])})
  for(const row of [...commandVector.refused,...commandVector.readerRefused])expect(()=>readGoalCommand(row.record),row.problem).toThrow(GoalRefused)
  for(const row of commandVector.transitions)expect(readGoalTransition(row)).toEqual(row)
  for(const row of [...commandVector.transitionsRefused,...commandVector.transitionsReaderRefused])expect(()=>readGoalTransition(row.record),row.problem).toThrow(GoalRefused)
  for(const row of commandVector.inspects)expect(readGoalInspect(row)).toEqual(row)
  commandVector.deliveries.forEach((row,i)=>{expect(readGoalDeliveries(row)).toEqual(row);expect(goalDeliveryDigest(row as never)).toBe(commandVector.deliveryDigests[i])})
 })
 it('every refusal is named, and nothing else is',()=>{
  expect(refusalVector.items.map(r=>r.reason)).toEqual(GOAL_REFUSAL_REASONS)
  expect(refusalVector.capability.map(r=>r.reason)).toEqual(GOAL_CAPABILITY_REASONS)
  for(const row of refusalVector.items){expect(readGoalRefusal(row)).toEqual(row);expect(new GoalRefused(row.reason as never).record()).toEqual(row)}
  for(const row of refusalVector.capability){expect(readGoalCapabilityRefusal(row)).toEqual(row);expect(new GoalCapabilityRefused(row.reason as never).record()).toEqual(row)}
  for(const row of refusalVector.refused)expect(()=>readGoalRefusal(row.record)).toThrow(GoalRefused)
 })
 it('the report is the record\'s, read back by its reader',()=>{
  goalVector.items.forEach((record,i)=>{expect(canonicalJSON(goalReportFromRecord(record as never))).toBe(canonicalJSON(reportVector.items[i]));expect(canonicalJSON(readGoalReport(reportVector.items[i]))).toBe(canonicalJSON(reportVector.items[i]))})
 })
 it('every scenario of the vector, against the goal table',()=>{
  for(const scenario of transitionVector.scenarios){
   let at=0
   const table=new GoalTable(()=>new Date(at))
   scenario.steps.forEach((step:Record<string,any>,i)=>{
    at=Date.parse(step.at)
    const held=step.context??{},context:GoalContext={...(held.head?{head:{revision:held.head.revision,checkpoint:new TextEncoder().encode(held.head.checkpoint)}}:{}),waits:(held.waits??[]) as RunWait[],jobs:(held.jobs??[]) as never,...(held.lease?{lease:held.lease as RunLease}:{})}
    const run=()=>step.op==='inspect'?table.inspect(step.input):step.op==='deliver'?table.deliver(step.input):table.command(step.input as GoalTransition,()=>context)
    let caught:unknown,answer:unknown
    try{answer=run()}catch(error){caught=error}
    expect((caught as GoalRefused|undefined)?.reason,`${scenario.name}, step ${i}`).toBe(step.refused)
    if(!step.refused&&step.op==='command'&&held.lease)expect((answer as {lease?:unknown}).lease).toEqual(held.lease)
   })
   const record=table.read(scenario.resource)
   expect(canonicalJSON(record),scenario.name).toBe(canonicalJSON(scenario.final))
   expect(readRunGoal(record)).toEqual(record)
  }
 })
})

/* #321's review: the goal record keeps its own copy of the deliveries an abandon answers (goal.ts JOB_DELIVERIES). A value the job
 * adds that the copy lacks could never be journaled, and every later create would refuse abandon-unfinished. The record below is
 * typed by the job's own JobDelivery, so it names each value exactly once (the type check fails one missing or one extra), and the
 * goal's reader and its schema must take exactly those. */
const EVERY_DELIVERY:Record<JobDelivery,true>={sent:true,'already-admitted':true,failed:true,undelivered:true,'no-handle':true,settled:true,contradicted:true,acknowledged:true}
describe('goal records: the deliveries are the job\'s (#321)',()=>{
 it('journals every delivery an abandon can answer, refuses any other, and its schema names the same list',()=>{
  const job=`sha256:${'a'.repeat(64)}`,journal=(delivery:string)=>readGoalDeliveries({version:1,resource:'run-1',command:'cmd-1',deliveries:[{job,delivery}]})
  for(const delivery of Object.keys(EVERY_DELIVERY))expect(journal(delivery).deliveries,delivery).toEqual([{job,delivery}])
  for(const other of ['already-sent','cancelled',''])expect(()=>journal(other),other).toThrow(GoalRefused)
  expect([...goalSchema.$defs.delivery.properties.delivery.enum].sort()).toEqual(Object.keys(EVERY_DELIVERY).sort())
 })
})
