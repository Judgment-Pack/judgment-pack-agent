/** ADR-0017. Host-owned patterns operate only on the outbound boundary. */
import base from './contract/output-patterns.json'
import {compilePattern,matchRanges,PATTERN_STEPS,type CompiledPattern} from './context/pattern'
import type {AssistantEvent} from './engine'
import type {PolicyMembers} from './policy'
export type OutputWhere='message'|'proposal'|'tool argument'|'memory proposal'|'draft edit'
export type OutputCounts=Record<string,number>
export interface RecordMarker {redactions:OutputCounts}
export interface OutputPattern {name:string;pattern:string}
export class OutputRefused extends Error {constructor(readonly pattern:string){super(`Output refused: ${pattern}.`)}}
export const validOutputCounts=(v:unknown):v is OutputCounts=>!!v&&typeof v==='object'&&!Array.isArray(v)&&Object.entries(v).every(([k,n])=>/^[a-z][a-z0-9-]{0,63}$/.test(k)&&Number.isSafeInteger(n)&&Number(n)>=0)
export function readOutputPatterns(value:unknown):OutputPattern[]{
 if(!Array.isArray(value)||value.length>32)throw Error('outputPatterns must be a list of at most 32 named patterns')
 const names=new Set([...base.patterns.map(p=>p.name),'work-bound','proposal-validation','invalid-output'])
 return value.map(v=>{
  if(!v||typeof v!=='object'||Object.keys(v).sort().join(',')!=='name,pattern'||typeof v.name!=='string'||!/^[a-z][a-z0-9-]{0,63}$/.test(v.name)||names.has(v.name)||typeof v.pattern!=='string')throw Error('Invalid or duplicate output pattern name')
  compilePattern(v.pattern);names.add(v.name);return {name:v.name,pattern:v.pattern}
 })
}
export function readOutputAllow(value:unknown):string[]{
 if(!Array.isArray(value)||value.length>64||value.some(v=>typeof v!=='string'||!/^[a-z][a-z0-9-]{0,63}$/.test(v))||new Set(value).size!==value.length)throw Error('Invalid outputAllow names')
 return [...value]
}
export class RecordsRefused extends OutputRefused {}
export class OutputGuard {
 declaredNames:ReadonlySet<string>=new Set()
 readonly mode:'off'|'redact'|'refuse'
 private patterns:{name:string;compiled:CompiledPattern}[]
 constructor(members:PolicyMembers,private counts:()=>OutputCounts,private notify:(e:AssistantEvent)=>void){
  this.mode=(members.outputGuard?.value??'redact') as typeof this.mode
  const extra=readOutputPatterns(members.outputPatterns?.value??[]),allow=readOutputAllow(members.outputAllow?.value??[])
  this.patterns=[...base.patterns,...extra].filter(p=>!allow.includes(p.name)).map(p=>({name:p.name,compiled:compilePattern(p.pattern)}))
 }
 private count(pattern:string){const counts=this.counts();counts[pattern]=(Object.hasOwn(counts,pattern)?counts[pattern]!:0)+1}
 adopt(pattern:string){this.count(pattern)}
 private record(where:OutputWhere,pattern:string,action:'redact'|'refuse',record:boolean,marker?:RecordMarker){
  if(marker){if(action==='redact')marker.redactions[pattern]=(Object.hasOwn(marker.redactions,pattern)?marker.redactions[pattern]!:0)+1;return}
  if(!record)return
  this.count(pattern)
  this.notify({type:'output_guarded',where,pattern,action})
 }
 refuse(where:OutputWhere,pattern:string,marker?:RecordMarker):never{
  this.record(where,pattern,'refuse',true,marker)
  this.notify({type:'guardrail',tool:marker?'records':where,action:'refused',reason:'output refused',detail:`Output refused: ${pattern}.`})
  throw marker?new RecordsRefused(pattern):new OutputRefused(pattern)
 }
 value<T>(input:T,where:OutputWhere,record=true,marker?:RecordMarker):T {
  if(this.mode==='off')return input
  const budget={steps:PATTERN_STEPS}
  const text=(s:string):string=>{
   const buckets:{start:number;end:number;name:string;order:number}[][]=[]
   for(const [order,p] of this.patterns.entries()){
    const ranges=matchRanges(p.compiled,s,budget)
    if(ranges===null)return this.refuse(where,'work-bound',marker)
    buckets.push(ranges.map(([start,end])=>({start,end,name:p.name,order})))
   }
   // Fixed-size k-way merge: linear in the number of matches, without sorting text-sized lists.
   const matches:typeof buckets[number]=[],heads=buckets.map(()=>0)
   for(;;){let selected=-1;for(let i=0;i<buckets.length;i++){const candidate=buckets[i]![heads[i]!];if(!candidate)continue;const prior=selected<0?undefined:buckets[selected]![heads[selected]!];if(!prior||candidate.start<prior.start||candidate.start===prior.start&&candidate.order<prior.order)selected=i}if(selected<0)break;matches.push(buckets[selected]![heads[selected]!]!);heads[selected]!++}
   let out='',at=0
   for(let i=0;i<matches.length;i++){
    const m=matches[i]!;if(m.start<at)continue
    let end=m.end
    while(i+1<matches.length&&matches[i+1]!.start<end){i++;end=Math.max(end,matches[i]!.end)}
    if(this.mode==='refuse')return this.refuse(where,m.name,marker)
    this.record(where,m.name,'redact',record,marker)
    out+=s.slice(at,m.start)+`[redacted: ${m.name}]`;at=end
   }
   return out+s.slice(at)
  }
  const walk=(v:unknown):unknown=>{
   if(typeof v==='string')return marker?v.split(/(\[redacted: [a-z][a-z0-9-]{0,63}\]|Output refused: [a-z][a-z0-9-]{0,63}\.)/g).map((part,i)=>i%2?part:text(part)).join(''):text(v)
   if(where==='draft edit'&&(v===null||typeof v==='number'||typeof v==='boolean')){const raw=JSON.stringify(v),clean=text(raw);return clean===raw?v:clean}
   if(Array.isArray(v))return v.map(walk)
   if(v&&typeof v==='object'){const out={...v} as Record<string,unknown>;for(const k of Object.keys(v).sort()){const key=walk(k) as string,clean=walk((v as Record<string,unknown>)[k]);if(key!==k)delete out[k];out[key]=clean}return out}
   return v
  }
  return walk(input) as T
 }
}
export const outputWhere=(name:string):OutputWhere=>name==='propose_pack'?'proposal':name==='remember'?'memory proposal':['propose_edit','propose_insert','propose_delete','undo_edit'].includes(name)?'draft edit':'tool argument'
