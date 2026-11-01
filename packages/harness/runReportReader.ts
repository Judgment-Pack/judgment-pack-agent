import {validTurnRoute} from './turnRoute'
import frozen from './contract/run-report-v1.json'
import current from './contract/run-report-v2.json'
import delegated from './contract/run-report-v3.json'
import {validPrincipalRecord} from './principal'
import {validRunFeedback} from './history/metadata'
import {historyDigestPattern} from './history/codec'
import {canonicalJSON} from './context/chat'
import {utf8Bytes} from './context/policy'
import {validTaskSteps,validDelegationFigures,validSourceFigures,validSourceIdentities,validSkillLoads,validUsage,type TaskStep} from './execution/state'
import {validPlanState} from './execution/plan'
import {validCriticRecord} from './criticSwitch'
import {validRecordHeader,PROVENANCE_SCHEMA} from './records'
import {readFailure} from './failures'
import {MAX_RUN_REPORT_BYTES,RunReportOverflow,type RetainedRunReport,type RunReportFacts} from './runReport'

/** These guards admit every <=4 MiB report allowed by the frozen graph (maximum depth 13; pending candidates cannot nest).
 * Each JSON value occupies at least one byte, so the node bound does not narrow that contract. */
export const MAX_RUN_REPORT_DEPTH=64, MAX_RUN_REPORT_NODES=4*1024*1024
export class RunReportInvalid extends Error {constructor(){super('RunReportInvalid: the retained run report is invalid or unsupported.');this.name='RunReportInvalid'}}
const invalid=():never=>{throw new RunReportInvalid()}
const object=(v:unknown):v is Record<string,unknown>=>v!==null&&typeof v==='object'&&!Array.isArray(v)
const typedArrayPrototype=Object.getPrototypeOf(Uint8Array.prototype)
const typedArrayTag=Object.getOwnPropertyDescriptor(typedArrayPrototype,Symbol.toStringTag)!.get!
const typedArrayBytes=Object.getOwnPropertyDescriptor(typedArrayPrototype,'byteLength')!.get!
const unicode=(s:string)=>!/[\uD800-\uDBFF](?![\uDC00-\uDFFF])|(?<![\uD800-\uDBFF])[\uDC00-\uDFFF]/u.test(s)

/** Copy data descriptors only: neither getters, toJSON nor custom iterators are invoked. */
function detached(input:unknown):unknown{
 let nodes=0,bytes=0
 const seen=new Set<object>()
 const charge=(n:number)=>{bytes+=n;if(bytes>MAX_RUN_REPORT_BYTES)throw new RunReportOverflow()}
 function copy(v:unknown,depth:number):unknown{
  if(++nodes>MAX_RUN_REPORT_NODES||depth>MAX_RUN_REPORT_DEPTH)throw new RunReportOverflow()
  if(v===null||typeof v==='boolean'){charge(v===null?4:v?4:5);return v}
  if(typeof v==='number'){if(!Number.isSafeInteger(v))invalid();charge(String(v).length);return v}
  if(typeof v==='string'){if(!unicode(v))invalid();charge(utf8Bytes(JSON.stringify(v)));return v}
  if(typeof v!=='object'||!v) return invalid()
  if(seen.has(v))return invalid()
  const array=Array.isArray(v),proto=Object.getPrototypeOf(v)
  if(array?proto!==Array.prototype:proto!==Object.prototype&&proto!==null)return invalid()
  const descriptors=Object.getOwnPropertyDescriptors(v),keys=Reflect.ownKeys(descriptors)
  if(keys.some(k=>typeof k!=='string'))return invalid()
  seen.add(v);charge(2)
  let out:unknown
  if(array){
   const length=descriptors.length?.value
   if(!Number.isSafeInteger(length)||length<0||length>MAX_RUN_REPORT_NODES)return invalid()
   if(keys.length!==length+1)return invalid()
   const rows:unknown[]=[]
   for(let at=0;at<length;at++){
    const d=descriptors[String(at)]
    if(!d||!d.enumerable||!Object.hasOwn(d,'value'))return invalid()
    if(at)charge(1);rows.push(copy(d.value,depth+1))
   }
   out=rows
  }else{
   const row:Record<string,unknown>=Object.create(null)
   for(const [at,key] of (keys as string[]).entries()){
    const d=descriptors[key]!
    if(!d.enumerable||!Object.hasOwn(d,'value')||!unicode(key))return invalid()
    charge((at?1:0)+utf8Bytes(JSON.stringify(key))+1);row[key]=copy(d.value,depth+1)
   }
   out=row
  }
  seen.delete(v);return out
 }
 return copy(input,0)
}

// All numeric domains in report v1 are integers. Check the decimal token exactly before
// Number can round a fractional/unsafe spelling to an apparently safe integer.
function exactNumber(token:string):number{
 const parts=/^(-?)([0-9]+)(?:\.([0-9]+))?(?:[eE]([+-]?[0-9]+))?$/.exec(token)!
 let digits=(parts[2]!+(parts[3]??'')).replace(/^0+/,'')
 if(!digits)return 0
 const exp=Number(parts[4]??0)
 let scale=exp-(parts[3]?.length??0)
 while(digits.endsWith('0')){digits=digits.slice(0,-1);scale++}
 if(!Number.isSafeInteger(scale)||scale<0||digits.length+scale>16)return invalid()
 const exact=BigInt(digits)*10n**BigInt(scale)
 if(exact>BigInt(Number.MAX_SAFE_INTEGER))return invalid()
 return (parts[1]?-1:1)*Number(exact)
}
function parse(text:string):unknown{
 let at=0,nodes=0
 const space=()=>{while(at<text.length&&/[\x20\t\n\r]/.test(text[at]!))at++}
 function string():string{
  const start=at++
  while(at<text.length){const c=text[at++]!;if(c==='"'){const s=JSON.parse(text.slice(start,at)) as string;if(!unicode(s))return invalid();return s}if(c==='\\')at++}
  return invalid()
 }
 function value(depth:number):unknown{
  if(++nodes>MAX_RUN_REPORT_NODES||depth>MAX_RUN_REPORT_DEPTH)throw new RunReportOverflow()
  space();const c=text[at]
  if(c==='"')return string()
  if(c==='{'||c==='['){
   at++;space();const array=c==='[',end=array?']':'}',out:unknown[]|Record<string,unknown>=array?[]:Object.create(null)
   if(text[at]===end){at++;return out}
   while(at<text.length){
    space();let key=''
    if(!array){if(text[at]!=='"')return invalid();key=string();if(Object.hasOwn(out,key))return invalid();space();if(text[at++]!==':')return invalid()}
    const held=value(depth+1);if(array)(out as unknown[]).push(held);else(out as Record<string,unknown>)[key]=held
    space();const next=text[at++];if(next===end)return out;if(next!==',')return invalid()
   }
   return invalid()
  }
  for(const [token,held] of [['null',null],['true',true],['false',false]] as const)if(text.startsWith(token,at)){at+=token.length;return held}
  const match=/^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?/.exec(text.slice(at))
  if(!match)return invalid();at+=match[0].length;return exactNumber(match[0])
 }
 const out=value(0);space();if(at!==text.length)return invalid();return out
}

type Shape={ $ref?:string;type?:string;const?:unknown;enum?:unknown[];properties?:Record<string,Shape>;additionalProperties?:boolean|Shape;items?:Shape;required?:string[];minimum?:number;maximum?:number;minLength?:number;maxLength?:number;pattern?:string;minItems?:number;maxItems?:number;uniqueItems?:boolean;minProperties?:number;propertyNames?:Shape;dependentRequired?:Record<string,string[]>;anyOf?:Shape[];oneOf?:Shape[];allOf?:Shape[];if?:Shape;then?:Shape;format?:string;not?:Shape }
const frozenDefinitions=frozen.definitions as unknown as Record<string,Shape>
/** This is deliberately the frozen report graph, not today's checkpoint structural reader. */
function matches(s:Shape,v:unknown,definitions:Record<string,Shape>=frozenDefinitions):boolean{
 if(s.not&&matches(s.not,v,definitions))return false
 if(s.$ref)return matches(definitions[s.$ref]!,v,definitions)
 if(Object.hasOwn(s,'const')&&s.const!==v||s.enum&&!s.enum.includes(v))return false
 if(s.type){const actual=v===null?'null':Array.isArray(v)?'array':typeof v;if(s.type==='integer'?!Number.isSafeInteger(v):s.type!==actual)return false}
 if(typeof v==='number'&&(s.minimum!==undefined&&v<s.minimum||s.maximum!==undefined&&v>s.maximum))return false
 if(typeof v==='string'&&(s.minLength!==undefined&&v.length<s.minLength||s.maxLength!==undefined&&v.length>s.maxLength||s.pattern&&!new RegExp(s.pattern).test(v)))return false
 if(Array.isArray(v)){
  if(s.minItems!==undefined&&v.length<s.minItems||s.maxItems!==undefined&&v.length>s.maxItems||s.items&&!v.every(x=>matches(s.items!,x,definitions)))return false
  if(s.uniqueItems&&new Set(v.map(canonicalJSON)).size!==v.length)return false
 }
 if(object(v)){
  if(s.required?.some(k=>!Object.hasOwn(v,k))||s.minProperties!==undefined&&Object.keys(v).length<s.minProperties)return false
  for(const [key,held] of Object.entries(v)){
   if(s.propertyNames&&!matches(s.propertyNames,key,definitions))return false
   const child=s.properties&&Object.hasOwn(s.properties,key)?s.properties[key]:undefined
   if(child){if(!matches(child,held,definitions))return false}
   else if(s.additionalProperties===false)return false
   else if(object(s.additionalProperties)&&!matches(s.additionalProperties as Shape,held,definitions))return false
  }
  for(const [key,required] of Object.entries(s.dependentRequired??{}))if(Object.hasOwn(v,key)&&required.some(k=>!Object.hasOwn(v,k)))return false
 }
 if(s.anyOf&&!s.anyOf.some(x=>matches(x,v,definitions)))return false
 if(s.oneOf&&s.oneOf.filter(x=>matches(x,v,definitions)).length!==1)return false
 if(s.allOf&&!s.allOf.every(x=>matches(x,v,definitions)))return false
 if(s.if&&matches(s.if,v,definitions)&&s.then&&!matches(s.then,v,definitions))return false
 return true
}
/** Source-version restrictions apply only to typed Plan facts, including retained revisions. */
function oldSourceTaskFacts(v:Record<string,unknown>):boolean{
 if(v.version!==2||v.checkpointVersion===3)return true // report 3 reads only checkpoint 4, whose Plan is current
 const step=(s:unknown):boolean=>!object(s)||(
  !Object.hasOwn(s,'dependsOn')&&
  !Object.hasOwn(s,'budget')&&
  !Object.hasOwn(s,'delegation')&&
  !(object(s.evidence)&&Object.hasOwn(s.evidence,'child')))
 const steps=(s:unknown)=>!Array.isArray(s)||s.every(step)
 const facts=(value:Record<string,unknown>):boolean=>{
  if(!steps(value.plan))return false
  const state=value.planState;if(!object(state))return true
  if(Object.hasOwn(state,'taskSchedule'))return false
  if(Object.hasOwn(state,'taskEnvelope'))return false
  if(Array.isArray(state.history)&&state.history.some(row=>object(row)&&Array.isArray(row.diff)&&row.diff.some(change=>object(change)&&(!step(change.before)||!step(change.after)))))return false
  return !(object(state.pending)&&object(state.pending.candidate))||facts(state.pending.candidate)
 }
 return facts(v)
}
function validRepeatReport(v:Record<string,unknown>):boolean {
 if(v.repeatTools===undefined)return true
 if(!(v.version===2&&v.checkpointVersion===3||v.version===3&&v.checkpointVersion===4)||!object(v.repeatTools)||!object(v.repeatTools.policy)||!Array.isArray(v.repeatTools.executions))return false
 const p=v.repeatTools.policy,mode=p['repeatTools.mode'],remind=p['repeatTools.remindAt'],stop=p['repeatTools.stopAt']
 if(!object(mode)||mode.value!=='on'||!object(remind)||!object(stop)||!Number.isSafeInteger(remind.value)||!Number.isSafeInteger(stop.value)||Number(remind.value)<1||Number(stop.value)<Number(remind.value))return false
 const seen=new Set<string>()
 return v.repeatTools.executions.length>0&&v.repeatTools.executions.every(row=>{if(!object(row)||typeof row.execution!=='string'||seen.has(row.execution)||row.remindAt!==remind.value||row.stopAt!==stop.value||!Number.isSafeInteger(row.count)||Number(row.count)<0||row.reminded!==(Number(row.count)>=Number(row.remindAt))||row.stopped!==(Number(row.count)>=Number(row.stopAt))||row.reminderDelivered===true&&row.reminded!==true)return false;seen.add(row.execution);return true})
}
function semantic(v:RunReportFacts):boolean{
 if(v.used>v.limit)return false
 const capped=Object.hasOwn(v,'requestCap')
 if(capped&&(v.unit!=='model-requests'||v.limit!==v.requestCap||v.grants.length!==0))return false
 let limit=capped?v.requestCap!:v.unit==='model-requests'?20:1
 for(const grant of v.grants){if(grant.from!==limit||grant.to<=limit||!Number.isFinite(Date.parse(grant.at)))return false;limit=grant.to}
 if(v.limit!==limit||v.plan.length>0&&!validTaskSteps(v.plan)||!validPlanState(v.plan as TaskStep[],v.planState))return false
 if(v.delegation!==undefined&&!validDelegationFigures(v.delegation,v.unit)||v.sources!==undefined&&!validSourceFigures(v.sources)||v.sourceIdentities!==undefined&&!validSourceIdentities(v.sourceIdentities)||v.skills!==undefined&&!validSkillLoads(v.skills)||v.critic!==undefined&&!validCriticRecord(v.critic)||v.usage!==undefined&&!validUsage(v.usage))return false
 if(v.childUsage!==undefined&&(new Set(v.childUsage.map(x=>x.id)).size!==v.childUsage.length||v.childUsage.some(x=>!validUsage(x.usage))))return false
 if(v.failover!==undefined&&(new Set(v.failover.map(x=>x.role)).size!==v.failover.length||v.failover.some(x=>x.consumed!==x.history.length)))return false
 if(v.prompts?.some((row,i,rows)=>i>0&&row.request<=rows[i-1]!.request))return false
 if(v.completions?.some(row=>row.request>v.used||row.failure!==undefined&&!readFailure(row.failure)||['schema','digest','retainUntil'].some(k=>Object.hasOwn(row,k))&&!validRecordHeader(row,PROVENANCE_SCHEMA)))return false
 return true
}
/** Read untrusted report data or UTF-8 JSON bytes/text. Returns detached facts only, never resume authority.
 * Already-rounded object numbers cannot reveal the caller's original numeric spelling. */
export function readRunReport(input:unknown):RetainedRunReport{
 try{
  let held:unknown
  if(typeof input==='string'){
   if(utf8Bytes(input)>MAX_RUN_REPORT_BYTES)throw new RunReportOverflow()
   if(!unicode(input))return invalid();held=parse(input)
  }else if(ArrayBuffer.isView(input)&&typedArrayTag.call(input)==='Uint8Array'){
   const bytes=input as Uint8Array
   if(typedArrayBytes.call(bytes)>MAX_RUN_REPORT_BYTES)throw new RunReportOverflow()
   held=parse(new TextDecoder('utf-8',{fatal:true,ignoreBOM:true}).decode(bytes))
  }else held=detached(input)
  // ADR-0029: report 3 (a delegated run's) reads as report 2 does, under its own graph, with its principal.
  const v3=object(held)&&held.version===3,v2=object(held)&&held.version===2||v3
  const graph=v3?delegated:current
  if(!(v2?matches(graph.root as Shape,held,graph.definitions as unknown as Record<string,Shape>):matches(frozen.root as Shape,held))||!semantic(held as RunReportFacts))return invalid()
  if(v3&&object(held)&&(held.checkpointVersion!==4||!validPrincipalRecord(held.principal)))return invalid()
  if(v2&&object(held)&&!oldSourceTaskFacts(held)||v2&&object(held)&&!validRepeatReport(held))return invalid()
  if(v2&&object(held)&&held.route!==undefined&&!validTurnRoute(held.route))return invalid()
  if(v2&&object(held)){if(held.checkpointVersion!==3&&held.checkpointVersion!==4&&(held.usageTotals!==undefined||held.route!==undefined||!Array.isArray(held.feedback)||held.feedback.length===0))return invalid();if(held.feedback!==undefined){if(!Array.isArray(held.feedback)||held.feedback.length<1||held.feedback.length>20000)return invalid();const prior=new Map<string,{sourceDigest:string;record:import('./history/types').RunFeedback}>();for(const item of held.feedback){if(!object(item)||Object.keys(item).length!==2||!historyDigestPattern.test(String(item.sourceDigest))||!validRunFeedback(item.record)||item.record.runId!==held.id)return invalid();const old=prior.get(item.record.id);if(item.record.sequence!==(old?old.record.sequence+1:1)||item.record.previous!==(old?.sourceDigest??null)||old&&canonicalJSON(old.record.target)!==canonicalJSON(item.record.target))return invalid();prior.set(item.record.id,item as never)}}}
  if(utf8Bytes(canonicalJSON(held))>MAX_RUN_REPORT_BYTES)throw new RunReportOverflow()
  return held as RetainedRunReport
 }catch(error){if(error instanceof RunReportOverflow||error instanceof RunReportInvalid)throw error;return invalid()}
}

/** Closed read-only presentation for explicitly derived archive/candidate data. Source hashes label
 * historical records; this accessor never validates a display as a native checkpoint or resume input. */
export function readRunReportDisplay(input:unknown):RetainedRunReport{
 try{const held=detached(input),v3=object(held)&&held.version===3,v2=object(held)&&held.version===2||v3,graph=v3?delegated:current
 if(!(v2?matches(graph.root as Shape,held,graph.definitions as unknown as Record<string,Shape>):matches(frozen.root as Shape,held))||utf8Bytes(canonicalJSON(held))>MAX_RUN_REPORT_BYTES)return invalid()
 if(v3&&object(held)&&(held.checkpointVersion!==4||!validPrincipalRecord(held.principal)))return invalid()
 if(v2&&object(held)&&!oldSourceTaskFacts(held)||v2&&object(held)&&!validRepeatReport(held))return invalid()
 return held as RetainedRunReport
 }catch{return invalid()}
}
