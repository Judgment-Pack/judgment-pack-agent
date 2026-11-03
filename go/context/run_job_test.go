package runcontext

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Judgment-Pack/judgment-pack-agent/go/contract"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

// The Go twin of packages/harness/storage/job.test.ts (#229): the shared job vectors, the store's transitions and
// job waits. The flow through the real adapters is go/adapter/jobtests.

func readJobVector(t *testing.T, name string) map[string]json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile("../../contract/vectors/job/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var file map[string]json.RawMessage
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	return file
}
func jobVectorRows(t *testing.T, raw json.RawMessage) []map[string]json.RawMessage {
	t.Helper()
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	return rows
}
func jobCanon(t *testing.T, raw []byte) string {
	t.Helper()
	value, err := ParseJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	return CanonicalJSON(value)
}
func jobCanonOf(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return jobCanon(t, raw)
}
func vectorName(row map[string]json.RawMessage) string { return string(row["name"]) }

func TestJobRecordVectors(t *testing.T) {
	file := readJobVector(t, "job.json")
	var items []json.RawMessage
	_ = json.Unmarshal(file["items"], &items)
	for i, record := range items {
		held, err := ReadRunJob(record)
		if err != nil {
			t.Fatalf("%d: %v", i, err)
		}
		if jobCanonOf(t, held) != jobCanon(t, record) {
			t.Fatalf("%d: %s", i, jobCanonOf(t, held))
		}
	}
	for _, list := range []string{"refused", "readerRefused"} {
		for _, row := range jobVectorRows(t, file[list]) {
			if _, err := ReadRunJob(row["record"]); asJobRefusal(err) == nil {
				t.Fatalf("%s: %v", row["problem"], err)
			}
		}
	}
}

func TestJobEvidenceVectors(t *testing.T) {
	file := readJobVector(t, "job.json")
	for _, row := range jobVectorRows(t, file["identities"]) {
		var resource, operation, job string
		_ = json.Unmarshal(row["resource"], &resource)
		_ = json.Unmarshal(row["operation"], &operation)
		_ = json.Unmarshal(row["job"], &job)
		if JobIdentity(resource, operation) != job {
			t.Fatal(resource, operation)
		}
	}
	for _, row := range jobVectorRows(t, file["keys"]) {
		var key, digest string
		_ = json.Unmarshal(row["key"], &key)
		_ = json.Unmarshal(row["digest"], &digest)
		if held, err := JobKeyDigest(key); err != nil || held != digest {
			t.Fatal(key, held, err)
		}
	}
	var refused []string
	_ = json.Unmarshal(file["keysRefused"], &refused)
	for _, key := range refused {
		if ValidJobKey(key) {
			t.Fatal(key)
		}
	}
	digestOf := map[string]func(json.RawMessage) (string, error){
		"answer.json": func(raw json.RawMessage) (string, error) {
			held, err := ReadJobAnswer(raw)
			return JobEvidenceDigest(held), err
		},
		"reconciliation.json": func(raw json.RawMessage) (string, error) {
			held, err := ReadJobReconciliation(raw)
			return JobEvidenceDigest(held), err
		},
		"completion.json": func(raw json.RawMessage) (string, error) {
			held, err := ReadJobCompletion(raw)
			return JobEvidenceDigest(held), err
		},
		"cancel.json": func(raw json.RawMessage) (string, error) {
			held, err := ReadJobCancelAcknowledgement(raw)
			return JobEvidenceDigest(held), err
		},
	}
	for name, read := range digestOf {
		vector := readJobVector(t, name)
		var items []json.RawMessage
		var digests []string
		_ = json.Unmarshal(vector["items"], &items)
		_ = json.Unmarshal(vector["digests"], &digests)
		for i, record := range items {
			held, err := read(record)
			if err != nil || held != digests[i] {
				t.Fatalf("%s: %s %v", name, held, err)
			}
		}
		for _, list := range []string{"refused", "readerRefused"} {
			for _, row := range jobVectorRows(t, vector[list]) {
				if _, err := read(row["record"]); asJobRefusal(err) == nil {
					t.Fatalf("%s %s: %v", name, row["problem"], err)
				}
			}
		}
	}
}

func TestJobRefusalVectors(t *testing.T) {
	file := readJobVector(t, "refusal.json")
	reasons := func(raw json.RawMessage) []string {
		out := []string{}
		for _, row := range jobVectorRows(t, raw) {
			var reason string
			_ = json.Unmarshal(row["reason"], &reason)
			out = append(out, reason)
		}
		return out
	}
	if strings.Join(reasons(file["items"]), ",") != strings.Join(JobRefusalReasons, ",") || strings.Join(reasons(file["capability"]), ",") != strings.Join(JobCapabilityReasons, ",") {
		t.Fatal(JobRefusalReasons, JobCapabilityReasons)
	}
	for _, reason := range JobRefusalReasons {
		raw, _ := json.Marshal((&JobRefused{Reason: reason}).Record())
		if _, err := ReadJobRefusal(raw); err != nil {
			t.Fatal(reason, err)
		}
	}
	for _, reason := range JobCapabilityReasons {
		raw, _ := json.Marshal((&JobCapabilityRefused{Reason: reason}).Record())
		if _, err := ReadJobCapabilityRefusal(raw); err != nil {
			t.Fatal(reason, err)
		}
	}
	var refused []json.RawMessage
	_ = json.Unmarshal(file["refused"], &refused)
	for _, raw := range refused {
		if _, err := ReadJobRefusal(raw); asJobRefusal(err) == nil {
			t.Fatal(string(raw), err)
		}
	}
}

func TestJobTransitionVectors(t *testing.T) {
	file := readJobVector(t, "transitions.json")
	for _, scenario := range jobVectorRows(t, file["scenarios"]) {
		name := vectorName(scenario)
		at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		table := NewJobTable(func() time.Time { return at })
		for i, step := range jobVectorRows(t, scenario["steps"]) {
			var op, when, refused, job string
			_ = json.Unmarshal(step["op"], &op)
			_ = json.Unmarshal(step["at"], &when)
			_ = json.Unmarshal(step["refused"], &refused)
			_ = json.Unmarshal(step["job"], &job)
			parsed, err := time.Parse("2006-01-02T15:04:05.000Z", when)
			if err != nil {
				t.Fatal(err)
			}
			at = parsed
			var fence LeaseFence
			fence.Version = 1
			if step["fence"] != nil {
				_ = json.Unmarshal(step["fence"], &fence)
			}
			switch op {
			case "register":
				var reg JobRegistration
				_ = json.Unmarshal(step["registration"], &reg)
				var held JobRegistration
				if held, err = table.Admit(fence.Resource, reg, false); err == nil {
					table.Register(fence.Resource, held, fence)
				}
			case "answer":
				var answer JobAnswer
				_ = json.Unmarshal(step["answer"], &answer)
				_, err = table.Answer(answer, fence)
			case "reconcile":
				var evidence JobReconciliation
				_ = json.Unmarshal(step["evidence"], &evidence)
				_, err = table.Reconcile(evidence)
			case "reclaim":
				_, err = table.Reclaim(fence.Resource, job, fence)
			case "cancel":
				var ask JobCancelAsk
				_ = json.Unmarshal(step["ask"], &ask)
				if step["fence"] == nil {
					_, err = table.RequestCancel(ask, nil)
				} else {
					_, err = table.RequestCancel(ask, &fence)
				}
			case "acknowledge":
				var evidence JobCancelAcknowledgement
				_ = json.Unmarshal(step["evidence"], &evidence)
				_, err = table.AcknowledgeCancel(evidence)
			case "complete":
				var evidence JobCompletionEvidence
				_ = json.Unmarshal(step["evidence"], &evidence)
				var wake *JobWake
				_, wake, err = table.Complete(evidence)
				if step["wakes"] != nil && (wake != nil) != (string(step["wakes"]) == "true") {
					t.Fatalf("%s, step %d: wake %v", name, i, wake)
				}
			default:
				t.Fatal(op)
			}
			if refused != "" {
				if !refusedAs(err, refused) {
					t.Fatalf("%s, step %d: %v, want %s", name, i, err, refused)
				}
			} else if err != nil {
				t.Fatalf("%s, step %d: %v", name, i, err)
			}
		}
		out, err := table.Export("run-j")
		if err != nil {
			t.Fatal(err)
		}
		if string(scenario["final"]) == "null" {
			if len(out.Jobs) != 0 {
				t.Fatalf("%s: %v", name, out.Jobs)
			}
			continue
		}
		if len(out.Jobs) == 0 || jobCanonOf(t, out.Jobs[0]) != jobCanon(t, scenario["final"]) {
			t.Fatalf("%s: %v", name, out.Jobs)
		}
		raw, _ := json.Marshal(out.Jobs[0])
		if _, err := ReadRunJob(raw); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestJobModelVectors(t *testing.T) {
	file := readJobVector(t, "model.json")
	jobOf := func(raw json.RawMessage) RunJob {
		held, err := ReadRunJob(raw)
		if err != nil {
			t.Fatal(err)
		}
		return held
	}
	for _, row := range jobVectorRows(t, file["projections"]) {
		if jobCanonOf(t, JobProjectionOf(jobOf(row["record"]))) != jobCanon(t, row["projection"]) {
			t.Fatal(string(row["projection"]))
		}
	}
	for _, row := range jobVectorRows(t, file["startResults"]) {
		var text string
		_ = json.Unmarshal(row["text"], &text)
		result := JobStartResult(jobOf(row["record"]))
		got := MustParseJSON(string(result.JSON())).Get("content").Items()[0].Get("text").Str()
		if got != text || result.Err() != (string(row["isError"]) == "true") {
			t.Fatalf("%s, want %s", got, text)
		}
	}
	var line string
	_ = json.Unmarshal(file["continuationLine"], &line)
	if JobContinuation != line {
		t.Fatal(JobContinuation)
	}
	projections := jobVectorRows(t, file["projections"])
	marked, err := JobContinuationText(jobOf(projections[3]["record"]), "marker")
	if err != nil || !strings.HasPrefix(marked, line+"\n") || !strings.Contains(marked, jobCanon(t, projections[3]["projection"])) {
		t.Fatal(marked, err)
	}
	for _, name := range []string{"argumentsRefused", "argumentsRefusedRaw"} {
		for _, row := range jobVectorRows(t, file[name]) {
			var raw string
			_ = json.Unmarshal(row["raw"], &raw)
			if _, err := readJobArgument([]byte(raw)); !refusedAs(err, "no-job") {
				t.Fatalf("%s: %v", raw, err)
			}
		}
	}
	for _, row := range jobVectorRows(t, file["arguments"]) {
		var raw, job string
		_ = json.Unmarshal(row["raw"], &raw)
		_ = json.Unmarshal(row["job"], &job)
		if held, err := readJobArgument([]byte(raw)); err != nil || held != job {
			t.Fatal(raw, err)
		}
	}
	for _, row := range jobVectorRows(t, file["startAnswers"]) {
		var held struct {
			Started bool   `json:"started"`
			Handle  string `json:"handle"`
			Receipt string `json:"receipt"`
		}
		_ = json.Unmarshal(row["answer"], &held)
		answer := JobStartAnswer{Started: held.Started, Handle: held.Handle, Receipt: held.Receipt}
		if err := readJobStartAnswer(&answer); err != nil {
			t.Fatal(string(row["answer"]), err)
		}
	}
	for _, row := range jobVectorRows(t, file["cancelResults"]) {
		var text string
		_ = json.Unmarshal(row["text"], &text)
		result := jobCancelResultOf(jobOf(row["record"]), string(row["sent"]) != "false")
		got := MustParseJSON(string(result.JSON())).Get("content").Items()[0].Get("text").Str()
		if got != text || result.Err() != (string(row["isError"]) == "true") {
			t.Fatalf("%s, want %s", got, text)
		}
	}
	for _, row := range jobVectorRows(t, file["cancelCards"]) {
		if jobCanonOf(t, JobCancelCardOf(jobOf(row["record"]))) != jobCanon(t, row["card"]) {
			t.Fatal(string(row["card"]))
		}
	}
	for _, row := range jobVectorRows(t, file["cancelInputs"]) {
		var want struct {
			Job, Source, Tool, Handle, HandleDigest string
			Generation                              int
		}
		_ = json.Unmarshal(row["input"], &want)
		got := JobCancelInputOf(jobOf(row["record"]))
		if got.Job != want.Job || got.Source != want.Source || got.Tool != want.Tool || got.Handle != want.Handle || got.HandleDigest != want.HandleDigest || got.Generation != want.Generation {
			t.Fatalf("%+v, want %+v", got, want)
		}
	}
	for _, row := range jobVectorRows(t, file["cancelArguments"]) {
		var raw, job string
		_ = json.Unmarshal(row["raw"], &raw)
		_ = json.Unmarshal(row["job"], &job)
		if held, err := readJobCancelArgument([]byte(raw)); err != nil || held != job {
			t.Fatal(raw, err)
		}
	}
	for _, row := range jobVectorRows(t, file["cancelArgumentsRefused"]) {
		var raw string
		_ = json.Unmarshal(row["raw"], &raw)
		if _, err := readJobCancelArgument([]byte(raw)); asJobRefusal(err) == nil {
			t.Fatal(raw)
		}
	}
	if job := strings.Repeat("a", 64); func() bool {
		_, err := readJobCancelArgument([]byte(`{"job":"sha256:` + job + `","generation":1,"JOB":"x"}`))
		return err == nil
	}() {
		t.Fatal("a folded name was read")
	}
	var failures struct {
		Unrecorded, NotSent json.RawMessage
	}
	_ = json.Unmarshal(file["cancelFailures"], &failures)
	if jobCanonOf(t, failure.Normalize(jobCancelUnrecorded(), "tool", time.Now())) != jobCanon(t, failures.Unrecorded) || jobCanonOf(t, failure.Normalize(jobCancelNotSent(), "tool", time.Now())) != jobCanon(t, failures.NotSent) {
		t.Fatal("a cancel's failure is not the vector's")
	}
	for _, row := range jobVectorRows(t, file["startAnswersKept"]) {
		var held, read struct {
			Started        bool   `json:"started"`
			Handle         string `json:"handle"`
			HandleDigested bool   `json:"handleDigested"`
			Receipt        string `json:"receipt"`
		}
		_ = json.Unmarshal(row["answer"], &held)
		_ = json.Unmarshal(row["read"], &read)
		answer := JobStartAnswer{Started: held.Started, Handle: held.Handle, Receipt: held.Receipt}
		if err := readJobStartAnswer(&answer); err != nil || answer.Handle != read.Handle || answer.Receipt != read.Receipt || answer.handleDigested != read.HandleDigested {
			t.Fatal(vectorName(row), answer, err)
		}
	}
	for _, row := range jobVectorRows(t, file["startAnswersRefused"]) {
		var held struct {
			Started bool   `json:"started"`
			Handle  string `json:"handle"`
		}
		if json.Unmarshal(row["answer"], &held) != nil || !held.Started || held.Handle != "" {
			continue // Go's answer is a struct: only a started answer with no handle has this form.
		}
		if readJobStartAnswer(&JobStartAnswer{Started: true}) == nil {
			t.Fatal(vectorName(row))
		}
	}
	if readJobStartAnswer(nil) == nil || readJobStartAnswer(&JobStartAnswer{Started: true}) == nil || readJobStartAnswer(&JobStartAnswer{Handle: "h"}) == nil {
		t.Fatal("an answer that is not typed was read")
	}
	gateway := readJobVector(t, "gateway.json")
	for _, row := range jobVectorRows(t, gateway["cases"]) {
		job, err := ReadRunJob(row["job"])
		if err != nil {
			t.Fatal(err)
		}
		members, err := GatewayActionMembers(job, string(row["reconcile"]) == "true")
		if err != nil || jobCanonOf(t, members) != jobCanon(t, row["members"]) {
			t.Fatal(vectorName(row), jobCanonOf(t, members), err)
		}
	}
	for _, row := range jobVectorRows(t, gateway["findings"]) {
		var refusal, finding string
		_ = json.Unmarshal(row["refusal"], &refusal)
		_ = json.Unmarshal(row["finding"], &finding)
		if GatewayRefusalFinding(refusal) != finding {
			t.Fatal(refusal)
		}
	}
	reported := readJobVector(t, "report.json")
	var exports, reports []json.RawMessage
	_ = json.Unmarshal(reported["exports"], &exports)
	_ = json.Unmarshal(reported["items"], &reports)
	for i, raw := range exports {
		source, err := ReadJobExport(raw)
		if err != nil {
			t.Fatal(err)
		}
		derived, err := JobReportFromExport(source)
		if err != nil || jobCanonOf(t, derived) != jobCanon(t, reports[i]) {
			t.Fatal(jobCanonOf(t, derived), err)
		}
	}
	for _, row := range jobVectorRows(t, reported["exportsRefused"]) {
		if _, err := ReadJobExport(row["record"]); asJobRefusal(err) == nil {
			t.Fatal(vectorName(row), err)
		}
	}
}

// jobTestRun is a store with a clock, one run committed under a lease, and one job registered in that run.
type jobTestRun struct {
	store durableJobStore
	clock *time.Time
	fence LeaseFence
	job   string
}

const jobTestRunID = "run-j"
const jobTestKey = "job.key.0001.Q7z2Xm9LpV4cR8tNb3Yh6Df1Gj5K"
const jobTestWake = "wk.job.0001.Hs8Pq3Lz6Wd1Rm9Tc4Vb7Nx2Ka5U"

func newJobTestRun(t *testing.T) *jobTestRun {
	t.Helper()
	ctx := context.Background()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	out := &jobTestRun{clock: &at}
	out.store = durableJobStore{NewMemoryRunStore(MemoryRunStoreOptions{Now: func() time.Time { return *out.clock }})}
	head, err := out.store.Commit(ctx, RunCommit{RunID: jobTestRunID, CommitID: "open", Checkpoint: []byte(`{"kit":"job"}`), Events: [][]byte{}})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := out.store.AcquireLease(ctx, LeaseAcquire{Resource: jobTestRunID, Owner: "worker-a", ExpectedRevision: head.Revision, DurationMs: 3600000})
	if err != nil {
		t.Fatal(err)
	}
	out.fence = FenceOf(lease)
	digest, _ := JobKeyDigest(jobTestKey)
	out.job = JobIdentity(jobTestRunID, "op-1")
	if _, err = out.store.CommitFenced(ctx, RunCommit{RunID: jobTestRunID, ExpectedRevision: head.Revision, CommitID: "claim", Checkpoint: []byte(`{"kit":"claim"}`), Events: [][]byte{}, Job: &JobRegistration{Job: out.job, Operation: "op-1", Source: "s", Tool: "t", RequestDigest: "sha256:" + strings.Repeat("a", 64), KeyDigest: digest}}, out.fence); err != nil {
		t.Fatal(err)
	}
	return out
}
func (r *jobTestRun) answer(t *testing.T) {
	t.Helper()
	handle := "ext-1"
	if _, err := r.store.AnswerJob(context.Background(), JobAnswer{Version: 1, Resource: jobTestRunID, Job: r.job, Generation: 1, Answer: "started", Handle: &handle}, r.fence); err != nil {
		t.Fatal(err)
	}
}
func (r *jobTestRun) completion(over func(*JobCompletionEvidence)) JobCompletionEvidence {
	out := JobCompletionEvidence{Version: 1, Resource: jobTestRunID, Job: r.job, Generation: 1, Evidence: "done-1", Outcome: "succeeded", Key: jobTestKey}
	if over != nil {
		over(&out)
	}
	return out
}
func (r *jobTestRun) park(t *testing.T, over func(*RunWait)) (RunWait, AttentionEvent, error) {
	t.Helper()
	head, err := r.store.Load(context.Background(), jobTestRunID)
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := WakeKeyDigest(jobTestWake)
	park := RunWait{Version: 1, Resource: jobTestRunID, Wait: "wait-j", Reason: "job", Pending: contract.RunWaitPending{Job: &contract.RunWaitPendingJob{Kind: "job", Job: r.job, Generation: 1}}, Condition: WaitCondition{Event: &contract.RunWaitEventCondition{Kind: JobWaitEvent, Subject: r.job}}, Continuation: "k", Checkpoint: contract.RunWaitCheckpointBinding{Revision: head.Revision, Digest: CheckpointBytesDigest(head.Checkpoint)}, Pins: jobKitPins, WakeKeyDigest: digest, Release: []WaitRelease{}}
	if over != nil {
		over(&park)
	}
	return r.store.ParkWait(context.Background(), park, r.fence)
}

func TestJobARegistrationCommitsOnlyUnderAFence(t *testing.T) {
	ctx := context.Background()
	store := durableJobStore{NewMemoryRunStore(MemoryRunStoreOptions{})}
	digest, _ := JobKeyDigest(jobTestKey)
	_, err := store.Commit(ctx, RunCommit{RunID: jobTestRunID, CommitID: "c1", Checkpoint: []byte(`{}`), Events: [][]byte{}, Job: &JobRegistration{Job: JobIdentity(jobTestRunID, "op-1"), Operation: "op-1", Source: "s", Tool: "t", RequestDigest: "sha256:" + strings.Repeat("a", 64), KeyDigest: digest}})
	if !refusedAs(err, "invalid") {
		t.Fatal(err)
	}
	if head, _ := store.Load(ctx, jobTestRunID); head != nil {
		t.Fatal("the commit landed")
	}
}

func TestJobARepeatedCommitMustCarryTheSameRegistration(t *testing.T) {
	ctx := context.Background()
	store := durableJobStore{NewMemoryRunStore(MemoryRunStoreOptions{})}
	lease, err := store.AcquireLease(ctx, LeaseAcquire{Resource: jobTestRunID, Owner: "worker-a", DurationMs: 3600000})
	if err != nil {
		t.Fatal(err)
	}
	fence := FenceOf(lease)
	commit := func(key, operation string) (RunReceipt, error) {
		digest, _ := JobKeyDigest(key)
		return store.CommitFenced(ctx, RunCommit{RunID: jobTestRunID, CommitID: "claim-1", Checkpoint: []byte(`{}`), Events: [][]byte{}, Job: &JobRegistration{Job: JobIdentity(jobTestRunID, operation), Operation: operation, Source: "s", Tool: "t", RequestDigest: "sha256:" + strings.Repeat("a", 64), KeyDigest: digest}}, fence)
	}
	first, err := commit(jobTestKey, "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if again, err := commit(jobTestKey, "op-1"); err != nil || again != first {
		t.Fatal(again, err)
	}
	var mismatch *StorageCommitMismatch
	if _, err := commit(jobTestWake, "op-2"); !errors.As(err, &mismatch) {
		t.Fatal(err)
	}
	if out, _ := store.ExportJobs(ctx, jobTestRunID); len(out.Jobs) != 1 {
		t.Fatal(out.Jobs)
	}
}

func TestJobAWaitIsHeldToItsJob(t *testing.T) {
	r := newJobTestRun(t)
	r.answer(t)
	wakeAt := "2026-01-01T01:00:00.000Z"
	for name, over := range map[string]func(*RunWait){
		"another event":   func(park *RunWait) { park.Condition.Event.Kind = "anything" },
		"another subject": func(park *RunWait) { park.Condition.Event.Subject = "another" },
		"a chat":          func(park *RunWait) { park.Chat = &contract.RunWaitChatBinding{ID: "chat-1", Revision: "r"} },
		"a generation":    func(park *RunWait) { park.Pending.Job.Generation = 2 },
		"a wake time":     func(park *RunWait) { park.Condition.WakeAt = &wakeAt },
	} {
		if _, _, err := r.park(t, over); jobKitReason(err) != "invalid" {
			t.Fatal(name, err)
		}
	}
	if wait, _, err := r.park(t, nil); err != nil || wait.State != "parked" {
		t.Fatal(wait, err)
	}
}

func TestJobCompletionNeedsTheKeyAndNoLease(t *testing.T) {
	ctx := context.Background()
	r := newJobTestRun(t)
	r.answer(t)
	if _, err := r.store.ReleaseLease(ctx, r.fence); err != nil {
		t.Fatal(err)
	}
	if _, err := CompleteJob(ctx, r.store, r.completion(func(e *JobCompletionEvidence) { e.Key = jobKitWrong })); !refusedAs(err, "unauthenticated") {
		t.Fatal(err)
	}
	if _, err := CompleteJob(ctx, r.store, r.completion(func(e *JobCompletionEvidence) { e.Resource = "run-other" })); !refusedAs(err, "no-job") {
		t.Fatal(err)
	}
	done, err := CompleteJob(ctx, r.store, r.completion(nil))
	if err != nil || done.State != "completed" {
		t.Fatal(done, err)
	}
	again, err := CompleteJob(ctx, r.store, r.completion(nil))
	if err != nil || jobCanonOf(t, again) != jobCanonOf(t, done) {
		t.Fatal(again, err)
	}
	if _, err := CompleteJob(ctx, r.store, r.completion(func(e *JobCompletionEvidence) { e.Outcome = "failed" })); !refusedAs(err, "conflict") {
		t.Fatal(err)
	}
	if _, err := CompleteJob(ctx, r.store, r.completion(func(e *JobCompletionEvidence) { e.Evidence, e.Outcome = "done-2", "failed" })); !refusedAs(err, "stale") {
		t.Fatal(err)
	}
}

func TestJobAReconciliationKeepsAContradictionAndReportsIt(t *testing.T) {
	ctx := context.Background()
	r := newJobTestRun(t)
	if _, err := r.store.ReleaseLease(ctx, r.fence); err != nil {
		t.Fatal(err)
	}
	reconcile := func(over func(*JobReconciliation)) JobReconciliation {
		out := JobReconciliation{Version: 1, Resource: jobTestRunID, Job: r.job, Generation: 1, Evidence: "find-1", Finding: "not-started", Key: jobTestKey}
		if over != nil {
			over(&out)
		}
		return out
	}
	if held, err := ReconcileJob(ctx, r.store, reconcile(func(e *JobReconciliation) { e.Finding = "unknown" })); err != nil || held.State != "unproven" {
		t.Fatal(held, err)
	}
	closed, err := ReconcileJob(ctx, r.store, reconcile(nil))
	if err != nil || closed.State != "not-started" {
		t.Fatal(closed, err)
	}
	held, err := CompleteJob(ctx, r.store, r.completion(nil))
	if err != nil || held.State != "not-started" || len(held.Contradictions) != 1 || held.Contradictions[0].Kind != "completed" {
		t.Fatal(held, err)
	}
	handle := "h2"
	held, err = ReconcileJob(ctx, r.store, reconcile(func(e *JobReconciliation) { e.Evidence, e.Finding, e.Handle = "find-2", "started", &handle }))
	if err != nil || len(held.Contradictions) != 2 || *held.Contradictions[1].Handle != "h2" {
		t.Fatal(held, err)
	}
	report, err := JobReportOf(ctx, r.store, jobTestRunID)
	if err != nil || len(report.Entries) != 4 || report.Entries[2].Decision != "contradiction" {
		t.Fatal(report, err)
	}
	if raw, _ := json.Marshal(report); strings.Contains(string(raw), jobTestKey) {
		t.Fatal("the report holds the key")
	}
	for n := 3; n <= 8; n++ {
		if held, err = CompleteJob(ctx, r.store, r.completion(func(e *JobCompletionEvidence) { e.Evidence = "done-" + string(rune('0'+n)) })); err != nil {
			t.Fatal(err)
		}
	}
	if len(held.Contradictions) != 8 {
		t.Fatal(held.Contradictions)
	}
	if _, err = CompleteJob(ctx, r.store, r.completion(func(e *JobCompletionEvidence) { e.Evidence = "done-9" })); !refusedAs(err, "stale") {
		t.Fatal(err)
	}
}

func TestJobAReclaimIsUnderTheCurrentFence(t *testing.T) {
	ctx := context.Background()
	r := newJobTestRun(t)
	other := r.fence
	other.Owner = "worker-x"
	if _, err := r.store.ReclaimJob(ctx, jobTestRunID, r.job, other); jobKitReason(err) != "other-owner" {
		t.Fatal(err)
	}
	if _, err := r.store.ReclaimJob(ctx, "run-other", r.job, r.fence); jobKitReason(err) != "resource-mismatch" {
		t.Fatal(err)
	}
	if held, err := r.store.ReclaimJob(ctx, jobTestRunID, r.job, r.fence); err != nil || held.Reclaimed == nil || held.Reclaimed.Owner != "worker-a" {
		t.Fatal(held, err)
	}
}

func TestJobTheStoreRefusesASettledJobInTheParksOwnTransaction(t *testing.T) {
	ctx := context.Background()
	r := newJobTestRun(t)
	if _, _, err := r.park(t, nil); !refusedAs(err, "job-unproven") {
		t.Fatal(err)
	}
	r.answer(t)
	if _, err := r.store.CompleteJob(ctx, r.completion(nil)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.park(t, nil); !refusedAs(err, "job-settled") {
		t.Fatal(err)
	}
	if held, _ := r.store.ReadLease(ctx, jobTestRunID); held == nil || held.State != "held" {
		t.Fatal(held)
	}
}

func TestJobAKeyBelongsToOneJobOrOneWait(t *testing.T) {
	r := newJobTestRun(t)
	r.answer(t)
	if _, _, err := r.park(t, func(park *RunWait) { park.WakeKeyDigest, _ = JobKeyDigest(jobTestKey) }); jobKitReason(err) != "key-reused" {
		t.Fatal(err)
	}
}

func TestJobOnlyACompletionWakesAJobWaitAndATimeoutDoesNotCloseTheRun(t *testing.T) {
	ctx := context.Background()
	r := newJobTestRun(t)
	r.answer(t)
	timeoutAt := "2026-01-01T00:10:00.000Z"
	wait, attention, err := r.park(t, func(park *RunWait) { park.TimeoutAt = &timeoutAt })
	if err != nil || wait.State != "parked" || attention.Needs != "job-completion" || attention.Subject == nil || *attention.Subject != r.job {
		t.Fatal(wait, attention, err)
	}
	forged := WakeEvidence{Version: 1, Resource: jobTestRunID, Wait: "wait-j", Evidence: "forged", Kind: "event", Event: &contract.RunWaitEventCondition{Kind: JobWaitEvent, Subject: r.job}, Key: jobTestWake}
	if _, err = r.store.WakeWait(ctx, forged); jobKitReason(err) != "mismatch" {
		t.Fatal(err)
	}
	at := r.clock.Add(10 * time.Minute)
	r.clock = &at
	if _, err = r.store.CompleteJob(ctx, r.completion(nil)); err != nil {
		t.Fatal(err)
	}
	held, _ := r.store.ReadWait(ctx, jobTestRunID, "wait-j")
	if held.State != "parked" {
		t.Fatal(held.State)
	}
	if _, err = r.store.WakeWait(ctx, WakeEvidence{Version: 1, Resource: jobTestRunID, Wait: "wait-j", Evidence: "timeout-1", Kind: "timeout", Key: jobTestWake}); err != nil {
		t.Fatal(err)
	}
	if held, _ = r.store.ReadWait(ctx, jobTestRunID, "wait-j"); held.State != "timed-out" {
		t.Fatal(held.State)
	}
	if err = r.store.waits.AdmitAcquire(jobTestRunID); jobKitReason(err) != "wait-pending" {
		t.Fatal(err)
	}
}

func TestJobACompletionWakesItsWaitInItsOwnTransaction(t *testing.T) {
	ctx := context.Background()
	r := newJobTestRun(t)
	r.answer(t)
	if _, _, err := r.park(t, nil); err != nil {
		t.Fatal(err)
	}
	done, err := r.store.CompleteJob(ctx, r.completion(nil))
	if err != nil {
		t.Fatal(err)
	}
	held, _ := r.store.ReadWait(ctx, jobTestRunID, "wait-j")
	if held.State != "woken" || held.Disposition.Evidence != "done-1" || held.Disposition.Kind != "event" || held.Disposition.Digest != done.Completion.Digest {
		t.Fatal(held)
	}
	// The wait's report names the job, read from the retained wait.
	exported, err := r.store.ExportWaits(ctx, jobTestRunID)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(exported)
	if _, err := ReadWaitExport(raw); err != nil {
		t.Fatal(err)
	}
	report, err := ReportFromExport(exported, time.Time{})
	if err != nil || report.Entries[0].Subject == nil || *report.Entries[0].Subject != r.job {
		t.Fatal(report, err)
	}
}

func TestJobACapabilityRefusalIsNamed(t *testing.T) {
	memory := NewMemoryRunStore(MemoryRunStoreOptions{})
	var capability *JobCapabilityRefused
	if err := AdmitJobs(memory, nil); !errors.As(err, &capability) || capability.Reason != "store-lifetime" {
		t.Fatal(err)
	}
	if _, err := JobReadTool(memory); !errors.As(err, &capability) || capability.Reason != "store-unsupported" {
		t.Fatal(err)
	}
	if err := AdmitJobs(durableJobStore{memory}, nil); err == nil {
		t.Fatal("no effect host was admitted")
	}
	durable := durableJobStore{NewMemoryRunStore(MemoryRunStoreOptions{})}
	if err := AdmitJobs(durable, NewReferenceEffectHost(durable)); err != nil {
		t.Fatal(err)
	}
	if _, err := JobStartTool(JobStartSpec{Name: "start", Source: &ToolSource{Kind: "gateway", ID: "g"}, Mint: func(context.Context, string) (string, error) { return "", nil }, Start: func(context.Context, JobStartInput) (JobStartAnswer, error) { return JobStartAnswer{}, nil }}); !errors.As(err, &capability) || capability.Reason != "job-host-unsupported" {
		t.Fatal(err)
	}
	if _, err := JobStartTool(JobStartSpec{Name: "start", GatewayKeyScope: true, Source: &ToolSource{Kind: "gateway", ID: "g"}, Mint: func(context.Context, string) (string, error) { return "", nil }, Start: func(context.Context, JobStartInput) (JobStartAnswer, error) { return JobStartAnswer{}, nil }}); err != nil {
		t.Fatal(err)
	}
}

// jobLiar answers a read with another run's job.
type jobLiar struct{ durableJobStore }

func (s jobLiar) ReadJob(ctx context.Context, resource, job string) (*RunJob, error) {
	return s.durableJobStore.ReadJob(ctx, jobTestRunID, job)
}
func TestJobReadBackRefusesAStoreThatAnswersAnotherRun(t *testing.T) {
	r := newJobTestRun(t)
	if _, err := ReadJob(context.Background(), jobLiar{r.store}, "run-other", r.job); !refusedAs(err, "invalid") {
		t.Fatal(err)
	}
}

func TestJobEvidenceIsHeldToTheJobsRecord(t *testing.T) {
	scope := DurableScope{Execution: jobTestRunID, Role: "author"}
	id := "op-1"
	book := &DurableBook{Dispatches: []DurableDispatch{{ID: id, Scope: scope, Kind: "host", State: "unknown"}, {ID: "op-read", Scope: scope, Kind: "host", State: "result", Result: json.RawMessage(`{"content":[]}`)}}}
	cite := func(which string, jobs ...map[string]RunJob) string {
		return planEvidenceFailure(ObjectOf("operations", Array(String(which)), "artifacts", Array()), book, scope, jobs...)
	}
	job := func(state, outcome string) map[string]RunJob {
		out := RunJob{State: state}
		if outcome != "" {
			out.Completion = &JobCompletion{Outcome: outcome}
		}
		return map[string]RunJob{id: out}
	}
	for _, c := range []struct {
		name string
		got  string
		want string
	}{
		{"a start with no job on record", cite(id), "unsuccessful-operation"},
		{"a job running", cite(id, job("started", "")), "unsuccessful-operation"},
		{"an unproven job", cite(id, job("unproven", "")), "unsuccessful-operation"},
		{"a failed job", cite(id, job("completed", "failed")), "unsuccessful-operation"},
		{"a cancelled job", cite(id, job("completed", "cancelled")), "unsuccessful-operation"},
		{"a job that succeeded, whatever its row says", cite(id, job("completed", "succeeded")), ""},
		{"a read's row, with no observation", cite("op-read", job("completed", "succeeded")), "unsuccessful-operation"},
	} {
		if c.got != c.want {
			t.Fatalf("%s: %q", c.name, c.got)
		}
	}
	other := planEvidenceFailure(ObjectOf("operations", Array(String(id)), "artifacts", Array()), book, DurableScope{Execution: "other", Role: "author"}, job("completed", "succeeded"))
	if other != "wrong-scope" {
		t.Fatal(other)
	}
}

// M-8: a sub-agent is offered no job tool: the declaration is withheld, and a child session is refused by name.
func TestJobToolsAreOfferedToNoSubAgent(t *testing.T) {
	ctx := context.Background()
	store := durableJobStore{NewMemoryRunStore(MemoryRunStoreOptions{})}
	lease, err := store.AcquireLease(ctx, LeaseAcquire{Resource: "run-child", Owner: "worker-a", DurationMs: 3600000})
	if err != nil {
		t.Fatal(err)
	}
	view, err := UnattendedRunStore(store, FenceOf(lease), NewReferenceEffectHost(store))
	if err != nil {
		t.Fatal(err)
	}
	read, err := JobReadTool(view)
	if err != nil {
		t.Fatal(err)
	}
	start, err := JobStartTool(JobStartSpec{Name: "start_job", Description: "Start.", InputSchema: json.RawMessage(`{"type":"object"}`), Source: &ToolSource{Kind: "custom", ID: "runner"}, Mint: func(context.Context, string) (string, error) { return "", nil }, Start: func(context.Context, JobStartInput) (JobStartAnswer, error) { return JobStartAnswer{}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	policy := Policy{CataloguedSubagents: 16, SubagentCatalogueBytes: 4000, MaxInputBytes: 1 << 20, TurnMilliseconds: 1 << 20, ToolResultBytes: 1 << 20}
	for _, tool := range []HostTool{read, start} {
		s := subagentFixture(t)
		s.Tools = []string{tool.Name}
		admitted, notices, err := DeclareSubagents([]Subagent{s}, []HostTool{tool}, nil, policy)
		raw, _ := json.Marshal(notices)
		if err != nil || len(admitted) != 0 || !strings.Contains(string(raw), "A sub-agent is offered no job tool: a job belongs to the run that started it.") {
			t.Fatal(tool.Name, err, len(admitted), string(raw))
		}
	}
	session := modelSession()
	child := subagentFixture(t)
	session.ChildDefinition = &child
	session.HostTools = []HostTool{read}
	session.Context = &Input{CurrentRequest: &CurrentRequest{Message: "Read.", Input: "r"}, RunStore: &DurableRunInput{Store: view, ID: "run-child"}}
	_, err = ContextualSession(ctx, session)
	var refused *JobCapabilityRefused
	if !errors.As(err, &refused) || refused.Reason != "child-unsupported" {
		t.Fatal(err)
	}
}

func TestJobCancelVectors(t *testing.T) {
	vector := readJobVector(t, "cancel.json")
	var asks []json.RawMessage
	_ = json.Unmarshal(vector["asks"], &asks)
	for _, raw := range asks {
		if _, err := ReadJobCancelAsk(raw); err != nil {
			t.Fatal(string(raw), err)
		}
	}
	for _, row := range jobVectorRows(t, vector["asksRefused"]) {
		if _, err := ReadJobCancelAsk(row["record"]); asJobRefusal(err) == nil {
			t.Fatal(string(row["record"]))
		}
	}
	for _, row := range jobVectorRows(t, vector["operations"]) {
		var command, job, operation string
		_ = json.Unmarshal(row["command"], &command)
		_ = json.Unmarshal(row["job"], &job)
		_ = json.Unmarshal(row["operation"], &operation)
		if JobCancelOperation(command, job) != operation {
			t.Fatal(JobCancelOperation(command, job), operation)
		}
	}
}
