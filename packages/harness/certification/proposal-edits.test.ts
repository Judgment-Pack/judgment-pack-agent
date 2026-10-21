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
import fixture from './scenarios/proposal-edits.json'
const {document,edit}=fixture
const saved=(r:Ran)=>r.events.filter(e=>e.type==='execution_checkpoint').at(-1)!.checkpoint
async function play(engine:Engine,turns:Turn[],context:ContextInput={},critic=false){
 const session={prompt:'Revise the draft.',testPrompt:'Review the entire proposal.',interactive:true,allowConversation:true,critic:critic?'on' as const:'off' as const,context:{currentRequest:{message:'Revise the draft.',input:'Revise the draft.'},...context}}
 const review:Turn[]=[{call:{tool:'propose_edit',arguments:edit}},{say:'Reviewed the whole proposal.'}]
 const options={engine,session,hostTools:[{name:'propose_pack',description:'Propose a pack.',effect:'proposal' as const,inputSchema:PROPOSE_PACK_SCHEMA,execute:async()=>{throw Error('host proposal executed')}}],handle:(call:{name:string;args:Record<string,unknown>})=>runtimeAnswer({status:call.name==='validate'&&JSON.parse(String(call.args.content)).specVersion==='1'?'valid':'invalid'})}
 if(engine.id==='codex')return runOnAgentEngine(async(r,c)=>agentRunFrom(r.phase==='critic'?review:turns)(r,c),options)
 const author=answerFrom(turns,'whole'),reviewer=answerFrom(review,'whole')
 return runOnModelEngine(r=>JSON.stringify(r.body).includes(REFUTATION_MARKER)?reviewer(r):author(r),options)
}
for(const engine of [vercel,deepagents,codex])describe(`${engine.id==='codex'?'agent ':''}engine ${engine.id} · scenarios`,()=>{
 it('15 · proposal edits: search, read, validate, refuse, undo, resume and stable prefix',async()=>{
  const accepted=await play(engine,[{call:{tool:'propose_edit',arguments:edit}},{say:'Two documents.'}],{acceptedDocuments:[{id:'pack',document},{id:'second',document:{...document,id:'second',title:'Second'}}]})
  expect(accepted.events.filter(e=>e.type==='proposal').map(e=>e.document)).toEqual([{...document,title:'After'},{...document,id:'second',title:'Second'}])
  const acceptedPrefix=accepted.runs[0]?.instructions??accepted.requests[0]?.body.messages?.find(m=>m.role==='system')?.content
  expect(JSON.stringify(acceptedPrefix)).not.toContain('PROPOSAL DRAFTS')
  const ran=await play(engine,[
   {call:{tool:'propose_pack',arguments:{document}}},
   {call:{tool:'search',arguments:{id:'draft:pack',pattern:'Before'}}},
   {call:{tool:'read_window',arguments:{id:'draft:pack',offset:3,limit:1}}},
   {call:{tool:'propose_edit',arguments:edit}},
   {call:{tool:'read_window',arguments:{id:'draft:pack',offset:3,limit:1}}},
   {call:{tool:'propose_edit',arguments:{document:'pack',old_text:'same',new_text:'x'}}},
   {call:{tool:'propose_delete',arguments:{document:'pack',old_text:'  "specVersion": "1",\n'}}},
   {call:{tool:'propose_insert',arguments:{document:'pack',after_anchor:'After',text:' again'}}},
   {call:{tool:'undo_edit',arguments:{document:'pack'}}},
   {say:'The revised draft is ready.'}])
  expect(ran.events.filter(e=>e.type==='error')).toEqual([])
  expect(ran.events.find(e=>e.type==='tool_result'&&e.name==='search')).toMatchObject({isError:false,text:expect.stringContaining('draft:pack')})
  expect(ran.events.find(e=>e.type==='tool_result'&&e.name==='read_window')).toMatchObject({isError:false,text:expect.stringContaining('Before')})
  expect(ran.events.filter(e=>e.type==='tool_result'&&e.name==='read_window').at(-1)).toMatchObject({isError:false,text:expect.stringContaining('After')})
  const proposal=ran.events.find(e=>e.type==='proposal')!
  expect(proposal).toMatchObject({document:{...document,title:'After'},edits:[{type:'propose_edit',line:4,bytes:5,valid:true},{type:'propose_insert',line:4,bytes:6,valid:true},{type:'undo_edit',line:4,bytes:0,valid:true}]})
  expect(ran.events.filter(e=>e.type==='edit_refused').map(e=>e.reason)).toEqual([expect.stringContaining('2 times'),expect.stringContaining('Validation failed')])
  expect(ran.seen.filter(c=>c.name==='validate')).toHaveLength(4)
  const prefixes=ran.runs.length?ran.runs.map(r=>r.instructions):ran.requests.map(r=>r.body.messages?.find(m=>m.role==='system')?.content)
  expect(new Set(prefixes).size).toBe(1)
  expect(JSON.stringify(prefixes)).not.toContain('PROPOSAL DRAFTS')
  const continued=await play(engine,[{call:{tool:'propose_edit',arguments:{document:'pack',old_text:'After',new_text:'Continued'}}},{say:'Continued.'}],{execution:JSON.parse(JSON.stringify(saved(ran)))})
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
