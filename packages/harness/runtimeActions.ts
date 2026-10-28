import type {SourceRecord, ApprovalAnswer} from './toolSources'
/** Trusted host observation of the execution reached by one actual Runtime connector.
 * Labels are display data. Digests and launch/session identifiers bind approval. */
export interface RuntimeExecutionOwner {
 artifactDigest:string; processId:string; projectId:string; projectLabel:string;
 sessionId:string; principalDigest:string; principalLabel:string; scopeDigest:string; scopeLabel:string
}
export interface RuntimeApprovalBinding {owner:RuntimeExecutionOwner; admissionDigest:string}
export interface RuntimeApprovalRequest {name:string;tool:string;args:Record<string,unknown>;source:SourceRecord;runtime:RuntimeApprovalBinding}
export interface RuntimeActionPort {
 owner:RuntimeExecutionOwner
 /** Observe this connection's actual execution. The host must prevent or signal retargeting
  * between observation and its transport; a cached configuration is not an observation. */
 current(signal:AbortSignal):Promise<RuntimeExecutionOwner>
 approve?:(call:RuntimeApprovalRequest)=>ApprovalAnswer
}
const fields=['artifactDigest','processId','projectId','projectLabel','sessionId','principalDigest','principalLabel','scopeDigest','scopeLabel'] as const
export function validRuntimeOwner(value:unknown):value is RuntimeExecutionOwner {
 if(!value||typeof value!=='object'||Array.isArray(value))return false
 const v=value as Record<string,unknown>
 return Object.keys(v).length===fields.length&&fields.every(k=>Object.hasOwn(v,k)&&typeof v[k]==='string'&&(v[k] as string).length>0&&(v[k] as string).length<=128&&!/[\u0000-\u001f\u007f]/.test(v[k] as string))&&['artifactDigest','principalDigest','scopeDigest'].every(k=>/^sha256:[0-9a-f]{64}$/.test(v[k] as string))
}
export function validRuntimeBinding(value:unknown):value is RuntimeApprovalBinding {
 if(!value||typeof value!=='object'||Array.isArray(value))return false
 const v=value as Record<string,unknown>
 return Object.keys(v).length===2&&validRuntimeOwner(v.owner)&&typeof v.admissionDigest==='string'&&/^sha256:[0-9a-f]{64}$/.test(v.admissionDigest)
}
