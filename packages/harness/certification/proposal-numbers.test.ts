/** Offline API fixtures run the actual model engines, their SDK readers and accepted-event custody. */
import {describe,it,expect} from 'vitest'
import {vercel} from '../engines/vercel'
import {deepagents} from '../engines/deepagents'
import {runOnModelEngine,runOnAgentEngine,agentRunFrom} from '../testing/scenarioDriver'
import {runtimeAnswer} from '../testing/scripted'
import {PROPOSE_PACK_SCHEMA} from '../engines/contract'
import type {EndpointKind} from '../host'
import type {AssistantEvent} from '../engine'
const source='{"id":"p","facts":{"amount":1.20,"zero":-0,"nested":{"amount":2.500},"exponent":1e2}}'
const canonical='{"id":"p","facts":{"amount":1.2,"zero":0,"nested":{"amount":2.5},"exponent":100}}'
const host={name:'propose_pack',taxonomy:{operation:'propose',effect:'propose'} as const,effect:'proposal' as const,description:'Propose.',inputSchema:PROPOSE_PACK_SCHEMA,execute:async()=>{throw Error('host proposal execute reached')}}
function reply(family:EndpointKind,args?:string,text='Done.'):Response{
 let body:string
 if(family==='anthropic')body='{"id":"a","type":"message","role":"assistant","model":"fixture","content":['+(args?' {"type":"tool_use","id":"p1","name":"propose_pack","input":'+args+'}':'{"type":"text","text":'+JSON.stringify(text)+'}')+'],"stop_reason":"'+(args?'tool_use':'end_turn')+'","usage":{"input_tokens":1,"output_tokens":1}}'
 else if(family==='gemini')body='{"candidates":[{"content":{"role":"model","parts":['+(args?'{"functionCall":{"name":"propose_pack","args":'+args+'}}':'{"text":'+JSON.stringify(text)+'}')+']},"finishReason":"STOP"}]}'
 else body=JSON.stringify({id:'a',object:'chat.completion',model:'fixture',choices:[{index:0,message:{role:'assistant',content:args?null:text,...(args?{tool_calls:[{id:'p1',type:'function',function:{name:'propose_pack',arguments:args}}]}:{})},finish_reason:args?'tool_calls':'stop'}]})
 return new Response(body,{headers:{'content-type':'application/json'}})
}
const proposals=(events:AssistantEvent[])=>events.filter((e):e is Extract<AssistantEvent,{type:'proposal'}>=>e.type==='proposal')
for(const engine of [vercel,deepagents])describe(engine.id+' numeric API/native proposals',()=>{
 for(const family of ['openai-compatible','anthropic','gemini'] as const){
  it(family+' accepts native JSON text and retains the same event/checkpoint document',async()=>{
   let requests=0
   const ran=await runOnModelEngine(()=>{throw Error('unexpected global fetch')},{engine,hostTools:[host],session:{interactive:true,critic:'off',context:{currentRequest:{message:'Draft.',input:'Draft.'}},model:{family,model:'fixture',call:async()=>reply(family,requests++===0?JSON.stringify({documentJson:source}):undefined)}}})
   expect(proposals(ran.events).map(e=>JSON.stringify(e.document))).toEqual([canonical])
   const checkpoint=ran.events.filter(e=>e.type==='execution_checkpoint').at(-1)?.checkpoint
   const text=checkpoint?.context.entries.map(e=>e.value).find((v):v is {text:string}=>!!v&&typeof v==='object'&&'text'in v&&typeof v.text==='string')?.text
   expect(text).toBeDefined();expect(JSON.stringify(JSON.parse(text!))).toBe(canonical)
  })
  for(const value of ['9007199254740993','9007199254740991.1','1e400'])it(family+' refuses already parsed legacy source '+value,async()=>{
   let requests=0
   const ran=await runOnModelEngine(()=>{throw Error('unexpected global fetch')},{engine,hostTools:[host],session:{critic:'off',model:{family,model:'fixture',call:async()=>reply(family,requests++===0?' {"document":{"id":"p","n":'+value+'}}':undefined)}}})
   expect(proposals(ran.events)).toHaveLength(0)
   expect(ran.events.some(e=>e.type==='tool_result'&&e.name==='propose_pack'&&e.isError&&e.text.includes('documentJson'))).toBe(true)
  })
  it(family+' checks a real fenced API response and refuses numerical changes',async()=>{
   for(const [document,accepted] of [[source,true],['{"id":"p","n":9007199254740993}',false]] as const){
    const text='```json\n{"proposal":{"document":'+document+'}}\n```'
    const ran=await runOnModelEngine(()=>{throw Error('unexpected global fetch')},{engine,session:{critic:'off',model:{family,model:'fixture',call:async()=>reply(family,undefined,text)}}})
    expect(proposals(ran.events)).toHaveLength(accepted?1:0)
    if(accepted)expect(JSON.stringify(proposals(ran.events)[0]!.document)).toBe(canonical)
    else expect(ran.events.some(e=>e.type==='error'&&e.message.includes('numerical value changes'))).toBe(true)
   }
  })
 }
})
it('Codex native callback refuses previously rounded objects and accepts the explicit text source',async()=>{
 for(const args of [{document:JSON.parse('{"n":9007199254740991.1}')},{document:{n:null}},{documentJson:source}]){
  const answers:{text:string;isError:boolean}[]=[]
  const ran=await runOnAgentEngine(agentRunFrom([{call:{tool:'propose_pack',arguments:args}},{say:'Done.'}],answers),{hostTools:[host],session:{critic:'off',interactive:true}})
  if('documentJson'in args){expect(proposals(ran.events).map(e=>JSON.stringify(e.document))).toEqual([canonical]);expect(answers[0]?.isError).toBe(false)}
  else {expect(proposals(ran.events)).toHaveLength(0);expect(answers[0]?.isError).toBe(true);expect(answers[0]?.text).toContain('documentJson')}
 }
})
it('native decoded escaped values stay guarded before the authoritative accepted event',async()=>{
 const documentJson='{"id":"p","n":1.20,"nested":{"\\u0041KIAABCDEFGHIJKLMNOP":"\\u0041KIAABCDEFGHIJKLMNOP"}}'
 for(const engine of [vercel,deepagents])for(const mode of ['redact','refuse'] as const){
  let requests=0
  const ran=await runOnModelEngine(()=>{throw Error('unexpected global fetch')},{engine,hostTools:[host],handle:()=>runtimeAnswer({status:'valid'}),session:{critic:'off',policy:{outputGuard:mode},model:{family:'openai-compatible',model:'fixture',call:async()=>reply('openai-compatible',requests++===0?JSON.stringify({documentJson}):undefined)}}})
  expect(ran.events.some(e=>e.type==='output_guarded'&&e.pattern==='aws-access-key')).toBe(true)
  if(mode==='refuse')expect(proposals(ran.events)).toHaveLength(0)
  else expect(JSON.stringify(proposals(ran.events)[0]?.document)).toBe('{"id":"p","n":1.2,"nested":{"[redacted: aws-access-key]":"[redacted: aws-access-key]"}}')
 }
})

const fence=(document:string)=>'```json\n{"proposal":{"document":'+document+'}}\n```'
const recoveryCases=[
 ['numeric loss','{"id":"p","n":9007199254740993}','numerical value changes'],
 ['exponent bound','{"id":"p","n":0e10000000}','work bound'],
 ['depth bound','{"id":"p","n":'+'['.repeat(128)+'0'+']'.repeat(128)+'}','work bound'],
 ['depth beyond generic parser limit','{"id":"p","n":'+'['.repeat(10001)+'0'+']'.repeat(10001)+'}','work bound']
] as const
for(const engine of [vercel,deepagents])for(const family of ['openai-compatible','anthropic','gemini'] as const){
 for(const [name,document,detail] of recoveryCases)it(engine.id+' '+family+' terminal source refusal prevents replacement: '+name,async()=>{
  let requests=0
  const ran=await runOnModelEngine(()=>{throw Error('unexpected global fetch')},{engine,session:{critic:'off',model:{family,model:'fixture',call:async()=>reply(family,undefined,fence(requests++===0?document:'{"id":"p","n":2}'))}}})
  expect(requests).toBe(1)
  expect(proposals(ran.events)).toHaveLength(0)
  expect(ran.events.some(e=>e.type==='error'&&e.message.includes(detail))).toBe(true)
  expect(ran.events.some(e=>e.type==='response_recovery')).toBe(false)
 })
 it(engine.id+' '+family+' malformed JSON still receives one reformat',async()=>{
  let requests=0
  const ran=await runOnModelEngine(()=>{throw Error('unexpected global fetch')},{engine,session:{critic:'off',model:{family,model:'fixture',call:async()=>reply(family,undefined,requests++===0?'```json\n{"proposal":{"document":oops}}\n```':fence('{"id":"p","n":2}'))}}})
  expect(requests).toBe(2)
  expect(proposals(ran.events).map(e=>JSON.stringify(e.document))).toEqual(['{"id":"p","n":2}'])
 })
}
for(const [name,document,detail] of recoveryCases)it('Codex terminal source refusal prevents replacement: '+name,async()=>{
 let requests=0
 const ran=await runOnAgentEngine(async(request,callbacks)=>agentRunFrom([{say:fence(requests++===0?document:'{"id":"p","n":2}')}])(request,callbacks),{session:{critic:'off'}})
 expect(requests).toBe(1)
 expect(proposals(ran.events)).toHaveLength(0)
 expect(ran.events.some(e=>e.type==='error'&&e.message.includes(detail))).toBe(true)
 expect(ran.events.some(e=>e.type==='response_recovery')).toBe(false)
})
it('Codex malformed JSON still receives one reformat',async()=>{
 let requests=0
 const ran=await runOnAgentEngine(async(request,callbacks)=>agentRunFrom([{say:requests++===0?'```json\n{"proposal":{"document":oops}}\n```':fence('{"id":"p","n":2}')}])(request,callbacks),{session:{critic:'off'}})
 expect(requests).toBe(2)
 expect(proposals(ran.events).map(e=>JSON.stringify(e.document))).toEqual(['{"id":"p","n":2}'])
})
