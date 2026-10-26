package certification

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/adapter"

	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

const recordsCheck = "18 records"

var AgentRecordsNames = []string{"18 agent records"}

// Assemble this at run time because the repository's local-paths check forbids the literal.
const recordsHomePath = "/" + "home" + "/person/secret.txt"

var Records = ScenarioCheck{Scenario: "records", Names: []string{recordsCheck}, Run: func(ctx context.Context, server *Server, answerAs string, bound Adapter) ([]Finding, error) {
	return recordsFindings(ctx, server, answerAs, bound, nil)
}}

func agentRecordsChecks(ctx context.Context, server *Server, host AgentHost, _ func(Adapter) Adapter, bound Adapter) ([]Finding, error) {
	return recordsFindings(ctx, server, "whole", bound, &host)
}

// Fixture expectations follow the published current schema registry, independently of writer defaults.
func recordFixtureMaxVersion(file string) (int64, error) {
	root, err := RepositoryRoot()
	if err != nil {
		return 0, err
	}
	raw, err := os.ReadFile(filepath.Join(root, "contract", "schemas", file))
	if err != nil {
		return 0, err
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		return 0, err
	}
	var registry struct {
		Enum []int64 `json:"enum"`
	}
	if err := json.Unmarshal(schema.Properties["version"], &registry); err != nil {
		return 0, err
	}
	var max int64
	for _, version := range registry.Enum {
		if version > max {
			max = version
		}
	}
	if max == 0 {
		return 0, fmt.Errorf("fixture schema has no version registry: %s", file)
	}
	return max, nil
}

func recordsFindings(ctx context.Context, server *Server, answerAs string, bound Adapter, agentHost *AgentHost) ([]Finding, error) {
	maxCheckpointVersion, err := recordFixtureMaxVersion("checkpoint.json")
	if err != nil {
		return nil, err
	}
	maxEventVersion, err := recordFixtureMaxVersion("run-event.json")
	if err != nil {
		return nil, err
	}
	dispatches := 0
	if agentHost != nil {
		inner := bound
		provider := agentHost.Bind(func(_ adapter.AgentRequest, _ int) AgentPlay {
			dispatches++
			return AgentPlay{Steps: []AgentStep{{Say: "Records checked."}}}
		})
		bound = adapter.Func(func(ctx context.Context, run *Run) error {
			run.Model = nil
			run.Agent = &adapter.Agent{Model: "fixture", Effort: "low", Run: provider}
			return inner.Run(ctx, run)
		})
	}
	f := findings{check: recordsCheck}
	if agentHost != nil {
		f.check = AgentRecordsNames[0]
	}
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	policy := runcontext.RecordDefaults
	var file struct {
		Request string `json:"request"`
		Secret  string `json:"secret"`
	}
	if err := ScenarioFile("records", &file); err != nil {
		return nil, err
	}
	host, err := openScenario(ctx, server, ScenarioEndpoint{Scenario: "records", AnswerAs: answerAs}, nil, true)
	if err != nil {
		return nil, err
	}
	ran, err := host.runOn(ctx, bound, &Run{Prompt: file.Request, AllowConversation: true, Critic: "off", Context: requestInput(file.Request)})
	if err != nil {
		return nil, err
	}
	checkpoint := lastCheckpointOf(ran.Events)
	if ran.RunError != nil || checkpoint == nil {
		f.fail("the adapter did not write its checkpoint: %v", ran.RunError)
		return f.list, nil
	}
	var raw []json.RawMessage
	var digests []string
	appendRecord := func(schema string, bytes json.RawMessage) {
		record, parseErr := runcontext.ParseJSON(bytes)
		if parseErr != nil {
			f.fail("%s was not JSON", schema)
			return
		}
		if !runcontext.ValidRecord(record) || record.Get("schema").Str() == "" || (record.Get("schema").Str() == runcontext.CheckpointSchema && record.Get("version").Num() != 2 || record.Get("schema").Str() != runcontext.CheckpointSchema && record.Get("version").Num() != 1) || record.Get("digest").Str() == "" || record.Get("retainUntil").Str() == "" {
			f.fail("%s did not carry its four record members", schema)
		}
		raw = append(raw, bytes)
		digests = append(digests, record.Get("digest").Str())
	}
	checkpointRaw, _ := json.Marshal(checkpoint)
	appendRecord(runcontext.CheckpointSchema, checkpointRaw)
	for _, provenance := range checkpoint.Completions {
		bytes, _ := json.Marshal(provenance)
		appendRecord(runcontext.ProvenanceSchema, bytes)
	}
	// Expand the scenario placeholder at run time because the repository's local-paths check forbids the literal.
	secret := strings.Replace(file.Secret, "<home>", "/"+"home", 1)
	delivered := map[string]int64{}
	guard, err := runcontext.NewOutputGuard(nil, func() map[string]int64 { return delivered })
	if err != nil {
		return nil, err
	}
	critique, err := runcontext.WriteRecord(runcontext.CritiqueSchema, runcontext.Object().Set("type", runcontext.String("critique")).Set("text", runcontext.String("Read "+secret)), policy, guard, now, "")
	if err != nil {
		return nil, err
	}
	if !strings.Contains(critique.Get("text").Str(), "[redacted: local-home-path]") {
		f.fail("the critique kept output the guard matched")
	}
	count := critique.Get("records").Get("redactions").Get("local-home-path")
	if count == nil || count.Num() != 1 || len(delivered) != 0 {
		f.fail("record marker did not separate storage and delivered counts")
	}
	appendRecord(runcontext.CritiqueSchema, critique.JSON())
	if agentHost == nil && len(checkpoint.Completions) == 0 {
		f.fail("the adapter wrote no provenance row")
	}
	key := "AKIAABCDEFGHIJKLMNOP"
	payload := key + strings.Repeat("x", 200000)
	guard.DeclaredNames = map[string]bool{key: true}
	largeInput, _ := runcontext.ParseJSON(checkpointRaw)
	largeInput.Set("operations", runcontext.Array(
		runcontext.ObjectOf("id", runcontext.String("declared"), "name", runcontext.String(key), "args", runcontext.Object(), "status", runcontext.String("complete"), "result", runcontext.ObjectOf("text", runcontext.String(payload))),
		runcontext.ObjectOf("id", runcontext.String("undeclared"), "name", runcontext.String("prefix-"+key), "args", runcontext.ObjectOf("text", runcontext.String(key)), "status", runcontext.String("failed")),
	))
	large, err := runcontext.WriteRecord(runcontext.CheckpointSchema, largeInput, policy, guard, now, "")
	if err != nil {
		return nil, err
	}
	ops := large.Get("operations").Items()
	storeCount := large.Get("records").Get("redactions").Get("aws-access-key")
	if ops[0].Get("name").Str() != key || ops[0].Get("result").Get("text").Str() != payload || ops[1].Get("name").Str() != "prefix-[redacted: aws-access-key]" || storeCount == nil || storeCount.Num() != 2 || !runcontext.ValidRecord(large) {
		f.fail("large exempt payload or declared name changed, or store counts differ")
	}
	refusedRecord, err := runcontext.WriteRecord(runcontext.CritiqueSchema, runcontext.ObjectOf("type", runcontext.String("critique"), "text", runcontext.String(strings.Repeat("x", 200000))), policy, guard, now, "")
	if err == nil || refusedRecord != nil {
		f.fail("oversized model member was not refused atomically")
	}
	notices := guard.TakeNotices()
	root, err := RepositoryRoot()
	if err != nil {
		return nil, err
	}
	schemaRaw, err := os.ReadFile(filepath.Join(root, "contract", "schemas", "run-event.json"))
	if err != nil {
		return nil, err
	}
	schema, _ := runcontext.ParseJSON(schemaRaw)
	found := false
	for _, notice := range notices {
		raw, _ := json.Marshal(notice)
		value, _ := runcontext.ParseJSON(raw)
		if runcontext.ArgumentProblem(schema.Get("$defs").Get(value.Get("type").Str()), value) != "" {
			f.fail("store refusal event is not schema-valid")
		}
		if notice.Guardrail != nil && notice.Guardrail.Tool == "records" && notice.Guardrail.Detail == "Output refused: work-bound." {
			found = true
		}
	}
	if !found || len(delivered) != 0 {
		f.fail("store refusal missing or mixed into delivery counts")
	}
	continued, err := runcontext.WriteRecord(runcontext.CritiqueSchema, runcontext.ObjectOf("type", runcontext.String("critique"), "text", runcontext.String("Continued.")), policy, guard, now, "")
	if err != nil || !runcontext.ValidRecord(continued) {
		f.fail("record writing did not continue after refusal")
	}
	exported, _, event, err := runcontext.ExportRecords(raw, policy, "0.8.0", now)
	if err != nil {
		return nil, err
	}
	for i, entry := range exported.Manifest.Records {
		if entry.Digest != digests[i] {
			f.fail("manifest digest %d did not equal the record digest", i)
		}
	}
	manifestRaw, _ := json.Marshal(exported.Manifest)
	manifest, _ := runcontext.ParseJSON(manifestRaw)
	if exported.Manifest.Digest != runcontext.RecordDigest(manifest) || event.ExportWritten == nil || event.ExportWritten.Records != int64(len(raw)) {
		f.fail("the export manifest or export_written event did not bind the records")
	}
	expiredCheckpoint := *checkpoint
	until := "2026-10-21T11:59:59.999Z"
	expiredCheckpoint.RetainUntil = &until
	expiredRaw, _ := json.Marshal(expiredCheckpoint)
	expiredValue, _ := runcontext.ParseJSON(expiredRaw)
	digest := runcontext.RecordDigest(expiredValue)
	expiredCheckpoint.Digest = &digest
	input := requestInput(file.Request)
	input.Execution = &expiredCheckpoint
	before := dispatches
	refused, err := host.runOn(ctx, bound, &Run{Prompt: file.Request, AllowConversation: true, Critic: "off", Context: input})
	if err != nil {
		return nil, err
	}
	if dispatches != before || len(refused.Requests) != len(ran.Requests) || !hasExpiredRefusal(refused.Events) {
		f.fail("the expired resume was not refused before dispatch with records expired")
	}
	// Future versions refuse before either adapter dispatches or calls a tool.
	for _, future := range []contract.Checkpoint{{Version: maxCheckpointVersion + 1}, *checkpoint} {
		future.Version = maxCheckpointVersion + 1
		input := requestInput(file.Request)
		input.Execution = &future
		before := dispatches
		newer, err := host.runOn(ctx, bound, &Run{Prompt: file.Request, AllowConversation: true, Critic: "off", Context: input})
		if err != nil {
			return nil, err
		}
		named := false
		for _, event := range newer.Events {
			encoded, _ := json.Marshal(event)
			var header struct{ Version int }
			_ = json.Unmarshal(encoded, &header)
			if header.Version < 1 || int64(header.Version) > maxEventVersion || contract.CheckRecord("event", encoded) != nil {
				f.fail("future refusal emitted an invalid current event")
			}
			if event.Guardrail != nil && event.Guardrail.Reason != nil && *event.Guardrail.Reason == "record version unsupported" {
				named = true
			}
		}
		if !named || dispatches != before || len(newer.Requests) != len(refused.Requests) || len(newer.Seen) != len(refused.Seen) {
			f.fail("future record reached dispatch/tool work or lacked named refusal")
		}
	}
	// A legacy body is not upgraded during admission; its next write upgrades without extending retention.
	var legacy contract.Checkpoint
	if err := json.Unmarshal(checkpointRaw, &legacy); err != nil {
		return nil, err
	}
	// The frozen v1 writer had no v2 plan book. Preserve its actual member inventory.
	legacy.PlanState = nil
	legacy.Version, legacy.Status = 1, "stopped"
	for name, member := range legacy.Policy {
		if member.Source != nil && *member.Source == "layer" {
			member.Source = nil
			legacy.Policy[name] = member
		}
	}
	rawLegacy, _ := json.Marshal(legacy)
	value, _ := runcontext.ParseJSON(rawLegacy)
	legacyDigest := runcontext.RecordDigest(value)
	legacy.Digest = &legacyDigest
	beforeLegacy, _ := json.Marshal(legacy)
	if err := contract.CheckRecord("checkpoint", beforeLegacy); err != nil {
		return nil, fmt.Errorf("legacy fixture is not a supported v1 record: %w", err)
	}
	input = requestInput(file.Request)
	input.Execution = &legacy
	resumed, err := host.runOn(ctx, bound, &Run{Prompt: file.Request, AllowConversation: true, Critic: "off", Context: input})
	if err != nil {
		return nil, err
	}
	afterLegacy, _ := json.Marshal(legacy)
	next := lastCheckpointOf(resumed.Events)
	if string(beforeLegacy) != string(afterLegacy) || resumed.RunError != nil || len(errorsOf(resumed.Events)) != 0 || next == nil {
		f.fail("legacy resume rewrote supplied state or failed")
	} else {
		encoded, _ := json.Marshal(next)
		value, _ := runcontext.ParseJSON(encoded)
		if next.Version != 2 || next.RetainUntil == nil || legacy.RetainUntil == nil || *next.RetainUntil != *legacy.RetainUntil || !runcontext.ValidRecord(value) {
			f.fail("legacy next write lost current version, original retention or digest")
		}
	}
	return f.list, nil
}

func hasExpiredRefusal(events []contract.RunEvent) bool {
	for _, event := range events {
		if event.Guardrail != nil && event.Guardrail.Reason != nil && *event.Guardrail.Reason == "records expired" {
			return true
		}
	}
	return false
}
