import {afterEach,describe,expect,it,vi} from 'vitest'
import {vercel} from '../engines/vercel'
import {deepagents} from '../engines/deepagents'
import {codex} from '../engines/codex'
import {agentRunFrom,answerFrom,runOnAgentEngine,runOnModelEngine,type Turn,type Ran} from '../testing/scenarioDriver'
import {runtimeAnswer} from '../testing/scripted'
import {PROPOSE_PACK_SCHEMA} from '../engines/contract'
import {REFUTATION_MARKER} from '../refutation'
import type {ContextInput} from '../context/session'
import type {Engine} from '../engine'
afterEach(()=>vi.unstubAllGlobals())
import sequence from '../../../contract/vectors/edits/sequence.json'
import resume from '../../../contract/vectors/edits/resume.json'
import expectedProposal from '../../../contract/vectors/edits/proposal.json'
import undo from '../../../contract/vectors/edits/undo.json'
import insert from '../../../contract/vectors/edits/propose_insert-provenance.json'
import deletion from '../../../contract/vectors/edits/propose_delete-provenance.json'
import deleteInsert from '../../../contract/vectors/edits/delete-insert.json'
const turns:Turn[]=sequence.items.map(call=>({call:{tool:call.name,arguments:call.arguments}}))
const document=sequence.items[0]!.arguments.document as Record<string,unknown>
const edit=sequence.items[3]!.arguments

const saved=(r:Ran)=>r.events.filter(e=>e.type==='execution_checkpoint').at(-1)!.checkpoint
async function play(engine:Engine,turns:Turn[],context:ContextInput={},critic=false,ending?:'failure'|'waiting'){
 const session={prompt:'Revise the draft.',testPrompt:'Review the entire proposal.',interactive:true,allowConversation:true,critic:critic?'on' as const:'off' as const,context:{currentRequest:{message:'Revise the draft.',input:'Revise the draft.'},...context}}
 const review:Turn[]=[{call:{tool:'propose_edit',arguments:edit}},{say:'Reviewed the whole proposal.'}]
 const options={engine,session,hostTools:[{name:'ask_user',description:'Ask the person.',interaction:'question' as const,inputSchema:{type:'object'},execute:async()=>runtimeAnswer({})},{name:'propose_pack',description:'Propose a pack.',effect:'proposal' as const,inputSchema:PROPOSE_PACK_SCHEMA,execute:async()=>{throw Error('host proposal executed')}}],handle:(call:{name:string;args:Record<string,unknown>})=>runtimeAnswer({status:call.name==='validate'&&JSON.parse(String(call.args.content)).specVersion==='1'?'valid':'invalid'})}
 if(engine.id==='codex')return runOnAgentEngine(async(r,c)=>{await agentRunFrom(r.phase==='critic'?review:turns)(r,c);if(ending==='failure')throw Error('Interrupted after edit')},options)
 const author=answerFrom(turns,'whole'),reviewer=answerFrom(review,'whole')
 return runOnModelEngine(r=>JSON.stringify(r.body).includes(REFUTATION_MARKER)?reviewer(r):author(r),options)
}
for(const engine of [vercel,deepagents,codex])describe(`${engine.id==='codex'?'agent ':''}engine ${engine.id} · scenarios`,()=>{
 for(const ending of ['failure','waiting'] as const)it(`15 · complete draft set on ${ending}`,async()=>{
  const question={title:'Continue',question:'What next?',selection:'single',options:[],recommendedOptionId:null,recommendation:null}
  const steps:Turn[]=[{call:{tool:'propose_edit',arguments:edit}}]
  if(ending==='waiting')steps.push({call:{tool:'ask_user',arguments:question}})
  const ran=await play(engine,steps,{acceptedDocuments:[{id:'pack',document},{id:'second',document:{...document,id:'second',title:'Second'}}]},false,ending)
  const proposals=ran.events.filter(e=>e.type==='proposal')
  expect(proposals.map(p=>p.document)).toEqual([{...document,title:'After'},{...document,id:'second',title:'Second'}])
  expect(proposals[0]?.edits).toEqual(expectedProposal.items[0]!.edits)
  expect(proposals[1]?.edits).toEqual([])
  if(ending==='waiting')expect(ran.events.some(e=>e.type==='user_question')).toBe(true)
  else expect(proposals.every(p=>p.note?.kind==='turn-unfinished')).toBe(true)
 })
 it('15 · successful deletion and insertion carry shared provenance',async()=>{
  const ran=await play(engine,[...deleteInsert.items.map(call=>({call:{tool:call.name,arguments:call.arguments}})),{say:'Edited.'}],{acceptedDocuments:[{id:'pack',document}]})
  expect(ran.events.find(e=>e.type==='proposal')).toMatchObject({document:{...document,title:'After!'},edits:[...deletion.items,...insert.items]})
 })
 it('15 · proposal edits: search, read, validate, refuse, undo, resume and stable prefix',async()=>{
  const accepted=await play(engine,[{call:{tool:'propose_edit',arguments:edit}},{say:'Two documents.'}],{acceptedDocuments:[{id:'pack',document},{id:'second',document:{...document,id:'second',title:'Second'}}]})
  expect(accepted.events.filter(e=>e.type==='proposal').map(e=>e.document)).toEqual([{...document,title:'After'},{...document,id:'second',title:'Second'}])
  const acceptedPrefix=accepted.runs[0]?.instructions??accepted.requests[0]?.body.messages?.find(m=>m.role==='system')?.content
  expect(JSON.stringify(acceptedPrefix)).not.toContain('PROPOSAL DRAFTS')
  const ran=await play(engine,[...turns,{say:'The revised draft is ready.'}])
  expect(ran.events.filter(e=>e.type==='error')).toEqual([])
  expect(ran.events.find(e=>e.type==='tool_result'&&e.name==='search')).toMatchObject({isError:false,text:expect.stringContaining('draft:pack')})
  expect(ran.events.find(e=>e.type==='tool_result'&&e.name==='read_window')).toMatchObject({isError:false,text:expect.stringContaining('Before')})
  expect(ran.events.filter(e=>e.type==='tool_result'&&e.name==='read_window').at(-1)).toMatchObject({isError:false,text:expect.stringContaining('After')})
  const proposal=ran.events.find(e=>e.type==='proposal')!
  expect(proposal).toMatchObject({...expectedProposal.items[0],edits:[...expectedProposal.items[0]!.edits,...insert.items,...undo.items]})
  expect(ran.events.filter(e=>e.type==='edit_refused').map(e=>e.reason)).toEqual([expect.stringContaining('2 times'),expect.stringContaining('Validation failed')])
  expect(ran.seen.filter(c=>c.name==='validate')).toHaveLength(4)
  const prefixes=ran.runs.length?ran.runs.map(r=>r.instructions):ran.requests.map(r=>r.body.messages?.find(m=>m.role==='system')?.content)
  expect(new Set(prefixes).size).toBe(1)
  expect(JSON.stringify(prefixes)).not.toContain('PROPOSAL DRAFTS')
  const continued=await play(engine,[...resume.items.map(call=>({call:{tool:call.name,arguments:call.arguments}})),{say:'Continued.'}],{execution:JSON.parse(JSON.stringify(saved(ran)))})
  expect(continued.events.filter(e=>e.type==='error')).toEqual([])
  expect(continued.events.find(e=>e.type==='proposal')).toMatchObject({document:{title:'Continued'}})
  expect(saved(continued).draft?.documents[0]?.edits).toHaveLength(4)
  const child=await play(engine,[{call:{tool:'propose_edit',arguments:edit}},{say:'Data only.'}],{objective:'Read the draft.'})
  expect(child.events).toContainEqual(expect.objectContaining({type:'guardrail',tool:'propose_edit',reason:'edit refused'}))
  const review=await play(engine,[{call:{tool:'propose_pack',arguments:{document}}},{say:'Review this.'}],{},true)
  expect(review.events).toContainEqual(expect.objectContaining({type:'guardrail',tool:'propose_edit',reason:'edit refused'}))
  expect(review.events.filter(e=>e.type==='edit_applied')).toEqual([])
 })
})
