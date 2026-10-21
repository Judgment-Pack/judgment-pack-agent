/** Host-declared, pinned, reads-only children (ADR-0013). Model text grants nothing. */
import {canonicalJSON} from './context/chat'
import {contextDigest} from './context/ledger'
import {prefix,utf8Bytes,type ContextPolicy} from './context/policy'
import {frameBlock} from './framing'
import {authoredFailure} from './failures'
import {sourceProblem,type SkillDeclaration} from './skills'
import type {HostTool} from './engine'
import type {SourceRecord} from './toolSources'
import {TASK_PROFILES,type TaskProfile} from './delegationContract'

// Mandatory report fields, the longest confidence, and 16 JSON bytes of summary (quotes included).
// Shared by admission and fitting; carried by subagents/result-minimum.json.
export const SUBAGENT_RESULT_MIN_BYTES = utf8Bytes(JSON.stringify({summary:'No findings...',findings:[],citations:[],openQuestions:[],confidence:'unstated',notDone:[]}))
export const SUBAGENT_CHANGED = 'subagent changed: The declared sub-agent was withdrawn or changed; it was not dispatched again.'
export const SUBAGENT_DEFAULTS = Object.freeze({cataloguedSubagents:16,subagentCatalogueBytes:4000})
export interface SubagentBounds {modelRequests:number;agentRuns:number;toolCalls:number;bytes:number;milliseconds:number;resultBytes:number}
export interface SubagentDeclaration {
  id:string;name:string;purpose:string;instructions:string;source:SkillDeclaration['source']
  tools:string[];skills:string[];model:string;reasoning:string;bounds:SubagentBounds;pinnedDigest:string
}
export interface SubagentRecord {id:string;digest:string;source:SourceRecord}
export interface Subagent extends SubagentDeclaration {digest:string}
export interface SubagentNotice {tool:string;detail:string;reason:'subagent withheld'}
const object=(v:unknown):v is Record<string,unknown>=>!!v&&typeof v==='object'&&!Array.isArray(v)
const id=/^[A-Za-z][A-Za-z0-9-]{0,23}$/
const text=(v:unknown,n:number)=>typeof v==='string'&&v.length>0&&v.length<=n
const members=['id','name','purpose','instructions','source','tools','skills','model','reasoning','bounds','pinnedDigest']
export async function subagentDigest(definition:SubagentDeclaration):Promise<string>{
  const bundle=Object.fromEntries(Object.entries(definition).filter(([key])=>key!=='pinnedDigest'&&key!=='digest'))
  return `sha256:${await contextDigest(canonicalJSON(bundle))}`
}
export function subagentRecord(definition:Subagent):SubagentRecord {
  return {id:definition.id,digest:definition.digest,source:structuredClone(definition.source) as SourceRecord}
}
export function subagentProfile(definition:Subagent):TaskProfile {
  return {id:definition.id,description:definition.purpose,instructions:'Complete the assigned read-only task.',capabilities:[],model:'child',report:'analyze',budget:{modelRequests:definition.bounds.modelRequests,toolCalls:definition.bounds.toolCalls,milliseconds:definition.bounds.milliseconds}}
}
/** All ceilings are host policy, never task arguments; profile ceilings remain in force too. */
export function subagentCeilings(policy:ContextPolicy):SubagentBounds {
  return {modelRequests:10,agentRuns:5,toolCalls:12,bytes:policy.maxInputBytes,milliseconds:Math.min(360000,policy.turnMilliseconds),resultBytes:policy.toolResultBytes}
}
export async function declareSubagents(declared:readonly unknown[]|undefined,tools:readonly HostTool[],skills:readonly Pick<SkillDeclaration,'id'>[],policy:ContextPolicy):Promise<{subagents:Subagent[];withheld:SubagentNotice[]}> {
  const subagents:Subagent[]=[],withheld:SubagentNotice[]=[]
  const ids=new Set<string>(),ceilings=subagentCeilings(policy)
  for(const raw of declared??[]){
    if(!object(raw)||Object.keys(raw).some(k=>!members.includes(k))||members.some(k=>!(k in raw))||!text(raw.id,24)||!id.test(String(raw.id))||!text(raw.name,64)||!text(raw.purpose,1024)||!text(raw.instructions,4000)||sourceProblem(raw.source)||!text(raw.model,200)||!text(raw.reasoning,16)||!Array.isArray(raw.tools)||raw.tools.length>64||raw.tools.some(x=>!text(x,128))||new Set(raw.tools).size!==raw.tools.length||!Array.isArray(raw.skills)||raw.skills.length>32||raw.skills.some(x=>typeof x!=='string'||!id.test(x))||new Set(raw.skills).size!==raw.skills.length||!object(raw.bounds)||Object.keys(raw.bounds).some(k=>!(k in ceilings))||Object.keys(ceilings).some(k=>!Number.isSafeInteger((raw.bounds as Record<string,unknown>)[k])||Number((raw.bounds as Record<string,unknown>)[k])<1)||typeof raw.pinnedDigest!=='string'||!/^sha256:[0-9a-f]{64}$/.test(raw.pinnedDigest))throw authoredFailure('configuration','The sub-agent declaration is not a closed subagent.json definition. Nothing was sent.','context')
    const definition=raw as unknown as SubagentDeclaration
    if(ids.has(definition.id))throw authoredFailure('configuration',`Duplicate sub-agent id ${definition.id}. Nothing was sent.`,'context')
    ids.add(definition.id)
    const hold=(detail:string)=>withheld.push({tool:definition.id,detail,reason:'subagent withheld'})
    if(TASK_PROFILES.some(p=>p.id===definition.id)){hold('A declaration cannot replace a built-in profile.');continue}
    const unknown=definition.tools.find(name=>!tools.some(t=>t.name===name))
    if(unknown!==undefined){hold(`Undeclared tool ${unknown}; the definition was withheld.`);continue}
    if(definition.tools.some(name=>!tools.some(t=>t.name===name&&!t.interaction&&(t.source?t.effectClass==='read':t.effect==='read'||!t.effect&&t.delegation==='read')))){hold('A sub-agent admits declared reads only; effects cannot be raised.');continue}
    if(definition.skills.some(name=>!skills.some(s=>s.id===name))){hold('An undeclared skill was named; the definition was withheld.');continue}
    if(Object.entries(definition.bounds).some(([key,value])=>value>ceilings[key as keyof SubagentBounds])){hold('The definition exceeds the turn or child bounds ceiling.');continue}
    if(definition.bounds.resultBytes<SUBAGENT_RESULT_MIN_BYTES){hold(`resultBytes ${definition.bounds.resultBytes} is below the report minimum ${SUBAGENT_RESULT_MIN_BYTES}.`);continue}
    if(utf8Bytes(definition.instructions)>policy.projectInstructionsBytes){hold('The definition instructions exceed projectInstructionsBytes.');continue}
    const digest=await subagentDigest(definition)
    if(digest!==definition.pinnedDigest){hold('The definition digest changed; re-approval is required.');continue}
    subagents.push({...structuredClone(definition),digest})
  }
  return {subagents,withheld}
}
/** Purpose lines are untrusted data, bounded before framing, beside the fixed built-ins. */
export function catalogueSubagents(definitions:readonly Subagent[],bounds:Pick<ContextPolicy,'cataloguedSubagents'|'subagentCatalogueBytes'>,marker:string){
  const subagents:Subagent[]=[],entries:string[]=[],withheld:SubagentNotice[]=[];let bytes=0
  for(const definition of definitions){
    const purpose=prefix(definition.purpose.split('\n')[0]!.trim(),160)
    const size=utf8Bytes(JSON.stringify({id:definition.id,purpose}))
    if(subagents.length>=bounds.cataloguedSubagents||bytes+size>bounds.subagentCatalogueBytes){withheld.push({tool:definition.id,detail:'The sub-agent catalogue count or byte bound was reached.',reason:'subagent withheld'});continue}
    bytes+=size;subagents.push(definition)
    entries.push(`${definition.id}\n${frameBlock(`the purpose of sub-agent ${JSON.stringify(definition.id)}`,purpose,marker)}`)
  }
  return {subagents,withheld,section:entries.length?'\n\nDECLARED SUB-AGENTS (reads only; select by profile id)\n'+entries.join('\n')+'\nEND DECLARED SUB-AGENTS':''}
}

/** Extend only the selectable ids; built-in descriptions and policies keep their bytes. */
export function subagentOffer(schema:unknown,definitions:readonly Subagent[]):unknown {
  const copy=structuredClone(schema) as {properties:{tasks:{items:{properties:{profile:{enum:string[]}}}}}}
  copy.properties.tasks.items.properties.profile.enum.push(...definitions.map(s=>s.id))
  return copy
}
export function recordSubagents(request:import('./delegationContract').DelegationRequest,definitions:readonly Subagent[]):void {
  for(const task of request.tasks){const definition=definitions.find(s=>s.id===task.profile);if(definition)task.subagent=subagentRecord(definition)}
}

export function validSubagentRecord(value:unknown):value is SubagentRecord {
 return object(value)&&Object.keys(value).length===3&&typeof value.id==='string'&&id.test(value.id)&&typeof value.digest==='string'&&/^sha256:[0-9a-f]{64}$/.test(value.digest)&&sourceProblem(value.source)===''
}
