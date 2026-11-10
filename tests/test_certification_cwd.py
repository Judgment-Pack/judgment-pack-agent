"""The certification suite runs from any working directory (#345).

A host runs the published suite from its own directory, as the Desk does when it certifies its
harness adapters, so a file the suite reads must be found relative to the test module, never to
the working directory. This runs packages/harness/certification/scenarios.test.ts, as it stands,
with the repository's installed vitest, from an empty directory outside the repository, and holds
every test of the file to having passed. Before #345 the current-contract replay check read
`certification/runtime.json` relative to the working directory and failed there with ENOENT.
"""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
PACKAGE = ROOT / 'packages/harness'
VITEST = ROOT / 'node_modules/vitest/vitest.mjs'
SUITE = 'certification/scenarios.test.ts'
# The check that read the file relative to the working directory.
READS_RUNTIME_JSON = 'serves explicit synthetic taxonomy while preserving the explicitly selected historical recording'


class FromAnotherDirectory(unittest.TestCase):
    def test_the_scenarios_pass_from_a_directory_outside_the_repository(self):
        self.assertTrue(VITEST.is_file(), 'vitest is not installed: run npm ci first')
        with tempfile.TemporaryDirectory(prefix='certification-cwd-') as room:
            elsewhere = Path(room, 'host')
            elsewhere.mkdir()
            report = Path(room, 'report.json')
            done = subprocess.run([shutil.which('node'), str(VITEST), 'run', '--root', str(PACKAGE), SUITE,
                                   '--reporter=json', '--outputFile=%s' % report],
                                  cwd=elsewhere, stdin=subprocess.DEVNULL,
                                  stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, timeout=900)
            self.assertEqual(sorted(os.listdir(elsewhere)), [], 'the run wrote into its working directory')
            parsed = json.loads(report.read_text(encoding='utf-8'))
        results = [test for file in parsed['testResults'] for test in file['assertionResults']]
        failed = ['%s: %s' % (test['fullName'], ' '.join(test.get('failureMessages', []))[:400])
                  for test in results if test['status'] == 'failed']
        self.assertEqual(failed, [], done.stdout[-4000:])
        self.assertEqual([file['status'] for file in parsed['testResults']], ['passed'], done.stdout[-4000:])
        self.assertEqual(done.returncode, 0, done.stdout[-4000:])
        self.assertIn(READS_RUNTIME_JSON, [test['title'] for test in results if test['status'] == 'passed'])


if __name__ == '__main__':
    unittest.main()
