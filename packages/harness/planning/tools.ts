import {toolInputFailure} from '../failures'
import type {HostTool} from '../engine'
import {answer} from '../context/ledger'
import {canonicalJSON} from '../context/chat'
import type {Chat,ChatStore} from '../host'
import {validPlans,type ChatPlan,type CurrentPlanReference,type PlanReference} from './model'
import {resolvePlanReference,LegacyPlanReferenceUnresolved,type PlanResolver,type LegacyPlanLocator} from './references'
const asChat=(value:import('../storage/chat').ChatEntry|undefined):Chat|undefined=>{
 if(!value)return
 if(value.chat.plans!==undefined&&!validPlans(value.chat.plans))throw toolInputFailure('The saved chat plan is not valid.')
 return value.chat as Chat
}
export const currentChat=async(store:ChatStore,id:string,signal?:AbortSignal)=>asChat(await store.get(id,signal))
export async function checkPlan(plan:ChatPlan,signal:AbortSignal,resolver?:PlanResolver){
 if(!resolver)throw new LegacyPlanReferenceUnresolved()
 for(const ref of plan.references)await resolvePlanReference(ref,resolver,signal)
}
export function planTools(store:ChatStore,id:string,resolver:PlanResolver):HostTool[]{return [{name:'propose_plan',taxonomy:{operation:'propose',effect:'propose'},effect:'plan',description:'Save a reviewable plan revision in this chat. Include ordered steps, unresolved questions, and opaque artifact or dialect ids. The host pins content digests and linked objects. Saving a plan never starts work.',inputSchema:{type:'object',properties:{objective:{type:'string',maxLength:2000},steps:{type:'array',minItems:1,maxItems:20,items:{type:'string',maxLength:1000}},questions:{type:'array',maxItems:10,items:{type:'string',maxLength:1000}},references:{type:'array',maxItems:6,items:{type:'object',properties:{kind:{type:'string',enum:['artifact','dialect']},id:{type:'string',maxLength:256}},required:['kind','id'],additionalProperties:false}}},required:['objective','steps','questions','references'],additionalProperties:false},artifacts:result=>{
 const refs=(result.structuredContent as {references?:CurrentPlanReference[]}|undefined)?.references??[]
 return refs.map(ref=>({...ref,digest:'sha256:'+ref.digest,access:'read' as const}))
},execute:async(args,signal)=>{
 signal.throwIfAborted();const loaded=await store.get(id,signal),chat=asChat(loaded)
 if(!loaded||!chat||chat.interaction!=='plan')throw toolInputFailure('Enter Plan mode before proposing a plan.')
 if((chat.plans?.length??0)>=24)throw toolInputFailure('This chat has 24 plan revisions. Start a new chat for another plan.')
 if(!Array.isArray(args.references)||args.references.length>6)throw toolInputFailure('Provide at most six additional references.')
 const refs:(PlanReference|LegacyPlanLocator|Pick<CurrentPlanReference,'kind'|'id'>)[]=[]
 for(const linked of [chat.pack,chat.graph])if(linked){if('id'in linked)refs.push({kind:'artifact',id:linked.id});else refs.push({kind:'file',id:linked.path,...(legacyPin(chat,linked.path)?{revision:legacyPin(chat,linked.path)!}:{})})}
 if(chat.input&&chat.input.id!=='list')refs.push({kind:'dialect',id:chat.input.id})
 for(const legacy of chat.plans?.at(-1)?.references??[])if('revision'in legacy)refs.push(legacy)
 for(const r of args.references){if(!r||!['artifact','dialect'].includes(r.kind)||typeof r.id!=='string'||r.id.length===0||r.id.length>256)throw toolInputFailure('Invalid plan reference.');refs.push(r)}
 const references:CurrentPlanReference[]=[]
 for(const ref of refs){const found=await resolvePlanReference(ref,resolver,signal);if(!references.some(r=>r.kind===found.kind&&r.id===found.id))references.push(found)}
 const plan:ChatPlan={id:crypto.randomUUID(),revision:(chat.plans?.length??0)+1,objective:args.objective as string,steps:args.steps as string[],questions:args.questions as string[],references,status:'proposed',createdAt:new Date().toISOString()}
 const plans=[...(chat.plans??[]).map(p=>({...p,status:'superseded' as const})),plan]
 if(!validPlans(plans))throw toolInputFailure('Provide a concise objective, one to twenty steps and up to ten non-empty questions.')
 signal.throwIfAborted();await store.update(id,{plans},loaded.revision,signal)
 return answer({status:'review-required',revision:plan.revision,references,message:'Plan saved. The person must choose Start work. No planned operations have started.'})
 }}]}
function legacyPin(chat:Chat,path:string){const pin=chat.plans?.at(-1)?.references.find(ref=>ref.kind==='file'&&ref.id===path);return pin&&'revision'in pin?pin.revision:undefined}
/** User action only. No engine receives this function. Host run admission remains authoritative. */
export async function acceptPlan(store:ChatStore,chat:Chat,plan:ChatPlan,signal:AbortSignal,resolver?:PlanResolver){
 if(plan.questions.length||plan.status!=='proposed')throw toolInputFailure('Review the current plan before starting work.')
 const loaded=await store.get(chat.id,signal),now=asChat(loaded),latest=now?.plans?.at(-1)
 if(!loaded||!now||now.interaction!=='plan'||!latest||canonicalJSON(latest)!==canonicalJSON(plan))throw toolInputFailure('The plan changed. Review its current revision.')
 await checkPlan(latest,signal,resolver)
 return store.update(chat.id,{interaction:'work',plans:now.plans!.map(p=>p.id===plan.id?{...p,status:'accepted',acceptedAt:new Date().toISOString()}:p)},loaded.revision,signal)
}
