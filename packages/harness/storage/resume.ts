import {AIFailureError,failure} from '../failures'
import {canonicalJSON} from '../context/chat'
import {sha256} from '../records'
import {storageId} from './common'
import {principalBinding,sameBinding,validPrincipalBinding,type PrincipalBinding} from '../principal'
import type {ApprovalRecord,ExecutionCheckpoint} from '../execution/state'
import type {PolicyMembers} from '../policy'
/* Deadlines, approval expiry and resume revalidation for unattended runs (ADR-0025 decisions 3 and 5, #225 and #227;
 * contract/schemas/run-resume.json). The package validates and records: the host supplies its deadline policy, the
 * model it admitted the run with and, on resume, its current evidence; the store decides every time by its own clock.
 * Nothing here schedules, polls or substitutes a model. The Go twin is go/context/run_resume.go. */
export const RESUME_RECORD_VERSION=1 as const
/** A wait or approval duration: 1 ms to 30 days of the store clock. */
export const DEADLINE_DURATION_MAX_MS=2_592_000_000
export const RESUME_SOURCE_LIMIT=64
export const RESUME_ARTIFACT_LIMIT=256
export const RESUME_ADMISSION_LIMIT=64
export const RESUME_CHANGE_LIMIT=384
export type ResumeRefusalReason='invalid'|'evidence-missing'|'identity-changed'|'lineage-changed'|'model-retired'|'model-changed'|'reference-changed'|'policy-changed'|'approval-changed'|'admission-invalid'|'principal-changed'
export const RESUME_REFUSAL_REASONS:readonly ResumeRefusalReason[]=['invalid','evidence-missing','identity-changed','lineage-changed','model-retired','model-changed','reference-changed','policy-changed','approval-changed','admission-invalid','principal-changed']

export interface WaitLimits {run?:string;waitMs?:number;approvalTtlMs?:number}
export interface RunLimit {version:1;resource:string;run:string}
export interface WaitDeadline {at:string;by:'run'|'wait'|'approval';run?:string;wait?:string;approval?:string}
export interface ApprovalBinding {callId:string;callDigest:string;source:string}
export interface PinnedModel {provider:string;model:string}
export interface SourcePin {source:string;identityDigest?:string;descriptorDigest?:string}
export interface ArtifactPin {kind:'artifact'|'dialect';id:string;digest:string}
export interface PlanPin {revision:string;accepted?:string}
export interface ResumePins {model:PinnedModel;policy:string;plan?:PlanPin;sources:SourcePin[];artifacts:ArtifactPin[];/** ADR-0029: the principals a delegated run's wait holds; a resume keeps them. */principal?:PrincipalBinding}
export interface ResumeAdmission {kind:'model'|'reference';subject:string;from:string;to:string;actor:string;at:string}
export interface ResumeEvidence {version:1;resource:string;wait:string;model:PinnedModel&{status:'available'|'retired'};policy:{digest:string;layer?:string};plan?:PlanPin;sources:SourcePin[];artifacts:ArtifactPin[];admissions?:ResumeAdmission[];principal?:PrincipalBinding}
export interface ResumeChange {kind:'model'|'reference'|'policy';subject:string;from:string;to:string;actor?:string;at?:string;layer?:string}
export interface Revalidation {version:1;resource:string;wait:string;pins:string;evidence:string;changes:ResumeChange[]}

/** A named revalidation refusal. Nothing was claimed or dispatched; the wait still holds the run. */
export class ResumeRefused extends AIFailureError {
 constructor(readonly reason:ResumeRefusalReason){super({...failure('stale-state','storage','not-started'),detail:`ResumeRefused: ${reason}.`});this.name='ResumeRefused'}
 record():{version:1;reason:ResumeRefusalReason}{return {version:1,reason:this.reason}}
}

const TIME=/^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{3}Z$/,DIGEST=/^sha256:[a-f0-9]{64}$/,SOURCE=/^[A-Za-z][A-Za-z0-9-]{0,23}$/,PROVIDER=/^[a-z][a-z0-9-]{0,31}$/
const object=(v:unknown):v is Record<string,unknown>=>v!==null&&typeof v==='object'&&!Array.isArray(v)&&(Object.getPrototypeOf(v)===Object.prototype||Object.getPrototypeOf(v)===null)
const invalid=():never=>{throw new ResumeRefused('invalid')}
function shape(v:unknown,required:readonly string[],optional:readonly string[]=[]):Record<string,unknown>{
 if(!object(v)||required.some(k=>!Object.hasOwn(v,k))||Object.keys(v).some(k=>!required.includes(k)&&!optional.includes(k)))return invalid()
 for(const k of optional)if(Object.hasOwn(v,k)&&v[k]===undefined)return invalid()
 return v
}
const id=(v:unknown):string=>storageId(v)?v:invalid()
const digest=(v:unknown):string=>typeof v==='string'&&DIGEST.test(v)?v:invalid()
const source=(v:unknown):string=>typeof v==='string'&&SOURCE.test(v)?v:invalid()
const one=<T extends string>(v:unknown,values:readonly T[]):T=>values.includes(v as T)?v as T:invalid()
/** A store-clock time: UTC with exactly millisecond precision. */
export const validResumeTime=(v:unknown):v is string=>{if(typeof v!=='string'||!TIME.test(v))return false;const ms=Date.parse(v);return Number.isSafeInteger(ms)&&new Date(ms).toISOString()===v}
const time=(v:unknown):string=>validResumeTime(v)?v:invalid()
const duration=(v:unknown):number=>Number.isSafeInteger(v)&&Number(v)>=1&&Number(v)<=DEADLINE_DURATION_MAX_MS?Number(v):invalid()
/** An admission's subject or value: 1 to 320 UTF-8 bytes, no control characters (a source, an artifact or a model). */
const label=(v:unknown):string=>typeof v==='string'&&v.length>0&&new TextEncoder().encode(v).length<=320&&!/[\u0000-\u001f\u007f-\u009f\ud800-\udfff]/u.test(v)?v:invalid()
const millis=(text:string)=>Date.parse(text)
const iso=(ms:number):string=>{const text=Number.isSafeInteger(ms)?new Date(ms).toISOString():'';return validResumeTime(text)?text:invalid()}
/** UTF-8 byte order, the order Go compares strings in. */
function byteOrder(a:string,b:string):number{const x=new TextEncoder().encode(a),y=new TextEncoder().encode(b);for(let i=0;i<Math.min(x.length,y.length);i++)if(x[i]!==y[i])return x[i]!-y[i]!;return x.length-y.length}
const artifactOrder=(a:ArtifactPin,b:ArtifactPin)=>a.kind===b.kind?byteOrder(a.id,b.id):a.kind==='artifact'?-1:1

export function readWaitLimits(v:unknown):WaitLimits{
 const held=shape(v,[],['run','waitMs','approvalTtlMs'])
 if(Object.keys(held).length===0)return invalid()
 return {...(held.run===undefined?{}:{run:time(held.run)}),...(held.waitMs===undefined?{}:{waitMs:duration(held.waitMs)}),...(held.approvalTtlMs===undefined?{}:{approvalTtlMs:duration(held.approvalTtlMs)})}
}
export function readRunLimit(v:unknown):RunLimit{const held=shape(v,['version','resource','run']);if(held.version!==1)return invalid();return {version:1,resource:id(held.resource),run:time(held.run)}}
const BY=['run','wait','approval'] as const
/** The deadline a park stamps: the earliest of the run's deadline (the earliest the store holds and this park's), the
 * park time plus waitMs and, for an approval wait, plus approvalTtlMs. Undefined when none applies. */
export function stampDeadline(limits:WaitLimits|undefined,parkedAt:number,runCeiling:number|undefined):WaitDeadline|undefined{
 const run=[limits?.run===undefined?undefined:millis(limits.run),runCeiling].filter((t):t is number=>t!==undefined)
 const times={run:run.length?Math.min(...run):undefined,wait:limits?.waitMs===undefined?undefined:parkedAt+limits.waitMs,approval:limits?.approvalTtlMs===undefined?undefined:parkedAt+limits.approvalTtlMs}
 const present=BY.filter(by=>times[by]!==undefined)
 if(!present.length)return undefined
 const at=Math.min(...present.map(by=>times[by]!))
 // #225: the earliest applicable deadline wins; on a tie the run's is named first.
 const by=present.find(name=>times[name]===at)!
 return {at:iso(at),by,...Object.fromEntries(present.map(name=>[name,iso(times[name]!)]))}
}
/** Reads a stamped deadline and holds its rules against the park's limits and time. */
export function readWaitDeadline(v:unknown,limits:WaitLimits|undefined,parkedAt:string):WaitDeadline{
 const held=shape(v,['at','by'],['run','wait','approval'])
 const deadline:WaitDeadline={at:time(held.at),by:one(held.by,BY),...(held.run===undefined?{}:{run:time(held.run)}),...(held.wait===undefined?{}:{wait:time(held.wait)}),...(held.approval===undefined?{}:{approval:time(held.approval)})}
 const present=BY.filter(by=>deadline[by]!==undefined),parked=millis(parkedAt)
 if(!present.length)return invalid()
 const at=Math.min(...present.map(by=>millis(deadline[by]!)))
 if(millis(deadline.at)!==at||present.find(by=>millis(deadline[by]!)===at)!==deadline.by||at<=parked)return invalid()
 // The wait and approval times are the park's own durations; the run's is never later than this park's run limit.
 if((limits?.waitMs===undefined)!==(deadline.wait===undefined)||deadline.wait!==undefined&&millis(deadline.wait)!==parked+limits!.waitMs!)return invalid()
 if((limits?.approvalTtlMs===undefined)!==(deadline.approval===undefined)||deadline.approval!==undefined&&millis(deadline.approval)!==parked+limits!.approvalTtlMs!)return invalid()
 if(limits?.run!==undefined&&(deadline.run===undefined||millis(deadline.run)>millis(limits.run)))return invalid()
 return deadline
}
export function readApprovalBinding(v:unknown):ApprovalBinding{
 const held=shape(v,['callId','callDigest','source'])
 return {callId:typeof held.callId==='string'&&held.callId.length>=1&&held.callId.length<=256?held.callId:invalid(),callDigest:digest(held.callDigest),source:source(held.source)}
}
function readModel(v:unknown):PinnedModel{const held=shape(v,['provider','model']);return {provider:typeof held.provider==='string'&&PROVIDER.test(held.provider)?held.provider:invalid(),model:id(held.model)}}
function readSourcePins(v:unknown):SourcePin[]{
 if(!Array.isArray(v)||v.length>RESUME_SOURCE_LIMIT)return invalid()
 const list=v.map(item=>{const held=shape(item,['source'],['identityDigest','descriptorDigest']);return {source:source(held.source),...(held.identityDigest===undefined?{}:{identityDigest:digest(held.identityDigest)}),...(held.descriptorDigest===undefined?{}:{descriptorDigest:digest(held.descriptorDigest)})}})
 // Unique and sorted by source: one pin per source, and one canonical order for the digest.
 for(let i=1;i<list.length;i++)if(byteOrder(list[i-1]!.source,list[i]!.source)>=0)return invalid()
 return list
}
function readArtifactPins(v:unknown):ArtifactPin[]{
 if(!Array.isArray(v)||v.length>RESUME_ARTIFACT_LIMIT)return invalid()
 const list=v.map(item=>{const held=shape(item,['kind','id','digest']);return {kind:one(held.kind,['artifact','dialect'] as const),id:id(held.id),digest:digest(held.digest)}})
 for(let i=1;i<list.length;i++)if(artifactOrder(list[i-1]!,list[i]!)>=0)return invalid()
 return list
}
function readPlanPin(v:unknown):PlanPin{const held=shape(v,['revision'],['accepted']);return {revision:digest(held.revision),...(held.accepted===undefined?{}:{accepted:id(held.accepted)})}}
function readBinding(v:unknown):PrincipalBinding{return validPrincipalBinding(v)?{sub:{kind:v.sub.kind,issuer:v.sub.issuer,subject:v.sub.subject},rootActor:v.rootActor,host:v.host}:invalid()}
export function readResumePins(v:unknown):ResumePins{
 const held=shape(v,['model','policy','sources','artifacts'],['plan','principal'])
 return {model:readModel(held.model),policy:digest(held.policy),...(held.plan===undefined?{}:{plan:readPlanPin(held.plan)}),sources:readSourcePins(held.sources),artifacts:readArtifactPins(held.artifacts),...(held.principal===undefined?{}:{principal:readBinding(held.principal)})}
}
function readAdmission(v:unknown):ResumeAdmission{const held=shape(v,['kind','subject','from','to','actor','at']);return {kind:one(held.kind,['model','reference'] as const),subject:label(held.subject),from:label(held.from),to:label(held.to),actor:id(held.actor),at:time(held.at)}}
export function readResumeEvidence(v:unknown):ResumeEvidence{
 const held=shape(v,['version','resource','wait','model','policy','sources','artifacts'],['plan','admissions','principal'])
 if(held.version!==1)return invalid()
 const model=shape(held.model,['provider','model','status']),policy=shape(held.policy,['digest'],['layer'])
 const evidence:ResumeEvidence={version:1,resource:id(held.resource),wait:id(held.wait),model:{...readModel({provider:model.provider,model:model.model}),status:one(model.status,['available','retired'] as const)},policy:{digest:digest(policy.digest),...(policy.layer===undefined?{}:{layer:digest(policy.layer)})},
  ...(held.plan===undefined?{}:{plan:readPlanPin(held.plan)}),sources:readSourcePins(held.sources),artifacts:readArtifactPins(held.artifacts),...(held.principal===undefined?{}:{principal:readBinding(held.principal)})}
 if(held.admissions!==undefined){
  if(!Array.isArray(held.admissions)||held.admissions.length>RESUME_ADMISSION_LIMIT)return invalid()
  const admissions=held.admissions.map(readAdmission)
  if(new Set(admissions.map(a=>canonicalJSON([a.kind,a.subject]))).size!==admissions.length)return invalid()
  evidence.admissions=admissions
 }
 return evidence
}
function readChange(v:unknown):ResumeChange{
 if(!object(v))return invalid()
 const kind=one(v.kind,['model','reference','policy'] as const)
 const held=kind==='policy'?shape(v,['kind','subject','from','to','layer']):shape(v,['kind','subject','from','to','actor','at'])
 const change:ResumeChange={kind,subject:label(held.subject),from:label(held.from),to:label(held.to)}
 if(kind==='policy'){if(change.subject!=='policy')return invalid();change.from=digest(change.from);change.to=digest(change.to);change.layer=digest(held.layer)}
 else{
  if(kind==='model'&&change.subject!=='model')return invalid()
  if(kind==='reference'){if(!/^(source|artifact|dialect):/.test(change.subject))return invalid();digest(change.from);digest(change.to)}
  change.actor=id(held.actor);change.at=time(held.at)
 }
 if(change.from===change.to)return invalid()
 return change
}
/** Reads a version 1 revalidation record. */
export function readRevalidation(v:unknown):Revalidation{
 const held=shape(v,['version','resource','wait','pins','evidence','changes'])
 if(held.version!==1||!Array.isArray(held.changes)||held.changes.length>RESUME_CHANGE_LIMIT)return invalid()
 const changes=held.changes.map(readChange)
 if(new Set(changes.map(c=>canonicalJSON([c.kind,c.subject]))).size!==changes.length)return invalid()
 return {version:1,resource:id(held.resource),wait:id(held.wait),pins:digest(held.pins),evidence:digest(held.evidence),changes}
}
export function readResumeRefusal(v:unknown):{version:1;reason:ResumeRefusalReason}{const held=shape(v,['version','reason']);if(held.version!==1)return invalid();return {version:1,reason:one(held.reason,RESUME_REFUSAL_REASONS)}}

const digestOf=(value:unknown)=>`sha256:${sha256(canonicalJSON(value))}`
/** The identity of a resolved policy: the SHA-256 of the canonical JSON of its members, or of {} without them. */
export const policyIdentity=(members:PolicyMembers|undefined)=>digestOf(members??{})
export const pinsDigest=(pins:ResumePins)=>digestOf(readResumePins(pins))
export const resumeEvidenceDigest=(evidence:ResumeEvidence)=>digestOf(readResumeEvidence(evidence))
/** An approval wait's binding: the asked call's id, the digest of the call the person reviewed (its wire name, tool name,
 * arguments and Runtime binding) and its source. Read from the retained checkpoint, never from the caller. */
export function approvalBinding(approval:ApprovalRecord):ApprovalBinding{
 return readApprovalBinding({callId:approval.callId,callDigest:digestOf({name:approval.name,tool:approval.tool,args:approval.args,...(approval.runtime===undefined?{}:{runtime:approval.runtime})}),source:approval.source.id})
}
/** The execution authority a park pins, read from the retained checkpoint, with the model the trusted host admitted the
 * run with: the resolved policy's identity, the Plan lineage, each source's identity digest (and the asked approval's
 * source with its descriptor digest), and the last observed revision of each artifact. */
export function pinsFrom(checkpoint:ExecutionCheckpoint,model:PinnedModel):ResumePins{
 const sources=new Map<string,SourcePin>()
 for(const row of checkpoint.sourceIdentities??[])sources.set(row.source,{source:row.source,identityDigest:row.identityDigest})
 const asked=checkpoint.approval?.state==='asked'?checkpoint.approval:undefined
 if(asked){
  const record=asked.source,pin={...(sources.get(record.id)??{source:record.id})}
  if(record.identityDigest!==undefined){if(pin.identityDigest!==undefined&&pin.identityDigest!==record.identityDigest)return invalid();pin.identityDigest=record.identityDigest}
  if(record.toolListDigest!==undefined)pin.descriptorDigest=record.toolListDigest
  sources.set(record.id,pin)
 }
 const artifacts=new Map<string,ArtifactPin>()
 for(const row of checkpoint.durable?.observations??[])if(row.artifact)artifacts.set(canonicalJSON([row.artifact.kind,row.artifact.id]),{kind:row.artifact.kind,id:row.artifact.id,digest:row.artifact.digest})
 const plan=checkpoint.planState?{revision:checkpoint.planState.revision,...(checkpoint.planState.accepted?{accepted:checkpoint.planState.accepted.revision}:{})}:undefined
 return readResumePins({model,policy:policyIdentity(checkpoint.policy),...(plan?{plan}:{}),sources:[...sources.values()].sort((a,b)=>byteOrder(a.source,b.source)),artifacts:[...artifacts.values()].sort(artifactOrder),...(checkpoint.principal?{principal:principalBinding(checkpoint.principal)}:{})})
}

/** What revalidation reads of a wait: its run, identity, reason, pins and an approval wait's binding. */
export interface RevalidatedWait {resource:string;wait:string;reason:string;pins:ResumePins;approval?:ApprovalBinding}
/** #227: revalidate a woken wait's pinned execution authority against the host's current evidence, before anything of
 * its continuation is claimed or dispatched. Missing evidence refuses; a changed source identity or Plan lineage needs a
 * new run; a retired model parks for an explicit decision and is never replaced; a changed model or reference needs the
 * matching explicit admission; a changed policy identity needs the trusted host's new layer; an approval whose source's
 * descriptor or policy changed cannot run. Returns the record of what the continuation runs under, old and new. */
export function revalidateResume(wait:RevalidatedWait,input:unknown):Revalidation{
 const current=readResumeEvidence(input),pins=readResumePins(wait.pins)
 // ADR-0029: a resume keeps the wait's principals: evidence of none for a delegated run is missing, and other principals, or some for an undelegated run, are a changed principal, never a refresh.
 if(pins.principal&&!current.principal)throw new ResumeRefused('evidence-missing')
 if(!sameBinding(pins.principal,current.principal))throw new ResumeRefused('principal-changed')
 if(current.resource!==wait.resource||current.wait!==wait.wait)return invalid()
 const sources=new Map(current.sources.map(s=>[s.source,s])),artifacts=new Map(current.artifacts.map(a=>[canonicalJSON([a.kind,a.id]),a]))
 for(const pin of pins.sources){
  const now=sources.get(pin.source)
  if(!now||pin.identityDigest!==undefined&&now.identityDigest===undefined||pin.descriptorDigest!==undefined&&now.descriptorDigest===undefined)throw new ResumeRefused('evidence-missing')
 }
 for(const pin of pins.artifacts)if(!artifacts.has(canonicalJSON([pin.kind,pin.id])))throw new ResumeRefused('evidence-missing')
 if(pins.plan&&(!current.plan||pins.plan.accepted!==undefined&&current.plan.accepted===undefined))throw new ResumeRefused('evidence-missing')
 // ADR-0019: a changed identity is never relabelled as a refresh; it needs a new admitted run.
 for(const pin of pins.sources)if(pin.identityDigest!==undefined&&sources.get(pin.source)!.identityDigest!==pin.identityDigest)throw new ResumeRefused('identity-changed')
 if((pins.plan===undefined)!==(current.plan===undefined)||pins.plan&&(pins.plan.revision!==current.plan!.revision||pins.plan.accepted!==current.plan!.accepted))throw new ResumeRefused('lineage-changed')
 // A retired model parks for an explicit decision: nothing is substituted, whatever an admission says.
 if(current.model.status==='retired')throw new ResumeRefused('model-retired')
 const admissions=current.admissions??[],used=new Set<number>(),changes:ResumeChange[]=[]
 const admitted=(kind:'model'|'reference',subject:string,from:string,to:string)=>{
  const at=admissions.findIndex(a=>a.kind===kind&&a.subject===subject)
  if(at<0)return undefined
  const admission=admissions[at]!
  // An admission is exactly this change: from the pin to the current value.
  if(admission.from!==from||admission.to!==to)throw new ResumeRefused('admission-invalid')
  used.add(at);return admission
 }
 const pinnedModel=`${pins.model.provider}/${pins.model.model}`,currentModel=`${current.model.provider}/${current.model.model}`
 if(pinnedModel!==currentModel){
  const admission=admitted('model','model',pinnedModel,currentModel)
  if(!admission)throw new ResumeRefused('model-changed')
  changes.push({kind:'model',subject:'model',from:pinnedModel,to:currentModel,actor:admission.actor,at:admission.at})
 }
 const reference=(subject:string,from:string,to:string)=>{
  const admission=admitted('reference',subject,from,to)
  if(!admission)throw new ResumeRefused('reference-changed')
  changes.push({kind:'reference',subject,from,to,actor:admission.actor,at:admission.at})
 }
 for(const pin of pins.sources){const now=sources.get(pin.source)!;if(pin.descriptorDigest!==undefined&&now.descriptorDigest!==pin.descriptorDigest)reference(`source:${pin.source}`,pin.descriptorDigest,now.descriptorDigest!)}
 for(const pin of pins.artifacts){const now=artifacts.get(canonicalJSON([pin.kind,pin.id]))!;if(now.digest!==pin.digest)reference(`${pin.kind}:${pin.id}`,pin.digest,now.digest)}
 // The pinned policy holds unless the trusted host supplies a new layer; both identities are kept.
 if(current.policy.digest!==pins.policy){
  if(current.policy.layer===undefined)throw new ResumeRefused('policy-changed')
  changes.push({kind:'policy',subject:'policy',from:pins.policy,to:current.policy.digest,layer:current.policy.layer})
 }
 // #225: an approval is bound to the reviewed call's source, descriptor and policy identities; a changed one cannot run.
 if(wait.reason==='approval'){
  const binding=readApprovalBinding(wait.approval)
  if(changes.some(c=>c.kind==='policy'||c.kind==='reference'&&c.subject===`source:${binding.source}`))throw new ResumeRefused('approval-changed')
 }
 if(admissions.some((_,at)=>!used.has(at)))throw new ResumeRefused('admission-invalid')
 return readRevalidation({version:1,resource:wait.resource,wait:wait.wait,pins:pinsDigest(pins),evidence:digestOf(current),changes})
}
