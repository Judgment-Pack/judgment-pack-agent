import type {HostTool} from './engine'
import {authoredFailure} from './failures'
import {catalogueSkills, type DeclaredSkills, type SkillDeclaration} from './skills'
import type {ContextPolicy} from './context/policy'

/** Versioned package-owned method bytes; host replacements retain their own provenance. */
export const ASKING_WELL_PIN = "sha256:265985dc2ea1d8290d5663e7a3ed4d8e25af47d4ba2beb7194d8029791b41692"
export const ASKING_WELL_INSTRUCTIONS = "Read the available context and use available reading tools before asking the person. Ask only for necessary information that remains unavailable. Do not repeat a question already answered in this run. Ask one decision per question and state what changes depending on the answer. Offer 2 to 4 mutually exclusive options, with the recommended option first and a one-line reason grounded in the available context. Preserve free text and the person’s choice. A recommendation is never an answer: wait for the person’s actual reply. This method grants no tool or permission; existing controls still apply."
export function askingWellSkill(): SkillDeclaration {
 return {id:'asking-well',name:'asking-well',kind:'method',description:"Ask only for missing necessary information, with clear choices and a reasoned recommendation.",
  source:{kind:'custom',id:'agent-methods',trust:'first-party',version:'1'},
  instructions:ASKING_WELL_INSTRUCTIONS,tools:['ask_user'],pinnedDigest:ASKING_WELL_PIN}
}
const refuse = ():never => {throw authoredFailure('configuration','The offered ask_user requires a catalogued asking-well method with only ask_user as its dependency and a current approved digest. Nothing was sent.','context')}
/** Inspect the retained offer after phase/source projection, never the conversation flag. */
export function offersAskUser(tools:readonly HostTool[]):boolean {
 return tools.some(t=>t.name==='ask_user'&&t.interaction==='question')
}
/** The name asking-well is reserved for an explicit host method replacement when ask_user exists. */
export function selectAskingWell(declared:readonly unknown[]|undefined,tools:readonly HostTool[]):readonly unknown[]|undefined {
 if(!offersAskUser(tools))return declared
 const ask=tools.find(t=>t.name==='ask_user')!
 if(ask.taxonomy?.operation!=='ask'||ask.taxonomy.effect!=='pure')refuse()
 const replacements=(declared??[]).filter((s):s is SkillDeclaration=>typeof s==='object'&&s!==null&&'name'in s&&s.name==='asking-well')
 if(replacements.length>1)refuse()
 if(replacements.length){const s=replacements[0]!;if(s.kind!=='method'||!Array.isArray(s.tools)||s.tools.length!==1||s.tools[0]!=='ask_user')refuse();return declared}
 return [...(declared??[]),askingWellSkill()]
}
/** Fail closed at startup if normal admission or catalogue bounds withheld the selected method. */
export function requireAskingWell(declared:DeclaredSkills,tools:readonly HostTool[],bounds:Pick<ContextPolicy,'cataloguedSkills'|'skillsCatalogueBytes'>,marker:string):void {
 if(!offersAskUser(tools))return
 const selected=catalogueSkills(declared.skills,bounds,marker).catalogued.find(s=>s.name==='asking-well')
 if(!selected||selected.kind!=='method'||selected.profile.effect!=='pure'||selected.profile.operations.length!==1||selected.profile.operations[0]!=='ask')refuse()
}
