import {describe,it,expect} from 'vitest'
import {resolvePolicy,PolicyRefusal,type PolicyLayer} from './policy'
import precedence from '../../contract/vectors/policy/precedence.json'
import alias from '../../contract/vectors/policy/alias.json'
import schema from './contract/policy-schema.json'
import canonical from '../../contract/schemas/policy.json'
import runEventSchema from '../../contract/schemas/run-event.json'
import {argumentProblem} from './sourceArguments'

const table:PolicyLayer={models:{author:{model:'connected',reasoning:'off'}}}
const host={connections:['connected'],agent:false,requests:16}
describe('ADR-0011 policy table',()=>{
 it('ships the closed contract schema unchanged',()=>expect(schema).toEqual(canonical))
 for(const [i,c] of precedence.cases.entries())it(`resolves every section from ${c.name}`,()=>{
  const result=resolvePolicy(c.run as PolicyLayer,c.project as PolicyLayer,c.table as PolicyLayer,host)
  expect(result.members).toMatchObject(precedence.items[i]!)
  expect(Object.values(result.members).every(m=>['run','project','table'].includes(m.layer))).toBe(true)
 })
 it('reads budget.json as policy.budget for one release',()=>{
  const result=resolvePolicy({}, {}, {...alias.items[0],readBytes:512,models:table.models},host)
  expect(result.members['budget.readBytes']).toEqual({value:512,layer:'table'})
  expect(resolvePolicy({}, {}, {...alias.items[0],models:table.models,budget:{readBytes:512}},host).members['budget.readBytes']).toEqual({value:512,layer:'table'})
 })
 it('warns at the threshold and refuses past the period budget',()=>{
  const project:PolicyLayer={pace:{requestsPerDay:10,warnAt:0.8}}
  expect(resolvePolicy({},project,table,{...host,paceUsed:{requestsPerDay:7}}).warning).toBeUndefined()
  expect(resolvePolicy({},project,table,{...host,paceUsed:{requestsPerDay:8}}).warning).toContain('pace.requestsPerDay (project)')
  expect(()=>resolvePolicy({},project,table,{...host,paceUsed:{requestsPerDay:11}})).toThrow('pace.requestsPerDay (project)')
  try{resolvePolicy({},project,table,{...host,paceUsed:{requestsPerDay:11}})}catch(e){expect(e).toBeInstanceOf(PolicyRefusal);expect((e as PolicyRefusal).failure.code).toBe('pace exceeded')}
 })
 it('refuses exhausted period allowances, including zero, before starting work',()=>{
  for(const key of ['requestsPerDay','invocationsPerDay','bytesPerDay'] as const)for(const limit of [0,10]){
   try { resolvePolicy({}, {pace:{[key]:limit}}, table, {...host,paceUsed:{[key]:limit}}); expect.fail('exhausted pace admitted') }
   catch(error){ expect(error).toBeInstanceOf(PolicyRefusal); expect((error as PolicyRefusal).failure.code).toBe('pace exceeded'); expect((error as PolicyRefusal).member).toBe(`pace.${key}`) }
  }
 })
 it('holds the child and subagent ceiling unless the project authorizes it',()=>{
  for(const role of ['child.review','subagent.audit']){
   const raised:PolicyLayer={models:{[role]:{reasoning:'ultra'}}}
   expect(()=>resolvePolicy(raised,{},table,host)).toThrow('tier exceeds')
   expect(resolvePolicy({},raised,table,host).members[`models.${role}.reasoning`]).toEqual({value:'ultra',layer:'project'})
   expect(()=>resolvePolicy(raised,{models:{[role]:{reasoning:'on'}}},table,host)).toThrow('tier exceeds')
  }
 })
 it('inherits the critic floor and permits only an explicit lower tier',()=>{
  const author:PolicyLayer={models:{author:{reasoning:'ultra'}}}
  expect(resolvePolicy(author,{},table,host).members['models.critic.reasoning']?.value).toBe('ultra')
  expect(resolvePolicy({...author,models:{...author.models,critic:{reasoning:'off'}}},{},table,host).members['models.critic.reasoning']).toEqual({value:'off',layer:'run'})
 })
 it('refuses missing defaults, unconnected models, unknown members and invalid tiers',()=>{
  expect(()=>resolvePolicy({},{},{},host)).toThrow('no default')
  expect(()=>resolvePolicy({models:{critic:{model:'missing'}}},{},table,host)).toThrow('models.critic.model (run)')
  expect(()=>resolvePolicy({budget:{unlimited:1}},{},table,host)).toThrow('unknown policy member')
  expect(()=>resolvePolicy({models:{author:{reasoning:'max'}}},{},table,host)).toThrow('tier is not supported')
  expect(()=>resolvePolicy({concurrency:{children:2}},{},{models:{author:{model:'connected',reasoning:'low'}}},{...host,agent:true})).toThrow('agent children are sequential')
 })
 it('uses task-class tiers without replacing a more specific run tier',()=>{
  const project:PolicyLayer={taskClass:'boundary',models:{taskClasses:{boundary:'ultra'}}}
  expect(resolvePolicy({},project,table,host).members['models.author.reasoning']).toEqual({value:'ultra',layer:'project'})
  expect(resolvePolicy({models:{author:{reasoning:'on'}}},project,table,host).members['models.author.reasoning']).toEqual({value:'on',layer:'run'})
 })
 it('detaches host layers from the resolved record',()=>{
  const layer:PolicyLayer={budget:{readBytes:512}}
  const resolved=resolvePolicy(layer,{},table,host)
  layer.budget!.readBytes=2048
  expect(resolved.members['budget.readBytes']).toEqual({value:512,layer:'run'})
 })
 it('admits only connected same-family fallbacks and keeps their order',()=>{
  const same={connections:['connected','backup-1','backup-2'],families:{connected:'openai' as const,'backup-1':'openai' as const,'backup-2':'openai' as const},agent:false,requests:16}
  const resolved=resolvePolicy({models:{author:{fallback:['backup-1','backup-2']}}},{},table,same)
  expect(resolved.members['models.author.fallback']).toEqual({value:['backup-1','backup-2'],layer:'run'})
  expect(()=>resolvePolicy({models:{author:{fallback:['other']}}},{},table,{...same,connections:[...same.connections,'other'],families:{...same.families,other:'anthropic'}})).toThrow('cross-family fallback is refused')
  expect(()=>resolvePolicy({models:{author:{fallback:['missing']}}},{},table,same)).toThrow('fallback connection is not available')
 })
 it('emits schema-valid failover refusals for empty, missing, oversized and cross-family entries',()=>{
  const same={connections:['connected','other'],families:{connected:'openai' as const,other:'anthropic' as const},agent:false,requests:16}
  for(const fallback of ['', 'missing', 'x'.repeat(129), 'other']){
   try{resolvePolicy({models:{author:{fallback:[fallback]}}},{},table,same);expect.fail('malformed fallback admitted')}
   catch(error){expect(error).toBeInstanceOf(PolicyRefusal);const event=(error as PolicyRefusal).event();expect(event.reason).toBe('failover refused');expect(argumentProblem(runEventSchema.$defs.guardrail,event)).toBeUndefined()}
  }
 })
})
