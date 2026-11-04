"""The agent's adoption of the devkit release evidence gate, v0.15.0 (agent #239, #240), whose release-os
compare mode every profile takes: the mutation table's since agent #332, the CI jobs' since #336 (devkit #79).

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
import sys
import tempfile
import unittest
from unittest.mock import patch
import zipfile

ROOT = Path(__file__).resolve().parents[1]
_spec = importlib.util.spec_from_file_location('release_evidence', ROOT / 'scripts/dev/release_evidence.py')
g = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(g)

KIT = 'v0.15.0'
# Each vendored file: the kit file it copies (line 2 names both) and the SHA-256 of the copy as the
# kit's v0.15.0 tag (commit 9651d531d978be6c8cd296ffe644e8fd3dc0fe9b) ships it under scripts/dev/.
VENDORED = {
    'scripts/dev/release_evidence.py': ('lib/release_evidence.py',
                                        'd6a4ead2421dea0475e47a7c5536c66efec2c95bfdabda3217bc521793bd2742'),
    'scripts/dev/check.sh': ('templates/check.sh',
                             'e25ad69477cbb3c923cae51a557a6cb7d18598cdeaad1a78ddeca226279c686b'),
    'scripts/dev/handover_check.sh': ('lib/handover_check.sh',
                                      '053bbc0b526634307ee949e782a40d247e2049ed3b7867e3d12d2eb43f8969f7'),
    'scripts/dev/changed_checks.py': ('lib/changed_checks.py',
                                      '7be3b8297e08df021b883961da29173149404b4cad4e4e764064f9c7230dcd16'),
}
MANIFEST = '.github/runner-identity.json'
MANIFEST_SHA = '333c3ffe1f5d93a863298133ad15717c384146797c0cc88f2463a361e1e358da'
# The mutation table's shards compare "release-os" (#332): their tools at these pins, their image name,
# platform and OS-release digest the release runner's, their image version recorded and compared with
# nothing. node is dev.json's one exact Node release, as `node --version` prints it; npm is the npm that
# release bundles, as main CI's check job (run 41757278975) and the table (run 41757326630, attempt 2)
# reported it at e8436e2.
MUTATION_PINS = {'node': 'v22.23.4', 'npm': '10.9.9'}
# The one exact Python release every actions/setup-python step of the workflows asks for (#336). The kit
# takes only a minor version in dev.json (toolchains.python.version, its schema's ^3\.[0-9]+$), so the
# workflows name the release, and the base and check profiles pin it as `python3 --version` prints it.
PYTHON_RELEASE = '3.12.16'
# The CI jobs compare "release-os" too (#336). Each pin is what main CI's jobs reported at 1c60cdc (run
# 41836158895): node, npm and go every job; python and python-packages the check job, whose image's tool
# cache held 3.12.16 (the other jobs reported 3.12.15, and the same packages: pip 25.0.1 is the pip both
# releases bundle). python-packages is the virtual environment requirements-dev.txt makes, with that pip.
BASE_PINS = {'python': 'Python 3.12.16'}
CHECK_PINS = {'node': 'v22.23.4', 'npm': '10.9.9', 'go': 'go version go1.26.10 linux/amd64',
              'python': 'Python 3.12.16',
              'python-packages': '[["attrs", "26.1.0"], ["jsonschema", "4.26.0"], ["jsonschema-specifications", '
                                 '"2025.9.1"], ["pip", "25.0.1"], ["referencing", "0.37.0"], ["rpds-py", "2026.6.3"], '
                                 '["typing_extensions", "4.16.0"]]'}
# The pip that Python release bundles, which the virtual environment is made with.
BUNDLED_PIP = '25.0.1'

CONFIG_PATH = ROOT / '.github/ci-evidence.json'
CONTRACT_CHECKS = ['contract', 'local-paths', 'agent-docs', 'diff-check']
# The full mutation table's shards (#281): the workflow's matrix and the configuration's jobs. The gate
# reads the jobs from the configuration, so the number is this repository's, not the kit's. 28 since #322,
# at most 14 at a time.
SHARDS = 28
MAX_PARALLEL = 14
# The race steps and their jobs: the two legs of one matrix job (#292), each its own key, name, check
# and evidence in the configuration, the same way the mutation shards are.
RACE_JOBS = ['go-race-a', 'go-race-b', 'go-race-c']
# The jobs that plan reuse from the merged pull request: the check job, and the race steps' own (#254).
PLANNING_JOBS = ['check'] + RACE_JOBS
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


def workflow_job_id(key):
    """The job of ci.yml that a planning job's key is a leg of."""
    return config()['workflows']['ci']['jobs'][key]['job_id']


GO_MODULE = 'github.com/Judgment-Pack/judgment-pack-agent/go'


def packages_under_the_race_patterns():
    """What `go list ./context/... ./adapter/... ./gate/...` names, by Go's own rules, from the tree: each
    directory holding a Go file, leaving out testdata and directories starting with a dot or an underscore."""
    found = []
    for top in ('context', 'adapter', 'gate'):
        for directory, children, files in os.walk(ROOT / 'go' / top):
            children[:] = [name for name in children if name != 'testdata' and name[0] not in '._']
            if any(name.endswith('.go') and name[0] not in '._' for name in files):
                found.append('%s/%s' % (GO_MODULE, Path(directory).relative_to(ROOT / 'go').as_posix()))
    return sorted(found)


# A `go` for the race steps' run text: `list` answers over the packages it is given, by Go's patterns,
# and when told to fail does so part of the way through its answer, as a broken package makes it; a
# package pattern that names nothing fails, as it does; `test` says what it was asked.
GO_SHIM = '''
import json, os, sys

packages = os.environ['GO_SHIM_PACKAGES'].split()
module = os.environ['GO_SHIM_MODULE']


def resolve(pattern):
    if not pattern.startswith('./'):
        wanted = pattern
    else:
        wanted = module + pattern[1:]
    if wanted.endswith('/...'):
        base = wanted[:-len('/...')]
        return [name for name in packages if name == base or name.startswith(base + '/')]
    if wanted not in packages:
        sys.exit('package %s is not in the module' % pattern)
    return [wanted]


verb, rest = sys.argv[1], sys.argv[2:]
if verb == 'list':
    if os.environ['GO_SHIM_LIST_FAILS']:
        print(packages[0])
        sys.exit('go list failed')
    for pattern in rest:
        print('\\n'.join(resolve(pattern)))
elif verb == 'test':
    flags = [word for word in rest if word.startswith('-')]
    chosen = [name for word in rest if not word.startswith('-') for name in resolve(word)]
    print('go test: ' + json.dumps({'flags': flags, 'packages': chosen}))
else:
    sys.exit('no such verb ' + verb)
'''


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

    def test_every_profile_is_a_linux_x64_runner_of_the_pinned_image_name(self):
        current = config()
        self.assertEqual(sorted(current['profiles']), ['base', 'check', 'mutation'])
        for name, profile in current['profiles'].items():
            with self.subTest(profile=name):
                self.assertEqual(profile['variant'], 'linux-x64')
                self.assertEqual(profile['platform'], {'RUNNER_OS': 'Linux', 'RUNNER_ARCH': 'X64',
                                                       'ImageOS': 'ubuntu26'})
                self.assertTrue(profile['tools'])
        # Another OS release, or a tool that is not its pin, is a refusal and the fresh route, never a
        # revalidation; the release workflow has no dispatch trigger and admits no prerelease tag.
        self.assertNotIn('revalidation', current)
        self.assertNotIn('dispatch', current)
        self.assertNotIn('tag', current)
        self.assertEqual((current['branch'], current['max_age_hours']), ('main', 48))

    def test_every_profile_compares_with_the_release_runners_os_release_every_tool_pinned(self):
        """#332, #336: every profile is "release-os", with a pin for every tool it records: the shards'
        node and npm, the contract job's python, and the check and race jobs' five tools. node and go are
        dev.json's exact releases, python the workflows' exact release, as their commands print them."""
        current = config()
        profiles = current['profiles']
        for name, profile in profiles.items():
            with self.subTest(profile=name):
                self.assertEqual(profile['compare'], 'release-os')
                self.assertEqual(set(profile['pins']), set(profile['tools']))
        self.assertEqual(profiles['base']['tools'], {'python': ['python3', '--version']})
        self.assertEqual(profiles['base']['pins'], BASE_PINS)
        self.assertEqual(set(profiles['check']['tools']), {'node', 'npm', 'go', 'python', 'python-packages'})
        self.assertEqual(profiles['check']['tools']['python'], ['.venv/bin/python3', '--version'])
        self.assertEqual(profiles['check']['pins'], CHECK_PINS)
        mutation = profiles['mutation']
        self.assertEqual(mutation['tools'], {'node': ['node', '--version'], 'npm': ['npm', '--version']})
        self.assertEqual(mutation['pins'], MUTATION_PINS)
        # The table's jobs, and only they, take the mutation profile; contract alone takes base.
        self.assertEqual({spec['profile'] for spec in current['workflows']['mutation']['jobs'].values()}, {'mutation'})
        self.assertEqual({key: spec['profile'] for key, spec in current['workflows']['ci']['jobs'].items()},
                         dict({'contract': 'base', 'check': 'check'}, **{leg: 'check' for leg in RACE_JOBS}))
        toolchains = json.loads(read('dev.json'))['toolchains']
        node, go = toolchains['node']['version'], toolchains['go']['version']
        self.assertRegex(node, r'^[0-9]+\.[0-9]+\.[0-9]+$')
        self.assertRegex(go, r'^[0-9]+\.[0-9]+\.[0-9]+$')
        for pins in (mutation['pins'], profiles['check']['pins']):
            self.assertEqual(pins['node'], 'v' + node)
        self.assertEqual(profiles['check']['pins']['npm'], mutation['pins']['npm'])
        self.assertEqual(profiles['check']['pins']['go'], 'go version go%s linux/amd64' % go)
        for name in ('base', 'check'):
            self.assertEqual(profiles[name]['pins']['python'], 'Python ' + PYTHON_RELEASE)

    def test_the_python_packages_pin_is_the_environment_requirements_dev_txt_makes(self):
        """#336: the check profile's python-packages pin is requirements-dev.txt's exact pins and the pip the
        Python release bundles, in the form and order the tool's command prints them. A change of either
        moves the pin in the same change, or every CI job's evidence refuses at the release."""
        pin = config()['profiles']['check']['pins']['python-packages']
        listed = json.loads(pin)
        self.assertEqual(json.dumps(sorted(tuple(item) for item in listed)), pin)

        def normal(name):
            return re.sub(r'[-_.]+', '-', name).lower()
        required = {}
        for line in read('requirements-dev.txt').splitlines():
            line = line.split('#', 1)[0].strip()
            if line:
                exact = re.fullmatch(r'([A-Za-z0-9_.-]+)==(\S+)', line)
                self.assertIsNotNone(exact, 'not an exact pin: ' + line)
                required[normal(exact.group(1))] = exact.group(2)
        self.assertEqual({normal(name): version for name, version in listed},
                         dict(required, pip=BUNDLED_PIP))

    def test_the_check_list_is_divided_between_the_ci_jobs_each_check_once(self):
        jobs = config()['workflows']['ci']['jobs']
        self.assertEqual(sorted(jobs), sorted(['check', 'contract'] + RACE_JOBS))
        self.assertEqual(jobs['contract']['checks'], CONTRACT_CHECKS)
        self.assertEqual(sorted(sum((jobs[key]['checks'] for key in jobs), [])), sorted(manifest_checks()))
        self.assertTrue(jobs['check']['setup'])
        self.assertEqual(jobs['check']['optional_skips'], ['dependency-audit-live'])
        for key in RACE_JOBS:
            with self.subTest(job=key):
                # Each leg holds its one step under its own name; the workflow's job id is shared.
                self.assertEqual((jobs[key]['name'], jobs[key]['job_id'], jobs[key]['checks']), (key, 'go-race', [key]))
                self.assertTrue(jobs[key]['setup'])
                self.assertEqual(jobs[key]['profile'], 'check')
                self.assertNotIn('optional_skips', jobs[key])
        mutation = config()['workflows']['mutation']
        self.assertEqual(list(mutation['jobs']), ['full-%d' % shard for shard in range(1, SHARDS + 1)])
        for shard, (key, spec) in enumerate(mutation['jobs'].items(), 1):
            with self.subTest(job=key):
                self.assertEqual(spec['checks'], [])
                self.assertEqual(spec['job_id'], 'full')
                self.assertEqual(spec['name'], 'every row (shard %d of %d)' % (shard, SHARDS))
        self.assertEqual(mutation['skipped_jobs'], ['rows of the files this pull request changes, and a sample',
                                                    'report a failed nightly table'])
        self.assertNotIn('pull_request_reuse', mutation)
        # Only a dispatch run is evidence (#299): a push does not run the table, and a schedule is not trusted.
        self.assertEqual(mutation['events'], ['workflow_dispatch'])

    def test_every_check_of_the_planning_jobs_is_either_reusable_or_fresh_on_main(self):
        workflow = config()['workflows']['ci']
        reusable = workflow['pull_request_reuse']['checks']
        checks = [name for job in PLANNING_JOBS for name in workflow['jobs'][job]['checks']]
        self.assertEqual(sorted(reusable + list(FRESH_ON_MAIN)), sorted(checks))
        self.assertEqual([name for name in RACE_JOBS if name not in reusable], [])
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
                    elif spec['job_id'] == 'go-race':
                        # The same for the race steps' two legs.
                        capture = 'python3 scripts/dev/release_evidence.py capture --workflow ci --job go-race-${{ matrix.part }} '
                        name = 'name: ci-evidence-ci-go-race-${{ matrix.part }}-${{ github.run_attempt }}'
                    else:
                        name = 'name: ci-evidence-%s-%s-${{ github.run_attempt }}' % (key, job_key)
                    self.assertIn(capture, job)
                    self.assertIn(name, job)
        mutation = read('.github/workflows/mutation.yml')
        self.assertIn('        shard: [%s]\n' % ', '.join(str(shard) for shard in range(1, SHARDS + 1)), mutation)
        # The matrix makes exactly the configuration's two legs, under the names the gate reads.
        race = job_block(read('.github/workflows/ci.yml'), 'go-race')
        self.assertIn('    name: go-race-${{ matrix.part }}\n', race)
        self.assertIn('      fail-fast: false\n      matrix:\n        part: [a, b, c]\n', race)
        self.assertEqual(['go-race-%s' % part for part in ('a', 'b', 'c')], RACE_JOBS)

    def test_the_table_runs_nightly_and_by_dispatch_and_only_a_dispatch_records_evidence(self):
        mutation = read('.github/workflows/mutation.yml')
        triggers = mutation[mutation.index('\non:\n'):mutation.index('\npermissions:')]
        self.assertNotIn('  push:', triggers)
        self.assertIn("  schedule:\n    - cron: '23 4 * * *'\n", triggers)
        self.assertIn('  workflow_dispatch:\n', triggers)
        # A pull request still runs its own rows and its sample, and never the table.
        self.assertIn("  pull_request:\n    paths:\n      - 'packages/harness/**'\n      - '.github/workflows/mutation.yml'\n", triggers)
        self.assertIn("    if: github.event_name == 'pull_request'\n", job_block(mutation, 'sample'))
        self.assertIn("    if: github.event_name != 'pull_request'\n", job_block(mutation, 'full'))
        main = "github.ref == 'refs/heads/main' && github.event_name == 'workflow_dispatch'"
        conditions = re.findall(r'^        if: (.*)$', job_block(mutation, 'full'), re.M)
        self.assertEqual(conditions, [main, main])
        # The duplicate rows-applicability job is gone: dev.json's mutation-rows step is the one run.
        self.assertNotIn('rows-apply', mutation)
        self.assertNotIn('mutation-check.mjs --check', mutation)
        self.assertIn('mutation-rows', config()['workflows']['ci']['jobs']['check']['checks'])

    def test_no_other_run_can_cancel_or_replace_a_candidates_table(self):
        """A run at another commit or from another ref is in another group, nothing but a pull request
        cancels (a new push replaces its own older run, so its group has no commit), and each trigger has its
        own group, so neither a nightly run nor a dispatch from a branch at the candidate's commit can hold or
        replace a dispatch from main waiting at the same commit."""
        self.assertIn("concurrency:\n  group: mutation-${{ github.event_name }}-${{ github.ref }}"
                      "${{ github.event_name != 'pull_request' && format('-{0}', github.sha) || '' }}\n"
                      "  cancel-in-progress: ${{ github.event_name == 'pull_request' }}\n",
                      read('.github/workflows/mutation.yml'))

    def test_releasing_states_the_step_and_tags_the_commit_the_table_ran_at(self):
        releasing = read('docs/releasing.md')
        self.assertIn('gh workflow run mutation.yml --repo %s --ref main\n' % REPO, releasing)
        self.assertIn('--event workflow_dispatch --commit "$(git rev-parse origin/main)"\n', releasing)
        # Main may have moved since the table started: its tip is not the commit to tag.
        self.assertNotIn('git tag -s <tag> -m "agent harness <tag>" "$(git rev-parse origin/main)"', releasing)
        self.assertIn('git tag -s <tag> -m "agent harness <tag>" <the merge commit>\n', releasing)

    def test_the_full_legs_partition_the_table(self):
        """Each leg runs its own shard: the matrix, shard N of the same number, and every key of the
        configuration, under the job limit the table has to fit."""
        full = job_block(read('.github/workflows/mutation.yml'), 'full')
        shards = ', '.join(str(shard) for shard in range(1, SHARDS + 1))
        self.assertIn('    strategy:\n      fail-fast: false\n', full)
        self.assertIn('      max-parallel: %d\n      matrix:\n        shard: [%s]\n' % (MAX_PARALLEL, shards), full)
        # The plan's 20 jobs at a time are the organization's: the table leaves some for everything else.
        self.assertLess(MAX_PARALLEL, 20)
        self.assertIn('    name: every row (shard ${{ matrix.shard }} of %d)\n' % SHARDS, full)
        self.assertIn('          SHARD: ${{ matrix.shard }}\n', full)
        # Each shard's rows run under the table's bound of 1500 s a run (docs/ci-evidence.md), not the runner's default.
        self.assertEqual(re.findall(r'mutation-check\.mjs[^\n]*', full), ['mutation-check.mjs --shard "$SHARD/%d" --timeout 1500' % SHARDS])
        self.assertEqual(list(config()['workflows']['mutation']['jobs']), ['full-%d' % n for n in range(1, SHARDS + 1)])
        self.assertIn('    timeout-minutes: 120\n', full)

    def mutation_rows(self, *options):
        """The names of the rows the runner chooses with these options, without running any."""
        done = subprocess.run([shutil.which('node'), 'packages/harness/scripts/mutation-check.mjs', '--names', *options],
                              cwd=ROOT, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                              text=True, timeout=60)
        self.assertEqual(done.returncode, 0, done.stderr)
        return done.stdout.splitlines()

    def test_every_row_is_in_exactly_one_shard_of_the_full_table(self):
        """The release's evidence is the shards: together they are the table, each row once."""
        every = self.mutation_rows()
        self.assertEqual(every, [row['name'] for row in json.loads(read('packages/harness/scripts/mutation-rows.json'))['rows']])
        shards = [self.mutation_rows('--shard', '%d/%d' % (shard, SHARDS)) for shard in range(1, SHARDS + 1)]
        self.assertTrue(all(shards))
        self.assertEqual(sorted(name for shard in shards for name in shard), sorted(every))

    def test_a_pull_request_runs_the_rows_it_always_ran(self):
        """Its sample is the same two commands, and a selection with no shard keeps the table's order:
        the seed names the same rows whatever the shards do."""
        sample = job_block(read('.github/workflows/mutation.yml'), 'sample')
        self.assertEqual(re.findall(r'mutation-check\.mjs[^\n]*', sample),
                         ['mutation-check.mjs --changed "$BASE" --sample 15 --seed "$SEED"',
                          'mutation-check.mjs --sample 5 --seed "$SEED"'])
        self.assertNotIn('--shard', sample)
        every = self.mutation_rows()
        chosen = self.mutation_rows('--sample', '5', '--seed', '277')
        self.assertEqual(len(chosen), 5)
        self.assertEqual(chosen, [name for name in every if name in chosen])
        self.assertEqual(self.mutation_rows('--shard', '1/1'), every)

    def test_a_shard_warns_before_its_limit(self):
        step = step_block(job_block(read('.github/workflows/mutation.yml'), 'full'), 'The rows of this shard')
        self.assertIn('if [ "$SECONDS" -gt 5400 ]; then echo "::warning::', step)

    def test_each_planning_job_plans_before_its_checks_and_records_the_plan(self):
        for job_id, name in (('check', 'check'), ('go-race', 'go-race-${{ matrix.part }}')):
            with self.subTest(job=name):
                job = job_block(read('.github/workflows/ci.yml'), job_id)
                main = "github.ref == 'refs/heads/main' && github.event_name != 'pull_request'"
                plan = step_block(job, 'Plan reuse from the merged pull request')
                self.assertIn('        id: plan\n        if: %s\n' % main, plan)
                self.assertIn('run: python3 scripts/dev/release_evidence.py reuse --workflow ci --job %s '
                              '--output "${{ runner.temp }}/reuse-plan.json"' % name, plan)
                checks = step_block(job, 'Checks')
                self.assertIn('          COVERED: ${{ steps.plan.outputs.covered }}\n', checks)
                record = step_block(job, 'Record exact CI inputs and coverage')
                self.assertIn('        if: %s\n' % main, record)
                self.assertIn('python3 scripts/dev/release_evidence.py capture --workflow ci --job %s --check-log' % name, record)
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
        self.assertIn('          skip=%s\n' % ','.join(CONTRACT_CHECKS + RACE_JOBS), checks)
        self.assertIn('          if [ -n "$COVERED" ]; then skip="$skip,$COVERED"; fi\n', checks)
        invocations = re.findall(r'scripts/dev/check\.sh [^|]*', checks)
        self.assertEqual(invocations, ['scripts/dev/check.sh --all --changed "origin/$BASE_REF" --skip "$skip" ',
                                       'scripts/dev/check.sh --all --skip "$skip" '])

    def go_race_others(self, manifest, leg):
        """What a race leg's own step computes as every other step, from the workflow's text."""
        step = step_block(job_block(read('.github/workflows/ci.yml'), 'go-race'), 'Read the toolchains from dev.json')
        self.assertIn('          RACE_STEP: go-race-${{ matrix.part }}\n', step)
        source = step[step.index("<<'PY'\n") + len("<<'PY'\n"):step.index('          PY\n')]
        source = '\n'.join(line[10:] for line in source.splitlines())
        with tempfile.TemporaryDirectory(prefix='go-race-others-') as room:
            Path(room, 'dev.json').write_text(json.dumps(manifest), encoding='utf-8')
            out = subprocess.run([shutil.which('python3'), '-I', '-c', source], cwd=room, check=True,
                                 env=dict(os.environ, RACE_STEP=leg), stdin=subprocess.DEVNULL,
                                 stdout=subprocess.PIPE, text=True).stdout
        return dict(line.split('=', 1) for line in out.splitlines())

    def test_the_go_race_job_skips_every_other_step_of_the_manifest(self):
        """The job selects by skipping (check.sh refuses --changed with --only), so the list is
        derived from the manifest: a step added later is not run here, each leg skips the other's
        step, and a leg's own is never skipped but for what the plan covered."""
        for leg, other in zip(RACE_JOBS, RACE_JOBS[1:] + RACE_JOBS[:1]):
            with self.subTest(leg=leg):
                manifest = json.loads(read('dev.json'))
                others = self.go_race_others(manifest, leg)['others'].split(',')
                self.assertEqual(sorted(others + [leg]), sorted(manifest_checks()))
                self.assertIn(other, others)
                manifest['checks'].append({'name': 'added-later', 'run': 'true'})
                self.assertIn('added-later', self.go_race_others(manifest, leg)['others'].split(','))
        checks = step_block(job_block(read('.github/workflows/ci.yml'), 'go-race'), 'Checks')
        self.assertIn('          skip="$OTHERS"\n', checks)
        self.assertIn('          OTHERS: ${{ steps.dev.outputs.others }}\n', checks)

    def test_each_race_step_keeps_the_command_and_limits_on_its_own_runner(self):
        """The step was divided, not changed: dev.json's flags and limits, every path that selected it,
        each path selecting both. The job needs neither the Desk nor the Gateway checkout, and its limit
        leaves each step its own."""
        rows = {row['name']: row for row in json.loads(read('dev.json'))['checks']}
        self.assertNotIn('go-race', rows)
        job = job_block(read('.github/workflows/ci.yml'), 'go-race')
        for name in RACE_JOBS:
            with self.subTest(step=name):
                row = rows[name]
                self.assertEqual(row['run'].count('go test -count=1 -race -timeout=40m '), 1)
                self.assertEqual((row['cwd'], row['toolchain'], row['summary'], row['timeout']),
                                 ('go', 'go', '^(ok|FAIL|---) ', 3000))
                self.assertGreater(60 * 60, row['timeout'])
                mapped = {path for rule in json.loads(read('jpdev.json'))['changed'] if name in rule['checks']
                          for path in rule['paths']}
                self.assertEqual(sorted(mapped), ['contract/**', 'corpus/**', 'go/**', 'packages/harness/**'])
        # The adapter's test-only packages are found by go-test's own pattern too.
        self.assertEqual((rows['go-test']['run'], rows['go-test']['cwd']), ('go test -count=1 ./...', 'go'))
        self.assertNotIn('judgment-pack-desk', job)
        self.assertNotIn('judgment-pack-gateway', job)
        self.assertIn('    timeout-minutes: 60\n', job)

    def race_step(self, name, packages, go_list_fails=False):
        """The race step's own run text from dev.json, under bash in a directory that stands for go/,
        with a `go` first on the PATH that answers `go list` over these packages by Go's own rules and
        says what `go test` was asked. Returns (exit status, flags, packages handed to go test), each
        package as its import path."""
        row = next(row for row in json.loads(read('dev.json'))['checks'] if row['name'] == name)
        with tempfile.TemporaryDirectory(prefix='go-race-split-') as room:
            shim = Path(room, 'bin')
            shim.mkdir()
            Path(room, 'go.py').write_text(GO_SHIM, encoding='utf-8')
            go = shim / 'go'
            go.write_text('#!/bin/sh\nexec "$GO_SHIM_PYTHON" -I "$GO_SHIM" "$@"\n', encoding='utf-8')
            go.chmod(0o755)
            env = {'PATH': '%s%s%s' % (shim, os.pathsep, os.environ['PATH']), 'GO_SHIM': str(Path(room, 'go.py')),
                   'GO_SHIM_PYTHON': sys.executable, 'GO_SHIM_PACKAGES': '\n'.join(packages),
                   'GO_SHIM_MODULE': GO_MODULE, 'GO_SHIM_LIST_FAILS': '1' if go_list_fails else ''}
            done = subprocess.run(['bash', '-c', row['run']], cwd=room, env=env, stdin=subprocess.DEVNULL,
                                  stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, timeout=30)
        said = [line[len('go test: '):] for line in done.stdout.splitlines() if line.startswith('go test: ')]
        self.assertLessEqual(len(said), 1, done.stdout)
        asked = json.loads(said[0]) if said else {'flags': [], 'packages': []}
        return done.returncode, asked['flags'], asked['packages']

    def race_parts(self, packages):
        parts = {}
        for name in RACE_JOBS:
            status, flags, chosen = self.race_step(name, packages)
            self.assertEqual(status, 0, name)
            self.assertEqual(flags, ['-count=1', '-race', '-timeout=40m'], name)
            self.assertEqual(len(chosen), len(set(chosen)), name)
            parts[name] = chosen
        return parts

    def test_the_two_race_steps_together_test_every_package_once(self):
        """Together they run what `go test ./context/... ./adapter/...` ran and `./gate`, over this tree:
        no package left out, none twice, and neither step empty."""
        every = packages_under_the_race_patterns()
        self.assertIn(GO_MODULE + '/context', every)
        self.assertIn(GO_MODULE + '/adapter', every)
        self.assertIn(GO_MODULE + '/gate', every)
        parts = self.race_parts(every)
        self.assertTrue(all(parts.values()))
        self.assertEqual(sorted(sum(parts.values(), [])), sorted(every))
        for one, other in (('go-race-a', 'go-race-b'), ('go-race-a', 'go-race-c'), ('go-race-b', 'go-race-c')):
            self.assertFalse(set(parts[one]) & set(parts[other]), (one, other))

    def test_a_new_test_binary_cannot_be_left_out_of_both_race_steps(self):
        """A package nobody has placed runs, and exactly once, in the step that takes what the other
        does not name: beside the named ones, under one of them, and under ./context."""
        every = packages_under_the_race_patterns()
        for added in ('adapter/newtests', 'adapter/delegate/inner', 'adapter/agentish', 'context/inner'):
            with self.subTest(added=added):
                parts = self.race_parts(every + ['%s/%s' % (GO_MODULE, added)])
                self.assertEqual(sorted(sum(parts.values(), [])), sorted(every + ['%s/%s' % (GO_MODULE, added)]))
                self.assertIn('%s/%s' % (GO_MODULE, added), parts['go-race-b'])
        # The other way: outside the two patterns is not a race package, as it was not before.
        parts = self.race_parts(every + [GO_MODULE + '/cmd/elsewhere'])
        self.assertEqual(sorted(sum(parts.values(), [])), sorted(every))

    def test_a_named_race_package_that_has_gone_stops_its_step(self):
        """The first step names its packages: one that has moved fails it, it is not quietly dropped."""
        every = packages_under_the_race_patterns()
        status, _, _ = self.race_step('go-race-a', [name for name in every if not name.endswith('/adapter/plantests')])
        self.assertNotEqual(status, 0)

    def test_a_race_step_whose_package_list_failed_tests_nothing_and_fails(self):
        """A list that broke part of the way is not the list: the step must not test what it got and pass."""
        status, _, chosen = self.race_step('go-race-b', packages_under_the_race_patterns(), go_list_fails=True)
        self.assertNotEqual(status, 0)
        self.assertEqual(chosen, [])

    def test_each_race_job_prints_each_binary_and_warns_before_its_limits(self):
        """The summary keeps one line of the step's output, so the job prints what each binary took,
        on a failure too, and warns past three quarters of the 40-minute limit and of the step's own."""
        job = job_block(read('.github/workflows/ci.yml'), 'go-race')
        self.assertIn('          JPDEV_LOGDIR: ${{ runner.temp }}/logs\n', step_block(job, 'Checks'))
        step = step_block(job, "Each test binary's time")
        self.assertIn('        if: ${{ !cancelled() }}\n', step)
        self.assertIn('          LOG: ${{ runner.temp }}/logs/go-race-${{ matrix.part }}.log\n', step)
        self.assertIn('          STEP: go-race-${{ matrix.part }}\n', step)
        self.assertIn('          SUMMARY: ${{ runner.temp }}/checks.log\n', step)
        script = '\n'.join(line[10:] for line in step[step.index('        run: |\n') + len('        run: |\n'):].splitlines())
        with tempfile.TemporaryDirectory(prefix='go-race-times-') as room:
            log, summary = Path(room, 'go-race-a.log'), Path(room, 'checks.log')
            log.write_text('=== noise\nok  \tmodule/quick\t12.5s\nok  \tmodule/near\t1800.1s\nok  \tmodule/under\t1800.0s\n'
                           'panic: test timed out after 40m0s\nFAIL\tmodule/slow\t2400.1s\n', encoding='utf-8')
            summary.write_text('go-race-a: output\nPASS     go-race-a                2250s\n', encoding='utf-8')
            base = {'LOG': str(log), 'STEP': 'go-race-a', 'SUMMARY': str(summary), 'PATH': os.environ['PATH']}

            def times(**change):
                done = subprocess.run(['bash', '-c', script], env=dict(base, **change), stdin=subprocess.DEVNULL,
                                      stdout=subprocess.PIPE, text=True, timeout=30)
                self.assertEqual(done.returncode, 0)
                return done.stdout.splitlines()
            printed = ['ok  \tmodule/quick\t12.5s', 'ok  \tmodule/near\t1800.1s', 'ok  \tmodule/under\t1800.0s',
                       'panic: test timed out after 40m0s', 'FAIL\tmodule/slow\t2400.1s',
                       '::warning::module/near took 1800.1s, past three quarters of its 40-minute limit']
            self.assertEqual(times(), printed)
            summary.write_text('go-race-a: output\nPASS     go-race-a                2251s\nPASS     go-race-b                9999s\n',
                               encoding='utf-8')
            self.assertEqual(times(), printed + ['::warning::go-race-a took 2251s, past three quarters of its 3000-second limit'])
            # A main run that reused the step wrote no log; that is not a failure.
            self.assertEqual(times(LOG=str(log) + '.absent'), [])

    def checks_step(self, room, job, event, covered):
        """The Checks step of a job as the workflow writes it, run in a fixture repository whose
        manifest's steps only say their names."""
        ci = read('.github/workflows/ci.yml')
        step = step_block(job_block(ci, workflow_job_id(job)), 'Checks')
        script = step[step.index('        run: |\n') + len('        run: |\n'):]
        log = room.parent / 'checks.log'
        script = '\n'.join(line[10:] for line in script.splitlines()).replace('${{ runner.temp }}/checks.log', str(log))
        manifest = json.loads((room / 'dev.json').read_text(encoding='utf-8'))
        others = ','.join(row['name'] for row in manifest['checks'] if row['name'] != job)
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
            names = CONTRACT_CHECKS + ['go-test'] + RACE_JOBS + ['unit-tests']
            (room / 'dev.json').write_text(json.dumps({'version': 1, 'checks': [
                {'name': name, 'run': 'echo %s' % name, 'toolchain': 'shell'} for name in names]}), encoding='utf-8')
            (room / 'jpdev.json').write_text(json.dumps({'version': 1, 'changed': [
                {'paths': ['go/**'], 'checks': ['go-test'] + RACE_JOBS},
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
            for changed, check_part, race_selected in (('go/a.go', ['go-test'], True), ('docs/a.md', ['unit-tests'], False)):
                with self.subTest(changed=changed):
                    git_in_room('checkout', '-q', '-B', 'work', 'origin/main')
                    (room / changed).parent.mkdir(parents=True, exist_ok=True)
                    (room / changed).write_text('a change\n', encoding='utf-8')
                    git_in_room('add', '.')
                    git_in_room('commit', '-qm', 'work')
                    self.assertEqual(self.checks_step(room, 'check', 'pull_request', ''), check_part)
                    for leg in RACE_JOBS:
                        self.assertEqual(self.checks_step(room, leg, 'pull_request', ''), [leg] if race_selected else [])
            self.assertEqual(self.checks_step(room, 'check', 'push', ''), ['go-test', 'unit-tests'])
            self.assertEqual(self.checks_step(room, 'check', 'push', 'go-test'), ['unit-tests'])
            for leg in RACE_JOBS:
                self.assertEqual(self.checks_step(room, leg, 'push', ''), [leg])
                self.assertEqual(self.checks_step(room, leg, 'push', leg), [])

    def test_a_pull_request_into_main_records_its_checks_for_reuse(self):
        for job_id, name in (('check', 'check'), ('go-race', 'go-race-${{ matrix.part }}')):
            with self.subTest(job=name):
                job = job_block(read('.github/workflows/ci.yml'), job_id)
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

    def dev_step_output(self, job):
        """What a job's own `dev` step writes to GITHUB_OUTPUT, run as the workflow writes it, here over
        this checkout. Its `env:` is set as written, each `${{ }}` expression standing for a placeholder."""
        at = job.index('        id: dev\n')
        step = job[job.rindex('\n      - ', 0, at) + 1:]
        following = step.find('\n      - ', 1)
        step = step[:following + 1] if following != -1 else step

        def block(header):
            """The lines under a step's key, ten spaces in, without those ten."""
            if header not in step:
                return []
            lines = []
            for line in step[step.index(header) + len(header):].splitlines():
                if line and not line.startswith(' ' * 10):
                    break
                lines.append(line[10:])
            return lines
        env = dict(os.environ)
        for line in block('        env:\n'):
            name, value = line.split(': ', 1)
            env[name] = re.sub(r'\$\{\{[^}]*\}\}', 'placeholder', value)
        if '        run: |\n' in step:
            script = '\n'.join(block('        run: |\n')) + '\n'
        else:
            script = re.search(r'^        run: (.+)$', step, re.M).group(1)
        with tempfile.TemporaryDirectory(prefix='dev-step-') as room:
            output = Path(room, 'output')
            output.write_text('', encoding='utf-8')
            env['GITHUB_OUTPUT'] = str(output)
            done = subprocess.run(['bash', '-c', script], cwd=ROOT, env=env,
                                  stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                  text=True, timeout=60)
            self.assertEqual(done.returncode, 0, done.stdout)
            return dict(line.split('=', 1) for line in output.read_text(encoding='utf-8').splitlines() if line)

    def node_releases(self):
        """(workflow, job, the Node release its setup-node is given) for every setup-node of every job of
        every workflow: as written, or what the job's own `dev` step reads."""
        found = []
        for path in sorted((ROOT / '.github/workflows').glob('*.yml')):
            text = path.read_text(encoding='utf-8')
            jobs = text[text.index('\njobs:\n'):]
            for job_id in re.findall(r'^  ([A-Za-z0-9_-]+):$', jobs, re.M):
                job = job_block(jobs, job_id)
                given = re.findall(r'uses: actions/setup-node@[0-9a-f]{40}[^\n]*\n(?:        if: [^\n]*\n)?'
                                   r'        with:\n          node-version: ([^\n]*)\n', job)
                self.assertEqual(len(given), job.count('uses: actions/setup-node@'), (path.name, job_id))
                for value in given:
                    if value == '${{ steps.dev.outputs.node }}':
                        value = self.dev_step_output(job)['node']
                    found.append((path.name, job_id, value))
        return found

    def test_every_job_that_sets_node_up_installs_the_one_exact_release_dev_json_names(self):
        """#332: with `22`, the shard on an older image installed another Node 22 release than the others.
        Every setup-node of every workflow is given dev.json's exact release, the table's shards included,
        and the mutation and check profiles' node pins are that release. The release's `CI at the tag` job
        sets up no Node since #336 (below)."""
        node = json.loads(read('dev.json'))['toolchains']['node']['version']
        found = self.node_releases()
        self.assertLessEqual({('ci.yml', 'check'), ('ci.yml', 'go-race'), ('mutation.yml', 'sample'),
                              ('mutation.yml', 'full'), ('release.yml', 'package'),
                              ('dependency-audit.yml', 'dependency-audit')},
                             {(workflow, job) for workflow, job, _ in found})
        self.assertEqual([entry for entry in found if entry[2] != node], [])
        for name in ('mutation', 'check'):
            self.assertEqual(config()['profiles'][name]['pins']['node'], 'v' + node)

    def python_releases(self):
        """(workflow, job, the Python release its setup-python is given, as written) for every
        setup-python of every job of every workflow."""
        found = []
        for path in sorted((ROOT / '.github/workflows').glob('*.yml')):
            text = path.read_text(encoding='utf-8')
            jobs = text[text.index('\njobs:\n'):]
            for job_id in re.findall(r'^  ([A-Za-z0-9_-]+):$', jobs, re.M):
                job = job_block(jobs, job_id)
                given = re.findall(r'uses: actions/setup-python@[0-9a-f]{40}[^\n]*\n        with:\n'
                                   r'          python-version: ([^\n]*)\n', job)
                self.assertEqual(len(given), job.count('uses: actions/setup-python@'), (path.name, job_id))
                found.extend((path.name, job_id, value) for value in given)
        return found

    def test_every_setup_python_step_asks_for_the_one_exact_release_the_profiles_pin(self):
        """#336: with `3.12`, the jobs on the older image of a roll took 3.12.15 from its tool cache and
        the check job on the newer one 3.12.16, and the release refused. The kit takes only a minor
        version in dev.json, so each setup-python step names the exact release itself, as a literal:
        the contract, check and race jobs, the release's `CI at the tag` job and the live audit. It is
        the base and check profiles' python pin, and a release of dev.json's minor version, which is the
        python3.N check.sh makes the virtual environment with."""
        found = self.python_releases()
        self.assertLessEqual({('ci.yml', 'contract'), ('ci.yml', 'check'), ('ci.yml', 'go-race'),
                              ('release.yml', 'verify'), ('dependency-audit.yml', 'dependency-audit')},
                             {(workflow, job) for workflow, job, _ in found})
        self.assertEqual([entry for entry in found if entry[2] != "'%s'" % PYTHON_RELEASE], [])
        for name in ('base', 'check'):
            self.assertEqual(config()['profiles'][name]['pins']['python'], 'Python ' + PYTHON_RELEASE)
        minor = json.loads(read('dev.json'))['toolchains']['python']['version']
        self.assertRegex(minor, r'^3\.[0-9]+$')
        self.assertRegex(PYTHON_RELEASE, r'^%s\.[0-9]+$' % re.escape(minor))

    def test_the_release_runner_resolves_no_tool_so_ci_at_the_tag_sets_up_only_python(self):
        """#336: every profile compares "release-os", so the gate resolves on the release runner each
        profile's image name, platform and OS-release digest, and runs no tool of any profile. The job
        sets up no Node or Go and makes no Python environment; it runs the gate under the one exact Python
        release. A profile moved back to "release-runner" would need them again (and fails the profiles
        test)."""
        verify = job_block(read('.github/workflows/release.yml'), 'verify')
        self.assertEqual(re.findall(r'uses: ([a-z-]+/[a-z-]+)@', verify),
                         ['actions/checkout', 'actions/setup-python', 'actions/upload-artifact'])
        self.assertNotIn('check.sh', verify)
        self.assertNotIn('steps.dev', verify)
        self.assertEqual({profile['compare'] for profile in config()['profiles'].values()}, {'release-os'})

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
# What each profile's jobs report: their pins.
TOOLS = {'base': dict(BASE_PINS), 'check': dict(CHECK_PINS), 'mutation': dict(MUTATION_PINS)}
ENVIRONMENT_VALUES = {'CI': 'true', 'JPDEV_DESK': 'desk-checkout', 'JP_AGENT_GATEWAY_SOURCE': 'gateway-checkout'}
# The race jobs make neither checkout, so they run without those two (unset, on both sides of a reuse).
JOB_ENVIRONMENT = {'check': ENVIRONMENT_VALUES, 'go-race-a': {'CI': 'true'}, 'go-race-b': {'CI': 'true'}, 'go-race-c': {'CI': 'true'}}
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
    release runner's resolution fixed: one push run of CI and one dispatch run of the mutation table,
    every job."""

    def setUp(self):
        super().setUp()
        self.attempt = 1
        self.table_attempt = 1
        self.here = {name: {'runner': dict(RUNNER), 'tools': dict(tools)} for name, tools in TOOLS.items()}
        resolve = patch.object(g, 'runtime', side_effect=lambda profile, variant=None:
                               copy.deepcopy(self.here[profile_of(profile)]))
        self.resolved = resolve.start()
        self.addCleanup(resolve.stop)
        # A "release-os" profile, every profile since #336, is resolved without its tools.
        resolve_runner = patch.object(g, 'runner_runtime', side_effect=lambda profile, variant=None:
                                      copy.deepcopy(self.here[profile_of(profile)]['runner']))
        self.runner_resolved = resolve_runner.start()
        self.addCleanup(resolve_runner.stop)
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
        return g.evidence(self.source, key, workflow, job_key, run_id, attempt, workflow['events'][0], 'main',
                          {'runner': dict(RUNNER), 'tools': dict(TOOLS[spec['profile']])}, coverage,
                          g.job_identity(self.context, profile, key), None, extra)

    def world(self):
        data = {'': {'id': REPO_ID, 'full_name': REPO, 'default_branch': 'main'},
                'git/ref/tags/' + TAG: {'ref': 'refs/tags/' + TAG, 'object': {'type': 'tag', 'sha': TAG_OBJECT}},
                'git/tags/' + TAG_OBJECT: {'object': {'type': 'commit', 'sha': self.sha}}}
        for index, (key, workflow) in enumerate(self.config['workflows'].items()):
            workflow_id, run_id = 9 + index, 100 + 10 * index
            attempt = self.attempt if key == 'ci' else self.table_attempt
            data['actions/workflows/' + Path(workflow['path']).name] = {
                'id': workflow_id, 'name': workflow['name'], 'path': workflow['path'], 'state': 'active'}
            run = {'id': run_id, 'run_attempt': attempt, 'event': workflow['events'][0], 'head_branch': 'main', 'head_sha': self.sha,
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
            for number, name in enumerate(workflow.get('skipped_jobs', [])):
                jobs.append({'id': run_id * 10 + 90 + number, 'name': name, 'run_id': run_id, 'run_attempt': attempt,
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
        attempt = self.attempt if key == 'ci' else self.table_attempt
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
                         ['mutation/full-%d' % shard for shard in range(1, SHARDS + 1)])
        self.assertEqual(identity['groups']['linux-x64/check']['jobs'], ['ci/check', 'ci/go-race-a', 'ci/go-race-b', 'ci/go-race-c'])
        self.assertEqual(self.record['workflows']['ci']['jobs']['check']['reused'], None)
        for leg in RACE_JOBS:
            self.assertEqual(self.record['workflows']['ci']['jobs'][leg]['coverage'],
                             {leg: 'PASS', 'setup:npm-ci': 'PASS', 'setup:python-deps': 'PASS'})
        # #336: every group compares release-os, and the release runner resolved each profile's image and
        # platform, never a tool.
        for name, group in identity['groups'].items():
            with self.subTest(group=name):
                self.assertEqual(group['compare'], 'release-os')
                self.assertEqual(group['release_runner'], RUNNER)
                self.assertEqual(group['runtime'], {'runner': RUNNER, 'tools': TOOLS[name.split('/')[1]]})
        self.assertEqual([profile_of(call.args[0]) for call in self.runner_resolved.call_args_list],
                         ['base', 'check', 'mutation'])
        self.resolved.assert_not_called()

    def test_a_release_requires_both_race_jobs(self):
        """Each race step is as required as the one step was: without either job, its artifact or its
        result, the release refuses, and neither leg can stand for the other."""
        for leg, other in zip(RACE_JOBS, RACE_JOBS[1:] + RACE_JOBS[:1]):
            with self.subTest(leg=leg):
                self.data = self.world()
                listing = self.data['actions/runs/100/attempts/1/jobs?per_page=100&page=1']
                listing['jobs'] = [job for job in listing['jobs'] if job['name'] != leg]
                listing['total_count'] = len(listing['jobs'])
                self.assertEqual(self.decide(), 'job-coverage')
                self.data = self.world()
                artifacts = self.data['actions/runs/100/artifacts?per_page=100&page=1']
                artifacts['artifacts'] = [item for item in artifacts['artifacts'] if leg not in item['name']]
                artifacts['total_count'] = len(artifacts['artifacts'])
                self.assertEqual(self.decide(), 'artifact-coverage')
                self.data = self.world()
                self.change_evidence('ci', leg, lambda payload: payload['coverage'].pop(leg))
                self.assertEqual(self.decide(), 'check-coverage')
                self.data = self.world()
                self.change_evidence('ci', leg, lambda payload: payload['coverage'].update({leg: 'SKIP'}))
                self.assertEqual(self.decide(), 'check-result')
                self.data = self.world()
                self.change_evidence('ci', 'check', lambda payload: payload['coverage'].update({leg: 'PASS'}))
                self.assertEqual(self.decide(), 'check-coverage')
                self.data = self.world()
                self.change_evidence('ci', other, lambda payload: payload['coverage'].update({leg: 'PASS'}))
                self.assertEqual(self.decide(), 'check-coverage')

    def test_a_candidate_with_the_one_old_race_job_is_refused(self):
        """A run from before the division has the one go-race job and neither leg: it is not a candidate
        under this configuration."""
        listing = self.data['actions/runs/100/attempts/1/jobs?per_page=100&page=1']
        for job in listing['jobs']:
            if job['name'] == 'go-race-a':
                job['name'] = 'go-race'
        listing['jobs'] = [job for job in listing['jobs'] if job['name'] != 'go-race-b']
        listing['total_count'] = len(listing['jobs'])
        self.assertEqual(self.decide(), 'job-coverage')

    def test_a_release_runner_on_a_newer_image_version_reuses_every_job(self):
        """#336: an image rolled between main CI and the release changes only the image version, which is
        recorded and compared with nothing, for the CI jobs as for the shards."""
        for profile in self.here.values():
            profile['runner']['ImageVersion'] = '20261102.200.1'
        self.assertEqual(self.decide(), 'reuse', self.record.get('refusal'))
        for name in ('base', 'check', 'mutation'):
            with self.subTest(profile=name):
                group = self.record['identity']['groups']['linux-x64/' + name]
                self.assertEqual(group['release_runner']['ImageVersion'], '20261102.200.1')
                self.assertEqual(set(group['image_versions'].values()), {RUNNER['ImageVersion']})
        self.assertFalse(g.revalidation_allowed(self.config))

    # #336: main CI at 1c60cdc (run 41836158895), the v0.13.1 tag's evidence, as its jobs reported it: the check
    # job on the roll's newer image, the other jobs on the older one.
    CI_ROLL = {'check': '20261101.186.1'}

    def roll_the_ci_jobs(self):
        for job_key, version in self.CI_ROLL.items():
            self.change_evidence('ci', job_key,
                                 lambda payload, version=version: payload['runtime']['runner'].update(ImageVersion=version))

    def test_the_ci_jobs_of_the_v0_13_1_refusal_are_reused_with_python_pinned(self):
        """The v0.13.1 tag refused with `check: differs from this runner in ['ImageVersion', 'python']`.
        With every Python the one pinned release, the same image versions are reused, the release runner on
        either image of the roll, and the decision record keeps each job's version beside the runner's."""
        for here in ('20261026.180.1', '20261101.186.1'):
            with self.subTest(release_runner=here):
                self.data = self.world()
                self.roll_the_table()
                self.roll_the_ci_jobs()
                for profile in self.here.values():
                    profile['runner']['ImageVersion'] = here
                self.assertEqual(self.decide(), 'reuse', self.record.get('refusal'))
                group = self.record['identity']['groups']['linux-x64/check']
                self.assertEqual(group['compare'], 'release-os')
                self.assertEqual(group['image_versions'], dict({'ci/' + leg: RUNNER['ImageVersion'] for leg in RACE_JOBS},
                                                               **{'ci/check': '20261101.186.1'}))
                self.assertEqual(group['release_runner']['ImageVersion'], here)
                self.assertIsNone(group['runtime'])
                self.assertEqual(self.record['identity']['groups']['linux-x64/base']['image_versions'],
                                 {'ci/contract': RUNNER['ImageVersion']})
        self.resolved.assert_not_called()

    def test_a_ci_job_with_a_tool_that_is_not_its_pin_refuses(self):
        """The first is v0.13.1's case without the pin: a job on the older image reported the Python of its
        tool cache. Each is a refusal at that job, never revalidated."""
        packages = CHECK_PINS['python-packages'].replace('["pip", "25.0.1"]', '["pip", "25.3"]')
        for job_key, tool, value in (('contract', 'python', 'Python 3.12.15'), ('go-race-a', 'python', 'Python 3.12.15'),
                                     ('check', 'python', 'Python 3.12.15'), ('check', 'python-packages', packages),
                                     ('go-race-b', 'node', 'v22.23.3'), ('go-race-c', 'npm', '10.9.8'),
                                     ('check', 'go', 'go version go1.26.9 linux/amd64')):
            with self.subTest(job=job_key, tool=tool):
                self.data = self.world()
                self.change_evidence('ci', job_key,
                                     lambda payload, tool=tool, value=value: payload['runtime']['tools'].update({tool: value}))
                self.assertEqual(self.decide(), 'runtime-identity')
                pin = (BASE_PINS if job_key == 'contract' else CHECK_PINS)[tool]
                self.assertEqual(self.record['refusal']['detail'], '%s: %s is %r, pinned %r' % (job_key, tool, value, pin))
                self.assertNotIn('revalidation', self.record)

    def test_a_ci_job_on_another_os_release_refuses_and_is_never_revalidated(self):
        self.roll_the_ci_jobs()
        self.change_evidence('ci', 'go-race-b', lambda payload: payload['runtime']['runner'].update(os_release='d' * 64))
        self.assertEqual(self.decide(), 'runtime-identity')
        self.assertIn('go-race-b:', self.record['refusal']['detail'])
        self.assertIn('never revalidated', self.record['refusal']['detail'])

    # #332: the v0.13.0 table re-run at e8436e2, as its shards' evidence reported it.
    ROLL = {9: '20261101.186.1', 11: '20261101.186.1', 15: '20261101.186.1', 16: '20261101.186.1',
            19: '20261018.50.1', 23: '20261101.186.1', 27: '20261101.186.1'}

    def roll_the_table(self):
        for shard, version in self.ROLL.items():
            self.change_evidence('mutation', 'full-%d' % shard,
                                 lambda payload, version=version: payload['runtime']['runner'].update(ImageVersion=version))

    def test_shards_on_several_image_versions_of_the_release_runners_os_release_are_reused(self):
        """The shards' image versions are recorded and compared with nothing; their OS release, platform
        and pinned tools are, and the release runner resolves only its own image and platform for them."""
        self.roll_the_table()
        self.assertEqual(self.decide(), 'reuse', self.record.get('refusal'))
        group = self.record['identity']['groups']['linux-x64/mutation']
        self.assertEqual(group['compare'], 'release-os')
        self.assertEqual(group['image_versions'], {'mutation/full-%d' % shard: self.ROLL.get(shard, RUNNER['ImageVersion'])
                                                   for shard in range(1, SHARDS + 1)})
        self.assertEqual(group['release_runner'], RUNNER)
        self.assertIsNone(group['runtime'])
        self.assertEqual(self.record['identity']['groups']['linux-x64/check']['compare'], 'release-os')
        self.assertEqual([profile_of(call.args[0]) for call in self.runner_resolved.call_args_list],
                         ['base', 'check', 'mutation'])
        self.resolved.assert_not_called()

    def test_a_shard_on_another_os_release_refuses_and_is_never_revalidated(self):
        self.roll_the_table()
        self.change_evidence('mutation', 'full-19', lambda payload: payload['runtime']['runner'].update(os_release='d' * 64))
        self.assertEqual(self.decide(), 'runtime-identity')
        self.assertIn('full-19', self.record['refusal']['detail'])
        self.assertIn('never revalidated', self.record['refusal']['detail'])

    def test_a_release_runner_on_another_os_release_refuses_at_the_first_job_it_reads(self):
        """Since #336 there is no drift to wait for: the first job of the configuration, CI's contract job,
        refuses."""
        for profile in self.here.values():
            profile['runner']['os_release'] = 'd' * 64
        self.assertEqual(self.decide(), 'runtime-identity')
        self.assertTrue(self.record['refusal']['detail'].startswith('contract: '), self.record['refusal']['detail'])
        self.assertIn('os_release', self.record['refusal']['detail'])
        self.assertIn('never revalidated', self.record['refusal']['detail'])

    def test_a_shard_with_another_node_or_npm_than_its_pin_refuses(self):
        """The v0.13.0 table's shard 19 reported v22.23.3."""
        for tool, value in (('node', 'v22.23.3'), ('npm', '10.9.8')):
            with self.subTest(tool=tool):
                self.data = self.world()
                self.change_evidence('mutation', 'full-19', lambda payload: payload['runtime']['tools'].update({tool: value}))
                self.assertEqual(self.decide(), 'runtime-identity')
                self.assertIn('%s is %r, pinned %r' % (tool, value, MUTATION_PINS[tool]), self.record['refusal']['detail'])

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
        for shard in (3, SHARDS):
            with self.subTest(shard=shard):
                self.data = self.world()
                listing = self.data['actions/runs/110/attempts/1/jobs?per_page=100&page=1']
                listing['jobs'] = [job for job in listing['jobs'] if job['name'] != 'every row (shard %d of %d)' % (shard, SHARDS)]
                listing['total_count'] = len(listing['jobs'])
                self.assertEqual(self.decide(), 'job-coverage')
        self.data = self.world()
        for job in self.data['actions/runs/110/attempts/1/jobs?per_page=100&page=1']['jobs']:
            if job['conclusion'] == 'skipped':
                job['conclusion'] = 'success'
        self.assertEqual(self.decide(), 'job-not-skipped')

    def test_a_shard_cancelled_at_its_limit_refuses(self):
        """A shard the job limit stopped says `cancelled`, and so does its run: whichever one says it, and
        whichever shard it is, the release refuses. Every shard has to be green."""
        for shard in (1, 6, SHARDS):
            for run_says in ('cancelled', 'success'):
                with self.subTest(shard=shard, run=run_says):
                    self.data = self.world()
                    listing = self.data['actions/runs/110/attempts/1/jobs?per_page=100&page=1']
                    job = next(job for job in listing['jobs'] if job['name'] == 'every row (shard %d of %d)' % (shard, SHARDS))
                    job.update(conclusion='cancelled', started_at='2026-10-30T09:00:05Z', completed_at='2026-10-30T11:00:20Z')
                    self.data['actions/workflows/10/runs?head_sha=%s&per_page=100&page=1' % self.sha]['workflow_runs'][0]['conclusion'] = run_says
                    self.data['actions/runs/110']['conclusion'] = run_says
                    self.assertEqual(self.decide(), 'run-result' if run_says == 'cancelled' else 'job-result')
                    if run_says == 'success':
                        self.assertIn('every row (shard %d of %d): cancelled' % (shard, SHARDS), self.record['refusal']['detail'])

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


class TheTableAtACandidate(AgentWorld):
    """#299: the real gate over a candidate commit, whose table is the dispatch run 110 started at 09:00,
    and a nightly run landing on the same commit, or not."""

    EARLIER, LATER = '2026-10-30T08:00:00Z', '2026-10-30T11:00:00Z'

    def runs(self):
        return self.data['actions/workflows/10/runs?head_sha=%s&per_page=100&page=1' % self.sha]

    def add_run(self, event='schedule', started=LATER, conclusion='success', status='completed', run_id=120):
        runs = self.runs()
        run = dict(self.data['actions/runs/110'], id=run_id, event=event, run_started_at=started, created_at=started,
                   status=status, conclusion=conclusion)
        runs['workflow_runs'].append(run)
        runs['total_count'] = len(runs['workflow_runs'])
        return run

    def assert_the_evidence_is_the_dispatch_run(self, attempt=1):
        self.assertEqual(self.decide(), 'reuse', self.record.get('refusal'))
        trail = self.record['workflows']['mutation']
        self.assertEqual((trail['run_id'], trail['attempt'], trail['event']), (110, attempt, 'workflow_dispatch'))

    def test_a_dispatch_run_is_the_evidence(self):
        self.assert_the_evidence_is_the_dispatch_run()
        self.assertEqual(self.record['workflows']['mutation']['events'], ['workflow_dispatch'])

    def test_a_commit_without_a_dispatch_table_refuses_and_names_what_is_missing(self):
        """Neither a nightly run nor a push run stands in for it, however green."""
        runs = self.runs()
        runs['workflow_runs'], runs['total_count'] = [], 0
        self.assertEqual(self.decide(), 'run-missing')
        for event in ('schedule', 'push'):
            self.add_run(event, run_id=120 if event == 'schedule' else 121)
            self.assertEqual(self.decide(), 'run-missing')
        detail = self.record['refusal']['detail']
        self.assertIn('no workflow_dispatch run of .github/workflows/mutation.yml at %s' % self.sha, detail)

    def test_a_dispatch_on_another_branch_is_not_evidence(self):
        runs = self.runs()
        runs['workflow_runs'][0]['head_branch'] = 'feature'
        self.assertEqual(self.decide(), 'run-missing')

    def test_a_nightly_run_that_passed_changes_nothing_before_or_after_the_dispatch(self):
        for started in (self.EARLIER, self.LATER):
            with self.subTest(started=started):
                self.data = self.world()
                self.add_run(started=started)
                self.assert_the_evidence_is_the_dispatch_run()

    def test_a_nightly_run_not_finished_holds_the_release_and_is_named(self):
        for started in (self.EARLIER, self.LATER):
            for status in ('queued', 'in_progress'):
                with self.subTest(started=started, status=status):
                    self.data = self.world()
                    run = self.add_run(started=started, status=status, conclusion=None)
                    self.assertEqual(self.decide(), 'run-unfinished')
                    self.assertIn('120', self.record['refusal']['detail'])
                    run['status'], run['conclusion'] = 'completed', 'success'
                    self.assert_the_evidence_is_the_dispatch_run()

    def test_a_nightly_run_after_the_dispatch_that_did_not_pass_refuses_and_is_named(self):
        """The gate's rule is unchanged, for a cancelled run too."""
        for conclusion in ('failure', 'cancelled', 'timed_out'):
            with self.subTest(conclusion=conclusion):
                self.data = self.world()
                self.add_run(conclusion=conclusion)
                self.assertEqual(self.decide(), 'run-newer-failed')
                self.assertIn('120 (schedule, %s)' % conclusion, self.record['refusal']['detail'])
                self.assertIn('run 110', self.record['refusal']['detail'])

    def test_a_nightly_run_before_the_dispatch_that_did_not_pass_blocks_nothing(self):
        """A failed night does not stop a candidate's own table, and a cancelled run can stay cancelled."""
        for conclusion in ('failure', 'cancelled', 'timed_out'):
            with self.subTest(conclusion=conclusion):
                self.data = self.world()
                self.add_run(started=self.EARLIER, conclusion=conclusion)
                self.assert_the_evidence_is_the_dispatch_run()

    def test_re_running_the_failed_nightly_jobs_clears_the_refusal_and_is_not_evidence(self):
        run = self.add_run(conclusion='failure')
        self.assertEqual(self.decide(), 'run-newer-failed')
        run.update(run_attempt=2, conclusion='success', run_started_at='2026-10-30T12:00:00Z')
        self.assert_the_evidence_is_the_dispatch_run()

    def test_re_running_the_dispatch_run_makes_it_the_newest_again_after_main_moved(self):
        """The re-run's attempt started after the failed night, at the same commit, whatever main names."""
        self.table_attempt = 2
        self.data = self.world()
        for run in (self.runs()['workflow_runs'][0], self.data['actions/runs/110']):
            run['created_at'] = '2026-10-30T07:00:00Z'
        self.add_run(started=self.EARLIER, conclusion='failure')
        self.assert_the_evidence_is_the_dispatch_run(attempt=2)

    def test_a_nightly_run_re_run_is_still_not_evidence_without_a_dispatch(self):
        runs = self.runs()
        runs['workflow_runs'], runs['total_count'] = [], 0
        self.add_run(started='2026-10-30T12:00:00Z').update(run_attempt=2)
        self.assertEqual(self.decide(), 'run-missing')

    def test_a_newer_dispatch_that_did_not_pass_refuses_though_an_older_one_passed(self):
        for conclusion in ('failure', 'cancelled'):
            with self.subTest(conclusion=conclusion):
                self.data = self.world()
                run = self.add_run('workflow_dispatch', conclusion=conclusion)
                self.data['actions/runs/120'] = run
                self.assertEqual(self.decide(), 'run-result')
                self.assertEqual(self.record['workflows']['mutation']['run_id'], 120)

    def test_only_a_dispatch_on_main_can_record_the_table(self):
        """The gate refuses a capture on a schedule or on a push, as well as the workflow's condition."""
        source = {'schema': 2, 'repository': REPO, 'repository_id': REPO_ID, 'source': self.sha, 'inputs': {}}
        base = {'GITHUB_SHA': self.sha, 'GITHUB_REF': 'refs/heads/main', 'GITHUB_JOB': 'full', 'GITHUB_RUN_ID': '100',
                'GITHUB_RUN_ATTEMPT': '1', 'GITHUB_WORKFLOW_SHA': self.sha,
                'GITHUB_WORKFLOW_REF': '%s/.github/workflows/mutation.yml@refs/heads/main' % REPO}
        for event, ref, code in (('schedule', 'refs/heads/main', 'capture-event'), ('push', 'refs/heads/main', 'capture-event'),
                                 ('workflow_dispatch', 'refs/heads/feature', 'capture-ref')):
            with self.subTest(event=event, ref=ref):
                with patch.dict(os.environ, dict(base, GITHUB_EVENT_NAME=event, GITHUB_REF=ref)):
                    with self.assertRaises(g.Refusal) as caught:
                        g.capture_context(self.config, source, 'mutation', 'full-1')
                self.assertEqual(caught.exception.code, code)
        with patch.dict(os.environ, dict(base, GITHUB_EVENT_NAME='workflow_dispatch')):
            self.assertEqual(g.capture_context(self.config, source, 'mutation', 'full-1')[2], 'workflow_dispatch')

    def test_the_report_job_of_a_dispatch_run_must_be_skipped(self):
        name = 'report a failed nightly table'
        listing = self.data['actions/runs/110/attempts/1/jobs?per_page=100&page=1']
        job = next(job for job in listing['jobs'] if job['name'] == name)
        self.assertEqual(job['conclusion'], 'skipped')
        job['conclusion'] = 'success'
        self.assertEqual(self.decide(), 'job-not-skipped')
        listing['jobs'].remove(job)
        listing['total_count'] = len(listing['jobs'])
        self.assertEqual(self.decide(), 'job-coverage')

    def refusal_says(self, record):
        """What release.yml's own step prints for a decision record of the gate."""
        step = step_block(job_block(read('.github/workflows/release.yml'), 'verify'), 'Say what to do about a refusal')
        self.assertIn("        if: ${{ failure() && steps.gate.outcome == 'failure' }}\n", step)
        self.assertIn('          RECORD: ${{ runner.temp }}/release-evidence.json\n', step)
        source = step[step.index("<<'PYCURE'\n") + len("<<'PYCURE'\n"):step.index('          PYCURE\n')]
        source = '\n'.join(line[10:] for line in source.splitlines())
        path = self.root.parent / ('%s-record.json' % self.root.name)
        path.write_text(json.dumps(record), encoding='utf-8')
        self.addCleanup(path.unlink, missing_ok=True)
        done = subprocess.run([sys.executable, '-I', '-c', source], stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                              stderr=subprocess.PIPE, text=True, timeout=30,
                              env={'RECORD': str(path), 'GITHUB_REPOSITORY': REPO, 'PATH': os.environ['PATH']})
        self.assertEqual((done.returncode, done.stderr), (0, ''))
        self.assertTrue(done.stdout.startswith('::error::'), done.stdout)
        return done.stdout

    def test_the_release_run_says_what_to_do_about_each_refusal_of_the_table(self):
        runs = self.runs()
        dispatch = runs['workflow_runs'][0]
        runs['workflow_runs'], runs['total_count'] = [], 0
        self.assertEqual(self.decide(), 'run-missing')
        says = self.refusal_says(self.record)
        self.assertIn('gh workflow run mutation.yml --repo %s --ref main' % REPO, says)
        self.assertIn('While main names this commit', says)
        self.assertIn('a nightly run is not evidence', says)
        runs['workflow_runs'], runs['total_count'] = [dispatch], 1
        run = self.add_run(status='in_progress', conclusion=None)
        self.assertEqual(self.decide(), 'run-unfinished')
        self.assertIn('Wait for it', self.refusal_says(self.record))
        run.update(status='completed', conclusion='failure')
        self.assertEqual(self.decide(), 'run-newer-failed')
        says = self.refusal_says(self.record)
        self.assertIn('did not pass', says)
        self.assertIn('gh run rerun <run id> --repo %s' % REPO, says)
        # main CI's own missing run is not told to dispatch the table.
        self.data = self.world()
        ci = self.data['actions/workflows/9/runs?head_sha=%s&per_page=100&page=1' % self.sha]
        ci['workflow_runs'], ci['total_count'] = [], 0
        self.assertEqual(self.decide(), 'run-missing')
        says = self.refusal_says(self.record)
        self.assertNotIn('gh workflow run', says)
        self.assertIn('docs/ci-evidence.md#the-fresh-route', says)


# A `gh` for the report step: it says what it was asked, answers `api` with the jobs it is given and
# `issue list` with the open issue's number (the step's own --jq filter is held to its text below).
GH_SHIM = '''
import json, os, sys

args = sys.argv[1:]
with open(os.environ['GH_SHIM_CALLS'], 'a', encoding='utf-8') as calls:
    calls.write(json.dumps(args) + '\\n')
if args[0] == 'api':
    print(os.environ['GH_SHIM_JOBS'])
elif args[:2] == ['issue', 'list']:
    print(os.environ['GH_SHIM_OPEN'])
elif args[:2] not in (['issue', 'create'], ['issue', 'comment']):
    sys.exit('no such command')
'''


class NightlyReport(unittest.TestCase):
    """The report job of mutation.yml, its condition and its step over a stand-in gh, with the jobs
    as the API gave them on live runs: a shard at its limit and a run cancelled by hand both say
    `cancelled`, and differ only by time."""

    REPORT = 'report a failed nightly table'
    TITLE = 'The nightly mutation table did not pass'

    def job(self, shard, conclusion, started='2026-11-02T13:19:47Z', completed='2026-11-02T13:46:51Z'):
        return {'name': 'every row (shard %d of %d)' % (shard, SHARDS), 'conclusion': conclusion, 'started_at': started,
                'completed_at': completed}

    def run_step(self, jobs, open_issue=''):
        job = job_block(read('.github/workflows/mutation.yml'), 'report')
        step = step_block(job, 'Open the issue, or comment on the open one')
        script = step[step.index('        run: |\n') + len('        run: |\n'):]
        script = '\n'.join(line[10:] for line in script.splitlines())
        with tempfile.TemporaryDirectory(prefix='nightly-report-') as room:
            shim = Path(room, 'bin')
            shim.mkdir()
            Path(room, 'gh.py').write_text(GH_SHIM, encoding='utf-8')
            gh = shim / 'gh'
            gh.write_text('#!/bin/sh\nexec "$GH_SHIM_PYTHON" -I "$GH_SHIM" "$@"\n', encoding='utf-8')
            gh.chmod(0o755)
            calls = Path(room, 'calls')
            calls.write_text('', encoding='utf-8')
            listing = [{'name': 'rows of the files this pull request changes, and a sample', 'conclusion': 'skipped',
                        'started_at': None, 'completed_at': None},
                       {'name': self.REPORT, 'conclusion': None, 'started_at': '2026-11-02T15:00:00Z', 'completed_at': None}]
            env = {'PATH': '%s%s%s' % (shim, os.pathsep, os.environ['PATH']), 'GH_SHIM': str(Path(room, 'gh.py')),
                   'GH_SHIM_PYTHON': sys.executable, 'GH_SHIM_CALLS': str(calls), 'GH_SHIM_OPEN': open_issue,
                   'GH_SHIM_JOBS': json.dumps({'jobs': listing + jobs}), 'GH_TOKEN': 'token', 'GH_REPO': REPO,
                   'RUN_ID': '555', 'COMMIT': 'c' * 40, 'RUNNER_TEMP': room, 'GITHUB_SERVER_URL': 'https://github.example'}
            done = subprocess.run(['bash', '-c', script], env=env, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                                  stderr=subprocess.STDOUT, text=True, timeout=30)
            self.assertEqual(done.returncode, 0, done.stdout)
            return [json.loads(line) for line in calls.read_text(encoding='utf-8').splitlines()]

    def body_of(self, call):
        return call[call.index('--body') + 1]

    def test_the_job_runs_only_for_a_nightly_table_that_did_not_pass_and_can_write_only_issues(self):
        job = job_block(read('.github/workflows/mutation.yml'), 'report')
        self.assertIn('    name: %s\n    needs: full\n' % self.REPORT, job)
        self.assertIn("    if: ${{ always() && github.event_name == 'schedule' && needs.full.result != 'success' }}\n", job)
        self.assertIn('    permissions:\n      actions: read\n      issues: write\n    steps:\n', job)
        self.assertNotIn('uses:', job)
        self.assertEqual(re.findall(r': write', job), [': write'])
        # The limit the step tells a stopped shard by is under the job's own limit.
        self.assertIn('    timeout-minutes: 120\n', job_block(read('.github/workflows/mutation.yml'), 'full'))
        self.assertIn('.total_seconds() >= 7000:', job)
        self.assertLess(7000, 120 * 60)
        self.assertIn(self.REPORT, config()['workflows']['mutation']['skipped_jobs'])

    def test_a_failed_shard_opens_an_issue_naming_the_commit_the_run_and_the_shards(self):
        calls = self.run_step([self.job(1, 'success'), self.job(4, 'failure'), self.job(7, 'failure')])
        self.assertEqual([call[:2] for call in calls], [['api', 'repos/%s/actions/runs/555/jobs?per_page=100' % REPO],
                                                        ['issue', 'list'], ['issue', 'create']])
        create = calls[-1]
        self.assertEqual(create[create.index('--title') + 1], self.TITLE)
        self.assertEqual(create[create.index('--repo') + 1], REPO)
        body = self.body_of(create)
        self.assertIn('c' * 40, body)
        self.assertIn('https://github.example/%s/actions/runs/555' % REPO, body)
        self.assertIn('- every row (shard 4 of %d): failure\n- every row (shard 7 of %d): failure\n' % (SHARDS, SHARDS), body)
        self.assertNotIn('shard 1 of', body)
        self.assertNotIn(self.REPORT, body)

    def test_a_later_failure_comments_on_the_open_issue(self):
        calls = self.run_step([self.job(4, 'failure')], open_issue='77')
        self.assertEqual(calls[-1][:3], ['issue', 'comment', '77'])
        self.assertEqual([call for call in calls if call[:2] == ['issue', 'create']], [])
        self.assertIn('- every row (shard 4 of %d): failure' % SHARDS, self.body_of(calls[-1]))

    def test_the_open_issue_is_found_by_its_exact_title(self):
        step = step_block(job_block(read('.github/workflows/mutation.yml'), 'report'), 'Open the issue, or comment on the open one')
        self.assertIn('''--state open --search "\\"$title\\" in:title" --json number,title --jq "[.[] | select(.title == \\"$title\\") | .number][0] // empty"''', step)

    def test_a_shard_stopped_at_its_limit_is_reported_though_it_says_cancelled(self):
        """The times of shard 4 of 4 on run 41371912091, which the job limit stopped."""
        calls = self.run_step([self.job(4, 'cancelled', '2026-11-02T01:19:45Z', '2026-11-02T03:20:03Z')])
        self.assertEqual(calls[-1][:2], ['issue', 'create'])
        self.assertIn('- every row (shard 4 of %d): stopped at its 120-minute limit' % SHARDS, self.body_of(calls[-1]))

    def test_any_other_conclusion_is_reported(self):
        calls = self.run_step([self.job(4, 'timed_out')])
        self.assertIn('- every row (shard 4 of %d): timed_out' % SHARDS, self.body_of(calls[-1]))

    def test_a_run_cancelled_by_hand_reports_nothing(self):
        """The times of run 41452025111, cancelled by hand. An unneeded run stays quietly cancelled."""
        calls = self.run_step([self.job(shard, 'cancelled') for shard in range(1, SHARDS + 1)])
        self.assertEqual([call[0] for call in calls], ['api'])
        # A job cancelled before it started has no times; that is not a limit either.
        calls = self.run_step([self.job(1, 'cancelled', None, None), self.job(2, 'success')])
        self.assertEqual([call[0] for call in calls], ['api'])

    def test_a_failure_in_a_run_also_cancelled_is_reported(self):
        calls = self.run_step([self.job(1, 'cancelled'), self.job(2, 'failure')])
        self.assertEqual(calls[-1][:2], ['issue', 'create'])
        self.assertNotIn('shard 1 of', self.body_of(calls[-1]))


class ReuseWorld(AgentWorld):
    """The push run took tree-only checks from the merged pull request's run, each job its own: the
    check job two, and each race job its one."""

    COVERED = ['go-test', 'unit-tests']
    # The pull request run's job and artifact ids, for each job.
    IDS = {'check': (551, 651), 'go-race-a': (552, 652), 'go-race-b': (553, 653), 'go-race-c': (554, 654)}

    def setUp(self):
        super().setUp()
        self.add_pull_request()

    def add_pull_request(self, covered=None, race_covered=None):
        """race_covered names the race steps the pull request's jobs passed; every one, by default."""
        covered = dict({leg: [leg] if race_covered is None or leg in race_covered else [] for leg in RACE_JOBS},
                       check=covered or self.COVERED)
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
        for leg in RACE_JOBS:
            self.assertEqual(jobs[leg]['reused']['checks'], [leg])
            self.assertEqual(jobs[leg]['reused']['job_id'], self.IDS[leg][0])
            self.assertEqual(jobs[leg]['reused']['artifact']['name'], 'ci-evidence-ci-%s-1' % leg)
            self.assertEqual(jobs[leg]['coverage'][leg], 'REUSED')
            self.assertNotIn(leg, jobs['check']['coverage'])
        self.assertEqual(jobs['check']['reused']['job_id'], 551)
        self.assertEqual(jobs['check']['reused']['artifact']['name'], 'ci-evidence-ci-check-1')

    def test_a_race_result_recorded_by_another_job_is_not_reusable(self):
        """Evidence that carries a race step in another job's record is not that step's job's: the
        record says which job ran what, and each job is held to its own list."""
        for carrier, leg in (('check', 'go-race-a'), ('check', 'go-race-b'), ('go-race-a', 'go-race-b'), ('go-race-b', 'go-race-a')):
            with self.subTest(carrier=carrier, leg=leg):
                self.data = self.world()
                self.add_pull_request()
                self.pr_payloads[carrier]['coverage'][leg] = 'PASS'
                artifact = self.repack_pull_request(carrier)
                self.change_evidence('ci', carrier, lambda payload: payload['reused']['artifact'].update(digest=artifact['digest']))
                self.assertEqual(self.decide(), 'reuse-coverage')

    def test_a_race_result_the_pull_request_job_did_not_pass_is_not_reused(self):
        """A race job claimed the reuse of a check its pull request job never ran."""
        for leg in RACE_JOBS:
            with self.subTest(leg=leg):
                self.data = self.world()
                self.add_pull_request()
                self.pr_payloads[leg]['coverage'].pop(leg)
                artifact = self.repack_pull_request(leg)
                self.change_evidence('ci', leg, lambda payload: payload['reused']['artifact'].update(digest=artifact['digest']))
                self.assertEqual(self.decide(), 'reuse-coverage')

    def test_a_race_job_reuses_nothing_when_its_pull_request_job_ran_nothing(self):
        """A pull request whose paths did not select the race steps recorded an empty coverage; its main
        jobs then ran them (no reused record), and that evidence stands alone. One leg's reuse does not
        depend on the other's."""
        for race_covered, reused in (([], []), (['go-race-a'], ['go-race-a']), (['go-race-b'], ['go-race-b'])):
            with self.subTest(race_covered=race_covered):
                self.data = self.world()
                self.add_pull_request(race_covered=race_covered)
                self.assertEqual(self.decide(), 'reuse')
                jobs = self.record['workflows']['ci']['jobs']
                for leg in RACE_JOBS:
                    if leg in reused:
                        self.assertEqual(jobs[leg]['reused']['checks'], [leg])
                        self.assertEqual(jobs[leg]['coverage'][leg], 'REUSED')
                    else:
                        self.assertIsNone(jobs[leg]['reused'])
                        self.assertEqual(jobs[leg]['coverage'][leg], 'PASS')
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
                   GITHUB_JOB=workflow_job_id(job), GITHUB_RUN_ID='100', GITHUB_RUN_ATTEMPT=str(attempt),
                   GITHUB_WORKFLOW_REF='%s/.github/workflows/ci.yml@refs/heads/main' % REPO, GITHUB_WORKFLOW_SHA=self.sha)
        with patch.dict(os.environ, env, clear=True):
            return g.plan(FakeAPI(self.data), self.config, copy.deepcopy(self.source), 'ci', job, NOW)

    def test_a_first_push_attempt_plans_the_reusable_checks_the_pull_request_passed(self):
        record = self.plan()
        self.assertEqual(record['decision'], 'reuse', record['refusal'])
        self.assertEqual(record['covered'], self.COVERED)

    def test_a_pull_request_run_from_before_the_division_gives_the_race_jobs_no_reuse(self):
        """Its run has the one go-race job and neither leg, so on main each leg runs its step; the check
        job's own reuse does not depend on them."""
        jobs = self.data['actions/runs/50/attempts/1/jobs?per_page=100&page=1']
        artifacts = self.data['actions/runs/50/artifacts?per_page=100&page=1']
        jobs['jobs'] = [dict(job, name='go-race') if job['name'] == 'go-race-a' else job for job in jobs['jobs']
                        if job['name'] not in ('go-race-b', 'go-race-c')]
        jobs['total_count'] = len(jobs['jobs'])
        artifacts['artifacts'] = [dict(item, name='ci-evidence-ci-go-race-1') if item['name'] == 'ci-evidence-ci-go-race-a-1'
                                  else item for item in artifacts['artifacts'] if item['name'] not in ('ci-evidence-ci-go-race-b-1', 'ci-evidence-ci-go-race-c-1')]
        artifacts['total_count'] = len(artifacts['artifacts'])
        for leg in RACE_JOBS:
            with self.subTest(leg=leg):
                record = self.plan(job=leg)
                self.assertEqual((record['decision'], record['refusal']['code'], record['covered']), ('fresh', 'reuse-job', []))
        self.assertEqual(self.plan()['covered'], self.COVERED)

    def test_each_race_job_plans_its_own_check_from_its_own_pull_request_job(self):
        for leg in RACE_JOBS:
            with self.subTest(leg=leg):
                record = self.plan(job=leg)
                self.assertEqual(record['decision'], 'reuse', record['refusal'])
                self.assertEqual(record['covered'], [leg])
                self.assertEqual(record['job'], leg)
                self.assertEqual(record['reused']['job_id'], self.IDS[leg][0])
                self.assertEqual(record['reused']['artifact']['name'], 'ci-evidence-ci-%s-1' % leg)
                self.assertNotIn(leg, self.plan()['covered'])

    def test_a_race_job_runs_its_step_when_its_pull_request_job_did_not_pass_it(self):
        for race_covered, plans_reuse in (([], {}), (['go-race-a'], {'go-race-a'}), (['go-race-b'], {'go-race-b'})):
            with self.subTest(race_covered=race_covered):
                self.data = self.world()
                self.add_pull_request(race_covered=race_covered)
                for leg in RACE_JOBS:
                    record = self.plan(job=leg)
                    if leg in plans_reuse:
                        self.assertEqual((record['decision'], record['covered']), ('reuse', [leg]))
                    else:
                        self.assertEqual((record['decision'], record['refusal']['code']), ('fresh', 'reuse-coverage'))
                        self.assertEqual(record['covered'], [])
                self.assertEqual(self.plan()['covered'], self.COVERED)

    def test_a_race_job_is_bound_to_its_own_environment(self):
        """A flag set on main's race job and not on the pull request's refuses that job's reuse."""
        for leg in RACE_JOBS:
            with self.subTest(leg=leg):
                JOB_ENVIRONMENT[leg]['GOFLAGS'] = '-tags=other'
                self.addCleanup(JOB_ENVIRONMENT[leg].pop, 'GOFLAGS')
                record = self.plan(job=leg)
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
            self.addCleanup(path.unlink, missing_ok=True)
            paths.append((kind, str(path)))
        return paths

    def capture(self, logs, job='check', plan=None):
        env = {'GITHUB_SHA': self.sha, 'GITHUB_EVENT_NAME': 'push', 'GITHUB_REF': 'refs/heads/main',
               'GITHUB_JOB': workflow_job_id(job), 'GITHUB_RUN_ID': '100', 'GITHUB_RUN_ATTEMPT': '1',
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

    def test_each_race_job_records_only_its_step_and_its_setup(self):
        for leg in RACE_JOBS:
            with self.subTest(leg=leg):
                record = self.capture(self.logs(job=leg), leg)
                self.assertEqual(record['coverage'], {leg: 'PASS', 'setup:npm-ci': 'PASS', 'setup:python-deps': 'PASS'})
                self.assertEqual(record['job'], leg)
                names = self.config['workflows']['ci']['pull_request_reuse']['environment']
                self.assertEqual(record['environment'], environment_record(names, leg))
                self.assertIsNone(record['environment']['JPDEV_DESK'])

    def test_the_check_job_refuses_a_log_in_which_it_ran_a_race_step(self):
        """Each check runs once: a race step in the check job's log is not coverage it may record."""
        for leg in RACE_JOBS:
            with self.subTest(leg=leg):
                checks = self.config['workflows']['ci']['jobs']['check']['checks'] + [leg]
                with self.assertRaises(g.Refusal) as caught:
                    self.capture(self.logs(ran=checks))
                self.assertEqual(caught.exception.code, 'check-coverage')

    def test_a_race_job_refuses_a_log_in_which_it_ran_another_step(self):
        for leg, other in zip(RACE_JOBS, RACE_JOBS[1:] + RACE_JOBS[:1]):
            for extra in ('go-test', other):
                with self.subTest(leg=leg, extra=extra):
                    with self.assertRaises(g.Refusal) as caught:
                        self.capture(self.logs(job=leg, ran=[leg, extra]), leg)
                    self.assertEqual(caught.exception.code, 'check-coverage')

    def test_a_race_job_refuses_a_log_without_its_step(self):
        for leg, other in zip(RACE_JOBS, RACE_JOBS[1:] + RACE_JOBS[:1]):
            for ran in ([], [other]):
                with self.subTest(leg=leg, ran=ran):
                    with self.assertRaises(g.Refusal) as caught:
                        self.capture(self.logs(job=leg, ran=ran), leg)
                    self.assertEqual(caught.exception.code, 'check-coverage')

    def test_a_checks_log_without_the_environment_line_refuses(self):
        with self.assertRaises(g.Refusal) as caught:
            self.capture(self.logs(line=False))
        self.assertEqual(caught.exception.code, 'check-environment')


if __name__ == '__main__':
    unittest.main()
