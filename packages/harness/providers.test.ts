import {describe,it,expect} from 'vitest'
import {resolvePolicy,PolicyRefusal,failoverRefusal,type PolicyLayer} from './policy'
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
import type {ThinkingTier} from './host'
import {openThinking,normalize} from './thinking'
import {capabilitiesFor} from './capabilities'
import {critiqueEvent} from './refutation'
const vectors:Record<string,any>={'same':v0,'other':v1,'explicit':v2,'child':v3,'subagent':v4,'precedence':v5,'unavailable':v6,'critique':v7,'failover':v8,'tier-override':v9}
const vector=(name:string)=>vectors[name]
const table:PolicyLayer={models:{author:{model:'author',reasoning:'ultra'}}}
const host={connections:['author','reviewer'],families:{author:'openai',reviewer:'anthropic'} as const,agent:false,requests:2}
describe('ADR-0012 provider boundary',()=>{
 for(const name of ['same','other','explicit','child','subagent'])it(name,()=>{
  const v=vector(name), result=resolvePolicy(v.items[0],{},table,host)
  expect(result.members[`models.${v.role}.provider`]).toEqual({value:v.expected,layer:'run'})
 })
 it('defaults every declared role to same',()=>{
  const result=resolvePolicy({models:{'child.review':{reasoning:'on'},'subagent.audit':{reasoning:'off'}}},{},table,host)
  for(const role of ['author','critic','child.review','subagent.audit'])expect(result.members[`models.${role}.provider`]).toEqual({value:'author',layer:'table'})
 })
 it('a model connection does not override the provider default',()=>{
  const result=resolvePolicy({models:{critic:{model:'reviewer'}}},{},table,host)
  expect(result.members['models.critic.provider']).toEqual({value:'author',layer:'table'})
 })
 it('uses run over project over table independently per member',()=>{
  const [run,project,defaults]=vector('precedence').items
  expect(resolvePolicy(run,project,{...table,models:{...table.models,...defaults.models}},host).members['models.critic.provider']).toEqual({value:'author',layer:'run'})
  expect(resolvePolicy({},project,table,host).members['models.critic.provider']).toEqual({value:'reviewer',layer:'project'})
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

import {contextualSession} from './context/session'
import {managedContext} from './context/managed'
import type {AssistantSession,AssistantEvent} from './engine'
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
