package certification

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode"
)

type parityRow struct {
	Block  string   `json:"block"`
	Check  string   `json:"check"`
	Go     []string `json:"go"`
	Reason string   `json:"reason"`
}

var engineBlock = regexp.MustCompile(`^describe\.each\(MODEL_ENGINES\)\('([^']*)'`)

// typescriptChecks reads the check names of every describe.each(MODEL_ENGINES) block of the
// TypeScript suite: each it(…) and it.each(…)(…) by its name, under its top-level block's title.
func typescriptChecks(source string) map[string][]string {
	checks := map[string][]string{}
	block := ""
	var body strings.Builder
	flush := func() {
		if block != "" {
			checks[block] = append(checks[block], itNames(body.String())...)
		}
		body.Reset()
	}
	for _, line := range strings.Split(source, "\n") {
		if strings.HasPrefix(line, "describe") {
			flush()
			block = ""
			if m := engineBlock.FindStringSubmatch(line); m != nil {
				block = m[1]
			}
			continue
		}
		body.WriteString(line)
		body.WriteByte('\n')
	}
	flush()
	return checks
}

var itCall = regexp.MustCompile(`(^|[^\w.])it(\.each)?\(`)

// itNames finds each it(name, …) and it.each(table)(name, …) in a block's text.
func itNames(text string) []string {
	var names []string
	for at := 0; at < len(text); {
		m := itCall.FindStringSubmatchIndex(text[at:])
		if m == nil {
			break
		}
		next := at + m[1]
		if m[4] >= 0 {
			next = skipParens(text, next)
			if next >= len(text) || text[next] != '(' {
				at = next
				continue
			}
			next++
		}
		for next < len(text) && unicode.IsSpace(rune(text[next])) {
			next++
		}
		name, end := readString(text, next)
		if end < 0 {
			at = next
			continue
		}
		names = append(names, name)
		at = end
	}
	return names
}

// skipParens returns the index after the parenthesis that closes the one open before from.
func skipParens(text string, from int) int {
	depth := 1
	for i := from; i < len(text); i++ {
		switch text[i] {
		case '\'', '"', '`':
			_, end := readString(text, i)
			if end < 0 {
				return len(text)
			}
			i = end - 1
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return len(text)
}

// readString reads the quoted literal at text[at]; end is the index after it, or -1.
func readString(text string, at int) (string, int) {
	if at >= len(text) || !strings.ContainsRune(`'"`+"`", rune(text[at])) {
		return "", -1
	}
	quote := text[at]
	var b strings.Builder
	for i := at + 1; i < len(text); i++ {
		switch text[i] {
		case '\\':
			if i+1 < len(text) {
				b.WriteByte(text[i+1])
			}
			i++
		case quote:
			return b.String(), i + 1
		default:
			b.WriteByte(text[i])
		}
	}
	return "", -1
}

// The parity table holds the Go runner's checks in step with the TypeScript suite's (agent#53):
// every check of the TypeScript engine blocks is in the table, every row of the table is a check
// there, every Go counterpart named is a check the Go leg makes, and every check of the engine
// leg has one.
func TestTheParityTableHoldsTheGoChecksInStepWithTheTypeScriptLeg(t *testing.T) {
	root, err := RepositoryRoot()
	if err != nil {
		t.Fatal(err)
	}
	var table struct {
		Source    string      `json:"source"`
		Checks    []parityRow `json:"checks"`
		Scenarios struct {
			Source            string      `json:"source"`
			AdditionalSources []string    `json:"additionalSources"`
			Checks            []parityRow `json:"checks"`
		} `json:"scenarios"`
		Delegation struct {
			Source string      `json:"source"`
			Checks []parityRow `json:"checks"`
		} `json:"delegation"`
		AgentCapability struct {
			Checks []struct {
				Go         string `json:"go"`
				Acceptance string `json:"acceptance"`
			} `json:"checks"`
		} `json:"agentCapability"`
	}
	raw, err := os.ReadFile("parity.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &table); err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(filepath.Join(root, table.Source))
	if err != nil {
		t.Fatal(err)
	}
	typescript := typescriptChecks(string(source))
	if len(typescript["engine %s"]) == 0 {
		t.Fatalf("no checks read from the engine leg of %s", table.Source)
	}
	rows := map[string]parityRow{}
	for _, row := range table.Checks {
		key := row.Block + " › " + row.Check
		if _, repeated := rows[key]; repeated {
			t.Errorf("%s is in the table twice", key)
		}
		rows[key] = row
		if !slices.Contains(typescript[row.Block], row.Check) {
			t.Errorf("the table names %s, which the TypeScript suite does not have", key)
		}
		if (len(row.Go) == 0) == (row.Reason == "") {
			t.Errorf("%s: a row names its Go checks or a reason, one of the two", key)
		}
		if row.Block == "engine %s" && len(row.Go) == 0 {
			t.Errorf("%s: a check of the engine leg the Go runner runs has no Go counterpart", key)
		}
		for _, check := range row.Go {
			if !slices.Contains(AllCheckNames(), check) {
				t.Errorf("%s: %q is not a check the Go leg makes", key, check)
			}
		}
	}
	mapped, reasoned := 0, 0
	for block, names := range typescript {
		for _, name := range names {
			row, ok := rows[block+" › "+name]
			switch {
			case !ok:
				t.Errorf("the TypeScript suite has %q in %q, and the parity table does not", name, block)
			case len(row.Go) > 0:
				mapped++
			default:
				reasoned++
			}
		}
	}
	// The certified scenarios (scenarios.test.ts): every check the file makes, by its name, is a row,
	// each with its Go checks or a reason.
	scenarioSource, err := os.ReadFile(filepath.Join(root, table.Scenarios.Source))
	if err != nil {
		t.Fatal(err)
	}
	scenarioChecks := itNames(string(scenarioSource))
	for _, name := range table.Scenarios.AdditionalSources {
		raw, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		scenarioChecks = append(scenarioChecks, itNames(string(raw))...)
	}
	if len(scenarioChecks) < 20 {
		t.Fatalf("%d checks read from %s", len(scenarioChecks), table.Scenarios.Source)
	}
	scenarioRows := map[string]parityRow{}
	for _, row := range table.Scenarios.Checks {
		if _, repeated := scenarioRows[row.Check]; repeated {
			t.Errorf("%s is in the scenario table twice", row.Check)
		}
		scenarioRows[row.Check] = row
		if !slices.Contains(scenarioChecks, row.Check) {
			t.Errorf("the scenario table names %q, which the TypeScript suite does not have", row.Check)
		}
		if (len(row.Go) == 0) == (row.Reason == "") {
			t.Errorf("%s: a row names its Go checks or a reason, one of the two", row.Check)
		}
		for _, check := range row.Go {
			if !slices.Contains(AllCheckNames(), check) {
				t.Errorf("%s: %q is not a check the Go runner makes", row.Check, check)
			}
		}
	}
	for _, name := range scenarioChecks {
		if _, ok := scenarioRows[name]; !ok {
			t.Errorf("the TypeScript scenarios have %q, and the parity table does not", name)
		}
	}
	// The delegation checks every adapter runs (delegation.test.ts): every check, by its name, is a
	// row, each with its Go checks or a reason.
	delegationSource, err := os.ReadFile(filepath.Join(root, table.Delegation.Source))
	if err != nil {
		t.Fatal(err)
	}
	delegationChecks := itNames(string(delegationSource))
	if len(delegationChecks) < 2 {
		t.Fatalf("%d checks read from %s", len(delegationChecks), table.Delegation.Source)
	}
	delegationRows := map[string]parityRow{}
	for _, row := range table.Delegation.Checks {
		if _, repeated := delegationRows[row.Check]; repeated {
			t.Errorf("%s is in the delegation table twice", row.Check)
		}
		delegationRows[row.Check] = row
		if !slices.Contains(delegationChecks, row.Check) {
			t.Errorf("the delegation table names %q, which the TypeScript suite does not have", row.Check)
		}
		if (len(row.Go) == 0) == (row.Reason == "") {
			t.Errorf("%s: a row names its Go checks or a reason, one of the two", row.Check)
		}
		for _, check := range row.Go {
			if !slices.Contains(AllCheckNames(), check) {
				t.Errorf("%s: %q is not a check the Go runner makes", row.Check, check)
			}
		}
	}
	for _, name := range delegationChecks {
		if _, ok := delegationRows[name]; !ok {
			t.Errorf("the TypeScript delegation checks have %q, and the parity table does not", name)
		}
	}
	for _, check := range AllCheckNames() {
		used := false
		for _, row := range slices.Concat(table.Checks, table.Scenarios.Checks, table.Delegation.Checks) {
			used = used || slices.Contains(row.Go, check)
		}
		if !used {
			t.Errorf("the Go check %q is no TypeScript check's counterpart", check)
		}
	}
	// The agent capability's own checks (ADR-0007): every one the runner makes is listed, with the
	// acceptance item it holds, and none the runner does not make; none is a TypeScript check's
	// counterpart, and the Eino model leg certifies none of them.
	var own []string
	for _, row := range table.AgentCapability.Checks {
		if row.Acceptance == "" {
			t.Errorf("%s: no acceptance item", row.Go)
		}
		own = append(own, row.Go)
	}
	if !slices.Equal(own, AgentOwnNames) {
		t.Errorf("the agentCapability table lists %v, and the runner makes %v", own, AgentOwnNames)
	}
	for _, name := range own {
		if slices.Contains(AllCheckNames(), name) {
			t.Errorf("%s is the agent capability's own check and also a TypeScript check's counterpart", name)
		}
	}
	all := AllAgentCheckNames()
	for _, name := range slices.Concat(AgentScenarioNames, AgentSourcesNames, AgentApprovalNames, AgentSkillsNames, AgentDelegationNames, AgentOwnNames) {
		if !slices.Contains(all, name) {
			t.Errorf("%s is named and no agent check reports it", name)
		}
	}
	t.Logf("%d TypeScript checks: %d with their Go counterparts, %d with a reason", mapped+reasoned, mapped, reasoned)
}

// The reader finds what a new check would look like, so the table cannot fall behind silently.
func TestTheParityReaderFindsANewCheck(t *testing.T) {
	source := "describe.each(MODEL_ENGINES)('engine %s', (engineId) => {\n  it('a', async () => {})\n" +
		"  it.each([['x', (1)]])('b %s', async () => {})\n  it(\"c’s\", () => {})\n})\ndescribe('other', () => {\n  it('d', () => {})\n})\n"
	if got := typescriptChecks(source); !slices.Equal(got["engine %s"], []string{"a", "b %s", "c’s"}) || len(got) != 1 {
		t.Fatalf("read %v", got)
	}
}
