"""Handwritten fixture repositories exercise the actual shared checker CLI."""
import importlib.util
import copy
import contextlib
import io
from unittest.mock import patch
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
HELPER = Path(os.environ.get('GENERATED_DOCS_HELPER', ROOT / 'lib/generated_docs.py'))
if not HELPER.exists():
    HELPER = ROOT / 'scripts/dev/generated_docs.py'
SPEC = importlib.util.spec_from_file_location('generated_docs', HELPER)
g = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(g)


class GeneratedDocsTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix='generated-docs-fixture-')
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        subprocess.run(['git', 'init', '-q', str(self.root)], check=True, capture_output=True)
        self.write('source.txt', 'fresh\n')
        self.write('docs/result.md', 'fresh\n')
        self.write('generator.py', "from pathlib import Path\nPath('docs/result.md').write_text(Path('source.txt').read_text())\n")
        self.item = {'name':'reference', 'run':'python3 generator.py', 'outputs':['docs/result.md'], 'timeout':5}
        self.declare()

    def write(self, value, text):
        path = self.root / value
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text)

    def declare(self):
        self.write('jpdev.json', json.dumps({'version':1, 'generators':[self.item]}))

    def cli(self):
        self.declare()
        return subprocess.run([sys.executable, str(HELPER), '--root', str(self.root)],
                              capture_output=True, text=True, timeout=15)

    def test_closed_declaration_guards(self):
        good = {'version':1,'generators':[self.item]}
        cases = {
            'version': dict(good,version=2),
            'nonempty': dict(good,generators=[]),
            'members': dict(good,generators=[dict(self.item,unexpected=True)]),
            'name': dict(good,generators=[dict(self.item,name='bad name')]),
            'run': dict(good,generators=[dict(self.item,run='')]),
            'timeout': dict(good,generators=[dict(self.item,timeout=0)]),
            'outputs': dict(good,generators=[dict(self.item,outputs=[])]),
            'overlap': dict(good,generators=[dict(self.item,outputs=['docs/a','docs/a/b'])]),
            'dependencies': dict(good,generators=[dict(self.item,dependencies=['node_modules','node_modules'])]),
            'dependency-kind': dict(good,generators=[dict(self.item,dependencies=['vendor'])]),
            'dependency-output': dict(good,generators=[dict(self.item,outputs=['node_modules/pkg/file'],dependencies=['node_modules'])]),
        }
        selected=os.environ.get('GENERATED_GUARD_CASE')
        for name,value in cases.items():
            if selected and name != selected:
                continue
            with self.subTest(guard=name), self.assertRaises(g.Invalid):
                g.config(value)

    def test_merged_changed_and_generators_configuration_runs(self):
        declaration = {'version': 1, 'generators': [self.item],
                       'changed': [{'paths': ['docs/**'], 'checks': ['generated-docs']}]}
        self.write('jpdev.json', json.dumps(declaration))
        result = subprocess.run([sys.executable, str(HELPER), '--root', str(self.root)],
                                capture_output=True, text=True, timeout=15)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('reference current', result.stdout)
        self.assertEqual(g.config(declaration), [self.item])

    def test_shared_configuration_remains_closed_and_requires_generator_members(self):
        good = {'version': 1, 'generators': [self.item],
                'changed': [{'paths': ['docs/**'], 'checks': ['generated-docs']}]}
        for declaration in (dict(good, unexpected=True), dict(good, changde=[]),
                            {key: value for key, value in good.items() if key != 'version'},
                            {key: value for key, value in good.items() if key != 'generators'}):
            with self.subTest(declaration=declaration), self.assertRaises(g.Invalid):
                g.config(declaration)

    def test_relative_shape_guard(self):
        with self.assertRaises(g.Invalid):
            g.relative('docs\\file')

    def test_relative_escape_guard(self):
        with self.assertRaises(g.Invalid):
            g.relative('../file')

    def test_missing_tracked_source_refused(self):
        subprocess.run(['git','add','source.txt'],cwd=self.root,check=True,capture_output=True)
        (self.root/'source.txt').unlink()
        with self.assertRaises(g.Invalid):
            g.source_files(self.root)

    def test_symlinked_scratch_base_refused(self):
        cache=self.root/'.cache';cache.mkdir()
        (cache/'link').symlink_to(self.root,target_is_directory=True)
        with patch.dict(os.environ,TMPDIR=str(cache/'link')):
            with self.assertRaises(g.Invalid):
                g.check(self.root)

    def test_fresh_untracked_candidate_inputs_pass(self):
        result = self.cli()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('reference current', result.stdout)

    def test_stale_fails_at_comparison_with_file_and_command(self):
        self.write('docs/result.md', 'stale\n')
        result = self.cli()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('stale docs/result.md; regenerate: python3 generator.py', result.stderr)
        self.assertEqual((self.root/'docs/result.md').read_text(), 'stale\n')

    def test_source_change_is_read_from_candidate(self):
        self.write('source.txt', 'changed\n')
        result = self.cli()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('stale docs/result.md', result.stderr)

    def test_output_must_be_regenerated_not_inherited(self):
        self.item['run'] = 'true'
        result = self.cli()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('generator output missing: docs/result.md', result.stderr)

    def test_missing_checked_output_fails(self):
        (self.root/'docs/result.md').unlink()
        self.write('docs/keep.md', 'sibling input\n')
        result = self.cli()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('checked-in output missing', result.stderr)

    def test_failing_generator_log_read_is_bounded_before_decode(self):
        self.item['run'] = "python3 -c 'import sys; sys.stdout.write(chr(120)*65536); sys.exit(7)'"
        self.declare()
        original_open = Path.open
        reads = []
        class ObservedReader:
            def __init__(self, stream, path):
                self.stream, self.path = stream, path
            def __enter__(self):
                self.stream.__enter__()
                return self
            def __exit__(self, *args):
                return self.stream.__exit__(*args)
            def read(self, size=-1):
                data = self.stream.read(size)
                reads.append((size, len(data), self.path.stat().st_size))
                return data
        def observed_open(path, mode='r', *args, **kwargs):
            stream = original_open(path, mode, *args, **kwargs)
            return ObservedReader(stream, path) if path.name == 'generator.log' and mode == 'rb' else stream
        stderr = io.StringIO()
        with patch.object(Path, 'open', observed_open), contextlib.redirect_stderr(stderr):
            result = g.check(self.root)
        self.assertEqual(result, 7)
        self.assertTrue(reads, 'the actual failed-generator log must be read')
        self.assertTrue(all(0 <= requested <= 8192 for requested, _, _ in reads), reads)
        self.assertLessEqual(sum(consumed for _, consumed, _ in reads), 8192)
        self.assertTrue(all(log_size == 65536 for _, _, log_size in reads), reads)
        self.assertEqual(stderr.getvalue().splitlines()[0], 'x' * 8192)
        self.assertIn('generator reference exited 7', stderr.getvalue())
        self.assertEqual((self.root/'docs/result.md').read_text(), 'fresh\n')

    def test_generator_exit_status_preserved(self):
        self.item['run'] = 'python3 generator.py; echo generator-detail >&2; exit 7'
        result = self.cli()
        self.assertEqual(result.returncode, 7)
        self.assertIn('generator-detail', result.stderr)
        self.assertIn('exited 7', result.stderr)

    def test_timeout_is_bounded(self):
        self.item.update(run='python3 generator.py; sleep 3', timeout=1)
        result = self.cli()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('timed out after 1s', result.stderr)

    def test_invalid_paths_and_config_refused_before_execution(self):
        for output in ('../outside', '/absolute', '.', 'docs//x', 'docs/../x', 'docs\\x'):
            with self.subTest(output=output):
                self.item['outputs'] = [output]
                result = self.cli()
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn('Traceback', result.stderr)
        for value in ({'version':2,'generators':[]}, {'version':1,'generators':[]},
                      {'version':1,'generators':[dict(self.item, outputs=['a','a/b'])]},
                      {'version':1,'generators':[dict(self.item, outputs=['a'], timeout=True)]}):
            with self.subTest(value=value), self.assertRaises(g.Invalid):
                g.config(value)

    def test_declared_output_symlink_refused_and_target_unchanged(self):
        (self.root/'docs/result.md').unlink()
        (self.root/'docs/result.md').symlink_to(self.root/'source.txt')
        result = self.cli()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('symlink path is unsupported', result.stderr)
        self.assertEqual((self.root/'source.txt').read_text(), 'fresh\n')

    def test_generator_cannot_return_output_symlink(self):
        self.item['run'] = "ln -s ../source.txt docs/result.md"
        result = self.cli()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('symlink path is unsupported', result.stderr)

    def test_explicit_dependency_link_and_secret_cache_exclusion(self):
        self.write('.gitignore', 'node_modules/\n.cache/\n.env\n')
        self.write('node_modules/package/value.txt', 'fresh\n')
        self.write('.cache/secret', 'secret')
        self.write('.env', 'secret')
        self.item['dependencies'] = ['node_modules']
        self.write('generator.py', "from pathlib import Path\nassert not Path('.env').exists()\nassert not Path('.cache').exists()\nassert not Path('.git').exists()\nassert Path('node_modules').is_symlink()\nPath('docs/result.md').write_text(Path('node_modules/package/value.txt').read_text())\n")
        result = self.cli()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((self.root/'node_modules/package/value.txt').read_text(), 'fresh\n')

    def test_output_in_dependency_refused(self):
        self.item.update(outputs=['node_modules/pkg/file'], dependencies=['node_modules'])
        result = self.cli()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('output cannot be in a dependency tree', result.stderr)

    def test_missing_dependency_refused(self):
        self.item['dependencies'] = ['node_modules']
        result = self.cli()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('missing installed dependency', result.stderr)


if __name__ == '__main__':
    unittest.main()
