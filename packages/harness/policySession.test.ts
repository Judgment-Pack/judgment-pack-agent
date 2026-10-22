import {describe,it,expect,vi} from 'vitest'
import {contextualSession} from './context/session'
import {codex} from './engines/codex'
import {withDelegation} from './delegation'
import type {AgentRun} from './agent'
import type {AgentAssistantSession,AssistantEvent} from './engine'
import collisions from '../../contract/vectors/policy/legacy-collisions.json'

const base=():AgentAssistantSession=>({prompt:'Draft a pack.',testPrompt:'Review the pack.',tools:[],hostTools:[],callTool:vi.fn(),signal:new AbortController().signal,allowConversation:true,agent:{model:'connected',run:vi.fn()}})
describe('ADR policy table review',()=>{
 it('omits an omitted agent effort and carries model-default source into the event and checkpoint',async()=>{
  const sent:Record<string,unknown>[]=[]
  const session={...base(),critic:'off' as const,context:{currentRequest:{message:'Draft a pack.',input:'Draft a pack.'}},agent:{model:'connected',run:vi.fn(async(request,callbacks)=>{sent.push({...request});await callbacks.event({type:'message',id:'done',text:'Done.',phase:'final'})})}}
  const events:AssistantEvent[]=[]
  for await(const event of codex.start(session))events.push(event)
  expect(sent).toHaveLength(1)
  expect(sent[0]).not.toHaveProperty('effort')
  expect(events.find(e=>e.type==='policy_resolved')).toMatchObject({members:{'models.author.reasoning':{layer:'table',source:'model-default'}}})
  expect(events.filter(e=>e.type==='execution_checkpoint').at(-1)).toMatchObject({checkpoint:{policy:{'models.author.reasoning':{layer:'table',source:'model-default'}}}})
  const saved=(events.find(e=>e.type==='execution_checkpoint') as Extract<AssistantEvent,{type:'execution_checkpoint'}>).checkpoint
  const resumed:Record<string,unknown>[]=[]
  const resumedSession={...session,context:{...session.context,execution:saved},agent:{model:'connected',run:vi.fn(async(request,callbacks)=>{resumed.push({...request});await callbacks.event({type:'message',id:'done',text:'Done.',phase:'final'})})}}
  const resumedEvents:AssistantEvent[]=[]
  for await(const event of codex.start(resumedSession))resumedEvents.push(event)
  expect(JSON.stringify(resumed[0])).toBe(JSON.stringify(sent[0]))
  expect(resumedEvents.find(e=>e.type==='policy_resolved')).toMatchObject({members:{'models.author.reasoning':{layer:'table',source:'model-default'}}})
  expect(resumedEvents.filter(e=>e.type==='execution_checkpoint').at(-1)).toMatchObject({checkpoint:{policy:{'models.author.reasoning':{layer:'table',source:'model-default'}}}})
 })
 it('uses a discovered default, but refuses an explicit unsupported none before generation',async()=>{
  const discovered={reasoning:{default:'high' as const,supported:['low','medium','high'] as const}}
  const run=vi.fn<AgentRun>(async(request,callbacks)=>{expect(request.effort).toBe('high');await callbacks.event({type:'message',id:'done',text:'Done.',phase:'final'})})
  const admitted={...base(),critic:'off' as const,policy:{models:{author:{discovered:{reasoning:{...discovered.reasoning,supported:[...discovered.reasoning.supported]}}}}},agent:{model:'connected',run}}
  const events:AssistantEvent[]=[]
  for await(const event of codex.start(admitted))events.push(event)
  expect(run).toHaveBeenCalledTimes(1)
  expect(events.find(e=>e.type==='policy_resolved')).toMatchObject({members:{'models.author.reasoning':{value:'high',source:'discovered'}}})
  const refusedRun=vi.fn<AgentRun>()
  const refused:AgentAssistantSession={...base(),policy:{models:{author:{reasoning:'none',discovered:{reasoning:{default:'high',supported:['low','medium','high']}}}}},agent:{model:'connected',run:refusedRun}}
  const refusedEvents:AssistantEvent[]=[]
  for await(const event of codex.start(refused))refusedEvents.push(event)
  expect(refusedRun).not.toHaveBeenCalled()
  expect(refusedEvents.find(e=>e.type==='guardrail')).toMatchObject({tool:'models.author.reasoning',action:'refused',reason:'policy refused',detail:expect.stringContaining('model "connected"')})
  expect(refusedEvents.find(e=>e.type==='error')).toMatchObject({failure:{code:'configuration',outcome:'not-started'},message:expect.stringContaining('reasoning value "none" is not supported by model "connected"')})
 })
 for(const [i,input] of collisions.items.entries())it(`legacy collision ${i}: record and agent wire agree`,async()=>{
  const expected=collisions.expected[i]!
  const sent:{phase:string;effort:unknown}[]=[]
  const run:AgentRun=async(request,callbacks)=>{
   sent.push({phase:request.phase,effort:request.effort})
   const text=request.phase==='critic'?'No counterexample found.':'```json\n{"proposal":{"document":{"id":"pack"},"unknowns":[]}}\n```'
   await callbacks.event({type:'message',id:'reply',text,phase:'final'})
  }
  const session={...base(),...input,...collisions.cases[i],critic:'on',agent:{model:'connected',run}} as AgentAssistantSession
  const opened=await contextualSession(session,'codex')
  expect(opened.context.resolvedPolicy['models.author.reasoning']?.value).toBe(expected.author)
  expect(opened.context.resolvedPolicy['models.critic.reasoning']).toEqual({value:expected.critic,layer:'run',source:'layer'})
  expect(opened.session.effort).toBe(expected.author)
  opened.context.close()
  const events:AssistantEvent[]=[]
  for await(const event of codex.start(session))events.push(event)
  expect(events.filter(e=>e.type==='error')).toEqual([])
  expect(sent).toEqual([{phase:'author',effort:expected.author},{phase:'critic',effort:expected.critic}])
  expect(events.find(e=>e.type==='critic_decided')).toMatchObject({reasoning:expected.critic})
  expect(events.find(e=>e.type==='policy_resolved')).toMatchObject({members:{'models.author.reasoning':{value:expected.author},'models.critic.reasoning':{value:expected.critic}}})
 })
 it('starts withDelegation(codex) with valid agent tiers and the real connection',async()=>{
  const original=vi.fn<AgentRun>()
  const selected=vi.fn<AgentRun>(async(request,callbacks)=>{
   expect(request.effort).toBe('low')
   await callbacks.event({type:'message',id:'done',text:'Done.',phase:'final'})
  })
  const session:AgentAssistantSession={...base(),interactive:true,agent:{model:'connected',run:original},
   policy:{models:{author:{model:'reviewer',provider:'reviewer',reasoning:'low'}}},policyConnections:{reviewer:{agent:{model:'review-model',run:selected}}}}
  const events:AssistantEvent[]=[]
  for await(const event of withDelegation(codex).start(session))events.push(event)
  expect(events.filter(e=>e.type==='error')).toEqual([])
  expect(selected).toHaveBeenCalledTimes(1);expect(original).not.toHaveBeenCalled()
  expect(events.find(e=>e.type==='policy_resolved')).toMatchObject({members:{'models.author.model':{value:'reviewer',layer:'run'}}})
 })
})
