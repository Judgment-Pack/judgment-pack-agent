"""The standing advisory exceptions' gate: each condition fails it, unavailable evidence fails it."""
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / 'scripts/advisory-exceptions.py'
SPEC = importlib.util.spec_from_file_location('advisory_exceptions', SCRIPT)
gate = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(gate)

CLEAN_CLOSURE = '\n'.join([
    'fmt',
    'github.com/Judgment-Pack/judgment-pack-agent/go/context',
    'github.com/Judgment-Pack/judgment-pack-agent/go/context [github.com/Judgment-Pack/judgment-pack-agent/go/context.test]',
    'github.com/Judgment-Pack/judgment-pack-agent/go/context.test',
    'golang.org/x/arch/x86/x86asm',
    'rsc.io/pdfx/notthemodule',
])
CLEAN_PACKAGES = [
    {'ImportPath': 'github.com/Judgment-Pack/judgment-pack-agent/go/context', 'Imports': ['fmt'],
     'TestImports': ['testing'], 'XTestImports': None},
]
CONFIG = {'config': {'protocol_version': 'v1.0.0', 'scanner_name': 'govulncheck', 'db': 'https://vuln.go.dev',
                     'go_version': 'go1.26.11'}}
OTHER = {'finding': {'osv': 'GO-2026-6859', 'trace': [{'module': 'stdlib', 'package': 'net/http', 'function': 'Header.Add'}]}}


def finding(level):
    frame = {'module': 'rsc.io/pdf', 'version': 'v0.1.1'}
    if level in ('package', 'symbol'):
        frame['package'] = 'rsc.io/pdf'
    if level == 'symbol':
        frame['function'] = 'Open'
    return {'finding': {'osv': 'GO-2026-5781', 'trace': [frame]}}


class Units(unittest.TestCase):
    def test_closure_finds_the_module_and_its_test_variants_only(self):
        self.assertEqual(gate.closure_hits(CLEAN_CLOSURE, 'rsc.io/pdf'), [])
        listing = CLEAN_CLOSURE + '\nrsc.io/pdf\nrsc.io/pdf/sub [example.test/a.test]\nrsc.io/pdf.test\n'
        self.assertEqual(gate.closure_hits(listing, 'rsc.io/pdf'), ['rsc.io/pdf', 'rsc.io/pdf/sub'])

    def test_every_import_field_is_read(self):
        for field in ('Imports', 'TestImports', 'XTestImports'):
            with self.subTest(field=field):
                packages = [dict(CLEAN_PACKAGES[0], **{field: ['rsc.io/pdf']})]
                self.assertEqual(gate.import_hits(packages, 'rsc.io/pdf'),
                                 [('github.com/Judgment-Pack/judgment-pack-agent/go/context', 'rsc.io/pdf')])
        self.assertEqual(gate.import_hits(CLEAN_PACKAGES, 'rsc.io/pdf'), [])

    def test_a_finding_at_any_level_counts_and_others_do_not(self):
        text = lambda *items: '\n'.join(json.dumps(i, indent=1) for i in items)
        self.assertEqual(gate.vulncheck_hits(text(CONFIG, OTHER), 'GO-2026-5781'), [])
        for level in ('module', 'package', 'symbol'):
            with self.subTest(level=level):
                self.assertEqual(gate.vulncheck_hits(text(CONFIG, OTHER, finding(level)), 'GO-2026-5781'), [level])

    def test_unreadable_or_databaseless_govulncheck_output_is_unavailable(self):
        for text in ['', 'not json', json.dumps({'progress': {'message': 'x'}}),
                     json.dumps({'config': {'scanner_name': 'govulncheck'}})]:
            with self.subTest(text=text):
                with self.assertRaises(gate.Unavailable):
                    gate.vulncheck_hits(text, 'GO-2026-5781')

    def test_each_standing_exception_is_named_by_the_policy_and_no_other_is(self):
        named = gate.policy_sections((ROOT / gate.POLICY).read_text())
        self.assertEqual(sorted(named), sorted(gate.STANDING))


class Gate(unittest.TestCase):
    """check() against a fixture tree and a fake go, as CI runs it."""

    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix='advisory-exceptions-')
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        for folder in ('go', 'bin', 'docs/security'):
            (self.root / folder).mkdir(parents=True)
        shutil.copy(ROOT / gate.POLICY, self.root / gate.POLICY)
        (self.root / 'dev.json').write_text(json.dumps({'toolchains': {'go': {'version': '1.26.11'}}}))
        self.serve()

    def serve(self, version='go1.26.11', closure=CLEAN_CLOSURE, packages=CLEAN_PACKAGES, vulns=(CONFIG, OTHER),
              fail=None):
        answers = {
            'env GOVERSION': version + '\n',
            'list -deps -test ./...': closure + '\n',
            'list -test -json=ImportPath,Imports,TestImports,XTestImports ./...': ''.join(json.dumps(p, indent=1) + '\n' for p in packages),
            'run %s -format json ./...' % gate.GOVULNCHECK: ''.join(json.dumps(v, indent=1) + '\n' for v in vulns),
        }
        script = ('#!%s\nimport sys\nanswers = %r\nfail = %r\nkey = " ".join(sys.argv[1:])\n'
                  'if key == fail or key not in answers:\n    print("fixture refused " + key, file=sys.stderr)\n'
                  '    raise SystemExit(3)\nsys.stdout.write(answers[key])\n') % (sys.executable, answers, fail)
        go = self.root / 'bin/go'
        go.write_text(script)
        go.chmod(0o755)
        self.env = dict(os.environ, PATH=str(self.root / 'bin'))

    def check(self):
        saved = dict(os.environ)
        os.environ.clear()
        os.environ.update(self.env)
        try:
            return gate.check(self.root)
        finally:
            os.environ.clear()
            os.environ.update(saved)

    def test_a_clean_tree_holds_and_another_advisory_does_not_matter(self):
        self.assertEqual(self.check(), [])

    def test_a_closure_holding_the_module_fails(self):
        self.serve(closure=CLEAN_CLOSURE + '\nrsc.io/pdf')
        self.assertEqual(self.check(), ['GO-2026-5781: rsc.io/pdf is in the build or test closure'])

    def test_an_import_of_the_module_fails(self):
        self.serve(packages=[dict(CLEAN_PACKAGES[0], XTestImports=['rsc.io/pdf'])])
        self.assertEqual(self.check(), ['GO-2026-5781: github.com/Judgment-Pack/judgment-pack-agent/go/context imports rsc.io/pdf'])

    def test_a_module_level_finding_fails(self):
        self.serve(vulns=(CONFIG, OTHER, finding('module')))
        self.assertEqual(self.check(), ['GO-2026-5781: govulncheck reports a module-level finding'])

    def test_unavailable_evidence_fails(self):
        cases = {
            'another go release': dict(version='go1.26.10'),
            'go list fails': dict(fail='list -deps -test ./...'),
            'the import listing fails': dict(fail='list -test -json=ImportPath,Imports,TestImports,XTestImports ./...'),
            'govulncheck fails': dict(fail='run %s -format json ./...' % gate.GOVULNCHECK),
            'govulncheck names no database': dict(vulns=({'config': {'scanner_name': 'govulncheck'}},)),
        }
        for name, change in cases.items():
            with self.subTest(case=name):
                self.serve(**change)
                problems = self.check()
                self.assertEqual(len(problems), 1, problems)
                self.assertTrue(problems[0].startswith('unavailable evidence: '), problems)

    def test_a_policy_without_the_section_or_with_another_fails(self):
        policy = self.root / gate.POLICY
        text = policy.read_text()
        policy.write_text(text.replace('## Standing exception: GO-2026-5781', '## Former exception: GO-2026-5781'))
        self.assertEqual(self.check(), ['GO-2026-5781 has no standing exception section in %s' % gate.POLICY])
        policy.write_text(text + '\n## Standing exception: GO-2026-0001 (example.test/other)\n')
        self.assertEqual(self.check(), ['%s names a standing exception for GO-2026-0001 that this check does not hold' % gate.POLICY])

    def test_the_command_line_refuses_with_exit_1(self):
        script = self.root / 'scripts/advisory-exceptions.py'
        script.parent.mkdir()
        shutil.copy(SCRIPT, script)
        self.serve(closure='rsc.io/pdf')
        result = subprocess.run([sys.executable, '-I', str(script)], env=self.env, capture_output=True, text=True,
                                timeout=60, stdin=subprocess.DEVNULL)
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn('rsc.io/pdf is in the build or test closure', result.stderr)
        self.assertIn('advisory exceptions: refused', result.stdout)
        self.serve()
        result = subprocess.run([sys.executable, '-I', str(script)], env=self.env, capture_output=True, text=True,
                                timeout=60, stdin=subprocess.DEVNULL)
        self.assertEqual((result.returncode, result.stdout.strip()), (0, 'advisory exceptions: standing exceptions hold: GO-2026-5781'))


if __name__ == '__main__':
    unittest.main()
