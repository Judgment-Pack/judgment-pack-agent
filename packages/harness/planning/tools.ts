import {toolInputFailure} from '../failures'
import type {HostTool} from '../engine'
import {answer} from '../context/ledger'
import type {Chat,ChatStore} from '../host'
import {readFile} from '../host'
import {dialectAPI,type PreparationReply} from '../host'
import {validPlans,type ChatPlan,type PlanReference} from './model'
export const currentChat=(store:ChatStore,id:string)=>{const s=store.getSnapshot();return [...s.chats,...s.drafts].find(c=>c.id===id)}
async function revision(ref:Pick<PlanReference,'kind'|'id'>,signal:AbortSignal){
 signal.throwIfAborted()
 const sha=ref.kind==='file'?(await readFile(ref.id,signal)).sha256:(await dialectAPI<PreparationReply>('/'+ref.id)).sha256
 signal.throwIfAborted();return sha
}
export async function checkPlan(plan:ChatPlan,signal:AbortSignal){for(const ref of plan.references)if(await revision(ref,signal)!==ref.revision)throw toolInputFailure('A referenced object changed. Revise and review the plan before starting work.')}
export function planTools(store:ChatStore,id:string):HostTool[]{return [{name:'propose_plan',effect:'plan',description:'Save a reviewable plan revision in this chat. Include ordered concrete steps and unresolved questions. Name any additional saved file paths or dialect IDs the plan depends on; Desk pins their current revisions and automatically includes the linked object. This only saves the plan, never starts work.',inputSchema:{type:'object',properties:{objective:{type:'string',maxLength:2000},steps:{type:'array',minItems:1,maxItems:20,items:{type:'string',maxLength:1000}},questions:{type:'array',maxItems:10,items:{type:'string',maxLength:1000}},references:{type:'array',maxItems:6,items:{type:'object',properties:{kind:{type:'string',enum:['file','dialect']},id:{type:'string',maxLength:1024}},required:['kind','id'],additionalProperties:false}}},required:['objective','steps','questions','references'],additionalProperties:false},execute:async(args,signal)=>{
 signal.throwIfAborted();const chat=currentChat(store,id)
 if(!chat||chat.interaction!=='plan')throw toolInputFailure('Enter Plan mode before proposing a plan.')
 if((chat.plans?.length??0)>=24)throw toolInputFailure('This chat has 24 plan revisions. Start a new chat for another plan.')
 if(!Array.isArray(args.references)||args.references.length>6)throw toolInputFailure('Provide at most six additional references.')
 const refs:Pick<PlanReference,'kind'|'id'>[]=[...(chat.pack?[{kind:'file' as const,id:chat.pack.path}]:[]),...(chat.graph?[{kind:'file' as const,id:chat.graph.path}]:[]),...(chat.input&&chat.input.id!=='list'?[{kind:'dialect' as const,id:chat.input.id}]:[])]
 for(const r of args.references){if(!r||!['file','dialect'].includes(r.kind)||typeof r.id!=='string'||r.id.length>1024)throw toolInputFailure('Invalid plan reference.');refs.push(r)}
 const references:PlanReference[]=[]
 for(const r of refs){if(references.some(x=>x.kind===r.kind&&x.id===r.id))continue;references.push({...r,revision:await revision(r,signal)})}
 const plan:ChatPlan={id:crypto.randomUUID(),revision:(chat.plans?.length??0)+1,objective:args.objective as string,steps:args.steps as string[],questions:args.questions as string[],references,status:'proposed',createdAt:new Date().toISOString()}
 const plans=[...(chat.plans??[]).map(p=>({...p,status:'superseded' as const})),plan]
 if(!validPlans(plans))throw toolInputFailure('Provide a concise objective, one to twenty steps and up to ten non-empty questions.')
 signal.throwIfAborted();if(currentChat(store,id)?.plans!==chat.plans||currentChat(store,id)?.interaction!=='plan')throw toolInputFailure('The plan changed. Read the current chat before proposing another revision.')
 store.update(id,{plans});await store.checkpoint(signal)
 return answer({status:'review-required',revision:plan.revision,message:'Plan saved in this chat. The person must choose Start work to execute it. No planned operations have started.'})
 }}]}
/** User action only. No engine receives this function. */
export async function acceptPlan(store:ChatStore,chat:Chat,plan:ChatPlan,signal:AbortSignal){
 if(store.running||plan.questions.length||currentChat(store,chat.id)?.plans?.at(-1)?.id!==plan.id||plan.status!=='proposed')throw toolInputFailure('Review the current plan before starting work.')
 await checkPlan(plan,signal)
 const now=currentChat(store,chat.id)
 if(store.running||!now||now.plans?.at(-1)?.id!==plan.id||now.plans.at(-1)?.status!=='proposed')throw toolInputFailure('The plan changed. Review its current revision.')
 store.update(chat.id,{interaction:'work',plans:now.plans.map(p=>p.id===plan.id?{...p,status:'accepted',acceptedAt:new Date().toISOString()}:p)})
 try{await store.checkpoint(signal)}catch(error){store.update(chat.id,{interaction:chat.interaction,plans:now.plans});throw error}
}
