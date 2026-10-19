import {it,expect} from 'vitest'
import {vercel} from './engines/vercel'
import {normalize} from './thinking'
import type {AssistantEvent,AssistantSession} from './engine'
const families=['openai-compatible','anthropic','gemini'] as const
it.each(families)('%s keeps structured failure metadata through the real SDK adapter',async family=>{
 // Retry-After asks for longer than one turn waits (engines/vercel/retry.ts), so
 // the refusal is reported after one request, with its retryAt.
 let calls=0
 const session:AssistantSession={prompt:'Explain this',context:{currentRequest:{message:'Explain this',input:'Supplied facts'}},testPrompt:'',tools:[],hostTools:[],callTool:async()=>({}),allowConversation:true,interactive:true,model:{family,model:'fixture',call:async()=>{calls++;return new Response(JSON.stringify({error:{message:'SECRET_PROMPT_DO_NOT_RETAIN',type:'rate_limit_error',code:429}}),{status:429,headers:{'Content-Type':'application/json','Retry-After':'120','request-id':'req_fixture'}})}},thinking:normalize('off',family),signal:new AbortController().signal}
 const events:AssistantEvent[]=[];for await(const e of vercel.start(session))events.push(e)
 expect(events.filter(e=>e.type==='error')).toHaveLength(1)
 expect(events.find(e=>e.type==='error')).toMatchObject({failure:{code:'rate-limited',source:'model',status:429,requestId:'req_fixture',retryAt:expect.any(String)}})
 expect(events.filter(e=>e.type==='end')).toHaveLength(1)
 expect(JSON.stringify(events)).not.toContain('SECRET_PROMPT_DO_NOT_RETAIN')
 expect(calls).toBe(1)
})
it.each(families)('%s handles provider errors after HTTP 200 without exposing their prose',async family=>{
 const data={error:{message:'SECRET_STREAM_ERROR',type:family==='anthropic'?'overloaded_error':'rate_limit_error',code:429,status:'RESOURCE_EXHAUSTED'},...(family==='anthropic'?{type:'error'}:{})}
 const wire=(family==='anthropic'?'event: error\n':'')+'data: '+JSON.stringify(data)+'\n\n'
 const session:AssistantSession={prompt:'Explain this',testPrompt:'',tools:[],hostTools:[],callTool:async()=>({}),allowConversation:true,model:{family,model:'fixture',call:async()=>new Response(wire,{headers:{'Content-Type':'text/event-stream'}})},thinking:normalize('off',family),signal:new AbortController().signal}
 const events:AssistantEvent[]=[];for await(const e of vercel.start(session))events.push(e)
 const error=events.find(e=>e.type==='error')
 expect(error?.failure).toBeDefined()
 // The adapter recovers Google's error envelope from raw chunks because
 // the installed provider's regular chat schema otherwise discards it.
 expect(error?.failure).toMatchObject({code:family==='anthropic'?'unavailable':'rate-limited',outcome:'unknown'})
 expect(events.filter(e=>e.type==='error')).toHaveLength(1)
 expect(events.filter(e=>e.type==='end')).toHaveLength(1)
 expect(JSON.stringify(events)).not.toContain('SECRET_STREAM_ERROR')
})
it('retains a partial Gemini answer and reports its later stream error without retrying',async()=>{
 const frame=(value:unknown)=>'data: '+JSON.stringify(value)+'\n\n'
 const wire=frame({candidates:[{content:{role:'model',parts:[{text:'Partial findings retained.'}]}}]})+frame({error:{code:503,status:'UNAVAILABLE',message:'PRIVATE_STREAM_SENTINEL'}})
 let calls=0
 const session:AssistantSession={prompt:'Explain this',testPrompt:'',tools:[],hostTools:[],callTool:async()=>({}),allowConversation:true,interactive:true,model:{family:'gemini',model:'fixture',call:async()=>{calls++;return new Response(wire,{headers:{'Content-Type':'text/event-stream'}})}},thinking:normalize('off','gemini'),signal:new AbortController().signal}
 const events:AssistantEvent[]=[];for await(const e of vercel.start(session))events.push(e)
 expect(events.some(e=>e.type==='message_progress'&&e.text.includes('Partial findings retained.'))).toBe(true)
 expect(events.filter(e=>e.type==='error')).toHaveLength(1)
 expect(events.find(e=>e.type==='error')).toMatchObject({failure:{code:'unavailable',outcome:'unknown',status:503}})
 expect(events.filter(e=>e.type==='end')).toHaveLength(1)
 expect(events.some(e=>e.type==='proposal')).toBe(false)
 expect(JSON.stringify(events)).not.toContain('PRIVATE_STREAM_SENTINEL')
 expect(calls).toBe(1)
})
