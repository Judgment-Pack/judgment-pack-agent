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
// agent#25: the offer this leg asserts is the contract's, not the session's own.
import offerVector from '../../../../contract/vectors/tool-offer/scenario-offer.json'
import criticOfferVector from '../../../../contract/vectors/tool-offer/critic-offer.json'
import recallVector from '../../../../contract/vectors/recall/agent.json'
import { recallQuery } from '../../certification/recall'
import { STATE_HEADER } from '../contract'
import type { AgentTool } from '../../agent'

/** An agent-wire offer read back to the tools it aliases: the real name, its description and schema. */
const offered = (tools: AgentTool[], kind: 'runtime' | 'host') =>
  tools.filter(tool => tool.name.startsWith(`desk_${kind}_`)).map(tool => {
    const at = tool.description.indexOf(': ')
    return { name: tool.description.slice(0, at), description: tool.description.slice(at + 2), inputSchema: tool.inputSchema }
  })
const asOffered = (tools: readonly { name: string; description?: string; inputSchema?: unknown }[]) =>
  tools.map(({ name, description, inputSchema }) => ({ name, description: description ?? '', inputSchema }))

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
      const offer = offerVector.items[0]!
      let passes = 0
      let writeAnswer: { text: string; isError: boolean } | undefined
      const run: AgentRun = async (request, callbacks) => {
        passes++
        expect(request.phase).toBe(passes === 1 ? 'author' : 'critic')
        if (passes === 1) {
          // **K2, on the agent wire (agent#25).** The offer is compared with the
          // contract's scenario offer, both members, by real name, description
          // and served schema: the runtime's five, then the context tools.
          expect(offered(request.tools, 'runtime')).toEqual(asOffered(offer.tools))
          expect(offered(request.tools, 'host')).toEqual(asOffered(offer.hostTools))
        } else {
          // The critic is offered its allow-list: the scenario's runtime reads and the harness's two readers (agent#10).
          expect(offered(request.tools, 'runtime')).toEqual(asOffered(criticOfferVector.items[0]!.tools))
          expect(offered(request.tools, 'host')).toEqual(asOffered(criticOfferVector.items[0]!.hostTools))
        }
        const calls = passes === 1 ? recording.calls : recording.calls.filter(call => call.step === 'T5' || call.step === 'T6')
        for (const [index, call] of calls.entries()) {
          const route = ready.tools.findIndex(tool => tool.name === call.tool)
          const args = structuredClone(call.arguments) as Record<string, unknown>
          if (call.tool === 'experimental_evaluate') args.rehearsal = false
          await callbacks.tool({ id: `p${passes}-c${index}`, name: `desk_runtime_${route}`, arguments: args }, callbacks.signal)
        }
        if (passes === 1) {
          // **K3b, on the agent wire (agent#25).** The scenario's T7: a write_file
          // the agent was never offered, which must reach the gate and be refused.
          const write = scenario.steps.find(step => step.id === 'T7')!
          writeAnswer = await callbacks.tool({ id: 'p1-write', name: write.tool!, arguments: structuredClone(write.arguments) as Record<string, unknown> }, callbacks.signal)
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
      // K3b: write_file never reached the runtime; the gate refused it on the
      // wire, between its call and its result, and the agent was told.
      expect(runtime.seen.map(call => call.name)).not.toContain('write_file')
      expect(runtime.seen.filter(call => call.refusal !== '')).toEqual([])
      const refused = events.filter((event): event is Extract<AssistantEvent, { type: 'guardrail' }> => event.type === 'guardrail' && event.action === 'refused')
      expect(refused.map(event => event.tool)).toEqual(['write_file'])
      const refusedAt = events.indexOf(refused[0]!)
      expect(events[refusedAt - 1]).toMatchObject({ type: 'tool_call', name: 'write_file' })
      expect(events[refusedAt + 1]).toMatchObject({ type: 'tool_result', name: 'write_file', isError: true })
      expect(writeAnswer).toMatchObject({ isError: true })
      expect(writeAnswer!.text).toContain('never calls a tool it was not offered')
      expect(events.filter(event => event.type === 'end')).toHaveLength(1)
    } finally {
      vi.unstubAllGlobals()
      await connection.close()
      await runtime.close()
    }
  })

  it('refutes a seeded wrong outcome in the recorded scenario: the rehearsal says evaluated, the declared case disagrees (agent#10)', async () => {
    const runtime = await scriptedRuntime()
    const events: AssistantEvent[] = []
    const connection = openAssistantConnection({ allowed: scenario.scenarioTools, transport: runtime.transport, onEvent: event => events.push(event) })
    try {
      const loaded = await loadEngine(engineId)
      const ready = await connection.ready
      const run: AgentRun = async (request, callbacks) => {
        if (request.phase === 'critic') {
          for (const [index, call] of recording.calls.filter(call => call.step === 'T5' || call.step === 'T6').entries()) {
            const route = ready.tools.findIndex(tool => tool.name === call.tool)
            await callbacks.tool({ id: `c${index}`, name: `desk_runtime_${route}`, arguments: structuredClone(call.arguments) as Record<string, unknown> }, callbacks.signal)
          }
        }
        await callbacks.event({ type: 'message', id: request.phase, text: request.phase === 'critic' ? 'No refutation found.' : proposal(scenario.documents.DRAFT_V2), phase: 'final' })
      }
      // The recorded rehearsal of FACTS approves; the declared case says decline.
      const criticCases = [{ source: 'declared' as const, id: 'receipted-120', facts: scenario.documents.FACTS as Record<string, unknown>, expectedDisposition: { kind: 'outcome', outcomeId: 'decline' } }]
      for await (const event of loaded.start(session(run, { ...ready, adversarialReview: true, criticCases }))) events.push(event)
      expect(events.find(event => event.type === 'critique')).toMatchObject({
        refuted: true,
        checks: [{ tool: 'validate', status: 'valid' }, { tool: 'experimental_evaluate', status: 'evaluated' }],
        cases: [{ case: 'receipted-120', source: 'declared', tool: 'experimental_evaluate', agrees: false, actual: { disposition: { kind: 'outcome', outcomeId: 'approve' } } }],
        incomplete: []
      })
      expect(events.find(event => event.type === 'proposal')).toMatchObject({ document: scenario.documents.DRAFT_V2, critique: { refuted: true } })
    } finally {
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
    // agent#25: an unoffered tool is put through the session's gate as the
    // agent named it, refused there, and answered refused; the run goes on.
    const callTool = vi.fn(async () => { throw new Error('write_file is not one of the tools this assistant may call') })
    let answered: { text: string; isError: boolean } | undefined
    const refused = await collect(session(async (_r, c) => {
      answered = await c.tool({ id: 'unknown', name: 'write_file', arguments: { path: 'x' } }, c.signal)
      await c.event({ type: 'message', id: 'm', text: proposal(), phase: 'final' })
    }, { callTool }))
    expect(callTool).toHaveBeenCalledExactlyOnceWith('write_file', { path: 'x' })
    expect(answered).toEqual({ isError: true, text: expect.stringContaining('refused: write_file is not one of the tools') })
    expect(refused.map(event => event.type)).toEqual(['tool_call', 'tool_result', 'message', 'proposal', 'end'])
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

  // agent#36: the state and guidance are headed by STATE_HEADER, as the relayed wires head the state
  // part, so the contract's recall rule applied to this engine's prompt yields the person's message.
  it('heads the state and guidance after the person’s message, and the contract’s recall rule yields the message', async () => {
    const item = recallVector.items[0]!
    const prompts: string[] = []
    const run: AgentRun = async (request, callbacks) => {
      prompts.push(request.prompt)
      await callbacks.event({ type: 'message', id: 'done', text: proposal(), phase: 'final' })
    }
    const loaded = await loadEngine(engineId)
    const events: AssistantEvent[] = []
    // A conversation with a host state: both are appended.
    for await (const event of loaded.start(session(run, { prompt: item.text, allowConversation: true, adversarialReview: false, context: { currentState: 'Draft snapshot: {"pack":"expense-reimbursement"}' } }) as never)) events.push(event)
    // Neither a state nor a conversation: nothing is appended and nothing is headed.
    for await (const event of loaded.start(session(run, { prompt: item.text, adversarialReview: false }) as never)) events.push(event)
    expect(events.filter(event => event.type === 'error')).toEqual([])
    expect(prompts).toHaveLength(2)
    const [chat, plain] = prompts as [string, string]
    // The shape is the vector's: the message, a blank line, the header, then the state and the guidance.
    const [head] = item.body.prompt.split('\n… (the host')
    expect(chat.startsWith(head!)).toBe(true)
    expect(chat.slice(item.text.length, item.text.length + STATE_HEADER.length + 3)).toBe('\n\n' + STATE_HEADER + '\n')
    expect(chat.split(STATE_HEADER)).toHaveLength(2)
    expect(chat).toContain('Draft snapshot: {"pack":"expense-reimbursement"}')
    expect(chat.indexOf('"requests"')).toBeGreaterThan(chat.indexOf('Draft snapshot'))
    expect(recallQuery('agent', { prompt: chat })).toEqual({ pointer: item.pointer, text: item.text })
    expect(plain).toBe(item.text)
    expect(recallQuery('agent', { prompt: plain })).toEqual({ pointer: item.pointer, text: item.text })
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

/** agent#12: a reading tool's answer is one data block; its data lies between the first line and the last. */
const dataOf=(text:string)=>{
 const lines=text.split('\n')
 expect(lines[0]).toMatch(/^BEGIN RETRIEVED DATA [0-9a-f]{32} from tool "/)
 expect(lines.at(-1)).toBe('END RETRIEVED DATA '+lines[0]!.split(' ')[3])
 return lines.slice(1,-1).join('\n')
}
it('retains full tool results for the UI, sends bounded references to Codex, and isolates the critic',async()=>{
 const value={records:Array.from({length:1000},(_,i)=>({id:i,decimal:'50',flag:false,note:'evidence '.repeat(40)}))}
 let passes=0
 let listAlias=''
 let critic:{offered:string[];refused?:{text:string;isError:boolean}}={offered:[]}
 const run:AgentRun=async(request,callbacks)=>{
  passes++
  const find=(name:string)=>request.tools.find(t=>t.description.startsWith(name+':'))!.name
  if(passes===1){
   listAlias=find('list_context')
   const result=await callbacks.tool({id:'large',name:find('read_records'),arguments:{}},callbacks.signal)
   expect(result.text.length).toBeLessThan(12000)
   const ref=JSON.parse(result.text).structuredContent
   expect(ref).toMatchObject({operationReturned:true,readWith:'read_window'})
   const page=await callbacks.tool({id:'page',name:find('read_context'),arguments:{id:ref.contextId,path:'/structuredContent/records/999'}},callbacks.signal)
   expect(JSON.parse(dataOf(page.text)).structuredContent.value).toEqual(value.records[999])
  }else{
   // agent#10: the critic is offered no host tool but the harness's two
   // readers, over its own isolated ledger; asked by the author's alias,
   // list_context is refused before any call (review row 5).
   critic={offered:request.tools.map(t=>t.description.split(':')[0]!),refused:await callbacks.tool({id:'list',name:listAlias,arguments:{}},callbacks.signal)}
  }
  await callbacks.event({type:'message',id:'answer-'+passes,phase:'final',text:passes===1?proposal({id:'p'}):'No change recommended.'})
 }
 const events:AssistantEvent[]=[]
 for await(const e of codex.start(session(run,{adversarialReview:true,hostTools:[{name:'read_records',description:'Read records',inputSchema:{type:'object'},execute:async()=>({structuredContent:value})}]})))events.push(e)
 expect(events.filter(e=>e.type==='error')).toEqual([])
 expect(passes).toBe(2)
 expect(events.find(e=>e.type==='tool_result'&&e.name==='read_records')).toMatchObject({structured:value})
 expect(critic.offered).toEqual(['read_window','search'])
 expect(critic.refused).toMatchObject({isError:true,text:expect.stringContaining('list_context is served to this session but was not offered to this run')})
 expect(events.filter(e=>e.type==='guardrail')).toEqual([{type:'guardrail',tool:'list_context',action:'refused',detail:expect.stringContaining('was not offered to this run')}])
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
  it('keeps the prose and the raw fence where the proposal cannot be read, after one reformat run (agent#29)', async () => {
    const answer = 'Here is the pack.\n\n```json\n{"proposal":{"document":{"id":"p",},"unknowns":[]}}\n```'
    const run = vi.fn(reply(answer))
    const events = await collect(session(run))
    // The reformat run answered the same unreadable block: the first answer is
    // delivered whole, then the first answer's failure.
    expect(run).toHaveBeenCalledTimes(2)
    expect(events.map(event => event.type)).toEqual(['response_recovery', 'message', 'error', 'end'])
    expect(events[1]).toEqual({ type: 'message', text: answer })
    expect(events[2]).toMatchObject({ failure: { code: 'invalid-response', detail: 'the fenced proposal block is not valid JSON' } })
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

  const proposeTool = { name: PROPOSE_PACK_TOOL, effect: 'proposal' as const, description: 'Propose', inputSchema: PROPOSE_PACK_SCHEMA, execute: async () => ({ content: [] }) }
  const callPropose = async (request: Parameters<AgentRun>[0], callbacks: Parameters<AgentRun>[1]) => {
    const tool = request.tools.find(offered => offered.description.startsWith(`${PROPOSE_PACK_TOOL}:`))!
    await callbacks.tool({ id: 'c1', name: tool.name, arguments: { document: { id: 'tool' }, unknowns: ['u'] } }, callbacks.signal)
  }

  it('delivers a propose_pack proposal with a note when the bridge times out after the call', async () => {
    const run: AgentRun = async (request, callbacks) => {
      await callPropose(request, callbacks)
      throw failureError('timeout', 'agent')
    }
    const events = await collect(session(run, { hostTools: [proposeTool] }))
    expect(events.map(event => event.type)).toEqual(['tool_call', 'tool_result', 'proposal', 'end'])
    expect(events[2]).toMatchObject({ document: { id: 'tool' }, unknowns: ['u'], note: { kind: 'turn-unfinished', failure: { code: 'timeout', source: 'agent' } } })
  })

  it('refuses a tool proposal beside a fenced one as ambiguous, keeping the answer whole', async () => {
    const answer = 'Final.\n\n```json\n' + JSON.stringify({ proposal: { document: { id: 'fence' }, unknowns: [] } }) + '\n```'
    const run: AgentRun = async (request, callbacks) => {
      // The reformat run (agent#29) is offered no tool and answers in prose only.
      if (request.tools.length === 0) {
        await callbacks.event({ type: 'message', id: 'r', text: 'I cannot choose between them.', phase: 'final' })
        return
      }
      await callPropose(request, callbacks)
      await callbacks.event({ type: 'message', id: 'm', text: answer, phase: 'final' })
    }
    const events = await collect(session(run, { hostTools: [proposeTool] }))
    expect(events.map(event => event.type)).toEqual(['tool_call', 'tool_result', 'response_recovery', 'message', 'error', 'end'])
    expect(events[3]).toEqual({ type: 'message', text: answer })
    expect(events[4]).toMatchObject({ failure: { code: 'invalid-response' } })
  })
})
