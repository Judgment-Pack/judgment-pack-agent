"""The wire contract's own checks: the schemas are well formed, every vector is valid against
its schema and states nothing its schema does not, and the schemas name every event kind and
failure class the Desk names at the pinned commit (contract/desk/)."""

import copy
import json
import re
import unittest
from pathlib import Path

from jsonschema import Draft202012Validator
from referencing import Registry, Resource
from referencing.jsonschema import DRAFT202012

ROOT = Path(__file__).resolve().parent.parent
CONTRACT = ROOT / "contract"
SCHEMAS = CONTRACT / "schemas"
VECTORS = CONTRACT / "vectors"
BASE = "https://github.com/Judgment-Pack/judgment-pack-agent/blob/main/contract/schemas/"

# Runtime tools the harness admits beyond the Desk's ceiling (contract/desk/inventory.json), in order.
HARNESS_CEILING_ADDITIONS = ["experimental_test_cases"]

EXPECTED_SCHEMAS = {
    "run-event.json",
    "tool-offer.json",
    "tool-call.json",
    "tool-result.json",
    "approval.json",
    "question.json",
    "delegated-task.json",
    "checkpoint.json",
    "failure.json",
    "budget.json",
    "provenance.json",
    "capability.json",
}

# The reasoning kinds each wire speaks (WIRE_REASONING in packages/harness/capabilities.ts): a
# capability record whose kind its family's wire does not speak is refused before any request.
WIRE_REASONING = {
    "openai-compatible": {"openai-effort", "none"},
    "anthropic": {"anthropic-thinking", "none"},
    "gemini": {"gemini-thinking", "none"},
}


def load(path):
    with open(path, encoding="utf-8") as handle:
        return json.load(handle)


def schemas():
    return {path.name: load(path) for path in sorted(SCHEMAS.glob("*.json"))}


def vectors():
    return {path.relative_to(VECTORS).as_posix(): load(path) for path in sorted(VECTORS.rglob("*.json"))}


def registry(documents):
    return Registry().with_resources(
        (document["$id"], Resource.from_contents(document, default_specification=DRAFT202012))
        for document in documents.values()
    )


def strict(schema):
    """The same schema with every object definition closed.

    A schema stays open where the Desk's reader is open, so plain validation cannot see a vector
    carrying a member its schema never names. Closing every definition that lists its members
    (type object with properties, outside an allOf) makes that visible: whatever a vector carries,
    some schema has to state.
    """

    def walk(node, in_all_of=False):
        if isinstance(node, list):
            for item in node:
                walk(item, in_all_of)
            return
        if not isinstance(node, dict):
            return
        if (
            not in_all_of
            and node.get("type") == "object"
            and isinstance(node.get("properties"), dict)
            and node.get("additionalProperties", True) is True
        ):
            node.pop("additionalProperties", None)
            node["unevaluatedProperties"] = False
        for key, value in node.items():
            if key in ("const", "enum", "default", "examples"):
                continue
            walk(value, key == "allOf")

    closed = copy.deepcopy(schema)
    walk(closed)
    return closed


def validator(documents, reference):
    return Draft202012Validator({"$ref": BASE + reference}, registry=registry(documents))


def problems(check, items):
    found = []
    for index, item in enumerate(items):
        for error in check.iter_errors(item):
            found.append(f"item {index}: {error.message[:200]}")
    return found


class SchemaTests(unittest.TestCase):
    def test_the_twelve_schemas_are_there_and_nothing_else(self):
        self.assertEqual(set(schemas()), EXPECTED_SCHEMAS)

    def test_every_schema_is_draft_2020_12_and_well_formed(self):
        for name, schema in schemas().items():
            with self.subTest(schema=name):
                self.assertEqual(schema["$schema"], "https://json-schema.org/draft/2020-12/schema")
                self.assertEqual(schema["$id"], BASE + name)
                Draft202012Validator.check_schema(schema)

    def test_every_reference_resolves(self):
        documents = schemas()
        known = registry(documents)
        for name, schema in documents.items():
            resolver = known.resolver(base_uri=BASE + name)
            for reference in re.findall(r'"\$ref": "([^"]+)"', json.dumps(schema)):
                with self.subTest(schema=name, ref=reference):
                    self.assertIsNotNone(resolver.lookup(reference).contents)

    def test_the_readme_table_lists_every_schema_with_its_closedness(self):
        table = {}
        for line in (CONTRACT / "README.md").read_text(encoding="utf-8").splitlines():
            row = re.match(r"^\| `([a-z-]+\.json)` \|[^|]*\| (closed|open) \|([^|]*)\|", line)
            if row:
                table[row.group(1)] = (row.group(2), set(re.findall(r"`\$defs/([A-Za-z]+)`", row.group(3))))
        self.assertEqual(set(table), EXPECTED_SCHEMAS)
        for name, schema in schemas().items():
            with self.subTest(schema=name):
                root = "closed" if schema.get("additionalProperties") is False else "open"
                closed = {key for key, value in schema.get("$defs", {}).items() if value.get("additionalProperties") is False}
                self.assertEqual(table[name], (root, closed))


class DeskInventoryTests(unittest.TestCase):
    """The schemas against the Desk's own lists, copied in contract/desk/."""

    def setUp(self):
        self.inventory = load(CONTRACT / "desk" / "inventory.json")
        self.catalogue = load(CONTRACT / "desk" / "catalog.json")
        self.schemas = schemas()

    def test_every_event_type_the_desk_emits_is_a_run_event(self):
        schema = self.schemas["run-event.json"]
        declared = schema["properties"]["type"]["enum"]
        desk = self.inventory["eventTypes"]["values"]
        self.assertEqual(sorted(set(desk) - set(declared)), [], "kinds the Desk emits that run-event.json lacks")
        self.assertEqual(sorted(set(declared) - set(desk)), [], "kinds run-event.json names that the Desk does not")

    def test_every_run_event_kind_has_exactly_one_definition(self):
        documents = self.schemas
        resolver = registry(documents).resolver(base_uri=BASE + "run-event.json")
        consts = []
        for branch in documents["run-event.json"]["oneOf"]:
            consts.append(resolver.lookup(branch["$ref"]).contents["properties"]["type"]["const"])
        self.assertEqual(sorted(consts), sorted(documents["run-event.json"]["properties"]["type"]["enum"]))

    def test_every_failure_class_in_the_catalogue_is_in_failure_json(self):
        declared = self.schemas["failure.json"]["$defs"]["code"]["enum"]
        catalogue = list(self.catalogue["failures"])
        self.assertEqual(sorted(set(catalogue) - set(declared)), [], "classes the catalogue has that failure.json lacks")
        self.assertEqual(sorted(set(declared) - set(catalogue)), [], "classes failure.json names that the catalogue does not")

    def test_failure_sources_outcomes_and_completion_reasons_match_the_desk(self):
        failure = self.schemas["failure.json"]["$defs"]
        self.assertEqual(failure["source"]["enum"], self.inventory["failureSources"]["values"])
        self.assertEqual(failure["outcome"]["enum"], self.inventory["failureOutcomes"]["values"])
        completion = self.schemas["provenance.json"]["$defs"]["completion"]["properties"]["finishReason"]["enum"]
        self.assertEqual(completion, self.inventory["completionReasons"]["values"])
        self.assertEqual(self.schemas["provenance.json"]["$defs"]["engine"]["enum"], self.inventory["engines"]["values"])
        # The ceiling is the Desk's five, then the one tool the harness admits beyond them
        # (agent#7: experimental_test_cases, under an argument allow-list and a per-turn cap).
        self.assertEqual(
            self.schemas["tool-offer.json"]["$defs"]["runtimeCeiling"]["enum"],
            self.inventory["runtimeToolCeiling"]["values"] + HARNESS_CEILING_ADDITIONS,
        )

    def test_approval_actions_are_the_servers_four(self):
        self.assertEqual(
            self.schemas["approval.json"]["$defs"]["action"]["enum"],
            self.inventory["approvalActions"]["values"],
        )

    def test_the_budget_vector_is_the_desks_table(self):
        policy = load(CONTRACT / "desk" / "policy.json")
        self.assertEqual(load(VECTORS / "budget" / "desk-defaults.json")["items"], [policy])
        self.assertEqual(problems(validator(self.schemas, "budget.json"), [policy]), [])

    def test_every_catalogue_entry_is_well_formed(self):
        check = validator(self.schemas, "failure.json#/$defs/catalogueEntry")
        for code, entry in self.catalogue["failures"].items():
            with self.subTest(code=code):
                self.assertEqual(problems(check, [entry]), [])
        for alias, code in self.catalogue["aliases"].items():
            with self.subTest(alias=alias):
                self.assertIn(code, self.catalogue["failures"])


class VectorTests(unittest.TestCase):
    def setUp(self):
        self.schemas = schemas()
        self.vectors = vectors()
        self.catalogue = load(CONTRACT / "desk" / "catalog.json")

    def test_every_vector_names_its_schema_and_carries_items(self):
        self.assertTrue(self.vectors)
        for name, vector in self.vectors.items():
            with self.subTest(vector=name):
                self.assertIn(vector["schema"].split("#")[0], EXPECTED_SCHEMAS)
                self.assertIsInstance(vector["items"], list)
                self.assertTrue(vector["description"].strip())
                source = vector["source"]
                if "desk" in source:
                    self.assertEqual(source["desk"]["repository"], "Judgment-Pack/judgment-pack-desk")
                else:
                    # Behaviour the harness changed or added since the extraction: rendered from
                    # the package, and its files named there.
                    self.assertEqual(source["harness"], {"repository": "Judgment-Pack/judgment-pack-agent", "package": "packages/harness"})
                    self.assertTrue(source["files"])
                    for file in source["files"]:
                        # "packages/harness/x.ts (symbol)": the path, without its annotation.
                        path = file.split(" (", 1)[0]
                        self.assertTrue(path.startswith("packages/harness/"), file)
                        self.assertTrue((ROOT / path).is_file(), f"{name} names {path}, which does not exist")

    def test_every_vector_validates_against_its_schema(self):
        for name, vector in self.vectors.items():
            with self.subTest(vector=name):
                check = validator(self.schemas, vector["schema"])
                self.assertEqual(problems(check, vector["items"]), [])

    def test_every_member_a_vector_carries_is_stated_by_its_schema(self):
        closed = {name: strict(schema) for name, schema in self.schemas.items()}
        for name, vector in self.vectors.items():
            with self.subTest(vector=name):
                check = validator(closed, vector["schema"])
                self.assertEqual(problems(check, vector["items"]), [])

    def test_a_run_ends_once_and_last(self):
        for name, vector in self.vectors.items():
            if vector["schema"] != "run-event.json":
                continue
            kinds = [item["type"] for item in vector["items"]]
            with self.subTest(vector=name):
                if "end" in kinds or "error" in kinds:
                    self.assertEqual(kinds.count("end"), 1)
                    self.assertEqual(kinds[-1], "end")

    def test_one_vector_per_failure_class_with_the_catalogue_message(self):
        seen = {}
        for name, vector in self.vectors.items():
            if name.startswith("failures/"):
                code = vector["failureClass"]
                self.assertEqual(name, f"failures/{code}.json")
                seen[code] = vector
        self.assertEqual(sorted(seen), sorted(self.catalogue["failures"]))
        failed_tool = validator(self.schemas, "failure.json#/$defs/toolFailureContent")
        for code, vector in seen.items():
            with self.subTest(failure=code):
                for item in vector["items"]:
                    if item["type"] == "error":
                        failure = item["failure"]
                        self.assertEqual(failure["code"], code)
                        self.assertEqual(item["message"], failure.get("detail", self.catalogue["failures"][code]["message"]))
                    if item["type"] == "tool_result":
                        self.assertEqual(problems(failed_tool, [item["structured"]]), [])
                        failure = item["structured"]["failure"]
                        self.assertTrue(item["isError"])
                        self.assertEqual(failure["code"], code)
                        self.assertEqual(item["text"], failure.get("detail", self.catalogue["failures"][code]["message"]))
                if not vector["items"]:
                    self.assertEqual(code, "cancelled", "only a cancelled run emits nothing")

    def test_every_capability_record_names_a_family_whose_wire_speaks_its_reasoning(self):
        records = {name: vector for name, vector in self.vectors.items() if vector["schema"] == "capability.json"}
        self.assertTrue(records)
        for name, vector in records.items():
            with self.subTest(vector=name):
                self.assertIn(vector["family"], WIRE_REASONING)
                for record in vector["items"]:
                    self.assertIn(record["reasoning"]["kind"], WIRE_REASONING[vector["family"]])
                    if "contextWindowTokens" in record:
                        self.assertLessEqual(record["outputTokens"], record["contextWindowTokens"] / 2)

    def test_one_default_record_per_family_and_an_open_weight_endpoint(self):
        for family in WIRE_REASONING:
            with self.subTest(family=family):
                vector = self.vectors[f"capability/{family}.json"]
                self.assertEqual(vector["family"], family)
                self.assertEqual(len(vector["items"]), 1)
                self.assertNotIn("contextWindowTokens", vector["items"][0], "a family default leaves the budget table's window")
        local = self.vectors["capability/open-weight-local.json"]
        self.assertEqual(local["family"], "openai-compatible")
        self.assertFalse(local["items"][0]["nativeToolCalls"])

    def test_a_host_may_answer_any_members_of_the_record_and_no_other(self):
        check = validator(self.schemas, "capability.json#/$defs/endpointCapabilities")
        record = self.vectors["capability/open-weight-local.json"]["items"][0]
        self.assertEqual(problems(check, [{}, {"strictAlternation": True}, record, {"vision": True}]), [])
        self.assertNotEqual(problems(check, [{"contextWindowTokens": 8192}]), [], "a window without its output cap")
        self.assertNotEqual(problems(check, [{"supportsTools": True}]), [], "a member the record does not have")
        self.assertEqual(
            set(self.schemas["capability.json"]["$defs"]["endpointCapabilities"]["properties"]),
            set(self.schemas["capability.json"]["properties"]),
        )

    def test_the_critic_reads_within_the_ceiling_and_runs_only_where_asked(self):
        # agent#10: the critic's allow-list is four of the ceiling's reads, and the table's default is off.
        critic = self.schemas["tool-offer.json"]["$defs"]["criticTools"]["enum"]
        ceiling = self.schemas["tool-offer.json"]["$defs"]["runtimeCeiling"]["enum"]
        self.assertEqual(critic, ["validate", "experimental_evaluate", "get_schema", "experimental_test_cases"])
        self.assertTrue(set(critic) <= set(ceiling))
        self.assertIs(load(CONTRACT / "desk" / "policy.json")["adversarialReview"], False)
        self.assertEqual(self.vectors["tool-offer/critic-tools.json"]["items"], critic)
        offered = [tool["name"] for tool in self.vectors["tool-offer/critic-offer.json"]["items"]]
        scenario = [tool["name"] for tool in self.vectors["tool-offer/scenario-offer.json"]["items"][0]["tools"]]
        self.assertEqual(offered, [name for name in scenario if name in critic])

    def test_the_scenario_is_the_order_the_conformance_session_asserts(self):
        vector = self.vectors["scenario/expense-reimbursement.json"]
        self.assertEqual(
            [item["type"] for item in vector["items"]],
            ["tool_call", "tool_result"] * 5
            + ["tool_call", "guardrail", "tool_result"] * 2
            + ["message", "proposal", "end"],
        )
        self.assertEqual(len(vector["steps"]), len(vector["items"]))


if __name__ == "__main__":
    unittest.main()
