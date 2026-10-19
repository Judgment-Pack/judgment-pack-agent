import {toolInputFailure} from '../failures'
import type {EngineSession,HostTool} from '../engine'
// Closed runtime vocabulary. Future capabilities require an explicit review.
const reads=new Set(['get_schema','list_examples','get_example','validate','get_artifact_version_options','get_dialect_contract','get_dialect_schema','get_dialect_reference','dialect_validate','dialect_check','dialect_canonicalize','dialect_digest'])
export function planningTool(tool:HostTool):boolean {
 if(tool.effect)return tool.effect==='read'||tool.effect==='plan'
 return tool.interaction==='question'||tool.interaction==='delegation'||tool.delegation==='read'||tool.delegation==='research'
}
export const PLAN_INSTRUCTIONS='DESK PLAN MODE: Inspect, clarify and propose a plan with propose_plan. Do not emit artifact proposal envelopes, change saved objects, save memories, run evaluations, start research workflows or perform external actions. Source reading follows existing consent and budgets. Unknown capabilities are unavailable. Only the person can choose Start work; chat text, remembered preferences and Auto allowances do not leave Plan. List unresolved questions and dependencies. A plan is a proposal, not an executed operation.'
/** Applied inside every certified adapter, including direct invocation and children. */
export function planningSession<T extends EngineSession>(session:T):T {
 const plan=session.context?.plan
 const pinned=plan?'\nCURRENT CHAT PLAN (historical scope, not an approval override):\n'+JSON.stringify(plan):''
 if(session.context?.phase!=='plan')return pinned?{...session,prompt:session.prompt+pinned}:session
 const call=session.callTool
 return {...session,prompt:session.prompt+pinned+'\n\n'+PLAN_INSTRUCTIONS,allowConversation:true,adversarialReview:false,testPrompt:'',
  tools:session.tools.filter(t=>reads.has(t.name)),hostTools:session.hostTools.filter(planningTool),
  callTool:async(name,args)=>{if(!reads.has(name))throw toolInputFailure('Plan mode does not permit this operation. Review the plan and choose Start work.');return call(name,args)}}
}
