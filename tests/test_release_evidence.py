"""The agent's adoption of the devkit release evidence gate, v0.14.0 (agent #239, #240).

The gate itself is the kit's, vendored unchanged as scripts/dev/release_evidence.py; the kit's own
suite tests its contract. These tests hold the agent's side of it: the vendored copies, the
configuration, the workflows that produce and consume the evidence, and the gate run over the
agent's own configuration and workflow files with handwritten Actions API fixtures. None of this is
live evidence: no remote workflow ran for any of it.
"""
import copy
import datetime as dt
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import unittest
from unittest.mock import patch
import zipfile

ROOT = Path(__file__).resolve().parents[1]
_spec = importlib.util.spec_from_file_location('release_evidence', ROOT / 'scripts/dev/release_evidence.py')
g = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(g)

KIT = 'v0.14.0'
# Each vendored file: the kit file it copies (line 2 names both) and the SHA-256 of the copy as the
# kit's v0.14.0 tag (commit 9f555498a9e4c35d426ca1ce45352abf3363cbb3) ships it under scripts/dev/.
VENDORED = {
    'scripts/dev/release_evidence.py': ('lib/release_evidence.py',
                                        '2d1b4303d29044ad70ca6b0ed448c08937cbeaa3753a87b1e03973c204ddcbb1'),
    'scripts/dev/check.sh': ('templates/check.sh',
                             'c203aa5253162068421831a679f063ce07110024e681341bf035dc739f14b410'),
    'scripts/dev/handover_check.sh': ('lib/handover_check.sh',
                                      '1a2b7e21c8acccd18da7779d1da62e775ac87853248ad51aea369daf8c7d13e4'),
    'scripts/dev/changed_checks.py': ('lib/changed_checks.py',
                                      '253f2be03751c609c44a2d29091ef5a9272b5c658012b6362089f56958e3fa22'),
}
MANIFEST = '.github/runner-identity.json'
MANIFEST_SHA = '333c3ffe1f5d93a863298133ad15717c384146797c0cc88f2463a361e1e358da'

CONFIG_PATH = ROOT / '.github/ci-evidence.json'
CONTRACT_CHECKS = ['contract', 'local-paths', 'agent-docs', 'diff-check']
# The jobs that plan reuse from the merged pull request: the check job, and the race step's own (#254).
PLANNING_JOBS = ['check', 'go-race']
# The check job's checks that run on every branch push, and why none of them is reusable from a
# pull request's run (docs/ci-evidence.md). Every other check of the two jobs is listed as reusable.
FRESH_ON_MAIN = {
    'generated-docs': 'a generator that reads Go module metadata',
    'python-tests': "reads the checkout's release tags (scripts/check-upgrade-guides.py)",
    'dependency-audit-live': 'a live advisory query',
    'build': 'its output is read by later checks of the job',
    'packed-smoke': 'packs and installs the package',
}


def read(path):
    return (ROOT / path).read_text(encoding='utf-8')


def config():
    return json.loads(CONFIG_PATH.read_text(encoding='utf-8'))


def manifest_checks():
    manifest = json.loads(read('dev.json'))
    return [row['name'] for row in manifest['checks']] + (['parity'] if 'parity' in manifest else [])


# The sources a check runs: every tracked script, test and module of the repository, and the
# manifest's own commands. The gate and this test are exempt (they read the run's context by design).
SOURCE_EXEMPT = {'scripts/dev/release_evidence.py', 'tests/test_release_evidence.py'}
SOURCE_SUFFIX = re.compile(r'\.(mjs|cjs|js|ts|tsx|go|py|sh)$')
# Not the source of a reusable check, for the bound environment: the kit's runner scripts (their
# variables are the runner's own options, not the checks' flags) and the Python test modules (the
# python-tests check, which always runs fresh on main). The event scan covers them all the same.
RUNNER_SCRIPTS = {'scripts/dev/check.sh', 'scripts/dev/handover_check.sh', 'scripts/dev/changed_checks.py'}
PYTHON_TESTS = re.compile(r'tests/[^/]+\.py')
# Reads of an environment variable by name, in each spelling the sources use.
ENV_READ = [
    re.compile(r'process\.env(?:\.([A-Za-z_][A-Za-z0-9_]*)|\[\s*[\'"`]([A-Za-z_][A-Za-z0-9_]*)[\'"`]\s*\])'),
    re.compile(r'os\.(?:getenv|environ\.get|environ\.setdefault|environ\.pop)\(\s*[\'"]([A-Za-z_][A-Za-z0-9_]*)[\'"]'),
    re.compile(r'os\.environ\[\s*[\'"]([A-Za-z_][A-Za-z0-9_]*)[\'"]\s*\]'),
    re.compile(r'\b(?:getenv|environ\.get)\(\s*[\'"]([A-Za-z_][A-Za-z0-9_]*)[\'"]'),
    re.compile(r'os\.(?:Getenv|LookupEnv)\(\s*"([A-Za-z_][A-Za-z0-9_]*)"'),
]
# A shell expansion of an upper-case name ($NAME, ${NAME}, ${NAME:-...}); lower-case names are the
# scripts' own locals.
SHELL_READ = re.compile(r'\$\{?([A-Z_][A-Z0-9_]*)')
# Read by a source but not bound, each for a stated reason.
ENV_EXEMPT = {
    'TMPDIR': 'a per-run scratch directory, not a flag',
    'JPDEV_SCRATCH': 'a per-run scratch directory, not a flag',
    'PATH': 'where the toolchains are; the runtime identity records the tools resolved through it',
    'GITHUB_STEP_SUMMARY': 'written only by the live audit (dependency-audit-live, never reused); its test sets it',
    'LANGSMITH_API_KEY': 'a secret: never bound; a Go certification test fails when it is set',
    'CERTIFY_MEASUREMENT': 'set by packages/harness/scripts/certify.mjs for its own child',
    'CERTIFY_PROVIDERS': 'set by packages/harness/scripts/certify.mjs for its own child',
    'PROBE_REPORT': 'set by scripts/dependency-audit.test.mjs for its own child',
    'PROBE_EXIT': 'set by scripts/dependency-audit.test.mjs for its own child',
    'RECEIPT_TEST_DESCENDANT_IDENTITY': 'set by scripts/gateway-receipt-process.test.mjs for its own child',
    'STAND_IN_FIXTURE': 'set by packages/harness/certification/recorder.test.ts for its own child',
    'JPACK_COMMIT': 'set by packages/harness/certification/recorder.test.ts for its own child',
    'JP_AGENT_GATEWAY_OUTPUT': 'set by scripts/gateway-receipt-check.mjs for the pinned Gateway producer test',
    'JP_AGENT_GATEWAY_PREPARED': 'set by scripts/gateway-receipt-check.mjs for the pinned Gateway producer test',
}
# Flags the toolchains themselves read, bound though no source names them.
TOOLCHAIN_FLAGS = {'GOFLAGS', 'GOEXPERIMENT', 'GORACE', 'GODEBUG', 'CGO_ENABLED', 'GOTOOLCHAIN', 'GOPROXY',
                   'GOSUMDB', 'NODE_OPTIONS', 'NODE_ENV', 'TZ'}
# The event, the ref, the commit and the run, as GitHub names them, and the Checks step's own
# variables derived from them: no check source may read any of them.
EVENT_VARIABLES = {'EVENT', 'BASE_REF', 'COVERED', 'GITHUB_EVENT_NAME', 'GITHUB_EVENT_PATH', 'GITHUB_REF',
                   'GITHUB_REF_NAME', 'GITHUB_REF_TYPE', 'GITHUB_HEAD_REF', 'GITHUB_BASE_REF', 'GITHUB_SHA',
                   'GITHUB_WORKFLOW_REF', 'GITHUB_WORKFLOW_SHA', 'GITHUB_RUN_ID', 'GITHUB_RUN_ATTEMPT',
                   'GITHUB_RUN_NUMBER', 'GITHUB_JOB'}
# The GitHub names as bare words, whatever reads them (a computed key, a template), and the
# workflow expression contexts.
EVENT_WORDS = re.compile(r'\b(%s)\b|\bgithub\.(event|ref|sha|head_ref|base_ref|run_id|workflow_ref)\b' % '|'.join(
    sorted(name for name in EVENT_VARIABLES if name.startswith('GITHUB_'))))


def check_sources():
    """(path, text) of every tracked source a check may run, and of dev.json's commands."""
    listed = subprocess.run(['git', 'ls-files', '-z', '--', 'scripts', 'tests', 'packages', 'go'], cwd=ROOT,
                            check=True, stdout=subprocess.PIPE, stdin=subprocess.DEVNULL).stdout.decode()
    for path in sorted(listed.split('\0')):
        fixture = path.startswith('tests/fixtures/') and path.endswith(('.txt', '.mjs', '.go', '.sh'))
        if path and path not in SOURCE_EXEMPT and (SOURCE_SUFFIX.search(path) or fixture):
            yield path, (ROOT / path).read_text(encoding='utf-8', errors='replace')
    manifest = json.loads(read('dev.json'))
    rows = manifest['setup'] + manifest['checks'] + ([manifest['parity']] if 'parity' in manifest else [])
    yield 'dev.json', '\n'.join(row['run'] for row in rows)


def env_reads(path, text):
    """The names of the environment variables the text reads."""
    names = set()
    for pattern in ENV_READ:
        names.update(next(group for group in match.groups() if group) for match in pattern.finditer(text))
    if path.endswith('.sh') or path == 'dev.json' or path.startswith('tests/fixtures/'):
        names.update(SHELL_READ.findall(text))
    return names


def job_block(workflow, job_id):
    """The text of one job of a workflow file: from its key to the next job's key."""
    begin = workflow.index('\n  %s:\n' % job_id) + 1
    following = re.search(r'\n  [A-Za-z0-9_-]+:\n', workflow[begin:])
    return workflow[begin:begin + following.start() + 1] if following else workflow[begin:]


def step_block(job, name):
    """The text of the step named `name` of a job."""
    begin = job.index('      - name: %s\n' % name)
    following = job.find('\n      - ', begin + 1)
    return job[begin:following + 1] if following != -1 else job[begin:]


class VendoredCopies(unittest.TestCase):
    def test_each_vendored_script_is_the_kit_release_copy(self):
        for path, (source, expected) in VENDORED.items():
            with self.subTest(path=path):
                data = (ROOT / path).read_bytes()
                self.assertEqual(data.decode('utf-8').splitlines()[1],
                                 '# from judgment-pack-devkit %s (%s)' % (KIT, source))
                self.assertEqual(hashlib.sha256(data).hexdigest(), expected)

    def test_the_runner_identity_manifest_is_the_shared_revision_the_configuration_pins(self):
        data = (ROOT / MANIFEST).read_bytes()
        self.assertEqual(hashlib.sha256(data).hexdigest(), MANIFEST_SHA)
        self.assertEqual(config()['identity'], {'manifest': MANIFEST, 'sha256': MANIFEST_SHA})
        self.assertEqual(g.IDENTITY_REVISIONS[json.loads(data)['revision']], MANIFEST_SHA)
        self.assertEqual(json.loads(data)['identity'], 'reported')

    def test_the_retired_verifier_is_gone_and_nothing_runs_it(self):
        self.assertFalse((ROOT / 'scripts/dev/ci_evidence.py').exists())
        for workflow in sorted((ROOT / '.github/workflows').glob('*.yml')):
            with self.subTest(workflow=workflow.name):
                self.assertNotIn('ci_evidence.py', workflow.read_text(encoding='utf-8'))


class Configuration(unittest.TestCase):
    def test_the_gate_accepts_the_configuration(self):
        g.check_config(config())

    def test_every_profile_is_a_linux_x64_runner_bound_to_the_release_runner(self):
        current = config()
        for name, profile in current['profiles'].items():
            with self.subTest(profile=name):
                self.assertEqual(profile['variant'], 'linux-x64')
                self.assertEqual(profile['platform'], {'RUNNER_OS': 'Linux', 'RUNNER_ARCH': 'X64',
                                                       'ImageOS': 'ubuntu26'})
                self.assertEqual(profile['compare'], 'release-runner')
                self.assertTrue(profile['tools'])
        # A rolled image is a refusal and the fresh route, never a revalidation; the release
        # workflow has no dispatch trigger and admits no prerelease tag.
        self.assertNotIn('revalidation', current)
        self.assertNotIn('dispatch', current)
        self.assertNotIn('tag', current)
        self.assertEqual((current['branch'], current['max_age_hours']), ('main', 48))

    def test_the_check_list_is_divided_between_the_ci_jobs_each_check_once(self):
        jobs = config()['workflows']['ci']['jobs']
        self.assertEqual(sorted(jobs), ['check', 'contract', 'go-race'])
        self.assertEqual(jobs['contract']['checks'], CONTRACT_CHECKS)
        self.assertEqual(jobs['go-race']['checks'], ['go-race'])
        self.assertEqual(sorted(jobs['contract']['checks'] + jobs['check']['checks'] + jobs['go-race']['checks']),
                         sorted(manifest_checks()))
        self.assertTrue(jobs['check']['setup'])
        self.assertTrue(jobs['go-race']['setup'])
        self.assertEqual(jobs['go-race']['profile'], 'check')
        self.assertEqual(jobs['check']['optional_skips'], ['dependency-audit-live'])
        self.assertNotIn('optional_skips', jobs['go-race'])
        mutation = config()['workflows']['mutation']
        self.assertEqual(sorted(mutation['jobs']), ['full-1', 'full-2', 'full-3', 'full-4'])
        for key, spec in mutation['jobs'].items():
            with self.subTest(job=key):
                self.assertEqual(spec['checks'], [])
                self.assertEqual(spec['name'], 'every row (shard %s of 4)' % key[-1])
        self.assertEqual(mutation['skipped_jobs'], ['rows of the files this pull request changes, and a sample'])
        self.assertNotIn('pull_request_reuse', mutation)

    def test_every_check_of_the_planning_jobs_is_either_reusable_or_fresh_on_main(self):
        workflow = config()['workflows']['ci']
        reusable = workflow['pull_request_reuse']['checks']
        checks = [name for job in PLANNING_JOBS for name in workflow['jobs'][job]['checks']]
        self.assertEqual(sorted(reusable + list(FRESH_ON_MAIN)), sorted(checks))
        self.assertIn('go-race', reusable)
        self.assertFalse(set(reusable) & set(FRESH_ON_MAIN))
        self.assertFalse(set(reusable) & set(CONTRACT_CHECKS))

    def test_the_bound_environment_is_what_every_ci_check_step_binds(self):
        names = config()['workflows']['ci']['pull_request_reuse']['environment']
        ci = read('.github/workflows/ci.yml')
        bound = re.findall(r'^  JPDEV_BIND_ENV: (\S+)$', ci, re.M)
        self.assertEqual(bound, [','.join(names)])
        # Workflow-level, so that no job or step sets it otherwise.
        self.assertEqual(ci.count('JPDEV_BIND_ENV:'), 1)
        self.assertIn('CI', names)
        self.assertFalse({'EVENT', 'BASE_REF', 'COVERED', 'TMPDIR', 'JPDEV_SCRATCH'} & set(names))

    def test_the_bound_environment_is_every_flag_the_check_sources_read(self):
        """Every variable a check's source reads is bound, or is a named exemption; the toolchain
        flags are bound whether or not a source names them."""
        names = config()['workflows']['ci']['pull_request_reuse']['environment']
        reads = {}
        for path, text in check_sources():
            if path in RUNNER_SCRIPTS or PYTHON_TESTS.fullmatch(path):
                continue
            for name in env_reads(path, text):
                reads.setdefault(name, set()).add(path)
        unbound = {name: sorted(paths) for name, paths in reads.items()
                   if name not in names and name not in ENV_EXEMPT}
        self.assertEqual(unbound, {})
        self.assertEqual(sorted(names), sorted((set(reads) - set(ENV_EXEMPT)) | TOOLCHAIN_FLAGS))
        # An exemption is for a variable a source still reads, never a placeholder.
        self.assertEqual(sorted(set(ENV_EXEMPT) - set(reads)), [])

    def test_ci_sets_no_bound_variable_but_the_two_pinned_checkouts(self):
        """A bound flag that the workflow set from the event or a run value would differ between the
        pull request's run and main's and refuse reuse; none is set but the two checkout paths, which
        are the same on every hosted run."""
        names = config()['workflows']['ci']['pull_request_reuse']['environment']
        ci = read('.github/workflows/ci.yml')
        ci = ci.replace(re.search(r'^  JPDEV_BIND_ENV: \S+$', ci, re.M).group(0), '')
        settings = re.findall(r'^.*\b(?:%s)\b\s*[:=].*$' % '|'.join(names), ci, re.M)
        self.assertEqual([line.strip() for line in settings], [
            'run: echo "JPDEV_DESK=${GITHUB_WORKSPACE}/desk-checkout" >> "$GITHUB_ENV"',
            'run: echo "JP_AGENT_GATEWAY_SOURCE=${GITHUB_WORKSPACE}/gateway-checkout" >> "$GITHUB_ENV"'])

    def test_no_check_reads_the_event_or_the_ref(self):
        """A reusable check must not branch on the event or on which commit or ref it runs at: no
        tracked source a check runs names the event, the ref, the commit or the run, in any
        spelling, nor reads the variables the Checks step sets from them."""
        found = []
        for path, text in check_sources():
            found += ['%s: %s' % (path, match.group(0)) for match in EVENT_WORDS.finditer(text)]
            found += ['%s: reads %s' % (path, name) for name in env_reads(path, text) if name in EVENT_VARIABLES]
        self.assertEqual(found, [])


class Workflows(unittest.TestCase):
    def test_ci_captures_every_configured_job_under_the_name_verify_reads(self):
        current = config()
        for key, workflow in current['workflows'].items():
            text = read(workflow['path'])
            self.assertTrue(text.startswith('name: %s\n' % workflow['name']), key)
            for job_key, spec in workflow['jobs'].items():
                with self.subTest(workflow=key, job=job_key):
                    job = job_block(text, spec['job_id'])
                    self.assertIn('    name: ', job)
                    self.assertIn('    runs-on: %s\n' % spec['labels'][0], job)
                    capture = 'python3 scripts/dev/release_evidence.py capture --workflow %s --job %s ' % (key, job_key)
                    if spec['job_id'] == 'full':
                        # One matrix job: the shard's leg names its key and artifact from the matrix.
                        capture = 'python3 scripts/dev/release_evidence.py capture --workflow mutation --job full-${{ matrix.shard }} '
                        name = 'name: ci-evidence-mutation-full-${{ matrix.shard }}-${{ github.run_attempt }}'
                    else:
                        name = 'name: ci-evidence-%s-%s-${{ github.run_attempt }}' % (key, job_key)
                    self.assertIn(capture, job)
                    self.assertIn(name, job)
        mutation = read('.github/workflows/mutation.yml')
        self.assertIn('        shard: [1, 2, 3, 4]\n', mutation)

    def test_main_evidence_is_recorded_on_push_or_dispatch_and_never_on_a_schedule(self):
        mutation = read('.github/workflows/mutation.yml')
        main = "github.ref == 'refs/heads/main' && (github.event_name == 'push' || github.event_name == 'workflow_dispatch')"
        conditions = re.findall(r'^        if: (.*)$', job_block(mutation, 'full'), re.M)
        self.assertEqual(conditions, [main, main])
        self.assertIn('  schedule:\n', mutation)
        # The duplicate rows-applicability job is gone: dev.json's mutation-rows step is the one run.
        self.assertNotIn('rows-apply', mutation)
        self.assertNotIn('mutation-check.mjs --check', mutation)
        self.assertIn('mutation-rows', config()['workflows']['ci']['jobs']['check']['checks'])

    def test_the_four_full_legs_partition_the_table(self):
        """Each leg runs its own quarter: four legs, shard N of 4, and every key of the configuration."""
        full = job_block(read('.github/workflows/mutation.yml'), 'full')
        self.assertIn('    strategy:\n      fail-fast: false\n      matrix:\n        shard: [1, 2, 3, 4]\n', full)
        self.assertIn('    name: every row (shard ${{ matrix.shard }} of 4)\n', full)
        self.assertIn('          SHARD: ${{ matrix.shard }}\n', full)
        self.assertEqual(re.findall(r'mutation-check\.mjs[^\n]*', full), ['mutation-check.mjs --shard "$SHARD/4"'])
        self.assertEqual(sorted(config()['workflows']['mutation']['jobs']), ['full-%d' % n for n in (1, 2, 3, 4)])

    def test_each_planning_job_plans_before_its_checks_and_records_the_plan(self):
        for name in PLANNING_JOBS:
            with self.subTest(job=name):
                job = job_block(read('.github/workflows/ci.yml'), name)
                main = "github.ref == 'refs/heads/main' && github.event_name != 'pull_request'"
                plan = step_block(job, 'Plan reuse from the merged pull request')
                self.assertIn('        id: plan\n        if: %s\n' % main, plan)
                self.assertIn('run: python3 scripts/dev/release_evidence.py reuse --workflow ci --job %s '
                              '--output "${{ runner.temp }}/reuse-plan.json"' % name, plan)
                checks = step_block(job, 'Checks')
                self.assertIn('          COVERED: ${{ steps.plan.outputs.covered }}\n', checks)
                record = step_block(job, 'Record exact CI inputs and coverage')
                self.assertIn('        if: %s\n' % main, record)
                self.assertIn('--reuse-plan "${{ runner.temp }}/reuse-plan.json"', record)
                order = [job.index('- name: Setup steps'), job.index('- name: Plan reuse'), job.index('- name: Checks'),
                         job.index('- name: Record exact CI inputs')]
                self.assertEqual(order, sorted(order))
                # The plan reads the API; the job writes nothing through it.
                self.assertIn('    permissions:\n      contents: read\n      actions: read\n      pull-requests: read\n'
                              '    steps:\n', job)
                self.assertNotIn(': write', job)

    def test_the_checks_step_runs_the_rest_of_the_list_once(self):
        ci = read('.github/workflows/ci.yml')
        contract = job_block(ci, 'contract')
        self.assertIn('scripts/dev/check.sh --manifest dev.json --all --only %s |' % ','.join(CONTRACT_CHECKS),
                      contract)
        checks = step_block(job_block(ci, 'check'), 'Checks')
        self.assertIn('          skip=%s\n' % ','.join(CONTRACT_CHECKS + ['go-race']), checks)
        self.assertIn('          if [ -n "$COVERED" ]; then skip="$skip,$COVERED"; fi\n', checks)
        invocations = re.findall(r'scripts/dev/check\.sh [^|]*', checks)
        self.assertEqual(invocations, ['scripts/dev/check.sh --all --changed "origin/$BASE_REF" --skip "$skip" ',
                                       'scripts/dev/check.sh --all --skip "$skip" '])

    def go_race_others(self, manifest):
        """What the go-race job's own step computes as every other step, from the workflow's text."""
        step = step_block(job_block(read('.github/workflows/ci.yml'), 'go-race'), 'Read the toolchains from dev.json')
        source = step[step.index("<<'PY'\n") + len("<<'PY'\n"):step.index('          PY\n')]
        source = '\n'.join(line[10:] for line in source.splitlines())
        with tempfile.TemporaryDirectory(prefix='go-race-others-') as room:
            Path(room, 'dev.json').write_text(json.dumps(manifest), encoding='utf-8')
            out = subprocess.run([shutil.which('python3'), '-I', '-c', source], cwd=room, check=True,
                                 stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, text=True).stdout
        return dict(line.split('=', 1) for line in out.splitlines())

    def test_the_go_race_job_skips_every_other_step_of_the_manifest(self):
        """The job selects by skipping (check.sh refuses --changed with --only), so the list is
        derived from the manifest: a step added later is not run here, and go-race is never skipped
        but for what the plan covered."""
        manifest = json.loads(read('dev.json'))
        others = self.go_race_others(manifest)['others'].split(',')
        self.assertEqual(sorted(others + ['go-race']), sorted(manifest_checks()))
        manifest['checks'].append({'name': 'added-later', 'run': 'true'})
        self.assertIn('added-later', self.go_race_others(manifest)['others'].split(','))
        checks = step_block(job_block(read('.github/workflows/ci.yml'), 'go-race'), 'Checks')
        self.assertIn('          skip="$OTHERS"\n', checks)
        self.assertIn('          OTHERS: ${{ steps.dev.outputs.others }}\n', checks)

    def test_the_go_race_step_is_the_same_command_on_its_own_runner(self):
        """The step moved, not changed: dev.json's command under -race over the same packages, and
        every path that selected it. The job needs neither the Desk nor the Gateway checkout, and its
        limit leaves the step its own."""
        row = next(row for row in json.loads(read('dev.json'))['checks'] if row['name'] == 'go-race')
        self.assertRegex(row['run'], r'^go test -count=1 -race -timeout=\d+m \./context/\.\.\. \./adapter/\.\.\.$')
        self.assertEqual(row['cwd'], 'go')
        mapped = {path for rule in json.loads(read('jpdev.json'))['changed'] if 'go-race' in rule['checks']
                  for path in rule['paths']}
        self.assertEqual(sorted(mapped), ['contract/**', 'corpus/**', 'go/**', 'packages/harness/**'])
        job = job_block(read('.github/workflows/ci.yml'), 'go-race')
        self.assertNotIn('judgment-pack-desk', job)
        self.assertNotIn('judgment-pack-gateway', job)
        self.assertIn('    timeout-minutes: 60\n', job)
        self.assertGreater(60 * 60, row['timeout'])

    def test_the_go_race_job_prints_each_binary_and_warns_before_its_limit(self):
        """The summary keeps one line of the step's output, so the job prints what each binary took,
        on a failure too, and warns past three quarters of the 40-minute limit."""
        job = job_block(read('.github/workflows/ci.yml'), 'go-race')
        self.assertIn('          JPDEV_LOGDIR: ${{ runner.temp }}/logs\n', step_block(job, 'Checks'))
        step = step_block(job, "Each test binary's time")
        self.assertIn('        if: ${{ !cancelled() }}\n', step)
        self.assertIn('          LOG: ${{ runner.temp }}/logs/go-race.log\n', step)
        script = '\n'.join(line[10:] for line in step[step.index('        run: |\n') + len('        run: |\n'):].splitlines())
        with tempfile.TemporaryDirectory(prefix='go-race-times-') as room:
            log = Path(room, 'go-race.log')
            log.write_text('=== noise\nok  \tmodule/quick\t12.5s\nok  \tmodule/near\t1800.1s\nok  \tmodule/under\t1800.0s\n'
                           'panic: test timed out after 40m0s\nFAIL\tmodule/slow\t2400.1s\n', encoding='utf-8')
            done = subprocess.run(['bash', '-c', script], env={'LOG': str(log), 'PATH': os.environ['PATH']},
                                  stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, text=True, timeout=30)
            self.assertEqual(done.returncode, 0)
            self.assertEqual(done.stdout.splitlines(), [
                'ok  \tmodule/quick\t12.5s', 'ok  \tmodule/near\t1800.1s', 'ok  \tmodule/under\t1800.0s',
                'panic: test timed out after 40m0s', 'FAIL\tmodule/slow\t2400.1s',
                '::warning::module/near took 1800.1s, past three quarters of its 40-minute limit'])
            # A main run that reused the step wrote no log; that is not a failure.
            done = subprocess.run(['bash', '-c', script], env={'LOG': str(log) + '.absent', 'PATH': os.environ['PATH']},
                                  stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, text=True, timeout=30)
            self.assertEqual((done.returncode, done.stdout), (0, ''))

    def checks_step(self, room, job, event, covered):
        """The Checks step of a job as the workflow writes it, run in a fixture repository whose
        manifest's steps only say their names."""
        ci = read('.github/workflows/ci.yml')
        step = step_block(job_block(ci, job), 'Checks')
        script = step[step.index('        run: |\n') + len('        run: |\n'):]
        log = room.parent / 'checks.log'
        script = '\n'.join(line[10:] for line in script.splitlines()).replace('${{ runner.temp }}/checks.log', str(log))
        manifest = json.loads((room / 'dev.json').read_text(encoding='utf-8'))
        others = ','.join(row['name'] for row in manifest['checks'] if row['name'] != 'go-race')
        env = {'PATH': os.environ['PATH'], 'HOME': room.as_posix(), 'EVENT': event, 'BASE_REF': 'main', 'COVERED': covered,
               'OTHERS': others, 'GIT_CONFIG_GLOBAL': os.devnull, 'GIT_CONFIG_NOSYSTEM': '1', 'JPDEV_BIND_ENV': ''}
        done = subprocess.run(['bash', '-c', script], cwd=room, env=env, stdin=subprocess.DEVNULL,
                              stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, timeout=60)
        self.assertEqual(done.returncode, 0, done.stdout)
        return re.findall(r'^PASS\s+(\S+)', log.read_text(encoding='utf-8'), re.M)

    def test_the_two_jobs_divide_a_selection_and_main_skips_what_the_plan_covered(self):
        """The workflow's own Checks steps over the vendored check.sh and its selection: every
        selected check runs in exactly one job, and a covered check in neither."""
        with tempfile.TemporaryDirectory(prefix='ci-division-') as name:
            room = Path(name, 'repository')
            room.mkdir()
            for path in ('scripts/dev/check.sh', 'scripts/dev/changed_checks.py'):
                (room / path).parent.mkdir(parents=True, exist_ok=True)
                shutil.copy(ROOT / path, room / path)
            names = CONTRACT_CHECKS + ['go-test', 'go-race', 'unit-tests']
            (room / 'dev.json').write_text(json.dumps({'version': 1, 'checks': [
                {'name': name, 'run': 'echo %s' % name, 'toolchain': 'shell'} for name in names]}), encoding='utf-8')
            (room / 'jpdev.json').write_text(json.dumps({'version': 1, 'changed': [
                {'paths': ['go/**'], 'checks': ['go-test', 'go-race']},
                {'paths': ['docs/**'], 'checks': ['unit-tests']}]}), encoding='utf-8')
            env = dict(os.environ, GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_NOSYSTEM='1',
                       GIT_AUTHOR_NAME='Fixture', GIT_AUTHOR_EMAIL='fixture@example.invalid',
                       GIT_COMMITTER_NAME='Fixture', GIT_COMMITTER_EMAIL='fixture@example.invalid')

            def git_in_room(*args):
                subprocess.run(('git',) + args, cwd=room, check=True, env=env, stdin=subprocess.DEVNULL,
                               stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

            git_in_room('init', '-q', '-b', 'main')
            git_in_room('add', '.')
            git_in_room('commit', '-qm', 'base')
            git_in_room('update-ref', 'refs/remotes/origin/main', 'HEAD')
            for changed, check_part, race_part in (('go/a.go', ['go-test'], ['go-race']), ('docs/a.md', ['unit-tests'], [])):
                with self.subTest(changed=changed):
                    git_in_room('checkout', '-q', '-B', 'work', 'origin/main')
                    (room / changed).parent.mkdir(parents=True, exist_ok=True)
                    (room / changed).write_text('a change\n', encoding='utf-8')
                    git_in_room('add', '.')
                    git_in_room('commit', '-qm', 'work')
                    self.assertEqual(self.checks_step(room, 'check', 'pull_request', ''), check_part)
                    self.assertEqual(self.checks_step(room, 'go-race', 'pull_request', ''), race_part)
            self.assertEqual(self.checks_step(room, 'check', 'push', ''), ['go-test', 'unit-tests'])
            self.assertEqual(self.checks_step(room, 'go-race', 'push', ''), ['go-race'])
            self.assertEqual(self.checks_step(room, 'check', 'push', 'go-test'), ['unit-tests'])
            self.assertEqual(self.checks_step(room, 'go-race', 'push', 'go-race'), [])

    def test_a_pull_request_into_main_records_its_checks_for_reuse(self):
        for name in PLANNING_JOBS:
            with self.subTest(job=name):
                job = job_block(read('.github/workflows/ci.yml'), name)
                pr = ("github.event_name == 'pull_request' && github.base_ref == 'main' && "
                      "github.event.pull_request.head.repo.full_name == github.repository")
                record = step_block(job, "Record the pull request's checks")
                self.assertIn('        if: %s\n' % pr, record)
                self.assertIn('python3 scripts/dev/release_evidence.py capture --workflow ci --job %s --check-log' % name,
                              record)
                self.assertNotIn('--reuse-plan', record)
                upload = step_block(job, 'Upload immutable CI evidence')
                self.assertIn("        if: (github.ref == 'refs/heads/main' && github.event_name != 'pull_request') || (%s)\n"
                              % pr, upload)
                # The merge commit is checked out with its parents.
                self.assertIn('          fetch-depth: 0\n', job)
        # The contract job records no pull request evidence: it plans no reuse.
        contract = job_block(read('.github/workflows/ci.yml'), 'contract')
        self.assertNotIn("github.event_name == 'pull_request'", contract)
        self.assertNotIn('release_evidence.py reuse', contract)

    def test_the_audit_workflow_runs_only_the_live_query(self):
        audit = read('.github/workflows/dependency-audit.yml')
        self.assertEqual(re.findall(r'scripts/dev/check\.sh .*', audit), ['scripts/dev/check.sh --only dependency-audit-live'])
        self.assertIn("          DEPENDENCY_AUDIT_LIVE: '1'\n", audit)
        self.assertIn('    name: dependency-audit\n', audit)

    def test_the_release_takes_the_gate_decision_and_nothing_else(self):
        release = read('.github/workflows/release.yml')
        verify = job_block(release, 'verify')
        self.assertIn('    name: CI at the tag\n', verify)
        self.assertIn('    permissions:\n      contents: read\n      actions: read\n', verify)
        self.assertIn('      pull-requests: read\n', verify)
        self.assertIn('      decision: ${{ steps.gate.outputs.decision }}\n', verify)
        gate = step_block(verify, 'Reuse exact-commit main CI evidence, or refuse')
        self.assertIn('        id: gate\n', gate)
        self.assertIn('run: python3 scripts/dev/release_evidence.py verify --output "${{ runner.temp }}/release-evidence.json"',
                      gate)
        self.assertIn('      - name: Keep the decision record\n        if: ${{ !cancelled() }}\n', verify)
        self.assertNotIn('revalidate', release)
        self.assertNotIn('continue-on-error', release)
        self.assertNotIn('uses: ./.github/workflows/ci.yml', release)
        package = job_block(release, 'package')
        self.assertIn('      - admit\n      - verify\n', package)
        steps = package[package.index('    steps:\n'):]
        first = step_block(steps, 'Require a publication decision from the gate')
        self.assertTrue(steps[len('    steps:\n'):].lstrip(' #').startswith('Only a publication decision'), steps[:200])
        self.assertIn('          DECISION: ${{ needs.verify.outputs.decision }}\n', first)
        script = first[first.index('        run: |\n') + len('        run: |\n'):]
        script = '\n'.join(line[10:] for line in script.splitlines())
        for decision, status in [('reuse', 0), ('refuse', 1), ('revalidate', 1), ('validated', 1), ('', 1),
                                 ('reuse ', 1), (' reuse', 1)]:
            with self.subTest(decision=decision):
                outcome = subprocess.run(['bash', '-c', script], env={'DECISION': decision, 'PATH': os.environ['PATH']},
                                         stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                         timeout=30)
                self.assertEqual(outcome.returncode, status, outcome.stderr)

    def test_producers_and_the_consumer_use_the_same_action_pins(self):
        pins = {}
        for name in ('ci.yml', 'mutation.yml', 'release.yml', 'dependency-audit.yml'):
            for action, sha in re.findall(r'uses: ([a-z-]+/[a-z-]+)@([0-9a-f]{40})', read('.github/workflows/' + name)):
                pins.setdefault(action, set()).add(sha)
        for action, shas in sorted(pins.items()):
            with self.subTest(action=action):
                self.assertEqual(len(shas), 1, shas)
        self.assertIn('actions/setup-python', pins)


def git_fixture(root, paths):
    """A repository at root holding the given files of this checkout, as they are now, committed."""
    env = dict(os.environ, GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_NOSYSTEM='1')
    for name in paths:
        target = root / name
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(ROOT / name, target)
    for argv in (['git', 'init', '-q', '-b', 'main'], ['git', 'add', '.'],
                 ['git', '-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', 'commit', '-qm', 'fixture']):
        subprocess.run(argv, cwd=root, check=True, env=env, stdin=subprocess.DEVNULL,
                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    return subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=root, text=True, stdin=subprocess.DEVNULL).strip()


FIXTURE_FILES = ['.github/ci-evidence.json', MANIFEST, '.github/workflows/ci.yml', '.github/workflows/mutation.yml',
                 '.github/workflows/release.yml', 'dev.json']
REPO = 'Judgment-Pack/judgment-pack-agent'
REPO_ID = 1424951648
TAG = 'v0.13.0'
TAG_OBJECT = 't' * 40
NOW = dt.datetime(2026, 10, 30, 12, tzinfo=dt.timezone.utc)
RUNNER = {'ImageOS': 'ubuntu26', 'ImageVersion': '20261026.180.1', 'RUNNER_OS': 'Linux', 'RUNNER_ARCH': 'X64',
          'os_release': 'c' * 64}
TOOLS = {'base': {'python': 'Python 3.12.15'},
         'check': {'node': 'v22.23.4', 'npm': '10.9.9', 'go': 'go version go1.26.10 linux/amd64',
                   'python': 'Python 3.12.15', 'python-packages': '[["pip", "25.0.1"]]'},
         'mutation': {'node': 'v22.23.4', 'npm': '10.9.9'}}
ENVIRONMENT_VALUES = {'CI': 'true', 'JPDEV_DESK': 'desk-checkout', 'JP_AGENT_GATEWAY_SOURCE': 'gateway-checkout'}
# The go-race job makes neither checkout, so it runs without those two (unset, on both sides of a reuse).
JOB_ENVIRONMENT = {'check': ENVIRONMENT_VALUES, 'go-race': {'CI': 'true'}}
BASE = 'e' * 40
HEAD = 'b' * 40
MERGE = 'd' * 40
TREE = '7' * 40
HOSTED = {'runner_group_id': 0, 'runner_group_name': 'GitHub Actions'}


def environment_record(names, job='check'):
    values = JOB_ENVIRONMENT[job]
    return {name: hashlib.sha256(values[name].encode()).hexdigest() if name in values else None for name in names}


def profile_of(profile):
    """The profile name of a profile object, by its tool set."""
    return next(name for name, tools in TOOLS.items() if set(tools) == set(profile['tools']))


def pack(data, artifact, payload):
    output = io.BytesIO()
    with zipfile.ZipFile(output, 'w') as archive:
        archive.writestr('evidence.json', json.dumps(payload))
    raw = output.getvalue()
    data['actions/artifacts/%d/zip' % artifact['id']] = raw
    artifact.update(size_in_bytes=len(raw), digest='sha256:' + hashlib.sha256(raw).hexdigest())


class FakeAPI(g.API):
    def __init__(self, responses):
        super().__init__(REPO)
        self.responses = responses

    def get(self, suffix, binary=False):
        if suffix not in self.responses:
            raise g.Refusal('api-unavailable', 'no fixture for ' + suffix)
        return copy.deepcopy(self.responses[suffix])


class AtCommit(unittest.TestCase):
    """A fixture repository holding this checkout's configuration, manifest, workflows and dev.json."""

    def setUp(self):
        directory = tempfile.TemporaryDirectory(prefix='release-evidence-fixture-')
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name)
        self.sha = git_fixture(self.root, FIXTURE_FILES)
        previous = os.getcwd()
        os.chdir(self.root)
        self.addCleanup(os.chdir, previous)
        self.config = config()
        self.context = g.identity_context(self.config, self.sha)


class IdentityAtCommit(AtCommit):
    def test_every_workflow_action_is_pinned_under_the_policy(self):
        for key, actions in self.context['actions'].items():
            with self.subTest(workflow=key):
                self.assertTrue(actions)
                for action in actions:
                    self.assertRegex(action, r'^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+@[0-9a-f]{40}$')
        with patch.dict(os.environ, GITHUB_WORKFLOW_REF='%s/.github/workflows/release.yml@refs/tags/%s' % (REPO, TAG)):
            release = g.release_workflow_actions({'repository': REPO, 'source': self.sha})
        self.assertEqual(release['path'], '.github/workflows/release.yml')
        self.assertTrue(release['actions'])

    def test_a_workflow_action_named_by_a_tag_refuses(self):
        ci = (self.root / '.github/workflows/ci.yml').read_text()
        pinned = re.search(r'actions/checkout@[0-9a-f]{40}', ci).group(0)
        (self.root / '.github/workflows/ci.yml').write_text(ci.replace(pinned, 'actions/checkout@v7', 1))
        env = dict(os.environ, GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_NOSYSTEM='1')
        subprocess.run(['git', '-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', 'commit',
                        '-qam', 'tag'], cwd=self.root, check=True, env=env, stdin=subprocess.DEVNULL)
        sha = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=self.root, text=True).strip()
        with self.assertRaises(g.Refusal) as caught:
            g.identity_context(self.config, sha)
        self.assertEqual(caught.exception.code, 'identity-unexpected')


class AgentWorld(AtCommit):
    """decide() over the agent's own configuration and workflow files at a fixture commit, with the
    release runner's resolution fixed: one push run of CI and one of the mutation table, every job."""

    def setUp(self):
        super().setUp()
        self.attempt = 1
        self.here = {name: {'runner': dict(RUNNER), 'tools': dict(tools)} for name, tools in TOOLS.items()}
        resolve = patch.object(g, 'runtime', side_effect=lambda profile, variant=None:
                               copy.deepcopy(self.here[profile_of(profile)]))
        resolve.start()
        self.addCleanup(resolve.stop)
        self.inputs = {path: '1' * 40 for path in self.config['inputs']}
        self.source = {'schema': 2, 'repository': REPO, 'repository_id': REPO_ID, 'source': self.sha,
                       'inputs': self.inputs}
        self.data = self.world()
        workflow_ref = '%s/.github/workflows/release.yml@refs/tags/%s' % (REPO, TAG)
        env = patch.dict(os.environ, GITHUB_WORKFLOW_REF=workflow_ref)
        env.start()
        self.addCleanup(env.stop)

    # --- the fixture world ---------------------------------------------------------------------

    def coverage(self, job_key):
        spec = self.config['workflows'][job_key[0]]['jobs'][job_key[1]]
        result = {name: 'PASS' for name in g.required_checks(spec)}
        if 'dependency-audit-live' in result:
            result['dependency-audit-live'] = 'SKIP'
        return result

    def payload(self, key, job_key, run_id, coverage, extra=None, attempt=1):
        workflow = self.config['workflows'][key]
        spec = workflow['jobs'][job_key]
        profile = self.config['profiles'][spec['profile']]
        if 'pull_request_reuse' in workflow and extra is None:
            extra = {'environment': environment_record(workflow['pull_request_reuse']['environment'],
                                                       job_key if job_key in JOB_ENVIRONMENT else 'check'), 'reused': None}
        return g.evidence(self.source, key, workflow, job_key, run_id, attempt, 'push', 'main',
                          {'runner': dict(RUNNER), 'tools': dict(TOOLS[spec['profile']])}, coverage,
                          g.job_identity(self.context, profile, key), None, extra)

    def world(self):
        data = {'': {'id': REPO_ID, 'full_name': REPO, 'default_branch': 'main'},
                'git/ref/tags/' + TAG: {'ref': 'refs/tags/' + TAG, 'object': {'type': 'tag', 'sha': TAG_OBJECT}},
                'git/tags/' + TAG_OBJECT: {'object': {'type': 'commit', 'sha': self.sha}}}
        for index, (key, workflow) in enumerate(self.config['workflows'].items()):
            workflow_id, run_id = 9 + index, 100 + 10 * index
            attempt = self.attempt if key == 'ci' else 1
            data['actions/workflows/' + Path(workflow['path']).name] = {
                'id': workflow_id, 'name': workflow['name'], 'path': workflow['path'], 'state': 'active'}
            run = {'id': run_id, 'run_attempt': attempt, 'event': 'push', 'head_branch': 'main', 'head_sha': self.sha,
                   'workflow_id': workflow_id, 'path': workflow['path'], 'status': 'completed',
                   'conclusion': 'success', 'pull_requests': [], 'created_at': '2026-10-30T09:00:00Z',
                   'run_started_at': '2026-10-30T09:00:00Z', 'updated_at': '2026-10-30T10:30:00Z',
                   'repository': {'id': REPO_ID, 'full_name': REPO},
                   'head_repository': {'id': REPO_ID, 'full_name': REPO}}
            data['actions/workflows/%d/runs?head_sha=%s&per_page=100&page=1' % (workflow_id, self.sha)] = {
                'total_count': 1, 'workflow_runs': [run]}
            data['actions/runs/%d' % run_id] = run
            jobs, artifacts = [], []
            for number, (job_key, spec) in enumerate(workflow['jobs'].items()):
                job = dict(HOSTED, id=run_id * 10 + number, name=spec['name'], run_id=run_id, run_attempt=attempt,
                           head_sha=self.sha, head_branch='main', workflow_name=workflow['name'],
                           status='completed', conclusion='success', labels=list(spec['labels']),
                           started_at='2026-10-30T09:00:05Z', completed_at='2026-10-30T10:20:00Z')
                jobs.append(job)
                artifact = {'id': run_id * 10 + 500 + number, 'name': 'ci-evidence-%s-%s-%d' % (key, job_key, attempt),
                            'expired': False, 'created_at': '2026-10-30T10:19:00Z',
                            'workflow_run': {'id': run_id, 'head_sha': self.sha, 'head_branch': 'main',
                                             'repository_id': REPO_ID, 'head_repository_id': REPO_ID}}
                artifacts.append(artifact)
                pack(data, artifact, self.payload(key, job_key, run_id, self.coverage((key, job_key)), attempt=attempt))
            for name in workflow.get('skipped_jobs', []):
                jobs.append({'id': run_id * 10 + 99, 'name': name, 'run_id': run_id, 'run_attempt': attempt,
                             'head_sha': self.sha, 'head_branch': 'main', 'workflow_name': workflow['name'],
                             'status': 'completed', 'conclusion': 'skipped', 'labels': ['ubuntu-latest'],
                             'runner_group_id': None, 'runner_group_name': None,
                             'started_at': '2026-10-30T09:00:01Z', 'completed_at': '2026-10-30T09:00:01Z'})
            data['actions/runs/%d/attempts/%d/jobs?per_page=100&page=1' % (run_id, attempt)] = {
                'total_count': len(jobs), 'jobs': jobs}
            data['actions/runs/%d/artifacts?per_page=100&page=1' % run_id] = {
                'total_count': len(artifacts), 'artifacts': artifacts}
        return data

    def artifact(self, key, job_key):
        run_id = 100 + 10 * list(self.config['workflows']).index(key)
        listing = self.data['actions/runs/%d/artifacts?per_page=100&page=1' % run_id]['artifacts']
        attempt = self.attempt if key == 'ci' else 1
        return next(item for item in listing if item['name'] == 'ci-evidence-%s-%s-%d' % (key, job_key, attempt))

    def change_evidence(self, key, job_key, change):
        artifact = self.artifact(key, job_key)
        with zipfile.ZipFile(io.BytesIO(self.data['actions/artifacts/%d/zip' % artifact['id']])) as archive:
            payload = json.loads(archive.read('evidence.json'))
        change(payload)
        pack(self.data, artifact, payload)

    def decide(self):
        record = {'workflows': {}, 'mode': 'publication'}
        g.decide(FakeAPI(self.data), self.config, copy.deepcopy(self.source), TAG, NOW, record)
        self.record = record
        if record['decision'] == 'reuse':
            self.assertIsNone(record['refusal'])
            return 'reuse'
        self.assertEqual(record['decision'], 'refuse')
        self.assertNotIn('revalidation', record)
        return record['refusal']['code']


class AgentGate(AgentWorld):

    def test_complete_evidence_of_both_workflows_is_reused_with_its_reported_identity(self):
        self.assertEqual(self.decide(), 'reuse')
        identity = self.record['identity']
        self.assertEqual(identity['statement'], 'a reported hosted-runner image identity, not a pinned VM image')
        self.assertEqual(sorted(identity['groups']), ['linux-x64/base', 'linux-x64/check', 'linux-x64/mutation'])
        self.assertEqual(identity['groups']['linux-x64/mutation']['jobs'],
                         ['mutation/full-1', 'mutation/full-2', 'mutation/full-3', 'mutation/full-4'])
        self.assertEqual(identity['groups']['linux-x64/check']['jobs'], ['ci/check', 'ci/go-race'])
        self.assertEqual(self.record['workflows']['ci']['jobs']['check']['reused'], None)
        self.assertEqual(self.record['workflows']['ci']['jobs']['go-race']['coverage'],
                         {'go-race': 'PASS', 'setup:npm-ci': 'PASS', 'setup:python-deps': 'PASS'})

    def test_a_release_requires_the_go_race_job(self):
        """The race step is as required as it was inside the check job: without its job, its
        artifact or its result, the release refuses."""
        listing = self.data['actions/runs/100/attempts/1/jobs?per_page=100&page=1']
        listing['jobs'] = [job for job in listing['jobs'] if job['name'] != 'go-race']
        listing['total_count'] = len(listing['jobs'])
        self.assertEqual(self.decide(), 'job-coverage')
        self.data = self.world()
        artifacts = self.data['actions/runs/100/artifacts?per_page=100&page=1']
        artifacts['artifacts'] = [item for item in artifacts['artifacts'] if 'go-race' not in item['name']]
        artifacts['total_count'] = len(artifacts['artifacts'])
        self.assertEqual(self.decide(), 'artifact-coverage')
        self.data = self.world()
        self.change_evidence('ci', 'go-race', lambda payload: payload['coverage'].pop('go-race'))
        self.assertEqual(self.decide(), 'check-coverage')
        self.data = self.world()
        self.change_evidence('ci', 'go-race', lambda payload: payload['coverage'].update({'go-race': 'SKIP'}))
        self.assertEqual(self.decide(), 'check-result')
        self.data = self.world()
        self.change_evidence('ci', 'check', lambda payload: payload['coverage'].update({'go-race': 'PASS'}))
        self.assertEqual(self.decide(), 'check-coverage')

    def test_a_rolled_image_refuses_and_is_never_revalidated(self):
        self.here['check']['runner']['ImageVersion'] = '20261102.200.1'
        self.assertEqual(self.decide(), 'runtime-identity')
        self.assertEqual(self.record['decision'], 'refuse')
        self.assertNotIn('revalidation', self.record)
        self.assertFalse(g.revalidation_allowed(self.config))

    def test_another_image_name_than_the_pin_refuses(self):
        self.change_evidence('mutation', 'full-2', lambda payload: payload['runtime']['runner'].update(ImageOS='ubuntu24'))
        self.assertEqual(self.decide(), 'identity-unexpected')

    def test_evidence_without_the_identity_record_refuses(self):
        self.change_evidence('ci', 'contract', lambda payload: payload.pop('identity'))
        self.assertEqual(self.decide(), 'identity-missing')

    def test_a_check_job_that_ran_the_contract_steps_again_refuses(self):
        self.change_evidence('ci', 'check', lambda payload: payload['coverage'].update(contract='PASS'))
        self.assertEqual(self.decide(), 'check-coverage')

    def test_only_the_live_audit_may_be_skipped(self):
        self.change_evidence('ci', 'check', lambda payload: payload['coverage'].update({'python-tests': 'SKIP'}))
        self.assertEqual(self.decide(), 'check-result')

    def test_a_shard_missing_or_the_pull_request_sample_run_on_main_refuses(self):
        listing = self.data['actions/runs/110/attempts/1/jobs?per_page=100&page=1']
        listing['jobs'] = [job for job in listing['jobs'] if job['name'] != 'every row (shard 3 of 4)']
        listing['total_count'] = len(listing['jobs'])
        self.assertEqual(self.decide(), 'job-coverage')
        self.data = self.world()
        for job in self.data['actions/runs/110/attempts/1/jobs?per_page=100&page=1']['jobs']:
            if job['conclusion'] == 'skipped':
                job['conclusion'] = 'success'
        self.assertEqual(self.decide(), 'job-not-skipped')

    def test_a_newer_failed_scheduled_mutation_run_refuses_and_a_green_one_is_not_evidence(self):
        runs = self.data['actions/workflows/10/runs?head_sha=%s&per_page=100&page=1' % self.sha]
        scheduled = dict(runs['workflow_runs'][0], id=120, event='schedule', run_started_at='2026-10-30T11:00:00Z',
                         created_at='2026-10-30T11:00:00Z', conclusion='failure')
        runs['workflow_runs'].append(scheduled)
        runs['total_count'] = 2
        self.assertEqual(self.decide(), 'run-newer-failed')
        scheduled['conclusion'] = 'success'
        self.assertEqual(self.decide(), 'reuse')
        self.assertEqual(self.record['workflows']['mutation']['run_id'], 110)


class ReuseWorld(AgentWorld):
    """The push run took tree-only checks from the merged pull request's run, each job its own: the
    check job two, and the go-race job its one."""

    COVERED = ['go-test', 'unit-tests']
    RACE_COVERED = ['go-race']
    # The pull request run's job and artifact ids, for each job.
    IDS = {'check': (551, 651), 'go-race': (552, 652)}

    def setUp(self):
        super().setUp()
        self.add_pull_request()

    def add_pull_request(self, covered=None, race_covered=None):
        covered = {'check': covered or self.COVERED, 'go-race': self.RACE_COVERED if race_covered is None else race_covered}
        workflow = self.config['workflows']['ci']
        names = workflow['pull_request_reuse']['environment']
        pull = {'number': 7, 'state': 'closed', 'created_at': '2026-10-30T08:00:00Z',
                'merged_at': '2026-10-30T08:55:00Z',
                'head': {'sha': HEAD, 'ref': 'feature', 'repo': {'id': REPO_ID}},
                'base': {'ref': 'main', 'repo': {'id': REPO_ID}}}
        run = {'id': 50, 'run_attempt': 1, 'event': 'pull_request', 'head_branch': 'feature', 'head_sha': HEAD,
               'workflow_id': 9, 'path': workflow['path'], 'status': 'completed', 'conclusion': 'success',
               'pull_requests': [], 'created_at': '2026-10-30T08:10:00Z', 'run_started_at': '2026-10-30T08:10:00Z',
               'updated_at': '2026-10-30T08:50:00Z', 'repository': {'id': REPO_ID, 'full_name': REPO},
               'head_repository': {'id': REPO_ID, 'full_name': REPO}}
        jobs, artifacts = [], []
        for job_key, (job_id, artifact_id) in self.IDS.items():
            jobs.append(dict(HOSTED, id=job_id, name=workflow['jobs'][job_key]['name'], run_id=50, run_attempt=1,
                             head_sha=HEAD, head_branch='feature', workflow_name='CI', status='completed',
                             conclusion='success', labels=['ubuntu-latest'], started_at='2026-10-30T08:10:05Z',
                             completed_at='2026-10-30T08:49:00Z'))
            artifacts.append({'id': artifact_id, 'name': 'ci-evidence-ci-%s-1' % job_key, 'expired': False,
                              'created_at': '2026-10-30T08:48:00Z',
                              'workflow_run': {'id': 50, 'head_sha': HEAD, 'head_branch': 'feature',
                                               'repository_id': REPO_ID, 'head_repository_id': REPO_ID}})
        self.data.update({
            'git/commits/' + self.sha: {'sha': self.sha, 'tree': {'sha': TREE}, 'parents': [{'sha': BASE}]},
            'git/commits/' + MERGE: {'sha': MERGE, 'tree': {'sha': TREE}, 'parents': [{'sha': BASE}, {'sha': HEAD}]},
            'commits/%s/pulls?per_page=100' % self.sha: [pull],
            'pulls?head=Judgment-Pack:feature&state=all&per_page=100&page=1': [pull],
            'pulls?head=Judgment-Pack:feature&state=all&per_page=100&page=2': [],
            'issues/7/timeline?per_page=100&page=1': [{'event': 'committed'}, {'event': 'reviewed'},
                                                      {'event': 'merged', 'commit_id': self.sha}],
            'issues/7/timeline?per_page=100&page=2': [],
            'actions/workflows/9/runs?head_sha=%s&per_page=100&page=1' % HEAD: {'total_count': 1, 'workflow_runs': [run]},
            'actions/runs/50': run,
            'actions/runs/50/attempts/1/jobs?per_page=100&page=1': {'total_count': len(jobs), 'jobs': jobs},
            'actions/runs/50/artifacts?per_page=100&page=1': {'total_count': len(artifacts), 'artifacts': artifacts}})
        # What a scoped pull request run passed: the reused checks and, in the check job, checks that
        # are not reusable.
        self.pr_payloads, self.reused = {}, {}
        for job_key in self.IDS:
            fresh = ['python-tests', 'build'] if job_key == 'check' else []
            pr_coverage = {name: 'PASS' for name in covered[job_key] + fresh + ['setup:npm-ci', 'setup:python-deps']}
            self.pr_payloads[job_key] = g.evidence(
                dict(self.source, source=MERGE), 'ci', workflow, job_key, 50, 1, 'pull_request', 'main',
                {'runner': dict(RUNNER), 'tools': dict(TOOLS['check'])}, pr_coverage,
                g.job_identity(self.context, self.config['profiles']['check'], 'ci'), 'refs/pull/7/merge',
                {'pull_request': {'number': 7, 'base': BASE, 'head': HEAD, 'tree': TREE},
                 'environment': environment_record(names, job_key)})
            artifact = self.repack_pull_request(job_key)
            if not covered[job_key]:
                continue
            self.reused[job_key] = {'pull_request': 7, 'head': HEAD, 'merge': MERGE, 'base': BASE, 'tree': TREE,
                                    'run_id': 50, 'attempt': 1, 'job_id': self.IDS[job_key][0],
                                    'artifact': {'id': artifact['id'], 'name': artifact['name'],
                                                 'digest': artifact['digest']},
                                    'checks': list(covered[job_key])}

            def branch(payload, job_key=job_key):
                payload['coverage'].update({name: 'REUSED' for name in covered[job_key]})
                payload['reused'] = copy.deepcopy(self.reused[job_key])
            self.change_evidence('ci', job_key, branch)

    def repack_pull_request(self, job_key='check'):
        listing = self.data['actions/runs/50/artifacts?per_page=100&page=1']['artifacts']
        artifact = next(item for item in listing if item['name'] == 'ci-evidence-ci-%s-1' % job_key)
        pack(self.data, artifact, self.pr_payloads[job_key])
        return artifact


class AgentReuse(ReuseWorld):

    def test_checks_reused_from_the_merged_pull_request_are_admitted_again(self):
        self.assertEqual(self.decide(), 'reuse')
        reused = self.record['workflows']['ci']['jobs']['check']['reused']
        self.assertEqual(reused['checks'], self.COVERED)
        self.assertEqual((reused['pull_request'], reused['run_id'], reused['merge']), (7, 50, MERGE))

    def test_each_job_reuses_its_own_checks_from_its_own_job_and_artifact(self):
        self.assertEqual(self.decide(), 'reuse')
        jobs = self.record['workflows']['ci']['jobs']
        self.assertEqual(jobs['go-race']['reused']['checks'], ['go-race'])
        self.assertEqual(jobs['go-race']['reused']['job_id'], 552)
        self.assertEqual(jobs['go-race']['reused']['artifact']['name'], 'ci-evidence-ci-go-race-1')
        self.assertEqual(jobs['check']['reused']['job_id'], 551)
        self.assertEqual(jobs['check']['reused']['artifact']['name'], 'ci-evidence-ci-check-1')
        self.assertEqual(jobs['go-race']['coverage']['go-race'], 'REUSED')
        self.assertNotIn('go-race', jobs['check']['coverage'])

    def test_a_race_result_recorded_by_the_other_job_is_not_reusable(self):
        """Evidence that carries go-race in the check job's record is not the go-race job's: the
        record says which job ran what, and each job is held to its own list."""
        self.pr_payloads['check']['coverage']['go-race'] = 'PASS'
        artifact = self.repack_pull_request('check')
        self.change_evidence('ci', 'check', lambda payload: payload['reused']['artifact'].update(digest=artifact['digest']))
        self.assertEqual(self.decide(), 'reuse-coverage')

    def test_a_race_result_the_pull_request_job_did_not_pass_is_not_reused(self):
        """The go-race job claimed the reuse of a check its pull request job never ran."""
        self.pr_payloads['go-race']['coverage'].pop('go-race')
        artifact = self.repack_pull_request('go-race')
        self.change_evidence('ci', 'go-race', lambda payload: payload['reused']['artifact'].update(digest=artifact['digest']))
        self.assertEqual(self.decide(), 'reuse-coverage')

    def test_the_go_race_job_reuses_nothing_when_its_pull_request_job_ran_nothing(self):
        """A pull request whose paths did not select go-race recorded an empty coverage; its main job
        then ran the step (no reused record), and that evidence stands alone."""
        self.data = self.world()
        self.add_pull_request(race_covered=[])
        self.assertEqual(self.decide(), 'reuse')
        jobs = self.record['workflows']['ci']['jobs']
        self.assertIsNone(jobs['go-race']['reused'])
        self.assertEqual(jobs['go-race']['coverage']['go-race'], 'PASS')
        self.assertEqual(jobs['check']['reused']['checks'], self.COVERED)

    def test_a_check_that_is_not_tree_only_is_never_taken_from_a_pull_request(self):
        for name in sorted(FRESH_ON_MAIN):
            with self.subTest(check=name):
                self.data = self.world()
                self.add_pull_request(self.COVERED + [name])
                self.assertEqual(self.decide(), 'reuse-coverage')

    def test_another_environment_in_the_pull_request_run_refuses(self):
        for job_key in self.IDS:
            with self.subTest(job=job_key):
                self.data = self.world()
                self.add_pull_request()
                self.pr_payloads[job_key]['environment']['JPDEV_DESK'] = hashlib.sha256(b'another-desk').hexdigest()
                artifact = self.repack_pull_request(job_key)
                self.change_evidence('ci', job_key, lambda payload: payload['reused']['artifact'].update(digest=artifact['digest']))
                self.assertEqual(self.decide(), 'reuse-environment')

    def test_a_retargeted_pull_request_refuses(self):
        self.data['issues/7/timeline?per_page=100&page=1'].append({'event': 'base_ref_changed'})
        self.assertEqual(self.decide(), 'reuse-pull-request')

    def test_a_newer_pull_request_attempt_even_a_green_one_is_not_the_one_reused(self):
        newer = dict(self.data['actions/runs/50'], run_attempt=2, run_started_at='2026-10-30T08:52:00Z')
        self.data['actions/runs/50'] = newer
        self.data['actions/workflows/9/runs?head_sha=%s&per_page=100&page=1' % HEAD]['workflow_runs'] = [newer]
        jobs = [dict(job, id=job['id'] + 100, run_attempt=2, started_at='2026-10-30T08:52:05Z',
                     completed_at='2026-10-30T08:58:00Z')
                for job in self.data['actions/runs/50/attempts/1/jobs?per_page=100&page=1']['jobs']]
        self.data['actions/runs/50/attempts/2/jobs?per_page=100&page=1'] = {'total_count': len(jobs), 'jobs': jobs}
        listing = self.data['actions/runs/50/artifacts?per_page=100&page=1']
        for job_key in self.IDS:
            first = next(item for item in listing['artifacts'] if item['name'] == 'ci-evidence-ci-%s-1' % job_key)
            artifact = dict(first, id=first['id'] + 100, name='ci-evidence-ci-%s-2' % job_key,
                            created_at='2026-10-30T08:57:00Z')
            listing['artifacts'].append(artifact)
            pack(self.data, artifact, dict(self.pr_payloads[job_key], attempt=2))
        listing['total_count'] = len(listing['artifacts'])
        self.assertEqual(self.decide(), 'reuse-changed')

    def test_a_re_run_of_the_branch_run_carries_no_reuse(self):
        self.attempt = 2
        self.data = self.world()
        self.add_pull_request()
        self.assertEqual(self.decide(), 'reuse-event')


class AgentReusePlan(ReuseWorld):
    """The branch job's plan, before its checks, over the same world."""

    def plan(self, event='push', attempt=1, job='check'):
        env = {name: value for name, value in os.environ.items()
               if name not in self.config['workflows']['ci']['pull_request_reuse']['environment']}
        env.update(JOB_ENVIRONMENT[job], GITHUB_SHA=self.sha, GITHUB_EVENT_NAME=event, GITHUB_REF='refs/heads/main',
                   GITHUB_JOB=job, GITHUB_RUN_ID='100', GITHUB_RUN_ATTEMPT=str(attempt),
                   GITHUB_WORKFLOW_REF='%s/.github/workflows/ci.yml@refs/heads/main' % REPO, GITHUB_WORKFLOW_SHA=self.sha)
        with patch.dict(os.environ, env, clear=True):
            return g.plan(FakeAPI(self.data), self.config, copy.deepcopy(self.source), 'ci', job, NOW)

    def test_a_first_push_attempt_plans_the_reusable_checks_the_pull_request_passed(self):
        record = self.plan()
        self.assertEqual(record['decision'], 'reuse', record['refusal'])
        self.assertEqual(record['covered'], self.COVERED)

    def test_the_go_race_job_plans_its_own_check_from_its_own_pull_request_job(self):
        record = self.plan(job='go-race')
        self.assertEqual(record['decision'], 'reuse', record['refusal'])
        self.assertEqual(record['covered'], ['go-race'])
        self.assertEqual(record['job'], 'go-race')
        self.assertEqual(record['reused']['job_id'], 552)
        self.assertEqual(record['reused']['artifact']['name'], 'ci-evidence-ci-go-race-1')
        self.assertNotIn('go-race', self.plan()['covered'])

    def test_the_go_race_job_runs_its_step_when_its_pull_request_job_did_not_pass_it(self):
        self.data = self.world()
        self.add_pull_request(race_covered=[])
        record = self.plan(job='go-race')
        self.assertEqual((record['decision'], record['refusal']['code']), ('fresh', 'reuse-coverage'))
        self.assertEqual(record['covered'], [])
        self.assertEqual(self.plan()['covered'], self.COVERED)

    def test_the_go_race_job_is_bound_to_its_own_environment(self):
        """A flag set on main's go-race job and not on the pull request's refuses that job's reuse."""
        JOB_ENVIRONMENT['go-race']['GOFLAGS'] = '-tags=other'
        self.addCleanup(JOB_ENVIRONMENT['go-race'].pop, 'GOFLAGS')
        record = self.plan(job='go-race')
        self.assertEqual((record['decision'], record['refusal']['code']), ('fresh', 'reuse-environment'))

    def test_a_dispatch_or_a_re_run_runs_the_whole_list(self):
        for job in PLANNING_JOBS:
            for event, attempt in (('workflow_dispatch', 1), ('push', 2)):
                with self.subTest(job=job, event=event, attempt=attempt):
                    record = self.plan(event, attempt, job)
                    self.assertEqual((record['decision'], record['refusal']['code']), ('fresh', 'reuse-event'))
                    self.assertEqual(record['covered'], [])


class AgentCapture(AtCommit):
    """capture on main for each planning job, with the agent's bound environment."""

    def logs(self, line=True, job='check', ran=None):
        names = self.config['workflows']['ci']['pull_request_reuse']['environment']
        manifest = hashlib.sha256((self.root / 'dev.json').read_bytes()).hexdigest()
        values = JOB_ENVIRONMENT[job]
        environment = 'environment: manifest=%s %s\n' % (manifest, ' '.join(
            '%s=%s' % (name, hashlib.sha256(values[name].encode()).hexdigest()
                       if name in values else 'unset') for name in names))
        checks = ''.join('%-8s %-24s 1s\n' % ('SKIP' if name == 'dependency-audit-live' else 'PASS', name)
                         for name in (self.config['workflows']['ci']['jobs'][job]['checks'] if ran is None else ran))
        setup = 'PASS     npm-ci                   1s\nPASS     python-deps              1s\n'
        paths = []
        for kind, rows in (('check', checks), ('setup', setup)):
            path = self.root.parent / ('%s-%s.log' % (self.root.name, kind))
            path.write_text('== %s summary (dev.json) ==\n%s%s' % (kind, environment if line else '', rows))
            self.addCleanup(path.unlink)
            paths.append((kind, str(path)))
        return paths

    def capture(self, logs, job='check', plan=None):
        env = {'GITHUB_SHA': self.sha, 'GITHUB_EVENT_NAME': 'push', 'GITHUB_REF': 'refs/heads/main',
               'GITHUB_JOB': job, 'GITHUB_RUN_ID': '100', 'GITHUB_RUN_ATTEMPT': '1',
               'GITHUB_WORKFLOW_REF': '%s/.github/workflows/ci.yml@refs/heads/main' % REPO,
               'GITHUB_WORKFLOW_SHA': self.sha}
        source = {'schema': 2, 'repository': REPO, 'repository_id': REPO_ID, 'source': self.sha, 'inputs': {}}
        here = {'runner': dict(RUNNER), 'tools': dict(TOOLS['check'])}
        with patch.dict(os.environ, env), patch.object(g, 'runtime', return_value=here):
            return g.capture(self.config, source, 'ci', job, logs, plan)

    def test_the_check_job_records_its_bound_environment_and_identity(self):
        record = self.capture(self.logs())
        names = self.config['workflows']['ci']['pull_request_reuse']['environment']
        self.assertEqual(record['environment'], environment_record(names))
        self.assertIsNone(record['reused'])
        self.assertEqual(record['identity']['variant'], 'linux-x64')
        self.assertEqual(record['coverage']['dependency-audit-live'], 'SKIP')
        self.assertNotIn('contract', record['coverage'])

    def test_the_go_race_job_records_only_its_step_and_its_setup(self):
        record = self.capture(self.logs(job='go-race'), 'go-race')
        self.assertEqual(record['coverage'], {'go-race': 'PASS', 'setup:npm-ci': 'PASS', 'setup:python-deps': 'PASS'})
        self.assertEqual(record['job'], 'go-race')
        names = self.config['workflows']['ci']['pull_request_reuse']['environment']
        self.assertEqual(record['environment'], environment_record(names, 'go-race'))
        self.assertIsNone(record['environment']['JPDEV_DESK'])

    def test_the_check_job_refuses_a_log_in_which_it_ran_the_race_step(self):
        """Each check runs once: go-race in the check job's log is not coverage it may record."""
        checks = self.config['workflows']['ci']['jobs']['check']['checks'] + ['go-race']
        with self.assertRaises(g.Refusal) as caught:
            self.capture(self.logs(ran=checks))
        self.assertEqual(caught.exception.code, 'check-coverage')

    def test_the_go_race_job_refuses_a_log_in_which_it_ran_another_step(self):
        with self.assertRaises(g.Refusal) as caught:
            self.capture(self.logs(job='go-race', ran=['go-race', 'go-test']), 'go-race')
        self.assertEqual(caught.exception.code, 'check-coverage')

    def test_the_go_race_job_refuses_a_log_without_its_step(self):
        with self.assertRaises(g.Refusal) as caught:
            self.capture(self.logs(job='go-race', ran=[]), 'go-race')
        self.assertEqual(caught.exception.code, 'check-coverage')

    def test_a_checks_log_without_the_environment_line_refuses(self):
        with self.assertRaises(g.Refusal) as caught:
            self.capture(self.logs(line=False))
        self.assertEqual(caught.exception.code, 'check-environment')


if __name__ == '__main__':
    unittest.main()
