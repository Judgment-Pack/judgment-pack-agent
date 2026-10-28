package runcontext

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestOpeningProjectionRefusesAmbiguousOrBroadenedInput(t *testing.T) {
	for _, mode := range []string{"duplicate-member", "duplicate-path", "overlap", "ancestor", "missing", "nonstring", "changed", "expanded", "too-many-fits", "cancelled", "invalid-utf8", "invalid-preview"} {
		t.Run(mode, func(t *testing.T) {
			sc := openContext(t, windowed(16384, nil))
			defer sc.Close()
			raw := `{"protected":9007199254740993,"source":{"preview":"abcdefgh"},"count":1}`
			paths := []string{"/source/preview"}
			replacement := "abc"
			switch mode {
			case "invalid-utf8":
				raw = "{\"source\":{\"preview\":\"" + string([]byte{0xc3}) + "\"}}"
			case "invalid-preview":
				raw = `{"source":{"preview":"é"}}`
				replacement = string([]byte{0xc3})
			case "duplicate-member":
				raw = `{"source":{"preview":"abcdefgh","preview":"other"}}`
			case "duplicate-path":
				paths = append(paths, paths[0])
			case "overlap":
				paths = append(paths, "/source/preview/child")
			case "ancestor":
				paths = []string{"/source"}
			case "missing":
				paths = []string{"/source/missing"}
			case "nonstring":
				paths = []string{"/count"}
			case "changed":
				replacement = "other"
			case "expanded":
				replacement = "abcdefghi"
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			hook := &OpeningProjection{PreviewPaths: paths, Project: func(_ context.Context, in OpeningProjectionInput) (map[string]string, error) {
				if mode == "too-many-fits" {
					for i := 0; i < 33; i++ {
						_, _ = in.Fit(nil)
					}
				}
				if mode == "cancelled" {
					cancel()
				}
				return map[string]string{paths[0]: replacement}, nil
			}}
			if _, err := sc.OpeningProjected(ctx, raw, "instructions", noTools(), nil, hook); err == nil {
				t.Fatal("unsafe structured projection was accepted")
			}
		})
	}
}
func TestOpeningProjectionDetachedFitExpiresAndFinalEnvelopeGuard(t *testing.T) {
	sc := openContext(t, windowed(16384, nil))
	defer sc.Close()
	raw := `{"protected":9007199254740993,"source":{"preview":"abcdefgh"}}`
	var saved func(map[string]string) (OpeningFit, error)
	hook := &OpeningProjection{PreviewPaths: []string{"/source/preview"}, Project: func(_ context.Context, in OpeningProjectionInput) (map[string]string, error) {
		saved = in.Fit
		fit, err := in.Fit(map[string]string{"/source/preview": "abc"})
		if err != nil || !fit.Fits || fit.Bytes <= 0 || fit.Allowance <= 0 {
			t.Fatalf("canonical fit absent: %+v %v", fit, err)
		}
		in.Input[0] = '['
		return map[string]string{"/source/preview": "abc"}, nil
	}}
	parts, err := sc.OpeningProjected(context.Background(), raw, "instructions", noTools(), nil, hook)
	if err != nil {
		t.Fatal(err)
	}
	var held map[string]json.RawMessage
	if len(parts) != 1 || json.Unmarshal([]byte(parts[0]), &held) != nil || string(held["protected"]) != "9007199254740993" {
		t.Fatal("protected complete JSON changed")
	}
	if _, err = saved(nil); err == nil {
		t.Fatal("late fit retained context authority")
	}
	if err = sc.CheckPhysicalRequest([]byte(strings.Repeat("x", InputBytes(*sc.Policy)+1))); err == nil {
		t.Fatal("complete physical envelope escaped canonical input budget")
	}
}
