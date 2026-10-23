import {describe,it,expect,vi} from 'vitest'
import {readFileSync} from 'node:fs'
import {contextualSession} from './context/session'
import {codex} from './engines/codex'
import {withDelegation} from './delegation'
import type {AgentRun} from './agent'
import type {AgentAssistantSession,AssistantEvent} from './engine'
import type {ReasoningValue} from './policy'
import collisions from '../../contract/vectors/policy/legacy-collisions.json'
import omission from '../../contract/vectors/providers/reasoning-omission.json'

const base=():AgentAssistantSession=>({prompt:'Draft a pack.',testPrompt:'Review the pack.',tools:[],hostTools:[],callTool:vi.fn(),signal:new AbortController().signal,allowConversation:true,agent:{model:'connected',run:vi.fn()}})
const source=(path:string)=>readFileSync(new URL(path,import.meta.url),'utf8')
const requestReads=[
 ['./context/session.ts',"const authorTier='model'in session?session.thinking?.tier:session.effort",'legacy layer admitted into the table'],
 ['./context/session.ts',"this.resolvedPolicy['models.compactor.reasoning']!.value",'resolved compactor member'],
 ['./context/session.ts',"context.resolvedPolicy['models.author.reasoning']!.value",'resolved author member'],
 ['./context/session.ts',"context.resolvedPolicy[`models.${role}.reasoning`]!.value as string|undefined",'resolved model fallback member'],
 ['./context/session.ts',"context.resolvedPolicy[`models.${role}.reasoning`]!.value as import('../agent').CodexEffort|undefined",'resolved agent fallback member'],
 ['./delegation.ts',"shared.context?.resolvedPolicy[`models.${shared.context.delegatedRole(profile.id)}.reasoning`]",'resolved delegated member'],
 ['./criticSwitch.ts',"this.settings.resolvedPolicy?.['models.critic.reasoning']",'resolved critic member; the floor below is legacy-only when no resolved policy exists'],
 ['./thinking.ts','const tier = session.thinking?.tier','contextualized model member'],
 ['./engines/codex/index.ts',"state, session.effort)",'contextualized author effort'],
 ['./engines/codex/index.ts','decided.reasoning as CodexEffort | undefined','resolved critic decision'],
 ['./engines/vercel/loop.ts','let slot = openThinking(session, capabilities.reasoning)','contextualized author thinking'],
 ['./engines/vercel/loop.ts','const criticTier = decided.reasoning as ThinkingTier | undefined','resolved critic decision'],
 ['./engines/deepagents/loop.ts','let slot = openThinking(session, capabilities.reasoning)','contextualized author thinking'],
 ['./engines/deepagents/loop.ts','const criticTier = decided.reasoning as ThinkingTier | undefined','resolved critic decision']
] as const
describe('ADR policy table review',()=>{
 it('constructs reasoning requests only from the enumerated admitted or resolved reads',()=>{
  for(const [path,needle] of requestReads)expect(source(path).split(needle),`${path}: ${needle}`).toHaveLength(2)
  const raw=[...new Set(requestReads.map(([path])=>path))].flatMap(path=>source(path).split('\n').filter(line=>/session\.(?:effort|thinking)|tiers\.author|this\.table\.criticReasoningFloor/.test(line)).map(line=>`${path}:${line.trim()}`))
  expect(raw).toEqual([
   "./context/session.ts:const authorTier='model'in session?session.thinking?.tier:session.effort",
   './criticSwitch.ts:? criticTier(tiers.author, this.settings.criticReasoning, this.table.criticReasoningFloor)',
   './thinking.ts:const tier = session.thinking?.tier',
   "./engines/codex/index.ts:final = await run(session.prompt, instructions + (offersProposeTool(session.hostTools) ? PROPOSE_PACK_INSTRUCTIONS : ''), 'author', undefined, undefined, state, session.effort)",
   './engines/vercel/loop.ts:let placement = statePlacement(session.model.family, session.thinking?.tier, capabilities)',
   './engines/vercel/loop.ts:placement=statePlacement(session.model.family,session.thinking?.tier,capabilities)',
   "./engines/vercel/loop.ts:const retiered = criticTier !== session.thinking?.tier || criticBinding !== context.connection('author')",
   './engines/deepagents/loop.ts:let placement = statePlacement(session.model.family, session.thinking?.tier, capabilities)',
   './engines/deepagents/loop.ts:placement=statePlacement(session.model.family,session.thinking?.tier,capabilities)',
   "./engines/deepagents/loop.ts:if (criticTier !== session.thinking?.tier || criticBinding !== context.connection('author')) {"
  ])
  expect(source('./context/session.ts')).toContain("'model'in author?{thinking:tier===undefined?undefined:")
  expect(source('./context/session.ts')).toContain(":{effort:tier===undefined?undefined:")
  expect(source('./engines/codex/index.ts')).not.toContain('effort: CodexEffort | undefined = session.effort')
  expect(source('./criticSwitch.ts')).toContain("const resolved = this.settings.resolvedPolicy?.['models.critic.reasoning']")
 })
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
 for(const row of omission.dropped.filter(row=>row.capability==='agent'))it(`${row.name} stays absent in the request and over resume`,async()=>{
  const requests:Record<string,unknown>[][]=[]
  const makeSession=(sent:Record<string,unknown>[]):AgentAssistantSession=>({...base(),effort:row.raw as AgentAssistantSession['effort'],critic:row.role==='critic'?'on':'off',testPrompt:row.role==='critic'?'Refute it.':'',
   policy:{models:{[row.role]:{discovered:{reasoning:{supported:[...row.supported] as ReasoningValue[]}}}}},context:{currentRequest:{message:'Draft a pack.',input:'Draft a pack.'}},
   agent:{model:'connected',run:vi.fn(async(request,callbacks)=>{sent.push({...request});const text=request.phase==='critic'?'No counterexample found.':row.role==='critic'?'```json\n{"proposal":{"document":{"id":"pack"},"unknowns":[]}}\n```':'Done.';await callbacks.event({type:'message',id:`${request.phase}-done`,text,phase:'final'})})}})
  const seed=makeSession([]),opened=await contextualSession(seed,'codex'),saved=opened.context.execution.snapshot('stopped');opened.context.close()
  for(const execution of [undefined,saved]){
   const sent:Record<string,unknown>[]=[],session=makeSession(sent)
   if(execution)session.context={...session.context,execution}
   const events:AssistantEvent[]=[];for await(const event of codex.start(session))events.push(event)
   expect(events.filter(event=>event.type==='error'),row.name).toEqual([])
   const request=sent.find(one=>one.phase===row.role)!
   expect(request,row.name).not.toHaveProperty(row.requestPath)
   const resolved=events.find(event=>event.type==='policy_resolved') as Extract<AssistantEvent,{type:'policy_resolved'}>
   expect(resolved.members[`models.${row.role}.reasoning`],row.name).toEqual(row.resolved)
   if(row.decisionPath)expect(events.find(event=>event.type==='critic_decided'),row.name).not.toHaveProperty(row.decisionPath)
   expect(events.filter(event=>event.type==='execution_checkpoint').at(-1)?.checkpoint.policy?.[`models.${row.role}.reasoning`],row.name).toEqual(row.resolved)
   requests.push(sent)
  }
  expect(JSON.stringify(requests[1]),row.name).toBe(JSON.stringify(requests[0]))
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
