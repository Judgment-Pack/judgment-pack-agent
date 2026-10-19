/**
 * `deepagents`: the third adapter of the wire contract, on LangChain's Deep Agents (ADR-0001, step 3;
 * ADR-0003).
 *
 * One object implementing one contract, like the other two. Its loop is Deep Agents' agent graph
 * (`deepagents/browser`, `createDeepAgent`), with Deep Agents' own tools and subagents off; its model
 * is a LangChain chat model whose only reach is the host's relay capability; every tool call it makes
 * is answered by its boundary through the host's gate. See `loop.ts`.
 */
import { runDeepAgents } from './loop'
import type { AssistantEvent, EngineSession, Engine } from '../../engine'

export const deepagents: Engine = {
  id: 'deepagents',
  start(session: EngineSession): AsyncIterable<AssistantEvent> {
    if ('agent' in session) throw new Error('Deep Agents requires a model endpoint capability')
    return runDeepAgents(session, deepagents.id)
  }
}

export default deepagents
