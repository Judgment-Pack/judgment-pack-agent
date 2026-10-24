import shapes from './contract/record-shapes.json'
import type {AssistantEvent} from './engine'
import type {PolicyLayer} from './policy'

type Shape={ $ref?:string;type?:string|string[];const?:unknown;enum?:unknown[];properties?:Record<string,Shape>;patternProperties?:Record<string,Shape>;additionalProperties?:boolean|Shape;items?:Shape;oneOf?:Shape[];anyOf?:Shape[];allOf?:Shape[];required?:string[];$defs?:Record<string,Shape> }
type Graph=Record<string,Shape>
export type RecordKind='checkpoint'|'event'|'policy'
export const RECORD_VERSIONS={checkpoint:2,event:1,policy:1} as const
const files={checkpoint:'checkpoint.json',event:'run-event.json',policy:'policy.json'} as const
const object=(v:unknown):v is Record<string,unknown>=>v!==null&&typeof v==='object'&&!Array.isArray(v)
export class UnsupportedRecordVersion extends Error {
 get failureCode(){return this.kind==='policy'?'configuration':'stale-state'}
 constructor(readonly kind:RecordKind,readonly version:number){super(`Unsupported record version: ${kind}.`);this.name='UnsupportedRecordVersion'}
 event(){return {version:1 as const,type:'guardrail' as const,tool:this.kind,action:'refused' as const,reason:'record version unsupported' as const,detail:this.message}}
}
export class UnknownRecordMember extends Error {
 get failureCode(){return this.kind==='policy'?'configuration':'stale-state'}
 constructor(readonly member:string,readonly kind:RecordKind='checkpoint'){super(`Unknown record member: ${member.slice(0,256)}.`);this.name='UnknownRecordMember'}
 event(){return {version:1 as const,type:'guardrail' as const,tool:'records',action:'refused' as const,reason:'record member unsupported' as const,detail:'Record contains an unsupported member.'}}
}
export class InvalidRecord extends Error {constructor(){super('Record is invalid.');this.name='InvalidRecord'}}
const invalid=():never=>{throw new InvalidRecord()} // fixed text, independent of supplied values
function versionOf(kind:RecordKind,value:unknown):number{
 if(!object(value))return invalid()
 const version=value.version
 if(version===undefined&&!Object.hasOwn(value,'version')&&kind!=='checkpoint')return 0
 if(!Number.isSafeInteger(version)||Number(version)<1)return invalid()
 if(Number(version)>RECORD_VERSIONS[kind])throw new UnsupportedRecordVersion(kind,Number(version))
 return Number(version)
}
const pointer=(path:string,key:string)=>`${path}/${key.replace(/~/g,'~0').replace(/\//g,'~1')}`
function resolve(graph:Graph,file:string,shape:Shape):[string,Shape]{
 if(!shape.$ref)return [file,shape]
 const [f,p='']=shape.$ref.split('#');file=f||file
 let node:unknown=graph[file]
 for(const bit of p.split('/').slice(1))node=(node as Record<string,unknown>)[bit.replace(/~1/g,'/').replace(/~0/g,'~')]
 return resolve(graph,file,node as Shape)
}
function matches(graph:Graph,file:string,s:Shape,v:unknown):boolean{
 ;[file,s]=resolve(graph,file,s)
 if(s.const!==undefined&&s.const!==v)return false
 if(s.enum&&!s.enum.includes(v))return false
 if(s.type){const type=v===null?'null':Array.isArray(v)?'array':typeof v;if(!(Array.isArray(s.type)?s.type:[s.type]).some(t=>t===type||t==='integer'&&typeof v==='number'&&Number.isSafeInteger(v)))return false}
 if(object(v)){
  if(s.required?.some(k=>!Object.hasOwn(v,k)))return false
  for(const [key,child] of Object.entries(s.properties??{}))if(Object.hasOwn(v,key)&&(child.const!==undefined||child.enum)&&!matches(graph,file,child,v[key]))return false
 }
 return true
}
/** Structural validation is bounded and follows the selected version's frozen member inventory. */
export function checkRecord(kind:RecordKind,value:unknown):void {checkRecordDepth(kind,value,0)}
function checkRecordDepth(kind:RecordKind,value:unknown,recordDepth:number):void {
 if(!Object.hasOwn(RECORD_VERSIONS,kind))return invalid()
 if(kind==='checkpoint'&&new TextEncoder().encode(JSON.stringify(value)).length>4*1024*1024)return invalid()
 const version=versionOf(kind,value)
 if(recordDepth>4)return invalid()
 const graph=(version===0||kind==='checkpoint'&&version===1?shapes.historical:shapes.current) as unknown as Graph
 let work=0
 function walk(file:string,s:Shape,v:unknown,path:string,depth:number):void{
  if(++work>200000||depth>64)return invalid()
  const ref=s.$ref
  if(ref==='checkpoint.json'&&path){checkRecordDepth('checkpoint',v,recordDepth+1);return}
  if(ref==='tool-result.json#/$defs/mcpToolResult')return // declared host/provider payload, kept whole
  ;[file,s]=resolve(graph,file,s)
  if(!matches(graph,file,s,v))return invalid()
  let branches=s.oneOf??(s.properties?undefined:s.anyOf)
  if(s.properties&&branches&&!branches.some(b=>resolve(graph,file,b)[1].type!==undefined))branches=undefined
  if(branches){const candidates=branches.filter(b=>matches(graph,file,b,v));if(!candidates.length)return invalid();for(const b of candidates){try{walk(file,b,v,path,depth+1);return}catch(e){if(!(e instanceof UnknownRecordMember))throw e}}walk(file,candidates[0]!,v,path,depth+1);return}
  const parts:[string,Shape][]=[[file,s]]
  for(const branch of s.allOf??[]){const [f,b]=resolve(graph,file,branch);if(b.properties||b.patternProperties)parts.push([f,b])}
  if(Array.isArray(v)){if(s.items)for(let i=0;i<v.length;i++)walk(file,s.items,v[i],pointer(path,String(i)),depth+1);return}
  if(!object(v))return
  const properties=new Map<string,[string,Shape]>()
  for(const [f,part] of parts)for(const [name,child] of Object.entries(part.properties??{}))properties.set(name,[f,child])
  const patterns=parts.flatMap(([f,p])=>Object.entries(p.patternProperties??{}).map(([pattern,child])=>({file:f,pattern,child})))
  const mapped=typeof s.additionalProperties==='object'?s.additionalProperties:undefined
  if(!properties.size&&!patterns.length&&!mapped&&s.additionalProperties!==false)return // schema-declared arbitrary value
  for(const name of Object.keys(v).sort()){
   const named=properties.get(name)
   if(named){walk(named[0],named[1],v[name],pointer(path,name),depth+1);continue}
   const pattern=patterns.find(p=>new RegExp(p.pattern).test(name))
   if(pattern){walk(pattern.file,pattern.child,v[name],pointer(path,name),depth+1);continue}
   if(mapped){walk(file,mapped,v[name],pointer(path,name),depth+1);continue}
   throw new UnknownRecordMember(pointer(path,name),kind)
  }
 }
 walk(files[kind],graph[files[kind]]!,value,'',0)
 if(kind==='event'&&(!object(value)||typeof value.type!=='string'||!shapes.current['run-event.json'].properties.type.enum.includes(value.type as never)))invalid()
}
/** Decode supported wire records without upgrading their bytes, version or metadata. */
export function readRunEvent(value:unknown):AssistantEvent{checkRecord('event',value);return structuredClone(value) as AssistantEvent}
export function checkRecordVersion(kind:RecordKind,value:unknown):void{versionOf(kind,value)}
export function readPolicyLayer(value:unknown):PolicyLayer{checkRecord('policy',value);return structuredClone(value) as PolicyLayer}
/** Internal unversioned event bodies become current records at the delivery boundary. */
export function writeRunEvent<T extends AssistantEvent>(event:T):T&{version:1}{return {...event,version:1}}
export function versionEvents(events:AsyncIterable<AssistantEvent>):AsyncIterable<AssistantEvent>{
 return {[Symbol.asyncIterator](){
  const source=events[Symbol.asyncIterator]()
  const stamp=(step:IteratorResult<AssistantEvent>):IteratorResult<AssistantEvent>=>step.done?step:{value:writeRunEvent(step.value),done:false}
  // Forward closing immediately: an async generator would queue it behind a pending next().
  return {
   async next(value?:unknown){return stamp(await source.next(value))},
   async return(value?:unknown){return source.return?stamp(await source.return(value)):{value,done:true as const}},
   async throw(cause?:unknown){if(source.throw)return stamp(await source.throw(cause));await source.return?.();throw cause}
  }
 }}
}
