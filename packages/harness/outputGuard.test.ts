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

it('guards every draft scalar and validates holes atomically',async()=>{
 for(const [before,after,pattern] of [[42,123456,'123456'],[false,true,'true'],['before',null,'null']] as const){
  for(const mode of ['redact','refuse','off'] as const)for(const valid of [true,false]){
   const events:AssistantEvent[]=[],counts={},ledger=new ContextLedger({...CONTEXT_DEFAULTS}),state={} as ExecutionCheckpoint
   const guard=new OutputGuard({outputGuard:{value:mode,layer:'run'},outputPatterns:{value:[{name:'host-scalar',pattern}],layer:'project'}},()=>counts,e=>events.push(e)),signal=new AbortController().signal
   const seen:unknown[]=[]
   const drafts=new Drafts(ledger,{...CONTEXT_DEFAULTS},()=>state,e=>events.push(e),()=>true,async text=>{seen.push(JSON.parse(text));return answer({status:valid?'valid':'invalid'})},()=> '0'.repeat(32),(v,w)=>guard.value(v,w),signal,(w,p)=>guard.refuse(w,p))
   await drafts.open([{id:'pack',document:{value:before,untouched:7}}])
   const prior=structuredClone(state.draft)
   const edit=drafts.edit('propose_edit',{document:'pack',old_text:JSON.stringify(before),new_text:JSON.stringify(after)},signal)
   if(mode==='refuse'||!valid){await expect(edit).rejects.toThrow();expect(drafts.proposal()?.document).toEqual({value:before,untouched:7});expect(state.draft).toEqual(prior)}
   else{await edit;expect(drafts.proposal()?.document).toEqual({value:mode==='off'?after:'[redacted: host-scalar]',untouched:7})}
   expect(seen).toEqual(mode==='refuse'?[]:[{value:mode==='off'?after:'[redacted: host-scalar]',untouched:7}])
   if(mode!=='off')expect(events).toContainEqual({type:'output_guarded',where:'draft edit',pattern:'host-scalar',action:mode})
   if(mode==='redact'&&!valid)expect(events).toContainEqual({type:'output_guarded',where:'draft edit',pattern:'proposal-validation',action:'refuse'})
  }
 }
})


import {managedContext,contextOf} from './context/managed'
it('guards each model-text notice field under the run mode and exempts tool results',async()=>{
 const key='AKIAABCDEFGHIJKLMNOP',hole='[redacted: aws-access-key]'
 const notices:AssistantEvent[]=[{type:'edit_refused',document:key,reason:key},{type:'edit_applied',document:key,line:1,bytes:1,valid:true},{type:'guardrail',tool:key,detail:key,action:'refused',reason:'edit refused'}]
 for(const mode of ['redact','refuse','off'] as const)for(const queued of [true,false]){
  const events:AssistantEvent[]=[]
  const session={prompt:'Review',testPrompt:'',tools:[],hostTools:[],callTool:async()=>answer({}),signal:new AbortController().signal,allowConversation:true,interactive:true,critic:'off' as const,policy:{outputGuard:mode},agent:{model:'fixture',run:async()=>{}}}
  for await(const event of managedContext(session,async function*(ready){
   if(queued)for(const notice of notices)contextOf(ready).memoryNotice(notice)
   else for(const notice of notices)yield notice
   yield {type:'tool_result' as const,name:'fixture',text:key,isError:false}
   yield {type:'end' as const}
  }))events.push(event)
  const delivered=events.filter(e=>e.type==='edit_refused'||e.type==='edit_applied'||e.type==='guardrail'&&e.reason!=='output refused')
  if(mode==='refuse')expect(delivered).toEqual([])
  else expect(delivered).toEqual(mode==='off'?notices:[{type:'edit_refused',document:hole,reason:hole},{type:'edit_applied',document:hole,line:1,bytes:1,valid:true},{type:'guardrail',tool:hole,detail:hole,action:'refused',reason:'edit refused'}])
  if(mode!=='refuse'||queued)expect(events.find(e=>e.type==='tool_result')).toMatchObject({text:key})
  for(const event of delivered)expect(argumentProblem(eventSchema.$defs[event.type as 'edit_refused'],event)).toBeUndefined()
 }
})

it('refuses a notice whose redaction would exceed its document bound',async()=>{
 const events:AssistantEvent[]=[],session={prompt:'Review',testPrompt:'',tools:[],hostTools:[],callTool:async()=>answer({}),signal:new AbortController().signal,allowConversation:true,interactive:true,critic:'off' as const,agent:{model:'fixture',run:async()=>{}}}
 for await(const event of managedContext(session,async function*(ready){contextOf(ready).memoryNotice({type:'edit_refused',document:'AKIAABCDEFGHIJKLMNOP '.repeat(24)+'x'.repeat(8),reason:'No draft.'});yield {type:'end' as const}}))events.push(event)
 expect(events.some(e=>e.type==='edit_refused')).toBe(false)
 expect(events).toContainEqual({type:'output_guarded',where:'draft edit',pattern:'invalid-output',action:'refuse'})
})


import {gatedRuntime} from './testing/scripted'
it('withholds oversized gate notices with schema-valid refusal labels in both modes',async()=>{
 for(const mode of ['redact','refuse'] as const){
  const events:AssistantEvent[]=[]
  const runtime=await gatedRuntime({tools:[],allowed:['experimental_evaluate'],handle:()=>answer({}),onEvent:event=>events.push(event)})
  try{
   const session={prompt:'Review',testPrompt:'',tools:[],hostTools:[],callTool:runtime.callTool,signal:new AbortController().signal,allowConversation:true,interactive:true,critic:'off' as const,policy:{outputGuard:mode},agent:{model:'fixture',run:async()=>{}}}
   for await(const event of managedContext(session,async function*(){
    await runtime.callTool('experimental_evaluate',{rehearsal:'x'.repeat(200000)})
    yield {type:'end' as const}
   }))events.push(event)
   expect(runtime.seen).toEqual([{name:'experimental_evaluate',args:{rehearsal:true}}])
   expect(events.filter(e=>e.type==='error')).toEqual([])
   const notices=events.filter(e=>e.type==='guardrail'||e.type==='output_guarded')
   expect(notices).toEqual([{type:'output_guarded',where:'message',pattern:'work-bound',action:'refuse'},{type:'guardrail',tool:'message',action:'refused',reason:'output refused',detail:'Output refused: work-bound.'}])
   for(const event of notices)expect(argumentProblem(eventSchema.$defs[event.type as 'guardrail'],event)).toBeUndefined()
  }finally{await runtime.close()}
 }
})

it('guards oversized undeclared names before calls and checkpoints with schema-valid refusal labels',async()=>{
 for(const mode of ['redact','refuse'] as const){
  const events:AssistantEvent[]=[],name='x'.repeat(151)
  const session={prompt:'Review',testPrompt:'',tools:[],hostTools:[],callTool:async()=>answer({}),signal:new AbortController().signal,allowConversation:true,interactive:true,critic:'off' as const,policy:{outputGuard:mode},context:{currentRequest:{message:'Review',input:'Review'}},agent:{model:'fixture',run:async()=>{}}}
  for await(const event of managedContext(session,async function*(){yield {type:'tool_call' as const,name,args:{}};yield {type:'end' as const}}))events.push(event)
  expect(events.find(e=>e.type==='tool_call')).toMatchObject({name:'Output refused: invalid-output.'})
  expect(events).toContainEqual({type:'output_guarded',where:'message',pattern:'invalid-output',action:'refuse'})
  expect(JSON.stringify(events)).not.toContain(name)
  for(const event of events)if(event.type==='guardrail'||event.type==='output_guarded')expect(argumentProblem(eventSchema.$defs[event.type],event)).toBeUndefined()
 }
})

it('counts record markers separately, including pattern labels inherited by plain objects',()=>{
 const counts:Record<string,number>={},events:AssistantEvent[]=[],marker={redactions:{}}
 const guard=new OutputGuard({outputPatterns:{value:[{name:'constructor',pattern:'SECRET'}],layer:'run'}},()=>counts,e=>events.push(e))
 expect(guard.value('SECRET','message',false,marker)).toBe('[redacted: constructor]')
 expect(marker.redactions).toEqual({constructor:1})
 expect(counts).toEqual({})
 expect(events).toEqual([])
 guard.value('SECRET','message')
 expect(counts).toEqual({constructor:1})
})
