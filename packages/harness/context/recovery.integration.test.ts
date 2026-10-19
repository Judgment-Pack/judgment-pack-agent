import {readFileSync} from 'node:fs'
import {it,expect} from 'vitest'
import {vercel} from '../engines/vercel'
import {normalize} from '../thinking'
import {answer} from './ledger'
import {extendExecution} from '../execution/state'
import type {AssistantEvent,AssistantSession} from '../engine'
const stream=(part:unknown)=>new Response('data: '+JSON.stringify({candidates:[{content:{role:'model',parts:[part]},finishReason:'STOP'}]})+'\n\n',{headers:{'content-type':'text/event-stream'}})
const proposal=(document:unknown)=>'Proposed change, ready for review.\n```json\n'+JSON.stringify({proposal:{kind:'create',document,unknowns:[]}})+'\n```'
const base=():AssistantSession=>({prompt:'Apply option 1: refine the exception. Keep the expectations.',context:{currentRequest:{message:'option 1',input:'Current pack and unchanged tests'},configuration:{version:1,models:{fixture:{contextWindowTokens:16384,outputTokens:4096}}}},testPrompt:'',tools:[],hostTools:[],callTool:async()=>({}),model:{family:'gemini',model:'fixture',call:async()=>stream({text:'unused'})},thinking:normalize('off','gemini'),signal:new AbortController().signal,allowConversation:true,interactive:true,adversarialReview:false})
it('finishes a focused edit with large Gemini signatures and a searchable schema in five requests',async()=>{
 const session=base();session.prompt+='\nEarlier context '.repeat(12000)
 const original={id:'current-pack',version:'0.1.0',exception:{onUnknown:'escalate'}},updated={...original,version:'0.1.1',exception:{onUnknown:'ignore'}}
 let calls=0,authorReads=0,evaluations=0
 session.hostTools=[{name:'get_authoring_instructions',contextPolicy:{immutable:true,reference:true},description:'Read contract',effect:'read',inputSchema:{type:'object',properties:{}},execute:async()=>{authorReads++;return answer({instructions:'Keep the current pack identity and decimal strings.'})}}]
 session.tools=[{name:'get_schema',inputSchema:{type:'object',properties:{}}},{name:'experimental_evaluate',inputSchema:{type:'object',properties:{pack:{type:'string'},facts:{type:'string'},rehearsal:{type:'boolean'}}}}]
 session.callTool=async(name,args)=>{
  if(name==='get_schema')return {content:[{type:'text',text:JSON.stringify({$defs:{condition:{operator:'equals',description:'schema '.repeat(4000)}}})}],structuredContent:{status:'ok'}}
  expect(name).toBe('experimental_evaluate');expect(args.rehearsal).toBe(true);evaluations++;return answer({outcome:'set-aside',facts:{decimal:'24',flag:false}})
 }
 session.model.call=async(_path,wire)=>{
  const body=JSON.parse(wire.body),text=JSON.stringify(body);calls++
  expect(calls).toBeLessThanOrEqual(5)
  let call:{name:string;args:unknown}|undefined
  if(calls===1)call={name:'get_authoring_instructions',args:{}}
  if(calls===2)call={name:'get_schema',args:{}}
  if(calls===3){
   const id=text.match(/[0-9a-f-]{36}:result-get_schema-[0-9a-f]+:[0-9a-f]{16}/)?.[0]
   expect(id).toBeDefined();expect(text).toContain('/content/0/text')
   call={name:'read_context',args:{id,query:'operator'}}
  }
  if(calls===4){expect(text).toContain('matches');call={name:'experimental_evaluate',args:{rehearsal:true,pack:JSON.stringify(updated),facts:'{"routing":"not-documented"}'}}}
  if(calls===5){expect(events.filter(e=>e.type==='tool_result').map(e=>({name:e.name,isError:e.isError,text:e.text.slice(0,350)})).filter(e=>e.isError)).toEqual([]);expect(text.includes('set-aside')).toBe(true);return stream({text:proposal(updated)})}
  return stream({functionCall:call,thoughtSignature:'s'.repeat(50000)})
 }
 const request=session.model.call;let callbackError:unknown;session.model.call=async(...args)=>{try{return await request(...args)}catch(error){callbackError=error;throw error}}
 const events:AssistantEvent[]=[];for await(const event of vercel.start(session))events.push(event)
 if(callbackError)throw callbackError
 expect(events.filter(e=>e.type==='error'||e.type==='budget_paused')).toEqual([])
 expect(events.find(e=>e.type==='proposal')).toMatchObject({document:updated})
 expect(events.filter(e=>e.type==='tool_result').every(e=>!e.isError)).toBe(true)
 expect(calls).toBe(5);expect(authorReads).toBe(1);expect(evaluations).toBe(1)
 expect(original.exception.onUnknown).toBe('escalate')
})

it.runIf(Boolean(process.env.JPS_CHAT_CONTEXT_STORE&&process.env.JPS_CHAT_CONTEXT_ID))('resumes the exact failed saved session within its remaining allowance, without replay or live inference',async()=>{
 const file=process.env.JPS_CHAT_CONTEXT_STORE!,before=readFileSync(file,'utf8')
 const saved=JSON.parse(before).chats.find((c:any)=>c.id===process.env.JPS_CHAT_CONTEXT_ID).checkpoint.state
 const latest=[...saved.turns].reverse().find((t:any)=>t.role==='user')
 let execution=structuredClone(saved.execution)
 if(execution.used===execution.limit)execution=extendExecution(execution,true)
 const last=[...execution.operations].reverse().find((op:any)=>op.name==='experimental_evaluate'&&op.args.pack)
 expect(last).toBeDefined();expect(execution.limit-execution.used).toBeGreaterThanOrEqual(3)
 const document=JSON.parse(last.args.pack),session=base()
 session.context={...session.context,currentRequest:{message:latest.statement??latest.text,input:latest.input??latest.text},execution}
 session.prompt='Continue the requested change using completed results.\n'+latest.input
 let calls=0,used=execution.used
 session.callTool=async()=>{throw Error('No completed runtime operation may be replayed')}
 session.context.checkpoint=async snapshot=>{used=snapshot.used}
 session.model.call=async(_path,wire)=>{
  calls++;expect(calls).toBeLessThanOrEqual(3)
  const text=JSON.stringify(JSON.parse(wire.body))
  if(calls===1){
   expect(text).toContain('resumeFromCompletedResults')
   const id=text.match(new RegExp('[0-9a-f-]{36}:operation-'+last.id+':[0-9a-f]{16}'))?.[0]
   expect(id).toBeDefined()
   return stream({functionCall:{name:'read_context',args:{id,path:'/args/pack',limit:6000}},thoughtSignature:'s'.repeat(50000)})
  }
  if(calls===2){
   expect(text).toContain('read_context')
   const id=text.match(new RegExp('[0-9a-f-]{36}:operation-'+last.id+':[0-9a-f]{16}'))?.[0]
   return stream({functionCall:{name:'read_context',args:{id,path:'/result',limit:6000}},thoughtSignature:'s'.repeat(50000)})
  }
  return stream({text:proposal(document)})
 }
 const request=session.model.call;let callbackError:unknown;session.model.call=async(...args)=>{try{return await request(...args)}catch(error){callbackError=error;throw error}}
 const events:AssistantEvent[]=[];for await(const event of vercel.start(session))events.push(event)
 if(callbackError)throw callbackError
 expect(events.filter(e=>e.type==='error'||e.type==='budget_paused')).toEqual([])
 expect(events.find(e=>e.type==='proposal')).toMatchObject({document})
 expect(events.filter(e=>e.type==='tool_call').map(e=>e.name)).toEqual(['read_context','read_context'])
 expect(used).toBe(execution.used+3);expect(used).toBeLessThanOrEqual(execution.limit)
 expect(readFileSync(file,'utf8')).toBe(before)
 console.log(JSON.stringify({savedRequests:execution.used,remaining:execution.limit-execution.used,recoveryRequests:calls,completedWorkReplayed:false,liveProviderCalls:0,savedDataUnchanged:true}))
})

const topics=['dialect mapping','job scheduling','document analysis','ordinary conversation','policy authoring'] as const
const families=['openai-compatible','anthropic','gemini'] as const
it.each(families.flatMap(family=>topics.map(topic=>({family,topic}))))('$family uses the shared recovery projection for $topic',async({family,topic})=>{
 const session=base();session.model.family=family;session.thinking=normalize('off',family)
 session.context={...session.context,currentRequest:{message:'Apply the requested change',input:'Current scope: '+topic}}
 session.prompt='Current task: '+topic+'\n'+'Older discussion '.repeat(5000)
 let calls=0
 session.hostTools=[
  {name:'future_reference',effect:'read',contextPolicy:{immutable:true,reference:true},description:'Read the task reference',inputSchema:{type:'object',properties:{}},execute:async()=>answer({reference:'REFERENCE FOR '+topic,rule:'Preserve approved scope. '.repeat(60)})},
  {name:'future_inspect',effect:'read',description:'Inspect current work',inputSchema:{type:'object',properties:{}},execute:async()=>answer({status:'ready',zero:0,flag:false,nothing:null,decimal:'24',details:'detail '.repeat(1400)})}
 ]
 session.model.call=async(_path,wire)=>{
  calls++;expect(calls).toBeLessThanOrEqual(3)
  const body=JSON.parse(wire.body),shown=JSON.stringify(body)
  if(calls===3){expect(shown.includes('REFERENCE FOR '+topic)).toBe(true);expect(shown.includes('ready')).toBe(true)}
  const name=calls===1?'future_reference':'future_inspect',text='Completed '+topic+' using the current scope.'
  let result:unknown
  if(family==='gemini')result={candidates:[{content:{role:'model',parts:calls===3?[{text}]:[{functionCall:{name,args:{}}}]},finishReason:'STOP'}]}
  else if(family==='anthropic')result={id:'message_'+calls,type:'message',role:'assistant',model:'fixture',content:calls===3?[{type:'text',text}]:[{type:'tool_use',id:'tool_'+calls,name,input:{}}],stop_reason:calls===3?'end_turn':'tool_use',stop_sequence:null,usage:{input_tokens:1,output_tokens:1}}
  else result={id:'completion_'+calls,object:'chat.completion',created:0,model:'fixture',choices:[{index:0,message:{role:'assistant',content:calls===3?text:null,...(calls===3?{}:{tool_calls:[{id:'tool_'+calls,type:'function',function:{name,arguments:'{}'}}]})},finish_reason:calls===3?'stop':'tool_calls'}],usage:{prompt_tokens:1,completion_tokens:1,total_tokens:2}}
  return new Response(JSON.stringify(result),{headers:{'Content-Type':'application/json'}})
 }
 const request=session.model.call;let callbackError:unknown;session.model.call=async(...args)=>{try{return await request(...args)}catch(error){callbackError=error;throw error}}
 const events:AssistantEvent[]=[];for await(const event of vercel.start(session))events.push(event)
 if(callbackError)throw callbackError
 expect(events.filter(e=>e.type==='error'||e.type==='budget_paused')).toEqual([])
 expect(events.filter(e=>e.type==='tool_result').every(e=>!e.isError)).toBe(true)
 expect(events.some(e=>e.type==='message'&&e.text.includes('Completed '+topic))).toBe(true)
 expect(calls).toBe(3)
 expect(events.some(e=>e.type==='execution_checkpoint'&&e.checkpoint.context.entries.some(entry=>entry.label==='Earlier completed model steps'))).toBe(true)
})
