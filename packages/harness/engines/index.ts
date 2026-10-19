/**
 * The engine registry: one lazily loaded chunk per certified engine.
 *
 * ADR-0001 makes the engine a slot and certification the price of shipping one:
 * an engine is here only once it has passed the desk's conformance session,
 * which is `assistant/conformance/` and runs in CI against **this list**. So
 * adding an adapter is one line here plus its module — and the suite certifies
 * it or the suite goes red.
 *
 * **There is one engine on the list, and the list is still the mechanism.** The
 * built-in fallback was withdrawn (ADR-0001's amendment); what a second engine
 * will be admitted by is unchanged, and it is this table plus the session that
 * runs over it.
 *
 * **Every engine a `desk.json` may name is certified in this build, and there is
 * one table that says so.** The loaders are total over `AssistantEngine`, the
 * certified list is *derived* from them rather than written beside them, and the
 * two are asserted equal at the type level — so an id cannot become loadable
 * without being put in front of the conformance session, and an id the decoder
 * declares cannot be left without an adapter. The substitution this used to do —
 * a desk configured for one engine running another, and a line in the tab saying
 * so — is gone with the reason for it.
 *
 * The loaders are `import()` so a session downloads one chunk. The release
 * grows by the sum of certified engines; a session does not.
 */
import type { AssistantEngine } from '../host'
import type { Engine } from '../engine'

/** One engine's chunk, by id. */
export type EngineLoaders = Record<string, () => Promise<Engine>>

/**
 * **The one table.** Every id this build can load, and nothing else.
 *
 * `satisfies` makes it total over `AssistantEngine` — a declared engine with no
 * chunk does not compile — and, because this is an object literal, refuses an id
 * the decoder does not declare as an excess property. So the table cannot drift
 * from the decoder in either direction.
 */
const LOADERS = {
  codex: async () => (await import('./codex')).codex,
  vercel: async () => (await import('./vercel')).vercel
} satisfies Record<AssistantEngine, () => Promise<Engine>>

export type CertifiedEngine = keyof typeof LOADERS

/**
 * The engines this build carries, in the order the conformance suite runs them.
 *
 * **Derived from the table rather than written beside it.** The two used to be
 * independent lists: adding a third engine and its loader while forgetting this
 * one compiled, let a `desk.json` select it, and left `describe.each` never
 * certifying it — a build that could run an engine the suite had never seen.
 */
export const CERTIFIED_ENGINES = Object.keys(LOADERS) as CertifiedEngine[]
/** Each transport has its own protocol matrix and shares the recorded JPS scenario. */
export const ENGINE_CAPABILITIES = { vercel: 'model', codex: 'agent' } as const satisfies Record<AssistantEngine, 'model' | 'agent'>
export const MODEL_ENGINES = CERTIFIED_ENGINES.filter(id => ENGINE_CAPABILITIES[id] === 'model')
export const AGENT_ENGINES = CERTIFIED_ENGINES.filter(id => ENGINE_CAPABILITIES[id] === 'agent')

/** True only where two key sets are the same set, both ways round. */
type Exactly<A, B> = [A] extends [B] ? ([B] extends [A] ? true : false) : false

/**
 * **Loadable and certified are the same set, asserted at the type level.**
 *
 * The `satisfies` above says the table covers every declared engine; this says
 * the certified list covers every loadable one. Together they are the sentence
 * ADR-0001 asks for: an engine ships only once it has passed the desk's
 * conformance session, and there is no way to make one loadable without putting
 * it in front of the suite.
 */
export const CERTIFICATION_IS_TOTAL: Exactly<AssistantEngine, CertifiedEngine> = true

/**
 * Load one engine's chunk.
 *
 * `loaders` exists for the **conformance session's certification fixtures**,
 * and for nothing else. Those fixtures are engines the desk would never ship —
 * one reaches for the network as it loads, one after it has finished — and the
 * point of them is to be loaded down the path a certified engine takes, under
 * the same seal, rather than by a test import that proves a different route. It
 * defaults to this build's own table, so the page has one registry and nothing
 * a `desk.json` can name reaches anything else.
 */
export async function loadEngine(
  id: string,
  loaders: EngineLoaders = LOADERS
): Promise<Engine> {
  const load = loaders[id]
  if (load === undefined) throw new Error(`no engine chunk is registered for ${id}`)
  return load()
}
