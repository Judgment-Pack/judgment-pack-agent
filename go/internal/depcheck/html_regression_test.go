package depcheck

import (
	"bytes"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// GO-2026-6866: every foreign ancestor transition must permit raw text before
// Render emits it literally. The nearest SVG integration point alone is insufficient.
func TestHTMLRenderForeignAncestorTransitions(t *testing.T) {
	for _, branch := range []string{"mglyph", "mglyph><svg><desc", "malignmark><svg><desc"} {
		t.Run(branch, func(t *testing.T) {
			prefix := "<math><mtext><table><" + branch + "><style>"
			doc, err := html.Parse(strings.NewReader(prefix + "<b>kept</b>"))
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			if err := html.Render(&output, doc); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), "<style>&lt;b&gt;kept&lt;/b&gt;</style>") {
				t.Fatalf("foreign ancestor transition emitted literal markup: %s", output.String())
			}
		})
	}
}

// GO-2026-6860: ignored end tags must not cause one allocation per concatenation
// of an ever-growing TextNode. Each text token may allocate its string; foster
// parenting may additionally create one node per token. Buffer growth and parser
// setup have a fixed allowance, without permitting another allocation per run.
func TestHTMLCoalescedTextAllocationBound(t *testing.T) {
	const runs = 4096
	for _, row := range []struct {
		name, prefix string
		limit        float64
	}{
		{"ordinary", "<div>", runs + 256},
		{"foster-parented", "<table>", 2*runs + 256},
	} {
		t.Run(row.name, func(t *testing.T) {
			input := row.prefix + strings.Repeat("a</x>", runs)
			allocations := testing.AllocsPerRun(3, func() {
				doc, err := html.Parse(strings.NewReader(input))
				if err != nil {
					t.Fatal(err)
				}
				text := ""
				var visit func(*html.Node)
				visit = func(n *html.Node) {
					if n.Type == html.TextNode {
						text += n.Data
					}
					for child := n.FirstChild; child != nil; child = child.NextSibling {
						visit(child)
					}
				}
				visit(doc)
				if text != strings.Repeat("a", runs) {
					t.Fatal("coalesced text changed")
				}
			})
			if allocations > row.limit {
				t.Fatalf("coalesced text allocations %.0f exceed %.0f", allocations, row.limit)
			}
		})
	}
}
