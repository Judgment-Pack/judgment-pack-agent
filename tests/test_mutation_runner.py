"""The mutation runner's verdicts (agent#285), through the runner itself, on a small repository.

The table is release evidence, so a row must not come back caught unless its mutant caused a test to
fail. These tests run packages/harness/scripts/mutation-check.mjs as it stands, with the repository's
installed vitest, over a fixture repository whose whole suite takes seconds. The fixture's sources are
written here by hand. broken.test.ts fails with no mutant, as a test can in the runner's copy, and
unloadable.test.ts does not load with no mutant.
"""
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
RUNNER = ROOT / 'packages/harness/scripts/mutation-check.mjs'

SOURCES = {
    'notes.txt': 'kept outside the package\n',
    'packages/harness/vitest.config.ts': "export default {test:{include:['**/*.test.ts'],exclude:['node_modules/**','dist/**'],maxWorkers:2}}\n",
    'packages/harness/adder.ts': '// the adder\nexport const add=(a:number,b:number)=>a+b\n',
    'packages/harness/adder.test.ts': "import {it,expect} from 'vitest'\nimport {add} from './adder'\nit('adds',()=>{expect(add(1,2)).toBe(3)})\n",
    'packages/harness/broken.test.ts': "import {it,expect} from 'vitest'\nit('fails with no mutant',()=>{expect(1).toBe(2)})\n",
    # Only held.test.ts notices held.ts: a row that names adder.test.ts needs the whole suite.
    'packages/harness/held.ts': "export const held='kept'\n",
    'packages/harness/held.test.ts': "import {it,expect} from 'vitest'\nimport {held} from './held'\nit('holds',()=>{expect(held).toBe('kept')})\n",
    # One test here fails with no mutant, and the other only under the mutant.
    'packages/harness/mixed.ts': "export const mixed='kept'\n",
    'packages/harness/mixed.test.ts': "import {it,expect} from 'vitest'\nimport {mixed} from './mixed'\n"
                                      "it('a failure already there',()=>{expect(1).toBe(2)})\nit('noticed',()=>{expect(mixed).toBe('kept')})\n",
    # Reads a file outside the package, as the suite's corpus, Go and generated-page tests do.
    'packages/harness/outside.test.ts': "import {it,expect} from 'vitest'\nimport {readFileSync} from 'node:fs'\n"
                                        "it('reads the repository',()=>{expect(readFileSync('../../notes.txt','utf8')).toBe('kept outside the package\\n')})\n",
    # Under its mutant this fails beside the rest of the suite, and passes when its file runs alone: load,
    # not the mutant, as a test near its time limit on a slow runner. The parent's arguments name the
    # file only in a run of it alone.
    'packages/harness/load.ts': "export const load='plain'\n",
    'packages/harness/load.test.ts': "import {it,expect} from 'vitest'\nimport {readFileSync} from 'node:fs'\nimport {load} from './load'\n"
                                     "const alone=readFileSync(`/proc/${process.ppid}/cmdline`,'utf8').includes('load.test.ts')\n"
                                     "it('holds beside the suite',()=>{expect(load==='plain'||alone).toBe(true)})\n",
    # The generated reference's check names source lines, so any mutant that moves a line fails it.
    'packages/harness/providers-reference.test.ts': "import {it,expect} from 'vitest'\nimport {readFileSync} from 'node:fs'\n"
                                                    "it('names the lines it was generated from',()=>{expect(readFileSync('adder.ts','utf8').split('\\n').length).toBe(3)})\n",
    # The same, in a file with a test that fails with no mutant, which fails again when the file runs alone.
    'packages/harness/loadmixed.ts': "export const loadMixed='plain'\n",
    'packages/harness/loadmixed.test.ts': "import {it,expect} from 'vitest'\nimport {readFileSync} from 'node:fs'\nimport {loadMixed} from './loadmixed'\n"
                                          "const alone=readFileSync(`/proc/${process.ppid}/cmdline`,'utf8').includes('loadmixed.test.ts')\n"
                                          "it('a failure beside it',()=>{expect(1).toBe(2)})\n"
                                          "it('holds beside the suite',()=>{expect(loadMixed==='plain'||alone).toBe(true)})\n",
    # agent#320: a test file that does not load runs no test. This one does not load with no mutant either.
    'packages/harness/unloadable.test.ts': "import {it} from 'vitest'\nit('never runs',()=>{})))\n",
    'packages/harness/data.json': '{"kept": true}\n',
    'packages/harness/data.test.ts': "import {it,expect} from 'vitest'\nimport data from './data.json'\nit('reads its data',()=>{expect(data.kept).toBe(true)})\n",
    # both.test.ts notices both.ts; bothload.test.ts refuses to load under the same mutant.
    'packages/harness/both.ts': "export const both='kept'\n",
    'packages/harness/both.test.ts': "import {it,expect} from 'vitest'\nimport {both} from './both'\nit('notices',()=>{expect(both).toBe('kept')})\n",
    'packages/harness/bothload.test.ts': "import {it} from 'vitest'\nimport {both} from './both'\n"
                                         "if(both!=='kept') throw new Error('refuses to load')\nit('loads',()=>{})\n",
    # Under its mutant this refuses to load beside the rest of the suite, and loads when its file runs alone.
    'packages/harness/loadonly.ts': "export const loadOnly='plain'\n",
    'packages/harness/loadonly.test.ts': "import {it} from 'vitest'\nimport {readFileSync} from 'node:fs'\nimport {loadOnly} from './loadonly'\n"
                                         "const alone=readFileSync(`/proc/${process.ppid}/cmdline`,'utf8').includes('loadonly.test.ts')\n"
                                         "if(loadOnly!=='plain'&&!alone) throw new Error('refuses to load beside the suite')\nit('loads',()=>{})\n",
}
COMMENT = {'file': 'adder.ts', 'find': '// the adder', 'replace': '// the adder, reworded'}
ROWS = {
    'a comment, whose selection passes': dict(COMMENT, tests=['adder.test.ts']),
    'a comment, whose selection fails with no mutant': dict(COMMENT, tests=['broken.test.ts']),
    'a comment, with no selection': dict(COMMENT, tests=[]),
    'a mutant its selection catches': {'file': 'adder.ts', 'find': 'a+b', 'replace': 'a-b', 'tests': ['adder.test.ts']},
    'a mutant only the whole suite catches': {'file': 'held.ts', 'find': "'kept'", 'replace': "'lost'", 'tests': ['adder.test.ts']},
    'a mutant beside a failure already there': {'file': 'mixed.ts', 'find': "'kept'", 'replace': "'lost'", 'tests': ['mixed.test.ts']},
    'a mutant that fails only under load on the whole suite': {'file': 'load.ts', 'find': "'plain'", 'replace': "'other'", 'tests': ['adder.test.ts']},
    'a mutant under load beside a failure already there': {'file': 'loadmixed.ts', 'find': "'plain'", 'replace': "'other'", 'tests': ['adder.test.ts']},
    'a mutant the whole suite catches, whose selection fails with no mutant': {'file': 'held.ts', 'find': "'kept'", 'replace': "'lost'", 'tests': ['broken.test.ts']},
    'a comment that moves a line, whose selection passes': dict(COMMENT, replace='// the adder\n// reworded', tests=['adder.test.ts']),
    'a mutant of a generated page, naming its check': dict(COMMENT, replace='// the adder\n// reworded', tests=['providers-reference.test.ts']),
    'a mutant of a file outside the package': {'file': '../../notes.txt', 'find': 'kept', 'replace': 'lost', 'tests': ['outside.test.ts']},
    # agent#320: a mutant that stops a test file from loading is BROKEN, and names the load error.
    'a mutant that stops its module loading': {'file': 'adder.ts', 'find': 'a+b', 'replace': '(a+b))', 'tests': ['adder.test.ts']},
    'a mutant that stops a module only the whole suite loads': {'file': 'held.ts', 'find': "'kept'", 'replace': "'kept'))", 'tests': ['adder.test.ts']},
    'a mutant that throws as its module loads': {'file': 'held.ts', 'find': "export const held='kept'",
                                                  'replace': "throw new Error('held does not load')\nexport const held='kept'", 'tests': ['held.test.ts']},
    'a mutant that leaves its JSON unreadable': {'file': 'data.json', 'find': 'true}', 'replace': 'true}}', 'tests': ['data.test.ts']},
    'a mutant that stops a file loading only beside the suite': {'file': 'loadonly.ts', 'find': "'plain'", 'replace': "'other'", 'tests': ['adder.test.ts']},
    'a mutant caught beside a test file it stops loading': {'file': 'both.ts', 'find': "'kept'", 'replace': "'lost'", 'tests': ['both.test.ts', 'bothload.test.ts']},
}


class Runner(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.directory = tempfile.TemporaryDirectory(prefix='mutation-runner-')
        cls.addClassCleanup(cls.directory.cleanup)
        cls.root = Path(cls.directory.name)
        for name, text in SOURCES.items():
            path = cls.root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(text, encoding='utf-8')
        (cls.root / 'package.json').write_text('{"name":"fixture","private":true}\n', encoding='utf-8')
        (cls.root / '.gitignore').write_text('node_modules\ndist\n', encoding='utf-8')
        scripts = cls.root / 'packages/harness/scripts'
        scripts.mkdir()
        shutil.copy(RUNNER, scripts / RUNNER.name)
        table = [dict(row, name=name, status='caught', origin='fixture') for name, row in ROWS.items()]
        (scripts / 'mutation-rows.json').write_text(json.dumps({'rows': table}), encoding='utf-8')
        os.symlink(ROOT / 'node_modules', cls.root / 'node_modules')
        env = dict(os.environ, GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_NOSYSTEM='1')
        for argv in (['git', 'init', '-q', '-b', 'main'], ['git', 'add', '.'],
                     ['git', '-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', 'commit', '-qm', 'fixture']):
            subprocess.run(argv, cwd=cls.root, check=True, env=env, stdin=subprocess.DEVNULL,
                           stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        cls.all = cls.mutation()
        cls.verdicts = dict(re.findall(r'^\| (.+?) \| \S+ \| (.+?) \|$', cls.all.stdout, re.M))

    @classmethod
    def mutation(cls, *options):
        return subprocess.run([shutil.which('node'), str(cls.root / 'packages/harness/scripts' / RUNNER.name), '--verbose', *options],
                              cwd=cls.root, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                              text=True, timeout=240)

    def verdict(self, name):
        self.assertIn(name, self.verdicts, self.all.stdout + self.all.stderr)
        return self.verdicts[name]

    def test_a_comment_only_row_is_not_caught_whatever_fails_with_no_mutant(self):
        for name in ('a comment, whose selection passes', 'a comment, whose selection fails with no mutant', 'a comment, with no selection'):
            with self.subTest(row=name):
                self.assertRegex(self.verdict(name), r'^NOT CAUGHT: ')
        self.assertNotEqual(self.all.returncode, 0)

    def test_the_generated_reference_catches_only_a_row_that_names_it(self):
        self.assertRegex(self.verdict('a comment that moves a line, whose selection passes'), r'^NOT CAUGHT: ')
        self.assertEqual(self.verdict('a mutant of a generated page, naming its check'),
                         'caught: selected: providers-reference.test.ts > names the lines it was generated from')

    def test_a_mutant_its_selection_notices_is_caught_by_the_selection(self):
        self.assertRegex(self.verdict('a mutant its selection catches'), r'^caught: selected: adder\.test\.ts > adds$')

    def test_a_mutant_only_another_test_notices_is_caught_by_the_whole_suite(self):
        self.assertRegex(self.verdict('a mutant only the whole suite catches'),
                         r'^caught: whole, after its selection decided nothing: held\.test\.ts > holds$')

    def test_only_the_mutants_own_failure_counts_beside_one_already_there(self):
        self.assertEqual(self.verdict('a mutant beside a failure already there'), 'caught: selected: mixed.test.ts > noticed')

    def test_a_failure_that_does_not_fail_again_in_its_own_file_does_not_count(self):
        for name, test in (('a mutant that fails only under load on the whole suite', 'load'),
                           ('a mutant under load beside a failure already there', 'loadmixed')):
            with self.subTest(row=name):
                self.assertRegex(self.verdict(name), r'^NOT CAUGHT: whole, after its selection decided nothing: '
                                 r'what failed did not fail again in its own file: %s\.test\.ts' % test)

    def test_a_selection_that_only_fails_with_no_mutant_leaves_the_row_to_the_whole_suite(self):
        self.assertRegex(self.verdict('a mutant the whole suite catches, whose selection fails with no mutant'),
                         r'^caught: whole, after its selection decided nothing: held\.test\.ts > holds$')

    def test_a_row_on_a_file_outside_the_package_applies_and_is_caught(self):
        self.assertEqual(self.verdict('a mutant of a file outside the package'), 'caught: selected: outside.test.ts > reads the repository')

    def test_a_file_is_asked_with_no_mutant_once_and_what_fails_there_is_named(self):
        lines = [line for line in self.all.stderr.splitlines() if line.startswith('mutation-baseline fails ')]
        self.assertEqual(sorted(lines), ['mutation-baseline fails with no mutant: broken.test.ts > fails with no mutant',
                                         'mutation-baseline fails with no mutant: loadmixed.test.ts > a failure beside it',
                                         'mutation-baseline fails with no mutant: mixed.test.ts > a failure already there'])
        unloaded = [line for line in self.all.stderr.splitlines() if line.startswith('mutation-baseline does not load ')]
        self.assertEqual(len(unloaded), 1, self.all.stderr)
        self.assertRegex(unloaded[0], r'^mutation-baseline does not load with no mutant: unloadable\.test\.ts: \S')

    def test_a_mutant_that_stops_a_test_file_loading_is_broken_and_names_the_error(self):
        """agent#320: not NOT CAUGHT, which sends a reader looking for a safeguard nothing holds."""
        whole = 'whole, after its selection decided nothing: no test failed, and the mutant stops '
        for name, file, error in (('a mutant that stops its module loading', 'adder.test.ts', r'.*\(at \S*adder\.ts:2:\d+\)'),
                                  ('a mutant that stops a module only the whole suite loads', 'held.test.ts', r'.*\(at \S*held\.ts:1:\d+\)'),
                                  ('a mutant that throws as its module loads', 'held.test.ts', r'held does not load'),
                                  ('a mutant that leaves its JSON unreadable', 'data.test.ts', r'\S')):
            with self.subTest(row=name):
                verdict = self.verdict(name)
                self.assertRegex(verdict, r'^BROKEN: %s%s from loading: %s' % (re.escape(whole), re.escape(file), error))
                # The test file that does not load with no mutant is not the row's.
                self.assertNotIn('unloadable', verdict)
                self.assertNotIn('\x1b', verdict)
                self.assertNotRegex(verdict, r'/mutation-\w+/')

    def test_a_test_file_that_loads_again_in_a_run_of_its_own_does_not_make_the_row_broken(self):
        """As a failure must fail again in its own file: load on the whole suite cannot break a row."""
        self.assertEqual(self.verdict('a mutant that stops a file loading only beside the suite'),
                         'NOT CAUGHT: whole, after its selection decided nothing: no test failed beyond the failures with no mutant')

    def test_a_failure_the_mutant_caused_is_a_catch_beside_a_test_file_it_stops_loading(self):
        self.assertEqual(self.verdict('a mutant caught beside a test file it stops loading'), 'caught: selected: both.test.ts > notices')

    def test_the_counts_say_what_the_rows_did(self):
        self.assertIn('18 rows chosen: 7 caught, 7 not caught, 4 broken, 0 did not apply, 0 undecided, 0 retired', self.all.stdout)
        self.assertNotEqual(self.all.returncode, 0)

    def test_the_row_lint_applies_each_row_and_parses_the_file_it_touches(self):
        """--check: a row whose replacement no longer fits its find fails before any test runs. It parses
        as the loader would; a mutant that throws only when its module runs is the runner's to find."""
        done = self.mutation('--check')
        self.assertEqual(done.returncode, 1, done.stdout + done.stderr)
        broken = re.findall(r'^BROKEN \((\S+) does not load under the mutant: (.+)\): (.+)$', done.stdout, re.M)
        self.assertEqual(sorted((file, name) for file, _, name in broken),
                         [('adder.ts', 'a mutant that stops its module loading'),
                          ('data.json', 'a mutant that leaves its JSON unreadable'),
                          ('held.ts', 'a mutant that stops a module only the whole suite loads')])
        self.assertTrue(all(error.strip() for _, error, _ in broken))
        self.assertNotIn('DOES NOT APPLY', done.stdout)
        self.assertNotIn('DOES NOT LOAD WITH NO MUTANT', done.stdout)
        self.assertIn('18 rows checked, 3 problems; 0 retired', done.stdout)

    def test_the_copy_holds_the_repository_and_the_no_mutant_run_names_only_what_truly_fails(self):
        """outside.test.ts reads a file outside the package: in a copy of the package alone it fails."""
        done = self.mutation('--no-mutant')
        self.assertEqual(done.returncode, 1, done.stdout + done.stderr)
        self.assertEqual(sorted(re.findall(r'^fails with no mutant: (.+)$', done.stdout, re.M)),
                         ['broken.test.ts > fails with no mutant', 'loadmixed.test.ts > a failure beside it',
                          'mixed.test.ts > a failure already there'])
        self.assertIn('the whole suite with no mutant: 3 failed, 1 test file did not load', done.stdout)
        self.assertRegex(done.stdout, r'(?m)^does not load with no mutant: unloadable\.test\.ts: \S')

    def test_a_selection_with_a_timeout_decides_nothing_and_fails_the_run(self):
        """A run that cannot answer is never a catch, under a mutant or with none."""
        done = self.mutation('--row', 'a mutant its selection catches', '--timeout', '0.001')
        self.assertEqual(done.returncode, 1, done.stdout + done.stderr)
        self.assertRegex(done.stdout, r'\| a mutant its selection catches \| \S+ \| UNDECIDED: ')


if __name__ == '__main__':
    unittest.main()
