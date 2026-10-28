package runcontext

// The search pattern (agent#5): a regular expression in the subset RE2 accepts, matched in time
// linear in the text it reads (the port of packages/harness/context/pattern.ts).
//
// Why not the host language's engine. The browser's backtracks, and so would a hand-written
// backtracker here: (a+)+$, (x|x)*y or merely .*.*z run in time exponential or cubic in the line,
// and a model writing a pattern over a long line would stop the page. So the pattern is parsed
// here, compiled to a small program and run as a Pike VM, the construction RE2 uses: every
// position of the text is read once, by at most one thread per instruction. The work is counted,
// and a search that reaches its bound stops and says where to go on. Go's own regexp is RE2, but
// its syntax, its classes, its case folding and its indices are not the TypeScript's, so the
// subset is kept as the TypeScript states it.
//
// The subset: literals and escaped punctuation; .; classes with ranges and negation; \d \D \w \W
// \s \S; \b \B; ^ and $ (a line's start and end); groups ( ), (?: ) and (?<name> ); alternation;
// the quantifiers * + ? {n} {n,} {n,m}, greedy or lazy; the control escapes \t \n \r \f \v \0,
// \xHH, \uHHHH and \u{H…}. Refused, each with its reason: backreferences (\1…\9, \k<name>),
// lookahead and lookbehind, Unicode property classes, inline flags, a pattern over PatternLength
// characters, a repetition over PatternRepeat, and a pattern whose compiling takes more than
// PatternProgram steps or whose program holds more than PatternProgram instructions.
//
// Two readings differ from a plain regular expression, on purpose: $ also matches just before a
// line's final carriage return (a line of a CRLF text ends in one), and the work is bounded. With
// ignoreCase, letters fold through the closure of their case mappings: σ, ς and Σ are one letter,
// and so are k, K and the Kelvin sign, and s, S and ſ; the dotless ı and the dotted İ have no
// simple case folding and match only themselves.

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf16"
)

// PatternLength is the longest pattern a search takes.
const PatternLength = 256

// PatternRepeat is the largest count a repetition may name, as RE2 bounds it.
const PatternRepeat = 1000

// PatternProgram is the most instructions a compiled pattern may hold.
const PatternProgram = 2000

// PatternError is a pattern the search refuses, with the reason.
type PatternError struct{ Message string }

func (e *PatternError) Error() string { return e.Message }

type patternTest func(code rune) bool

type nodeKind int

const (
	nodeChar nodeKind = iota
	nodeSeq
	nodeAlt
	nodeRepeat
	nodeAssert
)

type node struct {
	kind       nodeKind
	test       patternTest
	literal    rune
	hasLiteral bool
	items      []*node
	inner      *node
	min, max   int // max < 0 is Infinity
	assertion  string
}

const (
	opChar = iota
	opSplit
	opJmp
	opAssert
	opMatch
)

// CompiledPattern is a compiled pattern: its program, and a literal every match contains, for a
// quick refusal of a line.
type CompiledPattern struct {
	Source     string
	IgnoreCase bool
	ops        []int
	x          []int
	y          []int
	tests      []patternTest
	asserts    []string
	// Required is a run of literal characters every match contains, to skip a line without it;
	// empty where there is none.
	Required string
	// word is what \b and \B read as a word character: with ignoreCase, folded as \w is.
	word patternTest
}

var whitespace = map[rune]bool{0x09: true, 0x0a: true, 0x0b: true, 0x0c: true, 0x0d: true, 0x20: true,
	0xa0: true, 0x1680: true, 0x2028: true, 0x2029: true, 0x202f: true, 0x205f: true, 0x3000: true, 0xfeff: true}

func isSpace(c rune) bool { return whitespace[c] || (c >= 0x2000 && c <= 0x200a) }
func isDigit(c rune) bool { return c >= 0x30 && c <= 0x39 }
func isWord(c rune) bool {
	return (c >= 0x30 && c <= 0x39) || (c >= 0x41 && c <= 0x5a) || (c >= 0x61 && c <= 0x7a) || c == 0x5f
}
func isLineEnd(c rune) bool { return c == 0x0a || c == 0x0d || c == 0x2028 || c == 0x2029 }

const patternSyntax = "^$\\.*+?()[]{}|/-"

// The case-equivalence classes, as the closure of each code point's single-code-point lower and
// upper case mappings, built once on the first search that ignores case. The dotless ı (U+0131)
// takes no part: its mapping to I is Turkic, not a simple case folding. The dotted İ (U+0130) is
// left out for the same reading: JavaScript's full lower case of it is two code points, which the
// TypeScript skips, while Go's simple mapping is one, so leaving it out keeps both letters
// matching only themselves, as the TypeScript's comment states.
var (
	foldOnce  sync.Once
	foldTable map[rune][]rune
)

func folds() map[rune][]rune {
	foldOnce.Do(func() {
		parent := map[rune]rune{}
		var find func(code rune) rune
		find = func(code rune) rune {
			root := code
			for {
				up, ok := parent[root]
				if !ok || up == root {
					break
				}
				root = up
			}
			at := code
			for at != root {
				up := parent[at]
				parent[at] = root
				at = up
			}
			return root
		}
		union := func(a, b rune) {
			if _, ok := parent[a]; !ok {
				parent[a] = a
			}
			if _, ok := parent[b]; !ok {
				parent[b] = b
			}
			left, right := find(a), find(b)
			if left != right {
				parent[left] = right
			}
		}
		for code := rune(0); code <= 0x1ffff; code++ {
			if (code >= 0xd800 && code <= 0xdfff) || code == 0x0131 || code == 0x0130 {
				continue
			}
			for _, mapped := range [2]rune{unicode.ToLower(code), unicode.ToUpper(code)} {
				if mapped == code || mapped == 0x0131 || mapped == 0x0130 {
					continue
				}
				union(code, mapped)
			}
		}
		classes := map[rune][]rune{}
		for code := range parent {
			root := find(code)
			classes[root] = append(classes[root], code)
		}
		foldTable = map[rune][]rune{}
		for _, members := range classes {
			for _, code := range members {
				foldTable[code] = members
			}
		}
	})
	return foldTable
}

// Folded is one code point's case-equivalence class: itself, and every letter its case mappings
// reach (folded).
func Folded(code rune) []rune {
	if members, ok := folds()[code]; ok {
		return members
	}
	return []rune{code}
}

type parser struct {
	source     string
	units      []uint16
	at         int
	ignoreCase bool
	err        *PatternError
}

func (p *parser) fail(reason string) {
	if p.err == nil {
		p.err = &PatternError{reason}
	}
	// A failed parse stops: every reader checks p.err.
	p.at = len(p.units)
}

func (p *parser) peek() (uint16, bool) {
	if p.at >= len(p.units) {
		return 0, false
	}
	return p.units[p.at], true
}

func (p *parser) peekIs(c byte) bool {
	held, ok := p.peek()
	return ok && held == uint16(c)
}

func (p *parser) rest() string { return string(utf16.Decode(p.units[p.at:])) }

func (p *parser) caseless(test patternTest) patternTest {
	if !p.ignoreCase {
		return test
	}
	return func(c rune) bool {
		for _, other := range Folded(c) {
			if test(other) {
				return true
			}
		}
		return false
	}
}

func (p *parser) literal(code rune) *node {
	if !p.ignoreCase {
		return &node{kind: nodeChar, literal: code, hasLiteral: true, test: func(c rune) bool { return c == code }}
	}
	set := map[rune]bool{}
	for _, other := range Folded(code) {
		set[other] = true
	}
	return &node{kind: nodeChar, literal: code, hasLiteral: true, test: func(c rune) bool { return set[c] }}
}

func (p *parser) hex(digits int) rune {
	if p.at+digits > len(p.units) {
		p.fail("an escape that names a character by its code needs that many hexadecimal digits")
		return 0
	}
	text := string(utf16.Decode(p.units[p.at : p.at+digits]))
	for _, c := range text {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			p.fail("an escape that names a character by its code needs that many hexadecimal digits")
			return 0
		}
	}
	p.at += digits
	value, _ := strconv.ParseInt(text, 16, 32)
	return rune(value)
}

// escape reads one escape after its backslash: a code point, a class test, or an assertion.
func (p *parser) escape(inClass bool) (rune, patternTest, string) {
	held, ok := p.peek()
	if !ok {
		p.fail("a pattern cannot end in a backslash")
		return 0, nil, ""
	}
	c := rune(held)
	p.at++
	switch {
	case c >= '1' && c <= '9', c == 'k':
		p.fail("backreferences are not supported")
		return 0, nil, ""
	case c == 'p' || c == 'P':
		p.fail("Unicode property classes are not supported")
		return 0, nil, ""
	case c == 'd':
		return 0, isDigit, ""
	case c == 'D':
		return 0, func(x rune) bool { return !isDigit(x) }, ""
	case c == 'w':
		word := p.caseless(isWord)
		return 0, word, ""
	case c == 'W':
		word := p.caseless(isWord)
		return 0, func(x rune) bool { return !word(x) }, ""
	case c == 's':
		return 0, isSpace, ""
	case c == 'S':
		return 0, func(x rune) bool { return !isSpace(x) }, ""
	case c == 'b':
		if inClass {
			return 0x08, nil, ""
		}
		return 0, nil, "word"
	case c == 'B':
		if inClass {
			p.fail("\\B has no meaning in a class")
			return 0, nil, ""
		}
		return 0, nil, "notword"
	case c == 't':
		return 0x09, nil, ""
	case c == 'n':
		return 0x0a, nil, ""
	case c == 'r':
		return 0x0d, nil, ""
	case c == 'f':
		return 0x0c, nil, ""
	case c == 'v':
		return 0x0b, nil, ""
	case c == '0':
		if held, ok := p.peek(); ok && isDigit(rune(held)) {
			p.fail("backreferences are not supported")
		}
		return 0, nil, ""
	case c == 'x':
		return p.hex(2), nil, ""
	case c == 'u':
		if !p.peekIs('{') {
			return p.hex(4), nil, ""
		}
		end := -1
		for at := p.at; at < len(p.units); at++ {
			if p.units[at] == '}' {
				end = at
				break
			}
		}
		if end < 0 || end-p.at < 2 || end-p.at > 7 {
			p.fail("\\u{\u2026} names a code point in one to six hexadecimal digits")
			return 0, nil, ""
		}
		p.at++
		code := p.hex(end - p.at)
		p.at++
		if code > 0x10ffff {
			p.fail("\\u{\u2026} names a code point up to 10FFFF")
		}
		return code, nil, ""
	}
	if strings.ContainsRune(patternSyntax, c) {
		return c, nil, ""
	}
	p.fail("\\" + string(c) + " is not an escape this search knows")
	return 0, nil, ""
}

func (p *parser) codeAt() rune {
	code, width := codePointAt(p.units, p.at)
	p.at += width
	return code
}

func (p *parser) characterClass() *node {
	// After the [.
	negated := false
	if p.peekIs('^') {
		negated = true
		p.at++
	}
	tests := []patternTest{}
	first := true
	for {
		if p.err != nil {
			return &node{kind: nodeChar, test: func(rune) bool { return false }}
		}
		held, ok := p.peek()
		if !ok {
			p.fail("a character class is not closed")
			continue
		}
		if held == ']' && !first {
			p.at++
			break
		}
		if held == ']' && first {
			p.fail("an empty character class matches nothing")
			continue
		}
		first = false
		var low rune
		var lowTest patternTest
		if held == '\\' {
			p.at++
			code, test, assertion := p.escape(true)
			if assertion != "" {
				p.fail("an assertion has no meaning in a class")
				continue
			}
			low, lowTest = code, test
		} else {
			low = p.codeAt()
		}
		next, hasNext := uint16(0), false
		if p.at+1 < len(p.units) {
			next, hasNext = p.units[p.at+1], true
		}
		if lowTest == nil && p.peekIs('-') && hasNext && next != ']' {
			p.at++
			var high rune
			if p.peekIs('\\') {
				p.at++
				code, test, assertion := p.escape(true)
				if test != nil || assertion != "" {
					p.fail("a class range runs between two characters")
					continue
				}
				high = code
			} else {
				high = p.codeAt()
			}
			if high < low {
				p.fail("a class range runs from a lower character to a higher one")
				continue
			}
			from, to := low, high
			tests = append(tests, func(x rune) bool { return x >= from && x <= to })
			continue
		}
		if lowTest == nil {
			code := low
			tests = append(tests, func(x rune) bool { return x == code })
		} else {
			tests = append(tests, lowTest)
		}
	}
	held := tests
	inside := p.caseless(func(x rune) bool {
		for _, test := range held {
			if test(x) {
				return true
			}
		}
		return false
	})
	if negated {
		return &node{kind: nodeChar, test: func(x rune) bool { return !inside(x) }}
	}
	return &node{kind: nodeChar, test: inside}
}

var bracePattern = regexp.MustCompile(`^\{(\d{1,5})(,(\d{0,5}))?\}`)

// quantifier reads a quantifier, and reports whether there was one.
func (p *parser) quantifier() (min, max int, ok bool) {
	held, has := p.peek()
	if !has {
		return 0, 0, false
	}
	switch held {
	case '*':
		p.at++
		return 0, -1, true
	case '+':
		p.at++
		return 1, -1, true
	case '?':
		p.at++
		return 0, 1, true
	case '{':
	default:
		return 0, 0, false
	}
	found := bracePattern.FindStringSubmatch(p.rest())
	if found == nil {
		p.fail("a brace is a repetition {n}, {n,} or {n,m}; write \\{ for the character")
		return 0, 0, false
	}
	p.at += U16Length(found[0])
	min, _ = strconv.Atoi(found[1])
	max = min
	if found[2] != "" {
		if found[3] == "" {
			max = -1
		} else {
			max, _ = strconv.Atoi(found[3])
		}
	}
	if min > PatternRepeat || (max >= 0 && max > PatternRepeat) {
		p.fail("a repetition counts at most " + strconv.Itoa(PatternRepeat))
		return 0, 0, false
	}
	if max >= 0 && max < min {
		p.fail("a repetition {n,m} needs n at most m")
		return 0, 0, false
	}
	return min, max, true
}

func (p *parser) atom() *node {
	held, ok := p.peek()
	if !ok || held == '|' || held == ')' {
		return nil
	}
	switch held {
	case '(':
		p.at++
		rest := p.rest()
		if strings.HasPrefix(rest, "?=") || strings.HasPrefix(rest, "?!") || strings.HasPrefix(rest, "?<=") || strings.HasPrefix(rest, "?<!") {
			p.fail("lookahead and lookbehind are not supported")
			return nil
		}
		if strings.HasPrefix(rest, "?:") {
			p.at += 2
		} else if strings.HasPrefix(rest, "?<") {
			name := regexp.MustCompile(`^\?<([A-Za-z_][A-Za-z0-9_]*)>`).FindStringSubmatch(rest)
			if name == nil {
				p.fail("a named group is written (?<name>\u2026)")
				return nil
			}
			p.at += U16Length(name[0])
		} else if p.peekIs('?') {
			p.fail("inline flags and other (? groups are not supported")
			return nil
		}
		inner := p.alternation()
		if !p.peekIs(')') {
			p.fail("a group is not closed")
			return nil
		}
		p.at++
		// A group is never itself an assertion, so (?:^)* repeats a group.
		if inner != nil && inner.kind == nodeAssert {
			return &node{kind: nodeSeq, items: []*node{inner}}
		}
		return inner
	case '[':
		p.at++
		return p.characterClass()
	case '.':
		p.at++
		return &node{kind: nodeChar, test: func(x rune) bool { return !isLineEnd(x) }}
	case '^':
		p.at++
		return &node{kind: nodeAssert, assertion: "bol"}
	case '$':
		p.at++
		return &node{kind: nodeAssert, assertion: "eol"}
	case '\\':
		p.at++
		code, test, assertion := p.escape(false)
		if p.err != nil {
			return nil
		}
		if assertion != "" {
			return &node{kind: nodeAssert, assertion: assertion}
		}
		if test != nil {
			return &node{kind: nodeChar, test: p.caseless(test)}
		}
		return p.literal(code)
	case '*', '+', '?':
		p.fail("a repetition needs something before it to repeat")
		return nil
	case '{', '}', ']':
		p.fail("write \\" + string(rune(held)) + " for the character " + string(rune(held)))
		return nil
	}
	return p.literal(p.codeAt())
}

func (p *parser) sequence() *node {
	items := []*node{}
	for p.err == nil {
		held := p.atom()
		if held == nil {
			break
		}
		min, max, ok := p.quantifier()
		if !ok {
			items = append(items, held)
			continue
		}
		if held.kind == nodeAssert {
			p.fail("an assertion cannot be repeated")
			break
		}
		if p.peekIs('?') {
			p.at++
		}
		if p.peekIs('*') || p.peekIs('+') || p.peekIs('?') || p.peekIs('{') {
			p.fail("a repetition cannot itself be repeated without a group")
			break
		}
		items = append(items, &node{kind: nodeRepeat, inner: held, min: min, max: max})
	}
	if len(items) == 1 {
		return items[0]
	}
	return &node{kind: nodeSeq, items: items}
}

func (p *parser) alternation() *node {
	options := []*node{p.sequence()}
	for p.err == nil && p.peekIs('|') {
		p.at++
		options = append(options, p.sequence())
	}
	if len(options) == 1 {
		return options[0]
	}
	return &node{kind: nodeAlt, items: options}
}

// requiredLiteral is the longest run of literal characters the top-level sequence requires.
func requiredLiteral(tree *node) string {
	items := []*node{tree}
	if tree.kind == nodeSeq {
		items = tree.items
	}
	best, run := "", ""
	for _, item := range items {
		if item.kind == nodeChar && item.hasLiteral {
			run += string(item.literal)
			if U16Length(run) > U16Length(best) {
				best = run
			}
		} else if item.kind == nodeAssert {
			continue
		} else {
			run = ""
		}
	}
	return best
}

// emitsNothing reports a node that compiles to no instruction at all: an empty group, or a
// repetition or sequence of nothing.
func emitsNothing(n *node) bool {
	if n.kind == nodeSeq {
		for _, item := range n.items {
			if !emitsNothing(item) {
				return false
			}
		}
		return true
	}
	if n.kind == nodeRepeat {
		return emitsNothing(n.inner)
	}
	return false
}

// iterations is how many times a repetition builds its body: its minimum, then each optional
// copy, or one loop.
func iterations(n *node) int {
	if n.max < 0 {
		return n.min + 1
	}
	return n.max
}

// compileSteps is the compile steps a node takes when built, every node and every repetition's
// every iteration counted, saturating just past the bound so nesting cannot overflow it.
func compileSteps(n *node) int {
	limit := PatternProgram + 1
	switch n.kind {
	case nodeChar, nodeAssert:
		return 1
	case nodeSeq, nodeAlt:
		sum := 1
		for _, item := range n.items {
			sum += compileSteps(item)
			if sum > limit {
				return limit
			}
		}
		return sum
	default:
		sum := 1 + iterations(n)*compileSteps(n.inner)
		if sum > limit || sum < 0 {
			return limit
		}
		return sum
	}
}

// CompilePattern is compilePattern: a pattern, checked and compiled, or a PatternError saying why
// it is refused.
func CompilePattern(source string, ignoreCase bool) (*CompiledPattern, error) {
	if source == "" {
		return nil, &PatternError{"a pattern is a non-empty string"}
	}
	if U16Length(source) > PatternLength {
		return nil, &PatternError{"a pattern is at most " + strconv.Itoa(PatternLength) + " characters"}
	}
	p := &parser{source: source, units: u16(source), ignoreCase: ignoreCase}
	tree := p.alternation()
	if p.err == nil && p.at < len(p.units) {
		if p.peekIs(')') {
			p.fail("a parenthesis closes a group that was never opened")
		} else {
			p.fail("the pattern could not be read to its end")
		}
	}
	if p.err != nil {
		return nil, p.err
	}
	program := &CompiledPattern{Source: source, IgnoreCase: ignoreCase}
	if ignoreCase {
		program.word = func(code rune) bool {
			for _, other := range Folded(code) {
				if isWord(other) {
					return true
				}
			}
			return false
		}
	} else {
		program.word = isWord
	}
	// Every build and every repetition's every iteration is charged, a repetition of nothing too:
	// a nested repetition of an empty group emits no instruction, so the instruction bound alone
	// never stopped it compiling.
	steps := 0
	var refused *PatternError
	charge := func(count int) bool {
		steps += count
		if steps > PatternProgram {
			refused = &PatternError{"the pattern takes more than " + strconv.Itoa(PatternProgram) + " steps to compile; use smaller repetitions"}
			return false
		}
		return true
	}
	emit := func(op, x, y int, test patternTest, kind string) int {
		if len(program.ops) >= PatternProgram {
			refused = &PatternError{"the pattern compiles to more than " + strconv.Itoa(PatternProgram) + " instructions; use smaller repetitions"}
			return -1
		}
		program.ops = append(program.ops, op)
		program.x = append(program.x, x)
		program.y = append(program.y, y)
		program.tests = append(program.tests, test)
		program.asserts = append(program.asserts, kind)
		return len(program.ops) - 1
	}
	var build func(n *node)
	build = func(n *node) {
		if refused != nil || !charge(1) {
			return
		}
		switch n.kind {
		case nodeChar:
			emit(opChar, 0, 0, n.test, "")
		case nodeAssert:
			emit(opAssert, 0, 0, nil, n.assertion)
		case nodeSeq:
			for _, item := range n.items {
				build(item)
				if refused != nil {
					return
				}
			}
		case nodeAlt:
			jumps := []int{}
			for index, option := range n.items {
				if index == len(n.items)-1 {
					build(option)
					break
				}
				split := emit(opSplit, 0, 0, nil, "")
				if split < 0 {
					return
				}
				program.x[split] = len(program.ops)
				build(option)
				if refused != nil {
					return
				}
				jump := emit(opJmp, 0, 0, nil, "")
				if jump < 0 {
					return
				}
				jumps = append(jumps, jump)
				program.y[split] = len(program.ops)
			}
			if refused != nil {
				return
			}
			for _, jump := range jumps {
				program.x[jump] = len(program.ops)
			}
		case nodeRepeat:
			// A repetition of nothing collapses to nothing: it matches only the empty string, as
			// nothing does. Its iterations are still charged.
			if emitsNothing(n.inner) {
				charge(iterations(n) * compileSteps(n.inner))
				return
			}
			for index := 0; index < n.min; index++ {
				build(n.inner)
				if refused != nil {
					return
				}
			}
			if n.max < 0 {
				split := emit(opSplit, 0, 0, nil, "")
				if split < 0 {
					return
				}
				program.x[split] = len(program.ops)
				build(n.inner)
				if refused != nil {
					return
				}
				if emit(opJmp, split, 0, nil, "") < 0 {
					return
				}
				program.y[split] = len(program.ops)
				return
			}
			exits := []int{}
			for index := n.min; index < n.max; index++ {
				split := emit(opSplit, 0, 0, nil, "")
				if split < 0 {
					return
				}
				program.x[split] = len(program.ops)
				exits = append(exits, split)
				build(n.inner)
				if refused != nil {
					return
				}
			}
			for _, split := range exits {
				program.y[split] = len(program.ops)
			}
		}
	}
	build(tree)
	if refused == nil {
		emit(opMatch, 0, 0, nil, "")
	}
	if refused != nil {
		return nil, refused
	}
	// Only a case-sensitive pattern skips a line by its literal: a folded letter (the Kelvin sign
	// for k, ſ for s) has no lower case a line search would find.
	if !ignoreCase {
		program.Required = requiredLiteral(tree)
	}
	return program, nil
}

// PatternBudget is the work a search may do, counted in thread steps, shared by every line it
// reads.
type PatternBudget struct{ Steps int }

// Matcher is one search's matcher: the pattern, and the thread lists it reuses for every line.
// It answers where the pattern first matches in a line, as a string index in UTF-16 code units,
// as JavaScript's String.prototype.search does; -1 where it does not; and false where the budget
// ran out before the line was read through.
type Matcher func(line string, budget *PatternBudget) (int, bool)

type threads struct {
	pcs    []int
	starts []int
	length int
}

// NewMatcher is matcher: a matcher for one search.
//
// A Pike VM: the threads alive at one position are a list in priority order, at most one per
// instruction, each with the position it started at. Threads carried from earlier positions come
// before the one started here, so the list is ordered by start, and the first thread to reach the
// match has the earliest start of those still alive. Nothing is ever read twice.
//
// The lists are allocated once per search and reset by a generation count that carries across
// lines, and each line is charged the program's size before it is read: so millions of short
// lines stop at the work bound as one long one does, rather than costing their setup uncounted.
func NewMatcher(pattern *CompiledPattern) Matcher {
	ops, x, y, tests, asserts, word := pattern.ops, pattern.x, pattern.y, pattern.tests, pattern.asserts, pattern.word
	size := len(ops)
	current := &threads{pcs: make([]int, size), starts: make([]int, size)}
	next := &threads{pcs: make([]int, size), starts: make([]int, size)}
	seen := make([]int, size)
	for i := range seen {
		seen[i] = -1
	}
	stack := make([]int, 0, size)
	generation := 0
	return func(line string, budget *PatternBudget) (int, bool) {
		budget.Steps -= size
		if budget.Steps <= 0 {
			return 0, false
		}
		if pattern.Required != "" && !strings.Contains(line, pattern.Required) {
			return -1, true
		}
		units := u16(line)
		end := len(units)
		// Word characters are all in the Basic Multilingual Plane, so a code unit tells.
		wordBefore := func(pos int) bool { return pos > 0 && word(rune(units[pos-1])) }
		wordAt := func(pos int) bool { return pos < end && word(rune(units[pos])) }
		holds := func(kind string, pos int) bool {
			if kind == "bol" {
				return pos == 0
			}
			// Also just before a final carriage return: a line of a CRLF text ends in one.
			if kind == "eol" {
				return pos == end || (pos == end-1 && units[pos] == 0x0d)
			}
			boundary := wordBefore(pos) != wordAt(pos)
			if kind == "word" {
				return boundary
			}
			return !boundary
		}
		// add follows the empty moves from one instruction at one position, adding the threads
		// they reach.
		add := func(list *threads, at0, start, pos int) {
			stack = append(stack, at0)
			for len(stack) > 0 {
				at := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				if seen[at] == generation {
					continue
				}
				seen[at] = generation
				budget.Steps--
				switch ops[at] {
				case opJmp:
					stack = append(stack, x[at])
				case opSplit:
					// The preferred branch is followed first, so it is pushed last.
					stack = append(stack, y[at], x[at])
				case opAssert:
					if holds(asserts[at], pos) {
						stack = append(stack, at+1)
					}
				default:
					list.pcs[list.length] = at
					list.starts[list.length] = start
					list.length++
				}
			}
		}
		current.length = 0
		matched := -1
		generation++
		for pos := 0; ; {
			if budget.Steps <= 0 {
				return 0, false
			}
			// The thread that starts here, after every thread carried here: the lowest priority.
			if matched < 0 {
				add(current, 0, pos, pos)
			}
			// Nothing alive: done where a match is in hand or the line is read; otherwise on to
			// the next position.
			if current.length == 0 && (matched >= 0 || pos >= end) {
				return matched, true
			}
			code, width := rune(-1), 1
			if pos < end {
				code, width = codePointAt(units, pos)
			}
			generation++
			next.length = 0
			for index := 0; index < current.length; index++ {
				pc := current.pcs[index]
				start := current.starts[index]
				budget.Steps--
				if ops[pc] == opMatch {
					// Every thread after this one started here or later: none can match earlier.
					if matched < 0 || start < matched {
						matched = start
					}
					break
				}
				if matched >= 0 && start >= matched {
					continue
				}
				if code >= 0 && tests[pc](code) {
					add(next, pc+1, start, pos+width)
				}
			}
			if pos >= end {
				return matched, true
			}
			current, next = next, current
			pos += width
		}
	}
}

// FirstMatch is firstMatch: where the pattern first matches in one line, with a matcher of its
// own.
func FirstMatch(pattern *CompiledPattern, line string, budget *PatternBudget) (int, bool) {
	return NewMatcher(pattern)(line, budget)
}

// MatchRanges unions overlapping matches in one bounded Pike pass.
// Offsets are UTF-16 units, as in the shared TypeScript matcher.
func MatchRanges(pattern *CompiledPattern, text string, budget *PatternBudget) ([][2]int, bool) {
	units := u16(text)
	ranges := [][2]int{}
	current, next := map[int]int{}, map[int]int{}
	pcs := make([]int, 0, len(pattern.ops))
	stack := make([]int, 0, len(pattern.ops))
	holds := func(kind string, pos int) bool {
		if kind == "bol" {
			return pos == 0
		}
		if kind == "eol" {
			return pos == len(units) || pos == len(units)-1 && units[pos] == 13
		}
		before := pos > 0 && pattern.word(rune(units[pos-1]))
		after := pos < len(units) && pattern.word(rune(units[pos]))
		return (before != after) == (kind == "word")
	}
	add := func(set map[int]int, pc, start, pos int) {
		stack = append(stack[:0], pc)
		for len(stack) > 0 {
			at := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if prior, ok := set[at]; ok && prior <= start {
				continue
			}
			set[at] = start
			budget.Steps--
			switch pattern.ops[at] {
			case opJmp:
				stack = append(stack, pattern.x[at])
			case opSplit:
				stack = append(stack, pattern.y[at], pattern.x[at])
			case opAssert:
				if holds(pattern.asserts[at], pos) {
					stack = append(stack, at+1)
				}
			}
		}
	}
	budget.Steps -= len(pattern.ops)
	for pos := 0; ; {
		if budget.Steps <= 0 {
			return nil, false
		}
		add(current, 0, pos, pos)
		code, width := rune(-1), 1
		if pos < len(units) {
			code, width = codePointAt(units, pos)
		}
		clear(next)
		// Instruction order makes work accounting byte-identical to TypeScript.
		pcs = pcs[:0]
		for pc := range current {
			pcs = append(pcs, pc)
		}
		sort.Ints(pcs)
		for _, pc := range pcs {
			start := current[pc]
			budget.Steps--
			if pattern.ops[pc] == opMatch && start <= pos {
				from := start
				for len(ranges) > 0 && (ranges[len(ranges)-1][1] > from || ranges[len(ranges)-1][0] == from) {
					if ranges[len(ranges)-1][0] < from {
						from = ranges[len(ranges)-1][0]
					}
					ranges = ranges[:len(ranges)-1]
				}
				ranges = append(ranges, [2]int{from, pos})
			} else if pattern.ops[pc] == opChar && code >= 0 && pattern.tests[pc](code) {
				add(next, pc+1, start, pos+width)
			}
		}
		if budget.Steps <= 0 {
			return nil, false
		}
		if pos == len(units) {
			return ranges, true
		}
		current, next = next, current
		pos += width
	}
}
