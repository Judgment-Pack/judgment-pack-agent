import {storageId} from './common'
import {canonicalJSON} from '../context/chat'
import {sha256} from '../records'
import type {DurableBook} from './writer'
export function validDurable(value:unknown):value is DurableBook {
 if(!value||typeof value!=='object')return false
 const v=value as DurableBook,digest=(x:unknown)=>typeof x==='string'&&/^sha256:[a-f0-9]{64}$/.test(x),scope=(s:DurableBook['dispatches'][number]['scope'])=>s&&storageId(s.execution)&&storageId(s.role)&&(s.task===undefined||storageId(s.task))
 try{return Array.isArray(v.dispatches)&&v.dispatches.length<=400&&Array.isArray(v.observations)&&v.observations.length<=400&&Array.isArray(v.children)&&v.children.length<=32&&new Set(v.dispatches.map(d=>d.id)).size===v.dispatches.length&&v.dispatches.every(d=>storageId(d.id)&&scope(d.scope)&&storageId(d.source)&&storageId(d.tool)&&digest(d.fingerprint)&&typeof d.request==='string'&&new TextEncoder().encode(d.request).length<=4*1024*1024&&d.requestDigest===`sha256:${sha256(d.request)}`&&d.fingerprint===`sha256:${sha256(canonicalJSON({kind:d.kind,scope:d.scope,source:d.source,tool:d.tool,request:JSON.parse(d.request)}))}`&&['model','agent','host','runtime'].includes(d.kind)&&['claimed','result','unknown'].includes(d.state)&&typeof d.read==='boolean')&&v.observations.every(o=>storageId(o.id)&&scope(o.scope)&&storageId(o.dispatchId)&&storageId(o.commitId)&&['operation','artifact-read','artifact-produced'].includes(o.kind)&&v.dispatches.some(d=>d.id===o.dispatchId&&d.state==='result'&&d.source===o.source&&canonicalJSON(d.scope)===canonicalJSON(o.scope)))&&v.children.every(c=>scope(c.scope)&&c.checkpoint?.id===c.scope.execution)
 }catch{return false}
}
