package runcontext

// ToolContextPolicy is host-declared context behavior; never model arguments or action
// permission (ContextToolPolicy in context/toolPolicy.ts).
type ToolContextPolicy struct {
	// Immutable: the same arguments return the same value: for one run, and, where the run has a
	// chat-scoped context, for as long as the chat lasts (agent#8), from the same Runtime where
	// Runtime is set.
	Immutable bool `json:"immutable,omitempty"`
	// Reference: prefer this reference material in the bounded working summary.
	Reference bool `json:"reference,omitempty"`
	// Runtime: the result depends on the Runtime that serves it, so a chat's read cache keys it by
	// the Runtime's identity and version (agent#8). Every runtime tool is.
	Runtime bool `json:"runtime,omitempty"`
}

// RuntimeToolContextPolicy is runtimeToolContextPolicy: the runtime protocol contracts with known
// immutable semantics. Mutable object reads, evaluations and workflow actions must not acquire
// caching by name.
func RuntimeToolContextPolicy(name string) ToolContextPolicy {
	switch name {
	case "get_schema", "get_dialect_contract", "get_dialect_schema", "get_dialect_reference":
		return ToolContextPolicy{Immutable: true, Reference: true}
	case "get_artifact_version_options":
		return ToolContextPolicy{Immutable: true}
	}
	return ToolContextPolicy{}
}
