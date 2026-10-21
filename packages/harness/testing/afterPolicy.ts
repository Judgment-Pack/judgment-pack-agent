import type { AssistantEvent, Engine } from '../engine'

/** Adapter lifecycle tests start after admission; scenario 11 checks the full policy stream.
 * Forward cancellation directly, including while next() is pending. */
export function afterPolicy<T extends Engine>(engine: T): T {
  return {...engine, start(session: Parameters<T['start']>[0]) {
    return {[Symbol.asyncIterator]() {
      // Legacy streaming lifecycle is tested with the output boundary explicitly off.
      // Scenario 17 exercises the default guarded stream.
      const inner = engine.start({...session,policy:{outputGuard:'off',...session.policy}})[Symbol.asyncIterator]()
      return {
        async next(): Promise<IteratorResult<AssistantEvent>> {
          let result = await inner.next()
          while (!result.done && result.value.type === 'policy_resolved') result = await inner.next()
          return result
        },
        return: inner.return?.bind(inner),
        throw: inner.throw?.bind(inner)
      }
    }}
  }} as T
}
