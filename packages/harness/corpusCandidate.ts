import {canonicalJSON} from './context/chat'
import {readRunFeedback} from './history/metadata'
import {readRunReportDisplay} from './runReportReader'
import {HistoryInputInvalid,HISTORY_OBJECT_BYTES,historyJSONBytes,historyJSONDigest,historyDigestPattern,readCanonicalHistoryJSON} from './history/codec'
import {storageId} from './storage/common'
import type {RunFeedback} from './history/types'
/** Pending human review material. This is never a replay entry or automatic corpus admission. */
export interface CorpusCandidate{
 version:1;id:string;status:'pending-review';runId:string;feedbackDigest:string;archiveDigest:string;sourceView:string
 feedback:RunFeedback;review:{report:unknown;messages:{sequence:number;text:string}[]};replay:{kind:'unavailable';reason:'not-recorded'}
}
export interface CorpusCandidateSink{writeCandidate(id:string,candidate:CorpusCandidate):Promise<void>|void}
export function corpusCandidateIdentity(runId:string,feedbackDigest:string,archiveDigest:string){return historyJSONDigest({runId,feedbackDigest,archiveDigest})}
export function readCorpusCandidate(bytes:Uint8Array):CorpusCandidate{
 try{const v=readCanonicalHistoryJSON(bytes,HISTORY_OBJECT_BYTES) as CorpusCandidate,closed=(v:unknown,keys:string[])=>v!==null&&typeof v==='object'&&!Array.isArray(v)&&Object.keys(v).length===keys.length&&keys.every(k=>Object.hasOwn(v,k))
 if(!closed(v,['version','id','status','runId','feedbackDigest','archiveDigest','sourceView','feedback','review','replay'])||v.version!==1||v.status!=='pending-review'||!storageId(v.runId)||!storageId(v.sourceView)||!historyDigestPattern.test(v.feedbackDigest)||!historyDigestPattern.test(v.archiveDigest)||v.id!==corpusCandidateIdentity(v.runId,v.feedbackDigest,v.archiveDigest)||!closed(v.review,['report','messages'])||!Array.isArray(v.review.messages)||v.review.messages.length>20||!closed(v.replay,['kind','reason'])||v.replay.kind!=='unavailable'||v.replay.reason!=='not-recorded')throw new HistoryInputInvalid()
 readRunFeedback(historyJSONBytes(v.feedback));if(v.feedback.runId!==v.runId)throw new HistoryInputInvalid();const report=readRunReportDisplay(v.review.report);if(report.id!==v.runId)throw new HistoryInputInvalid()
 let previous=0;for(const row of v.review.messages){if(!closed(row,['sequence','text'])||!Number.isSafeInteger(row.sequence)||row.sequence<=previous||typeof row.text!=='string'||row.text.length>2000)throw new HistoryInputInvalid();previous=row.sequence}
 if(v.feedback.target.kind==='assistant-message'&&(v.review.messages.length!==1||v.review.messages[0]!.sequence!==v.feedback.target.eventSequence))throw new HistoryInputInvalid()
 return v
 }catch{throw new HistoryInputInvalid()}
}
export function validateCandidateFeedback(candidate:CorpusCandidate,feedback:RunFeedback){
 const {remark:_,...source}=feedback,{remark:__,...display}=candidate.feedback
 if(candidate.feedbackDigest!==historyJSONDigest(feedback)||candidate.runId!==feedback.runId||canonicalJSON(source)!==canonicalJSON(display)||Object.hasOwn(feedback,'remark')!==Object.hasOwn(candidate.feedback,'remark'))throw new HistoryInputInvalid()
}
