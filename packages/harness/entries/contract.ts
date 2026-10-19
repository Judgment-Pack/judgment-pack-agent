/**
 * The contract a host sees: the engine slot's types and events (`engine.ts`),
 * what every adapter shares (`engines/contract.ts`), the registry a host loads
 * an adapter through (`engines/index.ts`), the host's own side, its contract
 * values and injection points (`host.ts`), the capability record a host
 * answers with (`capabilities.ts`) and the image part (`images.ts`).
 */
export * from '../engine'
export * from '../engines/contract'
export * from '../engines/index'
export * from '../host'
export * from '../capabilities'
export * from '../images'
