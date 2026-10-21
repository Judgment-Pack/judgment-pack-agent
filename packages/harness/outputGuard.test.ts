import {describe,it,expect} from 'vitest'
import {readFileSync,readdirSync} from 'node:fs'
import {fileURLToPath,URL as NodeURL} from 'node:url'
import {OutputGuard,OutputRefused,readOutputPatterns,readOutputAllow} from './outputGuard'
import type {AssistantEvent} from './engine'
const dir=fileURLToPath(new NodeURL('../../contract/vectors/output/',import.meta.url))
const vectors=readdirSync(dir).map(file=>JSON.parse(readFileSync(dir+file,'utf8')))
describe('output boundary shared vectors',()=>{
 for(const vector of vectors.filter(v=>v.guard))it(vector.description,()=>{
  const v=vector.guard,counts:Record<string,number>={},events:AssistantEvent[]=[]
  const guard=new OutputGuard({outputGuard:{value:v.mode,layer:'run'},outputPatterns:{value:v.patterns??[],layer:'project'},outputAllow:{value:v.allow??[],layer:'project'}},()=>counts,e=>events.push(e))
  if(v.refused)expect(()=>guard.value(v.input,v.where)).toThrow(new OutputRefused(v.refused))
  else expect(guard.value(v.input,v.where)).toBe(v.output)
  expect(counts).toEqual(v.counts)
  if(vector.schema.endsWith('output_guarded'))expect(events.filter(e=>e.type==='output_guarded')).toEqual(vector.items)
  if(v.negative!==undefined){expect(guard.value(v.negative,v.where)).toBe(v.negative);expect(counts).toEqual(v.counts)}
 })
 it('refuses invalid host patterns and names before use',()=>{
  for(const value of [null,{},[{name:'',pattern:'x'}],[{name:'aws-access-key',pattern:'x'}],[{name:'secret',pattern:'(?=x)'}]])expect(()=>readOutputPatterns(value)).toThrow()
  for(const value of [null,{},[''],['x','x']])expect(()=>readOutputAllow(value)).toThrow()
 })
 it('bounds aggregate work and never returns a partially checked answer',()=>{
  const counts={},events:AssistantEvent[]=[]
  const guard=new OutputGuard({},()=>counts,e=>events.push(e))
  expect(()=>guard.value('x'.repeat(200000),'message')).toThrow(OutputRefused)
  expect(events).toContainEqual({type:'output_guarded',where:'message',pattern:'work-bound',action:'refuse'})
 })
 it('does not scan holes again; traverses nested values and keys',()=>{
  const counts={},guard=new OutputGuard({},()=>counts,()=>{})
  expect(guard.value({nested:['AKIAABCDEFGHIJKLMNOP']},'tool argument')).toEqual({nested:['[redacted: aws-access-key]']})
 })
})

import eventSchema from '../../contract/schemas/run-event.json'
import {argumentProblem} from './sourceArguments'
import {Drafts} from './edits'
import {ContextLedger,answer} from './context/ledger'
import {CONTEXT_DEFAULTS} from './context/policy'
import type {ExecutionCheckpoint} from './execution/state'
it('refusal notices stay schema-valid with missing, empty and oversized outbound fields',()=>{
 for(const input of [{secret:'AKIAABCDEFGHIJKLMNOP'},{empty:'',secret:'AKIAABCDEFGHIJKLMNOP'},{oversized:'x'.repeat(200000)}]){
  const events:AssistantEvent[]=[],counts={}
  const guard=new OutputGuard({outputGuard:{value:'refuse',layer:'run'}},()=>counts,e=>events.push(e))
  expect(()=>guard.value(input,'tool argument')).toThrow(OutputRefused)
  expect(events.map(e=>e.type)).toEqual(['output_guarded','guardrail'])
  for(const event of events)if(event.type==='guardrail'||event.type==='output_guarded')expect(argumentProblem(eventSchema.$defs[event.type],event)).toBeUndefined()
 }
})
it('redacted proposals and edits validate the hole; malformed validation replies refuse with schema-valid notices',async()=>{
 for(const result of [undefined,{}, {status:''},{status:'x'.repeat(10000)}]){
  const events:AssistantEvent[]=[],counts={},ledger=new ContextLedger({...CONTEXT_DEFAULTS}),state={} as ExecutionCheckpoint
  const guard=new OutputGuard({},()=>counts,e=>events.push(e)),signal=new AbortController().signal
  const seen:string[]=[]
  const drafts=new Drafts(ledger,{...CONTEXT_DEFAULTS},()=>state,e=>events.push(e),()=>true,async text=>{seen.push(text);return answer(result)},()=> '0'.repeat(32),(v,w)=>guard.value(v,w),signal,(w,p)=>guard.refuse(w,p))
  await drafts.open([{id:'pack',document:{title:'Before'}}])
  await expect(drafts.seed({document:{title:'AKIAABCDEFGHIJKLMNOP'},unknowns:[]})).rejects.toThrow(OutputRefused)
  await expect(drafts.edit('propose_edit',{document:'pack',old_text:'Before',new_text:'AKIAABCDEFGHIJKLMNOP'},signal)).rejects.toThrow(OutputRefused)
  expect(drafts.proposal()?.document).toEqual({title:'Before'})
  expect(seen).toHaveLength(2)
  expect(seen.every(text=>text.includes('[redacted: aws-access-key]')&&!text.includes('AKIAABCDEFGHIJKLMNOP'))).toBe(true)
  for(const event of events)if(event.type==='guardrail'||event.type==='output_guarded')expect(argumentProblem(eventSchema.$defs[event.type],event)).toBeUndefined()
  expect(events.filter(e=>e.type==='output_guarded'&&e.pattern==='proposal-validation')).toHaveLength(2)
 }
})
