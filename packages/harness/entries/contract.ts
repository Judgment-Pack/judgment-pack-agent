/**
 * The contract a host sees: the engine slot's types and events (`engine.ts`),
 * what every adapter shares (`engines/contract.ts`), the registry a host loads
 * an adapter through (`engines/index.ts`), the host's own side, its contract
 * values and injection points (`host.ts`), the capability record a host
 * answers with (`capabilities.ts`), the image part (`images.ts`), the
 * chat-scoped context a host makes once per chat (`context/chat.ts`, agent#8)
 * and what a host hands and reads of the critic (`refutation.ts`: its
 * allow-list, the cases it declares, the comparisons and the figures; agent#10).
 */
export * from '../engine'
export * from '../engines/contract'
export * from '../engines/index'
export * from '../host'
export * from '../capabilities'
export * from '../images'
export { ChatContext, canonicalJSON, readCacheKey, type ReadCacheKey, type RuntimeIdentity } from '../context/chat'
export type { ContextInput } from '../context/session'
export { CRITIC_TOOLS, readCriticCases, criticMatrix, criticRuns } from '../refutation'
export type { CriticCase, CaseComparison, CaseOutcome, CriticFigures } from '../refutation'
