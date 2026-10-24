"""Focused schema/vector controls for fixed host request caps (#186)."""
import copy
import json
import unittest
from test_contract import ROOT, schemas, validator


class RequestCapSchemaTests(unittest.TestCase):
    def test_request_cap_vectors_and_refusals(self):
        documents = schemas()
        for name in ("request-cap.json", "request-cap-refusal.json"):
            vector = json.loads((ROOT / "contract/vectors/checkpoint" / name).read_text())
            check = validator(documents, vector["schema"])
            for item in vector["items"]:
                with self.subTest(name=name, item=item):
                    self.assertEqual(list(check.iter_errors(item)), [])

    def test_request_cap_schema_member_cannot_be_removed_or_relaxed(self):
        documents = schemas()
        vector = json.loads((ROOT / "contract/vectors/checkpoint/request-cap.json").read_text())
        check = validator(documents, "checkpoint.json")
        self.assertEqual(list(check.iter_errors(vector["items"][1])), [])
        for cap in (None, 0, -1, 21, 1.5, "1", True, [], {}):
            item = {**vector["items"][1], "requestCap": cap}
            with self.subTest(cap=cap):
                self.assertTrue(list(check.iter_errors(item)))
        removed = copy.deepcopy(documents)
        removed["checkpoint.json"]["properties"].pop("requestCap", None)
        # A closed checkpoint detects member removal through a valid capped record.
        self.assertTrue(list(validator(removed, "checkpoint.json").iter_errors(vector["items"][1])))
        # Keep the member declared to isolate relaxation of its value constraints.
        relaxed = copy.deepcopy(documents)
        relaxed["checkpoint.json"]["properties"]["requestCap"] = {}
        self.assertEqual(list(validator(relaxed, "checkpoint.json").iter_errors({**vector["items"][1], "requestCap": 0})), [])
