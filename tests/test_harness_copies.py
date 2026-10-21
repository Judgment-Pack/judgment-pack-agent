"""The harness package's own copies of the Desk's Go-side JSON, held to the contract.

packages/harness/contract/catalog.json is the failure catalogue and policy.json the context budget
table, each copied byte for byte from the Desk's internal/aifailure/catalog.json and
internal/aicontext/policy.json at the pinned commit. The package reads them where the Desk's
browser code read the Go files. These checks hold each copy to the contract's own copy
(contract/desk/), to the contract's schema and to the budget vector, so they cannot drift apart.
"""

import unittest

from test_contract import CONTRACT, ROOT, VECTORS, load, problems, schemas, validator

HARNESS = ROOT / "packages" / "harness" / "contract"


class HarnessCopyTests(unittest.TestCase):
    def setUp(self):
        self.schemas = schemas()

    def test_the_catalogue_copy_is_the_contracts_byte_for_byte(self):
        self.assertEqual((HARNESS / "catalog.json").read_bytes(), (CONTRACT / "desk" / "catalog.json").read_bytes())

    def test_every_catalogue_class_is_a_failure_json_class_and_every_entry_is_well_formed(self):
        catalogue = load(HARNESS / "catalog.json")
        self.assertEqual(sorted(catalogue["failures"]), sorted(self.schemas["failure.json"]["$defs"]["code"]["enum"]))
        check = validator(self.schemas, "failure.json#/$defs/catalogueEntry")
        for code, entry in catalogue["failures"].items():
            with self.subTest(code=code):
                self.assertEqual(problems(check, [entry]), [])

    def test_the_policy_copy_is_the_contracts_byte_for_byte(self):
        self.assertEqual((HARNESS / "policy.json").read_bytes(), (CONTRACT / "desk" / "policy.json").read_bytes())

    def test_the_policy_copy_is_a_budget_table_and_the_contracts_defaults(self):
        policy = load(HARNESS / "policy.json")
        self.assertEqual(problems(validator(self.schemas, "policy.json"), [policy]), [])
        legacy = {**policy["budget"], "criticDefault": policy["critic"]["default"], "criticReasoningFloor": policy["critic"]["reasoningFloor"]}
        self.assertEqual(problems(validator(self.schemas, "budget.json"), [legacy]), [])
        self.assertEqual(legacy, load(VECTORS / "budget" / "desk-defaults.json")["items"][0])


if __name__ == "__main__":
    unittest.main()
