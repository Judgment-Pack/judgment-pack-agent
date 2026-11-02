package principaltests

// Moved from go/adapter/run_reauth_host_test.go unchanged: the heaviest test of that family, beside these
// so that neither binary holds both of its heaviest (#254).

import (
	"strings"
	"testing"

	runcontext "github.com/Judgment-Pack/judgment-pack-agent/go/context"
	"github.com/Judgment-Pack/judgment-pack-agent/go/failure"
)

// H1: the re-send is admitted, then fails with a failure of the typed shape. No later view sends it again,
// whatever the effect host remembers: that is the row the grant reads.
func TestReauthorizationNoLaterViewSendsAnAdmittedCall(t *testing.T) {
	var kept *runcontext.ReauthorizationRequired
	for name, fails := range map[string]func() error{
		"the exported type": func() error { return &runcontext.ReauthorizationRequired{} },
		"a typed failure of the same shape": func() error {
			f := failure.New("authentication", "tool", "not-started")
			detail := "SENTINEL-HOST-DETAIL the operation was not admitted"
			f.Detail = &detail
			return failure.NewError(f)
		},
		"the run's own proof, kept and returned again": func() error { return kept },
	} {
		for _, engine := range hostEngines {
			t.Run(name+"/"+engine, func(t *testing.T) {
				s := hostRefused(t, engine, nil, func(call int32) error {
					if call == 1 {
						return fails()
					}
					return nil
				})
				kept = runcontext.ReauthorizationOf(s.view)
				resumed := s.claimed(t)
				first := runHostTool(t, s.viewOf(t, resumed, s.host), "run-r", engine, true, nil, s.tool, []string{hostArgs})
				rows := hostRows(t, s.store, "run-r")
				if s.executed.Load() != 1 || !unknownFailure(rows[len(rows)-1]) {
					t.Fatalf("executed %d rows %+v", s.executed.Load(), rows)
				}
				if strings.Contains(strings.Join(first.bodies, "\n"), "SENTINEL-HOST-DETAIL") {
					t.Fatal("the failure's detail reached the model")
				}
				// A second view of the claim, over the same host: not sent, so the host is not even asked.
				_ = runHostTool(t, s.viewOf(t, resumed, s.host), "run-r", engine, true, nil, s.tool, []string{hostArgs})
				if s.executed.Load() != 1 || len(s.host.Refusals()) != 0 {
					t.Fatalf("executed %d refusals %+v", s.executed.Load(), s.host.Refusals())
				}
				// Over a new reference host, as after a restart under the held lease: its admissions are gone.
				_ = runHostTool(t, s.viewOf(t, resumed, runcontext.NewReferenceEffectHost(s.store)), "run-r", engine, true, nil, s.tool, []string{hostArgs})
				if s.executed.Load() != 1 {
					t.Fatalf("the admitted call ran %d times", s.executed.Load())
				}
			})
		}
	}
}
