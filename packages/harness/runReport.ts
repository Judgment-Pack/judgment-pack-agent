import {canonicalJSON} from './context/chat'
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
export const RUN_REPORT_EXCLUDED=['schema','digest','retainUntil','records','requestKey','operations','context','testCalls','durable','handoff','approval','outputGuard','memories','draft','policy','crossProvider','compaction','steers'] as const
export type RunReport={version:1;checkpointVersion:ExecutionCheckpoint['version']}&Pick<ExecutionCheckpoint,typeof RUN_REPORT_SOURCES[number]>
export class RunReportInputInvalid extends Error {constructor(){super('RunReportInputInvalid: a valid complete or stopped checkpoint is required.');this.name='RunReportInputInvalid'}}
export class RunReportPrefixInvalid extends Error {constructor(){super('RunReportPrefixInvalid: the supplied event prefix does not match the terminal head.');this.name='RunReportPrefixInvalid'}}
export class RunReportOverflow extends Error {constructor(){super('RunReportOverflow: the report or retained event prefix exceeds its bounds.');this.name='RunReportOverflow'}}
function terminal(value:unknown):ExecutionCheckpoint {
 try{const held=readExecution(value);if(held&&(held.status==='complete'||held.status==='stopped'))return held}catch{}
 throw new RunReportInputInvalid()
}
/** Pure projection: no clock, store access, totals, default zeros, or inferred provider facts. */
export function projectRunReport(value:ExecutionCheckpoint):RunReport {
 const checkpoint=terminal(value)
 const out:Record<string,unknown>={version:1,checkpointVersion:checkpoint.version}
 for(const name of RUN_REPORT_SOURCES)if(Object.hasOwn(checkpoint,name))out[name]=structuredClone(checkpoint[name])
 const report=out as RunReport
 canonicalRunReport(report)
 return report
}
/** Canonical UTF-8 text shared byte for byte with Go. */
export function canonicalRunReport(report:RunReport):string {
 const text=canonicalJSON(report)
 if(utf8Bytes(text)>MAX_RUN_REPORT_BYTES)throw new RunReportOverflow()
 return text
}
/** Validate supplied pages against a terminal head. Cursor values are opaque.
 * The caller fetches from null, then each returned cursor, within one host-bound resource.
 * Pages carry no predecessor proof: validation cannot prove completeness or authenticate the host.
 * Events corroborate the checkpoint and are not exposed as raw journal/request payloads.
 */
export function buildRunReport(checkpoint:ExecutionCheckpoint,head:RunHead,pages:readonly RunEventPage[]):RunReport {
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
