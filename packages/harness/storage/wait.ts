import {AIFailureError,failure} from '../failures'
import {canonicalJSON} from '../context/chat'
import {sha256} from '../records'
import {readExecution} from '../execution/state'
import {readUserQuestion,validQuestionDraft,type QuestionDraft,type UserQuestion} from '../questions'
import {readStoredRunEvent} from './writer'
import {storageId,STORAGE_PAGE_LIMIT} from './common'
import {sameBinding} from '../principal'
import {LeaseRefused,attachReauthorizedDispatch,fenceOf,validConnectionReference,readLeaseFence,readRunLease,reauthDispositionOf,trustedReauthorization,unattendedRunStore,useReauthorization,validLeaseEpoch,type EffectHost,type LeaseAcquire,type LeaseFence,type LeaseRunStore,type LeaseTable,type RunLease,type ReauthDisposition,type ReauthorizationRequired} from './lease'
import {ReauthorizationRefused,permitted,readReauthDisposition,readReauthorized,reauthorize,type Reauthorized} from './reauth'
import type {StorageRevision} from './common'
import type {RunStore} from './run'
import type {ChatStore} from './chat'
import {ResumeRefused,approvalBinding,pinsDigest,pinsFrom,readApprovalBinding,readResumePins,readRevalidation,readRunLimit,readWaitDeadline,readWaitLimits,revalidateResume,stampDeadline,type ApprovalBinding,type PinnedModel,type ResumePins,type Revalidation,type RunLimit,type WaitDeadline,type WaitLimits} from './resume'
/* Durable waits, attention and the park/wake report (ADR-0025 decisions 2 and 6, #224 and #228;
 * contract/schemas/run-wait.json), with the deadlines, approval expiry and resume revalidation of decisions 3 and 5
 * (#225, #227; contract/schemas/run-resume.json). The package validates and records: a park reads the pending decision
 * and the pins from the run's retained checkpoint or event log, and the store commits the wait and its one
 * needs_attention event and releases the run's lease in one transaction. Until the wait is resumed it holds the run: no
 * lease of the run is granted but the resume's own claim. The host owns scheduling, clocks, wake delivery, keys,
 * notifications and processes; nothing here polls, schedules or sends. A wake is never an approval, a budget grant or an
 * answer of another reason. The Go twin is go/context/run_wait.go. */
export const WAIT_RECORD_VERSION=1 as const
export type WaitReason='approval'|'budget'|'question'|'reauthorization'
export type WaitState='parked'|'woken'|'timed-out'|'resumed'|'expired'
export type WaitLifetime='durable'|'process'
export type WaitRefusalReason='invalid'|'nothing-pending'|'wait-pending'|'conflict'|'checkpoint-changed'|'no-wait'|'unauthenticated'|'mismatch'|'not-due'|'stale'|'unacknowledged'|'already-resumed'|'decision-required'|'timed-out'|'release-uncertain'|'chat-changed'|'deadline-passed'|'closed'|'approval-ttl-required'|'key-reused'|'not-resumable'
export type WaitCapabilityReason='store-unsupported'|'store-lifetime'|'chat-store-unsupported'
export const WAIT_REFUSAL_REASONS:readonly WaitRefusalReason[]=['invalid','nothing-pending','wait-pending','conflict','checkpoint-changed','no-wait','unauthenticated','mismatch','not-due','stale','unacknowledged','already-resumed','decision-required','timed-out','release-uncertain','chat-changed','deadline-passed','closed','approval-ttl-required','key-reused','not-resumable']
export const WAIT_CAPABILITY_REASONS:readonly WaitCapabilityReason[]=['store-unsupported','store-lifetime','chat-store-unsupported']
const REASONS=['approval','budget','question','reauthorization'] as const
/** At most this many release entries per park, and waits per export page. */
export const WAIT_RELEASE_LIMIT=16
export const WAIT_EXPORT_LIMIT=1024

export interface EventCondition {kind:string;subject:string}
export interface WaitCondition {event?:EventCondition;wakeAt?:string}
export type PendingDecision=
 |{kind:'approval';callId:string;name:string;tool:string}
 |{kind:'budget';unit:'model-requests';used:number;limit:number}
 |{kind:'question';event:string;questionDigest:string;question?:UserQuestion}
 /** #226: the never-admitted operation of a trusted disposition: its dispatch claim id, source and tool, and the connection. */
 |{kind:'reauthorization';operation:string;source:string;tool:string;connection:string;cause:'expired'|'revoked'}
export interface ReleaseEvidence {resource:string;outcome:'released'|'not-held'|'uncertain'}
export interface WaitActor {owner:string;epoch:string;at:string}
export interface WakeDisposition {evidence:string;kind:'event'|'time'|'timeout'|'deadline';at:string;digest:string}
export interface QuestionResolution {kind:'answered'|'unanswered';at:string;actor?:string;answerDigest?:string;answer?:QuestionDraft}
export interface RunWait {
 version:1;resource:string;wait:string;reason:WaitReason;pending:PendingDecision;condition:WaitCondition;timeoutAt?:string
 /** #225: the host's deadline policy for this park, the deadline the store stamped, and an approval wait's binding. */
 limits?:WaitLimits;deadline?:WaitDeadline;approval?:ApprovalBinding
 continuation:string;checkpoint:{revision:string;digest:string};pins:ResumePins;chat?:{id:string;revision:string};wakeKeyDigest:string
 release:ReleaseEvidence[];parked:WaitActor;attention:string;retainUntil?:string;state:WaitState
 disposition?:WakeDisposition;expired?:WakeDisposition;resolution?:QuestionResolution;resumed?:WaitActor;revalidation?:Revalidation;reconciled?:ReleaseEvidence[];redacted?:true
 /** #226: a reauthorization wait's trusted disposition, and the refresh its claim recorded. */
 reauthorization?:ReauthDisposition;reauthorized?:Reauthorized
}
/** What a park asks the store to commit: the wait before the store stamps who parked it, when, its deadline and its
 * attention. */
export type WaitPark=Omit<RunWait,'parked'|'attention'|'state'|'deadline'|'disposition'|'expired'|'resolution'|'resumed'|'revalidation'|'reconciled'|'redacted'|'reauthorized'>
export interface WakeEvidence {version:1;resource:string;wait:string;evidence:string;kind:'event'|'time'|'timeout'|'deadline';event?:EventCondition;answer?:QuestionDraft;actor?:string;key:string}
export interface AttentionEvent {version:1;type:'needs_attention';id:string;resource:string;wait:string;reason:WaitReason;needs:'approval-decision'|'budget-grant'|'question-answer'|'reauthorization';subject?:string;condition:WaitCondition;timeoutAt?:string;deadline?:string;releaseUncertain:boolean;at:string}
export interface AttentionDelivery {version:1;attention:string;delivery:string;outcome:'sent'|'failed';at:string}
export interface ReportContent {state:'available'|'redacted'|'expired';digest:string;question?:UserQuestion;answer?:QuestionDraft}
export interface WaitReportEntry {wait:string;decision:'park'|'wake'|'timeout'|'answer'|'unanswered'|'resume'|'deadline';reason:WaitReason;at:string;owner?:string;epoch?:string;evidence?:string;wake?:'event'|'time'|'timeout'|'deadline';actor?:string;subject?:string;condition?:WaitCondition;timeoutAt?:string;deadline?:string;release?:'clean'|'uncertain';content?:ReportContent}
export interface WaitReport {version:1;resource:string;entries:WaitReportEntry[]}
export interface WaitExport {version:1;resource:string;waits:RunWait[];attention:AttentionEvent[];deliveries:AttentionDelivery[]}

/** A named wait refusal. Nothing changed. */
export class WaitRefused extends AIFailureError {
 constructor(readonly reason:WaitRefusalReason){super({...failure('stale-state','storage','not-started'),detail:`WaitRefused: ${reason}.`});this.name='WaitRefused'}
 record():{version:1;reason:WaitRefusalReason}{return {version:1,reason:this.reason}}
}
/** A store that cannot keep the wait guarantee refuses by name; it never pretends. */
export class WaitCapabilityRefused extends AIFailureError {
 constructor(readonly reason:WaitCapabilityReason){super({...failure('configuration','storage','not-started'),detail:`WaitCapabilityRefused: ${reason}.`});this.name='WaitCapabilityRefused'}
 record():{version:1;reason:WaitCapabilityReason}{return {version:1,reason:this.reason}}
}

const TIME=/^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{3}Z$/,DIGEST=/^sha256:[a-f0-9]{64}$/,KEY=/^[!-~]{32,512}$/
/** #265 M1: a wake key is 32 to 512 printable ASCII characters, at least 16 of them distinct. The host mints a fresh one
 * for each wait from a cryptographic random source (32 random bytes in base64url, for example); the rule refuses a short
 * or repetitive key, and cannot tell a guessable one. */
export const validWakeKey=(v:unknown):v is string=>typeof v==='string'&&KEY.test(v)&&new Set(v).size>=16
const object=(v:unknown):v is Record<string,unknown>=>v!==null&&typeof v==='object'&&!Array.isArray(v)&&(Object.getPrototypeOf(v)===Object.prototype||Object.getPrototypeOf(v)===null)
const invalid=():never=>{throw new WaitRefused('invalid')}
/** Exactly the required members, and of the optional ones only those named. */
function shape(v:unknown,required:readonly string[],optional:readonly string[]=[]):Record<string,unknown>{
 if(!object(v)||required.some(k=>!Object.hasOwn(v,k))||Object.keys(v).some(k=>!required.includes(k)&&!optional.includes(k)))return invalid()
 for(const k of optional)if(Object.hasOwn(v,k)&&v[k]===undefined)return invalid()
 return v
}
const id=(v:unknown):string=>storageId(v)?v:invalid()
const digest=(v:unknown):string=>typeof v==='string'&&DIGEST.test(v)?v:invalid()
const epoch=(v:unknown):string=>validLeaseEpoch(v)?v:invalid()
export const validWaitTime=(v:unknown):v is string=>{if(typeof v!=='string'||!TIME.test(v))return false;const ms=Date.parse(v);return Number.isSafeInteger(ms)&&new Date(ms).toISOString()===v}
const time=(v:unknown):string=>validWaitTime(v)?v:invalid()
const text=(v:unknown,min:number,max:number):string=>typeof v==='string'&&v.length>=min&&v.length<=max?v:invalid()
const integer=(v:unknown,min:number,max:number):number=>Number.isSafeInteger(v)&&Number(v)>=min&&Number(v)<=max?Number(v):invalid()
const version=(v:Record<string,unknown>)=>{if(v.version!==1)invalid()}
const one=<T extends string>(v:unknown,values:readonly T[]):T=>values.includes(v as T)?v as T:invalid()
const at=(text:string)=>Date.parse(text)
/** #265 M4: one whitespace set and one case fold in both languages. Blank is only JavaScript's \s and U+0085 (Go:
 * unicode.IsSpace and U+FEFF); labels are compared trimmed of them, each code point lowercased on its own (U+0130 is i
 * with U+0307) and final sigma read as sigma. */
const BLANK=/^[\s\u0085]*$/u,EDGES=/^[\s\u0085]+|[\s\u0085]+$/gu
const blank=(text:string)=>BLANK.test(text)
const fold=(text:string)=>[...text.replace(EDGES,'')].map(c=>c.toLowerCase()).join('').replace(/\u03c2/gu,'\u03c3')
function question(v:unknown):UserQuestion{
 let prompt:UserQuestion
 try{prompt=readUserQuestion(v)}catch{return invalid()}
 if(blank(prompt.title)||blank(prompt.question)||prompt.recommendation!==null&&blank(prompt.recommendation)||prompt.options.some(o=>blank(o.label))||new Set(prompt.options.map(o=>fold(o.label))).size!==prompt.options.length)return invalid()
 return prompt
}
function answer(prompt:UserQuestion|undefined,v:unknown):QuestionDraft{
 const held=shape(v,['selected','text'])
 if(!Array.isArray(held.selected)||held.selected.length>4||held.selected.some(s=>typeof s!=='string'||!/^[a-zA-Z0-9_-]{1,100}$/.test(s))||new Set(held.selected).size!==held.selected.length||typeof held.text!=='string'||held.text.length>4000)return invalid()
 if(prompt&&!validQuestionDraft(prompt,held))return invalid()
 return {selected:[...held.selected] as string[],text:held.text}
}
/** A real answer: a chosen option or non-blank free text. An empty draft is not an answer. */
const realAnswer=(draft:QuestionDraft)=>draft.selected.length>0||!blank(draft.text)

function readEventCondition(v:unknown):EventCondition{const held=shape(v,['kind','subject']);return {kind:id(held.kind),subject:id(held.subject)}}
export function readWaitCondition(v:unknown):WaitCondition{
 const held=shape(v,[],['event','wakeAt'])
 if(held.event===undefined&&held.wakeAt===undefined)return invalid()
 return {...(held.event===undefined?{}:{event:readEventCondition(held.event)}),...(held.wakeAt===undefined?{}:{wakeAt:time(held.wakeAt)})}
}
function readPending(v:unknown,reason:WaitReason,redacted:boolean):PendingDecision{
 if(!object(v)||v.kind!==reason)return invalid()
 if(reason==='approval'){const held=shape(v,['kind','callId','name','tool']);return {kind:'approval',callId:text(held.callId,1,256),name:text(held.name,1,256),tool:text(held.tool,1,128)}}
 // #265 M3: a provider-managed allowance takes no grant, so no wait carries one.
 if(reason==='budget'){const held=shape(v,['kind','unit','used','limit']);const limit=integer(held.limit,1,100);return {kind:'budget',unit:one(held.unit,['model-requests'] as const),used:integer(held.used,0,limit),limit}}
 if(reason==='reauthorization'){const held=shape(v,['kind','operation','source','tool','connection','cause']);return {kind:'reauthorization',operation:id(held.operation),source:id(held.source),tool:id(held.tool),connection:validConnectionReference(held.connection)?held.connection:invalid(),cause:one(held.cause,['expired','revoked'] as const)}}
 const held=shape(v,['kind','event','questionDigest'],['question'])
 // A question is carried until the wait is redacted, and never after; its digest is always kept.
 if(redacted===(held.question!==undefined))return invalid()
 const prompt=held.question===undefined?undefined:question(held.question)
 const pending:PendingDecision={kind:'question',event:id(held.event),questionDigest:digest(held.questionDigest),...(prompt?{question:prompt}:{})}
 if(prompt&&questionDigest(prompt)!==pending.questionDigest)return invalid()
 return pending
}
function readReleaseList(v:unknown):ReleaseEvidence[]{
 if(!Array.isArray(v)||v.length>WAIT_RELEASE_LIMIT)return invalid()
 const list=v.map(item=>{const held=shape(item,['resource','outcome']);return {resource:id(held.resource),outcome:one(held.outcome,['released','not-held','uncertain'] as const)}})
 if(new Set(list.map(r=>r.resource)).size!==list.length)return invalid()
 return list
}
function readActor(v:unknown):WaitActor{const held=shape(v,['owner','epoch','at']);return {owner:id(held.owner),epoch:epoch(held.epoch),at:time(held.at)}}
function readDisposition(v:unknown):WakeDisposition{const held=shape(v,['evidence','kind','at','digest']);return {evidence:id(held.evidence),kind:one(held.kind,['event','time','timeout','deadline'] as const),at:time(held.at),digest:digest(held.digest)}}
/** The resume's own refusal names stay theirs; a malformed member of a wait record is the wait's invalid. */
function within<T>(read:()=>T):T{try{return read()}catch(error){if(error instanceof ResumeRefused||error instanceof ReauthorizationRefused)return invalid();throw error}}
function readResolution(v:unknown,prompt:UserQuestion|undefined,redacted:boolean):QuestionResolution{
 if(!object(v))return invalid()
 if(v.kind==='unanswered'){const held=shape(v,['kind','at']);return {kind:'unanswered',at:time(held.at)}}
 if(v.kind!=='answered')return invalid()
 const held=shape(v,['kind','at','actor','answerDigest'],['answer'])
 if(redacted===(held.answer!==undefined))return invalid()
 const resolution:QuestionResolution={kind:'answered',at:time(held.at),actor:id(held.actor),answerDigest:digest(held.answerDigest)}
 if(held.answer!==undefined){const draft=answer(prompt,held.answer);if(!realAnswer(draft)||answerDigest(draft)!==resolution.answerDigest)return invalid();resolution.answer=draft}
 return resolution
}
const PARK_REQUIRED=['version','resource','wait','reason','pending','condition','continuation','checkpoint','pins','wakeKeyDigest','release'] as const
const PARK_OPTIONAL=['timeoutAt','limits','approval','chat','retainUntil','reauthorization'] as const
/** `stored`: a wait as the store keeps it, which may carry the approval binding the store attached to a reauthorization
 * wait; a park never does. */
function readParkMembers(held:Record<string,unknown>,redacted:boolean,stored:boolean):WaitPark{
 version(held)
 const reason=one(held.reason,REASONS),condition=readWaitCondition(held.condition)
 // A question waits for its answer event, and a reauthorization for the host's or the person's action; a wake time would
 // end either with neither.
 if((reason==='question'||reason==='reauthorization')&&(condition.event===undefined||condition.wakeAt!==undefined))return invalid()
 const checkpoint=shape(held.checkpoint,['revision','digest'])
 const park:WaitPark={version:1,resource:id(held.resource),wait:id(held.wait),reason,pending:readPending(held.pending,reason,redacted),condition,
  ...(held.timeoutAt===undefined?{}:{timeoutAt:time(held.timeoutAt)}),continuation:id(held.continuation),checkpoint:{revision:id(checkpoint.revision),digest:digest(checkpoint.digest)},
  pins:within(()=>readResumePins(held.pins)),...(held.chat===undefined?{}:{chat:(c=>({id:id(c.id),revision:id(c.revision)}))(shape(held.chat,['id','revision']))}),wakeKeyDigest:digest(held.wakeKeyDigest),release:readReleaseList(held.release),
  ...(held.retainUntil===undefined?{}:{retainUntil:time(held.retainUntil)})}
 if(held.limits!==undefined)park.limits=within(()=>readWaitLimits(held.limits))
 if(held.approval!==undefined)park.approval=within(()=>readApprovalBinding(held.approval))
 if(held.reauthorization!==undefined)park.reauthorization=within(()=>readReauthDisposition(held.reauthorization))
 // #225: an approval wait is bound to its pending call and pinned source, and expires; no other wait carries either.
 if(reason==='approval'){
  const pending=park.pending as {callId:string}
  if(!park.approval||park.approval.callId!==pending.callId||!park.pins.sources.some(s=>s.source===park.approval!.source)||park.limits?.approvalTtlMs===undefined)return invalid()
 }else if(reason==='reauthorization'){
  // #226: the trusted disposition is this run's, and its operation, connection and cause are the pending ones. An
  // unsettled approval's binding is the store's to attach, and its operation's source is pinned; no TTL is the park's.
  const pending=park.pending as Extract<PendingDecision,{kind:'reauthorization'}>,held=park.reauthorization
  if(!held||held.resource!==park.resource||held.operation.operation!==pending.operation||held.operation.source!==pending.source||held.operation.tool!==pending.tool||held.connection!==pending.connection||held.cause!==pending.cause)return invalid()
  if(park.limits?.approvalTtlMs!==undefined||park.approval!==undefined&&(!stored||!park.pins.sources.some(s=>s.source===park.approval!.source)))return invalid()
 }else if(park.approval!==undefined||park.limits?.approvalTtlMs!==undefined)return invalid()
 if(reason!=='reauthorization'&&park.reauthorization!==undefined)return invalid()
 return park
}
/** Reads the store's input: a park before the store stamps it. */
export function readWaitPark(v:unknown):WaitPark{return readParkMembers(shape(v,PARK_REQUIRED,PARK_OPTIONAL),false,false)}
/** Reads a version 1 wait record and holds every rule the schema cannot state. */
export function readRunWait(v:unknown):RunWait{
 const held=shape(v,[...PARK_REQUIRED,'parked','attention','state'],[...PARK_OPTIONAL,'deadline','disposition','expired','resolution','resumed','revalidation','reconciled','redacted','reauthorized'])
 if(held.redacted!==undefined&&held.redacted!==true)return invalid()
 const redacted=held.redacted===true
 if(redacted&&held.reason!=='question')return invalid()
 const park=readParkMembers(held,redacted,true),state=one(held.state,['parked','woken','timed-out','resumed','expired'] as const)
 const wait:RunWait={...park,parked:readActor(held.parked),attention:digest(held.attention),state}
 if(wait.attention!==attentionId(wait.resource,wait.wait))return invalid()
 // #225: the stamped deadline follows the park's limits and time; waitMs and approvalTtlMs always stamp one.
 const bound=wait.reason==='reauthorization'&&wait.approval!==undefined
 if(held.deadline!==undefined)wait.deadline=within(()=>readWaitDeadline(held.deadline,wait.limits,wait.parked.at,bound))
 else if(wait.limits?.waitMs!==undefined||wait.limits?.approvalTtlMs!==undefined||wait.limits?.run!==undefined||bound)return invalid()
 if(state==='parked'){if(held.disposition!==undefined||held.expired!==undefined||held.resolution!==undefined||held.resumed!==undefined)return invalid()}
 else if(state==='expired'){
  // A deadline ends the wait for good: it keeps the wake it was woken with, if any, and is never resumed.
  wait.expired=readDisposition(held.expired)
  if(wait.expired.kind!=='deadline'||!wait.deadline||at(wait.expired.at)<at(wait.deadline.at))return invalid()
  if(held.disposition!==undefined){wait.disposition=readDisposition(held.disposition);if(wait.disposition.kind==='deadline'||at(wait.disposition.at)<at(wait.parked.at)||at(wait.expired.at)<at(wait.disposition.at))return invalid()}
  // #266 N1: an approved call that did not settle by the approval's expiry ends its resumed wait there; its outcome is
  // unknown. Only an approval wait, woken and claimed, is expired after its claim.
  if(held.resumed!==undefined){
   wait.resumed=readActor(held.resumed)
   if(wait.reason!=='approval'||!wait.disposition||wait.disposition.kind==='timeout'||wait.deadline.approval===undefined||at(wait.expired.at)<at(wait.deadline.approval)||at(wait.resumed.at)<at(wait.disposition.at)||at(wait.expired.at)<at(wait.resumed.at))return invalid()
  }
 }else{
  if(held.expired!==undefined)return invalid()
  wait.disposition=readDisposition(held.disposition)
  // A timeout ends the wait timed-out; a resumed wait keeps the disposition it was resumed after (a timed-out
  // approval or budget wait is never resumed).
  if(wait.disposition.kind==='deadline'||state==='woken'&&wait.disposition.kind==='timeout'||state==='timed-out'&&wait.disposition.kind!=='timeout'||state==='resumed'&&wait.disposition.kind==='timeout'&&wait.reason!=='question')return invalid()
  if(state==='resumed'){wait.resumed=readActor(held.resumed);if(at(wait.resumed.at)<at(wait.disposition.at))return invalid()}else if(held.resumed!==undefined)return invalid()
  if(at(wait.disposition.at)<at(wait.parked.at))return invalid()
 }
 const prompt=wait.pending.kind==='question'?wait.pending.question:undefined
 if(wait.reason==='question'&&state!=='parked'){
  const resolution=readResolution(held.resolution,prompt,redacted)
  // A timeout ends the question unanswered, and so does a deadline that came before any wake; only an event wake carries
  // an answer.
  const ended=wait.disposition??wait.expired!
  if((ended.kind==='timeout'||ended.kind==='deadline')!==(resolution.kind==='unanswered')||resolution.at!==ended.at)return invalid()
  wait.resolution=resolution
 }else if(held.resolution!==undefined)return invalid()
 // #227: a claimed continuation records the revalidation it was claimed after, for this wait and its pins.
 if(held.revalidation!==undefined){
  if(!wait.resumed)return invalid()
  wait.revalidation=within(()=>readRevalidation(held.revalidation))
  if(wait.revalidation.resource!==wait.resource||wait.revalidation.wait!==wait.wait||wait.revalidation.pins!==pinsDigest(wait.pins))return invalid()
 }else if(wait.resumed)return invalid()
 if(held.reconciled!==undefined){if(!wait.resumed)return invalid();wait.reconciled=readReleaseList(held.reconciled)}
 // #226: a resumed reauthorization wait records the refresh it was claimed after: the parked principal, permissions that
 // cover what the operation requires, taken after the park and no later than the claim.
 if(held.reauthorized!==undefined){
  if(wait.reason!=='reauthorization'||!wait.resumed)return invalid()
  const reauthorized=within(()=>readReauthorized(held.reauthorized)),disposition=wait.reauthorization!
  if(reauthorized.principal!==disposition.principal||!permitted(disposition.requires,reauthorized.permissions)||at(reauthorized.at)<at(wait.parked.at)||at(reauthorized.at)>at(wait.resumed.at))return invalid()
  wait.reauthorized=reauthorized
 }else if(wait.reason==='reauthorization'&&wait.resumed)return invalid()
 if(redacted)wait.redacted=true
 return wait
}
/** Reads bounded wake evidence. A model-supplied event name, job id or time is never evidence: only the host presents
 * the wait's key. */
export function readWakeEvidence(v:unknown):WakeEvidence{
 const held=shape(v,['version','resource','wait','evidence','kind','key'],['event','answer','actor'])
 version(held)
 const kind=one(held.kind,['event','time','timeout','deadline'] as const)
 if((kind==='event')!==(held.event!==undefined)||(held.answer===undefined)!==(held.actor===undefined)||held.answer!==undefined&&kind!=='event')return invalid()
 if(!validWakeKey(held.key))return invalid()
 return {version:1,resource:id(held.resource),wait:id(held.wait),evidence:id(held.evidence),kind,...(held.event===undefined?{}:{event:readEventCondition(held.event)}),...(held.answer===undefined?{}:{answer:answer(undefined,held.answer),actor:id(held.actor)}),key:held.key}
}
export function readAttentionEvent(v:unknown):AttentionEvent{
 const held=shape(v,['version','type','id','resource','wait','reason','needs','condition','releaseUncertain','at'],['subject','timeoutAt','deadline'])
 version(held)
 if(held.type!=='needs_attention'||typeof held.releaseUncertain!=='boolean')return invalid()
 const event:AttentionEvent={version:1,type:'needs_attention',id:digest(held.id),resource:id(held.resource),wait:id(held.wait),reason:one(held.reason,REASONS),needs:one(held.needs,['approval-decision','budget-grant','question-answer','reauthorization'] as const),
  ...(held.subject===undefined?{}:{subject:id(held.subject)}),condition:readWaitCondition(held.condition),...(held.timeoutAt===undefined?{}:{timeoutAt:time(held.timeoutAt)}),...(held.deadline===undefined?{}:{deadline:time(held.deadline)}),releaseUncertain:held.releaseUncertain,at:time(held.at)}
 if(event.deadline!==undefined&&at(event.deadline)<=at(event.at))return invalid()
 if(event.id!==attentionId(event.resource,event.wait)||event.needs!==NEEDS[event.reason]||(event.reason==='budget')!==(event.subject===undefined))return invalid()
 return event
}
export function readAttentionDelivery(v:unknown):AttentionDelivery{
 const held=shape(v,['version','attention','delivery','outcome','at']);version(held)
 return {version:1,attention:digest(held.attention),delivery:id(held.delivery),outcome:one(held.outcome,['sent','failed'] as const),at:time(held.at)}
}
function readContent(v:unknown):ReportContent{
 const held=shape(v,['state','digest'],['question','answer'])
 const state=one(held.state,['available','redacted','expired'] as const),content:ReportContent={state,digest:digest(held.digest)}
 if(state!=='available'){if(held.question!==undefined||held.answer!==undefined)return invalid();return content}
 if((held.question===undefined)===(held.answer===undefined))return invalid()
 if(held.question!==undefined){content.question=question(held.question);if(questionDigest(content.question)!==content.digest)return invalid()}
 else{content.answer=answer(undefined,held.answer);if(answerDigest(content.answer)!==content.digest)return invalid()}
 return content
}
const ENTRY_MEMBERS:Record<WaitReportEntry['decision'],readonly string[]>={park:['owner','epoch','subject','condition','timeoutAt','deadline','release','content'],wake:['evidence','wake'],timeout:['evidence','wake'],answer:['actor','content'],unanswered:['content'],resume:['owner','epoch'],deadline:['evidence','wake']}
function readEntry(v:unknown):WaitReportEntry{
 if(!object(v))return invalid()
 const decision=one(v.decision,['park','wake','timeout','answer','unanswered','resume','deadline'] as const)
 const held=shape(v,['wait','decision','reason','at'],ENTRY_MEMBERS[decision])
 const entry:WaitReportEntry={wait:id(held.wait),decision,reason:one(held.reason,REASONS),at:time(held.at)}
 if(decision==='park'||decision==='resume'){entry.owner=id(held.owner);entry.epoch=epoch(held.epoch)}
 if(decision==='park'){
  if(held.subject!==undefined)entry.subject=id(held.subject)
  entry.condition=readWaitCondition(held.condition)
  if(held.timeoutAt!==undefined)entry.timeoutAt=time(held.timeoutAt)
  if(held.deadline!==undefined)entry.deadline=time(held.deadline)
  entry.release=one(held.release,['clean','uncertain'] as const)
  if((entry.reason==='question')!==(held.content!==undefined))return invalid()
 }
 if(decision==='wake'||decision==='timeout'||decision==='deadline'){
  entry.evidence=id(held.evidence);entry.wake=one(held.wake,['event','time','timeout','deadline'] as const)
  if((decision==='timeout')!==(entry.wake==='timeout')||(decision==='deadline')!==(entry.wake==='deadline'))return invalid()
 }
 if(decision==='answer'||decision==='unanswered'){if(entry.reason!=='question')return invalid()}
 if(decision==='answer')entry.actor=id(held.actor)
 if(held.content!==undefined){
  entry.content=readContent(held.content)
  if(decision==='answer'&&entry.content.question!==undefined||decision!=='answer'&&entry.content.answer!==undefined)return invalid()
 }else if(decision==='answer'||decision==='unanswered')return invalid()
 return entry
}
export function readWaitReport(v:unknown):WaitReport{
 const held=shape(v,['version','resource','entries']);version(held)
 if(!Array.isArray(held.entries)||held.entries.length>4096)return invalid()
 return {version:1,resource:id(held.resource),entries:held.entries.map(readEntry)}
}
export function readWaitExport(v:unknown):WaitExport{
 const held=shape(v,['version','resource','waits','attention','deliveries']);version(held)
 const resource=id(held.resource)
 if(!Array.isArray(held.waits)||held.waits.length>WAIT_EXPORT_LIMIT||!Array.isArray(held.attention)||held.attention.length>WAIT_EXPORT_LIMIT||!Array.isArray(held.deliveries)||held.deliveries.length>4096)return invalid()
 const waits=held.waits.map(readRunWait),attention=held.attention.map(readAttentionEvent),deliveries=held.deliveries.map(readAttentionDelivery)
 // Every record is this run's, each wait has exactly its one attention event, and a delivery names one of them.
 if(waits.some(w=>w.resource!==resource)||new Set(waits.map(w=>w.wait)).size!==waits.length||attention.length!==waits.length||attention.some((e,i)=>e.resource!==resource||e.id!==waits[i]!.attention)||deliveries.some(d=>!attention.some(e=>e.id===d.attention)))return invalid()
 return {version:1,resource,waits,attention,deliveries}
}
export function readWaitRefusal(v:unknown):{version:1;reason:WaitRefusalReason}{const held=shape(v,['version','reason']);version(held);return {version:1,reason:one(held.reason,WAIT_REFUSAL_REASONS)}}
export function readWaitCapabilityRefusal(v:unknown):{version:1;reason:WaitCapabilityReason}{const held=shape(v,['version','reason']);version(held);return {version:1,reason:one(held.reason,WAIT_CAPABILITY_REASONS)}}

const NEEDS={approval:'approval-decision',budget:'budget-grant',question:'question-answer',reauthorization:'reauthorization'} as const
/** The attention event's identity: the SHA-256 of the canonical JSON of {resource, wait}. A notifier deduplicates by it. */
export const attentionId=(resource:string,wait:string)=>`sha256:${sha256(canonicalJSON({resource,wait}))}`
export const questionDigest=(prompt:UserQuestion)=>`sha256:${sha256(canonicalJSON(prompt))}`
export const answerDigest=(draft:QuestionDraft)=>`sha256:${sha256(canonicalJSON(draft))}`
/** The evidence's digest, without its key: a duplicate is the same evidence id with this digest. */
export const wakeEvidenceDigest=(evidence:WakeEvidence)=>{const {key:_,...rest}=readWakeEvidence(evidence);return `sha256:${sha256(canonicalJSON(rest))}`}
export const wakeKeyDigest=(key:string)=>{if(!validWakeKey(key))return invalid();return `sha256:${sha256(key)}`}
export const checkpointBytesDigest=(bytes:Uint8Array)=>`sha256:${sha256(new TextDecoder('utf-8',{fatal:true}).decode(bytes))}`
/** The one needs_attention event of a committed park. */
export function attentionFor(wait:RunWait):AttentionEvent{
 const subject=wait.pending.kind==='approval'?wait.pending.callId:wait.pending.kind==='question'?wait.pending.event:wait.pending.kind==='reauthorization'?wait.pending.connection:undefined
 return {version:1,type:'needs_attention',id:wait.attention,resource:wait.resource,wait:wait.wait,reason:wait.reason,needs:NEEDS[wait.reason],...(subject===undefined?{}:{subject}),condition:structuredClone(wait.condition),...(wait.timeoutAt===undefined?{}:{timeoutAt:wait.timeoutAt}),...(wait.deadline===undefined?{}:{deadline:wait.deadline.at}),releaseUncertain:wait.release.some(r=>r.outcome==='uncertain'),at:wait.parked.at}
}

/** A resume's claim: the wake whose acknowledgement the host holds, the lease the continuation takes (against the parked
 * revision), the host's reconciliation of each uncertain release, and the revalidation it was claimed after. */
export interface WaitClaim {resource:string;wait:string;evidence:string;lease:LeaseAcquire;reconciled:ReleaseEvidence[];revalidation:Revalidation
 /** #226: a reauthorization wait's claim carries the refresh it continues after; no other claim does. */
 reauthorized?:Reauthorized}
/** The wait port a store kind implements beside its lease port. Every transition is atomic with the store's commits and
 * checked against its own clock, and everything it decides by survives the store's restarts: the waits, their attention
 * events and deliveries, each run's earliest deadline, and an approved call not yet settled with its expiry and binding.
 * Until a wait is resumed it holds its run: the store's acquireLease refuses the run (wait-pending, or closed once a wait
 * ended terminally), so does its unfenced commit (#266 M4), and only resumeWait leases it. From a run's deadline every
 * lease of the run expires no later than that deadline, and acquireLease and an unfenced commit refuse deadline-passed.
 * The package checks none of this in a host store; runStoreWaitConformance (storage/waitConformance) does. */
export interface WaitStore {
 readonly waitVersion:1
 /** durable: waits and attention events survive the store's process. process: they end with it (memory test kinds). */
 readonly waitLifetime:WaitLifetime
 /** One transaction: check the fence and the run head (checkpoint-changed), refuse while a wait not yet resumed holds the
  * run (wait-pending; closed after a terminal one), stamp the deadline from the limits, the run's earliest deadline and
  * the store clock (deadline-passed when it is not after the park), refuse a wakeKeyDigest any other wait of the store
  * holds (key-reused), commit the wait and its one needs_attention event, and release the lease. A repeat of the same
  * park under the same fence returns it; anything else under its wait id is conflict. A refused park changes nothing and
  * leaves the lease held. #226: a reauthorization park also refuses a disposition minted under another fence (stale) and a
  * head that does not hold its operation as the never-admitted dispatch (unbound, as holdsNeverAdmitted decides), and, when
  * the run's approved call is unsettled, the store attaches that approval's binding to the wait and stamps its expiry as
  * the deadline's approval time, from its own record and never from the park. It records the attention event needing
  * reauthorization and naming the connection, and keeps the disposition and the attached approval across its restarts. The
  * attached approval is no part of a repeated park's comparison. */
 parkWait(park:WaitPark,fence:LeaseFence,signal?:AbortSignal):Promise<{wait:RunWait;attention:AttentionEvent}>
 /** The first valid wake wins, decided against the named wait only; a duplicate returns the retained disposition;
  * anything else changes nothing, and a resumed wait is never woken again. At or after the wait's deadline or the run's
  * only a deadline wake is accepted, and it ends the wait terminally (expired). A resumed approval wait whose approved
  * call has not settled is ended by a deadline wake at the approval's expiry (#266 N1): expired, its outcome unknown. #226:
  * a reauthorization wait's timeout or deadline wake is terminal, as an approval wait's is: its run is closed for good. */
 wakeWait(evidence:WakeEvidence,signal?:AbortSignal):Promise<WakeDisposition>
 /** One transaction: check the acknowledged wake, the deadline, the head and the release reconciliation, then lease the
  * run (the only lease a held run is granted) and claim the continuation once; a ceiling of the run not after the store's
  * time refuses it (deadline-passed). The lease ends no later than the run's deadline and any unsettled approval's
  * expiry. From an approval wait's claim until its approved call settles, no lease of the run (the claim's, a takeover's
  * or a re-acquired one) is granted, renewed or held past the approval's expiry, and every checkpoint the run commits,
  * fenced or not, must carry exactly the reviewed call (approval-changed otherwise); the commit that records the call
  * executed, refused or answered settles it in the same transaction. resumeParked checks what this returns. #226: a
  * reauthorization wait's claim carries the refresh and no other claim does. It refuses a claim with none
  * (decision-required), for another principal than the disposition's (identity-changed), taken before the park or after
  * the store's time (stale), or whose permissions do not cover what the operation requires (permission-insufficient),
  * changing nothing, and it records the refresh (reauthorized) and keeps it across its restarts. The lease ends no later
  * than the attached approval's expiry too. */
 resumeWait(claim:WaitClaim,signal?:AbortSignal):Promise<{wait:RunWait;lease:RunLease}>
 /** Sets the run's deadline; the earliest one the store holds wins, a held lease is shortened to it, and every wait of
  * the run not yet resumed or ended is stamped again with it (#266 M1). #226: stamping a reauthorization wait again keeps
  * its attached approval's time. */
 limitRun(limit:RunLimit,signal?:AbortSignal):Promise<RunLimit>
 readWait(resource:string,wait:string,signal?:AbortSignal):Promise<RunWait|undefined>
 /** The run's waits in park order, with their attention events and deliveries: the report's source. */
 exportWaits(resource:string,signal?:AbortSignal):Promise<WaitExport>
 /** Records a delivery attempt apart from the event. A repeat delivery id returns the first record. */
 recordDelivery(delivery:Omit<AttentionDelivery,'at'|'version'>,signal?:AbortSignal):Promise<AttentionDelivery>
 /** Removes the question and answer content; every identity and digest is kept. */
 redactWait(resource:string,wait:string,signal?:AbortSignal):Promise<RunWait>
}
export interface WaitRunStore extends LeaseRunStore,WaitStore {}

const copy=<T,>(v:T):T=>structuredClone(v)
/** A terminal wait ends its run: a timed-out approval or budget wait, or one past its deadline. */
const terminal=(row:RunWait)=>row.state==='expired'||row.state==='timed-out'&&row.reason!=='question'
function readClaim(v:unknown):WaitClaim{
 const held=shape(v,['resource','wait','evidence','lease','reconciled','revalidation'],['reauthorized'])
 if(!object(held.lease))return invalid()
 return {resource:id(held.resource),wait:id(held.wait),evidence:id(held.evidence),lease:held.lease as unknown as LeaseAcquire,reconciled:readReleaseList(held.reconciled),revalidation:within(()=>readRevalidation(held.revalidation)),...(held.reauthorized===undefined?{}:{reauthorized:within(()=>readReauthorized(held.reauthorized))})}
}
/** #226: the run's head holds the operation as the never-admitted dispatch the disposition names: its last dispatch, a
 * host or Runtime effect whose outcome is unknown and whose recorded failure is the typed refusal (authentication, tool,
 * not-started), with the disposition's source, tool and request digest. Nothing was dispatched after it. */
export function holdsNeverAdmitted(checkpoint:Uint8Array|null,disposition:ReauthDisposition):boolean{
 if(checkpoint===null)return false
 let rows:unknown
 try{rows=JSON.parse(new TextDecoder('utf-8',{fatal:true}).decode(checkpoint))?.durable?.dispatches}catch{return false}
 if(!Array.isArray(rows)||rows.length===0)return false
 const row=rows[rows.length-1] as Record<string,unknown>,failed=row?.failure as Record<string,unknown>|undefined,op=disposition.operation
 return object(row)&&row.id===op.operation&&(row.kind==='host'||row.kind==='runtime')&&row.read===false&&row.state==='unknown'&&row.source===op.source&&row.tool===op.tool&&row.requestDigest===op.requestDigest&&object(failed)&&failed.code==='authentication'&&failed.source==='tool'&&failed.outcome==='not-started'
}
/** The wait transitions, held in memory. MemoryRunStore calls them inside its own serialized transactions; a host store
 * kind implements the same transitions against its own durable rows. */
export class WaitTable {
 private rows=new Map<string,RunWait[]>()
 private events=new Map<string,AttentionEvent[]>()
 private deliveries=new Map<string,AttentionDelivery[]>()
 /** #225: each run's earliest deadline, and the expiry and binding of an approved call not yet settled. */
 private ceilings=new Map<string,number>()
 private approvals=new Map<string,{at:number;binding:ApprovalBinding;wait:string}>()
 constructor(private readonly clock:()=>Date=()=>new Date()){}
 private now():string{const t=this.clock();const ms=t instanceof Date?t.getTime():NaN;if(!Number.isSafeInteger(ms))return invalid();return new Date(ms).toISOString()}
 private find(resource:string,wait:string):RunWait|undefined{return this.rows.get(resource)?.find(row=>row.wait===wait)}
 read(resource:string,wait:string):RunWait|undefined{const row=this.find(id(resource),id(wait));return row?copy(row):undefined}
 /** The latest any lease of the run may expire: its deadline and, until the approved call settles, the approval's expiry
  * (#225 H1: every lease, not only the claim's, so neither a takeover nor a re-acquired lease outlives the approval). */
 ceiling(resource:string):number|undefined{
  const limits=[this.ceilings.get(resource),this.approvals.get(resource)?.at].filter((t):t is number=>t!==undefined)
  return limits.length?Math.min(...limits):undefined
 }
 /** #225 H1 and M3: while the run's approved call is unsettled, every checkpoint it commits must carry exactly the call
  * the person reviewed (its call id, the digest of the call and its arguments, and its source), or the commit is refused
  * approval-changed; nothing else can be claimed or dispatched as that call. Returns true when the checkpoint records the
  * call executed, refused or answered: that commit settles it, and the approval's expiry stops bounding the run. */
 commitApproval(resource:string,checkpoint:Uint8Array|null):boolean{
  const held=this.approvals.get(resource)
  if(!held||checkpoint===null)return false
  let approval:{state?:unknown}|undefined,binding:ApprovalBinding|undefined
  try{approval=JSON.parse(new TextDecoder('utf-8',{fatal:true}).decode(checkpoint))?.approval;binding=approvalBinding(approval as never)}catch{binding=undefined}
  if(!binding||canonicalJSON(binding)!==canonicalJSON(held.binding))throw new ResumeRefused('approval-changed')
  return ['executed','refused','answered'].includes(approval!.state as string)
 }
 settleApproval(resource:string):void{this.approvals.delete(resource)}
 /** #266 M1: whether the run's deadline the store holds is at or before `now` (equal time is past). */
 private passed(resource:string,now:string):boolean{const ceiling=this.ceilings.get(resource);return ceiling!==undefined&&at(now)>=ceiling}
 /** #265 M2: a committed park closes the run. Refuses acquiring its lease while a wait not yet resumed holds it, for good
  * once a wait ended terminally, and from the run's deadline. */
 admitAcquire(resource:string):void{
  const rows=this.rows.get(resource)??[]
  if(rows.some(terminal))throw new WaitRefused('closed')
  // #225: from the run's deadline, or an unsettled approval's expiry, nothing leases the run; equal time is past.
  const ceiling=this.ceiling(resource)
  if(ceiling!==undefined&&at(this.now())>=ceiling)throw new WaitRefused('deadline-passed')
  if(rows.some(row=>row.state!=='resumed'))throw new WaitRefused('wait-pending')
 }
 limit(input:RunLimit):RunLimit{
  const limit=within(()=>readRunLimit(input)),held=this.ceilings.get(limit.resource),next=Math.min(held??Infinity,at(limit.run))
  this.ceilings.set(limit.resource,next)
  // #266 M1: a run deadline set after a park reaches the run's waits. Each wait not yet resumed or ended is stamped again
  // with it, so no wake, answer, decision or claim is accepted from it and its deadline wake is due at it; a deadline
  // before the park is stamped as the earliest a committed park can carry, a millisecond after it.
  for(const row of this.rows.get(limit.resource)??[])if(row.state!=='resumed'&&!terminal(row)){
   const parked=at(row.parked.at)
   row.deadline=stampDeadline(row.limits,parked,Math.max(next,parked+1),row.reason==='reauthorization'&&row.deadline?.approval!==undefined?at(row.deadline.approval):undefined)
  }
  return {version:1,resource:limit.resource,run:new Date(next).toISOString()}
 }
 park(input:WaitPark,fence:LeaseFence,head:{revision:StorageRevision;checkpoint:Uint8Array|null}|undefined,leases:LeaseTable):{wait:RunWait;attention:AttentionEvent}{
  const park=readWaitPark(input),held=readLeaseFence(fence)
  if(held.resource!==park.resource)throw new LeaseRefused('resource-mismatch')
  const existing=this.find(park.resource,park.wait)
  if(existing){
   // A repeated park acknowledges the committed one: read, never parked twice.
   const {parked,attention:_a,state:_s,deadline:_l,disposition:_d,expired:_e,resolution:_r,resumed:_m,revalidation:_v,reconciled:_c,redacted:_x,reauthorized:_z,...prior}=existing
   // #226: the approval binding the store attached to a reauthorization wait is not the park's.
   if(prior.reason==='reauthorization')delete prior.approval
   if(canonicalJSON(prior)!==canonicalJSON(park)||parked.owner!==held.owner||parked.epoch!==held.epoch)throw new WaitRefused('conflict')
   return {wait:copy(existing),attention:copy(this.events.get(park.resource)!.find(e=>e.id===existing.attention)!)}
  }
  leases.check(held)
  // #265 M2: every wait not yet resumed holds the run, so one pause is never parked, and claimed, twice.
  const rows=this.rows.get(park.resource)??[]
  if(rows.some(terminal))throw new WaitRefused('closed')
  if(rows.some(row=>row.state!=='resumed'))throw new WaitRefused('wait-pending')
  // #265 M1: a wake key belongs to one wait. A park whose key digest any other wait of the store holds is refused.
  for(const list of this.rows.values())if(list.some(row=>row.wakeKeyDigest===park.wakeKeyDigest))throw new WaitRefused('key-reused')
  if(!head||head.checkpoint===null||head.revision!==park.checkpoint.revision||checkpointBytesDigest(head.checkpoint)!==park.checkpoint.digest)throw new WaitRefused('checkpoint-changed')
  // #226: a reauthorization park presents the disposition minted under its own lease (the lease this park releases, so
  // the disposition parks once), for the never-admitted dispatch its head holds. The binding of an approved call not yet
  // settled is the store's to attach.
  let unsettled:{at:number;binding:ApprovalBinding}|undefined
  if(park.reauthorization){
   const disposition=park.reauthorization
   if(disposition.fence.owner!==held.owner||disposition.fence.epoch!==held.epoch||disposition.fence.resource!==held.resource)throw new ReauthorizationRefused('stale')
   if(!holdsNeverAdmitted(head.checkpoint,disposition))throw new ReauthorizationRefused('unbound')
   unsettled=this.approvals.get(park.resource)
  }
  const now=this.now()
  // #225: the earliest applicable deadline wins, and this park's run limit never extends the one the store holds.
  const deadline=stampDeadline(park.limits,at(now),this.ceilings.get(park.resource),unsettled?.at)
  if(deadline&&at(deadline.at)<=at(now))throw new WaitRefused('deadline-passed')
  const wait:RunWait={...copy(park),...(unsettled?{approval:copy(unsettled.binding)}:{}),...(deadline?{deadline}:{}),parked:{owner:held.owner,epoch:held.epoch,at:now},attention:attentionId(park.resource,park.wait),state:'parked'}
  const attention=attentionFor(wait)
  // #224: the lease is released in the same transaction that commits the wait and its attention event.
  leases.release(held)
  if(deadline?.run!==undefined)this.ceilings.set(wait.resource,at(deadline.run))
  this.rows.set(wait.resource,[...rows,wait])
  this.events.set(wait.resource,[...(this.events.get(wait.resource)??[]),attention])
  return {wait:copy(wait),attention:copy(attention)}
 }
 wake(input:WakeEvidence):WakeDisposition{
  const evidence=readWakeEvidence(input),row=this.find(evidence.resource,evidence.wait)
  if(!row)throw new WaitRefused('no-wait')
  if(wakeKeyDigest(evidence.key)!==row.wakeKeyDigest)throw new WaitRefused('unauthenticated')
  const digest=wakeEvidenceDigest(evidence)
  if(evidence.kind==='deadline'){
   if(row.expired){
    if(row.expired.evidence!==evidence.evidence)throw new WaitRefused('stale')
    if(row.expired.digest!==digest)throw new WaitRefused('conflict')
    return copy(row.expired)
   }
   // #266 N1: an approved call that has not settled by the approval's expiry ends its resumed wait there. Its outcome is
   // unknown (it may have been performed); nothing dispatches it, and the run is closed for good.
   const unsettled=row.state==='resumed'?this.approvals.get(row.resource):undefined
   if(unsettled?.wait===row.wait){
    const now=this.now()
    if(at(now)<unsettled.at)throw new WaitRefused('not-due')
    const expired:WakeDisposition={evidence:evidence.evidence,kind:'deadline',at:now,digest}
    row.expired=expired;row.state='expired';this.approvals.delete(row.resource)
    return copy(expired)
   }
   // A resumed wait whose call settled, or one that already ended, has nothing left for its deadline to end.
   if(row.state==='resumed'||terminal(row))throw new WaitRefused('stale')
   if(!row.deadline)throw new WaitRefused('mismatch')
   const now=this.now()
   // Equal time is due.
   if(at(now)<at(row.deadline.at))throw new WaitRefused('not-due')
   const expired:WakeDisposition={evidence:evidence.evidence,kind:'deadline',at:now,digest}
   row.expired=expired;row.state='expired'
   // A question its deadline ends before any answer is unanswered; the recommended option is never chosen.
   if(row.reason==='question'&&!row.resolution)row.resolution={kind:'unanswered',at:now}
   return copy(expired)
  }
  if(row.disposition){
   if(row.disposition.evidence!==evidence.evidence)throw new WaitRefused('stale')
   if(row.disposition.digest!==digest)throw new WaitRefused('conflict')
   return copy(row.disposition)
  }
  if(row.expired)throw new WaitRefused('stale')
  const now=this.now(),prompt=row.pending.kind==='question'?row.pending.question:undefined
  // #225: late evidence changes nothing. From the deadline only the deadline wake ends the wait; from the timeout only
  // the timeout does: an answer or a decision delivered after either is refused, equal time included.
  if(row.deadline&&at(now)>=at(row.deadline.at)||this.passed(row.resource,now))throw new WaitRefused('deadline-passed')
  if(evidence.kind!=='timeout'&&row.timeoutAt!==undefined&&at(now)>=at(row.timeoutAt))throw new WaitRefused('timed-out')
  if(evidence.kind==='event'){
   const expected=row.condition.event
   if(!expected||expected.kind!==evidence.event!.kind||expected.subject!==evidence.event!.subject)throw new WaitRefused('mismatch')
   // An answer belongs to a question wait only, and must be a real answer to the retained question.
   if(row.reason==='question'){if(!prompt||!evidence.answer||!validQuestionDraft(prompt,evidence.answer)||!realAnswer(evidence.answer))throw new WaitRefused('mismatch')}
   else if(evidence.answer)throw new WaitRefused('mismatch')
  }else{
   const due=evidence.kind==='time'?row.condition.wakeAt:row.timeoutAt
   if(due===undefined)throw new WaitRefused('mismatch')
   if(at(now)<at(due))throw new WaitRefused('not-due')
  }
  const disposition:WakeDisposition={evidence:evidence.evidence,kind:evidence.kind,at:now,digest}
  row.disposition=disposition;row.state=evidence.kind==='timeout'?'timed-out':'woken'
  if(row.reason==='question')row.resolution=evidence.kind==='timeout'?{kind:'unanswered',at:now}:{kind:'answered',at:now,actor:evidence.actor!,answerDigest:answerDigest(evidence.answer!),answer:copy(evidence.answer!)}
  return copy(disposition)
 }
 resume(input:WaitClaim,head:{revision:StorageRevision;checkpoint:Uint8Array|null}|undefined,leases:LeaseTable):{wait:RunWait;lease:RunLease}{
  const claim=readClaim(input),row=this.find(claim.resource,claim.wait),settled=claim.reconciled,now=this.now()
  if(!row)throw new WaitRefused('no-wait')
  // An acknowledgement the store does not hold is reconciled by reading, never by dispatching the continuation.
  if(!row.disposition||row.disposition.evidence!==claim.evidence)throw new WaitRefused('unacknowledged')
  if(row.state==='resumed')throw new WaitRefused('already-resumed')
  if(claim.lease.resource!==row.resource)throw new LeaseRefused('resource-mismatch')
  if(row.state==='timed-out'&&row.reason!=='question')throw new WaitRefused('timed-out')
  // #225: nothing continues past the wait's deadline, an approval's expiry or the run's deadline; equal time is past.
  if(row.state==='expired'||row.deadline&&at(now)>=at(row.deadline.at))throw new WaitRefused('deadline-passed')
  if(!head||head.checkpoint===null||head.revision!==row.checkpoint.revision||checkpointBytesDigest(head.checkpoint)!==row.checkpoint.digest)throw new WaitRefused('checkpoint-changed')
  const uncertain=row.release.filter(r=>r.outcome==='uncertain').map(r=>r.resource)
  if(uncertain.some(r=>!settled.some(s=>s.resource===r&&s.outcome!=='uncertain'))||settled.some(s=>!uncertain.includes(s.resource)))throw new WaitRefused('release-uncertain')
  // #227: the claim records the revalidation of this wait's own pins.
  if(claim.revalidation.resource!==row.resource||claim.revalidation.wait!==row.wait||claim.revalidation.pins!==pinsDigest(row.pins))throw new WaitRefused('invalid')
  // #226: a reauthorization wait continues only after a refresh for the same principal, taken after the park and not after
  // the store's time, whose permissions cover what the operation requires. A wake is never one.
  if((row.reason==='reauthorization')!==(claim.reauthorized!==undefined)){if(row.reason==='reauthorization')throw new WaitRefused('decision-required');throw new WaitRefused('invalid')}
  if(claim.reauthorized){
   const disposition=row.reauthorization!,refresh=claim.reauthorized
   if(refresh.principal!==disposition.principal)throw new ResumeRefused('identity-changed')
   if(at(refresh.at)<at(row.parked.at)||at(refresh.at)>at(now))throw new ReauthorizationRefused('stale')
   if(!permitted(disposition.requires,refresh.permissions))throw new ReauthorizationRefused('permission-insufficient')
  }
  // #265 M2: the claim is the one lease a held run is granted, taken in this transaction. Every ceiling of the run bounds
  // it (#266 N2: an earlier approval not yet settled too), and so does this wait's own approval, which then bounds every
  // later lease of the run until the approved call settles (#266 H1). A ceiling not after the store's time refuses it.
  const approval=row.reason==='approval'&&row.deadline?.approval!==undefined?at(row.deadline.approval):undefined
  const ceiling=[this.ceiling(row.resource),approval].filter((t):t is number=>t!==undefined)
  const limit=ceiling.length?Math.min(...ceiling):undefined
  if(limit!==undefined&&limit<=at(now))throw new WaitRefused('deadline-passed')
  const lease=leases.acquire(claim.lease,head.revision,limit)
  if(approval!==undefined)this.approvals.set(row.resource,{at:approval,binding:copy(row.approval!),wait:row.wait})
  row.state='resumed';row.resumed={owner:lease.owner,epoch:lease.epoch,at:now};row.revalidation=copy(claim.revalidation)
  if(settled.length)row.reconciled=settled
  if(claim.reauthorized)row.reauthorized=copy(claim.reauthorized)
  return {wait:copy(row),lease}
 }
 export(resource:string):WaitExport{
  id(resource)
  return {version:1,resource,waits:copy(this.rows.get(resource)??[]),attention:copy(this.events.get(resource)??[]),deliveries:copy((this.events.get(resource)??[]).flatMap(e=>this.deliveries.get(e.id)??[]))}
 }
 deliver(input:Omit<AttentionDelivery,'at'|'version'>):AttentionDelivery{
  const held=shape(input,['attention','delivery','outcome'])
  const attention=digest(held.attention),delivery=id(held.delivery),outcome=one(held.outcome,['sent','failed'] as const)
  if(![...this.events.values()].some(list=>list.some(e=>e.id===attention)))throw new WaitRefused('no-wait')
  const list=this.deliveries.get(attention)??[],prior=list.find(d=>d.delivery===delivery)
  if(prior){if(prior.outcome!==outcome)throw new WaitRefused('conflict');return copy(prior)}
  const record:AttentionDelivery={version:1,attention,delivery,outcome,at:this.now()}
  this.deliveries.set(attention,[...list,record])
  return copy(record)
 }
 redact(resource:string,waitId:string):RunWait{
  const row=this.find(id(resource),id(waitId))
  if(!row)throw new WaitRefused('no-wait')
  if(row.pending.kind==='question'&&row.pending.question)delete row.pending.question
  if(row.resolution?.answer)delete row.resolution.answer
  if(row.reason==='question')row.redacted=true
  return copy(row)
 }
}

const waitStoreOf=(store:RunStore):WaitRunStore=>{
 const held=store as Partial<WaitRunStore>
 if(held.waitVersion!==1||held.leaseVersion!==1||typeof held.parkWait!=='function'||typeof held.limitRun!=='function'||typeof held.acquireLease!=='function'||store.durability==='single-writer-callback')throw new WaitCapabilityRefused('store-unsupported')
 // A wait that ends with the store's process is not a durable wait.
 if(held.waitLifetime!=='durable'||held.leaseLifetime!=='durable')throw new WaitCapabilityRefused('store-lifetime')
 return held as WaitRunStore
}
const chatStoreOf=(store:ChatStore):ChatStore=>{if(store.consistency==='single-writer-legacy'||typeof store.get!=='function')throw new WaitCapabilityRefused('chat-store-unsupported');return store}
/** Durable waits need a store whose waits and leases outlive its process, and a chat store that is not a legacy
 * single-writer adapter. Anything else refuses by name before a park. */
export function admitWaits(input:{store:RunStore;chatStore?:ChatStore}):void{waitStoreOf(input.store);if(input.chatStore)chatStoreOf(input.chatStore)}
/** #225: set the run's deadline in its store. The earliest deadline the store holds wins; from it every lease of the run
 * expires no later than it, so no commit, claim or effect admission is accepted at or after it. */
export async function limitRun(store:RunStore,limit:RunLimit,signal?:AbortSignal):Promise<RunLimit>{
 const held=waitStoreOf(store),input=within(()=>readRunLimit(limit))
 const out=await held.limitRun(input,signal)
 return within(()=>readRunLimit(out))
}

export interface ParkInput {
 store:RunStore;fence:LeaseFence;wait:string;continuation:string;condition:WaitCondition;timeoutAt?:string
 /** The model the trusted host admitted the run with; pinned with the rest of the run's execution authority. */
 model:PinnedModel
 /** #225: the host's deadline policy. An approval wait needs approvalTtlMs; it is left out of any other wait. */
 limits?:WaitLimits
 /** The SHA-256 of the host's wake key (wakeKeyDigest(key)); the key stays the host's. */
 wakeKeyDigest:string
 /** The host's evidence for each provider resource the paused run held. Uncertain evidence is retained, never a clean park. */
 release:ReleaseEvidence[]
 /** The retained user_question event a question wait is for. */
 question?:string
 /** #226: the trusted re-authorization refusal the run's dispatch ended in (reauthorizationOf on the fenced view the run
  * used). It parks one reauthorization wait, under the same lease it was minted under. */
 reauthorization?:ReauthorizationRequired
 chat?:{store:ChatStore;id:string};retainUntil?:string;signal?:AbortSignal
}
async function retainedQuestion(store:RunStore,run:string,event:string,signal?:AbortSignal):Promise<UserQuestion|undefined>{
 let after:string|null=null,found:UserQuestion|undefined,last:string|undefined
 for(;;){
  const page=await store.readEvents(run,after,STORAGE_PAGE_LIMIT,signal)
  for(const item of page.items){const stored=readStoredRunEvent(item.bytes);if(stored.event.type==='user_question'){last=stored.event.id;if(stored.event.id===event)found=stored.event.question}}
  if(!page.hasMore||page.nextCursor===null||page.nextCursor===after)break
  after=page.nextCursor
 }
 // Only the run's latest question is pending; an earlier one was answered by a later turn.
 return last===event?found:undefined
}
/** Park a paused run: read its pending decision and its pins from the retained checkpoint or event log, then commit the
 * wait and its one needs_attention event and release the lease in one store transaction. */
export async function parkRun(input:ParkInput):Promise<{wait:RunWait;attention:AttentionEvent}>{
 const store=waitStoreOf(input.store),fence=readLeaseFence(input.fence)
 const chat=input.chat?{store:chatStoreOf(input.chat.store),id:id(input.chat.id)}:undefined
 const head=await store.load(fence.resource,input.signal)
 if(!head||head.checkpoint===null)throw new WaitRefused('nothing-pending')
 let checkpoint
 try{checkpoint=readExecution(JSON.parse(new TextDecoder('utf-8',{fatal:true}).decode(head.checkpoint)))}catch{checkpoint=undefined}
 if(!checkpoint)throw new WaitRefused('nothing-pending')
 let pending:PendingDecision,approval:ApprovalBinding|undefined,disposition:ReauthDisposition|undefined
 let limits=input.limits===undefined?undefined:within(()=>readWaitLimits(input.limits))
 if(input.reauthorization!==undefined){
  // #226: only a trusted, invocation-bound disposition parks a run for re-authorization: one the package minted from the
  // run's proven effect host during that live dispatch, not yet used, under this park's own lease, for the never-admitted
  // dispatch the head holds. Anything else keeps the operation's unknown outcome and is reconciled.
  if(input.question!==undefined)return invalid()
  disposition=reauthDispositionOf(input.reauthorization)
  if(!disposition)throw new ReauthorizationRefused(trustedReauthorization(input.reauthorization)?'used':'untrusted')
  if(disposition.fence.owner!==fence.owner||disposition.fence.epoch!==fence.epoch||disposition.resource!==fence.resource)throw new ReauthorizationRefused('stale')
  if(!holdsNeverAdmitted(head.checkpoint,disposition))throw new ReauthorizationRefused('unbound')
  pending={kind:'reauthorization',operation:disposition.operation.operation,source:disposition.operation.source,tool:disposition.operation.tool,connection:disposition.connection,cause:disposition.cause}
 }else if(checkpoint.status==='paused'){
  if(input.question!==undefined)return invalid()
  const asked=checkpoint.approval?.state==='asked'?checkpoint.approval:undefined
  // #265 M3, #266 M5: a pause no package input can resume is never parked, so it never closes its run. A plan decision
  // is no grant, and a provider-managed allowance takes none (extendExecution).
  if(!asked&&(checkpoint.planState?.pending!==undefined||checkpoint.unit!=='model-requests'))throw new WaitRefused('not-resumable')
  pending=asked?{kind:'approval',callId:asked.callId,name:asked.name,tool:asked.tool}:{kind:'budget',unit:'model-requests',used:checkpoint.used,limit:checkpoint.limit}
  // #225: an unattended approval never waits indefinitely; it is bound to the exact call the person reviews.
  if(asked){if(limits?.approvalTtlMs===undefined)throw new WaitRefused('approval-ttl-required');approval=within(()=>approvalBinding(asked))}
 }else{
  if(input.question===undefined)throw new WaitRefused('nothing-pending')
  const prompt=await retainedQuestion(store,fence.resource,id(input.question),input.signal)
  if(!prompt)throw new WaitRefused('nothing-pending')
  pending={kind:'question',event:input.question,questionDigest:questionDigest(prompt),question:prompt}
 }
 if(!approval&&limits?.approvalTtlMs!==undefined){const {approvalTtlMs:_,...rest}=limits;limits=Object.keys(rest).length?rest:undefined}
 const pins=within(()=>pinsFrom(checkpoint,input.model,disposition!==undefined))
 let chatBinding:{id:string;revision:string}|undefined
 if(chat){const entry=await chat.store.get(chat.id,input.signal);if(!entry)return invalid();chatBinding={id:chat.id,revision:entry.revision}}
 const park=readWaitPark({version:1,resource:fence.resource,wait:input.wait,reason:pending.kind,pending,condition:input.condition,...(input.timeoutAt===undefined?{}:{timeoutAt:input.timeoutAt}),
  ...(limits===undefined?{}:{limits}),...(approval===undefined?{}:{approval}),continuation:input.continuation,
  checkpoint:{revision:head.revision,digest:checkpointBytesDigest(head.checkpoint)},pins,...(chatBinding?{chat:chatBinding}:{}),wakeKeyDigest:input.wakeKeyDigest,release:input.release,...(input.retainUntil===undefined?{}:{retainUntil:input.retainUntil}),...(disposition?{reauthorization:disposition}:{})})
 const parked=await store.parkWait(park,fence,input.signal)
 if(input.reauthorization!==undefined)useReauthorization(input.reauthorization)
 return parked
}
/** Deliver host wake evidence to the store. A store error leaves the outcome unknown: read the wait to reconcile it. */
export async function wakeRun(store:RunStore,evidence:WakeEvidence,signal?:AbortSignal):Promise<WakeDisposition>{return waitStoreOf(store).wakeWait(readWakeEvidence(evidence),signal)}

export interface ApprovalDecisionInput {callId:string;approved:boolean;note?:string}
export interface BudgetGrantInput {from:number;to:number;at:string}
export interface ResumeInput {
 store:RunStore;resource:string;wait:string
 /** The wake evidence id whose acknowledgement the host holds. */
 evidence:string
 owner:string;durationMs:number
 /** An approval wait continues only with the person's decision for its call; a wake is never one. */
 approval?:ApprovalDecisionInput
 /** A budget wait continues only with the person's grant from its paused limit; a wake is never one. */
 grant?:BudgetGrantInput
 /** The host's reconciliation of each uncertain release. */
 reconciled?:ReleaseEvidence[]
 /** #227: the host's current evidence (run-resume.json#/$defs/evidence): what the continuation would run under now,
  * with any explicit admission of a changed model or reference. Without it nothing resumes. */
 current?:unknown
 /** #226: a reauthorization wait continues only with the host's or the person's explicit action
  * (run-reauth.json#/$defs/action); a wake is never one. */
 reauthorization?:unknown
 chat?:ChatStore;signal?:AbortSignal
}
export interface ResumedWait {lease:RunLease;fence:LeaseFence;wait:RunWait;revalidation:Revalidation;approval?:ApprovalDecisionInput;grant?:BudgetGrantInput;resolution?:QuestionResolution;reauthorized?:Reauthorized}
function decisionFor(wait:RunWait,input:ResumeInput):{approval?:ApprovalDecisionInput;grant?:BudgetGrantInput}{
 if(wait.reason==='approval'){
  if(input.grant!==undefined||input.reauthorization!==undefined)throw new WaitRefused('decision-required')
  const decision=input.approval
  if(!object(decision)||decision.callId!==(wait.pending as {callId:string}).callId||typeof decision.approved!=='boolean'||decision.note!==undefined&&(typeof decision.note!=='string'||decision.note.length>2000)||Object.keys(decision).some(k=>!['callId','approved','note'].includes(k)))throw new WaitRefused('decision-required')
  return {approval:{callId:decision.callId,approved:decision.approved,...(decision.note===undefined?{}:{note:decision.note})}}
 }
 if(wait.reason==='budget'){
  if(input.approval!==undefined||input.reauthorization!==undefined)throw new WaitRefused('decision-required')
  const grant=input.grant,pending=wait.pending as {limit:number;unit:string}
  // #265 M4: the grant is dated by a store-clock time, the form both writers produce, in both languages.
  if(!object(grant)||pending.unit!=='model-requests'||grant.from!==pending.limit||!Number.isSafeInteger(grant.to)||grant.to<=grant.from||grant.to>100||!validWaitTime(grant.at)||Object.keys(grant).some(k=>!['from','to','at'].includes(k)))throw new WaitRefused('decision-required')
  return {grant:{from:grant.from,to:grant.to,at:grant.at}}
 }
 if(wait.reason==='reauthorization'){
  // #226: the host's or the person's action is checked against the wait (reauthorize) after the revalidation.
  if(input.approval!==undefined||input.grant!==undefined||input.reauthorization===undefined)throw new WaitRefused('decision-required')
  return {}
 }
 // A question's continuation is its resolution; an approval, a grant or a re-authorization is not a question's to take.
 if(input.approval!==undefined||input.grant!==undefined||input.reauthorization!==undefined)throw new WaitRefused('decision-required')
 return {}
}
/** Resume a woken wait: check the host's decision, revalidate the pinned execution authority against the host's current
 * evidence (#227), then have the store check the deadline, the head and the release reconciliation, lease the run and
 * claim the continuation once, in one transaction. The host then continues the run through the fenced store with the
 * returned decision; nothing is dispatched before that claim. */
export async function resumeParked(input:ResumeInput):Promise<ResumedWait>{
 const store=waitStoreOf(input.store),resource=id(input.resource),waitId=id(input.wait)
 const held=await store.readWait(resource,waitId,input.signal)
 if(!held)throw new WaitRefused('no-wait')
 // #265 M6: the store's record is read through the reader, and it must be the wait asked for.
 const wait=readRunWait(copy(held))
 if(wait.resource!==resource||wait.wait!==waitId)return invalid()
 if(!wait.disposition||wait.disposition.evidence!==input.evidence)throw new WaitRefused('unacknowledged')
 if(wait.state==='resumed')throw new WaitRefused('already-resumed')
 if(wait.state==='timed-out'&&wait.reason!=='question')throw new WaitRefused('timed-out')
 if(wait.state==='expired')throw new WaitRefused('deadline-passed')
 const decision=decisionFor(wait,input)
 if(wait.chat){
  if(!input.chat)throw new WaitCapabilityRefused('chat-store-unsupported')
  const entry=await chatStoreOf(input.chat).get(wait.chat.id,input.signal)
  if(!entry||entry.revision!==wait.chat.revision)throw new WaitRefused('chat-changed')
 }
 // #227: missing current evidence refuses; nothing of the continuation is claimed before the revalidation passes.
 if(input.current===undefined)throw new ResumeRefused('evidence-missing')
 const revalidation=revalidateResume(wait,input.current)
 // #226: the identity, the action's time and the permissions, before anything is claimed.
 const reauthorized=wait.reason==='reauthorization'?reauthorize(wait,input.reauthorization):undefined
 // ADR-0029: the claim's lease holds the principals the wait pinned, so a takeover by resume keeps them.
 const lease:LeaseAcquire={resource:wait.resource,owner:input.owner,expectedRevision:wait.checkpoint.revision,durationMs:input.durationMs,...(wait.pins.principal?{principal:copy(wait.pins.principal)}:{})}
 const claimed=readClaimed(await store.resumeWait({resource:wait.resource,wait:wait.wait,evidence:input.evidence,lease,reconciled:input.reconciled??[],revalidation,...(reauthorized?{reauthorized}:{})},input.signal),wait,lease,revalidation,reauthorized)
 const fence=fenceOf(claimed.lease)
 return {lease:claimed.lease,fence,wait:claimed.wait,revalidation:copy(claimed.wait.revalidation!),...decision,...(claimed.wait.resolution?{resolution:copy(claimed.wait.resolution)}:{}),...(claimed.wait.reauthorized?{reauthorized:copy(claimed.wait.reauthorized)}:{})}
}
/** #226: the fenced view a resumed reauthorization wait's continuation runs on: unattended admission of the store and
 * effect host, the claim's fence, and the one grant the claim gives: the never-admitted operation may be dispatched again,
 * under its own identity, once on this view, if the run asks for that exact request; nothing else that the run holds as
 * unknown is. The store's own records decide it: the wait must be a reauthorization wait resumed under this fence with its
 * refresh recorded, and the fence the run's held lease. The head's compare-and-set and the effect host's idempotency
 * identity keep a second view from sending it twice. */
export async function reauthorizedRunStore(input:{store:RunStore;effectHost:EffectHost;resumed:{fence:LeaseFence;wait:{resource:string;wait:string}};signal?:AbortSignal}):Promise<RunStore>{
 const store=waitStoreOf(input.store),fence=readLeaseFence(input.resumed.fence)
 const held=await store.readWait(id(input.resumed.wait.resource),id(input.resumed.wait.wait),input.signal)
 if(!held)throw new WaitRefused('no-wait')
 const wait=readRunWait(copy(held)),lease=await store.readLease(fence.resource,input.signal)
 if(wait.reason!=='reauthorization'||wait.state!=='resumed'||!wait.reauthorized||wait.resource!==fence.resource||wait.resumed?.owner!==fence.owner||wait.resumed.epoch!==fence.epoch)return invalid()
 if(!lease||lease.owner!==fence.owner||lease.epoch!==fence.epoch||lease.state!=='held')throw new LeaseRefused('stale-epoch')
 const view=unattendedRunStore({store:input.store,fence,effectHost:input.effectHost})
 attachReauthorizedDispatch(view,wait.reauthorization!.operation,fence)
 return view
}
/** #266 M6: the store's claim is checked before its fence is handed out. It must be this wait, resumed by the lease it
 * returns, with the revalidation it was given, and that lease must be held by the claimer for this run and end no later
 * than the run's deadline and an approval's expiry the wait records. A store that answers otherwise is refused invalid:
 * nothing of the continuation is handed to the host. */
function readClaimed(value:unknown,wait:RunWait,request:LeaseAcquire,revalidation:Revalidation,reauthorized?:Reauthorized):{wait:RunWait;lease:RunLease}{
 if(!object(value))return invalid()
 const held=readRunWait(copy(value.wait))
 let lease:RunLease
 try{lease=readRunLease(copy(value.lease))}catch{return invalid()}
 const expiry=at(lease.expiry),bound=[held.deadline?.run,held.reason==='approval'?held.deadline?.approval:undefined].filter((t):t is string=>t!==undefined)
 if(held.resource!==wait.resource||held.wait!==wait.wait||held.state!=='resumed'||held.resumed?.owner!==lease.owner||held.resumed.epoch!==lease.epoch||canonicalJSON(held.revalidation)!==canonicalJSON(revalidation)||canonicalJSON(held.checkpoint)!==canonicalJSON(wait.checkpoint)
  ||lease.resource!==wait.resource||lease.owner!==request.owner||lease.state!=='held'||!sameBinding(lease.principal,request.principal)||held.reason==='approval'&&held.deadline?.approval===undefined||bound.some(t=>expiry>at(t))
  ||canonicalJSON(held.reauthorized??null)!==canonicalJSON(reauthorized??null)||held.reason==='reauthorization'&&held.deadline?.approval!==undefined&&expiry>at(held.deadline.approval))return invalid()
 return {wait:held,lease}
}

/** The park/wake report of one run, read from its retained waits (exportWaits), never from a prompt. Content whose wait
 * was redacted, or whose retainUntil is at or before `now` (the host's time), is unavailable with only its digest. */
export function reportFromExport(source:WaitExport,now?:Date):WaitReport{
 const held=readWaitExport(source),entries:WaitReportEntry[]=[]
 const time=now===undefined?undefined:now.getTime()
 for(const wait of held.waits){
  const expired=time!==undefined&&wait.retainUntil!==undefined&&at(wait.retainUntil)<=time
  const content=(available:{question?:UserQuestion;answer?:QuestionDraft},digest:string):ReportContent=>expired?{state:'expired',digest}:wait.redacted?{state:'redacted',digest}:{state:'available',digest,...copy(available)}
  const pending=wait.pending
  entries.push({wait:wait.wait,decision:'park',reason:wait.reason,at:wait.parked.at,owner:wait.parked.owner,epoch:wait.parked.epoch,
   ...(pending.kind==='approval'?{subject:pending.callId}:pending.kind==='question'?{subject:pending.event}:pending.kind==='reauthorization'?{subject:pending.connection}:{}),condition:copy(wait.condition),...(wait.timeoutAt===undefined?{}:{timeoutAt:wait.timeoutAt}),
   ...(wait.deadline===undefined?{}:{deadline:wait.deadline.at}),release:wait.release.some(r=>r.outcome==='uncertain')?'uncertain':'clean',...(pending.kind==='question'?{content:content({question:pending.question},pending.questionDigest)}:{})})
  if(wait.disposition)entries.push({wait:wait.wait,decision:wait.disposition.kind==='timeout'?'timeout':'wake',reason:wait.reason,at:wait.disposition.at,evidence:wait.disposition.evidence,wake:wait.disposition.kind})
  const resolution=()=>{
   if(!wait.resolution||pending.kind!=='question')return
   if(wait.resolution.kind==='answered')entries.push({wait:wait.wait,decision:'answer',reason:wait.reason,at:wait.resolution.at,actor:wait.resolution.actor!,content:content({answer:wait.resolution.answer},wait.resolution.answerDigest!)})
   else entries.push({wait:wait.wait,decision:'unanswered',reason:wait.reason,at:wait.resolution.at,content:content({question:pending.question},pending.questionDigest)})
  }
  // #225: a deadline that ended the wait is reported after what woke it, and before the unanswered end it caused; #266
  // N1: one that ended an unsettled approved call after its claim is reported after the resume.
  const deadline=()=>{if(wait.expired)entries.push({wait:wait.wait,decision:'deadline',reason:wait.reason,at:wait.expired.at,evidence:wait.expired.evidence,wake:'deadline'})}
  if(wait.disposition)resolution()
  if(!wait.resumed)deadline()
  if(!wait.disposition)resolution()
  if(wait.resumed){entries.push({wait:wait.wait,decision:'resume',reason:wait.reason,at:wait.resumed.at,owner:wait.resumed.owner,epoch:wait.resumed.epoch});deadline()}
 }
 return readWaitReport({version:1,resource:held.resource,entries})
}
/** The retained wait records of one run, exactly as the store holds them. */
export async function exportWaits(store:RunStore,resource:string,signal?:AbortSignal):Promise<WaitExport>{
 const held=store as Partial<WaitStore>
 if(held.waitVersion!==1||typeof held.exportWaits!=='function')throw new WaitCapabilityRefused('store-unsupported')
 return readWaitExport(await held.exportWaits(id(resource),signal))
}
export async function waitReport(store:RunStore,resource:string,now?:Date,signal?:AbortSignal):Promise<WaitReport>{return reportFromExport(await exportWaits(store,resource,signal),now)}
