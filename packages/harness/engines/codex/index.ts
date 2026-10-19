import {failureEvent,failureError,toolFailureResult} from '../../failures'
import {managedContext,contextOf} from '../../context/managed'
import { readDelegationRequest } from '../../delegationContract'
import { readModelQuestion } from '../../questions'
import { questionFailureResult } from '../../questionFailure'
import { assistantLanguageInstructions } from '../../host'
import type { AssistantEvent, AgentAssistantSession, EngineSession, Engine, McpToolResult } from '../../engine'
import type { AgentTool, AgentEvent } from '../../agent'
import {
  SYSTEM, CONVERSATION_SYSTEM, TEST_DESIGN_SYSTEM, BRIEF_SYSTEM, GRAPH_SYSTEM,
  openRun, eventIterator, guardedCallTool, withAbort, isCancelled,
  servedSchema, textOf, streamingProse, proseOf, hasProposalFence, extractProposal
} from '../contract'
import { eventChannel } from '../vercel/channel'
import { CRITIC_SYSTEM, criticMessage, criticCannotRun, critiqueEvent, critiqueOnProposal, openCritique } from '../../refutation'
import type { CritiqueRecorder, Critique } from '../../refutation'

/** Candidate contract. No fake model endpoint, and no selectable registry entry
 * until provider configuration, UI readiness and live certification are done. */
export type CodexSession = AgentAssistantSession

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
    const run = async (prompt: string, instructions: string, phase: 'author' | 'critic', recorder?: CritiqueRecorder) => {
      if(phase==='critic')context.beginIsolatedPhase()
      instructions += assistantLanguageInstructions(session.replyLanguage)
      prompt = await context.prompt(prompt,instructions,tools,phase==='author'?session.context?.currentRequest:undefined)
      context.check(instructions,prompt,tools)
      const partial = new Map<string, string>()
      let final = ''
      let messages = 0
      await withAbort(() => session.agent.run({ prompt, instructions, tools: phase === 'critic' ? tools.filter(tool => !routes.get(tool.name)?.host?.interaction && routes.get(tool.name)?.host?.effect!=='memory' && !['update_plan','request_budget_increase'].includes(routes.get(tool.name)?.name??'')) : tools, effort: session.effort, phase }, {
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
          if (!route) throw new Error('Codex requested an unoffered tool')
          if (!route.host?.interaction) await deliver({ type: 'tool_call', callId: call.id, name: route.name, args: call.arguments })
          let result: McpToolResult
          let runtimeAnswered = false
          try {
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
            result = route.host
              ? await withAbort(() => route.host!.execute(call.arguments, toolSignal), toolSignal)
              : await withAbort(() => callTool(route.name, call.arguments), toolSignal)
            runtimeAnswered = !route.host
          } catch (cause) {
            if (isCancelled(cause) || executionSignal.aborted) throw cause
            result = route.host?.interaction === 'question' ? questionFailureResult(cause) : toolFailureResult(cause)
          }
          if (awaitingInput) throw new DOMException('Waiting for input', 'AbortError')
          if (toolSignal.aborted) throw new DOMException('Stopped', 'AbortError')
          const text = textOf(result)
          await deliver({ type: 'tool_result', callId: call.id, name: route.name, isError: result.isError === true, text,
            ...(result.structuredContent === undefined ? {} : { structured: result.structuredContent }) })
          if (runtimeAnswered) recorder?.saw(route.name, text)
          // Text-only host callback wire, including structured results in a
          // JSON envelope. The visible result keeps its existing Desk shape.
          const projected = await context.result(route.name,result)
          return {isError:result.isError===true,text:projected.structuredContent===undefined?textOf(projected):JSON.stringify(projected)}
        }
      }), executionSignal)
      if (!messages || partial.size || !final.trim()) throw failureError('invalid-response','agent')
      return final
    }
    const instructions = session.purpose === 'graph' ? GRAPH_SYSTEM : session.purpose === 'brief' ? BRIEF_SYSTEM
      : session.purpose === 'test-design' ? TEST_DESIGN_SYSTEM
      : session.allowConversation ? CONVERSATION_SYSTEM : SYSTEM
    // The agent's prompt carries the host's changing state and the execution guidance at its end;
    // its instructions stay the stable system text.
    const prompt = session.prompt + (session.context?.currentState ? '\n\n' + session.context.currentState : '') + (session.allowConversation ? '\n\n' + context.execution.guidance() : '')
    const final = await run(prompt, instructions, 'author')
    if (session.allowConversation && !hasProposalFence(final)) {
      await deliver({ type: 'message', text: proseOf(final) })
      return
    }
    const proposal = extractProposal(final)
    const prose = proseOf(final)
    if (prose) await deliver({ type: 'message', text: prose })
    let critique: Critique | null = null
    if (session.adversarialReview) {
      critique = criticCannotRun(session.testPrompt)
      if (!critique) {
        const recorder = openCritique()
        const text = await run(criticMessage(session.testPrompt, proposal.document), CRITIC_SYSTEM, 'critic', recorder)
        critique = recorder.critique(text)
      }
      await deliver(critiqueEvent(critique))
    }
    await deliver({ type: 'proposal', document: proposal.document, unknowns: proposal.unknowns, ...critiqueOnProposal(critique) })
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
