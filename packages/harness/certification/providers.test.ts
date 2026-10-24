import {describe,it,expect} from 'vitest'
import refusals from '../../../contract/vectors/failover/refused-mid-delegation.json'
import {contextualSession} from '../context/session'
import partialUsage from '../../../contract/vectors/failover/usage-partial.json'
import geminiModelPaths from '../../../contract/vectors/failover/gemini-model-path.json'
import {appendFileSync} from 'node:fs'
import {vercel} from '../engines/vercel'
import {deepagents} from '../engines/deepagents'
import {codex} from '../engines/codex'
import {normalize} from '../thinking'
import {ChatContext} from '../context/chat'
import {withDelegation} from '../delegation'
import type {Engine,EngineSession,AssistantEvent,AssistantSession} from '../engine'
import type {ProviderFamily} from '../policy'
import type {AgentRun} from '../agent'
import {AIFailureError} from '../failures'
const proposal='```json\n{"proposal":{"document":{"id":"pack"},"unknowns":[]}}\n```'
const task={title:'Review',objective:'Check the supplied claims.',profile:'review'}
function answer(family:ProviderFamily,text:string,delegate=false){
 const args={tasks:[task]}
 const body=family==='anthropic'?{id:'message',type:'message',role:'assistant',model:'review-model',content:[{type:'text',text}],stop_reason:'end_turn',usage:{input_tokens:10,output_tokens:5}}
 :family==='gemini'?{candidates:[{content:{role:'model',parts:[{text}]},finishReason:'STOP'}],usageMetadata:{promptTokenCount:10,candidatesTokenCount:5,totalTokenCount:15}}
 :{id:'response',object:'chat.completion',model:'author-model',choices:[{index:0,message:{role:'assistant',content:delegate?null:text,...(delegate?{tool_calls:[{id:'task-call',type:'function',function:{name:'delegate_tasks',arguments:JSON.stringify(args)}}]}:{})},finish_reason:delegate?'tool_calls':'stop'}],usage:{prompt_tokens:10,completion_tokens:5,total_tokens:15}}
 return new Response(JSON.stringify(body),{headers:{'content-type':'application/json'}})
}
async function play(engine:Engine,choice:string,connected=true,child=false,family:ProviderFamily='anthropic',subagent=false,chat?:ChatContext){
 const requests:{connection:string;body:Record<string,any>}[]=[]
 let authorCalls=0
 const common={prompt:'Draft a pack.',testPrompt:'Review the pack.',tools:[],hostTools:[],callTool:async()=>({content:[]}),signal:new AbortController().signal,allowConversation:true,interactive:child,critic:child?'off' as const:'on' as const,
  policy:{models:{[child?(subagent?'subagent.review':'child.review'):'critic']:{provider:choice,reasoning:engine.id==='codex'?'high':'ultra'}}},context:{chat,currentRequest:{message:'Draft a pack.',input:'Draft a pack.'},connection:{connectionId:'author',capability:engine.id==='codex'?'agent' as const:'model' as const}}}
 let session:EngineSession
 if(engine.id==='codex'){
  const run=(connection:string):AgentRun=>async(request,callbacks)=>{
   requests.push({connection,body:request})
   if(connection==='author'&&child&&authorCalls++===0){const tool=request.tools.find(t=>t.description.startsWith('delegate_tasks:')||t.name==='delegate_tasks')!;await callbacks.tool({id:'task-call',name:tool.name,arguments:{tasks:[task]}},callbacks.signal)}
   await callbacks.event({type:'message',id:'reply',text:connection==='author'?(child?'Findings received.':proposal):'Untrusted child findings.',phase:'final'})
  }
  session={...common,effort:'high',agent:{model:'author-model',run:run('author')},policyConnections:connected?{reviewer:{agent:{family,model:'review-model',tiers:{low:'low',medium:'medium',high:'medium'},run:run('reviewer')}}}:{}}
 }else{
  const wire=family==='openai'?'openai-compatible':family as 'anthropic'|'gemini'
  session={...common,thinking:normalize('ultra','openai-compatible'),model:{family:'openai-compatible',model:'author-model',call:async(_suffix,request)=>{
   requests.push({connection:'author',body:JSON.parse(request.body??'{}')});return answer('openai',child?'Findings received.':proposal,child&&authorCalls++===0)
  }},policyConnections:connected?{reviewer:{model:{family:wire,model:'review-model',capabilities:{tiers:family==='gemini'?{low:'1024',medium:'2048',high:'4096'}:{low:'low',medium:'medium',high:'high'}},call:async(_suffix,request)=>{requests.push({connection:'reviewer',body:JSON.parse(request.body??'{}')});return answer(family,'Untrusted child findings.')}}}}:{}} as AssistantSession
 }
 const events:AssistantEvent[]=[]
 for await(const event of (child?withDelegation(engine):engine).start(session))events.push(event)
 return {events,requests}
}
for(const engine of [vercel,deepagents,codex])describe(`${engine.id==='codex'?'agent ':''}engine ${engine.id} · scenarios`,()=>{
 it('12 other family, explicit ID, tier map and measurement',async()=>{
  for(const family of ['anthropic',...(engine.id==='codex'?[]:['gemini'])] as ProviderFamily[])for(const choice of ['other','reviewer']){
   const {events,requests}=await play(engine,choice,true,false,family)
   expect(events.filter(e=>e.type==='error')).toEqual([])
   expect(requests.map(r=>r.connection)).toEqual(['author','reviewer'])
   expect(events.find(e=>e.type==='critique')).toMatchObject({provider:{connection:'reviewer',family}})
   const sent=requests[1]!.body
   if(engine.id==='codex')expect(sent.effort).toBe('medium')
   else if(family==='anthropic')expect(sent.output_config).toEqual({effort:'high'})
   else expect(sent.generationConfig.thinkingConfig.thinkingBudget).toBe(4096)
   const checkpoints=events.filter(e=>e.type==='execution_checkpoint')
   expect(checkpoints.length).toBeGreaterThan(0)
   expect(checkpoints.at(-1)!.checkpoint.crossProvider).toContainEqual({role:'critic',relation:'other',author:{connection:'author',family:engine.id==='codex'?'codex':'openai'},provider:{connection:'reviewer',family}})
   if(process.env.CERTIFY_PROVIDERS)appendFileSync(process.env.CERTIFY_PROVIDERS,JSON.stringify({engine:engine.id,author:engine.id==='codex'?'codex':'openai',critic:family,policy:choice,requests:requests.length,checks:events.find(e=>e.type==='critique')?.checks.length??0})+'\n')
  }
 })
 it('same-family critic reopens its slot with the connection override at the same tier',async()=>{
  if(engine.id==='codex')return
  const {events,requests}=await play(engine,'reviewer',true,false,'openai')
  expect(events.filter(e=>e.type==='error')).toEqual([])
  expect(requests.map(r=>r.connection)).toEqual(['author','reviewer'])
  expect(requests[0]!.body.reasoning_effort).toBe('xhigh')
  expect(requests[1]!.body.reasoning_effort).toBe('high')
 })
 it('12 other with one family is refused before dispatch',async()=>{
  const {events,requests}=await play(engine,'other',false)
  expect(requests).toEqual([])
  expect(events.find(e=>e.type==='guardrail')).toMatchObject({reason:'policy refused',tool:'models.critic.provider'})
 })
 it('12 reuse never relabels another family result',async()=>{
  if(engine.id==='codex')return
  const chat=new ChatContext({scope:'providers'})
  try{
   await play(engine,'other',true,true,'anthropic',false,chat)
   const second=await play(engine,'other',true,true,'gemini',false,chat)
   expect(second.requests.some(r=>r.connection==='reviewer')).toBe(true)
   expect(second.events.filter(e=>e.type==='delegated_task').at(-1)?.task.reused).toBeUndefined()
  }finally{chat.dispose()}
 })
 it('12 a child on another family returns data and records its family',async()=>{
  for(const subagent of [false,true]){
  const {events,requests}=await play(engine,'other',true,true,'anthropic',subagent)
  expect(events.filter(e=>e.type==='error')).toEqual([])
  expect(requests.some(r=>r.connection==='reviewer')).toBe(true)
  const tasks=events.filter(e=>e.type==='delegated_task')
  expect(tasks.length).toBeGreaterThan(0)
  for(const event of tasks)expect(event.provider).toEqual({connection:'reviewer',family:'anthropic'})
  expect(tasks.at(-1)?.task).toMatchObject({state:'complete',result:'Untrusted child findings.'})
  }
 })
 it('20 qualifying failure switches once, rebuilds the request and emits the usage ledger',async()=>{
  const events:AssistantEvent[]=[],requests:{connection:string;body:string}[]=[]
  const common={prompt:'Draft a pack.',testPrompt:'Review the pack.',tools:[],hostTools:[],callTool:async()=>({content:[]}),signal:new AbortController().signal,allowConversation:true,interactive:false,critic:'off' as const,policy:{models:{author:{fallback:['backup']}}},context:{currentRequest:{message:'Draft a pack.',input:'Draft a pack.'},connection:{connectionId:'primary'}}}
  let session:EngineSession
  if(engine.id==='codex'){
   const primary:AgentRun=async(request)=>{requests.push({connection:'primary',body:JSON.stringify(request)});throw new AIFailureError({version:1,code:'unavailable',source:'agent',outcome:'unknown'})}
   const backup:AgentRun=async(request,callbacks)=>{requests.push({connection:'backup',body:JSON.stringify(request)});await callbacks.event({type:'message',id:'answer',text:proposal,phase:'final'})}
   session={...common,effort:'high',agent:{family:'codex',model:'author-model',run:primary},policyConnections:{backup:{agent:{family:'codex',model:'backup-model',tiers:{low:'low',medium:'medium',high:'medium'},run:backup}}}}
  }else{
   session={...common,thinking:normalize('ultra','openai-compatible'),model:{family:'openai-compatible',model:'author-model',call:async(_suffix,request)=>{requests.push({connection:'primary',body:request.body??''});return new Response('{}',{status:503})}},policyConnections:{backup:{model:{family:'openai-compatible',model:'backup-model',capabilities:{tiers:{low:'low',medium:'medium',high:'high'}},call:async(_suffix,request)=>{requests.push({connection:'backup',body:request.body??''});return answer('openai',proposal)}}}}} as AssistantSession
  }
  for await(const event of engine.start(session))events.push(event)
  expect(requests.map(r=>r.connection)).toEqual(['primary','backup'])
  expect(JSON.parse(requests[1]!.body)).toMatchObject({model:'backup-model',[engine.id==='codex'?'effort':'reasoning_effort']:engine.id==='codex'?'medium':'high'})
  const {model:primaryModel,effort:primaryEffort,reasoning_effort:primaryReasoning,...primaryBody}=JSON.parse(requests[0]!.body)
  const {model:backupModel,effort:backupEffort,reasoning_effort:backupReasoning,...backupBody}=JSON.parse(requests[1]!.body)
  expect(backupBody).toEqual(primaryBody)
  expect(events.find(e=>e.type==='connection_switched')).toEqual({ version: 1,type:'connection_switched',role:'author',from:'primary',to:'backup',reason:'unavailable'})
  const usage=events.filter(e=>e.type==='usage')
  expect(usage).toHaveLength(1)
  expect(events.at(-2)).toEqual(usage[0])
  const rows=usage[0]!.usage.rows
  for(const connection of ['primary','backup'])expect(rows.find(row=>row.connection===connection)).toMatchObject({bytes:expect.any(Number),[engine.id==='codex'?'invocations':'requests']:1})
  expect(rows.filter(row=>row.connection==='primary'||row.connection==='backup').every(row=>row.bytes>0)).toBe(true)
  expect(rows.find(row=>row.connection==='backup')?.model).toBe('backup-model')
  if(engine.id==='codex')expect(rows.every(row=>row.tokens===undefined)).toBe(true)
  const saved=events.filter(e=>e.type==='execution_checkpoint').at(-1)!.checkpoint
  expect(saved.failover).toEqual([{role:'author',connection:'backup',consumed:1,history:[{from:'primary',to:'backup',reason:'unavailable'}]}])
  const resumed:AssistantEvent[]=[]
  for await(const event of engine.start({...session,context:{...session.context,execution:saved}}))resumed.push(event)
  expect(resumed.filter(e=>e.type==='error')).toEqual([])
  expect(requests.map(r=>r.connection)).toEqual(['primary','backup','backup'])
  expect(resumed.filter(e=>e.type==='execution_checkpoint').at(-1)!.checkpoint.failover).toEqual(saved.failover)
  const restored=await contextualSession({...session,context:{...session.context,execution:saved}},engine.id)
  expect(restored.context.fallback('author','timeout')).toBeUndefined()
  expect(restored.context.takeNotices()).toContainEqual(refusals.items[1])
  restored.context.close()

 })
 it('20 fallback rebuilds a Gemini model path and thinking budget',async()=>{
  if(engine.id==='codex')return
  for(const path of geminiModelPaths.items){
   const requests:{suffix:string;body:Record<string,any>}[]=[],events:AssistantEvent[]=[]
   const session:AssistantSession={prompt:'Answer briefly.',testPrompt:'',tools:[],hostTools:[],callTool:async()=>({}),signal:new AbortController().signal,allowConversation:true,critic:'off',thinking:normalize('high','gemini'),
    model:{family:'gemini',model:'primary-model',call:async()=>new Response('{}',{status:503})},
    policy:{models:{author:{fallback:['backup']}}},context:{connection:{connectionId:'primary'}},
    policyConnections:{backup:{model:{family:'gemini',model:path.model,capabilities:{tiers:{low:'1024',medium:'2048',high:'4096'}},call:async(suffix,request)=>{requests.push({suffix,body:JSON.parse(request.body)});return answer('gemini','Finished.')}}}}
   }
   for await(const event of engine.start(session))events.push(event)
   expect(events.filter(e=>e.type==='error'),path.model).toEqual([])
   expect(requests,path.model).toHaveLength(1)
   expect(requests[0]!.suffix,path.model).toContain(`models/${path.segment}:`)
   expect(requests[0]!.body.generationConfig.thinkingConfig,path.model).toEqual({includeThoughts:true,thinkingBudget:4096})
   expect(events.find(e=>e.type==='usage')?.usage.rows.find(row=>row.connection==='backup')?.model,path.model).toBe(path.model)
  }
 })
 it('20 exhausted switch bound emits a refusal',async()=>{
  const events:AssistantEvent[]=[],calls:string[]=[]
  const model=(id:string)=>({family:'openai-compatible' as const,model:id,call:async()=>{calls.push(id);return new Response('{}',{status:503})}})
  const agent=(id:string)=>({family:'codex' as const,model:id,run:async()=>{calls.push(id);throw new AIFailureError({version:1,code:'unavailable',source:'agent',outcome:'unknown'})}})
  const common={prompt:'',testPrompt:'',tools:[],hostTools:[],callTool:async()=>({content:[]}),signal:new AbortController().signal,critic:'off' as const,policy:{models:{author:{fallback:['backup']}}},context:{connection:{connectionId:'primary'}}}
  const session:EngineSession=engine.id==='codex'?{...common,effort:'high',agent:agent('primary'),policyConnections:{backup:{agent:agent('backup')}}}:{...common,thinking:normalize('off','openai-compatible'),model:model('primary'),policyConnections:{backup:{model:model('backup')}}}
  for await(const event of engine.start(session))events.push(event)
  expect(calls).toEqual(['primary','backup'])
  expect(events.filter(e=>e.type==='guardrail'&&e.reason==='failover refused')).toEqual([{...refusals.items[2],version:1}])
 })
 it('20 partial usage preserves wire presence on every model family',async()=>{
  if(engine.id==='codex')return
  for(const family of ['openai','anthropic','gemini'] as const){
   const events:AssistantEvent[]=[]
   const session:AssistantSession={prompt:'Answer.',testPrompt:'',tools:[],hostTools:[],callTool:async()=>({content:[]}),signal:new AbortController().signal,allowConversation:true,interactive:true,critic:'off',thinking:normalize('off',family==='openai'?'openai-compatible':family),model:{family:family==='openai'?'openai-compatible':family,model:'author-model',call:async()=>answer(family,'Answer.')},context:{connection:{connectionId:'primary'}}}
   for await(const event of engine.start(session))events.push(event)
   expect(events.filter(e=>e.type==='error')).toEqual([])
   expect(events.find(e=>e.type==='usage')!.usage.rows.find(r=>r.source==='model')!.tokens).toEqual(partialUsage.items[0]!.rows[0]!.tokens)
  }
 })
 it('20 child.analyze carries fallback and the parent retains its usage once',async()=>{
  const events:AssistantEvent[]=[],calls:string[]=[]
  let parentCalls=0
  const durable:import('../execution/state').ExecutionCheckpoint[]=[]
  const delegate={tasks:[{title:'Analyze',objective:'Check the supplied claims.',profile:'analyze'}]}
  const model=(id:string)=>({family:'openai-compatible' as const,model:id,call:async()=>{
   calls.push(id)
   if(id.startsWith('child-')){const saved=durable.at(-1)!;expect(saved.usage!.rows.reduce((sum,row)=>sum+row.requests,0)).toBe(saved.used);expect(saved.childUsage?.length).toBe(1)}
   if(id==='child-primary')return new Response('{}',{status:504})
   if(id==='parent'&&parentCalls++===0)return new Response(JSON.stringify({id:'response',object:'chat.completion',model:id,choices:[{index:0,message:{role:'assistant',content:null,tool_calls:[{id:'task-call',type:'function',function:{name:'delegate_tasks',arguments:JSON.stringify(delegate)}}]},finish_reason:'tool_calls'}],usage:{prompt_tokens:10,completion_tokens:5}}),{headers:{'content-type':'application/json'}})
   return answer('openai','Findings received.')
  }})
  const agent=(id:string)=>({family:'codex' as const,model:id,run:async(request:any,callbacks:any)=>{
   calls.push(id)
   if(id==='child-primary')throw new AIFailureError({version:1,code:'timeout',source:'agent',outcome:'unknown'})
   if(id==='parent'&&parentCalls++===0){const tool=request.tools.find((t:any)=>t.name==='delegate_tasks'||t.description.startsWith('delegate_tasks:'));await callbacks.tool({id:'task-call',name:tool.name,arguments:delegate},callbacks.signal)}
   await callbacks.event({type:'message',id:'reply',text:'Findings received.',phase:'final'})
  }})
  const common={prompt:'Analyze the claims.',testPrompt:'',tools:[],hostTools:[],callTool:async()=>({content:[]}),signal:new AbortController().signal,allowConversation:true,interactive:true,critic:'off' as const,policy:{models:{'child.analyze':{provider:'child-primary',fallback:['child-backup']}}},context:{chat:new ChatContext(),currentRequest:{message:'Analyze the claims.',input:''},checkpoint:async(state:import('../execution/state').ExecutionCheckpoint)=>{durable.push(state)},connection:{connectionId:'parent'}}}
  const session:EngineSession=engine.id==='codex'?{...common,effort:'high',agent:agent('parent'),policyConnections:{'child-primary':{agent:agent('child-primary')},'child-backup':{agent:agent('child-backup')}}}:{...common,thinking:normalize('off','openai-compatible'),model:model('parent'),policyConnections:{'child-primary':{model:model('child-primary')},'child-backup':{model:model('child-backup')}}}
  for await(const event of withDelegation(engine).start(session))events.push(event)
  if(engine.id==='codex'){
   expect(calls).not.toContain('child-backup')
   expect(events.find(e=>e.type==='guardrail'&&e.reason==='failover refused')).toBeDefined()
  }else{
   expect(calls).toContain('child-backup')
   const usage=events.filter(e=>e.type==='usage').at(-1)!.usage
   expect(usage.rows.filter(r=>r.source==='model').reduce((sum,r)=>sum+r.requests,0)).toBe(calls.length)
   expect(usage.rows.find(r=>r.connection==='child-backup')).toMatchObject({role:'child.analyze',model:'child-backup',tokens:partialUsage.items[0]!.rows[0]!.tokens})
  }
 })
 it('20 cross-family fallback is refused before dispatch',async()=>{
  const events:AssistantEvent[]=[],requests:unknown[]=[]
  const common={prompt:'Draft a pack.',testPrompt:'Review the pack.',tools:[],hostTools:[],callTool:async()=>({content:[]}),signal:new AbortController().signal,allowConversation:true,interactive:false,critic:'off' as const,policy:{models:{author:{fallback:['backup']}}},context:{currentRequest:{message:'Draft a pack.',input:'Draft a pack.'},connection:{connectionId:'primary'}}}
  const session:EngineSession=engine.id==='codex'?{...common,effort:'high',agent:{family:'codex',model:'author',run:async r=>{requests.push(r)}},policyConnections:{backup:{agent:{family:'anthropic',model:'backup',run:async()=>{}}}}}:{...common,thinking:normalize('ultra','openai-compatible'),model:{family:'openai-compatible',model:'author',call:async(_s,r)=>{requests.push(r);return answer('openai',proposal)}},policyConnections:{backup:{model:{family:'anthropic',model:'backup',call:async()=>answer('anthropic',proposal)}}}} as AssistantSession
  for await(const event of engine.start(session))events.push(event)
  expect(requests).toEqual([])
  expect(events.find(e=>e.type==='guardrail')).toMatchObject({tool:'models.author.fallback',reason:'failover refused'})
  expect(events.filter(e=>e.type==='usage')).toHaveLength(1)
 })
 it('20 a refusal never switches',async()=>{
  const events:AssistantEvent[]=[],connections:string[]=[]
  const common={prompt:'Draft a pack.',testPrompt:'Review the pack.',tools:[],hostTools:[],callTool:async()=>({content:[]}),signal:new AbortController().signal,allowConversation:true,interactive:false,critic:'off' as const,policy:{models:{author:{fallback:['backup']}}},context:{currentRequest:{message:'Draft a pack.',input:'Draft a pack.'},connection:{connectionId:'primary'}}}
  const session:EngineSession=engine.id==='codex'?{...common,effort:'high',agent:{family:'codex',model:'author',run:async()=>{connections.push('primary');throw new AIFailureError({version:1,code:'permission',source:'agent',outcome:'refused'})}},policyConnections:{backup:{agent:{family:'codex',model:'backup',run:async()=>{connections.push('backup')}}}}}:{...common,thinking:normalize('ultra','openai-compatible'),model:{family:'openai-compatible',model:'author',call:async()=>{connections.push('primary');return new Response('{}',{status:403})}},policyConnections:{backup:{model:{family:'openai-compatible',model:'backup',call:async()=>{connections.push('backup');return answer('openai',proposal)}}}}} as AssistantSession
  for await(const event of engine.start(session))events.push(event)
  expect(connections).toEqual(['primary'])
  expect(events.filter(e=>e.type==='connection_switched')).toEqual([])
 })
 it('20 an agent switch during delegated work is refused',async()=>{
  if(engine.id!=='codex')return
  const events:AssistantEvent[]=[],connections:string[]=[]
  const session:EngineSession={prompt:'Check the pack.',testPrompt:'',tools:[],hostTools:[],callTool:async()=>({content:[]}),signal:new AbortController().signal,allowConversation:true,interactive:false,critic:'off',effort:'high',agent:{family:'codex',model:'author',run:async()=>{connections.push('primary');throw new AIFailureError({version:1,code:'unavailable',source:'agent',outcome:'unknown'})}},policy:{models:{author:{fallback:['backup']}}},policyConnections:{backup:{agent:{family:'codex',model:'backup',run:async()=>{connections.push('backup')}}}},context:{objective:'Delegated review.',currentRequest:{message:'Check the pack.',input:'Check the pack.'},connection:{connectionId:'primary'}}}
  for await(const event of engine.start(session))events.push(event)
  expect(connections).toEqual(['primary'])
  expect(events.find(e=>e.type==='guardrail')).toMatchObject({reason:'failover refused',tool:'models.author.fallback'})
  expect(events.filter(e=>e.type==='connection_switched')).toEqual([])
 })
})
