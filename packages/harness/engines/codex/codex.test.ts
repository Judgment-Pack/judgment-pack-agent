import { afterEach, describe, expect, it, vi } from 'vitest'
import { codex, type CodexSession } from './index'
import type { AgentRun } from '../../agent'
import type { AssistantEvent } from '../../engine'
import { scriptedRuntime } from '../../certification/scriptedServer'
import recording from '../../certification/runtime.json'
import scenario from '../../certification/scenario.json'
import { openAssistantConnection } from '../../session'
import { CERTIFIED_ENGINES, AGENT_ENGINES, loadEngine } from '../index'
import { failureError } from '../../failures'
import { PROPOSAL_RECEIVED, PROPOSE_PACK_SCHEMA, PROPOSE_PACK_TOOL } from '../contract'

const proposal = (document: unknown = { id: 'pack' }) => 'Ready.\n\n```json\n' + JSON.stringify({ proposal: { document, unknowns: ['Who reviews?'] } }) + '\n```'
function session(run: AgentRun, changes: Partial<CodexSession> = {}): CodexSession {
  return { prompt: 'Author a pack', testPrompt: 'Test the pack', tools: [], hostTools: [], callTool: vi.fn(),
    signal: new AbortController().signal, agent: { model: 'model', run }, ...changes }
}
async function collect(s: CodexSession) {
  const events: AssistantEvent[] = []
  for await (const event of codex.start(s)) events.push(event)
  return events
}
const reply = (text: string): AgentRun => async (_request, callbacks) => {
  await callbacks.event({ type: 'text', id: 'message', text })
  await callbacks.event({ type: 'message', id: 'message', text, phase: 'final' })
}
afterEach(() => vi.unstubAllGlobals())

describe.each(AGENT_ENGINES)('agent engine %s', engineId => {
  it('keeps the full recorded runtime scenario behind the real ToolGate, including its critic', async () => {
    const runtime = await scriptedRuntime()
    const events: AssistantEvent[] = []
    const connection = openAssistantConnection({ allowed: scenario.scenarioTools, transport: runtime.transport, onEvent: event => events.push(event) })
    try {
      const loaded = await loadEngine(engineId)
      const ready = await connection.ready
      let passes = 0
      const run: AgentRun = async (request, callbacks) => {
        passes++
        expect(request.phase).toBe(passes === 1 ? 'author' : 'critic')
        expect(request.tools.filter(tool=>tool.name.startsWith('desk_runtime_')).map(tool => tool.inputSchema)).toEqual(ready.tools.map(tool => tool.inputSchema))
        const calls = passes === 1 ? recording.calls : recording.calls.filter(call => call.step === 'T5' || call.step === 'T6')
        for (const [index, call] of calls.entries()) {
          const route = ready.tools.findIndex(tool => tool.name === call.tool)
          const args = structuredClone(call.arguments) as Record<string, unknown>
          if (call.tool === 'experimental_evaluate') args.rehearsal = false
          await callbacks.tool({ id: `p${passes}-c${index}`, name: `desk_runtime_${route}`, arguments: args }, callbacks.signal)
        }
        const text = passes === 1 ? proposal(scenario.documents.DRAFT_V2) : 'I claim this was refuted.'
        await callbacks.event({ type: 'message', id: `p${passes}`, text, phase: 'final' })
      }
      for (const name of ['fetch', 'WebSocket', 'XMLHttpRequest', 'EventSource']) {
        vi.stubGlobal(name, () => { throw new Error('Adapter touched a network global') })
      }
      for await (const event of loaded.start(session(run, { ...ready, adversarialReview: true }))) events.push(event)
      expect(events.filter(event => event.type === 'error')).toEqual([])
      expect(passes).toBe(2)
      expect(events.find(event => event.type === 'proposal')).toMatchObject({ document: scenario.documents.DRAFT_V2 })
      expect(events.find(event => event.type === 'critique')).toMatchObject({ refuted: false })
      const evaluations = runtime.seen.filter(call => call.name === 'experimental_evaluate')
      expect(evaluations).toHaveLength(2)
      expect(evaluations.every(call => call.args.rehearsal === true && call.refusal === '')).toBe(true)
      const guardIndex = events.findIndex(event => event.type === 'guardrail' && event.tool === 'experimental_evaluate')
      expect(events[guardIndex - 1]?.type).toBe('tool_call')
      expect(events[guardIndex + 1]?.type).toBe('tool_result')
      expect(events.filter(event => event.type === 'end')).toHaveLength(1)
    } finally {
      vi.unstubAllGlobals()
      await connection.close()
      await runtime.close()
    }
  })

  it('streams prose while keeping proposal JSON hidden and uses the shared parser', async () => {
    const events = await collect(session(reply(proposal()), { interactive: true }))
    expect(events.find(event => event.type === 'message_progress')).toEqual({ type: 'message_progress', text: 'Ready.' })
    expect(events.find(event => event.type === 'proposal')).toMatchObject({ document: { id: 'pack' }, unknowns: ['Who reviews?'] })
    expect(events.at(-1)?.type).toBe('end')
  })

  it.each(['brief', 'test-design', 'graph'] as const)('preserves the %s workflow and explicit language instructions', async purpose => {
    let instructions = ''
    const events = await collect(session(async (request, callbacks) => {
      instructions = request.instructions
      await reply(proposal())(request, callbacks)
    }, { purpose, replyLanguage: 'fr' }))
    expect(instructions).toContain(purpose === 'graph' ? 'graph authoring assistant' : purpose === 'brief' ? 'one-page brief' : 'design tests')
    expect(instructions.toLowerCase()).toContain('french')
    expect(events.some(event => event.type === 'proposal')).toBe(true)
  })

  it('does not manufacture proposals or claim verification for an ordinary chat reply', async () => {
    const events = await collect(session(reply('Hello.'), { allowConversation: true }))
    expect(events).toEqual([{ type: 'message', text: 'Hello.' }, { type: 'end' }])
  })

  it('executes a host tool with cancellation and returns structured content without treating it as a runtime check', async () => {
    const execute = vi.fn(async () => ({ structuredContent: { source: 'verified-document' } }))
    const events = await collect(session(async (request, callbacks) => {
      const answer = await callbacks.tool({ id: 'call', name: request.tools[0]!.name, arguments: {} }, callbacks.signal)
      expect(JSON.parse(answer.text)).toMatchObject({ structuredContent: { source: 'verified-document' } })
      await reply(proposal())(request, callbacks)
    }, { hostTools: [{ name: 'read_source', description: 'Read', inputSchema: { type: 'object' }, execute }] }))
    expect(execute).toHaveBeenCalledOnce()
    expect(events.find(event => event.type === 'tool_result')).toMatchObject({ structured: { source: 'verified-document' } })
  })

  it('refuses colliding or unoffered tools and never falls back to another engine', async () => {
    const run = vi.fn(reply(proposal()))
    const tool = { name: 'validate', inputSchema: { type: 'object' } }
    const events = await collect(session(run, { tools: [tool], hostTools: [{ ...tool, description: '', execute: vi.fn() }] }))
    expect(run).not.toHaveBeenCalled()
    expect(events[0]?.type).toBe('error')
    const callTool = vi.fn()
    const refused = await collect(session(async (_r, c) => {
      await c.tool({ id: 'unknown', name: 'write_file', arguments: {} }, c.signal)
    }, { callTool }))
    expect(callTool).not.toHaveBeenCalled()
    expect(refused[0]?.type).toBe('error')
    expect(CERTIFIED_ENGINES).toContain(engineId)
  })

  it('reports an unverified critique when the critic only speaks', async () => {
    const events = await collect(session(reply(proposal()), { adversarialReview: true }))
    expect(events.find(event => event.type === 'critique')).toMatchObject({ checks: [] })
    expect(events.find(event => event.type === 'proposal')).not.toHaveProperty('critique')
  })

  it('stops before executing a tool when the consumer leaves at tool_call', async () => {
    const callTool = vi.fn()
    const iterator = codex.start(session(async (request, c) => {
      await c.tool({ id: 'call', name: request.tools[0]!.name, arguments: {} }, c.signal)
    }, { tools: [{ name: 'validate', inputSchema: { type: 'object' } }], callTool }))[Symbol.asyncIterator]()
    expect((await iterator.next()).value?.type).toBe('tool_call')
    await iterator.return?.()
    await Promise.resolve()
    expect(callTool).not.toHaveBeenCalled()
  })

  it('cancels a pending native capability and accepts no late output', async () => {
    const stop = new AbortController()
    let entered!: () => void
    const started = new Promise<void>(resolve => { entered = resolve })
    const events: AssistantEvent[] = []
    const promise = (async () => {
      for await (const event of codex.start(session(async (_r, c) => {
        entered()
        await new Promise<void>(resolve => c.signal.addEventListener('abort', () => resolve(), { once: true }))
        await c.event({ type: 'message', id: 'late', text: proposal(), phase: 'final' })
      }, { signal: stop.signal }))) events.push(event)
    })()
    await started
    stop.abort()
    await promise
    expect(events).toEqual([])
  })
})

it('releases the agent connection at a question without returning a fabricated user answer',async()=>{
 const {questionTool}=await import('../../questions')
 const {interviewQuestion}=await import('../../testing/question')
 let answered=false,aborted=false
 const events=await collect(session(async(request,callbacks)=>{
  callbacks.signal.addEventListener('abort',()=>{aborted=true})
  await callbacks.tool({id:'call-question',name:request.tools[0]!.name,arguments:interviewQuestion as unknown as Record<string,unknown>},callbacks.signal)
  answered=true
 },{allowConversation:true,interactive:true,hostTools:[questionTool]}))
 expect(aborted).toBe(true);expect(answered).toBe(false)
 expect(events.filter(e=>e.type==='user_question')).toEqual([{type:'user_question',id:expect.any(String),question:interviewQuestion}])
 expect(events.filter(e=>e.type==='error'||e.type==='tool_result'||e.type==='proposal')).toEqual([])
 expect(events.filter(e=>e.type==='end')).toHaveLength(1)
})

it('keeps native recovery as progress without fabricating assistant text or another run',async()=>{
 const run=vi.fn<AgentRun>(async(_request,callbacks)=>{
  await callbacks.event({type:'retry',attempt:1})
  await callbacks.event({type:'message',id:'answer',text:'Recovered answer',phase:'final'})
 })
 const events=await collect(session(run,{allowConversation:true}))
 expect(run).toHaveBeenCalledOnce()
 expect(events).toContainEqual({type:'retry',attempt:1})
 expect(events.filter(e=>e.type==='message')).toEqual([{type:'message',text:'Recovered answer'}])
 expect(events.some(e=>e.type==='error')).toBe(false)
})

it('returns a precise question refusal and accepts the corrected call in the same Codex turn',async()=>{
 const {questionTool}=await import('../../questions')
 const {interviewQuestion}=await import('../../testing/question')
 let calls=0
 const run=vi.fn<AgentRun>(async(request,callbacks)=>{
  const first=await callbacks.tool({id:'invalid',name:request.tools[0]!.name,arguments:{...interviewQuestion,title:'x'.repeat(41)}},callbacks.signal)
  calls++;expect(first.isError).toBe(true);expect(first.text).toContain('40 characters');expect(first.text).toContain('call ask_user again')
  await callbacks.tool({id:'corrected',name:request.tools[0]!.name,arguments:interviewQuestion as unknown as Record<string,unknown>},callbacks.signal)
  calls++
 })
 const events=await collect(session(run,{allowConversation:true,interactive:true,hostTools:[questionTool]}))
 expect(run).toHaveBeenCalledOnce();expect(calls).toBe(1)
 expect(events.find(e=>e.type==='tool_result')).toMatchObject({name:'ask_user',isError:true,structured:{questionFailure:'title'}})
 expect(events.filter(e=>e.type==='user_question')).toHaveLength(1)
 expect(events.some(e=>e.type==='error')).toBe(false)
})

it('retains full tool results for the UI, sends bounded references to Codex, and isolates the critic',async()=>{
 const value={records:Array.from({length:1000},(_,i)=>({id:i,decimal:'50',flag:false,note:'evidence '.repeat(40)}))}
 let passes=0
 const run:AgentRun=async(request,callbacks)=>{
  passes++
  const find=(name:string)=>request.tools.find(t=>t.description.startsWith(name+':'))!.name
  if(passes===1){
   const result=await callbacks.tool({id:'large',name:find('read_records'),arguments:{}},callbacks.signal)
   expect(result.text.length).toBeLessThan(12000)
   const ref=JSON.parse(result.text).structuredContent
   expect(ref).toMatchObject({operationReturned:true,readWith:'read_context'})
   const page=await callbacks.tool({id:'page',name:find('read_context'),arguments:{id:ref.contextId,path:'/structuredContent/records/999'}},callbacks.signal)
   expect(JSON.parse(page.text).structuredContent.value).toEqual(value.records[999])
  }else{
   const refs=await callbacks.tool({id:'list',name:find('list_context'),arguments:{}},callbacks.signal)
   expect(JSON.parse(refs.text).structuredContent.items).toEqual([])
  }
  await callbacks.event({type:'message',id:'answer-'+passes,phase:'final',text:passes===1?proposal({id:'p'}):'No change recommended.'})
 }
 const events:AssistantEvent[]=[]
 for await(const e of codex.start(session(run,{adversarialReview:true,hostTools:[{name:'read_records',description:'Read records',inputSchema:{type:'object'},execute:async()=>({structuredContent:value})}]})))events.push(e)
 expect(events.filter(e=>e.type==='error')).toEqual([])
 expect(passes).toBe(2)
 expect(events.find(e=>e.type==='tool_result'&&e.name==='read_records')).toMatchObject({structured:value})
})

it('preserves the current question and reads searchable context through the subscription adapter',async()=>{
 const message='Explain this mismatch',input='Pack evidence '.repeat(12000)+'EXACT CASE: missing size'
 const events=await collect(session(async(request,callbacks)=>{
  expect(request.prompt).toContain(message)
  const id=request.prompt.match(/[0-9a-f-]{36}:current-request:[0-9a-f]{16}/)?.[0]
  expect(id).toBeDefined()
  const tool=request.tools.find(t=>t.description.startsWith('read_context:'))!
  const result=await callbacks.tool({id:'read',name:tool.name,arguments:{id,path:'/input',query:'EXACT CASE'}},callbacks.signal)
  expect(JSON.stringify(result)).toContain('EXACT CASE: missing size')
  await callbacks.event({type:'message',id:'reply',text:'Explained without changes.',phase:'final'})
 },{prompt:'history '.repeat(40000),context:{currentRequest:{message,input}},allowConversation:true,adversarialReview:false}))
 expect(events.filter(e=>e.type==='error')).toEqual([])
 expect(events.filter(e=>e.type==='message')).toEqual([{type:'message',text:'Explained without changes.'}])
})

it('uses shared task progress without inventing subscription model-request usage',async()=>{
 const run:AgentRun=async(request,callbacks)=>{
  const tool=request.tools.find(t=>t.description.startsWith('update_plan:'))!
  const result=await callbacks.tool({id:'plan-progress',name:tool.name,arguments:{explanation:'Inspection complete',plan:[{step:'Inspect the rule',status:'completed'}]}},callbacks.signal)
  expect(result.text).toContain('provider-managed')
  expect(result.text).not.toContain('remaining')
  await callbacks.event({type:'message',id:'done',text:'The exception needs a guard.',phase:'final'})
 }
 const events=await collect(session(run,{allowConversation:true,interactive:true,context:{currentRequest:{message:'Inspect the rule',input:'Current pack'}}}))
 expect(events.filter(e=>e.type==='execution_checkpoint').at(-1)).toMatchObject({checkpoint:{unit:'provider-managed',plan:[{step:'Inspect the rule',status:'completed'}]}})
 expect(events.filter(e=>e.type==='error')).toEqual([])
})

describe('nothing finished is lost, on the agent wire (agent#6)', () => {
  it('keeps the prose and the raw fence where the proposal cannot be read', async () => {
    const answer = 'Here is the pack.\n\n```json\n{"proposal":{"document":{"id":"p",},"unknowns":[]}}\n```'
    const events = await collect(session(reply(answer)))
    expect(events.map(event => event.type)).toEqual(['message', 'error', 'end'])
    expect(events[0]).toEqual({ type: 'message', text: answer })
    expect(events[1]).toMatchObject({ failure: { code: 'invalid-response' } })
  })

  it('delivers the proposal with a note when the critic fails', async () => {
    let passes = 0
    const run: AgentRun = async (request, callbacks) => {
      passes++
      if (request.phase === 'critic') throw failureError('unavailable', 'agent')
      await callbacks.event({ type: 'message', id: 'm', text: proposal(), phase: 'final' })
    }
    const events = await collect(session(run, { adversarialReview: true }))
    expect(passes).toBe(2)
    expect(events.filter(event => event.type === 'error' || event.type === 'critique')).toEqual([])
    expect(events.find(event => event.type === 'proposal')).toMatchObject({ document: { id: 'pack' }, note: { kind: 'critic-unfinished', failure: { code: 'unavailable', source: 'agent' } } })
    expect(events.at(-1)?.type).toBe('end')
  })

  it('accepts the proposal through propose_pack and never reaches the host’s execute', async () => {
    const execute = vi.fn(async () => ({ content: [] }))
    let instructions = ''
    let answered = ''
    const run: AgentRun = async (request, callbacks) => {
      instructions = request.instructions
      const tool = request.tools.find(offered => offered.description.startsWith(`${PROPOSE_PACK_TOOL}:`))!
      answered = (await callbacks.tool({ id: 'c1', name: tool.name, arguments: { document: { id: 'p' }, unknowns: ['u'] } }, callbacks.signal)).text
      await callbacks.event({ type: 'message', id: 'm', text: 'Proposed.', phase: 'final' })
    }
    const events = await collect(session(run, { hostTools: [{ name: PROPOSE_PACK_TOOL, effect: 'proposal', description: 'Propose', inputSchema: PROPOSE_PACK_SCHEMA, execute }] }))
    expect(execute).not.toHaveBeenCalled()
    expect(answered).toBe(PROPOSAL_RECEIVED)
    expect(instructions).toContain(`calling ${PROPOSE_PACK_TOOL}`)
    expect(events.find(event => event.type === 'proposal')).toEqual({ type: 'proposal', document: { id: 'p' }, unknowns: ['u'] })
    expect(events.filter(event => event.type === 'error')).toEqual([])
  })
})
