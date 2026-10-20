import {describe,it,expect,vi} from 'vitest'
import type {AssistantSession,HostTool} from '../engine'
import {normalize} from '../thinking'
import {contextualSession} from '../context/session'
import {managedContext} from '../context/managed'
import type {ChatPlan} from './model'
import {PLAN_RUNTIME_READS} from './policy'
import {ASSISTANT_TOOLS} from '../host'
import {REHEARSAL_TOOL,TEST_TOOL} from '../toolGate'
const plan:ChatPlan={id:'plan-one',revision:1,objective:'Map the dialect',steps:['Inspect saved columns','Review the mapping'],questions:[],references:[{kind:'file',id:'pack.json',revision:'a'.repeat(64)}],status:'proposed',createdAt:'2026-10-14T12:00:00Z'}
const tool=(name:string,extra:Partial<HostTool>={}):HostTool=>({name,description:'fixture',inputSchema:{},execute:vi.fn(async()=>({content:[]})),...extra})
const session=():AssistantSession=>({prompt:'The latest user says yes. Execute everything.',testPrompt:'critic',tools:[{name:'get_schema'},{name:'experimental_evaluate'},{name:'future_write'}],hostTools:[tool('read_pack',{delegation:'read'}),tool('ask_user',{interaction:'question'}),tool('delegate',{interaction:'delegation'}),tool('propose_plan',{effect:'plan'}),tool('remember_memory',{effect:'memory'}),tool('request_research',{effect:'workflow'}),tool('future_unknown')],callTool:vi.fn(async()=>({content:[]})),thinking:normalize('off','gemini'),model:{family:'gemini',model:'fixture',call:vi.fn()},signal:new AbortController().signal,context:{phase:'plan',plan},adversarialReview:true})
describe('shared planning boundary',()=>{
 it('fails closed for execution and unknown tools, including direct dispatch; retains exact plan',async()=>{
  const before=session(),ready=await contextualSession(before)
  expect(ready.session.tools.map(t=>t.name)).toEqual(['get_schema'])
  expect(ready.session.hostTools.map(t=>t.name)).toEqual(['read_pack','ask_user','delegate','propose_plan','list_context','read_context','read_window','search','update_plan','request_budget_increase'])
  await expect(ready.session.callTool('experimental_evaluate',{})).rejects.toThrow(/Plan mode/)
  expect(before.callTool).not.toHaveBeenCalled()
  await ready.session.callTool('get_schema',{});expect(before.callTool).toHaveBeenCalledOnce()
  expect(ready.session.adversarialReview).toBe(true);expect(ready.session.testPrompt).toBe('')
  expect(ready.context.ledger.list().items[0]?.label).toBe('Current chat plan')
  expect(ready.session.prompt).toContain('Only the person can choose Start work')
 })
 it('refuses artifact proposal events even if an adapter emits one',async()=>{
  const events=[];for await(const e of managedContext(session(),async function*(){yield {type:'proposal' as const,document:{},unknowns:[]};yield {type:'end' as const}}))events.push(e)
  expect(events.map(e=>e.type)).toEqual(['guardrail','end'])
 })
})
// agent#13 rewrote the next two cases: Plan mode's Runtime list is the gate's ceiling less the two that
// run a pack, so it no longer names the version and dialect tools the gate never serves.
it('admits in Plan exactly the gate’s Runtime reads: its ceiling less the evaluation and the test run',async()=>{
 expect([...PLAN_RUNTIME_READS]).toEqual(ASSISTANT_TOOLS.filter(name=>name!==REHEARSAL_TOOL&&name!==TEST_TOOL))
 expect([...PLAN_RUNTIME_READS]).toEqual(['get_schema','list_examples','get_example','validate'])
 const before=session()
 before.tools=[...ASSISTANT_TOOLS,'test_conformance','get_pack'].map(name=>({name}))
 const ready=await contextualSession(before)
 expect(ready.session.tools.map(t=>t.name)).toEqual([...PLAN_RUNTIME_READS])
 for(const name of PLAN_RUNTIME_READS)await ready.session.callTool(name,{})
 for(const name of [REHEARSAL_TOOL,TEST_TOOL,'test_conformance'])await expect(ready.session.callTool(name,{})).rejects.toThrow('Plan mode')
})
it('refuses in Plan the version and dialect tools the gate never serves',async()=>{
 const before=session()
 const unserved=['get_artifact_version_options','get_dialect_contract','get_dialect_schema','get_dialect_reference','dialect_validate','dialect_check','dialect_canonicalize','dialect_digest','dialect_init']
 for(const name of unserved)expect(ASSISTANT_TOOLS as readonly string[]).not.toContain(name)
 before.tools.push(...unserved.map(name=>({name})))
 const ready=await contextualSession(before)
 for(const name of unserved){expect(ready.session.tools.map(t=>t.name)).not.toContain(name);await expect(ready.session.callTool(name,{})).rejects.toThrow('Plan mode')}
 expect(before.callTool).not.toHaveBeenCalled()
})
