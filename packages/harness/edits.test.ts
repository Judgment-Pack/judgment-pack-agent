import {describe,it,expect} from 'vitest'
import {Drafts} from './edits'
import {ContextLedger,answer} from './context/ledger'
import {CONTEXT_DEFAULTS} from './context/policy'
import type {ExecutionCheckpoint} from './execution/state'
import type {AssistantEvent} from './engine'
const signal=new AbortController().signal
async function opened(policy={...CONTEXT_DEFAULTS},saved?:ExecutionCheckpoint){
 const ledger=new ContextLedger(policy),state=saved??({} as ExecutionCheckpoint),events:AssistantEvent[]=[],validated:string[]=[]
 if(saved)await ledger.restore(saved.context)
 const drafts=new Drafts(ledger,policy,()=>state,e=>events.push(e),()=>true,async text=>{validated.push(text);return answer({status:JSON.parse(text).specVersion==='1'?'valid':'invalid',diagnostics:['specVersion is required']})},()=> '0'.repeat(32))
 await drafts.open(saved?[]:[{id:'pack',document:{specVersion:'1',id:'pack',title:'Before',notes:'same same'}}])
 return {drafts,ledger,state,events,validated}
}
describe('proposal edits',()=>{
 it('searches and reads the current text, validates atomic edits, refuses counts and invalid schema, and undoes once',async()=>{
  const {drafts,ledger,events,validated}=await opened()
  expect(ledger.search({id:'draft:pack',pattern:'Before'}).matches[0]?.line).toBe(4)
  expect(ledger.readUnits({id:'draft:pack',offset:3,limit:1}).text).toBe('4\t  "title": "Before",')
  await drafts.edit('propose_edit',{document:'pack',old_text:'Before',new_text:'After'},signal)
  expect(drafts.proposal()?.document).toEqual({specVersion:'1',id:'pack',title:'After',notes:'same same'})
  expect(drafts.proposal()?.edits).toEqual([{type:'propose_edit',document:'pack',line:4,bytes:5,valid:true}])
  expect(validated).toHaveLength(1)
  const good=drafts.proposal()
  await expect(drafts.edit('propose_edit',{document:'pack',old_text:'same',new_text:'x'},signal)).rejects.toThrow('2 times')
  await expect(drafts.edit('propose_delete',{document:'pack',old_text:'absent'},signal)).rejects.toThrow('0 times')
  await expect(drafts.edit('propose_delete',{document:'pack',old_text:'  "specVersion": "1",\n'},signal)).rejects.toThrow('Validation failed')
  expect(drafts.proposal()).toEqual(good)
  await drafts.edit('undo_edit',{document:'pack'},signal)
  expect((drafts.proposal()?.document as {title:string}).title).toBe('Before')
  expect(drafts.proposal()?.edits).toHaveLength(2)
  await expect(drafts.edit('undo_edit',{document:'pack'},signal)).rejects.toThrow('no edit to undo')
  expect(events.filter(e=>e.type==='edit_applied')).toHaveLength(2)
  expect(events.filter(e=>e.type==='edit_refused')).toHaveLength(4)
 })
 it('restores the text, provenance and undo stack, then continues editing',async()=>{
  const first=await opened()
  await first.drafts.edit('propose_edit',{document:'pack',old_text:'Before',new_text:'After'},signal)
  const resumed=await opened({...CONTEXT_DEFAULTS},JSON.parse(JSON.stringify({...first.state,context:first.ledger.snapshot()})))
  expect(resumed.drafts.proposal()).toEqual(first.drafts.proposal())
  await resumed.drafts.edit('propose_insert',{document:'pack',after_anchor:'After',text:' again'},signal)
  expect((resumed.drafts.proposal()?.document as {title:string}).title).toBe('After again')
  await resumed.drafts.edit('undo_edit',{document:'pack'},signal)
  expect((resumed.drafts.proposal()?.document as {title:string}).title).toBe('After')
  await resumed.drafts.edit('undo_edit',{document:'pack'},signal)
  expect((resumed.drafts.proposal()?.document as {title:string}).title).toBe('Before')
  await expect(resumed.drafts.edit('undo_edit',{document:'pack'},signal)).rejects.toThrow('no edit to undo')
 })
 it('serializes competing edits and enforces UTF-8, proposal and response bounds',async()=>{
  const {drafts}=await opened({...CONTEXT_DEFAULTS,editsPerTurn:1,editBytes:5})
  await expect(drafts.edit('propose_edit',{document:'pack',old_text:'Before',new_text:'ééé'},signal)).rejects.toThrow('editBytes')
  const results=await Promise.allSettled([drafts.edit('propose_edit',{document:'pack',old_text:'Before',new_text:'After'},signal),drafts.edit('undo_edit',{document:'pack'},signal)])
  expect(results.map(r=>r.status)).toEqual(['fulfilled','rejected'])
  const other=await opened({...CONTEXT_DEFAULTS,proposalBytes:95})
  await expect(other.drafts.edit('propose_insert',{document:'pack',after_anchor:'Before',text:'x'.repeat(40)},signal)).rejects.toThrow('proposalBytes')
 })
})
