export interface CurrentPlanReference {kind:'artifact'|'dialect';id:string;digest:string}
/** Historical only. Current writers require a host mapping and write opaque digest references. */
export interface LegacyPlanReference {kind:'file'|'dialect';id:string;revision:string}
export type PlanReference=CurrentPlanReference|LegacyPlanReference
export interface ChatPlan {id:string;revision:number;objective:string;steps:string[];questions:string[];references:PlanReference[];status:'proposed'|'accepted'|'started'|'superseded';createdAt:string;acceptedAt?:string}
const text=(v:unknown,n:number):v is string=>typeof v==='string'&&v.trim().length>0&&v.length<=n
export function validPlans(value:unknown):value is ChatPlan[]{
 if(!Array.isArray(value)||value.length>24)return false
 return value.every((p,i)=>p&&typeof p==='object'&&text(p.id,80)&&Number.isSafeInteger(p.revision)&&p.revision===i+1&&text(p.objective,2000)
  &&Array.isArray(p.steps)&&p.steps.length>0&&p.steps.length<=20&&p.steps.every((s:unknown)=>text(s,1000))
  &&Array.isArray(p.questions)&&p.questions.length<=10&&p.questions.every((s:unknown)=>text(s,1000))
  &&Array.isArray(p.references)&&p.references.length<=8&&p.references.every((r:PlanReference)=>r&&('digest'in r?['artifact','dialect'].includes(r.kind)&&text(r.id,256)&&/^[a-f0-9]{64}$/.test(r.digest):['file','dialect'].includes(r.kind)&&text(r.id,1024)&&/^[a-f0-9]{64}$/.test(r.revision)))
  &&['proposed','accepted','started','superseded'].includes(p.status)&&Number.isFinite(Date.parse(p.createdAt))
  &&(i===value.length-1||p.status==='superseded')&&(p.acceptedAt===undefined||Number.isFinite(Date.parse(p.acceptedAt)))
  &&(!['accepted','started'].includes(p.status)||typeof p.acceptedAt==='string'))&&new Set(value.map(p=>p.id)).size===value.length
}
