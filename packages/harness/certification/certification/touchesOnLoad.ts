/**
 * A certification fixture: an engine that touches the network **as it loads**.
 *
 * The conformance session used to install its sentinels *after* the lazy
 * `import()` of the engine, so a module could reach for `fetch` during its own
 * initialization and never meet one. This is that module. The suite loads it
 * inside the sealed interval and requires the leg to fail by the sentinel's
 * named exception.
 *
 * It is not in `CERTIFIED_ENGINES` and never will be. It exists so the guard
 * can be shown to fail, which is the only reason to believe it holds.
 */
import type { AssistantEvent, AssistantSession, Engine } from '../../engine'

// The whole fixture: one read of a network global, at module scope.
const probed = typeof globalThis.fetch === 'function' ? globalThis.fetch('/anything') : null
void probed

export const touchesOnLoad: Engine = {
  id: 'vercel',
  async *start(_session: AssistantSession): AsyncGenerator<AssistantEvent> {
    yield { type: 'end' }
  }
}

export default touchesOnLoad
