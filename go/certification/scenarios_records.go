package certification

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
)

const recordsCheck = "18 records"

type recordsGuard struct{}

func (recordsGuard) JSON(raw json.RawMessage, where string, record bool) (json.RawMessage, error) {
	if where != "message" || !record {
		return nil, errors.New("record output did not pass the output guard as a record")
	}
	return json.RawMessage(strings.ReplaceAll(string(raw), "/home/person/secret.txt", "[redacted: local-home-path]")), nil
}

var Records = ScenarioCheck{Scenario: "records", Names: []string{recordsCheck}, Run: func(ctx context.Context, server *Server, answerAs string, adapter Adapter) ([]Finding, error) {
	f := findings{check: recordsCheck}
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	policy := runcontext.RecordDefaults
	var file struct {
		Request string `json:"request"`
	}
	if err := ScenarioFile("records", &file); err != nil {
		return nil, err
	}
	host, err := openScenario(ctx, server, ScenarioEndpoint{Scenario: "records", AnswerAs: answerAs}, nil, true)
	if err != nil {
		return nil, err
	}
	ran, err := host.runOn(ctx, adapter, &Run{Prompt: file.Request, AllowConversation: true, Critic: "off", Context: requestInput(file.Request)})
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
		if !runcontext.ValidRecord(record) || record.Get("schema").Str() == "" || record.Get("version").Num() != 1 || record.Get("digest").Str() == "" || record.Get("retainUntil").Str() == "" {
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
	critique, err := runcontext.WriteRecord(runcontext.CritiqueSchema, runcontext.Object().Set("type", runcontext.String("critique")).Set("text", runcontext.String("Read /home/person/secret.txt")), policy, recordsGuard{}, now, "")
	if err != nil {
		return nil, err
	}
	if !strings.Contains(critique.Get("text").Str(), "[redacted: local-home-path]") {
		f.fail("the critique kept output the guard matched")
	}
	appendRecord(runcontext.CritiqueSchema, critique.JSON())
	if len(checkpoint.Completions) == 0 {
		f.fail("the adapter wrote no provenance row")
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
	refused, err := host.runOn(ctx, adapter, &Run{Prompt: file.Request, AllowConversation: true, Critic: "off", Context: input})
	if err != nil {
		return nil, err
	}
	if len(refused.Requests) != len(ran.Requests) || !hasExpiredRefusal(refused.Events) {
		f.fail("the expired resume was not refused before dispatch with records expired")
	}
	return f.list, nil
}}

func hasExpiredRefusal(events []contract.RunEvent) bool {
	for _, event := range events {
		if event.Guardrail != nil && event.Guardrail.Reason != nil && *event.Guardrail.Reason == "records expired" {
			return true
		}
	}
	return false
}
