import {StorageConflict} from '../storage/common'
import {canonicalJSON} from '../context/chat'
import {OutputGuard} from '../outputGuard'
import type {PolicyMembers} from '../policy'
import {corpusCandidateIdentity,readCorpusCandidate,validateCandidateFeedback,type CorpusCandidate,type CorpusCandidateSink} from '../corpusCandidate'
import type {HistoryRunStore,HistoryMetadataCommit,RunHistoryHead} from '../storage/history'
import {readExecution} from '../execution/state'
import {readHistoryHead,readMetadataEntry,readRetainedRunHistory,copyHistoryEnvelope} from './view'
import {queryHistoryEvents} from './query'
import {exportRunArchive,readRunArchive} from './archive'
import {readRunFeedback} from './metadata'
import type {FeedbackTarget} from './types'
import {HistoryInputInvalid,HistoryChanged,HISTORY_OBJECT_BYTES,historyJSONBytes,historyJSONDigest,historyObject,readCanonicalHistoryJSON,type HistoryObjectRef} from './codec'
export interface RunFeedbackInput{runId:string;id:string;commitId:string;expectedHistoryRevision:string;expectedMetadataRevision:string|null;previous:string|null;target:FeedbackTarget;rating:'good'|'bad';remark?:string;actor:string;recordedAt:string}
export interface PreparedRunFeedback{commit:HistoryMetadataCommit;source:RunHistoryHead;candidate?:CorpusCandidate}
export class FeedbackOutcomeUnknown extends Error{constructor(){super('FeedbackOutcomeUnknown: metadata acknowledgement could not be verified.');this.name='FeedbackOutcomeUnknown'}}
function invalid():never{throw new HistoryInputInvalid()}
const reviewText=(value:string)=>{const text=value.slice(0,2000);return /[\uD800-\uDBFF]$/.test(text)?text.slice(0,-1):text}
/** Prepare all sanitized material before publication. Nothing calls a provider or changes execution. */
export async function prepareRunFeedback(store:HistoryRunStore,input:RunFeedbackInput,options:{members?:PolicyMembers;proposeGood?:boolean;signal?:AbortSignal}={}):Promise<PreparedRunFeedback>{
 const v=copyHistoryEnvelope(input) as RunFeedbackInput
 if(!v||Object.keys(v).some(k=>!['runId','id','commitId','expectedHistoryRevision','expectedMetadataRevision','previous','target','rating','remark','actor','recordedAt'].includes(k)))invalid()
 const view=await readRetainedRunHistory(store,v.runId,options.signal),source=view.head
 if(source.revision!==v.expectedHistoryRevision||(source.metadata?.revision??null)!==v.expectedMetadataRevision)throw new HistoryChanged()
 if(!source.checkpoint)invalid();const checkpoint=readExecution(readCanonicalHistoryJSON(view.readObject(source.checkpoint),HISTORY_OBJECT_BYTES,'finite'));if(!checkpoint)invalid()
 const previous=view.metadata.map(row=>row.record).filter(row=>row.kind==='feedback'&&row.feedback.id===v.id).at(-1),prior=previous?.kind==='feedback'?previous.feedback:undefined
 if(v.previous!==(prior?historyJSONDigest(prior):null)||prior&&canonicalJSON(prior.target)!==canonicalJSON(v.target))invalid()
 const feedback=readRunFeedback(historyJSONBytes({version:1,id:v.id,runId:v.runId,sequence:(prior?.sequence??0)+1,previous:v.previous,target:v.target,rating:v.rating,...(Object.hasOwn(v,'remark')?{remark:v.remark}:{}),actor:v.actor,recordedAt:v.recordedAt}))
 if(feedback.target.kind==='assistant-message'){const page=queryHistoryEvents(view,{afterSequence:feedback.target.eventSequence-1,limit:1}),row=page.items[0];if(!row||row.sequence!==feedback.target.eventSequence||row.source.digest!==feedback.target.eventDigest||row.event.type!=='message')invalid()}
 let candidate:CorpusCandidate|undefined,candidateRef:HistoryObjectRef|undefined
 const candidateRequested=feedback.rating==='bad'||options.proposeGood===true
 if(candidateRequested&&['complete','stopped'].includes(checkpoint.status)){
  const exported=await exportRunArchive(store,v.runId,{members:options.members,signal:options.signal});if(exported.manifest.source.revision!==source.revision)throw new HistoryChanged();const archived=readRunArchive(exported.bytes,exported.digest),guard=new OutputGuard({...options.members,outputGuard:{value:'redact'} as never},()=>({}),()=>{}),feedbackDigest=historyJSONDigest(feedback)
  const cleanFeedback=guard.value(feedback,'tool argument',false);const {remark:_,...before}=feedback,{remark:__,...after}=cleanFeedback;if(canonicalJSON(before)!==canonicalJSON(after))invalid()
  const messages=queryHistoryEvents(archived,{types:['message'],...(feedback.target.kind==='assistant-message'?{afterSequence:feedback.target.eventSequence-1,limit:1}:{limit:20})}).items.map(row=>({sequence:row.sequence,text:reviewText(String((row.event as {text:string}).text))}))
  candidate=readCorpusCandidate(historyJSONBytes({version:1,id:corpusCandidateIdentity(v.runId,feedbackDigest,exported.digest),status:'pending-review',runId:v.runId,feedbackDigest,archiveDigest:exported.digest,sourceView:source.revision,feedback:cleanFeedback,review:{report:archived.readDisplay(archived.manifest.report,'report').document,messages},replay:{kind:'unavailable',reason:'not-recorded'}}));validateCandidateFeedback(candidate,feedback)
  const object=await historyObject(historyJSONBytes(candidate));await store.stageHistoryObjects({kind:'run',id:v.runId},[object],options.signal);candidateRef={digest:object.digest,bytes:object.bytes}
 }
 return {commit:{runId:v.runId,expectedHistoryRevision:v.expectedHistoryRevision,expectedMetadataRevision:v.expectedMetadataRevision,commitId:v.commitId,record:{version:1,kind:'feedback',feedback,...(candidateRequested?{candidateRequested:true as const}:{}),...(candidateRef?{candidate:candidateRef}:{})}},source,candidate}
}
/** An explicit replay of this same prepared commit uses the store's original idempotency identity. */
export async function commitPreparedRunFeedback(store:HistoryRunStore,prepared:PreparedRunFeedback,signal?:AbortSignal):Promise<{head:RunHistoryHead;candidate?:CorpusCandidate;candidateDeferred?:true;superseded?:true}>{
 const source=readHistoryHead(prepared.source),commit=copyHistoryEnvelope(prepared.commit) as HistoryMetadataCommit
 if(commit.runId!==source.runId||commit.expectedHistoryRevision!==source.revision||commit.expectedMetadataRevision!==(source.metadata?.revision??null)||commit.record.kind!=='feedback')invalid()
 let receipt:unknown
 try{receipt=await store.commitHistoryMetadata(commit,signal)}catch(error){if(error instanceof StorageConflict||error instanceof HistoryInputInvalid)throw error;throw new FeedbackOutcomeUnknown()}
 try{
 const head=readHistoryHead(receipt)
 const {revision:_,metadata:__,...before}=source,{revision:___,metadata:____,...after}=head
 if(canonicalJSON(before)!==canonicalJSON(after)||head.revision===source.revision||!head.metadata||head.metadata.count!==(source.metadata?.count??0)+1||head.metadata.revision===source.metadata?.revision)invalid()
 const afterSequence=source.metadata?.count??0,page=copyHistoryEnvelope(await store.readHistoryMetadataPage(source.runId,head.revision,afterSequence,1,signal)) as {revision:string;afterSequence:number;items:unknown[];nextSequence:number;hasMore:boolean;total:number}
 if(Object.keys(page).sort().join(',')!=='afterSequence,hasMore,items,nextSequence,revision,total'||page.revision!==head.revision||page.afterSequence!==afterSequence||!Array.isArray(page.items)||page.items.length!==1||page.nextSequence!==afterSequence+1||page.hasMore!==false||page.total!==head.metadata.count)invalid()
 const row=readMetadataEntry(page.items[0]);if(row.sequence!==afterSequence+1||row.previousDigest!==(source.metadata?.root??null)||row.digest!==head.metadata.root||row.executionRevision!==source.executionRevision||row.commitSequence!==source.commitCount||canonicalJSON(row.record)!==canonicalJSON(commit.record))invalid()
 let candidate:CorpusCandidate|undefined
 if(commit.record.candidate){const ref=commit.record.candidate,object=await store.readHistoryObject({kind:'run',id:source.runId},head.revision,ref.digest,0,HISTORY_OBJECT_BYTES,signal);if(object.digest!==ref.digest||object.total!==ref.bytes||object.offset!==0||object.nextOffset!==ref.bytes||object.hasMore)invalid();const held=await historyObject(object.value);if(held.digest!==ref.digest||held.bytes!==ref.bytes)invalid();candidate=readCorpusCandidate(held.value);validateCandidateFeedback(candidate,commit.record.feedback)}
 const feedbackId=commit.record.feedback.id,current=await readRetainedRunHistory(store,source.runId,signal),latest=current.metadata.map(row=>row.record).filter(row=>row.kind==='feedback'&&row.feedback.id===feedbackId).at(-1)
 if(!latest||latest.kind!=='feedback'||historyJSONDigest(latest.feedback)!==historyJSONDigest(commit.record.feedback))return {head,superseded:true}
 return {head,candidate,...(commit.record.candidateRequested&&!candidate?{candidateDeferred:true as const}:{})}
 }catch{throw new FeedbackOutcomeUnknown()}
}
export async function writeRunFeedback(store:HistoryRunStore,input:RunFeedbackInput,options:{members?:PolicyMembers;proposeGood?:boolean;sink?:CorpusCandidateSink;signal?:AbortSignal}={}){
 const prepared=await prepareRunFeedback(store,input,options),published=await commitPreparedRunFeedback(store,prepared,options.signal)
 let delivery:'not-requested'|'deferred'|'superseded'|'pending'|'delivered'|'unknown'=published.superseded?'superseded':published.candidateDeferred?'deferred':published.candidate?'pending':'not-requested'
 if(published.candidate&&options.sink){try{await options.sink.writeCandidate(published.candidate.id,structuredClone(published.candidate));delivery='delivered'}catch{delivery='unknown'}}
 return {...published,delivery}
}
