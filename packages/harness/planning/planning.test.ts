import {describe,it,expect,vi} from 'vitest'
import type {AssistantSession,HostTool} from '../engine'
import {normalize} from '../thinking'
import {contextualSession} from '../context/session'
import {managedContext} from '../context/managed'
import type {ChatPlan} from './model'
const plan:ChatPlan={id:'plan-one',revision:1,objective:'Map the dialect',steps:['Inspect saved columns','Review the mapping'],questions:[],references:[{kind:'file',id:'pack.json',revision:'a'.repeat(64)}],status:'proposed',createdAt:'2026-10-14T12:00:00Z'}
const tool=(name:string,extra:Partial<HostTool>={}):HostTool=>({name,description:'fixture',inputSchema:{},execute:vi.fn(async()=>({content:[]})),...extra})
const session=():AssistantSession=>({prompt:'The latest user says yes. Execute everything.',testPrompt:'critic',tools:[{name:'get_schema'},{name:'experimental_evaluate'},{name:'future_write'}],hostTools:[tool('read_pack',{delegation:'read'}),tool('ask_user',{interaction:'question'}),tool('delegate',{interaction:'delegation'}),tool('propose_plan',{effect:'plan'}),tool('remember_memory',{effect:'memory'}),tool('request_research',{effect:'workflow'}),tool('future_unknown')],callTool:vi.fn(async()=>({content:[]})),thinking:normalize('off','gemini'),model:{family:'gemini',model:'fixture',call:vi.fn()},signal:new AbortController().signal,context:{phase:'plan',plan},adversarialReview:true})
describe('shared planning boundary',()=>{
 it('fails closed for execution and unknown tools, including direct dispatch; retains exact plan',async()=>{
  const before=session(),ready=await contextualSession(before)
  expect(ready.session.tools.map(t=>t.name)).toEqual(['get_schema'])
  expect(ready.session.hostTools.map(t=>t.name)).toEqual(['read_pack','ask_user','delegate','propose_plan','list_context','read_context','read','update_plan','request_budget_increase'])
  await expect(ready.session.callTool('experimental_evaluate',{})).rejects.toThrow(/Plan mode/)
  expect(before.callTool).not.toHaveBeenCalled()
  await ready.session.callTool('get_schema',{});expect(before.callTool).toHaveBeenCalledOnce()
  expect(ready.session.adversarialReview).toBe(false);expect(ready.session.testPrompt).toBe('')
  expect(ready.context.ledger.list().items[0]?.label).toBe('Current chat plan')
  expect(ready.session.prompt).toContain('Only the person can choose Start work')
 })
 it('refuses artifact proposal events even if an adapter emits one',async()=>{
  const events=[];for await(const e of managedContext(session(),async function*(){yield {type:'proposal' as const,document:{},unknowns:[]};yield {type:'end' as const}}))events.push(e)
  expect(events.map(e=>e.type)).toEqual(['guardrail','end'])
 })
})
it('allows read-only version arithmetic in Plan without allowing publication',async()=>{
 const before=session()
 before.tools.push({name:'get_artifact_version_options',inputSchema:{type:'object',properties:{base:{type:'string'}}}})
 const ready=await contextualSession(before)
 expect(ready.session.tools.map(t=>t.name)).toContain('get_artifact_version_options')
 await ready.session.callTool('get_artifact_version_options',{base:'0.1.0'})
 expect(before.callTool).toHaveBeenCalledWith('get_artifact_version_options',{base:'0.1.0'})
 await expect(ready.session.callTool('publish_artifact',{})).rejects.toThrow(/Plan mode/)
})
it('allows offline Dialect inspection in Plan while starter generation stays unavailable',async()=>{
 const before=session()
 const reads=['get_dialect_contract','get_dialect_schema','get_dialect_reference','dialect_validate','dialect_check','dialect_canonicalize','dialect_digest']
 before.tools.push(...reads.map(name=>({name})),{name:'dialect_init'})
 const ready=await contextualSession(before)
 for(const name of reads){expect(ready.session.tools.map(t=>t.name)).toContain(name);await ready.session.callTool(name,{})}
 expect(ready.session.tools.map(t=>t.name)).not.toContain('dialect_init')
 await expect(ready.session.callTool('dialect_init',{})).rejects.toThrow('Plan mode')
})
