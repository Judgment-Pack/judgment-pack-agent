import type {DelegationRequest,DelegatedTask} from '../delegationContract'
export interface DurableHandoff {callId:string;batch:number;state:'released'|'children'|'answered'|'continued';request:DelegationRequest;tasks:DelegatedTask[];answer?:{tasks:unknown[];note:string};continuation?:string}
export function validHandoff(value:unknown):value is DurableHandoff {
 if(!value||typeof value!=='object')return false
 const v=value as DurableHandoff
 return typeof v.callId==='string'&&v.callId.length>0&&v.callId.length<=256&&Number.isSafeInteger(v.batch)&&v.batch>=1&&v.batch<=5&&['released','children','answered','continued'].includes(v.state)&&Array.isArray(v.request?.tasks)&&v.request.tasks.length>=1&&v.request.tasks.length<=3&&Array.isArray(v.tasks)&&v.tasks.length<=v.request.tasks.length&&new Set(v.tasks.map(t=>t.id)).size===v.tasks.length&&v.tasks.every(t=>typeof t.id==='string'&&['queued','running','complete','failed','interrupted'].includes(t.state))&&(['answered','continued'].includes(v.state)===(v.answer!==undefined))&&(v.continuation===undefined||typeof v.continuation==='string'&&new TextEncoder().encode(v.continuation).length<=4*1024*1024)
}
