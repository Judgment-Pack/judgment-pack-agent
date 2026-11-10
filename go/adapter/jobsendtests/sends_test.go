package jobsendtests

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

// #229: what a cancel or an abandon sends through the effect host, and when it sends nothing: the job tests whose
// names say what is sent, in a binary of their own since #364 (the rest are go/adapter/jobtests). The shared
// fixtures are helpers_test.go, a copy of jobtests'.

func TestJobsACancelIsRequestedBeforeAdmissionAndSendsTheRecord(t *testing.T) {
	for _, engine := range engines {
		t.Run(engine, func(t *testing.T) {
			var l *leasedRun
			by := ""
			p := pairOf(t, "runner", nil, func(ctx context.Context, input runcontext.JobCancelInput) error {
				held, err := runcontext.ReadJob(ctx, l.store, jobRun, input.Job)
				if err == nil && held != nil && held.Cancel != nil {
					by = held.Cancel.Requested.By
				}
				return nil
			}, "allow", jobKey)
			var job runcontext.RunJob
			l, job = startedPair(t, engine, p)
			result := drive(t, l.view, engine, p.tools(), []jobCall{{"runner__cancel_job", cancelArgs(job.Job)}}, options{resume: true})
			held := onlyJob(t, l.store)
			if p.sent.Load() != 1 || by != "tool" || held.State != "started" || held.Cancel == nil || held.Cancel.Requested.Owner == nil || *held.Cancel.Requested.Owner != "worker-a" {
				t.Fatalf("%d %q %+v", p.sent.Load(), by, held)
			}
			sent := (*p.inputs)[0]
			if sent.Job != job.Job || sent.Generation != 1 || sent.Source != job.Source || sent.Tool != job.Tool || sent.Handle == nil || *sent.Handle != jobSecret || sent.Operation == "" {
				t.Fatalf("%+v", sent)
			}
			rows := cancelRows(t, l.store)
			if len(rows) != 1 || rows[0].State != "result" || rows[0].ID != held.Cancel.Requested.Operation {
				t.Fatalf("%+v", rows)
			}
			answer := lastToolResult(t, result)
			if !strings.Contains(answer, "cancel requested") || strings.Contains(answer, jobSecret) {
				t.Fatal(answer)
			}
		})
	}
}

func TestJobsADigestedHandleIsNeverSentAsTheReference(t *testing.T) {
	long := strings.Repeat("h", 300)
	p := pairOf(t, "runner", answered(runcontext.JobStartAnswer{Started: true, Handle: long}, nil), nil, "allow", jobKey)
	l, job := startedPair(t, "eino", p)
	drive(t, l.view, "eino", p.tools(), []jobCall{{"runner__cancel_job", cancelArgs(job.Job)}}, options{resume: true})
	if p.sent.Load() != 1 || (*p.inputs)[0].Handle != nil || (*p.inputs)[0].HandleDigest == nil || *(*p.inputs)[0].HandleDigest != fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(long))) {
		t.Fatalf("%+v", *p.inputs)
	}
}

func TestJobsACancelThatFailsHoldsOneFixedFailureAndIsNotSentAgain(t *testing.T) {
	for _, engine := range engines {
		t.Run(engine, func(t *testing.T) {
			p := pairOf(t, "runner", nil, func(context.Context, runcontext.JobCancelInput) error {
				return failure.Authored("tool-failed", "SENTINEL-HOST-TEXT: half sent", "tool")
			}, "allow", jobKey)
			l, job := startedPair(t, engine, p)
			call := []jobCall{{"runner__cancel_job", cancelArgs(job.Job)}}
			result := drive(t, l.view, engine, p.tools(), call, options{resume: true})
			rows := cancelRows(t, l.store)
			if len(rows) != 1 || rows[0].State != "unknown" || rows[0].Failure["outcome"] != "unknown" || !strings.Contains(fmt.Sprint(rows[0].Failure["detail"]), "JobCancelUnrecorded") {
				t.Fatalf("%+v", rows)
			}
			answer := toolAnswer(result)
			if strings.Contains(answer, "SENTINEL-HOST-TEXT") || strings.Contains(answer, "invalid-tool-input") || !strings.Contains(lastToolResult(t, result), "cancel requested") {
				t.Fatal(answer)
			}
			drive(t, l.view, engine, p.tools(), call, options{resume: true})
			if p.sent.Load() != 1 {
				t.Fatal(p.sent.Load())
			}
		})
	}
}

// settlesFirst completes the job just before the store is asked to record the cancel.
type settlesFirst struct {
	durableJobStore
	settle func()
}

func (s *settlesFirst) RequestJobCancel(ctx context.Context, ask runcontext.JobCancelAsk, fence runcontext.LeaseFence) (runcontext.RunJob, error) {
	s.settle()
	return s.durableJobStore.RequestJobCancel(ctx, ask, fence)
}

func TestJobsAJobThatSettlesAfterItWasAskedAboutIsNotSent(t *testing.T) {
	for _, engine := range engines {
		t.Run(engine, func(t *testing.T) {
			p := pairOf(t, "runner", nil, nil, "allow", jobKey)
			l, job := startedPair(t, engine, p)
			store := &settlesFirst{durableJobStore: l.store, settle: func() {
				if _, err := runcontext.CompleteJob(context.Background(), l.store, completion(job.Job, nil)); err != nil {
					t.Fatal(err)
				}
			}}
			view, err := runcontext.UnattendedRunStore(store, l.fence, runcontext.NewReferenceEffectHost(store))
			if err != nil {
				t.Fatal(err)
			}
			result := drive(t, view, engine, p.tools(), []jobCall{{"runner__cancel_job", cancelArgs(job.Job)}}, options{resume: true})
			held := onlyJob(t, l.store)
			rows := cancelRows(t, l.store)
			if p.sent.Load() != 0 || held.State != "completed" || held.Cancel != nil || len(rows) != 1 || rows[0].State != "unknown" || rows[0].Failure["outcome"] != "not-started" || !strings.Contains(fmt.Sprint(rows[0].Failure["detail"]), "JobCancelNotSent") {
				t.Fatalf("%d %+v %+v", p.sent.Load(), held, rows)
			}
			if !strings.Contains(lastToolResult(t, result), "completed") {
				t.Fatal(lastToolResult(t, result))
			}
		})
	}
}

func TestJobsAnAbandonTakesAFreshFenceAndSendsThroughTheEffectHost(t *testing.T) {
	ctx := context.Background()
	p := pairOf(t, "runner", nil, nil, "refuse", jobKey)
	l, job := startedPair(t, "eino", p)
	before, err := l.store.Load(ctx, jobRun)
	if err != nil || before == nil {
		t.Fatal(before, err)
	}
	inputs := []runcontext.JobCancelInput{}
	send := func(_ context.Context, input runcontext.JobCancelInput) error {
		inputs = append(inputs, input)
		return nil
	}
	answer, err := runcontext.AbandonJobs(ctx, l.store, l.host, jobRun, "cmd-1", jobHour, send)
	if err != nil {
		t.Fatal(err)
	}
	if answer.Fence == nil || answer.Fence.Owner != runcontext.JobAbandonOwner || answer.Fence.Epoch == l.fence.Epoch || len(answer.Jobs) != 1 || answer.Jobs[0].Delivery != "sent" {
		t.Fatalf("%+v", answer)
	}
	if len(inputs) != 1 || inputs[0].Handle == nil || *inputs[0].Handle != jobSecret || p.asked.Load() != 0 {
		t.Fatalf("%+v %d", inputs, p.asked.Load())
	}
	after, _ := l.store.Load(ctx, jobRun)
	if after.Revision != before.Revision {
		t.Fatal("an abandon committed to the run")
	}
	var refused *runcontext.LeaseRefused
	if _, err := l.store.CommitFenced(ctx, runcontext.RunCommit{RunID: jobRun, ExpectedRevision: before.Revision, CommitID: "late", Checkpoint: before.Checkpoint, Events: [][]byte{}}, l.fence); !errors.As(err, &refused) || refused.Reason != "stale-epoch" {
		t.Fatal(err)
	}
	held := onlyJob(t, l.store)
	if held.State != "started" || held.Cancel == nil || held.Cancel.Requested.By != "command" || held.Cancel.Requested.Operation != runcontext.JobCancelOperation("cmd-1", job.Job) {
		t.Fatalf("%+v", held)
	}
	// A retry of the command sends nothing twice: the effect host admits its stable operation once.
	again, err := runcontext.AbandonJobs(ctx, l.store, l.host, jobRun, "cmd-1", jobHour, send)
	if err != nil || len(inputs) != 1 || again.Jobs[0].Delivery != "already-admitted" {
		t.Fatalf("%+v %v %d", again, err, len(inputs))
	}
}

func TestJobsAnAbandonSaysWhatItCouldNotSend(t *testing.T) {
	ctx := context.Background()
	send := func(context.Context, runcontext.JobCancelInput) error { return errors.New("down") }
	abandon := func(t *testing.T, l *leasedRun) runcontext.JobAbandonAnswer {
		t.Helper()
		answer, err := runcontext.AbandonJobs(ctx, l.store, l.host, jobRun, "cmd-1", jobHour, send)
		if err != nil {
			t.Fatal(err)
		}
		return answer
	}
	t.Run("a failed cancel", func(t *testing.T) {
		l, _ := startedPair(t, "eino", pairOf(t, "runner", nil, nil, "allow", jobKey))
		if got := abandon(t, l); got.Jobs[0].Delivery != "failed" {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("no handle", func(t *testing.T) {
		l, _ := startedPair(t, "eino", pairOf(t, "runner", answered(runcontext.JobStartAnswer{Started: true}, nil), nil, "allow", jobKey))
		if got := abandon(t, l); got.Jobs[0].Delivery != "no-handle" {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("settled", func(t *testing.T) {
		l, job := startedPair(t, "eino", pairOf(t, "runner", nil, nil, "allow", jobKey))
		if _, err := runcontext.CompleteJob(ctx, l.store, completion(job.Job, nil)); err != nil {
			t.Fatal(err)
		}
		if got := abandon(t, l); got.Jobs[0].Delivery != "settled" {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("evidence on a closed job", func(t *testing.T) {
		l, job := startedPair(t, "eino", pairOf(t, "runner", answered(runcontext.JobStartAnswer{}, nil), nil, "allow", jobKey))
		if _, err := runcontext.CompleteJob(ctx, l.store, completion(job.Job, nil)); err != nil {
			t.Fatal(err)
		}
		if got := abandon(t, l); got.Jobs[0].Delivery != "contradicted" {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("the host accepted", func(t *testing.T) {
		p := pairOf(t, "runner", nil, nil, "allow", jobKey)
		l, job := startedPair(t, "eino", p)
		drive(t, l.view, "eino", p.tools(), []jobCall{{"runner__cancel_job", cancelArgs(job.Job)}}, options{resume: true})
		if _, err := runcontext.AcknowledgeJobCancel(ctx, l.store, runcontext.JobCancelAcknowledgement{Version: 1, Resource: jobRun, Job: job.Job, Generation: 1, Evidence: "word-1", Outcome: "accepted", Key: jobKey}); err != nil {
			t.Fatal(err)
		}
		if got := abandon(t, l); got.Jobs[0].Delivery != "acknowledged" {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("past its deadline", func(t *testing.T) {
		l, _ := startedPair(t, "eino", pairOf(t, "runner", nil, nil, "allow", jobKey))
		limit := l.clock.now().Add(time.Minute).UTC().Format("2006-01-02T15:04:05.000Z")
		if _, err := l.store.LimitRun(ctx, runcontext.RunLimit{Version: 1, Resource: jobRun, Run: limit}); err != nil {
			t.Fatal(err)
		}
		l.clock.move(time.Minute)
		got := abandon(t, l)
		held := onlyJob(t, l.store)
		if got.Fence != nil || got.Jobs[0].Delivery != "undelivered" || held.Cancel == nil || held.Cancel.Requested.Undelivered == nil {
			t.Fatalf("%+v %+v", got, held)
		}
	})
	t.Run("a digest is never the reference", func(t *testing.T) {
		l, _ := startedPair(t, "eino", pairOf(t, "runner", answered(runcontext.JobStartAnswer{Started: true, Handle: strings.Repeat("h", 300)}, nil), nil, "allow", jobKey))
		var sent runcontext.JobCancelInput
		if _, err := runcontext.AbandonJobs(ctx, l.store, l.host, jobRun, "cmd-1", jobHour, func(_ context.Context, input runcontext.JobCancelInput) error { sent = input; return nil }); err != nil {
			t.Fatal(err)
		}
		if sent.Handle != nil || sent.HandleDigest == nil {
			t.Fatalf("%+v", sent)
		}
	})
	t.Run("a host that only declares", func(t *testing.T) {
		l, _ := startedPair(t, "eino", pairOf(t, "runner", nil, nil, "allow", jobKey))
		var refused *runcontext.LeaseCapabilityRefused
		if _, err := runcontext.AbandonJobs(ctx, l.store, declaredOnly{}, jobRun, "cmd-1", jobHour, send); !errors.As(err, &refused) {
			t.Fatal(err)
		}
		if lease, _ := l.store.ReadLease(ctx, jobRun); lease == nil || lease.Epoch != l.fence.Epoch {
			t.Fatalf("%+v", lease)
		}
	})
}

func TestJobsAHeldCancelWhoseJobSettledBeforeTheDecisionIsNotSent(t *testing.T) {
	for _, engine := range engines {
		t.Run(engine, func(t *testing.T) {
			p := pairOf(t, "runner", nil, nil, "ask", jobKey)
			l, job := startedPair(t, engine, p)
			first := drive(t, l.view, engine, p.tools(), []jobCall{{"runner__cancel_job", cancelArgs(job.Job)}}, options{resume: true})
			callID := ""
			for _, event := range first.events {
				if event.ApprovalRequested != nil {
					callID = event.ApprovalRequested.CallID
				}
			}
			decision := heldProof(t, l.store, callID)
			if _, err := runcontext.CompleteJob(context.Background(), l.store, completion(job.Job, nil)); err != nil {
				t.Fatal(err)
			}
			result := drive(t, l.view, engine, p.tools(), nil, options{resume: true, decision: decision})
			if p.sent.Load() != 0 || !strings.Contains(toolAnswer(result), "JobRefused: job-settled") || len(cancelRows(t, l.store)) != 0 {
				t.Fatal(p.sent.Load(), toolAnswer(result), cancelRows(t, l.store))
			}
		})
	}
}

// askLost records the cancel and loses the acknowledgement, as a crash there leaves it.
type askLost struct {
	durableJobStore
	lost *atomic.Bool
}

func (s askLost) RequestJobCancel(ctx context.Context, ask runcontext.JobCancelAsk, fence runcontext.LeaseFence) (runcontext.RunJob, error) {
	job, err := s.durableJobStore.RequestJobCancel(ctx, ask, fence)
	if err == nil && s.lost.Swap(false) {
		return runcontext.RunJob{}, errors.New("acknowledgement lost")
	}
	return job, err
}

// F-17: a cancel whose effect host refuses after it was handed over reads as maybe sent, never as not started.
func TestJobsACancelWhoseHostRefusesAfterPerformMayHaveBeenSent(t *testing.T) {
	for _, engine := range engines {
		t.Run(engine, func(t *testing.T) {
			p := pairOf(t, "runner", nil, nil, "allow", jobKey)
			l, on := newLeased(t, nil), &atomic.Bool{}
			view := certifiedView(t, l, func(inner *runcontext.ReferenceEffectHost) runcontext.EffectHost {
				return refusingAfter{inner: inner, on: on, refusal: func() error { return &runcontext.LeaseRefused{Reason: "stale-epoch"} }}
			})
			drive(t, view, engine, p.tools(), []jobCall{{"runner__start_job", jobArgs}}, options{})
			job := onlyJob(t, l.store)
			on.Store(true)
			result := drive(t, view, engine, p.tools(), []jobCall{{"runner__cancel_job", cancelArgs(job.Job)}}, options{resume: true})
			rows := cancelRows(t, l.store)
			if p.sent.Load() != 1 || len(rows) != 1 || rows[0].State != "unknown" || !strings.HasPrefix(failureDetail(rows[0]), "JobCancelUnrecorded:") {
				t.Fatal(p.sent.Load(), rows)
			}
			// The model is told what the job says: a cancel requested, which may not have been delivered.
			if answer := lastToolResult(t, result); strings.Contains(answer, "this cancel was not sent") || !strings.Contains(answer, "cancel requested") {
				t.Fatal(answer)
			}
		})
	}
}

// refusingOnError is an effect host that answers any failure of its dispatch with its own LeaseRefused, once on.
type refusingOnError struct {
	inner *runcontext.ReferenceEffectHost
	on    *atomic.Bool
}

func (h refusingOnError) EffectFencing() string { return h.inner.EffectFencing() }
func (h refusingOnError) Dispatch(ctx context.Context, admission runcontext.EffectAdmission, perform func(context.Context) error) error {
	err := h.inner.Dispatch(ctx, admission, perform)
	if err != nil && h.on.Load() {
		return &runcontext.LeaseRefused{Reason: "stale-epoch"}
	}
	return err
}

// agent#323 LOW-1: the host's cancel fails after its request left, so the execute never returns, and the effect host
// answers with its own LeaseRefused. The cancel was handed over: its row is keyed on that, not on the execute returning.
func TestJobsACancelWhoseHostThrowsAfterItWasHandedOverMayHaveBeenSent(t *testing.T) {
	for _, engine := range engines {
		t.Run(engine, func(t *testing.T) {
			p := pairOf(t, "runner", nil, func(context.Context, runcontext.JobCancelInput) error {
				return errors.New("the request left; its answer did not come back")
			}, "allow", jobKey)
			l, on := newLeased(t, nil), &atomic.Bool{}
			view := certifiedView(t, l, func(inner *runcontext.ReferenceEffectHost) runcontext.EffectHost {
				return refusingOnError{inner: inner, on: on}
			})
			drive(t, view, engine, p.tools(), []jobCall{{"runner__start_job", jobArgs}}, options{})
			job := onlyJob(t, l.store)
			on.Store(true)
			result := drive(t, view, engine, p.tools(), []jobCall{{"runner__cancel_job", cancelArgs(job.Job)}}, options{resume: true})
			rows := cancelRows(t, l.store)
			if p.sent.Load() != 1 || len(rows) != 1 || rows[0].State != "unknown" || !strings.HasPrefix(failureDetail(rows[0]), "JobCancelUnrecorded:") {
				t.Fatal(p.sent.Load(), rows)
			}
			if answer := lastToolResult(t, result); strings.Contains(answer, "this cancel was not sent") || !strings.Contains(answer, "cancel requested") {
				t.Fatal(answer)
			}
		})
	}
}

// F-20: an effect host that refuses with LeaseRefused before anything was handed over: the cancel was not sent.
func TestJobsACancelWhoseHostRefusesBeforePerformWasNotSent(t *testing.T) {
	for _, engine := range engines {
		t.Run(engine, func(t *testing.T) {
			p := pairOf(t, "runner", nil, nil, "allow", jobKey)
			l, on := newLeased(t, nil), &atomic.Bool{}
			view := certifiedView(t, l, func(inner *runcontext.ReferenceEffectHost) runcontext.EffectHost {
				return failingHost{inner: inner, on: on, err: &runcontext.LeaseRefused{Reason: "other-owner"}}
			})
			drive(t, view, engine, p.tools(), []jobCall{{"runner__start_job", jobArgs}}, options{})
			job := onlyJob(t, l.store)
			on.Store(true)
			result := drive(t, view, engine, p.tools(), []jobCall{{"runner__cancel_job", cancelArgs(job.Job)}}, options{resume: true})
			if p.sent.Load() != 0 || !strings.Contains(lastToolResult(t, result), "this cancel was not sent") {
				t.Fatal(p.sent.Load(), lastToolResult(t, result))
			}
		})
	}
}

// F-2b: a store that answers the job asked for with another job's handle, or another source, sends nothing. agent#323
// LOW-3: a store that alters its read of the job as it alters its ask is held to what the writer saw of the job at attach.
func TestJobsACancelSendsNothingTheRecordReadBeforeDoesNotSay(t *testing.T) {
	for _, engine := range engines {
		for _, both := range []bool{false, true} {
			// read is what a store that also alters its read is answered with: a job read under another source or tool is
			// not this pair's, and is refused before anything is asked (jobCancelRefusal); the others reach the ask.
			for _, c := range []struct {
				name, first, read string
				alter             func(*runcontext.RunJob)
			}{
				{"another job's handle", "H-ONE", "this cancel was not sent", func(job *runcontext.RunJob) { other := "H-TWO"; job.Handle = &other }},
				{"another source", "H-ONE", "JobRefused: no-job.", func(job *runcontext.RunJob) { job.Source = "other-source" }},
				// A handle kept as its digest, answered without its marker, would go out as the reference.
				{"its digest marker dropped", strings.Repeat("L", 300), "this cancel was not sent", func(job *runcontext.RunJob) { job.HandleDigested = nil }},
				{"another tool", "H-ONE", "JobRefused: no-job.", func(job *runcontext.RunJob) { job.Tool = "other-tool" }},
			} {
				t.Run(fmt.Sprintf("%s/%s/read-altered=%t", engine, c.name, both), func(t *testing.T) {
					p := pairOf(t, "runner", answered(runcontext.JobStartAnswer{Started: true, Handle: c.first}, nil), nil, "allow", jobKey)
					l := newLeased(t, nil)
					drive(t, l.view, engine, p.tools(), []jobCall{{"runner__start_job", `{"name":"one"}`}}, options{})
					second := pairOf(t, "runner", answered(runcontext.JobStartAnswer{Started: true, Handle: "H-TWO"}, nil), nil, "allow", "job.key.0002.Hs8Pq3Lz6Wd1Rm9Tc4Vb7Nx2Ka5U")
					drive(t, l.view, engine, second.tools(), []jobCall{{"runner__start_job", `{"name":"two"}`}}, options{resume: true})
					page, _ := runcontext.ListJobs(context.Background(), l.store, jobRun, "", 10)
					if len(page.Jobs) != 2 {
						t.Fatalf("%+v", page.Jobs)
					}
					first := page.Jobs[0].Job
					if page.Jobs[0].Handle != nil && *page.Jobs[0].Handle == "H-TWO" {
						first = page.Jobs[1].Job
					}
					store := answersAltered{l.store, &c.alter}
					view, err := runcontext.UnattendedRunStore(store, l.fence, runcontext.NewReferenceEffectHost(store))
					if both {
						store := readsAltered{store, first}
						view, err = runcontext.UnattendedRunStore(store, l.fence, runcontext.NewReferenceEffectHost(store))
					}
					if err != nil {
						t.Fatal(err)
					}
					result := drive(t, view, engine, p.tools(), []jobCall{{"runner__cancel_job", cancelArgs(first)}}, options{resume: true})
					want := "this cancel was not sent"
					if both {
						want = c.read
					}
					if p.sent.Load() != 0 || second.sent.Load() != 0 || !strings.Contains(lastToolResult(t, result), want) {
						t.Fatal(p.sent.Load(), second.sent.Load(), lastToolResult(t, result))
					}
				})
			}
		}
	}
}

// failingHost fails a dispatch before anything is performed, while on: with err, or a plain error.
type failingHost struct {
	inner *runcontext.ReferenceEffectHost
	on    *atomic.Bool
	err   error
}

func (h failingHost) EffectFencing() string { return h.inner.EffectFencing() }
func (h failingHost) Dispatch(ctx context.Context, admission runcontext.EffectAdmission, perform func(context.Context) error) error {
	if h.on.Load() {
		if h.err != nil {
			return h.err
		}
		return errors.New("the authority is down")
	}
	return h.inner.Dispatch(ctx, admission, perform)
}

func TestJobsACancelThatNeverLeftSaysItWasNotSent(t *testing.T) {
	ctx := context.Background()
	for _, engine := range engines {
		t.Run(engine+"/an acknowledgement lost after requested", func(t *testing.T) {
			p := pairOf(t, "runner", nil, nil, "allow", jobKey)
			l, job := startedPair(t, engine, p)
			lost := &atomic.Bool{}
			lost.Store(true)
			store := askLost{l.store, lost}
			view, err := runcontext.UnattendedRunStore(store, l.fence, runcontext.NewReferenceEffectHost(store))
			if err != nil {
				t.Fatal(err)
			}
			first := drive(t, view, engine, p.tools(), []jobCall{{"runner__cancel_job", cancelArgs(job.Job)}}, options{resume: true})
			held := onlyJob(t, l.store)
			if p.sent.Load() != 0 || held.Cancel == nil || !strings.Contains(lastToolResult(t, first), "this cancel was not sent") {
				t.Fatal(p.sent.Load(), held.Cancel, lastToolResult(t, first))
			}
			repeat := drive(t, view, engine, p.tools(), []jobCall{{"runner__cancel_job", cancelArgs(job.Job)}}, options{resume: true})
			if p.sent.Load() != 0 || !strings.Contains(lastToolResult(t, repeat), "may not have been delivered") {
				t.Fatal(p.sent.Load(), lastToolResult(t, repeat))
			}
		})
		t.Run(engine+"/an effect host that fails", func(t *testing.T) {
			p := pairOf(t, "runner", nil, nil, "allow", jobKey)
			l := newLeased(t, nil)
			on := &atomic.Bool{}
			certified, err := runcontext.CertifyEffectHost(ctx, func(authority runcontext.LeaseAuthority) runcontext.EffectHost {
				return failingHost{inner: runcontext.NewReferenceEffectHost(authority), on: on}
			}, l.store)
			if err != nil {
				t.Fatal(err)
			}
			view, err := runcontext.FencedRunStore(l.store, l.fence, certified)
			if err != nil {
				t.Fatal(err)
			}
			drive(t, view, engine, p.tools(), []jobCall{{"runner__start_job", jobArgs}}, options{})
			job := onlyJob(t, l.store)
			on.Store(true)
			result := drive(t, view, engine, p.tools(), []jobCall{{"runner__cancel_job", cancelArgs(job.Job)}}, options{resume: true})
			held := onlyJob(t, l.store)
			if p.sent.Load() != 0 || held.Cancel == nil || !strings.Contains(lastToolResult(t, result), "this cancel was not sent") {
				t.Fatal(p.sent.Load(), held.Cancel, lastToolResult(t, result))
			}
		})
		// The run's own reads inside the dispatch, before the cancel is handed over: a delegation's counted read
		// is one, and the host's Authorize is another at the same point. Go's job tests have no delegated driver,
		// so Authorize stands in for it here; TypeScript drives the delegation itself.
		t.Run(engine+"/a refusal inside the dispatch", func(t *testing.T) {
			p := pairOf(t, "runner", nil, nil, "allow", jobKey)
			l, job := startedPair(t, engine, p)
			// It admits the run's opening and refuses once the cancel is asked for: the point a counted read is at.
			refuse := func(ctx context.Context) error {
				if held, err := runcontext.ReadJob(ctx, l.store, jobRun, job.Job); err == nil && held != nil && held.Cancel != nil {
					return failure.Authored("permission", "revoked", "context")
				}
				return nil
			}
			result := drive(t, l.view, engine, p.tools(), []jobCall{{"runner__cancel_job", cancelArgs(job.Job)}}, options{resume: true, authorize: refuse})
			held := onlyJob(t, l.store)
			if p.sent.Load() != 0 || held.Cancel == nil || !strings.Contains(lastToolResult(t, result), "this cancel was not sent") {
				t.Fatal(p.sent.Load(), held.Cancel, lastToolResult(t, result))
			}
		})
	}
}

// otherJobAnswer records the cancel and answers another job's record.
type otherJobAnswer struct {
	durableJobStore
	other *string
}

func (s otherJobAnswer) RequestJobCancel(ctx context.Context, ask runcontext.JobCancelAsk, fence runcontext.LeaseFence) (runcontext.RunJob, error) {
	if _, err := s.durableJobStore.RequestJobCancel(ctx, ask, fence); err != nil {
		return runcontext.RunJob{}, err
	}
	held, err := s.durableJobStore.ReadJob(ctx, ask.Resource, *s.other)
	if err != nil || held == nil {
		return runcontext.RunJob{}, err
	}
	return *held, nil
}

func TestJobsWhatIsSentIsTheRecordOfTheJobAskedFor(t *testing.T) {
	for _, engine := range engines {
		t.Run(engine, func(t *testing.T) {
			p := pairOf(t, "runner", nil, nil, "allow", jobKey)
			l := newLeased(t, nil)
			drive(t, l.view, engine, p.tools(), []jobCall{{"runner__start_job", `{"name":"one"}`}}, options{})
			page, _ := runcontext.ListJobs(context.Background(), l.store, jobRun, "", 10)
			first := page.Jobs[0].Job
			second := pairOf(t, "runner", nil, nil, "allow", "job.key.0002.Hs8Pq3Lz6Wd1Rm9Tc4Vb7Nx2Ka5U")
			drive(t, l.view, engine, second.tools(), []jobCall{{"runner__start_job", `{"name":"two"}`}}, options{resume: true})
			page, _ = runcontext.ListJobs(context.Background(), l.store, jobRun, "", 10)
			if len(page.Jobs) != 2 {
				t.Fatalf("%+v", page.Jobs)
			}
			other := page.Jobs[1].Job
			store := otherJobAnswer{l.store, &other}
			view, err := runcontext.UnattendedRunStore(store, l.fence, runcontext.NewReferenceEffectHost(store))
			if err != nil {
				t.Fatal(err)
			}
			result := drive(t, view, engine, p.tools(), []jobCall{{"runner__cancel_job", cancelArgs(first)}}, options{resume: true})
			if p.sent.Load() != 0 || !strings.Contains(lastToolResult(t, result), "this cancel was not sent") {
				t.Fatal(p.sent.Load(), lastToolResult(t, result))
			}
		})
	}
}
