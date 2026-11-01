import {canonicalJSON} from './context/chat'
import {sha256} from './records'
import {AIFailureError} from './failures'
import type {DurableDispatch} from './storage/writer'
import type {PolicyMembers} from './policy'

/** The original durable journal admits at most 400 physical dispatches in one root. */
export const REPEAT_DISPATCH_LIMIT=400
export interface RepeatPolicy {remindAt:number;stopAt:number}
export interface RepeatObservation {dispatchId:string;keyDigest:string}
export interface RepeatExecution extends RepeatPolicy {execution:string;count:number;reminded:boolean;reminderDelivered:boolean;stopped:boolean;seen:RepeatObservation[]}
export interface RepeatTools {executions:RepeatExecution[]}
export interface RepeatDetected {type:'repeat_detected';execution:string;dispatchId:string;keyDigest:string;count:number;reminder:boolean;stopped:boolean}
export class RepeatedToolLimit extends AIFailureError {
 constructor(){super({version:1,code:'repeated-work',source:'desk',outcome:'not-started',detail:'RepeatedToolLimit: repeated identical acknowledged tool results reached the host policy threshold.'});this.name='RepeatedToolLimit'}
}
export function repeatPolicy(members:PolicyMembers):RepeatPolicy|undefined {
 if(members['repeatTools.mode']?.value!=='on')return
 return {remindAt:Number(members['repeatTools.remindAt']!.value),stopAt:Number(members['repeatTools.stopAt']!.value)}
}
export function repeatKey(row:DurableDispatch):string {
 if(!['host','runtime'].includes(row.kind)||row.state!=='result'||row.result===undefined||row.failure)throw Error('Repeat result is not acknowledged terminal evidence.')
 if(typeof row.repeatResultJson!=='string'||new TextEncoder().encode(row.repeatResultJson).length>4*1024*1024||canonicalJSON(JSON.parse(row.repeatResultJson))!==canonicalJSON(row.result))throw Error('Exact repeat result custody differs.')
 return 'sha256:'+sha256(canonicalJSON({kind:row.kind,source:row.source,tool:row.tool,args:canonicalRepeatArgs(row.request),resultDigest:'sha256:'+sha256(row.repeatResultJson)}))
}
/** Detached derivation only. The original writer publishes it in the exact result transaction. */
export function observeRepeat(state:RepeatExecution,row:DurableDispatch):{state:RepeatExecution;event?:RepeatDetected} {
 if(row.scope.execution!==state.execution)throw Error('Repeat scope differs.')
 if(state.seen.some(item=>item.dispatchId===row.id))return {state:structuredClone(state)}
 if(state.seen.length>=REPEAT_DISPATCH_LIMIT)throw Error('Repeat dispatch capacity exhausted.')
 const next=structuredClone(state),keyDigest=repeatKey(row),repeated=next.seen.some(item=>item.keyDigest===keyDigest)
 next.seen.push({dispatchId:row.id,keyDigest})
 if(!repeated)return {state:next}
 next.count++;const reminder=!next.reminded&&next.count>=next.remindAt
 next.reminded ||= reminder;next.stopped ||= next.count>=next.stopAt
 return {state:next,event:{type:'repeat_detected',execution:next.execution,dispatchId:row.id,keyDigest,count:next.count,reminder,stopped:next.stopped}}
}
export function validRepeatTools(value:unknown):value is RepeatTools {
 const object=(x:unknown):x is Record<string,unknown>=>!!x&&typeof x==='object'&&!Array.isArray(x)
 const exact=(x:Record<string,unknown>,keys:string[])=>Object.keys(x).length===keys.length&&keys.every(k=>Object.hasOwn(x,k))
 const text=(x:unknown)=>typeof x==='string'&&x.length>0&&x.length<=100
 const count=(x:unknown,min=0)=>Number.isSafeInteger(x)&&Number(x)>=min&&Number(x)<=REPEAT_DISPATCH_LIMIT
 if(!object(value)||!exact(value,['executions'])||!Array.isArray(value.executions)||value.executions.length<1||value.executions.length>33)return false
 let total=0;const executions=new Set(),dispatches=new Set()
 return value.executions.every(row=>{
  if(!object(row)||!exact(row,['execution','remindAt','stopAt','count','reminded','reminderDelivered','stopped','seen'])||!text(row.execution)||executions.has(row.execution)||!count(row.remindAt,1)||!count(row.stopAt,1)||Number(row.remindAt)>Number(row.stopAt)||!count(row.count)||typeof row.reminded!=='boolean'||typeof row.reminderDelivered!=='boolean'||row.reminderDelivered&&!row.reminded||typeof row.stopped!=='boolean'||!Array.isArray(row.seen)||(total+=row.seen.length)>REPEAT_DISPATCH_LIMIT)return false
  executions.add(row.execution);const keys=new Set();let repeats=0
  for(const item of row.seen){if(!object(item)||!exact(item,['dispatchId','keyDigest'])||!text(item.dispatchId)||dispatches.has(item.dispatchId)||typeof item.keyDigest!=='string'||!/^sha256:[a-f0-9]{64}$/.test(item.keyDigest))return false;dispatches.add(item.dispatchId);if(keys.has(item.keyDigest))repeats++;keys.add(item.keyDigest)}
  return row.count===repeats&&row.reminded===(repeats>=Number(row.remindAt))&&row.stopped===(repeats>=Number(row.stopAt))
 })
}

/** Decimal value identity, without IEEE rounding or expanding exponent-sized powers. */
function repeatNumber(text:string):string {
 const match=/^(-?)([0-9]+)(?:\.([0-9]+))?(?:[eE]([+-]?[0-9]+))?$/.exec(text)!
 let digits=(match[2]!+(match[3]??'')).replace(/^0+/,'');if(!digits)return '0'
 let scale=BigInt(match[4]??'0')-BigInt(match[3]?.length??0)
 const tail=/0+$/.exec(digits)?.[0].length??0;if(tail){digits=digits.slice(0,-tail);scale+=BigInt(tail)}
 return match[1]+digits+'e'+scale
}
/** Canonical JSON argument values: sorted UTF16 keys, exact decimal numbers, last duplicate key wins. */
export function canonicalRepeatArgs(text:string):string {
 if(new TextEncoder().encode(text).length>4*1024*1024)throw Error('Repeat arguments exceed the durable bound.')
 let at=0,nodes=0
 const space=()=>{while(/[\x20\t\n\r]/.test(text[at]??'\0'))at++}
 const quoted=()=>{const start=at++;while(at<text.length){const c=text[at++]!;if(c==='\\')at++;else if(c==='"')return JSON.parse(text.slice(start,at)) as string}throw Error('Invalid repeat arguments.')}
 const value=(depth:number):string=>{
  if(depth>64||++nodes>4*1024*1024)throw Error('Repeat argument structure exceeds the durable bound.')
  space();const c=text[at]
  if(c==='"')return JSON.stringify(quoted())
  if(c==='{'||c==='['){
   at++;space();const array=c==='[',end=array?']':'}',items:string[]=[],members=new Map<string,string>()
   if(text[at]===end){at++;return array?'[]':'{}'}
   for(;;){space();let key='';if(!array){if(text[at]!=='"')throw Error('Invalid repeat arguments.');key=quoted();space();if(text[at++]!==':')throw Error('Invalid repeat arguments.')}
    const next=value(depth+1);if(array)items.push(next);else members.set(key,next)
    space();const token=text[at++];if(token===end)return array?'['+items.join(',')+']':'{'+[...members.keys()].sort().map(key=>JSON.stringify(key)+':'+members.get(key)).join(',')+'}';if(token!==',')throw Error('Invalid repeat arguments.')
   }
  }
  for(const token of ['true','false','null'])if(text.startsWith(token,at)){at+=token.length;return token}
  const match=/^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?/.exec(text.slice(at));if(!match)throw Error('Invalid repeat arguments.');at+=match[0].length;return repeatNumber(match[0])
 }
 const out=value(0);space();if(at!==text.length)throw Error('Invalid repeat arguments.');return out
}
/** Representation consistency only; dispatch/result authority remains with the original writer CAS. */
export function validRepeatRecordCustody(value:unknown):boolean {
 try{
  const cp=value as {version:number;repeatTools?:RepeatTools;durable?:{dispatches:DurableDispatch[]}}
  for(const row of cp.durable?.dispatches??[])if(row.repeatResultJson!==undefined){
   if(cp.version<3||!cp.repeatTools?.executions.some(state=>state.execution===row.scope.execution&&state.seen.some(item=>item.dispatchId===row.id&&item.keyDigest===repeatKey(row))))return false
  }
  return true
 }catch{return false}
}
