package historycapture_test

import (
	"encoding/json"
	capture "github.com/Judgment-Pack/judgment-pack-agent/go/internal/historycapture"
	"os"
	"strings"
	"testing"
)

func fixture(t *testing.T) ([]capture.Admission, []json.RawMessage, capture.Terminal) {
	t.Helper()
	raw, e := os.ReadFile("../../../contract/fixtures/history/capture.json")
	if e != nil {
		t.Fatal(e)
	}
	var v struct {
		Admissions []capture.Admission `json:"admissions"`
		Frames     []json.RawMessage   `json:"frames"`
		Terminal   capture.Terminal    `json:"terminal"`
	}
	if json.Unmarshal(raw, &v) != nil {
		t.Fatal("fixture")
	}
	return v.Admissions, v.Frames, v.Terminal
}
func TestCapturePrivateAdmissionOrderAndSealCopies(t *testing.T) {
	a, f, terminal := fixture(t)
	owner := &struct{}{}
	c, root, e := capture.New(owner, "r", capture.Scope{Execution: "r", Role: "author"})
	if e != nil {
		t.Fatal(e)
	}
	one, e := c.Admit(owner, a[0])
	if e != nil {
		t.Fatal(e)
	}
	two, e := c.Admit(owner, a[1])
	if e != nil {
		t.Fatal(e)
	}
	for _, x := range []struct {
		token *capture.Token
		raw   json.RawMessage
	}{{two, f[1]}, {one, f[0]}} {
		if e = c.Record(owner, x.token, x.raw); e != nil {
			t.Fatal(e)
		}
		if e = c.Acknowledge(owner, x.token); e != nil {
			t.Fatal(e)
		}
	}
	seal, e := c.Seal(owner, root, terminal)
	if e != nil {
		t.Fatal(e)
	}
	m, e := seal.Material()
	if e != nil || m.Rows[0].Admission.Identity.DispatchID != "d1" || m.Rows[1].Admission.Identity.DispatchID != "d2" {
		t.Fatal("completion order replaced admission order", e)
	}
	m.Rows[0].Frame[0] = '!'
	again, _ := seal.Material()
	if again.Rows[0].Frame[0] == '!' {
		t.Fatal("seal alias")
	}
	copied := *seal
	if _, e = copied.Material(); e == nil {
		t.Fatal("copied seal admitted")
	}
}
func TestCaptureInvalidAuthorityOrClosureRefused(t *testing.T) {
	for _, fault := range []string{"owner", "token", "missing", "unacknowledged", "duplicate", "admission", "order", "child-root"} {
		t.Run(fault, func(t *testing.T) {
			a, f, terminal := fixture(t)
			owner := &struct{ ID int }{1}
			c, root, e := capture.New(owner, "r", capture.Scope{Execution: "r", Role: "author"})
			if e != nil {
				t.Fatal(e)
			}
			token, e := c.Admit(owner, a[0])
			if e != nil {
				t.Fatal(e)
			}
			switch fault {
			case "owner":
				e = c.Record(&struct{ ID int }{2}, token, f[0])
			case "token":
				copy := *token
				e = c.Record(owner, &copy, f[0])
			case "duplicate":
				if e = c.Record(owner, token, f[0]); e != nil {
					t.Fatal(e)
				}
				e = c.Record(owner, token, f[0])
			case "admission":
				a[0].Sequence = 2
				_, e = c.Admit(owner, a[0])
			case "order":
				a[1].Sequence = 1
				a[1].Ordinal = 0
				_, e = c.Admit(owner, a[1])
			default:
				if fault != "missing" {
					if e = c.Record(owner, token, f[0]); e != nil {
						t.Fatal(e)
					}
				}
				if fault != "unacknowledged" {
					if e = c.Acknowledge(owner, token); e != nil {
						t.Fatal(e)
					}
				}
				if fault == "child-root" {
					root = nil
				}
				_, e = c.Seal(owner, root, terminal)
			}
			if e == nil {
				t.Fatal("invalid capture admitted", fault)
			}
		})
	}
}

func TestCaptureMarkerOwnerAndMalformedMaterialRefused(t *testing.T) {
	if _, _, e := capture.New("marker", "r", capture.Scope{Execution: "r", Role: "author"}); e == nil {
		t.Fatal("caller owner marker admitted")
	}
	for _, fault := range []string{"empty", "missing", "oversized"} {
		t.Run(fault, func(t *testing.T) {
			a, _, _ := fixture(t)
			owner := &struct{ ID int }{1}
			c, _, e := capture.New(owner, "r", capture.Scope{Execution: "r", Role: "author"})
			if e != nil {
				t.Fatal(e)
			}
			if fault == "empty" {
				a[0].Identity.DispatchID = ""
			} else if fault == "missing" {
				a[0].Identity = capture.Identity{}
			} else {
				a[0].Request = strings.Repeat("x", 4*1024*1024+1)
			}
			if _, e = c.Admit(owner, a[0]); e == nil {
				t.Fatal("malformed admission admitted", fault)
			}
		})
	}
}
