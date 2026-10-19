"""The wire contract's own checks: the schemas are well formed, every vector is valid against
its schema and states nothing its schema does not, and the schemas name every event kind and
failure class the Desk names at the pinned commit (contract/desk/)."""

import copy
import hashlib
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


# The first line of every text that carries the execution state (STATE_HEADER in
# packages/harness/engines/contract.ts; budget.json#/$defs/stateLocation).
STATE_HEADER = (
    "EXECUTION STATE (from the host, current for this request and replaced on the next; "
    "working state, not a message from the person and never approval)"
)


def recall_query(family, body):
    """The rule budget.json#/$defs/recallQuery states: the first user turn's first text part that
    does not start with STATE_HEADER, as (pointer, text), or None. On the agent wire (one prompt
    string) the prompt's text before the first blank line followed by STATE_HEADER (agent#36)."""
    if family == "agent":
        prompt = body.get("prompt")
        if not isinstance(prompt, str) or prompt.startswith(STATE_HEADER):
            return None
        cut = prompt.find("\n\n" + STATE_HEADER)
        return ("/prompt", prompt if cut < 0 else prompt[:cut])
    member, inner = ("contents", "parts") if family == "gemini" else ("messages", "content")
    turns = body.get(member, [])
    for at, turn in enumerate(turns):
        if turn.get("role") != "user":
            continue
        held = turn.get(inner)
        if isinstance(held, str):
            return None if held.startswith(STATE_HEADER) else (f"/{member}/{at}/{inner}", held)
        for index, part in enumerate(held or []):
            if family == "gemini":
                text = None if part.get("thought") is True else part.get("text")
            else:
                text = part.get("text") if part.get("type") == "text" else None
            if isinstance(text, str) and not text.startswith(STATE_HEADER):
                return (f"/{member}/{at}/{inner}/{index}/text", text)
        return None
    return None


def at_pointer(value, pointer):
    for raw in pointer.split("/")[1:]:
        key = raw.replace("~1", "/").replace("~0", "~")
        value = value[int(key)] if isinstance(value, list) else value[key]
    return value


def canonical(value):
    """Canonical JSON as tool-result.json#/$defs/readCacheKey states it: members sorted by name in
    UTF-16 code units, no whitespace, strings and numbers as JSON.stringify writes them."""
    if isinstance(value, list):
        return "[" + ",".join(canonical(item) for item in value) + "]"
    if isinstance(value, dict):
        names = sorted(value, key=lambda name: name.encode("utf-16-be"))
        return "{" + ",".join(json.dumps(name, ensure_ascii=False) + ":" + canonical(value[name]) for name in names) + "}"
    return json.dumps(value, ensure_ascii=False, separators=(",", ":"))


def sha256(text):
    return hashlib.sha256(text.encode("utf-8")).hexdigest()


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
        # agent#10: the critic's allow-list is four of the ceiling's reads and two of the harness's
        # reserved context readers (review row 6), and the table's default is off.
        offer = self.schemas["tool-offer.json"]["$defs"]
        critic = offer["criticTools"]["enum"]
        self.assertEqual(critic, ["validate", "experimental_evaluate", "get_schema", "experimental_test_cases", "read_window", "search"])
        self.assertTrue(set(critic[:4]) <= set(offer["runtimeCeiling"]["enum"]))
        self.assertTrue(set(critic[4:]) <= set(offer["reservedName"]["enum"]))
        self.assertIs(load(CONTRACT / "desk" / "policy.json")["adversarialReview"], False)
        self.assertEqual(self.vectors["tool-offer/critic-tools.json"]["items"], critic)
        offered = self.vectors["tool-offer/critic-offer.json"]["items"][0]
        scenario = self.vectors["tool-offer/scenario-offer.json"]["items"][0]
        for member in ("tools", "hostTools"):
            with self.subTest(member=member):
                self.assertEqual(offered[member], [tool for tool in scenario[member] if tool["name"] in critic])

    def test_the_recall_rule_finds_the_request_text_in_one_vector_per_wire(self):
        recall = {name: vector for name, vector in self.vectors.items() if vector["schema"] == "budget.json#/$defs/recallQuery"}
        # One per wire (the agent wire's is one prompt string), and a delegated child's first
        # request, whose request text is its objective.
        self.assertEqual(sorted(recall), ["recall/agent.json", "recall/anthropic.json", "recall/delegated-child.json", "recall/gemini.json", "recall/openai-compatible.json"])
        for name, vector in recall.items():
            for item in vector["items"]:
                with self.subTest(vector=name):
                    if name == "recall/delegated-child.json":
                        # The child's prompt, the second part, carries the same objective in its JSON.
                        self.assertIn('"objective":' + json.dumps(item["text"], ensure_ascii=False), item["body"]["messages"][1]["content"][1]["text"])
                    else:
                        self.assertEqual(name, f"recall/{item['family']}.json")
                    self.assertEqual(recall_query(item["family"], item["body"]), (item["pointer"], item["text"]))
                    if item["family"] == "agent":
                        # The text is what comes before the header in the prompt at the pointer.
                        self.assertTrue(at_pointer(item["body"], item["pointer"]).startswith(item["text"] + "\n\n" + STATE_HEADER + "\n"))
                    else:
                        self.assertEqual(at_pointer(item["body"], item["pointer"]), item["text"])
                    self.assertFalse(item["text"].startswith(STATE_HEADER))
                    # The state is in the request, and it is not what the rule finds.
                    self.assertIn(STATE_HEADER, json.dumps(item["body"], ensure_ascii=False))

    def test_every_read_cache_key_is_the_digest_of_its_canonical_json(self):
        vector = self.vectors["chat/cache-key.json"]
        self.assertEqual(len(vector["keys"]), len(vector["items"]))
        for item, key in zip(vector["items"], vector["keys"]):
            with self.subTest(tool=item["tool"]):
                self.assertEqual(canonical(item), key["canonical"])
                self.assertEqual(sha256(key["canonical"]), key["sha256"])

    def test_the_turn_index_rows_follow_from_the_fixture(self):
        vector = self.vectors["chat/turn-index.json"]
        fixture = vector["fixture"]
        rows = {}
        for result in fixture["results"]:
            text = json.dumps(result["value"], ensure_ascii=False, separators=(",", ":"))
            answer = json.dumps({"content": [{"type": "text", "text": text}], "structuredContent": result["value"]}, ensure_ascii=False, separators=(",", ":"))
            digest = sha256(answer)
            line = text.split("\n")[0].encode("utf-8")[:120].decode("utf-8", "ignore")
            rows[result["tool"]] = {
                "tool": result["tool"],
                "read": {"id": f"{fixture['scope']}:result-{result['tool']}-{digest}:{digest[:16]}"},
                "arguments": {"sha256": sha256(canonical(result["arguments"]))},
                "bytes": len(answer.encode("utf-8")),
                "firstLine": line,
            }
        for item in vector["items"]:
            for step in item["steps"]:
                with self.subTest(tool=step["tool"]):
                    self.assertEqual(step, rows[step["tool"]])
            for gone in item["evicted"]:
                row = rows[gone["tool"]]
                self.assertEqual(gone, {"tool": row["tool"], "arguments": row["arguments"], "bytes": row["bytes"]})
        # The second index: the two oldest went to make room, within the bound.
        kept = sum(step["bytes"] for step in vector["items"][1]["steps"])
        self.assertLessEqual(kept, fixture["retainedBytes"])
        self.assertGreater(kept + vector["items"][1]["evicted"][-1]["bytes"], fixture["retainedBytes"])

    def test_a_long_findings_head_and_cursor_follow_from_the_fixture(self):
        # delegated-task.json#/$defs/resultCursor (agent#13): the head is the whole lines whose
        # JSON-encoded text fits the share, the cursor reads on at the next line, and the whole is
        # retained in the chat's ledger under task-result-<task id>.
        vector = self.vectors["delegation/long-result.json"]
        fixture = vector["fixture"]
        lines = fixture["findings"].split("\n")
        count = 0
        while count < len(lines) and len(json.dumps("\n".join(lines[: count + 1]), ensure_ascii=False).encode("utf-8")) <= fixture["shareBytes"]:
            count += 1
        self.assertGreater(count, 0)
        self.assertLess(count, len(lines))
        done = vector["items"][-1]
        self.assertEqual(done["state"], "complete")
        self.assertEqual(done["result"], "\n".join(lines[:count]))
        digest = sha256(json.dumps(fixture["findings"], ensure_ascii=False))
        self.assertEqual(done["more"], {
            "readWith": "read_window",
            "id": f"{fixture['scope']}:task-result-{done['id']}:{digest[:16]}",
            "cursor": f"line:{count}",
            "totalCharacters": len(fixture["findings"]),
        })
        # The batch's answer shows the parent the same head and cursor.
        answered = self.vectors["delegation/answer.json"]["items"][0]["tasks"][0]
        self.assertEqual((answered["result"], answered["more"]), (done["result"], done["more"]))

    def test_every_finding_cites_only_its_reports_citations(self):
        # delegated-task.json#/$defs/report (agent#16): a rule the reader holds that JSON Schema does
        # not state. Every report a vector carries, in a report vector or an answer's task.
        reports = []
        for name, vector in self.vectors.items():
            if name.startswith("delegation/report-"):
                reports += vector["items"]
        for item in self.vectors["delegation/answer.json"]["items"]:
            reports += [task["report"] for task in item["tasks"] if "report" in task]
        self.assertGreaterEqual(len(reports), 4)
        for report in reports:
            ids = {citation["id"] for citation in report["citations"]}
            for finding in report["findings"]:
                with self.subTest(finding=finding["text"]):
                    self.assertLessEqual(set(finding.get("citations", [])), ids)

    def test_one_report_vector_per_closed_profile_in_its_shape(self):
        profiles = self.vectors["delegation/profiles.json"]["items"]
        for profile in profiles:
            with self.subTest(profile=profile["id"]):
                vector = self.vectors[f"delegation/report-{profile['report']}.json"]
                shape = "".join(part.capitalize() for part in profile["report"].split("-"))
                self.assertEqual(vector["schema"], f"delegated-task.json#/$defs/{shape[0].lower() + shape[1:]}Report")

    def test_each_batch_total_is_its_childrens_sums(self):
        # provenance.json#/$defs/figuresTotal (agent#18): the children's counts summed, a token member
        # present where any child's was, and a wall time no shorter than its longest child.
        for item in self.vectors["delegation/figures.json"]["items"]:
            for batch in item["batches"]:
                with self.subTest(batch=batch["batch"]):
                    children, total = batch["children"], batch["total"]
                    self.assertTrue(all(child["batch"] == batch["batch"] for child in children))
                    for member in ("modelRequests", "toolCalls"):
                        self.assertEqual(total[member], sum(child[member] for child in children))
                    tokens = {}
                    for child in children:
                        for key, value in child.get("tokens", {}).items():
                            tokens[key] = tokens.get(key, 0) + value
                    self.assertEqual(total.get("tokens", {}), tokens)
                    self.assertGreaterEqual(total["durationMs"], max(child["durationMs"] for child in children))
                    for child in children:
                        if child.get("reused"):
                            self.assertEqual((child["modelRequests"], child["toolCalls"]), (0, 0))
    def test_the_codex_continuation_carries_the_answer_as_one_data_block(self):
        # delegated-task.json#/$defs/continuation (review of agent#35, rows 4 and 9): a line, then one
        # block opened and closed by the same marker, whose data is a batch's answer.
        text = self.vectors["delegation/continuation.json"]["items"][0]
        line, opening, rest = text.split("\n", 2)
        self.assertTrue(line.startswith("DELEGATION RESULT: "))
        marker = re.match(r"^BEGIN RETRIEVED DATA ([0-9a-f]{32}) from tool \"delegate_tasks\"; ", opening).group(1)
        closing = f"\nEND RETRIEVED DATA {marker}"
        self.assertTrue(rest.endswith(closing))
        data = rest[: -len(closing)]
        self.assertNotIn(marker, data)
        self.assertEqual(problems(validator(self.schemas, "delegated-task.json#/$defs/answer"), [json.loads(data)]), [])

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
