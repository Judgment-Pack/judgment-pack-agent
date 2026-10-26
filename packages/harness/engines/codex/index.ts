import {nativeCaptureRequest} from '../../storage/physicalDispatch'
import {recordSubagents} from '../../subagents'
import {failureEvent,failureError,toolFailureResult} from '../../failures'
import {managedContext,contextOf} from '../../context/managed'
import { readDelegationRequest } from '../../delegationContract'
import { readModelQuestion } from '../../questions'
import { questionFailureResult } from '../../questionFailure'
import { assistantLanguageInstructions } from '../../host'
import { separateImages } from '../../images'
import { DATA_RULE, readingTool } from '../../framing'
import { GateViolation } from '../../toolGate'
import type { AssistantEvent, AgentAssistantSession, RequestAgentAssistantSession, EngineSession, Engine, McpToolResult, ProposalNote } from '../../engine'
import type { AgentTool, AgentEvent } from '../../agent'
import {
  SYSTEM, CONVERSATION_SYSTEM, TEST_DESIGN_SYSTEM, BRIEF_SYSTEM, GRAPH_SYSTEM,
  openRun, eventIterator, guardedCallTool, withAbort, isCancelled,
  servedSchema, textOf, streamingProse, proseOf, hasProposalFence, extractProposal, isTerminalProposalSourceRefusal,
  wholeAnswer, withDeadline, toolTimeout, PROPOSE_PACK_TOOL, PROPOSE_PACK_INSTRUCTIONS, PROPOSAL_RECEIVED,
  offersProposeTool, readProposedPack, twoProposals, reformatRequest, refusedUnoffered, unofferedRefusal, STATE_HEADER, type Proposal
} from '../contract'
import { eventChannel } from '../vercel/channel'
import { criticDecidedEvent } from '../../criticSwitch'
import type { CodexEffort } from '../../agent'
import { CRITIC_SYSTEM, compareTheRest, criticMessage, criticCannotRun, critiqueEvent, critiqueOnProposal, criticNote, proposalNote, openCritique } from '../../refutation'
import type { CritiqueRecorder, Critique, CriticFigures } from '../../refutation'

/** Candidate contract. No fake model endpoint, and no selectable registry entry
 * until provider configuration, UI readiness and live certification are done. */
export type CodexSession = AgentAssistantSession

/**
 * The one reformat run a proposal that cannot be read earns (agent#29).
 *
 * The Codex bridge starts every agent run on a new, ephemeral thread, so the
 * model is shown its own answer in the prompt rather than in a history; the
 * run's instructions are the author's, so it knows the shape asked for. The run
 * is offered no tool.
 */
export function reformatPrompt(answer: string, cause: unknown): string {
  return `${reformatRequest(cause)}\n\nYour previous answer is between the two lines below.\nBEGIN PREVIOUS ANSWER\n${answer}\nEND PREVIOUS ANSWER`
}

/**
 * How long one critic pass may take on this engine (agent#10 review row 8): the
 * Codex bridge's own deadline for an agent run, ten minutes, held from the
 * critic run's start over the harness's own calls after it. A harness call is
 * not made once it has passed, and its case stays incomplete; one in flight is
 * bounded by what is left of it.
 */
export const CRITIC_PASS_MILLISECONDS = 10 * 60_000

export function runCodex(session:CodexSession):AsyncIterable<AssistantEvent>{return managedContext(session,runCodexLoop,'codex')}
function runCodexLoop(session: RequestAgentAssistantSession): AsyncIterable<AssistantEvent> {
  const context=contextOf(session)
  let closeRun = () => {}
  const channel = eventChannel({ onAbandon: () => closeRun() })
  const gate = openRun(session, () => channel.abandon())
  closeRun = gate.close
  const inputPause = new AbortController()
  const executionSignal = AbortSignal.any([gate.signal, inputPause.signal])
  let awaitingInput = false
  context.claimApprovalPause=()=>{if(awaitingInput)return false;awaitingInput=true;return true}
  const callTool = guardedCallTool(session, executionSignal)
  const deliver = (event: AssistantEvent) => channel.push(event)
  const drive = async () => {
    /** A proposal delivered through `propose_pack`; the fence is the fallback. */
    let toolProposal: Proposal | null = context.drafts.proposal()
    const names = new Set<string>()
    const routes = new Map<string, { name: string; host?: CodexSession['hostTools'][number] }>()
    const tools: AgentTool[] = []
    for (const [kind, offered] of [['runtime', session.values.tools], ['host', session.values.hostTools]] as const) {
      for (const tool of offered) {
        if (names.has(tool.name)) throw new Error('Assistant tool names must be unique')
        names.add(tool.name)
        const name = `desk_${kind}_${tools.length}`
        routes.set(name, { name: tool.name, ...(kind === 'host' ? { host: tool as CodexSession['hostTools'][number] } : {}) })
        tools.push({ name, description: `${tool.name}: ${tool.description ?? ''}`, inputSchema: servedSchema(tool) })
      }
    }
    /**
     * The tools one run is offered: every tool to the author; to the critic its
     * allow-list of the runtime's reads and the harness's own context readers
     * (agent#10, review row 6); to the reformat run none (agent#29).
     */
    const offeredTo = (phase: 'author' | 'critic' | 'reformat') =>
      phase === 'author' ? tools
        : phase === 'reformat' ? []
          : tools.filter(tool => { const route = routes.get(tool.name); return route !== undefined && session.values.allowsTool(route.name,route.host===undefined?'runtime':'host','critic') })
    /**
     * One agent run. `prompt` is the run's request text; `state`, on the author's run, what changes
     * between runs (ADR-0005, layer 8). The instructions are the system text: the package's guidance,
     * then the host's parts (layers 3 to 5), the same on every run of the response. The prompt is the
     * request text, or the person's message and its preview with its reference where it does not fit,
     * then a chat's index of earlier results, then `STATE_HEADER` and the state: the parts and the
     * order the model wires send in the first user message (`SessionContext.opening`).
     */
    let invocation=0
    const run = async (prompt: string, instructions: string, phase: 'author' | 'critic' | 'reformat', recorder?: CritiqueRecorder, model?: string, state = '', effort?: CodexEffort) => {
      invocation++
      const steers=context.execution.takeSteers(session.input,context.marker,invocation,context.policy)
      for(const event of steers.events)await deliver(event)
      if(steers.text){
        if(phase==='author')state=[state,steers.text].filter(Boolean).join('\n\n')
        else prompt=[prompt,steers.text].filter(Boolean).join('\n\n')
      }
      const offeredHere = offeredTo(phase)
      const offeredNames = new Set(offeredHere.map(tool => tool.name))
      // The critic's phase is its caller's (it spans the harness's own calls after
      // the run); the reformat run's is this run's: every call refused (review row 5).
      if(phase==='reformat')context.beginReformatPhase()
      try {
      instructions = context.system(instructions + DATA_RULE + assistantLanguageInstructions(session.replyLanguage))
      const stated = state ? '\n\n' + STATE_HEADER + '\n' + state : ''
      prompt = phase==='author' ? (await context.opening(prompt,instructions+stated,offeredHere,session.context?.currentRequest)).join('\n\n') + stated : await context.prompt(prompt,instructions,offeredHere)
      context.check(instructions,prompt,offeredHere)
      const partial = new Map<string, string>()
      let final = ''
      let messages = 0
      await withAbort(() => session.values.agent.run(nativeCaptureRequest({ prompt, instructions, tools: offeredHere, ...(effort === undefined ? {} : { effort }), phase: phase === 'critic' ? 'critic' : 'author', ...(model === undefined ? {} : { model }) },new Map(offeredHere.map(tool=>[tool.name,routes.get(tool.name)!.name]))), {
        signal: executionSignal,
        connectionSwitched: deliver,
        event: async (event: AgentEvent) => {
          if (executionSignal.aborted) return
          if (event.type === 'retry') {
            await deliver({type: 'retry', attempt: event.attempt})
            return
          }
          if (event.type === 'text') {
            const text = (partial.get(event.id) ?? '') + event.text
            partial.set(event.id, text)
            if (phase === 'author' && session.interactive) await deliver({ type: 'message_progress', text: streamingProse(text) })
          } else {
            partial.delete(event.id)
            messages++
            if (event.phase === 'commentary') {
              if (phase === 'author' && proseOf(event.text)) await deliver({ type: 'message', text: proseOf(event.text) })
            } else final = event.text
          }
        },
        tool: async (call, transportSignal) => {
          const toolSignal = AbortSignal.any([executionSignal, transportSignal])
          if (awaitingInput) throw new DOMException('Waiting for input', 'AbortError')
          if (toolSignal.aborted) throw new DOMException('Stopped', 'AbortError')
          const route = routes.get(call.name)
          if (!route || !offeredNames.has(call.name)) {
            // A tool this run was not offered (agent#25), answered refused; the
            // run goes on. One the session serves, by its alias or its real name,
            // is refused here before any call (review row 5); any other is put
            // through the gate as the agent asked for it. The Desk's agent
            // transport and bridge refuse an unoffered name before it gets here.
            const name = route?.name ?? call.name
            await deliver({ type: 'tool_call', callId: call.id, name, args: call.arguments })
            const text = route !== undefined || names.has(call.name)
              ? unofferedRefusal(context.refuseUnoffered(name,call.arguments).message)
              : await refusedUnoffered(callTool, name, call.arguments)
            if (toolSignal.aborted) throw new DOMException('Stopped', 'AbortError')
            await deliver({ type: 'tool_result', callId: call.id, name, isError: true, text })
            return { isError: true, text }
          }
          // ADR-0008: an act call the host's policy asks about ends the run as a delegation handoff
          // does: its approval_requested in place of the call, the call never run, the invocation stopped.
          const runtime = route.host ? undefined : await context.prepareRuntimeCall(route.name, call.arguments, call.id)
          const held = route.host ? await context.approval(route.name, call.arguments, call.id) : runtime?.requested
          if (held !== undefined) {
            await deliver(held)
            awaitingInput = true
            inputPause.abort()
            throw new DOMException('Waiting for the person’s decision', 'AbortError')
          }
          if (!route.host?.interaction) await deliver({ type: 'tool_call', callId: call.id, name: route.name, args: runtime?.args??call.arguments })
          let result: McpToolResult
          let runtimeAnswered = false
          /** The tool itself answered: not a failure the harness wrote, not propose_pack's acknowledgement. */
          let toolAnswered = false
          try {
            // agent#10: the critic's allow-list, before anything of the host's runs.
            context.admit(route.name, route.host ? 'host' : 'runtime',call.arguments)
            if (route.host?.interaction === 'delegation') {
              const request = await (context.taskDelegationRequest(route.host,call.arguments,toolSignal)??Promise.resolve(readDelegationRequest(call.arguments)))
              recordSubagents(request,context.subagents)
              awaitingInput = true
              await deliver({type:'delegation_requested',callId:call.id,request,...(request.tasks.length===1&&request.tasks[0]?.subagent?{subagent:request.tasks[0].subagent}:{})})
              inputPause.abort()
              throw new DOMException('Delegation handoff', 'AbortError')
            }
            if (route.host?.interaction === 'question') {
              const question = readModelQuestion(call.arguments)
              if (awaitingInput) throw new DOMException('Waiting for input', 'AbortError')
              awaitingInput = true
              await deliver({type:'user_question', id:crypto.randomUUID(), question})
              inputPause.abort()
              throw new DOMException('Waiting for input', 'AbortError')
            }
            // The proposal as a tool call is the engine's to read; every other
            // call is bounded by the policy's per-call deadline as well.
            const limit = context.policy.toolCallMilliseconds
            if (route.host && route.name === PROPOSE_PACK_TOOL) {
              toolProposal = await context.drafts.seed(readProposedPack(call.arguments))
              result = { content: [{ type: 'text', text: PROPOSAL_RECEIVED }] }
            } else {
              result = route.host
                ? await withDeadline((signal) => route.host!.execute(call.arguments, signal, call.id), toolSignal, limit, () => toolTimeout(limit))
                : await withDeadline(signal => runtime?runtime.execute(signal):callTool(route.name,call.arguments), toolSignal, limit, () => toolTimeout(limit))
              toolAnswered = true
            }
            runtimeAnswered = !route.host
          } catch (cause) {
            if (isCancelled(cause) || executionSignal.aborted) throw cause
            result = route.host?.interaction === 'question' && !(cause instanceof GateViolation) ? questionFailureResult(cause) : toolFailureResult(cause)
          }
          if (awaitingInput) throw new DOMException('Waiting for input', 'AbortError')
          if (toolSignal.aborted) throw new DOMException('Stopped', 'AbortError')
          const text = textOf(result)
          await deliver({ type: 'tool_result', callId: call.id, name: route.name, isError: result.isError === true, text,
            ...(result.structuredContent === undefined ? {} : { structured: result.structuredContent }) })
          if(context.execution.state.planState?.pending){awaitingInput=true;inputPause.abort();throw new DOMException('Waiting for the person’s plan decision','AbortError')}
          // agent#10: the arguments and the structured answer, which the outcome comparison reads.
          if (runtimeAnswered) recorder?.saw(route.name, text, runtime?.args??call.arguments, result.structuredContent)
          // Text-only host callback wire, including structured results in a
          // JSON envelope. The visible result keeps its existing Desk shape.
          // The agent wire carries text only: an image is shown as the host's text rendering of it (agent#5).
          const shown = await separateImages(result, { vision: false, resolve: (reference) => context.ledger.image(reference), signal: toolSignal })
          const projected = await context.result(route.name,shown.result)
          const answer = projected.structuredContent===undefined?textOf(projected):JSON.stringify(projected)
          // A reading tool's own answer travels as one data block closed by
          // this run's marker, as the vercel engine shows it (agent#12).
          return {isError:result.isError===true,text:toolAnswered&&readingTool(route.host)?context.frame(route.name,answer):answer}
        }
      }), executionSignal)
      if ((!messages || partial.size || !final.trim()) && !(phase === 'author' && toolProposal !== null)) throw failureError('invalid-response','agent')
      return final
      } finally {
        if(phase==='reformat')context.endReformatPhase()
      }
    }
    /**
     * One runtime call the harness makes itself, in the critic's pass (agent#10):
     * the declared cases the critic's run left uncompared. Through the same
     * gated `callTool` and the same per-call deadline as the agent's calls, on
     * the stream as a call and its result, and shown to the recorder.
     */
    const harnessCall = async (name: string, args: Record<string, unknown>, recorder: CritiqueRecorder, left: number): Promise<boolean> => {
      // Past the pass's clock the call is not made, and its case stays incomplete (review row 8).
      if (left <= 0) return false
      const callId = crypto.randomUUID()
      await deliver({ type: 'tool_call', callId, name, args, origin: 'harness' })
      let result: McpToolResult
      let answered = false
      try {
        const limit = Math.min(context.policy.toolCallMilliseconds, left)
        result = await withDeadline(() => callTool(name, args), executionSignal, limit, () => toolTimeout(limit))
        answered = true
      } catch (cause) {
        if (isCancelled(cause) || executionSignal.aborted) throw cause
        result = toolFailureResult(cause)
      }
      const text = textOf(result)
      await deliver({ type: 'tool_result', callId, name, isError: result.isError === true, text,
        ...(result.structuredContent === undefined ? {} : { structured: result.structuredContent }), origin: 'harness' })
      if (answered) recorder.saw(name, text, args, result.structuredContent)
      return true
    }
    const instructions = session.purpose === 'graph' ? GRAPH_SYSTEM : session.purpose === 'brief' ? BRIEF_SYSTEM
      : session.purpose === 'test-design' ? TEST_DESIGN_SYSTEM
      : session.allowConversation ? CONVERSATION_SYSTEM : SYSTEM
    // The agent's prompt ends with the host's changing state and, in a conversation, the execution
    // guidance, headed by STATE_HEADER as the model wires head the state part (agent#36, ADR-0005
    // layer 8), after the request text and a chat's index of earlier results (`run`). Its
    // instructions stay the stable system text.
    const state = [session.context?.currentState, session.allowConversation ? context.execution.guidance() : undefined].filter((part): part is string => !!part).join('\n\n')
    let final: string
    try {
      final = await run(session.prompt, instructions + (offersProposeTool(session.values.hostTools) ? PROPOSE_PACK_INSTRUCTIONS : ''), 'author', undefined, undefined, state, session.values.reasoning)
    } catch (cause) {
      // **A proposal made through propose_pack is finished work.** The run
      // failing after it (the bridge's deadline, a lost connection) does not
      // take it with it: it is delivered with a note naming the failure, and
      // the run ends. A person's Stop still stops; a question to the person
      // leaves the proposal delivered and the turn waiting.
      const made = context.drafts.proposal() ?? toolProposal as Proposal | null
      if (made === null || gate.signal.aborted) throw cause
      const drafts = context.drafts.proposals()
      const candidates = drafts.length ? drafts : [made]
      toolProposal = null
      if (awaitingInput) {
        for (const made of candidates) await deliver({ type: 'proposal', document: made.document, unknowns: made.unknowns, ...(made.edits?{edits:made.edits}:{}) })
        throw cause
      }
      if (isCancelled(cause) || executionSignal.aborted) throw cause
      for (const made of candidates) await deliver({ type: 'proposal', document: made.document, unknowns: made.unknowns, ...(made.edits?{edits:made.edits}:{}), note: proposalNote('turn-unfinished', cause, 'agent') })
      return
    }
    if (session.allowConversation && toolProposal === null && !hasProposalFence(final)) {
      // ADR-0010: nothing to refute; the decision says so.
      await deliver(criticDecidedEvent(context.decideCritic(false)))
      await deliver({ type: 'message', text: proseOf(final) })
      return
    }
    let proposal: Proposal
    /** What the model said beside the document: the first answer's, where a reformat run recovered its proposal. */
    let prose: string
    try {
      // A tool proposal beside a fenced one is ambiguous, refused as two
      // fenced proposals are.
      if (toolProposal !== null && hasProposalFence(final)) throw twoProposals()
      proposal = context.drafts.proposal() ?? toolProposal ?? extractProposal(final)
      prose = proseOf(final)
    } catch (unread) {
      toolProposal = null
      // **One reformat run** (agent#29), tool-free, shown the model its own
      // answer and asked for the same proposal in the shape the contract reads.
      // Nothing finished is lost: whatever it returns, the first answer is
      // delivered whole, the prose and the raw fence, before the run reports
      // why its proposal was not read.
      const keep = async () => {
        const kept = wholeAnswer(final)
        if (kept && !gate.isClosed()) await deliver({ type: 'message', text: kept })
      }
      if (isTerminalProposalSourceRefusal(unread)) { await keep(); throw unread }
      await deliver({ type: 'response_recovery' })
      let reformatted: string
      try {
        reformatted = await run(reformatPrompt(final, unread), instructions, 'reformat')
      } catch (cause) {
        if (isCancelled(cause) || executionSignal.aborted) throw cause
        await keep()
        throw cause
      }
      if (session.allowConversation && !hasProposalFence(reformatted)) {
        // The reformat came back as prose, which a conversation allows: the
        // first answer whole, then what the reformat said, then why the first
        // answer's proposal was not read.
        await keep()
        const said = proseOf(reformatted)
        if (said) await deliver({ type: 'message', text: said })
        throw unread
      }
      try {
        proposal = extractProposal(reformatted)
      } catch {
        await keep()
        throw unread
      }
      prose = proseOf(final) || proseOf(reformatted)
    }
    if (prose) await deliver({ type: 'message', text: prose })
    const decided = context.decideCritic(true)
    await deliver(criticDecidedEvent(decided))
    const drafts=toolProposal === null ? [] : context.drafts.proposals()
    for(const candidate of drafts.length?drafts:[proposal]){
    proposal=candidate
    let critique: Critique | null = null
    let note: ProposalNote | null = null
    // ADR-0010: one decision on every engine, emitted before anything the critic does; on the agent
    // capability the critic has room where the response's invocations leave it one.
    if (decided.ran) {
      critique = criticCannotRun(session.testPrompt)
      if (!critique) {
        const recorder = openCritique({ cases: context.criticCases, document: proposal.document })
        // What the critic costs (agent#10): one agent run on the host's critic
        // model, or the run's; the bridge reports no tokens; and the harness's
        // own calls (review row 8).
        let figures: CriticFigures = { model: session.values.agent.model, unit: 'agent-runs', requests: 0 }
        let harnessCalls = 0
        const counted = (): CriticFigures => ({ ...figures, ...(harnessCalls === 0 ? {} : { harnessCalls }) })
        // The critic's phase spans its run and the harness's calls after it, so
        // both are held to its allow-list (review row 8).
        context.beginCriticPhase()
        const passStart = Date.now()
        try {
          // The host is asked once per pass, and its answer passed down (review row 10).
          // The host's modelsFor answer for the run's connection, asked once per run (review of agent#35, row 5).
          const model = context.models('codex').critic
          figures = { model, unit: 'agent-runs', requests: 1 }
          // ADR-0010: the critic's effort, the decision's tier (at least the author's unless lowered).
          const text = await run(criticMessage(session.testPrompt, proposal.document, context.criticCases), CRITIC_SYSTEM, 'critic', recorder, model === session.values.agent.model ? undefined : model, '', session.values.forRole('critic').reasoning)
          // agent#10: the declared cases the critic's run left uncompared, run once each by the harness.
          const offered = new Set(offeredTo('critic').map(tool => routes.get(tool.name)!.name))
          await compareTheRest(recorder, proposal.document, offered, async (name, args) => {
            if (await harnessCall(name, args, recorder, CRITIC_PASS_MILLISECONDS - (Date.now() - passStart))) harnessCalls += 1
          })
          critique = { ...recorder.critique(text), figures: counted() }
        } catch (cause) {
          // The proposal is finished work: a critic that did not finish is a
          // note on it, never an error raised ahead of it. A Stop still stops.
          if (isCancelled(cause) || executionSignal.aborted) throw cause
          note = criticNote(cause, 'agent', counted())
        } finally {
          context.endCriticPhase()
        }
      }
      if (critique) await deliver(critiqueEvent(critique, context.recordProvider('critic')))
    }
    await deliver({ type: 'proposal', document: proposal.document, unknowns: proposal.unknowns, ...(proposal.edits?{edits:proposal.edits}:{}), ...critiqueOnProposal(critique), ...(note === null ? {} : { note }) })
    }
  }
  const open = () => {
    void drive().catch(async (cause: unknown) => {
      if (!awaitingInput && !isCancelled(cause) && !gate.signal.aborted) await deliver(failureEvent(cause,'agent'))
    }).then(async () => { await deliver({ type: 'end' }); channel.close() })
    return channel.drain()
  }
  return eventIterator({ gate, open })
}

export const codex: Engine = { id: 'codex', start(session: EngineSession) {
  if (!('agent' in session)) throw new Error('Codex requires a subscription agent capability')
  return runCodex(session)
} }
