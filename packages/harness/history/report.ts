import {readExecution} from '../execution/state'
import {projectRunReport,type RetainedRunReport,type RunReportV2,type RunReportV3} from '../runReport'
import {readRunReport} from '../runReportReader'
import {HistoryInputInvalid,HISTORY_OBJECT_BYTES,readCanonicalHistoryJSON,historyJSONDigest} from './codec'
import type {RetainedRunHistory} from './view'
import type {RunFeedback} from './types'
export interface HistoryFeedbackView{sourceDigest:string;record:RunFeedback}
/** Metadata alone upgrades an old checkpoint's presentation; the actual checkpoint version is preserved. */
export function projectHistoryRunReport(view:RetainedRunHistory):RetainedRunReport{
 const head=view.head;if(!head.checkpoint)throw new HistoryInputInvalid();const checkpoint=readExecution(readCanonicalHistoryJSON(view.readObject(head.checkpoint),HISTORY_OBJECT_BYTES,'finite'));if(!checkpoint)throw new HistoryInputInvalid()
 const base=projectRunReport(checkpoint),feedback=view.metadata.flatMap(row=>row.record.kind==='feedback'?[{sourceDigest:historyJSONDigest(row.record.feedback),record:row.record.feedback}]:[])
 if(feedback.length===0)return base
 // ADR-0029: a delegated run's report stays version 3.
 return readRunReport({...base,version:base.version===3?3:2,checkpointVersion:checkpoint.version,feedback}) as RunReportV2|RunReportV3
}
