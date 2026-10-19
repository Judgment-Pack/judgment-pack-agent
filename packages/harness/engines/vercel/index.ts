/**
 * `vercel` — the default engine ADR-0001 names, and the second the desk ships.
 *
 * One object implementing one contract. Everything it can reach passes through
 * a seam the desk owns: `callTool` is bound through the ToolGate, and its model
 * traffic goes to the chassis relay with no credential of its own — the SDK's
 * providers are constructed against a placeholder origin and a `fetch` that is
 * the desk's own capability. Swapping this engine for a second one changes
 * none of those seams, which is what the slot is for.
 */
import { runVercel } from './loop'
import type { AssistantEvent, EngineSession, Engine } from '../../engine'

export const vercel: Engine = {
  id: 'vercel',
  start(session: EngineSession): AsyncIterable<AssistantEvent> {
    if ('agent' in session) throw new Error('Vercel requires a model endpoint capability')
    // The id travels with the session: what a loop removes from a schema is a
    // property of the engine, and the loop must not decide which engine it is.
    return runVercel(session, vercel.id)
  }
}

export default vercel
