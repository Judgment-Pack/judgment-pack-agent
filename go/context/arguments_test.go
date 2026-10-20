package runcontext

// A catalogued tool's arguments held to the schema load_tool loaded (ADR-0006): the subset of JSON
// Schema the boundary refuses on, in the words it refuses with. sourceArguments.test.ts holds the
// same cases to the same words.

import (
	"strings"
	"testing"
)

const issueSchema = `{"type":"object","properties":{
	"repository":{"type":"string","minLength":1,"maxLength":20},
	"title":{"type":"string"},
	"labels":{"type":"array","items":{"type":"string"},"minItems":0,"maxItems":2},
	"priority":{"type":"integer","minimum":1,"maximum":5},
	"state":{"enum":["open","closed"]},
	"weight":{"type":"number"}},
	"required":["repository","title"],"additionalProperties":false}`

func TestArgumentsThatMatchAreAdmittedAndSoAreOnesTheSchemaLeavesOpen(t *testing.T) {
	issue := MustParseJSON(issueSchema)
	for _, args := range []string{
		`{"repository":"expense","title":"Missing mileage rate"}`,
		`{"repository":"expense","title":"t","labels":["a","b"],"priority":3,"state":"open","weight":1}`,
	} {
		if problem := ArgumentProblem(issue, MustParseJSON(args)); problem != "" {
			t.Errorf("%s: %s", args, problem)
		}
	}
	for _, open := range []*Value{MustParseJSON(`{"type":"object"}`), Null(), Bool(true)} {
		if problem := ArgumentProblem(open, MustParseJSON(`{"anything":[1,{"deeper":null}]}`)); problem != "" {
			t.Errorf("%s: %s", open.Text(), problem)
		}
	}
}

func TestEachWayTheArgumentsDoNotMatchIsRefusedNamingTheMemberAndTheRule(t *testing.T) {
	issue := MustParseJSON(issueSchema)
	for _, c := range []struct{ args, problem string }{
		{`[]`, "the arguments must be object, not array"},
		{`{"title":"t"}`, "/repository is required and missing"},
		{`{"repository":"expense"}`, "/title is required and missing"},
		{`{"repository":"expense","title":7}`, "/title must be string, not integer"},
		{`{"repository":"","title":"t"}`, "/repository is shorter than the schema's 1 characters"},
		{`{"repository":"` + strings.Repeat("x", 21) + `","title":"t"}`, "/repository is longer than the schema's 20 characters"},
		{`{"repository":"expense","title":"t","labels":["a",2]}`, "/labels/1 must be string, not integer"},
		{`{"repository":"expense","title":"t","labels":["a","b","c"]}`, "/labels has more than the schema's 2 items"},
		{`{"repository":"expense","title":"t","priority":0}`, "/priority is below the schema's minimum of 1"},
		{`{"repository":"expense","title":"t","priority":6}`, "/priority is above the schema's maximum of 5"},
		{`{"repository":"expense","title":"t","priority":1.5}`, "/priority must be integer, not number"},
		{`{"repository":"expense","title":"t","state":"merged"}`, "/state is not one of the values the schema lists"},
		{`{"repository":"expense","title":"t","extra":true}`, "/extra is not a member the schema names"},
		{`{"repository":"expense","title":"t","weight":"heavy"}`, "/weight must be number, not string"},
	} {
		if got := ArgumentProblem(issue, MustParseJSON(c.args)); got != c.problem {
			t.Errorf("%s: %q, not %q", c.args, got, c.problem)
		}
	}
}

func TestANumberIsANumberAndAnIntegerBothATypeListIsAnyOfItsTypes(t *testing.T) {
	for _, c := range []struct{ schema, value, problem string }{
		{`{"type":"number"}`, `3`, ""},
		{`{"type":["string","null"]}`, `null`, ""},
		{`{"type":["string","null"]}`, `4`, "the arguments must be string or null, not integer"},
		{`{"type":"object","properties":{"a":{"const":"x"}}}`, `{"a":"y"}`, "/a is not the value the schema requires"},
	} {
		if got := ArgumentProblem(MustParseJSON(c.schema), MustParseJSON(c.value)); got != c.problem {
			t.Errorf("%s %s: %q, not %q", c.schema, c.value, got, c.problem)
		}
	}
}

func TestAllOfAnyOfAndOneOfAreReadAndAMemberNamedWithASlashIsAnEscapedPointer(t *testing.T) {
	either := MustParseJSON(`{"anyOf":[{"type":"object","required":["a"]},{"type":"object","required":["b"]}]}`)
	if problem := ArgumentProblem(either, MustParseJSON(`{"b":1}`)); problem != "" {
		t.Error(problem)
	}
	for _, c := range []struct{ schema, value, problem string }{
		{`{"anyOf":[{"type":"object","required":["a"]},{"type":"object","required":["b"]}]}`, `{}`, "the arguments matches none of the schema's alternatives (/a is required and missing)"},
		{`{"oneOf":[{"type":"string"},{"type":"integer"}]}`, `true`, "the arguments matches none of the schema's alternatives (the arguments must be string, not boolean)"},
		{`{"allOf":[{"type":"object"},{"required":["a"]}]}`, `{}`, "/a is required and missing"},
		{`{"type":"object","properties":{"a/b":{"type":"string"}}}`, `{"a/b":1}`, "/a~1b must be string, not integer"},
	} {
		if got := ArgumentProblem(MustParseJSON(c.schema), MustParseJSON(c.value)); got != c.problem {
			t.Errorf("%s %s: %q, not %q", c.schema, c.value, got, c.problem)
		}
	}
}

func TestWhatIsNotReadIsLeftToTheHostAndASchemaPastSixteenLevelsAsksNothing(t *testing.T) {
	left := MustParseJSON(`{"type":"object","properties":{"a":{"type":"string","pattern":"^x$","format":"uri"},"b":{"$ref":"#/$defs/b"}},"$defs":{"b":{"type":"integer"}}}`)
	if problem := ArgumentProblem(left, MustParseJSON(`{"a":"nope","b":"not an integer"}`)); problem != "" {
		t.Error(problem)
	}
	deep, value := MustParseJSON(`{"type":"integer"}`), MustParseJSON(`"text"`)
	for range 20 {
		deep = ObjectOf("type", String("object"), "properties", ObjectOf("next", deep))
		value = ObjectOf("next", value)
	}
	if problem := ArgumentProblem(deep, value); problem != "" {
		t.Error(problem)
	}
}
