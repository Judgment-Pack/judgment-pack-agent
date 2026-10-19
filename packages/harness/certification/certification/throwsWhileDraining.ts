/**
 * A certification fixture whose deferred callback throws.
 *
 * The cleanup used to be one `finally` with the drain first in it, so a
 * callback that threw left the block before the network sentinels and the timer
 * wrappers came off — and a leg that failed could poison every leg after it
 * with globals nobody restored. The blocks are nested now, and this is what
 * that is measured against: the throw is reported, and every global is back.
 *
 * It is not in `CERTIFIED_ENGINES` and never will be.
 */
import type { AssistantEvent, AssistantSession, Engine } from '../../engine'

export const throwsWhileDraining: Engine = {
  id: 'vercel',
  async *start(_session: AssistantSession): AsyncGenerator<AssistantEvent> {
    setTimeout(() => {
      throw new Error('an engine that threw from a callback nobody was awaiting')
    }, 10)
    yield { type: 'end' }
  }
}

export default throwsWhileDraining
