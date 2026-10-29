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

    def test_published_and_historical_inspection_tuples_are_exact(self):
        schema = json.loads((ROOT / 'contract/schemas/dialect-draft.json').read_text())
        rows = json.loads((ROOT / 'contract/vectors/dialect-authoring/retention.json').read_text())['items']
        current = json.loads((ROOT / 'contract/testdata/dialect-authoring/authoring.json').read_text())['conversionContract']
        superseded = json.loads((ROOT / 'contract/testdata/dialect-authoring/superseded-prototype-identity.json').read_text())
        validator = jsonschema.Draft202012Validator(schema)
        present = [row for row in rows if row.get('candidate', {}).get('conversionContract')]
        historical = json.loads((ROOT / 'contract/testdata/dialect-authoring/superseded-private-draft-identity.json').read_text())
        self.assertEqual(len(present), 11)
        self.assertEqual(sum(row['candidate']['conversionContract'] == historical for row in present), 8)
        self.assertEqual(sum(row['candidate']['conversionContract'] == current for row in present), 3)
        for row in present:
            self.assertIn(row['candidate']['conversionContract'], [current, historical])
            validator.validate(row)
        for choices in itertools.product([False, True], repeat=3):
            row = copy.deepcopy(present[-1])
            for stale, member in zip(choices, ['revision', 'schemaSha256', 'corpusSha256']):
                if stale:
                    row['candidate']['conversionContract']['spec'][member] = superseded['spec'][member]
            self.assertEqual(validator.is_valid(row), not any(choices), choices)

        # Historical inspection support never permits mixing identities across versions.
        for choices in itertools.product([False, True], repeat=4):
            row = copy.deepcopy(present[-1])
            for stale, member in zip(choices, ['version', 'revision', 'schemaSha256', 'corpusSha256']):
                if stale:
                    row['candidate']['conversionContract']['spec'][member] = historical['spec'][member]
            self.assertEqual(validator.is_valid(row), all(choices) or not any(choices), choices)

    def test_version_two_carries_the_inventory_index_and_version_one_still_reads(self):
        """#243: version 2 carries researchIndex, never researchDeclarations; v0.11.0 version-1 rows still read."""
        schema = json.loads((ROOT / 'contract/schemas/dialect-draft.json').read_text())
        rows = json.loads((ROOT / 'contract/vectors/dialect-authoring/retention.json').read_text())['items']
        validator = jsonschema.Draft202012Validator(schema)
        second = [row for row in rows if row.get('version') == 2]
        first = [row for row in rows if row.get('version') == 1]
        self.assertEqual(len(second), 2)
        self.assertTrue(any(row['candidate']['researchIndex'] is None for row in second))
        self.assertTrue(any('researchDeclarations' in row['candidate'] for row in first))
        for row in first + second:
            validator.validate(row)
        missing = copy.deepcopy(second[0])
        del missing['candidate']['researchIndex']
        self.assertFalse(validator.is_valid(missing))
        mixed = copy.deepcopy(second[0])
        mixed['candidate']['researchDeclarations'] = None
        self.assertFalse(validator.is_valid(mixed))
        old = copy.deepcopy(first[-1])
        old['candidate']['researchIndex'] = second[0]['candidate']['researchIndex']
        self.assertFalse(validator.is_valid(old))
        relabelled = copy.deepcopy(second[0])
        relabelled['version'] = 1
        self.assertFalse(validator.is_valid(relabelled))

    def test_paging_vector_digests_are_exact(self):
        """The handwritten index digests are SHA-256 over exact declaration bytes and the canonical index."""
        import hashlib
        vector = json.loads((ROOT / 'contract/vectors/dialect-authoring/paging.json').read_text())
        canonical = lambda value: json.dumps(value, sort_keys=True, separators=(',', ':'), ensure_ascii=False)
        sha = lambda text: 'sha256:' + hashlib.sha256(text.encode()).hexdigest()
        for inventory, index in ((vector['inventory'], vector['index']), (vector['changedInventory'], vector['changedIndex'])):
            self.assertEqual(index['digest'], sha(canonical(index['revisions'])))
            self.assertEqual([r['revision'] for r in inventory], [r['revision'] for r in index['revisions']])
            for revision, indexed in zip(inventory, index['revisions']):
                if revision['fields'] is None:
                    self.assertIsNone(indexed['fields'])
                    continue
                self.assertEqual([(f['name'], sha(f['fieldJson'])) for f in revision['fields']], [(f['name'], f['digest']) for f in indexed['fields']])
        self.assertNotEqual(vector['index']['digest'], vector['changedIndex']['digest'])
        schema = json.loads((ROOT / 'contract/schemas/dialect-authoring.json').read_text())
        requests = jsonschema.Draft202012Validator({**schema, '$ref': '#/$defs/pageRequest'})
        for case in vector['cases']:
            for request in case.get('requests', []):
                requests.validate(request)
