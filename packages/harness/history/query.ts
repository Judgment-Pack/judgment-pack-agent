import type {AssistantEvent} from '../engine'
import {canonicalJSON} from '../context/chat'
import {readStoredRunEvent,type DurableScope} from '../storage/writer'
import {storageId} from '../storage/common'
import {HistoryInputInvalid,historyJSONBytes,type HistoryObjectRef} from './codec'
import {readHistoryObservation} from './metadata'
import {copyHistoryEnvelope,RetainedRunHistory} from './view'
import type {ImportedRunHistory,ArchiveRepresentation} from './archive'
export interface HistoryEventQuery{afterSequence?:number;limit?:number;types?:string[];tool?:string;step?:{execution:string;revision:string;id:string};from?:string;through?:string;text?:string}
export interface HistoryEventRow{sequence:number;commitSequence:number;recordedAt:string;scope:DurableScope;event:AssistantEvent;source:HistoryObjectRef;representation?:ArchiveRepresentation}
export interface HistoryEventPage{revision:string;items:HistoryEventRow[];nextSequence:number;hasMore:boolean}
const invalid=():never=>{throw new HistoryInputInvalid()}
const time=(v:unknown)=>typeof v==='string'&&/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/.test(v)&&Number.isFinite(Date.parse(v))&&new Date(v).toISOString()===v
function query(input:HistoryEventQuery):HistoryEventQuery{
 const v=copyHistoryEnvelope(input) as HistoryEventQuery
 if(!v||typeof v!=='object'||Array.isArray(v)||Object.keys(v).some(k=>!['afterSequence','limit','types','tool','step','from','through','text'].includes(k))||v.afterSequence!==undefined&&(!Number.isSafeInteger(v.afterSequence)||v.afterSequence<0)||v.limit!==undefined&&(!Number.isSafeInteger(v.limit)||v.limit<1||v.limit>100)||v.types!==undefined&&(!Array.isArray(v.types)||v.types.length>64||!v.types.every(storageId)||new Set(v.types).size!==v.types.length)||v.tool!==undefined&&!storageId(v.tool)||v.from!==undefined&&!time(v.from)||v.through!==undefined&&!time(v.through)||v.from!==undefined&&v.through!==undefined&&v.from>v.through||v.text!==undefined&&(typeof v.text!=='string'||v.text.length>2000))return invalid()
 if(v.step!==undefined&&(!v.step||typeof v.step!=='object'||Object.keys(v.step).length!==3||!storageId(v.step.execution)||!/^sha256:[a-f0-9]{64}$/.test(v.step.revision)||!storageId(v.step.id)))return invalid()
 return v
}
function stepMatches(event:AssistantEvent,step:NonNullable<HistoryEventQuery['step']>):boolean{
 if(!('execution'in event)||event.execution!==step.execution||!('revision'in event)||event.revision!==step.revision)return false
 if(event.type==='plan_revised')return event.diff.some(change=>change.id===step.id)
 if(event.type==='plan_departed')return event.departures.some(change=>change.step===step.id)
 return event.type==='plan_completion_refused'&&event.step===step.id
}
/** Exact filters over the stored public event envelope. Store sequence owns order, even when time moves backward. */
export function queryHistoryEvents(view:RetainedRunHistory|ImportedRunHistory,input:HistoryEventQuery={}):HistoryEventPage{
 const q=query(input),head=view.head,after=q.afterSequence??0,limit=q.limit??100
 if(after>head.eventCount)return invalid()
 const matches:HistoryEventRow[]=[]
 for(const commit of view.commits)for(const [index,source] of commit.events.entries()){
  const sequence=commit.eventStart+index;if(sequence<=after)continue
  if(q.from!==undefined&&commit.recordedAt<q.from||q.through!==undefined&&commit.recordedAt>q.through)continue
  const display='readEventDisplay'in view?view.readEventDisplay(source):undefined
  const {scope,event}=display?{scope:display.scope,event:display.event as unknown as AssistantEvent}:readStoredRunEvent((view as RetainedRunHistory).readObject(source))
  if(q.types&&!q.types.includes(event.type)||q.tool!==undefined&&(!('name'in event)||event.name!==q.tool)&&(!('tool'in event)||event.tool!==q.tool)||q.step&&!stepMatches(event,q.step)||q.text!==undefined&&!canonicalJSON(event).includes(q.text))continue
  matches.push({sequence,commitSequence:commit.sequence,recordedAt:commit.recordedAt,scope,event,source,...(display?{representation:display.representation}:{})})
 }
 const items:HistoryEventRow[]=[];let bytes=0
 for(const row of matches.slice(0,limit)){const length=historyJSONBytes(row).length;if(bytes+length>8*1024*1024)break;bytes+=length;items.push(row)}
 return {revision:head.revision,items,nextSequence:items.at(-1)?.sequence??after,hasMore:items.length<matches.length}
}
/** Job/artifact/dialect relationships exist only when explicitly recorded by the bound host. */
export function findHistoryRelations(view:RetainedRunHistory|ImportedRunHistory,kind:'job'|'artifact'|'dialect',id:string){
 if(!['job','artifact','dialect'].includes(kind)||!storageId(id))return invalid()
 const rows:{commitSequence:number;recordedAt:string;source:HistoryObjectRef;observation:Extract<ReturnType<typeof readHistoryObservation>,{kind:'relation'}>}[]=[]
 for(const commit of view.commits)for(const source of commit.observations){const observation=readHistoryObservation('readDisplay'in view?historyJSONBytes(view.readDisplay(source,'observation').document):view.readObject(source));if(observation.kind==='relation'&&observation.target.kind===kind&&observation.target.id===id)rows.push({commitSequence:commit.sequence,recordedAt:commit.recordedAt,source,observation})}
 return rows.length?{kind:'observed' as const,rows}:{kind:'unavailable' as const,rows:[]}
}
