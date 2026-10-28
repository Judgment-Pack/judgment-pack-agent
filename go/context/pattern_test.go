package runcontext

// The search pattern (the port of the "the pattern" cases of
// packages/harness/context/search.test.ts).
//
// The TypeScript compares its engine with the browser's own on a fixed list of patterns, on two
// thousand random ones, and on the case class of every cased code point. There is no browser
// here, so the fixed list is compared with Go's regexp (RE2, the same construction) on the lines
// where the two agree, each pattern written twice: once as the search's subset states it and once
// in Go's syntax. The case classes are compared with Unicode's own simple folding
// (unicode.SimpleFold), which is what the browser's iu reads, and the cases the TypeScript lists
// are written out with the browser's answer for each.

import (
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"
)

// patternLines are the lines both engines are run over (LINES in search.test.ts).
var patternLines = []string{
	"A claim of 50 USD or less is approved.", "abcdabcd and cdab", "éé and 😀 and x😀y",
	"aaaaaaaaaaaaaaaaaaaaaaaab", "xxxxxxxxxxxxxxxxxxxxxxxxxy", "1,234.56 EUR", "at bat atom",
	"zzz", "", "abcd", "AI ai rain",
}

// patternPair is one pattern as the search takes it, and the same pattern in Go's syntax.
type patternPair struct{ source, native string }

var patternPairs = []patternPair{
	{"USD", "USD"}, {"^A claim", "^A claim"}, {`approved\.$`, `approved\.$`},
	{`\d+ (USD|EUR)`, `\d+ (USD|EUR)`}, {"[A-Z][a-z]+ is", "[A-Z][a-z]+ is"}, {"c[^l]", "c[^l]"},
	{`claim.*?\d`, `claim.*?\d`}, {`\bat\b`, `\bat\b`}, {`\Bai`, `\Bai`}, {"x?y*z+", "x?y*z+"},
	{"(?:ab|cd){2,3}", "(?:ab|cd){2,3}"}, {"a{2}", "a{2}"}, {`[\d,]+\.\d{2}`, `[\d,]+\.\d{2}`},
	{`(?<amount>\d+) USD`, `(?P<amount>\d+) USD`}, {"é+", "é+"}, {`é`, `\x{e9}`},
	{`\x41`, `\x41`}, {"😀", "😀"}, {".😀.", ".😀."}, {`[^\s]+$`, `[^\s]+$`},
	{"(a|ab)(c|bcd)(d*)", "(a|ab)(c|bcd)(d*)"}, {`(a+)+$`, `(a+)+$`}, {"(x|x)*y", "(x|x)*y"},
	{`\s\S\w\W\d\D`, `\s\S\w\W\d\D`},
}

func TestThePatternMatchesWhereRE2Does(t *testing.T) {
	for _, pair := range patternPairs {
		for _, ignoreCase := range []bool{false, true} {
			compiled, err := CompilePattern(pair.source, ignoreCase)
			if err != nil {
				t.Fatalf("%s: %v", pair.source, err)
			}
			source := pair.native
			if ignoreCase {
				source = "(?i)" + source
			}
			native, err := regexp.Compile(source)
			if err != nil {
				t.Fatalf("%s is not a Go pattern: %v", pair.native, err)
			}
			for _, line := range patternLines {
				want := -1
				if at := native.FindStringIndex(line); at != nil {
					want = U16Length(line[:at[0]])
				}
				held, read := FirstMatch(compiled, line, &PatternBudget{Steps: SearchSteps})
				if !read {
					t.Fatalf("%s on %q ran out of work", pair.source, line)
				}
				if held != want {
					t.Errorf("%s (i=%v) on %q: this engine says %d, RE2 %d", pair.source, ignoreCase, line, held, want)
				}
			}
		}
	}
}

func TestThePatternFoldsCaseThroughTheClosureOfTheCaseMappings(t *testing.T) {
	matches := func(pattern, line string) bool {
		compiled, err := CompilePattern(pattern, true)
		if err != nil {
			t.Fatalf("%s: %v", pattern, err)
		}
		at, read := FirstMatch(compiled, line, &PatternBudget{Steps: SearchSteps})
		return read && at == 0
	}
	// The cases search.test.ts lists, with the browser's iu answer for each: σ, ς and Σ are one
	// letter; so are k, K and the Kelvin sign; s, S and ſ; ß and ẞ; µ and μ. The dotless ı and
	// the dotted İ have no simple case folding and match only themselves.
	cases := []struct {
		letter, other string
		browser       bool
	}{
		{"σ", "ς", true}, {"ς", "Σ", true}, {"Σ", "σ", true}, {"k", "K", true},
		{"K", "K", true}, {"s", "ſ", true}, {"ſ", "S", true}, {"ı", "I", false},
		{"ı", "i", false}, {"İ", "i", false}, {"I", "i", true}, {"ß", "ẞ", true}, {"µ", "μ", true},
	}
	for _, held := range cases {
		if matches(held.letter, held.other) != held.browser {
			t.Errorf("%s and %s: this engine says %v", held.letter, held.other, !held.browser)
		}
		if matches("["+held.letter+"]", held.other) != held.browser {
			t.Errorf("[%s] and %s: this engine says %v", held.letter, held.other, !held.browser)
		}
		if matches("[^"+held.letter+"]", held.other) == held.browser {
			t.Errorf("[^%s] and %s: this engine says %v", held.letter, held.other, held.browser)
		}
	}
	// \w folds too: ſ and the Kelvin sign are word characters under ignoreCase, and \b reads them so.
	if !matches(`\w`, "ſ") {
		t.Error("ſ is not a word character under ignoreCase")
	}
	compiled, err := CompilePattern(`\bk`, true)
	if err != nil {
		t.Fatal(err)
	}
	if at, _ := FirstMatch(compiled, "aK", &PatternBudget{Steps: SearchSteps}); at != -1 {
		t.Errorf("\\bk matched the Kelvin sign after a word character at %d", at)
	}
}

func TestTheCaseClassesAreUnicodesOwnSimpleFolding(t *testing.T) {
	// The TypeScript compares every cased code point with the browser's iu. Unicode's simple case
	// folding is what that reads, so the classes this package builds from the case mappings are
	// held to it, with the two letters the TypeScript leaves out named.
	letters, disagreements := 0, []string{}
	for code := rune(0); code <= 0x1ffff; code++ {
		if code >= 0xd800 && code <= 0xdfff {
			continue
		}
		orbit := map[rune]bool{code: true}
		for held := unicode.SimpleFold(code); held != code; held = unicode.SimpleFold(held) {
			orbit[held] = true
		}
		if len(orbit) > 1 {
			letters++
		}
		class := map[rune]bool{}
		for _, held := range Folded(code) {
			class[held] = true
		}
		if code == 0x0130 || code == 0x0131 {
			// The dotless ı and the dotted İ: no simple case folding, and this package leaves them
			// out, so each matches only itself.
			if len(class) != 1 || !class[code] {
				disagreements = append(disagreements, "U+0130/U+0131")
			}
			continue
		}
		if len(class) != len(orbit) {
			disagreements = append(disagreements, string(code))
			continue
		}
		for held := range orbit {
			if !class[held] {
				disagreements = append(disagreements, string(code))
				break
			}
		}
	}
	if letters < 2000 {
		t.Errorf("only %d cased code points were compared", letters)
	}
	if len(disagreements) > 0 {
		t.Errorf("%d code points disagree, among them %q", len(disagreements), disagreements[:min(5, len(disagreements))])
	}
}

func TestDollarAlsoMatchesBeforeAFinalCarriageReturn(t *testing.T) {
	compiled, err := CompilePattern("end$", false)
	if err != nil {
		t.Fatal(err)
	}
	if at, _ := FirstMatch(compiled, "the end\r", &PatternBudget{Steps: SearchSteps}); at != 4 {
		t.Errorf("the match is at %d", at)
	}
	if at, _ := FirstMatch(compiled, "the end\rmore", &PatternBudget{Steps: SearchSteps}); at != -1 {
		t.Errorf("a carriage return in the middle matched at %d", at)
	}
}

func TestThePatternRunsInTimeLinearInTheLine(t *testing.T) {
	line := strings.Repeat("a", 50_000) + "!"
	// No literal any match must contain, so the quick refusal of a line cannot answer for the machine.
	for _, source := range []string{`(a+)+$`, `(a|a)*[bc]`, `(a*)*[bc]`, `.*.*.*[z]`, `(?:a|aa)+[c]`} {
		compiled, err := CompilePattern(source, false)
		if err != nil {
			t.Fatal(err)
		}
		budget := &PatternBudget{Steps: 50_000_000}
		started := time.Now()
		at, read := FirstMatch(compiled, line, budget)
		if !read || at != -1 {
			t.Errorf("%s answered %d (%v)", source, at, read)
		}
		if elapsed := time.Since(started); elapsed > 3*time.Second {
			t.Errorf("%s took %s", source, elapsed)
		}
		// The work is bounded by the line's length times the program's.
		if spent := 50_000_000 - budget.Steps; spent >= len(line)*64 {
			t.Errorf("%s spent %d steps", source, spent)
		}
	}
}

func TestThePatternRefusesWithASentenceAPersonCanActOn(t *testing.T) {
	for _, held := range []struct{ source, want string }{
		{"(open", "a group is not closed"},
		{"close)", "never opened"},
		{"*a", "needs something before it"},
		{"a**", "cannot itself be repeated"},
		{"[z-a]", "lower character to a higher"},
		{"a{3,2}", "n at most m"},
		{"{", `write \{`},
		{`\q`, "not an escape"},
		{"", "non-empty"},
	} {
		_, err := CompilePattern(held.source, false)
		refused(t, err, held.want)
		if _, ok := err.(*PatternError); !ok {
			t.Errorf("%s was refused with %T", held.source, err)
		}
	}
}

// UTF-16 ranges and work are hand-counted for these small instruction graphs.
func TestMatchRangesWorkspaceEdgesAndBudget(t *testing.T) {
	cases := []struct {
		pattern, text string
		limit         int
		ranges        [][2]int
		ok            bool
		remaining     int
	}{
		{"a", "", 100, [][2]int{}, true, 96},
		{"a", "a", 100, [][2]int{{0, 1}}, true, 92},
		{"a", "aa", 100, [][2]int{{0, 1}, {1, 2}}, true, 88},
		{"a{0}", "a", 100, [][2]int{{0, 0}, {1, 1}}, true, 95},
		{"😀", "x😀y😀", 100, [][2]int{{1, 3}, {4, 6}}, true, 84},
		{"a", "a", 2, nil, false, 0},
		{"a", "a", 7, nil, false, -1},
		// Hand-counted branch and assertion programs retain instruction-order charges.
		{"a|b", "b", 100, [][2]int{{0, 1}}, true, 81},
		{"a|b", "b", 18, nil, false, -1},
		{"^a$", "a", 100, [][2]int{{0, 1}}, true, 86},
	}
	for _, c := range cases {
		p, err := CompilePattern(c.pattern, false)
		if err != nil {
			t.Fatal(err)
		}
		budget := PatternBudget{Steps: c.limit}
		got, ok := MatchRanges(p, c.text, &budget)
		if !reflect.DeepEqual(got, c.ranges) || ok != c.ok || budget.Steps != c.remaining {
			t.Fatalf("%q %q budget%d: ranges%v ok%v remaining%d", c.pattern, c.text, c.limit, got, ok, budget.Steps)
		}
	}
}
func TestMatchRangesWorkspaceAllocationBound(t *testing.T) {
	p, err := CompilePattern("z", false)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Repeat("a", 4096)
	allocations := testing.AllocsPerRun(1, func() {
		budget := PatternBudget{Steps: 1000000}
		ranges, ok := MatchRanges(p, text, &budget)
		if !ok || len(ranges) != 0 || budget.Steps != 991804 {
			t.Fatal("no-match work accounting changed")
		}
	})
	if allocations > 32 {
		t.Fatalf("per-character matcher workspace allocations: %.0f exceeds 32", allocations)
	}
}
func TestMatchRangesWorkspaceIsolatedAcrossCalls(t *testing.T) {
	p, err := CompilePattern("a", false)
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for pass := 0; pass < 10; pass++ {
				exhausted := PatternBudget{Steps: 2}
				if ranges, ok := MatchRanges(p, "a", &exhausted); ok || ranges != nil {
					t.Error("exhausted call retained a result")
				}
				budget := PatternBudget{Steps: 100}
				ranges, ok := MatchRanges(p, "aa", &budget)
				if !ok || !reflect.DeepEqual(ranges, [][2]int{{0, 1}, {1, 2}}) || budget.Steps != 88 {
					t.Error("one matcher call contaminated another")
				}
			}
		}()
	}
	group.Wait()
}

func TestMatchRangesDenseProgramAllocationBound(t *testing.T) {
	// Many live instructions exercise the sparse active list and its per-position reset.
	p, err := CompilePattern("a?b?c?d?e?f?g?h?i?j?k?l?m?n?o?p?z", false)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Repeat("x", 512)
	allocations := testing.AllocsPerRun(5, func() {
		budget := PatternBudget{Steps: 1000000}
		ranges, ok := MatchRanges(p, text, &budget)
		if !ok || len(ranges) != 0 {
			t.Fatal("dense program changed a no-match result")
		}
	})
	if allocations > 14 {
		t.Fatalf("dense program workspace allocations: %.0f exceeds 14", allocations)
	}
}

func TestMatchRangesWorkspaceResetsAcrossPatternsAndTexts(t *testing.T) {
	// Vary program size, both list orientations, empty/UTF-16 text and the
	// return points before any thread and after a branch exhausts its budget.
	cases := []struct {
		pattern, text string
		limit         int
		ranges        [][2]int
		ok            bool
		remaining     int
	}{
		{"^a$", "a", 100, [][2]int{{0, 1}}, true, 86},
		{"a", "aa", 100, [][2]int{{0, 1}, {1, 2}}, true, 88},
		{"a|b", "b", 18, nil, false, -1},
		{"a", "a", 2, nil, false, 0},
		{"😀", "x😀y😀", 100, [][2]int{{1, 3}, {4, 6}}, true, 84},
		{"a", "a", 7, nil, false, -1},
		{"a{0}", "a", 100, [][2]int{{0, 0}, {1, 1}}, true, 95},
		{"a|b", "b", 100, [][2]int{{0, 1}}, true, 81},
		{"a", "", 100, [][2]int{}, true, 96},
		{"a", "a", 100, [][2]int{{0, 1}}, true, 92},
	}
	var workspace matchRangesWorkspace
	for pass := 0; pass < 3; pass++ {
		for _, c := range cases {
			pattern, err := CompilePattern(c.pattern, false)
			if err != nil {
				t.Fatal(err)
			}
			budget := PatternBudget{Steps: c.limit}
			ranges, ok := workspace.match(pattern, c.text, &budget)
			if !reflect.DeepEqual(ranges, c.ranges) || ok != c.ok || budget.Steps != c.remaining {
				t.Fatalf("pass %d %q %q: ranges %v ok %v remaining %d", pass, c.pattern, c.text, ranges, ok, budget.Steps)
			}
			for _, state := range [][]int{workspace.current, workspace.next} {
				for _, start := range state[:cap(state)] {
					if start != -1 {
						t.Fatal("workspace retained a touched counter after return")
					}
				}
			}
			if len(workspace.pcs) != 0 || len(workspace.nextPCs) != 0 || len(workspace.stack) != 0 {
				t.Fatal("workspace retained an active list after return")
			}
			freshBudget := PatternBudget{Steps: c.limit}
			fresh, freshOK := MatchRanges(pattern, c.text, &freshBudget)
			if !reflect.DeepEqual(ranges, fresh) || ok != freshOK || budget != freshBudget {
				t.Fatal("reused workspace differed from an isolated invocation")
			}
		}
	}
	pattern, _ := CompilePattern("a", false)
	budget := PatternBudget{Steps: 100}
	retained, _ := workspace.match(pattern, "aa", &budget)
	budget.Steps = 100
	workspace.match(pattern, "a", &budget)
	if !reflect.DeepEqual(retained, [][2]int{{0, 1}, {1, 2}}) {
		t.Fatal("workspace reuse changed previously returned ranges")
	}
}

func TestMatchRangesReusedWorkspaceAllocationBound(t *testing.T) {
	pattern, err := CompilePattern("a?b?c?d?e?f?g?h?i?j?k?l?m?n?o?p?z", false)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Repeat("x", 512)
	var workspace matchRangesWorkspace
	allocations := testing.AllocsPerRun(5, func() {
		budget := PatternBudget{Steps: 1000000}
		ranges, ok := workspace.match(pattern, text, &budget)
		if !ok || len(ranges) != 0 {
			t.Fatal("reused workspace changed a no-match result")
		}
	})
	// Only UTF-16 conversion may allocate after the workspace is warm.
	if allocations > 2 {
		t.Fatalf("reused matcher workspace allocations: %.0f exceeds 2", allocations)
	}
}
