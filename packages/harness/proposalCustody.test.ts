import {describe,it,expect} from 'vitest'
import {Drafts} from './edits'
import {readProposedPack} from './engines/contract'
import {ContextLedger,answer,contextDigest} from './context/ledger'
import {CONTEXT_DEFAULTS} from './context/policy'
import {OutputGuard,OutputRefused} from './outputGuard'
import type {ExecutionCheckpoint} from './execution/state'
import type {AssistantEvent} from './engine'
const signal=new AbortController().signal
async function opened(saved?:ExecutionCheckpoint,mode:'redact'|'refuse'|'off'='off',valid=true,proposalBytes=65536){
 const policy={...CONTEXT_DEFAULTS,proposalBytes},ledger=new ContextLedger(policy),state=saved??({} as ExecutionCheckpoint),events:AssistantEvent[]=[],validated:string[]=[]
 if(saved)await ledger.restore(saved.context)
 const guard=new OutputGuard({outputGuard:{value:mode,layer:'run'}},()=>({}),event=>events.push(event))
 const drafts=new Drafts(ledger,policy,()=>state,event=>events.push(event),()=>true,async text=>{validated.push(text);return answer({status:valid?'valid':'invalid'})},()=> '0'.repeat(32),(v,w)=>guard.value(v,w),signal,(w,p)=>guard.refuse(w,p))
 await drafts.open()
 return {drafts,ledger,state,events,validated}
}
describe('proposal numeric custody',()=>{
 it('retains the accepted source values through edit, checkpoint serialization, resume and undo',async()=>{
  const first=await opened()
  await first.drafts.seed(readProposedPack({documentJson:'{"id":"p","amount":1.20,"zero":-0,"nested":{"amount":2.500},"exponent":1e2}'}))
  const initial=JSON.stringify(first.drafts.proposal()?.document)
  expect(initial).toBe('{"id":"p","amount":1.2,"zero":0,"nested":{"amount":2.5},"exponent":100}')
  const before=JSON.stringify(first.state.draft)
  await expect(first.drafts.edit('propose_edit',{document:'p',old_text:'1.2',new_text:'9007199254740993'},signal)).rejects.toThrow('numerical value changes')
  expect(JSON.stringify(first.drafts.proposal()?.document)).toBe(initial)
  expect(JSON.stringify(first.state.draft)).toBe(before)
  expect(first.validated).toHaveLength(0)
  await first.drafts.edit('propose_edit',{document:'p',old_text:'1.2',new_text:'1.50'},signal)
  const saved=JSON.parse(JSON.stringify({...first.state,context:first.ledger.snapshot()})) as ExecutionCheckpoint
  const resumed=await opened(saved)
  expect(JSON.stringify(resumed.drafts.proposal()?.document)).toContain('"amount":1.5')
  await resumed.drafts.edit('undo_edit',{document:'p'},signal)
  expect(JSON.stringify(resumed.drafts.proposal()?.document)).toBe(initial)
  expect(resumed.state.draft?.documents[0]?.edits).toHaveLength(2)
 })
 it('refuses stored violating text after digest validation; valid already-rounded historical text remains readable',async()=>{
  for(const text of ['{"id":"p","n":9007199254740993}','{"id":"p","n":9007199254740992}']){
   const policy={...CONTEXT_DEFAULTS},ledger=new ContextLedger(policy),digest='sha256:'+await contextDigest(text)
   await ledger.retain({id:'proposal-state:handwritten',kind:'artifact',label:`draft:p@${digest}:0`,value:{text,unknowns:[],undo:[]}})
   const state={draft:{documents:[{id:'draft:p',digest,edits:[]}]},context:ledger.snapshot()} as unknown as ExecutionCheckpoint
   if(text.includes('9007199254740993'))await expect(opened(state)).rejects.toThrow('saved draft text violates')
   else expect(JSON.stringify((await opened(state)).drafts.proposal()?.document)).toBe(text)
  }
 })
 it('bounds custody by the effective lower policy without overriding supported larger policies',async()=>{
  const small=await opened(undefined,'off',true,20)
  await expect(small.drafts.seed(readProposedPack({documentJson:'{"id":"p","title":"too large"}'}))).rejects.toThrow('proposalBytes')
  expect(small.state.draft).toBeUndefined()
  const large=await opened(undefined,'off',true,100000)
  await large.drafts.seed(readProposedPack({documentJson:'{"id":"p","text":"'+'x'.repeat(70000)+'"}'}))
  expect((large.drafts.proposal()?.document as {text:string}).text).toHaveLength(70000)
 })
 it('refuses over-depth already typed seed values before draft custody',async()=>{
  const current=await opened();let document:Record<string,unknown>={n:1}
  for(let at=0;at<128;at++)document={nested:document}
  await expect(current.drafts.seed({document,unknowns:[]})).rejects.toThrow('work bound')
  expect(current.drafts.proposal()).toBeNull()
 })
 it('guards decoded escaped strings and keys before custody and validates redacted proposals',async()=>{
  const source='{"id":"p","n":1.20,"nested":{"\\u0041KIAABCDEFGHIJKLMNOP":"\\u0041KIAABCDEFGHIJKLMNOP"}}'
  for(const mode of ['redact','refuse'] as const)for(const valid of [true,false]){
   const current=await opened(undefined,mode,valid)
   const seed=current.drafts.seed(readProposedPack({documentJson:source}))
   if(mode==='refuse'||!valid){await expect(seed).rejects.toThrow(OutputRefused);expect(current.drafts.proposal()).toBeNull();expect(current.state.draft).toBeUndefined()}
   else {await seed;expect(JSON.stringify(current.drafts.proposal()?.document)).toBe('{"id":"p","n":1.2,"nested":{"[redacted: aws-access-key]":"[redacted: aws-access-key]"}}');expect(current.validated).toHaveLength(1)}
   expect(current.events.some(e=>e.type==='output_guarded')).toBe(true)
  }
 })
})
