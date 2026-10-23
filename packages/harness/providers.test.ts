import {describe,it,expect} from 'vitest'
import {resolvePolicy,PolicyRefusal,failoverRefusal,type PolicyLayer,type ReasoningValue} from './policy'
import v0 from '../../contract/vectors/providers/same.json'
import v1 from '../../contract/vectors/providers/other.json'
import v2 from '../../contract/vectors/providers/explicit.json'
import v3 from '../../contract/vectors/providers/child.json'
import v4 from '../../contract/vectors/providers/subagent.json'
import v5 from '../../contract/vectors/providers/precedence.json'
import v6 from '../../contract/vectors/providers/unavailable.json'
import v7 from '../../contract/vectors/providers/critique.json'
import v8 from '../../contract/vectors/providers/failover.json'
import v9 from '../../contract/vectors/providers/tier-override.json'
import fallback from '../../contract/vectors/providers/fallback-tiers.json'
import omission from '../../contract/vectors/providers/reasoning-omission.json'
import type {ThinkingTier} from './host'
import {openThinking,normalize} from './thinking'
import {capabilitiesFor} from './capabilities'
import {critiqueEvent} from './refutation'
import {vercel} from './engines/vercel'
import {deepagents} from './engines/deepagents'
const vectors:Record<string,any>={'same':v0,'other':v1,'explicit':v2,'child':v3,'subagent':v4,'precedence':v5,'unavailable':v6,'critique':v7,'failover':v8,'tier-override':v9}
const vector=(name:string)=>vectors[name]
const table:PolicyLayer={models:{author:{model:'author',reasoning:'ultra'}}}
const host={connections:['author','reviewer'],families:{author:'openai',reviewer:'anthropic'} as const,agent:false,requests:2}
describe('ADR-0012 provider boundary',()=>{
 for(const name of ['same','other','explicit','child','subagent'])it(name,()=>{
  const v=vector(name), result=resolvePolicy(v.items[0],{},table,host)
  expect(result.members[`models.${v.role}.provider`]).toEqual({value:v.expected,layer:'run',source:'layer'})
 })
 it('defaults every declared role to same',()=>{
  const result=resolvePolicy({models:{'child.review':{reasoning:'on'},'subagent.audit':{reasoning:'off'}}},{},table,host)
  for(const role of ['author','critic','child.review','subagent.audit'])expect(result.members[`models.${role}.provider`]).toEqual({value:'author',layer:'table',source:'layer'})
 })
 it('a model connection does not override the provider default',()=>{
  const result=resolvePolicy({models:{critic:{model:'reviewer'}}},{},table,host)
  expect(result.members['models.critic.provider']).toEqual({value:'author',layer:'table',source:'layer'})
 })
 it('uses run over project over table independently per member',()=>{
  const [run,project,defaults]=vector('precedence').items
  expect(resolvePolicy(run,project,{...table,models:{...table.models,...defaults.models}},host).members['models.critic.provider']).toEqual({value:'author',layer:'run',source:'layer'})
  expect(resolvePolicy({},project,table,host).members['models.critic.provider']).toEqual({value:'reviewer',layer:'project',source:'layer'})
 })
 it('refuses other without a connected other family, naming the role',()=>{
  try{resolvePolicy(vector('other').items[0],{},table,{...host,connections:['author']});expect.fail('other fell back to same')}
  catch(e){expect(e).toBeInstanceOf(PolicyRefusal);expect((e as PolicyRefusal).event()).toEqual(vector('unavailable').items[0])}
 })
 it('records the family in critiques',()=>{
  const event=vector('critique').items[0]
  expect(critiqueEvent({refuted:false,checks:[],text:'No runtime checks.',modelText:''},event.provider)).toEqual(event)
 })
 it('records why cross-family failover is refused',()=>{
  expect(failoverRefusal('critic',{connection:'author',family:'openai'},{connection:'reviewer',family:'anthropic'})).toEqual(vector('failover').items[0])
  expect(failoverRefusal('critic',{connection:'author',family:'openai'},{connection:'other-account',family:'openai'})).toBeUndefined()
 })
 it('reads per-connection tier overrides',()=>{
  const [anthropic,gemini]=vector('tier-override').items
  for(const [family,capabilities,path,want] of [['anthropic',anthropic,'output_config','high'],['gemini',gemini,'thinkingConfig',4096]] as const){
   const model={family,model:'test',call:async()=>new Response(),capabilities}
   const slot=openThinking({model,thinking:normalize('ultra',family)},capabilitiesFor(model).reasoning)
   expect(slot.members()?.[path]).toMatchObject(family==='anthropic'?{effort:want}:{thinkingBudget:want})
  }
 })
})

for(const c of fallback.cases)it(`fallback map ${c.record} ${c.tier}`,()=>{
 const record=fallback.items[c.record]!
 const family=record.family as 'anthropic'|'gemini'
 const model={family,model:'test',call:async()=>new Response(),capabilities:{...record,family}}
 const slot=openThinking({model,thinking:normalize(c.tier as ThinkingTier,family)},capabilitiesFor(model).reasoning)
 expect(slot.refused(400,family==='anthropic'?'thinking':'thinkingBudget').kind).toBe('retry')
 expect(slot.members()).toEqual(c.members)
})

it('refuses a non-integer fallback token budget with the shared message',()=>{
 expect(()=>capabilitiesFor({family:'anthropic',model:'test',capabilities:fallback.refused as any})).toThrow(fallback.error)
})

import {contextualSession} from './context/session'
import {managedContext} from './context/managed'
import type {AssistantSession,AssistantEvent,Engine} from './engine'
for(const role of ['author','critic','child.review'])it(`resume refuses a rebound ${role} before dispatch and preserves the report`,async()=>{
 const model={family:'openai-compatible' as const,model:'connected',call:async()=>new Response()}
 const session:AssistantSession={prompt:'Continue.',testPrompt:'Review.',tools:[],hostTools:[],callTool:async()=>({content:[]}),signal:new AbortController().signal,
  model,thinking:normalize('high','openai-compatible'),policy:{models:{[role]:{provider:'saved'}}},policyConnections:{saved:{model}},
  context:{currentRequest:{message:'Continue.',input:'Continue.'}}}
 const ready=await contextualSession(session)
 ready.context.recordProvider(role)
 const saved=ready.context.execution.snapshot('stopped')
 ready.context.close()
 const resumed={...session,context:{...session.context,execution:saved},policyConnections:{saved:{model:{...model,family:'anthropic' as const}}}}
 let dispatched=false
 const events:AssistantEvent[]=[]
 for await(const event of managedContext(resumed,async function*(){dispatched=true;yield {type:'end'} as const}))events.push(event)
 expect(dispatched).toBe(false)
 expect(events.find(e=>e.type==='guardrail')).toMatchObject({reason:'policy refused',tool:`models.${role}.provider`,detail:expect.stringContaining('cross-family continuation')})
 const stopped=events.find(e=>e.type==='execution_checkpoint')?.checkpoint
 expect(stopped?.status).toBe('stopped')
 expect(stopped?.crossProvider).toEqual(saved.crossProvider!.map(row=>({...row,continued:false})))
 expect(saved.crossProvider!.every(row=>row.continued===undefined)).toBe(true)
 // Same-family continuation still opens.
 const same=await contextualSession({...session,context:{...session.context,execution:saved}})
 same.context.close()
})

const omissionProposal='```json\n{"proposal":{"document":{"id":"pack"},"unknowns":[]}}\n```'
function omissionAnswer(family:'openai-compatible'|'gemini',text:string){
 const body=family==='gemini'
  ?{candidates:[{content:{role:'model',parts:[{text}]},finishReason:'STOP'}],usageMetadata:{promptTokenCount:10,candidatesTokenCount:5,totalTokenCount:15}}
  :{id:'response',object:'chat.completion',model:'fixture',choices:[{index:0,message:{role:'assistant',content:text},finish_reason:'stop'}],usage:{prompt_tokens:10,completion_tokens:5,total_tokens:15}}
 return new Response(JSON.stringify(body),{headers:{'content-type':'application/json'}})
}
const omissionSession=(family:'openai-compatible'|'gemini',record:unknown,call:AssistantSession['model']['call'],patch:Partial<AssistantSession>={}):AssistantSession=>({prompt:'Answer briefly.',testPrompt:'',tools:[],hostTools:[],callTool:async()=>({content:[]}),signal:new AbortController().signal,allowConversation:true,critic:'off',model:{family,model:'author-model',capabilities:record as Partial<import('./capabilities').ModelCapabilities>,call},...patch})
const omissionBody=(body:Record<string,any>,family:'openai-compatible'|'gemini')=>family==='gemini'?body.generationConfig:body

for(const engine of [vercel,deepagents] as Engine[])describe(`${engine.id} reasoning omission request bytes`,()=>{
 it('does not restore a table default that discovery dropped, including after resume',async()=>{
  for(const row of omission.dropped.filter(row=>row.capability==='model')){
   const record=omission.items[row.record!]!,family=record.family==='openai'?'openai-compatible' as const:'gemini' as const
   const makeSession=(bodies:Record<string,any>[])=>{
    const call=async(_suffix:string,request:import('./engine').ModelRequest)=>{bodies.push(JSON.parse(request.body));const text=bodies.length===1&&row.role==='critic'?omissionProposal:row.role==='critic'?'No check refuted it.':'Finished.';return omissionAnswer(family,text)}
    return omissionSession(family,record,call,{thinking:normalize(row.raw as ThinkingTier,family),prompt:row.role==='critic'?'Draft a pack.':'Answer briefly.',testPrompt:row.role==='critic'?'Refute it.':'',allowConversation:row.role!=='critic',critic:row.role==='critic'?'on':'off',
     policy:{models:{[row.role]:{discovered:{reasoning:{supported:[...row.supported] as ReasoningValue[]}}}}},context:{currentRequest:{message:'Draft a pack.',input:'Draft a pack.'},connection:{connectionId:'author'}}})
   }
   const seed=makeSession([]),opened=await contextualSession(seed,engine.id),saved=opened.context.execution.snapshot('stopped');opened.context.close()
   const attempts:Record<string,any>[][]=[]
   for(const execution of [undefined,saved]){
    const bodies:Record<string,any>[]=[],session=makeSession(bodies)
    if(execution)session.context={...session.context,execution}
    const events:AssistantEvent[]=[];for await(const event of engine.start(session))events.push(event)
    expect(events.filter(event=>event.type==='error'),row.name).toEqual([])
    const body=bodies[row.role==='critic'?1:0]!
    expect(body,row.name).not.toHaveProperty(row.requestPath)
    expect(events.find(event=>event.type==='policy_resolved'),row.name).toMatchObject({members:{[`models.${row.role}.reasoning`]:row.resolved}})
    if(row.decisionPath)expect(events.find(event=>event.type==='critic_decided'),row.name).not.toHaveProperty(row.decisionPath)
    expect(events.filter(event=>event.type==='execution_checkpoint').at(-1)?.checkpoint.policy?.[`models.${row.role}.reasoning`],row.name).toEqual(row.resolved)
    attempts.push(bodies)
   }
   expect(JSON.stringify(attempts[1]),row.name).toBe(JSON.stringify(attempts[0]))
  }
 })
 it('omits the author member on OpenAI and Gemini',async()=>{
  for(const one of omission.cases.filter(one=>one.phase==='author')){
   const record=omission.items[one.record]!,family=record.family==='openai'?'openai-compatible' as const:'gemini' as const,bodies:Record<string,any>[]=[]
   const session=omissionSession(family,record,async(_suffix,request)=>{bodies.push(JSON.parse(request.body));return omissionAnswer(family,'Finished.')})
   const events:AssistantEvent[]=[];for await(const event of engine.start(session))events.push(event)
   expect(events.filter(event=>event.type==='error'),one.name).toEqual([]);expect(bodies,one.name).toHaveLength(1);expect(omissionBody(bodies[0]!,family),one.name).not.toHaveProperty(one.absent)
  }
 })
 it('omits the critic member on OpenAI and Gemini',async()=>{
  for(const one of omission.cases.filter(one=>one.phase==='critic')){
   const record=omission.items[one.record]!,family=record.family==='openai'?'openai-compatible' as const:'gemini' as const,bodies:Record<string,any>[]=[]
   const call=async(_suffix:string,request:import('./engine').ModelRequest)=>{bodies.push(JSON.parse(request.body));return omissionAnswer(family,bodies.length===1?omissionProposal:'No check refuted it.')}
   const session=omissionSession(family,record,call,{prompt:'Draft a pack.',testPrompt:'Refute it.',allowConversation:false,critic:'on',context:{connection:{connectionId:'author'}},policy:{models:{critic:{provider:'reviewer'}}},policyConnections:{reviewer:{model:{family,model:'critic-model',capabilities:record as Partial<import('./capabilities').ModelCapabilities>,call}}}})
   const events:AssistantEvent[]=[];for await(const event of engine.start(session))events.push(event)
   expect(events.filter(event=>event.type==='error'),one.name).toEqual([]);expect(bodies.length,one.name).toBeGreaterThanOrEqual(2);expect(omissionBody(bodies[1]!,family),one.name).not.toHaveProperty(one.absent)
  }
 })
 it('keeps the member absent while rebuilding a fallback request',async()=>{
  for(const one of omission.cases.filter(one=>one.phase==='fallback')){
   const record=omission.items[one.record]!,family=record.family==='openai'?'openai-compatible' as const:'gemini' as const,bodies:Record<string,any>[]=[]
   const session=omissionSession(family,record,async()=>new Response('{}',{status:503}),{model:{family,model:'primary-model',capabilities:record as Partial<import('./capabilities').ModelCapabilities>,call:async()=>new Response('{}',{status:503})},policy:{models:{author:{fallback:['backup']}}},context:{connection:{connectionId:'primary'}},policyConnections:{backup:{model:{family,model:'backup-model',capabilities:record as Partial<import('./capabilities').ModelCapabilities>,call:async(_suffix,request)=>{bodies.push(JSON.parse(request.body));return omissionAnswer(family,'Finished.')}}}}})
   const events:AssistantEvent[]=[];for await(const event of engine.start(session))events.push(event)
   expect(events.filter(event=>event.type==='error'),one.name).toEqual([]);expect(bodies,one.name).toHaveLength(1);expect(omissionBody(bodies[0]!,family),one.name).not.toHaveProperty(one.absent)
  }
 })
})
