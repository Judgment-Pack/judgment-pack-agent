import {failureEvent,failureError,toolFailureResult} from '../../failures'
import {managedContext,contextOf} from '../../context/managed'
import { readDelegationRequest } from '../../delegationContract'
import { readModelQuestion } from '../../questions'
import { questionFailureResult } from '../../questionFailure'
import { assistantLanguageInstructions } from '../../host'
import { separateImages } from '../../images'
import { DATA_RULE, readingTool } from '../../framing'
import { GateViolation } from '../../toolGate'
import type { AssistantEvent, AgentAssistantSession, EngineSession, Engine, McpToolResult, ProposalNote } from '../../engine'
import type { AgentTool, AgentEvent } from '../../agent'
import {
  SYSTEM, CONVERSATION_SYSTEM, TEST_DESIGN_SYSTEM, BRIEF_SYSTEM, GRAPH_SYSTEM,
  openRun, eventIterator, guardedCallTool, withAbort, isCancelled,
  servedSchema, textOf, streamingProse, proseOf, hasProposalFence, extractProposal,
  wholeAnswer, withDeadline, toolTimeout, PROPOSE_PACK_TOOL, PROPOSE_PACK_INSTRUCTIONS, PROPOSAL_RECEIVED,
  offersProposeTool, readProposedPack, twoProposals, reformatRequest, refusedUnoffered, unofferedRefusal, STATE_HEADER, type Proposal
} from '../contract'
import { eventChannel } from '../vercel/channel'
import { CRITIC_SYSTEM, compareTheRest, criticMayCall, criticMayRead, criticMessage, criticCannotRun, criticRuns, critiqueEvent, critiqueOnProposal, criticNote, proposalNote, openCritique } from '../../refutation'
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

export function runCodex(session:CodexSession):AsyncIterable<AssistantEvent>{return managedContext(session,runCodexLoop)}
function runCodexLoop(session: CodexSession): AsyncIterable<AssistantEvent> {
  const context=contextOf(session)
  let closeRun = () => {}
  const channel = eventChannel({ onAbandon: () => closeRun() })
  const gate = openRun(session, () => channel.abandon())
  closeRun = gate.close
  const inputPause = new AbortController()
  const executionSignal = AbortSignal.any([gate.signal, inputPause.signal])
  let awaitingInput = false
  const callTool = guardedCallTool(session, executionSignal)
  const deliver = (event: AssistantEvent) => channel.push(event)
  const drive = async () => {
    /** A proposal delivered through `propose_pack`; the fence is the fallback. */
    let toolProposal: Proposal | null = null
    const names = new Set<string>()
    const routes = new Map<string, { name: string; host?: CodexSession['hostTools'][number] }>()
    const tools: AgentTool[] = []
    for (const [kind, offered] of [['runtime', session.tools], ['host', session.hostTools]] as const) {
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
          : tools.filter(tool => { const route = routes.get(tool.name); return route !== undefined && (route.host === undefined ? criticMayCall(route.name) : criticMayRead(route.name)) })
    const run = async (prompt: string, instructions: string, phase: 'author' | 'critic' | 'reformat', recorder?: CritiqueRecorder, model?: string) => {
      const offeredHere = offeredTo(phase)
      const offeredNames = new Set(offeredHere.map(tool => tool.name))
      // The critic's phase is its caller's (it spans the harness's own calls after
      // the run); the reformat run's is this run's: every call refused (review row 5).
      if(phase==='reformat')context.beginReformatPhase()
      try {
      instructions += DATA_RULE + assistantLanguageInstructions(session.replyLanguage)
      prompt = await context.prompt(prompt,instructions,offeredHere,phase==='author'?session.context?.currentRequest:undefined)
      if(phase==='author')prompt=context.indexed(prompt,instructions,offeredHere)
      context.check(instructions,prompt,offeredHere)
      const partial = new Map<string, string>()
      let final = ''
      let messages = 0
      await withAbort(() => session.agent.run({ prompt, instructions, tools: offeredHere, effort: session.effort, phase: phase === 'critic' ? 'critic' : 'author', ...(model === undefined ? {} : { model }) }, {
        signal: executionSignal,
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
              ? unofferedRefusal(context.refuseUnoffered(name).message)
              : await refusedUnoffered(callTool, name, call.arguments)
            if (toolSignal.aborted) throw new DOMException('Stopped', 'AbortError')
            await deliver({ type: 'tool_result', callId: call.id, name, isError: true, text })
            return { isError: true, text }
          }
          if (!route.host?.interaction) await deliver({ type: 'tool_call', callId: call.id, name: route.name, args: call.arguments })
          let result: McpToolResult
          let runtimeAnswered = false
          /** The tool itself answered: not a failure the harness wrote, not propose_pack's acknowledgement. */
          let toolAnswered = false
          try {
            // agent#10: the critic's allow-list, before anything of the host's runs.
            context.admit(route.name, route.host ? 'host' : 'runtime')
            if (route.host?.interaction === 'delegation') {
              const request = readDelegationRequest(call.arguments)
              awaitingInput = true
              await deliver({type:'delegation_requested',request})
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
              toolProposal = readProposedPack(call.arguments)
              result = { content: [{ type: 'text', text: PROPOSAL_RECEIVED }] }
            } else {
              result = route.host
                ? await withDeadline((signal) => route.host!.execute(call.arguments, signal), toolSignal, limit, () => toolTimeout(limit))
                : await withDeadline(() => callTool(route.name, call.arguments), toolSignal, limit, () => toolTimeout(limit))
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
          // agent#10: the arguments and the structured answer, which the outcome comparison reads.
          if (runtimeAnswered) recorder?.saw(route.name, text, call.arguments, result.structuredContent)
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
    // The agent's prompt carries the host's changing state and the execution guidance at its end,
    // after the person's message and headed by STATE_HEADER, as the relayed wires head the state
    // part: a host that takes its recall query from the prompt cuts at the header and keeps the
    // message (agent#36). Its instructions stay the stable system text. A chat's index of the
    // results earlier runs kept (agent#8) is added after both, in the room it leaves (`run`).
    const appended = [session.context?.currentState, session.allowConversation ? context.execution.guidance() : undefined].filter((part): part is string => !!part)
    const prompt = session.prompt + (appended.length > 0 ? '\n\n' + STATE_HEADER + '\n' + appended.join('\n\n') : '')
    let final: string
    try {
      final = await run(prompt, instructions + (offersProposeTool(session.hostTools) ? PROPOSE_PACK_INSTRUCTIONS : ''), 'author')
    } catch (cause) {
      // **A proposal made through propose_pack is finished work.** The run
      // failing after it (the bridge's deadline, a lost connection) does not
      // take it with it: it is delivered with a note naming the failure, and
      // the run ends. A person's Stop still stops; a question to the person
      // leaves the proposal delivered and the turn waiting.
      const made = toolProposal as Proposal | null
      if (made === null || gate.signal.aborted) throw cause
      toolProposal = null
      if (awaitingInput) {
        await deliver({ type: 'proposal', document: made.document, unknowns: made.unknowns })
        throw cause
      }
      if (isCancelled(cause) || executionSignal.aborted) throw cause
      await deliver({ type: 'proposal', document: made.document, unknowns: made.unknowns, note: proposalNote('turn-unfinished', cause, 'agent') })
      return
    }
    if (session.allowConversation && toolProposal === null && !hasProposalFence(final)) {
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
      proposal = toolProposal ?? extractProposal(final)
      prose = proseOf(final)
    } catch (unread) {
      // **One reformat run** (agent#29), tool-free, shown the model its own
      // answer and asked for the same proposal in the shape the contract reads.
      // Nothing finished is lost: whatever it returns, the first answer is
      // delivered whole, the prose and the raw fence, before the run reports
      // why its proposal was not read.
      const keep = async () => {
        const kept = wholeAnswer(final)
        if (kept && !gate.isClosed()) await deliver({ type: 'message', text: kept })
      }
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
    let critique: Critique | null = null
    let note: ProposalNote | null = null
    if (criticRuns(session, context.policy)) {
      critique = criticCannotRun(session.testPrompt)
      if (!critique) {
        const recorder = openCritique({ cases: context.criticCases, document: proposal.document })
        // What the critic costs (agent#10): one agent run on the host's critic
        // model, or the run's; the bridge reports no tokens; and the harness's
        // own calls (review row 8).
        let figures: CriticFigures = { model: session.agent.model, unit: 'agent-runs', requests: 0 }
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
          const text = await run(criticMessage(session.testPrompt, proposal.document, context.criticCases), CRITIC_SYSTEM, 'critic', recorder, model === session.agent.model ? undefined : model)
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
      if (critique) await deliver(critiqueEvent(critique))
    }
    await deliver({ type: 'proposal', document: proposal.document, unknowns: proposal.unknowns, ...critiqueOnProposal(critique), ...(note === null ? {} : { note }) })
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
