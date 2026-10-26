package wire

import (
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

// The one thing the desk does to a served schema, and the whole of it (geminiSchema.ts).
//
// The rule everywhere else is that the runtime's inputSchema travels untouched: the model is shown
// the contract the runtime enforces or it is shown nothing. Gemini retains the desk's
// compatibility policy originally introduced for the OpenAPI parameters field — a closed,
// documented removal list, on this family only. Each removal makes the contract the model is shown
// wider than the one the runtime enforces, and nothing is added, re-typed or rewritten. A keyword
// this list does not name is never removed: an endpoint that refuses one is reported by name.

// EngineID is this adapter's engine id, as the schema functions and the narrowing notices read it.
// The Go adapter shows the model what the deepagents adapter shows: the desk's own removals and
// nothing of a provider SDK's.
const EngineID = "eino"

// GeminiSchemaRemovals is GEMINI_SCHEMA_REMOVALS: the keywords removed from a served schema on the
// gemini family, and the whole of them. `oneOf` is deliberately absent — a union removed reads as
// "anything at all", so it travels and an endpoint that refuses it is reported by name.
var GeminiSchemaRemovals = []string{
	"$schema",
	"$id",
	"additionalProperties",
	"const",
	"examples",
	"patternProperties",
}

// SDKSchemaRemovals is SDK_SCHEMA_REMOVALS: the extra keywords the pinned Vercel AI SDK's Google
// provider removes underneath that engine — none. It is kept because KeywordsNotShown reads it for
// that engine id; this adapter has no provider SDK below it.
var SDKSchemaRemovals = []string{}

// nameMaps is NAME_MAPS: the keywords whose keys are names somebody wrote rather than keywords.
// A schema with a definition called `const` must keep it: a dangling reference is worse than the
// keyword this list exists to take out. patternProperties is here for the same reason and is on the
// removal list besides.
var nameMaps = []string{
	"properties",
	"$defs",
	"definitions",
	"dependentSchemas",
	"dependentRequired",
	"patternProperties",
}

// namesUnder reports whether this key's own object holds authored names rather than keywords.
func namesUnder(key string) bool { return slices.Contains(nameMaps, key) }

// WithoutKeywords is withoutKeywords: the served schema minus exactly the keywords given, at every
// depth, and nothing copied that does not have to be — a subtree with no removal in it is returned
// as it stands, so the value the runtime served is the value that travels wherever this changed
// nothing. The places a key is not a keyword are the name maps, where nothing is removed and only
// the schemas the names point at are descended into.
func WithoutKeywords(schema *Value, removals []string, inNameMap bool) *Value {
	if schema.Kind() == KindArray {
		items := schema.Items()
		walked := make([]*Value, len(items))
		changed := false
		for at, item := range items {
			walked[at] = WithoutKeywords(item, removals, false)
			if walked[at] != item {
				changed = true
			}
		}
		if !changed {
			return schema
		}
		return Arr(walked...)
	}
	if !schema.IsObject() {
		return schema
	}
	out := Obj()
	changed := false
	for _, field := range schema.Members() {
		if !inNameMap && slices.Contains(removals, field.Name) {
			changed = true
			continue
		}
		// A key inside a name map is a name, so its value is an ordinary schema: reading namesUnder
		// on it would ask whether the author's word is a name-map keyword.
		descend := false
		if !inNameMap {
			descend = namesUnder(field.Name)
		}
		walked := WithoutKeywords(field.Value, removals, descend)
		if walked != field.Value {
			changed = true
		}
		out.Set(field.Name, walked)
	}
	if !changed {
		return schema
	}
	return out
}

// WithoutUnsupportedKeywords is withoutUnsupportedKeywords: the desk's own removal, which is the
// one every engine on this family makes.
func WithoutUnsupportedKeywords(schema *Value) *Value {
	return WithoutKeywords(schema, GeminiSchemaRemovals, false)
}

// KeywordsNotShown is keywordsNotShown: the keywords one engine's model never sees, on one family
// — the desk's closed list on every engine, plus what a provider removes underneath on the
// SDK-backed one. Nothing at all on the two families whose wires take JSON Schema as written.
func KeywordsNotShown(engine string, family EndpointKind) []string {
	if family != Gemini {
		return nil
	}
	if engine == "vercel" {
		return append(append([]string{}, GeminiSchemaRemovals...), SDKSchemaRemovals...)
	}
	return GeminiSchemaRemovals
}

// SchemaShown is schemaShown: the schema this engine's model is actually shown, out of the one the
// runtime served.
func SchemaShown(engine string, family EndpointKind, served *Value) *Value {
	removals := KeywordsNotShown(engine, family)
	if len(removals) == 0 {
		return served
	}
	return WithoutKeywords(served, removals, false)
}

// KeywordsLost is keywordsLost: the keywords one tool loses beyond the desk's own declared ruling,
// or none — what the desk shows minus what this engine shows, so a notice means "your contract
// lost something this desk did not already tell you about in the README".
func KeywordsLost(engine string, family EndpointKind, served *Value) []string {
	if len(KeywordsNotShown(engine, family)) == 0 {
		return nil
	}
	desk := WithoutUnsupportedKeywords(served)
	shown := SchemaShown(engine, family, desk)
	before := KeywordsSent(desk)
	after := KeywordsSent(shown)
	var lost []string
	for keyword := range before {
		if !after[keyword] {
			lost = append(lost, keyword)
		}
	}
	sort.Strings(lost)
	return lost
}

// NarrowingNotice is narrowingNotice: the line the author reads where a tool's contract was
// narrowed for the model. Never silent: one line per tool that actually lost something, at the
// start of the run, before the model is asked anything.
func NarrowingNotice(keywords []string) string {
	sorted := append([]string{}, keywords...)
	sort.Strings(sorted)
	return fmt.Sprintf("shown to the model without: %s. This engine removes "+
		"those keywords; the ToolGate and the runtime hold the "+
		"contract the runtime actually enforces, whatever the model was shown.", strings.Join(sorted, ", "))
}

// KeywordsSent is keywordsSent: every keyword the desk actually sent in one schema, at every
// depth. Authored names are excluded for the reason the walk excludes them — the same name maps,
// so the two classifications cannot disagree.
func KeywordsSent(schema *Value) map[string]bool {
	found := map[string]bool{}
	collectKeywords(schema, found, false)
	return found
}

func collectKeywords(schema *Value, found map[string]bool, inNameMap bool) {
	if schema.Kind() == KindArray {
		for _, item := range schema.Items() {
			collectKeywords(item, found, false)
		}
		return
	}
	if !schema.IsObject() {
		return
	}
	for _, field := range schema.Members() {
		if !inNameMap {
			found[field.Name] = true
		}
		descend := false
		if !inNameMap {
			descend = namesUnder(field.Name)
		}
		collectKeywords(field.Value, found, descend)
	}
}

// RefusedSchemaKeyword is refusedSchemaKeyword: the schema keyword an endpoint refused, or "".
// Two conditions, and both are required: a 400, and a message naming a keyword this desk actually
// sent. The longest match wins, because `properties` contains `type` and a shorter name matching
// first would report the wrong keyword to a person about to go and look at it.
func RefusedSchemaKeyword(status int, message string, schemas []*Value) string {
	if status != 400 {
		return ""
	}
	sent := map[string]bool{}
	for _, schema := range schemas {
		collectKeywords(schema, sent, false)
	}
	var named []string
	for keyword := range sent {
		if strings.Contains(message, keyword) {
			named = append(named, keyword)
		}
	}
	if len(named) == 0 {
		return ""
	}
	// The longest first, and a stable order among equals so two runs answer alike.
	sort.Slice(named, func(i, j int) bool {
		if len(named[i]) != len(named[j]) {
			return len(named[i]) > len(named[j])
		}
		return named[i] < named[j]
	})
	return named[0]
}

// RefusedSchemaSentence is refusedSchemaSentence: the sentence a person reads when an endpoint
// refuses a keyword the desk sent. An error and not a strip — the removal list is closed and
// reviewed, and a desk that widened it at run time would be a desk whose idea of the runtime's
// contract is decided by whichever endpoint complained last.
func RefusedSchemaSentence(keyword string) string {
	return fmt.Sprintf("the endpoint refused the tool schema over %s, which the runtime "+
		"served and this desk does not remove. The removal list for this wire is closed and "+
		"documented (%s); nothing was written, and nothing was "+
		"stripped from the contract the runtime enforces.", jsQuote(keyword), strings.Join(GeminiSchemaRemovals, ", "))
}

// McpTool is one tool as the runtime served it, for the schema functions (McpTool in engine.ts, in
// the members they read).
type McpTool struct {
	Meta        *ToolTaxonomy
	Taxonomy    *ToolTaxonomy
	Annotations map[string]bool
	Name        string
	Description string
	// InputSchema is the schema the runtime served, or nil where it served none.
	InputSchema *Value
}

// ServedSchema is servedSchema: the schema the runtime served for one tool, or a refusal. The desk
// never invents a contract the runtime does not enforce — the model is shown the contract the
// runtime enforces or it is shown nothing, and a session that cannot show it does not run.
func ServedSchema(tool McpTool) (*Value, error) {
	if tool.InputSchema == nil || tool.InputSchema.Kind() == KindNull {
		return nil, failure.Authored("configuration",
			fmt.Sprintf("the runtime served %s without an input schema, and this desk will not write "+
				"one for it: the model is shown the contract the runtime enforces or it is shown "+
				"nothing. Nothing was written.", tool.Name), "desk")
	}
	return tool.InputSchema, nil
}

// ShownSchema is shownSchema: the schema a model is shown for one tool, as its endpoint's
// capability record says the endpoint takes them — the served schema, or the Gemini subset.
func ShownSchema(schemas ToolSchemas, tool McpTool) (*Value, error) {
	schema, err := ServedSchema(tool)
	if err != nil {
		return nil, err
	}
	if schemas == GeminiSubset {
		return WithoutUnsupportedKeywords(schema), nil
	}
	return schema, nil
}

// ServedSchemaFor is servedSchemaFor: the schema one family is shown, out of the one the runtime
// served — two families are shown the runtime's schema and the third is shown it minus the closed
// list. This is ShownSchema for the family's default record.
func ServedSchemaFor(family EndpointKind, tool McpTool) (*Value, error) {
	if family == Gemini {
		return ShownSchema(GeminiSubset, tool)
	}
	return ShownSchema(JSONSchema, tool)
}

// SchemasShown is schemasShown: every schema this session put in front of the model, and only
// those, read by the refusal classifier. The provider's own tool payload is deliberately not what
// it is given: that carries the wrapper's words, and a classifier reading it would report the
// wrapper instead of the keyword. `schemas` is a family or a record's toolSchemas, as the
// TypeScript takes either.
func SchemasShown(schemas string, tools []McpTool) ([]*Value, error) {
	shape := JSONSchema
	if schemas == string(Gemini) || schemas == string(GeminiSubset) {
		shape = GeminiSubset
	}
	shown := make([]*Value, 0, len(tools))
	for _, tool := range tools {
		schema, err := ShownSchema(shape, tool)
		if err != nil {
			return nil, err
		}
		shown = append(shown, schema)
	}
	return shown, nil
}

// NarrowingEvents is narrowingEvents: the narrowing notices one session owes its author, before
// the model is asked anything — one guardrail per tool that actually loses a keyword on this
// engine and this family, and none at all where nothing is lost. A tool the runtime served without
// a schema is not this function's refusal to make: ServedSchema refuses it where the session
// always ended.
func NarrowingEvents(engine string, family EndpointKind, tools []McpTool) []contract.RunEvent {
	var events []contract.RunEvent
	for _, tool := range tools {
		if tool.InputSchema == nil || tool.InputSchema.Kind() == KindNull {
			continue
		}
		lost := KeywordsLost(engine, family, tool.InputSchema)
		if len(lost) == 0 {
			continue
		}
		events = append(events, contract.RunEvent{Guardrail: &contract.RunEventGuardrail{
			Type:   "guardrail",
			Tool:   tool.Name,
			Action: "narrowed",
			Detail: NarrowingNotice(lost),
		}})
	}
	return events
}

// IsEventStream is isEventStream: whether an answer is a stream, read off the answer rather than
// off the request. A request that asked to stream may be answered whole — a gateway that buffers,
// an endpoint that ignores the member, an error envelope from the desk's own relay — so the content
// type decides, on every engine.
func IsEventStream(response *http.Response) bool {
	if response == nil {
		return false
	}
	return strings.Contains(strings.ToLower(response.Header.Get("content-type")), "text/event-stream")
}
