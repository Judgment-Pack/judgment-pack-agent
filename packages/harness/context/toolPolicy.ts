/** Host-declared context behavior; never model arguments or action permission. */
export interface ContextToolPolicy {
 /** The same arguments return the same value: for one run, and, where the run has a chat-scoped context, for as long as the chat lasts (agent#8), from the same Runtime where `runtime` is set. */
 immutable?:boolean
 /** Prefer this reference material in the bounded working summary. */
 reference?:boolean
 /** The result depends on the Runtime that serves it, so a chat's read cache keys it by the Runtime's identity and version (agent#8). Every runtime tool is. */
 runtime?:boolean
}
/** Runtime protocol contracts with known immutable semantics. Mutable object
 * reads, evaluations and workflow actions must not acquire caching by name. */
export function runtimeToolContextPolicy(name:string):ContextToolPolicy {
 return ['get_schema','get_dialect_contract','get_dialect_schema','get_dialect_reference'].includes(name)?{immutable:true,reference:true}:name==='get_artifact_version_options'?{immutable:true}:{}
}
