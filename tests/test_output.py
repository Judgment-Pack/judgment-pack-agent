"""The versioned pattern table is shipped unchanged to both language readers."""
import json
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parent.parent

class OutputPatternsTests(unittest.TestCase):
    def test_versioned_patterns_have_positive_and_negative_vectors_and_equal_copies(self):
        raw = (ROOT / 'contract/patterns/output.json').read_bytes()
        table = json.loads(raw)
        self.assertEqual(table['version'], 1)
        for target in ['packages/harness/contract/output-patterns.json', 'go/context/output_patterns.json']:
            self.assertEqual((ROOT / target).read_bytes(), raw)
        for pattern in table['patterns']:
            vector = json.loads((ROOT / ('contract/vectors/output/base-' + pattern['name'] + '.json')).read_text())
            self.assertIn('negative', vector['guard'])
            self.assertEqual(vector['guard']['output'], '[redacted: ' + pattern['name'] + ']')
