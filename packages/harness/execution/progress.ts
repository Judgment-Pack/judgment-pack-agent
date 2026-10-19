import {readFailure} from '../failures'
import type {Operation} from './state'

/** Compare completed work without interpreting its topic, tools or result meaning. */
const canonical=(value:unknown):string=>JSON.stringify(value,(_key,member)=>member&&typeof member==='object'&&!Array.isArray(member)?Object.fromEntries(Object.entries(member).sort(([a],[b])=>a<b?-1:a>b?1:0)):member)
export const REPEATED_WORK_LIMIT=3
export const RECOVER_REPEATED_WORK_INSTRUCTIONS='Several consecutive operations repeated completed work and returned unchanged results. This is one recovery request: choose a different concrete action using the retained working arguments and results, or finish with the exact remaining blocker. Repeating a completed call with the same arguments will be refused without executing it. A saved workspace read is not the unsaved working proposal. Do not restart discovery or request more allowance merely to repeat work. All existing permissions and approval requirements still apply.'
export const REPEATED_WORK_INSTRUCTIONS='Several consecutive operations repeated the same arguments and returned unchanged results. Further tool calls are unavailable for this response. Use the retained results to finish the current request or state the concrete remaining blocker. Do not claim a saved change, successful tests or task completion without the corresponding results. Do not request more budget merely to repeat these operations.'
export class ExecutionProgressTracker {
 private seen=new Set<string>()
 private repeated=0
 private requests=new Set<string>()
 private recovered=false
 private step:'work'|'recover'|'finish'='work'
 beginPhase(){this.seen.clear();this.requests.clear();this.repeated=0;this.recovered=false;this.step='work'}
 nextStep(remaining:number){
  this.step=!this.stalled?'work':!this.recovered&&remaining>1?'recover':'finish'
  if(this.step==='recover')this.recovered=true
  return this.step
 }
 rejects(name:string,args:unknown){return this.step==='recover'&&this.requests.has(canonical({name,args}))}
 seed(operations:Operation[]){this.beginPhase();for(const operation of operations)this.observe(operation);this.repeated=0}
 observe(operation:Operation){
  if(operation.status==='running'||operation.status==='interrupted'||!operation.result){this.repeated=0;return}
  // A host refusal is not new evidence or completed work. Keep the stalled
  // state so one refused recovery call cannot open another discovery loop.
  const failure=readFailure((operation.result.structuredContent as {failure?:unknown}|undefined)?.failure)
  if(operation.status==='failed'&&failure?.code==='repeated-work'&&failure.source==='desk'&&failure.outcome==='not-started')return
  this.requests.add(canonical({name:operation.name,args:operation.args}))
  const key=canonical({name:operation.name,args:operation.args,result:operation.result})
  this.repeated=this.seen.has(key)?this.repeated+1:0
  this.seen.add(key)
 }
 get stalled(){return this.repeated>=REPEATED_WORK_LIMIT}
 get guidance(){return this.repeated>=2?'Repeated unchanged results detected. Reuse the completed operations; make a materially different next step or finish the response. Avoid rereading or revalidating the same data.':''}
}
