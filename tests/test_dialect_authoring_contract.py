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
        """#243, #252: the handwritten top index, revision field indexes and declaration digests are exact SHA-256 values."""
        import hashlib
        vector = json.loads((ROOT / 'contract/vectors/dialect-authoring/paging.json').read_text())
        canonical = lambda value: json.dumps(value, sort_keys=True, separators=(',', ':'), ensure_ascii=False)
        sha = lambda text: 'sha256:' + hashlib.sha256(text.encode()).hexdigest()
        field_index = lambda revision: {'revision': revision['revision'], 'fields': [{'name': f['name'], 'digest': sha(f['fieldJson'])} for f in revision['fields']]}

        def top(inventory):
            rows = []
            for revision in inventory:
                if revision['fields'] is None:
                    rows.append({'revision': revision['revision'], 'fields': None, 'digest': None, 'bytes': None})
                    continue
                raw = canonical(field_index(revision))
                rows.append({'revision': revision['revision'], 'fields': len(revision['fields']), 'digest': sha(raw), 'bytes': len(raw.encode())})
            return {'digest': sha(canonical(rows)), 'revisions': rows}
        for inventory, index in ((vector['inventory'], vector['index']), (vector['changedInventory'], vector['changedIndex'])):
            self.assertEqual(index, top(inventory))
        self.assertNotEqual(vector['index']['digest'], vector['changedIndex']['digest'])
        entries = vector['index']['revisions']
        for page in vector['fieldIndexes'][:2]:
            raw = canonical(page['fieldIndex'])
            entry = entries[page['entry']]
            self.assertEqual((sha(raw), len(raw.encode()), len(page['fieldIndex']['fields'])), (entry['digest'], entry['bytes'], entry['fields']))
        wrong = vector['fieldIndexes'][2]
        raw = canonical(wrong['fieldIndex'])
        self.assertEqual(len(raw.encode()), entries[wrong['entry']]['bytes'])
        self.assertNotEqual(sha(raw), entries[wrong['entry']]['digest'])
        schema = json.loads((ROOT / 'contract/schemas/dialect-authoring.json').read_text())
        shape = lambda name: jsonschema.Draft202012Validator({'$defs': schema['$defs'], '$ref': '#/$defs/' + name})
        for case in vector['cases']:
            for request in case.get('requests', []):
                shape('pageRequest').validate(request)
        for request in vector['fieldIndexRequests']:
            shape('fieldIndexRequest').validate(request)
        for page in vector['fieldIndexes']:
            shape('fieldIndexPage').validate(page)
        for page in vector['items']:
            shape('page').validate(page)
        draft = json.loads((ROOT / 'contract/schemas/dialect-draft.json').read_text())
        index = jsonschema.Draft202012Validator({'$defs': draft['$defs'], '$ref': '#/$defs/researchIndex'})
        index.validate(vector['index'])
        for member in ('digest', 'bytes'):
            mixed = copy.deepcopy(vector['index'])
            mixed['revisions'][2][member] = mixed['revisions'][0][member]
            self.assertFalse(index.is_valid(mixed), member)
            missing = copy.deepcopy(vector['index'])
            missing['revisions'][0][member] = None
            self.assertFalse(index.is_valid(missing), member)

    def test_bound_proof_arithmetic(self):
        """#252: the top index and revision field index bounds, derived from the shape and the Desk domain."""
        canonical = lambda value: json.dumps(value, sort_keys=True, separators=(',', ':'), ensure_ascii=False)
        # An available top entry costs 117 bytes plus the digits of its byte length and field count and its
        # escaped label; the wrapper costs 99 bytes, and entries are separated by commas.
        entry = lambda label, fields, size: 117 + len(str(size)) + len(str(fields)) + label
        top = lambda revisions, each: 99 + revisions * each + revisions - 1
        # A field index entry costs 94 bytes plus its escaped name; the field index wrapper 27 bytes plus its label.
        field_index = lambda fields, name, label: 27 + label + fields * (94 + name) + fields - 1
        sample = {'revision': 'x' * 160, 'fields': 1000, 'digest': 'sha256:' + 'a' * 64, 'bytes': 1048576}
        self.assertEqual(len(canonical(sample)), entry(160, 1000, 1048576))
        self.assertEqual(len(canonical({'digest': sample['digest'], 'revisions': [sample, sample]})), top(2, entry(160, 1000, 1048576)))
        one = {'revision': 'x' * 160, 'fields': [{'name': 'n' * 64, 'digest': sample['digest']}] * 2}
        self.assertEqual(len(canonical(one)), field_index(2, 64, 160))
        # Canonical JSON escapes a one-byte character to at most six bytes (\u0001), so a 160-byte label costs at most 960.
        self.assertEqual(len(canonical('\u0001')) - 2, 6)
        index_bytes, page_bytes = 2097152, 1048576
        self.assertEqual(top(1000, entry(960, 1000, page_bytes)), 1089098)
        self.assertLessEqual(1089098, index_bytes)
        self.assertEqual(top(1000, entry(160, 30, field_index(30, 64, 160))), 284098)
        self.assertEqual(field_index(30, 64, 160), 4956)
        self.assertEqual(field_index(1000, 160, 160), 255186)
        self.assertLessEqual(255186, page_bytes)
        issue = 98 + 4842 * 450 + sum(len(str(n)) for n in range(1, 451))
        self.assertEqual(issue, 2180240)
        rows = {row['name']: row for row in json.loads((ROOT / 'contract/vectors/dialect-authoring/paging.json').read_text())['bounds']}
        self.assertEqual([row['indexBytes'] for row in rows.values()], [77390, 284098, 386, 1089098, 2178098, 1187, 1090187, 229, 230, 233])
        self.assertEqual(rows['issue #252 family: 450 revisions x 30 fields with 64-byte names']['oneLevelIndexBytes'], issue)
        self.assertEqual(rows['largest accepted Desk collection: 1,000 revisions x 30 fields, 160-byte labels, 64-byte names']['fieldIndexBytes'], 4956)
        self.assertEqual(rows['largest unescaped revision field index the shape admits: 1,000 fields with 160-byte names']['fieldIndexBytes'], 255186)
        self.assertEqual(top(2000, entry(960, 1000, page_bytes)), 2178098)
        self.assertEqual(top(1, entry(960, 1000, page_bytes + 1)), 1187)
        # 1,001 revisions stay inside the cap, so the revision limit, not the byte cap, refuses them.
        self.assertEqual(top(1001, entry(960, 1000, page_bytes)), 1090187)
        self.assertLessEqual(1090187, index_bytes)
