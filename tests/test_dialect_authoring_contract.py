"""Handwritten public tool-input vectors; every property/enum has a positive witness."""
import copy
import itertools
import json
from pathlib import Path
import unittest
import jsonschema
ROOT = Path(__file__).resolve().parents[1]
class DialectAuthoringContract(unittest.TestCase):
    def test_input_vectors_and_safeguard_witnesses(self):
        schema = json.loads((ROOT / 'contract/schemas/dialect-authoring.json').read_text())
        vectors = json.loads((ROOT / 'contract/vectors/dialect-authoring/inputs.json').read_text())['items']
        validator = jsonschema.Draft202012Validator(schema)
        for vector in vectors:
            validator.validate(vector)
        for definition, shape in schema['$defs'].items():
            for member in shape.get('properties', {}):
                mutant = copy.deepcopy(schema)
                del mutant['$defs'][definition]['properties'][member]
                broken = jsonschema.Draft202012Validator(mutant)
                self.assertTrue(any(not broken.is_valid(v) for v in vectors), (definition, member))
        for kind in schema['$defs']['conversion']['properties']['kind']['enum']:
            mutant = copy.deepcopy(schema)
            mutant['$defs']['conversion']['properties']['kind']['enum'].remove(kind)
            broken = jsonschema.Draft202012Validator(mutant)
            self.assertTrue(any(not broken.is_valid(v) for v in vectors), kind)
        for malformed in ({}, {'id': '', 'packKey': 'p'}, {**vectors[1], 'extra': True}):
            self.assertFalse(validator.is_valid(malformed))

    def test_retention_vectors_and_every_added_member_enum(self):
        schema = json.loads((ROOT / 'contract/schemas/dialect-draft.json').read_text())
        vectors = json.loads((ROOT / 'contract/vectors/dialect-authoring/retention.json').read_text())['items']
        validator = jsonschema.Draft202012Validator(schema)
        for vector in vectors:
            validator.validate(vector)
        for definition, shape in schema['$defs'].items():
            for member in shape.get('properties', {}):
                mutant = copy.deepcopy(schema)
                del mutant['$defs'][definition]['properties'][member]
                self.assertTrue(any(not jsonschema.Draft202012Validator(mutant).is_valid(v) for v in vectors), (definition, member))
            for member, prop in shape.get('properties', {}).items():
                for value in prop.get('enum', []):
                    mutant = copy.deepcopy(schema)
                    mutant['$defs'][definition]['properties'][member]['enum'].remove(value)
                    self.assertTrue(any(not jsonschema.Draft202012Validator(mutant).is_valid(v) for v in vectors), (definition, member, value))
        self.assertFalse(validator.is_valid({**vectors[0], 'check': {'results': []}}))
        self.assertFalse(validator.is_valid({**vectors[1], 'previousReceipt': None}))

    def test_reviewed_draft_tuple_is_exact(self):
        schema = json.loads((ROOT / 'contract/schemas/dialect-draft.json').read_text())
        rows = json.loads((ROOT / 'contract/vectors/dialect-authoring/retention.json').read_text())['items']
        current = json.loads((ROOT / 'contract/testdata/dialect-authoring/authoring.json').read_text())['conversionContract']
        superseded = json.loads((ROOT / 'contract/testdata/dialect-authoring/superseded-prototype-identity.json').read_text())
        validator = jsonschema.Draft202012Validator(schema)
        present = [row for row in rows if row.get('candidate', {}).get('conversionContract')]
        self.assertEqual(len(present), 8)
        for row in present:
            self.assertEqual(row['candidate']['conversionContract'], current)
            validator.validate(row)
        for choices in itertools.product([False, True], repeat=3):
            row = copy.deepcopy(present[0])
            for stale, member in zip(choices, ['revision', 'schemaSha256', 'corpusSha256']):
                if stale:
                    row['candidate']['conversionContract']['spec'][member] = superseded['spec'][member]
            self.assertEqual(validator.is_valid(row), not any(choices), choices)
