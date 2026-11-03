import {describe,it,expect} from 'vitest'
import {canonicalJSON} from '../context/chat'
import {jobCancelCard,jobCancelInput,jobCancelNotSent,jobCancelOperation,jobCancelResult,jobCancelUnrecorded,readJobCancelAcknowledgement,readJobCancelArgument,readJobCancelAsk,type JobCancelAcknowledgement,type JobCancelAsk,JOB_CONTINUATION,JOB_GENERATION,JOB_REFUSAL_REASONS,JOB_CAPABILITY_REASONS,JobCapabilityRefused,JobRefused,JobTable,gatewayActionMembers,gatewayRefusalFinding,jobContinuation,jobEvidenceDigest,jobIdentity,jobKeyDigest,jobProjection,jobReportFromExport,jobStartResult,readJobAnswer,readJobArgument,readJobCapabilityRefusal,readJobCompletion,readJobExport,readJobReconciliation,readJobRefusal,readJobStartAnswer,readRunJob,validJobKey,type JobAnswer,type JobCompletionEvidence,type JobReconciliation,type JobRegistration,type RunJob} from './job'
import jobVector from '../../../contract/vectors/job/job.json'
import answerVector from '../../../contract/vectors/job/answer.json'
import reconciliationVector from '../../../contract/vectors/job/reconciliation.json'
import completionVector from '../../../contract/vectors/job/completion.json'
import cancelVector from '../../../contract/vectors/job/cancel.json'
import gatewayVector from '../../../contract/vectors/job/gateway.json'
import reportVector from '../../../contract/vectors/job/report.json'
import refusalVector from '../../../contract/vectors/job/refusal.json'
import transitionVector from '../../../contract/vectors/job/transitions.json'
import modelVector from '../../../contract/vectors/job/model.json'

/* #229: the shared job vectors, read by this file, by go/context/run_job_test.go and by tests/test_contract.py. */
const T0=Date.parse('2026-01-01T00:00:00.000Z')

describe('job records and evidence: shared vectors (#229)',()=>{
 it('reads every record and refuses every record the vector refuses',()=>{
  jobVector.items.forEach((record,i)=>expect(canonicalJSON(readRunJob(record)),jobVector.names[i]).toBe(canonicalJSON(record)))
  for(const row of [...jobVector.refused,...jobVector.readerRefused])expect(()=>readRunJob(row.record),row.problem).toThrow(JobRefused)
 })
 it('keys, identities and evidence digests are the vector\'s, computed apart from the package',()=>{
  for(const row of jobVector.identities)expect(jobIdentity(row.resource,row.operation)).toBe(row.job)
  for(const row of jobVector.keys){expect(jobKeyDigest(row.key)).toBe(row.digest);expect(validJobKey(row.key)).toBe(true)}
  for(const key of jobVector.keysRefused)expect(validJobKey(key),key).toBe(false)
  answerVector.items.forEach((record,i)=>expect(jobEvidenceDigest(readJobAnswer(record))).toBe(answerVector.digests[i]))
  reconciliationVector.items.forEach((record,i)=>expect(jobEvidenceDigest(readJobReconciliation(record))).toBe(reconciliationVector.digests[i]))
  completionVector.items.forEach((record,i)=>expect(jobEvidenceDigest(readJobCompletion(record))).toBe(completionVector.digests[i]))
  for(const row of [...answerVector.refused,...answerVector.readerRefused])expect(()=>readJobAnswer(row.record),row.problem).toThrow(JobRefused)
  for(const row of [...reconciliationVector.refused,...reconciliationVector.readerRefused])expect(()=>readJobReconciliation(row.record),row.problem).toThrow(JobRefused)
  for(const row of [...completionVector.refused,...completionVector.readerRefused])expect(()=>readJobCompletion(row.record),row.problem).toThrow(JobRefused)
  cancelVector.items.forEach((row,i)=>{expect(readJobCancelAcknowledgement(row)).toEqual(row);expect(jobEvidenceDigest(readJobCancelAcknowledgement(row))).toBe(cancelVector.digests[i])})
  for(const row of [...cancelVector.refused,...cancelVector.readerRefused])expect(()=>readJobCancelAcknowledgement(row.record),row.problem).toThrow(JobRefused)
  for(const row of cancelVector.asks)expect(readJobCancelAsk(row)).toEqual(row)
  for(const row of cancelVector.asksRefused)expect(()=>readJobCancelAsk(row.record),row.problem).toThrow(JobRefused)
  for(const row of cancelVector.operations)expect(jobCancelOperation(row.command,row.job)).toBe(row.operation)
 })
 it('every refusal is named, and nothing else is',()=>{
  expect(refusalVector.items.map(r=>r.reason)).toEqual(JOB_REFUSAL_REASONS)
  expect(refusalVector.capability.map(r=>r.reason)).toEqual(JOB_CAPABILITY_REASONS)
  for(const row of refusalVector.items){expect(readJobRefusal(row)).toEqual(row);expect(new JobRefused(row.reason as never).record()).toEqual(row)}
  for(const row of refusalVector.capability){expect(readJobCapabilityRefusal(row)).toEqual(row);expect(new JobCapabilityRefused(row.reason as never).record()).toEqual(row)}
  for(const row of refusalVector.refused)expect(()=>readJobRefusal(row)).toThrow(JobRefused)
 })
 it('every transition of the vector, against the job table',()=>{
  for(const scenario of transitionVector.scenarios){
   let at=T0
   const table=new JobTable(()=>new Date(at))
   const name=(i:number)=>`${scenario.name}, step ${i}`
   scenario.steps.forEach((step:Record<string,any>,i)=>{
    at=Date.parse(step.at)
    const fence=step.fence?{version:1 as const,...step.fence}:undefined
    const run=()=>{
     if(step.op==='register'){const resource=fence!.resource;table.register(resource,table.admit(resource,step.registration as JobRegistration,false),fence!);return}
     if(step.op==='answer'){table.answer(step.answer as JobAnswer,fence!);return}
     if(step.op==='reconcile'){table.reconcile(step.evidence as JobReconciliation);return}
     if(step.op==='reclaim'){table.reclaim(fence!.resource,step.job,fence!);return}
     if(step.op==='cancel'){table.requestCancel(step.ask as JobCancelAsk,fence);return}
     if(step.op==='acknowledge'){table.acknowledgeCancel(step.evidence as JobCancelAcknowledgement);return}
     const done=table.complete(step.evidence as JobCompletionEvidence)
     if(step.wakes!==undefined)expect(done.wake!==undefined,name(i)).toBe(step.wakes)
    }
    if(step.refused){let caught:unknown;try{run()}catch(error){caught=error};expect((caught as JobRefused)?.reason,name(i)).toBe(step.refused)}
    else run()
   })
   const jobs=table.export('run-j').jobs
   if(scenario.final===null)expect(jobs,scenario.name).toEqual([])
   else{expect(canonicalJSON(jobs[0]),scenario.name).toBe(canonicalJSON(scenario.final));readRunJob(jobs[0])}
  }
 })
 it('what the model reads, a start\'s composed result, the continuation and the gateway members are the vector\'s',()=>{
  for(const row of modelVector.projections)expect(canonicalJSON(jobProjection(row.record as RunJob))).toBe(canonicalJSON(row.projection))
  for(const row of modelVector.startResults){const result=jobStartResult(row.record as RunJob);expect(result.content![0]!.text).toBe(row.text);expect(result.isError===true).toBe(row.isError)}
  expect(JOB_CONTINUATION).toBe(modelVector.continuationLine)
  const marked=jobContinuation(modelVector.projections[3]!.record as RunJob,'marker')
  expect(marked.startsWith(`${modelVector.continuationLine}\n`)).toBe(true);expect(marked).toContain(canonicalJSON(modelVector.projections[3]!.projection))
  for(const row of modelVector.arguments)expect(readJobArgument(JSON.parse(row.raw))).toBe(row.job)
  for(const row of modelVector.argumentsRefused)expect(()=>readJobArgument(JSON.parse(row.raw)),row.name).toThrow(JobRefused)
  for(const row of modelVector.startAnswers)expect(readJobStartAnswer(row.answer)).toEqual(row.answer)
  for(const row of modelVector.cancelResults){const result=jobCancelResult(row.record as RunJob,(row as {sent?:boolean}).sent!==false);expect(result.content![0]!.text).toBe(row.text);expect(result.isError===true).toBe(row.isError)}
  for(const row of modelVector.cancelCards)expect(jobCancelCard(row.record as RunJob)).toEqual(row.card)
  for(const row of modelVector.cancelInputs)expect(jobCancelInput(row.record as RunJob,row.operation)).toEqual(row.input)
  for(const row of modelVector.cancelArguments)expect(readJobCancelArgument(JSON.parse(row.raw))).toBe(row.job)
  for(const row of modelVector.cancelArgumentsRefused)expect(()=>readJobCancelArgument(JSON.parse(row.raw)),row.name).toThrow(JobRefused)
  expect(jobCancelUnrecorded().failure).toEqual(modelVector.cancelFailures.unrecorded)
  expect(jobCancelNotSent().failure).toEqual(modelVector.cancelFailures.notSent)
  for(const row of modelVector.startAnswersKept)expect(readJobStartAnswer(row.answer),row.name).toEqual(row.read)
  for(const row of modelVector.startAnswersRefused)expect(()=>readJobStartAnswer(row.answer),row.name).toThrow(JobRefused)
  for(const row of gatewayVector.cases)expect(gatewayActionMembers(row.job as RunJob,row.reconcile?{reconcile:true}:{}),row.name).toEqual(row.members)
  for(const row of gatewayVector.findings)expect(gatewayRefusalFinding(row.refusal),row.refusal).toBe(row.finding)
  reportVector.exports.forEach((source,i)=>expect(canonicalJSON(jobReportFromExport(source as never))).toBe(canonicalJSON(reportVector.items[i])))
  for(const row of reportVector.exportsRefused)expect(()=>readJobExport(row.record),row.name).toThrow(JobRefused)
  expect(JOB_GENERATION).toBe(1)
 })
})
