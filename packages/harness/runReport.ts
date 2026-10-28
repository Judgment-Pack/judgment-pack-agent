import type {RepeatTools} from './repeatTools'
import type {PolicyMembers} from './policy'
import {canonicalJSON} from './context/chat'
import {usageTotals,type UsageTotals} from './storage/usageJournal'
import {utf8Bytes} from './context/policy'
import {readExecution,type ExecutionCheckpoint} from './execution/state'
import {readStoredRunEvent} from './storage/writer'
import type {RunHead,RunEventPage} from './storage/run'
import {storageId,STORAGE_PAGE_LIMIT} from './storage/common'

export const MAX_RUN_REPORT_BYTES=4*1024*1024
export const MAX_RUN_REPORT_EVENTS=400
/** Each intended source is copied whole, in recorded array order. */
export const RUN_REPORT_SOURCES=['id','phase','status','unit','used','limit','requestCap','grants','reason','explanation','prompts','sources','sourceIdentities','skills','critic','delegation','usage','childUsage','failover','completions','plan','planState'] as const
/** Internal state is deliberately outside this presentation contract. */
export const RUN_REPORT_EXCLUDED=['schema','digest','retainUntil','records','requestKey','operations','context','testCalls','durable','handoff','approval','outputGuard','memories','draft','policy','crossProvider','compaction','steers','usageBudget','turnRoute','repeatTools'] as const
export type RunReportFacts=Pick<ExecutionCheckpoint,typeof RUN_REPORT_SOURCES[number]>
export type RunReport={version:1;checkpointVersion:1|2}&RunReportFacts
export type RunReportV2={version:2;checkpointVersion:1|2|3;feedback?:import('./history/report').HistoryFeedbackView[];usageTotals?:UsageTotals;route?:ExecutionCheckpoint['turnRoute'];repeatTools?:RepeatToolsReport}&RunReportFacts
export interface RepeatToolsReport {executions:Omit<RepeatTools['executions'][number],'seen'>[];policy:Pick<PolicyMembers,'repeatTools.mode'|'repeatTools.remindAt'|'repeatTools.stopAt'>}
/** Only admitted state and its actual three policy members reach the presentation. */
export function projectRepeatTools(checkpoint:Pick<ExecutionCheckpoint,'version'|'repeatTools'|'policy'>):RepeatToolsReport|undefined {
 if(checkpoint.version!==3||!checkpoint.repeatTools)return undefined
 const policy=checkpoint.policy
 if(!policy||policy['repeatTools.mode']?.value!=='on'||!policy['repeatTools.remindAt']||!policy['repeatTools.stopAt'])throw new RunReportInputInvalid()
 return {executions:checkpoint.repeatTools.executions.map(({execution,remindAt,stopAt,count,reminded,reminderDelivered,stopped})=>({execution,remindAt,stopAt,count,reminded,reminderDelivered,stopped})),policy:{'repeatTools.mode':structuredClone(policy['repeatTools.mode']),'repeatTools.remindAt':structuredClone(policy['repeatTools.remindAt']),'repeatTools.stopAt':structuredClone(policy['repeatTools.stopAt'])}}
}
/** Consumers narrow on version before reading feature-bearing totals. */
export type RetainedRunReport=RunReport|RunReportV2
export class RunReportInputInvalid extends Error {constructor(){super('RunReportInputInvalid: a valid complete or stopped checkpoint is required.');this.name='RunReportInputInvalid'}}
export class RunReportPrefixInvalid extends Error {constructor(){super('RunReportPrefixInvalid: the supplied event prefix does not match the terminal head.');this.name='RunReportPrefixInvalid'}}
export class RunReportOverflow extends Error {constructor(){super('RunReportOverflow: the report or retained event prefix exceeds its bounds.');this.name='RunReportOverflow'}}
function terminal(value:unknown):ExecutionCheckpoint {
 try{const held=readExecution(value);if(held&&(held.status==='complete'||held.status==='stopped'))return held}catch{}
 throw new RunReportInputInvalid()
}
/** Pure projection: no clock, store access, totals, default zeros, or inferred provider facts. */
export function projectRunReport(value:ExecutionCheckpoint):RetainedRunReport {
 const checkpoint=terminal(value)
 const feature=checkpoint.version===3
 const out:Record<string,unknown>={version:feature?2:1,checkpointVersion:checkpoint.version,...(feature&&checkpoint.usageBudget?{usageTotals:usageTotals(checkpoint.usageBudget)}:{}),...(feature&&checkpoint.turnRoute?{route:structuredClone(checkpoint.turnRoute)}:{})}
 for(const name of RUN_REPORT_SOURCES)if(Object.hasOwn(checkpoint,name))out[name]=structuredClone(checkpoint[name])
 const repeatTools=projectRepeatTools(checkpoint);if(repeatTools)out.repeatTools=repeatTools
 const report=out as RetainedRunReport
 canonicalRunReport(report)
 return report
}
/** Canonical UTF-8 text shared byte for byte with Go. This serializer does not validate retained input; use readRunReport. */
export function canonicalRunReport(report:RetainedRunReport):string {
 const text=canonicalJSON(report)
 if(utf8Bytes(text)>MAX_RUN_REPORT_BYTES)throw new RunReportOverflow()
 return text
}
/** Validate supplied pages against a terminal head. Cursor values are opaque.
 * The caller fetches from null, then each returned cursor, within one host-bound resource.
 * Pages carry no predecessor proof: validation cannot prove completeness or authenticate the host.
 * Events corroborate the checkpoint and are not exposed as raw journal/request payloads.
 */
export function buildRunReport(checkpoint:ExecutionCheckpoint,head:RunHead,pages:readonly RunEventPage[]):RetainedRunReport {
 const report=projectRunReport(checkpoint),expected=canonicalJSON(checkpoint)
 const invalid=():never=>{throw new RunReportPrefixInvalid()}
 if(!head||head.runId!==checkpoint.id||!storageId(head.runId)||!storageId(head.revision)||!storageId(head.commitId)||!storageId(head.cursor)||!head.checkpoint||head.checkpoint.length>MAX_RUN_REPORT_BYTES)return invalid()
 try{if(canonicalJSON(terminal(JSON.parse(new TextDecoder('utf-8',{fatal:true}).decode(head.checkpoint))))!==expected)return invalid()}catch{return invalid()}
 if(!Array.isArray(pages)||pages.length===0)return invalid()
 if(pages.length>MAX_RUN_REPORT_EVENTS)throw new RunReportOverflow()
 let count=0,bytes=0,last:string|null=null,root:string|undefined
 const cursors=new Set<string>()
 for(let at=0;at<pages.length;at++){
  const page=pages[at]!
  if(!page||!Array.isArray(page.items)||page.items.length===0||page.items.length>STORAGE_PAGE_LIMIT||page.hasMore!==(at<pages.length-1))return invalid()
  for(const row of page.items){
   if(++count>MAX_RUN_REPORT_EVENTS)throw new RunReportOverflow()
   if(!row||!storageId(row.cursor)||cursors.has(row.cursor)||!ArrayBuffer.isView(row.bytes)||Object.prototype.toString.call(row.bytes)!=='[object Uint8Array]')return invalid()
   cursors.add(row.cursor);last=row.cursor;bytes+=row.bytes.length
   if(bytes>MAX_RUN_REPORT_BYTES)throw new RunReportOverflow()
   let held:ReturnType<typeof readStoredRunEvent>
   try{held=readStoredRunEvent(row.bytes)}catch{return invalid()}
   const {scope,event}=held
   if(event.type==='execution_checkpoint'||event.type==='budget_paused'){
    try{if(!readExecution(event.checkpoint)||event.checkpoint.id!==scope.execution)return invalid()}catch{return invalid()}
   }
   if(scope.task===undefined){
    if(scope.execution!==checkpoint.id)return invalid()
    if(event.type==='execution_checkpoint'||event.type==='budget_paused'){
     if(event.checkpoint.id!==checkpoint.id)return invalid()
     root=canonicalJSON(event.checkpoint)
    }
   }else if(scope.execution!==scope.task)return invalid()
  }
  if(page.nextCursor!==last)return invalid()
 }
 if(last!==head.cursor||root!==expected)return invalid()
 return report
}

export {readRunReport,RunReportInvalid,MAX_RUN_REPORT_DEPTH,MAX_RUN_REPORT_NODES} from './runReportReader'
