package runcontext

// The tool context policy (the port of context/toolPolicy.test.ts).

import "testing"

func TestOnlyImmutableDialectReferencesAreRetained(t *testing.T) {
	for _, name := range []string{"get_dialect_contract", "get_dialect_schema", "get_dialect_reference"} {
		if held := RuntimeToolContextPolicy(name); held != (ToolContextPolicy{Immutable: true, Reference: true}) {
			t.Errorf("%s has %+v", name, held)
		}
	}
	for _, name := range []string{"dialect_validate", "dialect_check", "dialect_digest", "dialect_canonicalize", "dialect_init", "read_dialect"} {
		if held := RuntimeToolContextPolicy(name); held != (ToolContextPolicy{}) {
			t.Errorf("%s has %+v", name, held)
		}
	}
	// The pack schema is immutable reference material; the artifact's version options are
	// immutable but are not reference material.
	if held := RuntimeToolContextPolicy("get_schema"); held != (ToolContextPolicy{Immutable: true, Reference: true}) {
		t.Errorf("get_schema has %+v", held)
	}
	if held := RuntimeToolContextPolicy("get_artifact_version_options"); held != (ToolContextPolicy{Immutable: true}) {
		t.Errorf("get_artifact_version_options has %+v", held)
	}
}
