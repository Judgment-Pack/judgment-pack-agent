import {failureError,normalizeFailure,AIFailureError,readFailure} from './failures'
import type { AITarget } from './host'
import { deskSocketURL } from './host'
import { chassisUrl } from './host'
import { socketProtocols } from './host'
import { sessionBearer, whenSessionEnds } from './host'
import { RunCancelled } from './engines/contract'
import type { AgentEvent, AgentRun } from './agent'

const object = (v: unknown): v is Record<string, unknown> => v !== null && typeof v === 'object' && !Array.isArray(v)
const id = (v: unknown): v is string => typeof v === 'string' && /^[a-f0-9]{48}$/.test(v)
const failure = () => failureError('connection-lost','agent')
const serverError = (code: string,detail?:unknown) => Object.assign(new AIFailureError(readFailure(detail)??normalizeFailure({code},'agent')), {code})

/** Bound outside the engine, just like bindModelCall. Captures one constructor,
 * one selected model and a fixed route; never retries a run or changes engines. */
export function bindAgentRun(model: string, target?:AITarget): AgentRun {
  const Socket = globalThis.WebSocket
  const url = new URL(deskSocketURL(chassisUrl('/api/agent/run')), globalThis.location.href)
  if(target?.connectionId){url.searchParams.set('connection',target.connectionId);if(target.connectionRevision)url.searchParams.set('revision',target.connectionRevision)}
  url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:'
  return async (request, callbacks) => {
    if (callbacks.signal.aborted) throw new RunCancelled()
    // agent#10: a run's model is the one bound here, or, on a critic run only,
    // the one the engine passes down from the host's single answer for the
    // pass (criticModel, asked once; review row 10). The bridge admits only a
    // model the subscription lists.
    const chosen = request.model === undefined || request.model === model ? model
      : request.phase === 'critic' ? request.model : null
    if (chosen === null) throw failureError('configuration', 'agent', 'not-started')
    const token = await sessionBearer()
    if (callbacks.signal.aborted) throw new RunCancelled()
    return new Promise<void>((resolve, reject) => {
      const ws = new Socket(url, socketProtocols(token))
      const controller = new AbortController()
      let settled = false
      let runID = ''
      let queued = 0
      let received = 0
      let endQueued = false
      let announcedRunID = ''
      let terminalError: Error | undefined
      let tail = Promise.resolve()
      const seenCalls = new Set<string>()
      const tools = new Set(request.tools.map(tool => tool.name))
      let stopWatching = () => {}
      const finish = (error?: Error) => {
        if (settled) return
        settled = true
        controller.abort()
        callbacks.signal.removeEventListener('abort', aborted)
        stopWatching()
        clearTimeout(deadline)
        ws.close()
        if (error) reject(error)
        else resolve()
      }
      const aborted = () => finish(new RunCancelled())
      const deadline = setTimeout(() => finish(failureError('timeout','agent')), 12 * 60_000)
      const send = (body: unknown) => {
        if (settled || controller.signal.aborted) throw new RunCancelled()
        const text = JSON.stringify(body)
        if (text.length > 2 * 1024 * 1024 || ws.bufferedAmount > 2 * 1024 * 1024) throw failure()
        ws.send(text)
      }
      const handle = async (value: unknown) => {
        if (settled) return
        if (!object(value)) throw failure()
        if (value.type === 'started') {
          if (runID || !id(value.runId)) throw failure()
          runID = value.runId
          return
        }
        if (!runID || value.runId !== runID) throw failure()
        if (value.type === 'event') {
          const event = value.event
          if (object(event) && event.type === 'retry') {
            if (!Number.isInteger(event.attempt) || Number(event.attempt) < 1 || Number(event.attempt) > 2) throw failure()
            await callbacks.event({type: 'retry', attempt: Number(event.attempt)})
            return
          }
          if (!object(event) || !id(event.id) || typeof event.text !== 'string' ||
              !['text', 'message'].includes(String(event.type)) ||
              (event.phase !== undefined && event.phase !== 'commentary' && event.phase !== 'final')) throw failure()
          await callbacks.event({ type: event.type, id: event.id, text: event.text, phase: event.phase } as AgentEvent)
        } else if (value.type === 'tool-call') {
          const call = value.call
          if (!object(call) || !id(call.id) || seenCalls.has(call.id) || seenCalls.size >= 20 ||
              typeof call.name !== 'string' || !tools.has(call.name) || !object(call.arguments)) throw failure()
          seenCalls.add(call.id)
          if (terminalError) return
          let answer
          try { answer = await callbacks.tool({ id: call.id, name: call.name, arguments: call.arguments }, controller.signal) }
          catch (cause) { if (terminalError) return; throw cause }
          if (settled || terminalError) return
          send({ type: 'tool-result', runId: runID, callId: call.id, answer })
        } else if (value.type === 'end') {
          if (typeof value.error !== 'string') throw failure()
          if (value.error === '') finish()
          else finish(serverError(value.error,value.failure))
        } else throw failure()
      }
      callbacks.signal.addEventListener('abort', aborted, { once: true })
      stopWatching = whenSessionEnds(aborted)
      if (callbacks.signal.aborted) { aborted(); return }
      ws.onopen = () => {
        try { send({ type: 'start', request: { ...request, model: chosen } }) }
        catch { finish(failure()) }
      }
      ws.onmessage = event => {
        if (settled) return
        if (typeof event.data !== 'string' || event.data.length > 2 * 1024 * 1024 ||
            (received += event.data.length) > 8 * 1024 * 1024 || ++queued > 64) { finish(failure()); return }
        let parsed: unknown
        try { parsed = JSON.parse(event.data) } catch { finish(failure()); return }
        if (object(parsed) && parsed.type === 'started' && !announcedRunID && id(parsed.runId)) announcedRunID = parsed.runId
        if (object(parsed) && parsed.type === 'end' && announcedRunID && parsed.runId === announcedRunID && typeof parsed.error === 'string') {
          endQueued = true
          if (parsed.error) { terminalError = serverError(parsed.error,parsed.failure); controller.abort() }
        }
        tail = tail.then(async () => { queued--; await handle(parsed) })
          .catch(cause => finish(cause instanceof AIFailureError ? cause : failure()))
      }
      ws.onerror = () => { if (!endQueued) finish(failure()) }
      // A terminal frame can already be queued when the server closes.
      ws.onclose = () => {
        controller.abort()
        if (!endQueued) finish(failure())
        else void tail.then(() => { if (!settled) finish(failure()) })
      }
    })
  }
}
