#!/usr/bin/env python3
# from judgment-pack-devkit v0.13.0 (lib/release_evidence.py)
"""release_evidence.py capture|verify|reuse: reuse the CI evidence main produced at the release commit.

capture  Runs in a CI job on the trusted branch, after the job's checks. Writes evidence.json for
         the job's artifact: the commit, its input identities, the runner and tool identity and
         the check results.
verify   Runs in the release workflow. Reads this repository's Actions API for the configured
         workflow's runs at the exact release commit, selects the newest applicable attempt,
         verifies it, and writes the decision record, reuse or refuse, to standard output and to a
         file. Missing, partial, stale, unreadable or different evidence refuses: nothing here
         turns unavailable evidence into success. Where the configuration allows revalidation, a
         publication whose one refusal is the release runner's drift decides revalidate instead:
         the release-runner jobs' evidence is not taken, and the release workflow runs the check
         list dev.json declares, strictly, on its own runner.

The configuration is .github/ci-evidence.json, schema 2 (docs/ci-evidence.md). There is no input
for a local evidence file, another API host, a green flag or an age override.

Where the configuration names a runner identity manifest (identity), every job also records the
image identity its standard GitHub-hosted runner reports: the image name and version, the OS and
architecture, the OS-release digest, the tools and the actions its workflow names, under one
variant of the manifest. It is a reported image identity, not a pinned VM image: the gate compares
what runners report and refuses a missing, unexpected or changed identity.

reuse    Where a workflow configures pull_request_reuse, runs in a CI job on a push to the trusted
         branch, before its checks, and decides which of the job's tree-only checks the evidence of
         the merged pull request's newest run already covers (reuse), or none (fresh). Capture on
         the pull request records that evidence; capture on the branch records what was reused, and
         verify admits it again at the release. Tree equality alone never qualifies a result.
"""
import argparse
import datetime as dt
import hashlib
import io
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
from urllib.parse import quote
import zipfile
import zlib

SCHEMA = 2
MAX_AGE_HOURS = 48
EVENTS = ('push', 'workflow_dispatch')
# Runs of these events are neither evidence nor part of the newer-attempt scan.
OUTSIDE_EVENT_PREFIX = 'pull_request'
OUTSIDE_EVENTS = ('merge_group',)
COMPARE = ('release-runner', 'pins')
# runner_group_id and runner_group_name of a job on a GitHub-hosted runner.
HOSTED = (0, 'GitHub Actions')
RUNNER_KEYS = ('ImageOS', 'ImageVersion', 'RUNNER_OS', 'RUNNER_ARCH')
EVIDENCE_LIMIT = 256 * 1024
# evidence.json nests four levels; deeper JSON is refused before it is parsed, whatever the
# interpreter's recursion limit.
EVIDENCE_DEPTH = 16
ARTIFACT_LIMIT = 1024 * 1024
KEY = re.compile(r'[A-Za-z0-9_-]{1,64}')
REPOSITORY = re.compile(r'[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+')
PATH = re.compile(r'[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)*')
WORKFLOW_PATH = re.compile(r'\.github/workflows/[A-Za-z0-9_.-]+\.ya?ml')
TAG = re.compile(r'v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)')
# The same, with an optional SemVer prerelease: "-" and dot-separated identifiers of ASCII letters,
# digits and hyphens, none empty, a numeric one without leading zeroes. Build metadata never.
PRERELEASE_IDENTIFIER = r'(0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)'
TAG_PRERELEASE = re.compile(TAG.pattern + r'(-%s(\.%s)*)?' % (PRERELEASE_IDENTIFIER, PRERELEASE_IDENTIFIER))
# What a workflow_dispatch run of the release workflow validates: nothing (it does not use the
# gate), or the trusted branch's tip, as a validation-only decision.
DISPATCH = ('none', 'branch-tip')
# The decision each mode reaches when every check passes: only a publication reuses.
PASSED = {'publication': 'reuse', 'validation-only': 'validated'}
SHA = re.compile(r'[0-9a-f]{40}')
OBJECT_ID = re.compile(r'[0-9a-f]{40}|[0-9a-f]{64}')
DIGEST = re.compile(r'[0-9a-f]{64}')

# The runner identity manifest (templates/runner-identity.json), version 1: what a standard
# GitHub-hosted runner reports about its image, per variant. "reported" is the only identity kind:
# a hosted label is an address, and the runner's report is not a pinned VM image.
IDENTITY_VERSION = 1
IDENTITY_KIND = 'reported'
IDENTITY_RUNNER = ['ImageOS', 'ImageVersion', 'RUNNER_OS', 'RUNNER_ARCH', 'os_release']
IDENTITY_ACTIONS = 'full-commit-sha'
IDENTITY_OS = ('Linux', 'macOS', 'Windows')
IDENTITY_ARCH = ('X64', 'ARM64')
# What a job's evidence records of the manifest and its own variant and actions.
IDENTITY_BLOCK = ('manifest', 'sha256', 'revision', 'kind', 'variant', 'actions')
IDENTITY_LIMIT = 64 * 1024
# The form of the ImageVersion a hosted runner reports (20261026.180.1); "latest", a wildcard or
# any other form is not a reported image version.
IMAGE_VERSION = re.compile(r'[0-9]+(\.[0-9]+)+')
REVISION = re.compile(r'[A-Za-z0-9._-]{1,64}')
# A `uses:` key at the start of a block-style YAML line, and its value, bare or quoted. Every other
# line that may spell the key `uses` outside a comment line is one the reader cannot place, and
# refuses: a quoted key ("uses": or 'uses':), a space before the colon (uses :), an explicit key
# (? uses), a flow mapping ({uses: ...}), and any double-quoted key written with an escape, which
# could spell it.
USES_KEY = re.compile(r'^[ \t]*(?:-[ \t]+)?\buses:(.*)$', re.M)
USES_VALUE = re.compile(r'''[ \t]*(?:"([^"]*)"|'([^']*)'|([^\s#'"]+))[ \t]*(?:#.*)?''')
USES_SPELLINGS = (re.compile(r'''(?<![A-Za-z0-9_-])["']?uses["']?[ \t]*:'''),
                  re.compile(r'''^[ \t]*(?:-[ \t]+)*\?[ \t]*["']?uses["']?[ \t]*(?:#.*)?$'''),
                  re.compile(r'"[^"\n]*\\[^"\n]*"[ \t]*:'))
COMMENT_LINE = re.compile(r'[ \t]*#')
PINNED_ACTION = re.compile(r'[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)*@[0-9a-f]{40}')
PINNED_IMAGE = re.compile(r'docker://[^@\s]+@sha256:[0-9a-f]{64}')
LOCAL_ACTIONS = 32
# The manifest revisions this gate ships (templates/runner-identity.json), by the SHA-256 of their
# exact bytes: the policy is shared, so a locally edited manifest is not one of them.
IDENTITY_REVISIONS = {'1': '333c3ffe1f5d93a863298133ad15717c384146797c0cc88f2463a361e1e358da'}

# Pull-request-to-branch reuse (pull_request_reuse): the merge ref a pull request run checks out,
# the environment variable names a configuration may bind, and what a branch job records of the
# pull request evidence it reused.
PULL_MERGE_REF = re.compile(r'refs/pull/([1-9][0-9]*)/merge')
ENV_NAME = re.compile(r'[A-Za-z_][A-Za-z0-9_]{0,127}')
REUSED = ('pull_request', 'head', 'merge', 'base', 'tree', 'run_id', 'attempt', 'job_id', 'artifact',
          'checks')
PLAN_SCHEMA = 1


class Refusal(Exception):
    def __init__(self, code, detail=''):
        super().__init__(code)
        self.code = code
        self.detail = detail


class Drift(Refusal):
    """runtime-identity because a "release-runner" profile's recorded runner or tool identity
    differs from what this runner resolves (a hosted image rolled under the same label), and for
    no other reason. verify raises it only after every other check has passed; a publication that
    allows revalidation decides revalidate on it, and every other run refuses with its code.
    differences holds, per drifting job, each differing key's recorded and resolved values."""

    def __init__(self, detail, jobs, differences):
        super().__init__('runtime-identity', detail)
        self.jobs = jobs
        self.differences = differences


def need(condition, code, detail=''):
    if not condition:
        raise Refusal(code, detail)


def digest(data):
    return hashlib.sha256(data).hexdigest()


def command(argv, code, limit=8 * 1024 * 1024):
    # Spooled under TMPDIR: remote output is never accumulated unbounded in memory, and stderr,
    # which may echo a request, is never logged.
    with tempfile.TemporaryFile() as output:
        try:
            result = subprocess.run(argv, stdin=subprocess.DEVNULL, stdout=output,
                                    stderr=subprocess.DEVNULL, timeout=45, check=False)
        except (OSError, subprocess.TimeoutExpired) as error:
            raise Refusal(code, argv[0] + ' did not run') from error
        need(result.returncode == 0, code, '%s exited %d' % (argv[0], result.returncode))
        need(output.tell() <= limit, 'response-size', argv[0])
        output.seek(0)
        return output.read()


def git(*args, code='git-unavailable'):
    return command(['git', *args], code)


class API:
    """This repository's REST API through `gh api`, the token the workflow grants."""

    def __init__(self, repository):
        self.base = 'repos/' + repository

    def get(self, suffix, binary=False):
        raw = command(['gh', 'api', '--hostname', 'github.com', '-H',
                       'Accept: application/vnd.github+json', '-H',
                       'X-GitHub-Api-Version: 2026-03-10',
                       self.base + ('/' + suffix if suffix else '')],
                      'api-unavailable')
        return raw if binary else json.loads(raw)

    def pages(self, suffix, key):
        """Every row of a listing: stable total, full pages, unique ids, under 1,000 rows."""
        rows, count = [], None
        for page in range(1, 11):
            separator = '&' if '?' in suffix else '?'
            value = self.get(suffix + separator + 'per_page=100&page=' + str(page))
            total, part = value.get('total_count'), value.get(key)
            # Filtered workflow-run searches stop at 1,000 results.
            need(type(total) is int and 0 <= total < 1000, 'pagination-cap', suffix)
            need(isinstance(part, list) and len(part) <= 100, 'pagination-shape', suffix)
            need(count is None or count == total, 'pagination-changed', suffix)
            count = total
            rows.extend(part)
            need(len(rows) <= count, 'pagination-count', suffix)
            if len(rows) == count:
                need(len({row['id'] for row in rows}) == count, 'pagination-duplicate', suffix)
                return rows
            need(len(part) == 100, 'pagination-partial', suffix)
        raise Refusal('pagination-cap', suffix)


def timestamp(value):
    need(isinstance(value, str), 'timestamp')
    moment = dt.datetime.fromisoformat(value.replace('Z', '+00:00'))
    need(moment.tzinfo is not None, 'timestamp')
    return moment


def positive(value):
    return type(value) is int and value > 0


# --- configuration -------------------------------------------------------------------------------

def check_config(config):
    """Refuse anything but a complete schema 2 configuration. "$comment" is allowed, and ignored,
    in every object whose members are fixed names."""
    def fail(where, why):
        raise Refusal('config-invalid', where + ': ' + why)

    def fields(value, where, required, optional=()):
        if not isinstance(value, dict):
            fail(where, 'an object is required')
        unknown = sorted(set(value) - set(required) - set(optional) - {'$comment'})
        missing = sorted(set(required) - set(value))
        if unknown or missing:
            fail(where, 'unknown %s, missing %s' % (unknown, missing))
        note = value.get('$comment', '')
        if not (isinstance(note, str) or
                (isinstance(note, list) and all(isinstance(line, str) for line in note))):
            fail(where + '.$comment', 'a string or a list of strings is required')

    def text(value, where, pattern=None):
        if not isinstance(value, str) or not value or (pattern and not pattern.fullmatch(value)):
            fail(where, 'a string%s is required' % (' matching ' + pattern.pattern if pattern else ''))

    def texts(value, where, pattern=None, empty=True):
        if not isinstance(value, list) or (not value and not empty):
            fail(where, 'a list%s is required' % ('' if empty else ' with at least one entry'))
        for index, item in enumerate(value):
            text(item, '%s[%d]' % (where, index), pattern)
        if len(set(value)) != len(value):
            fail(where, 'an entry is repeated')

    def named(value, where, empty=False):
        if not isinstance(value, dict) or (not value and not empty):
            fail(where, 'an object%s is required' % ('' if empty else ' with at least one member'))
        for name in value:
            text(name, where + ' member name', KEY)

    def path(value, where, pattern=PATH):
        text(value, where, pattern)
        if any(part in ('.', '..') for part in value.split('/')):
            fail(where, 'a path without . or .. segments is required')

    fields(config, 'config', ['schema', 'repository', 'branch', 'max_age_hours', 'inputs',
                              'profiles', 'workflows'],
           ['fresh_at_release', 'tag', 'dispatch', 'revalidation', 'identity'])
    if config['schema'] != SCHEMA or type(config['schema']) is not int:
        fail('config.schema', 'must be %d' % SCHEMA)
    text(config['repository'], 'config.repository', REPOSITORY)
    path(config['branch'], 'config.branch')
    hours = config['max_age_hours']
    if type(hours) is not int or not 1 <= hours <= MAX_AGE_HOURS:
        fail('config.max_age_hours', 'an integer from 1 to %d is required' % MAX_AGE_HOURS)
    texts(config['inputs'], 'config.inputs')
    for index, item in enumerate(config['inputs']):
        path(item, 'config.inputs[%d]' % index)
    texts(config.get('fresh_at_release', []), 'config.fresh_at_release')
    tag = config.get('tag', {'prerelease': False})
    fields(tag, 'config.tag', ['prerelease'])
    if type(tag['prerelease']) is not bool:
        fail('config.tag.prerelease', 'true or false is required')
    dispatch = config.get('dispatch', {'validates': 'none'})
    fields(dispatch, 'config.dispatch', ['validates'])
    if dispatch['validates'] not in DISPATCH:
        fail('config.dispatch.validates', 'one of %s is required' % (DISPATCH,))
    revalidation = config.get('revalidation', {'allowed': False})
    fields(revalidation, 'config.revalidation', ['allowed'])
    if type(revalidation['allowed']) is not bool:
        fail('config.revalidation.allowed', 'true or false is required')
    identity = config.get('identity')
    if identity is not None:
        fields(identity, 'config.identity', ['manifest', 'sha256'])
        path(identity['manifest'], 'config.identity.manifest')
        text(identity['sha256'], 'config.identity.sha256', DIGEST)

    named(config['profiles'], 'config.profiles')
    for name, profile in config['profiles'].items():
        where = 'config.profiles.' + name
        fields(profile, where, ['platform', 'compare', 'tools'], ['pins', 'variant'])
        fields(profile['platform'], where + '.platform', ['RUNNER_OS', 'RUNNER_ARCH'], ['ImageOS'])
        # Under the identity policy each profile names its manifest variant and pins the image name
        # its runner reports; without it a variant means nothing and is refused.
        if identity is None and 'variant' in profile:
            fail(where + '.variant', 'a variant needs config.identity')
        if identity is not None:
            if 'variant' not in profile:
                fail(where + '.variant', 'config.identity needs a variant for every profile')
            text(profile['variant'], where + '.variant', KEY)
            if 'ImageOS' not in profile['platform']:
                fail(where + '.platform.ImageOS', 'config.identity needs the image name for every profile')
            if not profile['tools']:
                fail(where + '.tools', 'config.identity needs at least one tool for every profile')
        for key, value in profile['platform'].items():
            if key != '$comment':
                text(value, '%s.platform.%s' % (where, key))
        if profile['compare'] not in COMPARE:
            fail(where + '.compare', 'one of %s is required' % (COMPARE,))
        named(profile['tools'], where + '.tools', empty=True)
        for tool, argv in profile['tools'].items():
            texts(argv, '%s.tools.%s' % (where, tool), empty=False)
        pins = profile.get('pins', {})
        named(pins, where + '.pins', empty=True)
        for tool, pin in pins.items():
            text(pin, '%s.pins.%s' % (where, tool))
        if not set(pins) <= set(profile['tools']):
            fail(where + '.pins', 'every pin names a tool of the profile')
        if profile['compare'] == 'pins' and (not pins or set(pins) != set(profile['tools'])):
            fail(where + '.pins', 'compare "pins" needs a pin for every tool, and a tool')

    named(config['workflows'], 'config.workflows')
    for key, workflow in config['workflows'].items():
        where = 'config.workflows.' + key
        fields(workflow, where, ['path', 'name', 'events', 'jobs'], ['skipped_jobs', 'pull_request_reuse'])
        path(workflow['path'], where + '.path', WORKFLOW_PATH)
        text(workflow['name'], where + '.name')
        texts(workflow['events'], where + '.events', empty=False)
        if not set(workflow['events']) <= set(EVENTS):
            fail(where + '.events', 'only %s are trusted-branch events' % (EVENTS,))
        reuse = workflow.get('pull_request_reuse')
        if reuse is not None:
            fields(reuse, where + '.pull_request_reuse', ['checks'], ['environment'])
            texts(reuse['checks'], where + '.pull_request_reuse.checks', empty=False)
            texts(reuse.get('environment', []), where + '.pull_request_reuse.environment', ENV_NAME)
            if 'push' not in workflow['events']:
                fail(where + '.pull_request_reuse', 'reuse is planned on a push: events must hold push')
        texts(workflow.get('skipped_jobs', []), where + '.skipped_jobs')
        named(workflow['jobs'], where + '.jobs')
        names = list(workflow.get('skipped_jobs', []))
        for job_key, spec in workflow['jobs'].items():
            at = '%s.jobs.%s' % (where, job_key)
            fields(spec, at, ['name', 'job_id', 'labels', 'profile', 'checks'],
                   ['optional_skips', 'setup'])
            text(spec['name'], at + '.name')
            text(spec['job_id'], at + '.job_id', KEY)
            texts(spec['labels'], at + '.labels', empty=False)
            if spec['profile'] not in config['profiles']:
                fail(at + '.profile', 'names no profile')
            if spec['checks'] != 'all':
                texts(spec['checks'], at + '.checks')
            texts(spec.get('optional_skips', []), at + '.optional_skips')
            if spec['checks'] != 'all' and not set(spec.get('optional_skips', [])) <= set(spec['checks']):
                fail(at + '.optional_skips', 'every optional skip names a check of the job')
            if type(spec.get('setup', False)) is not bool:
                fail(at + '.setup', 'true or false is required')
            names.append(spec['name'])
        if len(names) != len(set(names)):
            fail(where, 'a job name is repeated')


def tag_grammar(config):
    """vMAJOR.MINOR.PATCH, and a SemVer prerelease only where the configuration allows one."""
    return TAG_PRERELEASE if config.get('tag', {}).get('prerelease') is True else TAG


def revalidation_allowed(config):
    """Whether a publication may decide revalidate on the release runner's drift: only where
    revalidation.allowed is true. Absent, it is false."""
    return config.get('revalidation', {}).get('allowed') is True


def release_mode(config):
    """What this run decides. A workflow_dispatch run is a validation of the trusted branch's tip,
    never a publication, where dispatch.validates is "branch-tip", and is refused otherwise; every
    other run is a publication, held to its tag."""
    if os.environ.get('GITHUB_EVENT_NAME') != 'workflow_dispatch':
        return 'publication'
    need(config.get('dispatch', {}).get('validates') == 'branch-tip', 'dispatch-not-gated',
         'a workflow_dispatch run does not use the gate (dispatch.validates is "none")')
    need(os.environ.get('GITHUB_REF') == 'refs/heads/' + config['branch'], 'dispatch-ref',
         'a validation run is dispatched on %s, not %s' % ('refs/heads/' + config['branch'],
                                                          os.environ.get('GITHUB_REF')))
    return 'validation-only'


def helper_path(root):
    try:
        return Path(__file__).resolve().relative_to(root.resolve()).as_posix()
    except ValueError as error:
        raise Refusal('helper-source', 'the helper does not run from the checkout') from error


def source_context(config_path):
    """The release commit and every input identity, read from the checkout of that commit."""
    sha = os.environ.get('GITHUB_SHA', '')
    need(SHA.fullmatch(sha), 'source-sha')
    need(PATH.fullmatch(config_path) and not {'.', '..'} & set(config_path.split('/')), 'config-source')
    root = Path(git('rev-parse', '--show-toplevel').decode().strip())
    need(root.resolve() == Path.cwd().resolve(), 'checkout-root')
    need(git('rev-parse', 'HEAD').decode().strip() == sha, 'checkout-sha')
    need(git('status', '--porcelain', '--untracked-files=no') == b'', 'checkout-changed')
    committed = git('show', sha + ':' + config_path, code='config-source')
    need(Path(config_path).read_bytes() == committed, 'config-source')
    config = json.loads(committed)
    check_config(config)
    need(config['repository'] == os.environ.get('GITHUB_REPOSITORY'), 'context-repository')
    repository_id = int(os.environ.get('GITHUB_REPOSITORY_ID', '0'))
    need(repository_id > 0, 'context-repository-id')
    helper = helper_path(root)
    need(Path(helper).read_bytes() == git('show', sha + ':' + helper, code='helper-source'),
         'helper-source')
    # The manifest, the configuration, the helper and each workflow file are inputs whatever the
    # configuration lists, and so is the runner identity manifest where one is named; each is
    # identified by its git object id at the commit.
    paths = sorted(set(config['inputs']) | {config_path, 'dev.json', helper} |
                   {workflow['path'] for workflow in config['workflows'].values()} |
                   ({config['identity']['manifest']} if 'identity' in config else set()))
    ids = git('rev-parse', *[sha + ':' + path for path in paths], code='input-missing').decode().split()
    need(len(ids) == len(paths) and all(OBJECT_ID.fullmatch(oid) for oid in ids), 'input-missing')
    return config, {'schema': SCHEMA, 'repository': config['repository'],
                    'repository_id': repository_id, 'source': sha, 'inputs': dict(zip(paths, ids))}


# --- the runner identity manifest ----------------------------------------------------------------

def unique_members(pairs):
    names = [name for name, _ in pairs]
    need(len(names) == len(set(names)), 'identity-config', 'a member is repeated')
    return dict(pairs)


def parse_identity_manifest(raw):
    """The manifest's bytes, refused unless they are exactly a version 1 manifest: bounded, UTF-8
    JSON with no repeated member, no unknown or missing member, the identity kind "reported", the
    fixed runner fields and action rule, and each variant an OS, an architecture and the absolute
    path of the file whose digest is the OS-release identity (null where the OS has none)."""
    def fail(where, why):
        raise Refusal('identity-config', where + ': ' + why)

    def fields(value, where, required):
        if not isinstance(value, dict):
            fail(where, 'an object is required')
        unknown = sorted(set(value) - set(required) - {'$comment'})
        missing = sorted(set(required) - set(value))
        if unknown or missing:
            fail(where, 'unknown %s, missing %s' % (unknown, missing))

    need(len(raw) <= IDENTITY_LIMIT, 'identity-config', 'the manifest is over %d bytes' % IDENTITY_LIMIT)
    try:
        manifest = json.loads(raw.decode('utf-8'), object_pairs_hook=unique_members)
    except (UnicodeDecodeError, ValueError, RecursionError) as error:
        raise Refusal('identity-config', 'the manifest is not JSON') from error
    fields(manifest, 'manifest', ['manifestVersion', 'revision', 'identity', 'runner', 'actions',
                                  'variants'])
    if type(manifest['manifestVersion']) is not int or manifest['manifestVersion'] != IDENTITY_VERSION:
        fail('manifest.manifestVersion', 'must be %d' % IDENTITY_VERSION)
    if not isinstance(manifest['revision'], str) or not REVISION.fullmatch(manifest['revision']):
        fail('manifest.revision', 'a string matching %s is required' % REVISION.pattern)
    if manifest['identity'] != IDENTITY_KIND:
        fail('manifest.identity', 'must be "%s": a hosted runner reports its image, nothing pins it'
             % IDENTITY_KIND)
    if manifest['runner'] != IDENTITY_RUNNER:
        fail('manifest.runner', 'must be %s' % IDENTITY_RUNNER)
    if manifest['actions'] != IDENTITY_ACTIONS:
        fail('manifest.actions', 'must be "%s"' % IDENTITY_ACTIONS)
    variants = manifest['variants']
    if not isinstance(variants, dict) or not variants:
        fail('manifest.variants', 'an object with at least one member is required')
    for name, variant in variants.items():
        where = 'manifest.variants.' + name
        if not KEY.fullmatch(name):
            fail(where, 'a variant name matches %s' % KEY.pattern)
        fields(variant, where, ['RUNNER_OS', 'RUNNER_ARCH', 'os_release'])
        if variant['RUNNER_OS'] not in IDENTITY_OS:
            fail(where + '.RUNNER_OS', 'one of %s is required' % (IDENTITY_OS,))
        if variant['RUNNER_ARCH'] not in IDENTITY_ARCH:
            fail(where + '.RUNNER_ARCH', 'one of %s is required' % (IDENTITY_ARCH,))
        release = variant['os_release']
        if release is not None and not (isinstance(release, str) and release.startswith('/') and
                                        '..' not in release.split('/') and '\n' not in release):
            fail(where + '.os_release', 'an absolute path or null is required')
    return manifest


def workflow_actions(sha, path, seen):
    """Every action and reusable workflow the file names at the commit, local ones followed: a local
    path (bound by the commit), an action or workflow at a full commit id, or a container image by
    digest. A tag, a branch, an expression or a container tag refuses: its resolution can change
    while the file does not."""
    if path in seen:
        return set()
    seen.add(path)
    need(len(seen) <= LOCAL_ACTIONS, 'identity-unexpected', 'more than %d local actions' % LOCAL_ACTIONS)
    text = git('show', sha + ':' + path, code='identity-unexpected').decode('utf-8', 'replace')
    keys = USES_KEY.findall(text)
    spelled = [line for line in text.splitlines() if not COMMENT_LINE.match(line) and
               any(pattern.search(line) for pattern in USES_SPELLINGS)]
    need(len(keys) == len(spelled), 'identity-unexpected',
         '%s has a uses key that is not a plain block-style key of its own line' % path)
    found = set()
    for value in keys:
        match = USES_VALUE.fullmatch(value)
        need(match is not None, 'identity-unexpected', '%s: uses:%s cannot be read' % (path, value))
        ref = next(group for group in match.groups() if group is not None)
        if ref.startswith('./'):
            local = ref[2:].rstrip('/')
            need(PATH.fullmatch(local) and not {'.', '..'} & set(local.split('/')), 'identity-unexpected',
                 '%s names the local action %r' % (path, ref))
            found.add(ref)
            if WORKFLOW_PATH.fullmatch(local):
                found |= workflow_actions(sha, local, seen)
                continue
            listed = git('ls-tree', '--name-only', sha, '--', local + '/action.yml', local + '/action.yaml',
                         code='identity-unexpected').decode().split()
            need(len(listed) == 1, 'identity-unexpected', '%s: no single action.yml in %s' % (path, local))
            found |= workflow_actions(sha, listed[0], seen)
        else:
            need(PINNED_ACTION.fullmatch(ref) or PINNED_IMAGE.fullmatch(ref), 'identity-unexpected',
                 '%s names %s, which is not pinned to a full commit (or an image digest)' % (path, ref))
            found.add(ref)
    return found


def actions_of(sha, path):
    return sorted(workflow_actions(sha, path, set()))


def identity_context(config, sha):
    """The identity policy at the commit: the manifest, held to the digest the configuration pins,
    its record, each profile's variant (the profile's platform is the variant's), and the actions
    each configured workflow names, every one pinned."""
    setting = config['identity']
    raw = git('show', sha + ':' + setting['manifest'], code='identity-config')
    need(digest(raw) == setting['sha256'], 'identity-config',
         '%s is not the manifest the configuration pins' % setting['manifest'])
    manifest = parse_identity_manifest(raw)
    need(IDENTITY_REVISIONS.get(manifest['revision']) == setting['sha256'], 'identity-config',
         'revision %s with these bytes is not a manifest revision this gate ships' % manifest['revision'])
    for name, profile in config['profiles'].items():
        variant = manifest['variants'].get(profile['variant'])
        need(variant is not None, 'identity-config', '%s names no variant of the manifest' % name)
        need(all(profile['platform'][key] == variant[key] for key in ('RUNNER_OS', 'RUNNER_ARCH')),
             'identity-config', '%s: its platform is not the variant %s' % (name, profile['variant']))
    return {'record': {'manifest': setting['manifest'], 'sha256': setting['sha256'],
                       'revision': manifest['revision'], 'kind': manifest['identity']},
            'variants': manifest['variants'],
            'actions': {key: actions_of(sha, workflow['path'])
                        for key, workflow in config['workflows'].items()}}


def release_workflow_actions(source):
    """The actions of the release workflow that is running (GITHUB_WORKFLOW_REF), every one pinned."""
    ref = os.environ.get('GITHUB_WORKFLOW_REF', '')
    prefix = source['repository'] + '/'
    path = ref[len(prefix):].split('@', 1)[0] if ref.startswith(prefix) else ''
    need(WORKFLOW_PATH.fullmatch(path), 'identity-missing', 'GITHUB_WORKFLOW_REF names no workflow file')
    return {'path': path, 'actions': actions_of(source['source'], path)}


def job_identity(context, profile, workflow_key):
    """What a job's evidence records of the policy: the manifest, its variant and its actions."""
    return dict(context['record'], variant=profile['variant'], actions=context['actions'][workflow_key])


def check_identity(context, profile, payload, workflow_key, job_key):
    """The job's recorded identity, before its runtime is compared: every field the variant has,
    each of the form a hosted runner reports, the image name the profile pins and the variant's
    platform, under the manifest and actions of the commit. A missing field is identity-missing,
    any other value identity-unexpected; neither is a drift, and neither is revalidated."""
    block = payload.get('identity')
    need(isinstance(block, dict) and set(block) == set(IDENTITY_BLOCK), 'identity-missing',
         job_key + ': no identity record')
    expected = job_identity(context, profile, workflow_key)
    need(block == expected, 'identity-unexpected', '%s: %s differ from the commit\'s' % (
        job_key, sorted(key for key in IDENTITY_BLOCK if block.get(key) != expected[key])))
    variant = context['variants'][profile['variant']]
    recorded = payload.get('runtime')
    runner = recorded.get('runner') if isinstance(recorded, dict) else None
    tools = recorded.get('tools') if isinstance(recorded, dict) else None
    need(isinstance(runner, dict) and isinstance(tools, dict), 'identity-missing', job_key + ': no runtime record')
    wanted = [key for key in IDENTITY_RUNNER if key != 'os_release' or variant['os_release'] is not None]
    missing = [key for key in wanted if not isinstance(runner.get(key), str) or not runner.get(key)]
    need(not missing, 'identity-missing', '%s: %s' % (job_key, missing))
    need(set(runner) == set(wanted), 'identity-unexpected', '%s: runner fields %s' % (job_key, sorted(runner)))
    need(tools and all(isinstance(value, str) and value for value in tools.values()), 'identity-missing',
         job_key + ': the tools')
    need(runner['ImageOS'] == profile['platform']['ImageOS'], 'identity-unexpected',
         '%s: ImageOS is %s, the profile pins %s' % (job_key, runner['ImageOS'], profile['platform']['ImageOS']))
    need((runner['RUNNER_OS'], runner['RUNNER_ARCH']) == (variant['RUNNER_OS'], variant['RUNNER_ARCH']),
         'identity-unexpected', '%s: %s %s is not the variant %s' % (
             job_key, runner['RUNNER_OS'], runner['RUNNER_ARCH'], profile['variant']))
    need(IMAGE_VERSION.fullmatch(runner['ImageVersion']), 'identity-unexpected',
         '%s: ImageVersion %r is not a reported image version' % (job_key, runner['ImageVersion']))
    need(variant['os_release'] is None or DIGEST.fullmatch(runner['os_release']), 'identity-unexpected',
         job_key + ': os_release is not a digest')


# --- the evidence a CI job records --------------------------------------------------------------

def runtime(profile, variant=None):
    """This runner's image, platform and tool identity, for a profile. Under the identity policy
    (a variant), the image version must have the form a hosted runner reports, and the OS-release
    digest is that of the variant's file, on every OS that has one."""
    runner = {key: os.environ.get(key, '') for key in RUNNER_KEYS}
    need(all(runner.values()), 'runner-identity')
    for key, value in profile['platform'].items():
        if key != '$comment':
            need(runner[key] == value, 'runner-platform', '%s is %s, not %s' % (key, runner[key], value))
    if variant is not None:
        need(IMAGE_VERSION.fullmatch(runner['ImageVersion']), 'identity-unexpected',
             'ImageVersion %r is not a reported image version' % runner['ImageVersion'])
        if variant['os_release'] is not None:
            try:
                runner['os_release'] = digest(Path(variant['os_release']).read_bytes())
            except OSError as error:
                raise Refusal('identity-missing', 'no OS-release file ' + variant['os_release']) from error
    elif runner['RUNNER_OS'] == 'Linux':
        runner['os_release'] = digest(Path('/etc/os-release').read_bytes())
    tools = {name: command(argv, 'tool-identity', 65536).decode().strip()
             for name, argv in profile['tools'].items()}
    need(all(tools.values()), 'tool-identity')
    return {'runner': runner, 'tools': tools}


def required_checks(spec):
    checks = spec['checks']
    if checks == 'all':
        manifest = json.loads(Path('dev.json').read_text(encoding='utf-8'))
        checks = [row['name'] for row in manifest['checks']]
        if 'parity' in manifest:
            checks.append('parity')
    result = list(checks)
    if spec.get('setup'):
        manifest = json.loads(Path('dev.json').read_text(encoding='utf-8'))
        result += ['setup:' + row['name'] for row in manifest['setup']]
    need(len(result) == len(set(result)), 'check-config')
    return result


def log_rows(logs):
    result = {}
    for kind, path in logs:
        text = Path(path).read_text(encoding='utf-8')
        need('infrastructure failure:' not in text, 'check-infrastructure')
        for status, name in re.findall(r'^(PASS|SKIP|FAIL|NOT RUN)\s+(\S+)(?:\s|$)', text, re.M):
            key = ('setup:' if kind == 'setup' else '') + name
            need(key not in result, 'check-duplicate', key)
            result[key] = status
    return result


def coverage(spec, logs, reused=()):
    """The job's check results from its logs; a check the pull request evidence covered (reused)
    must not have run here, and is recorded as REUSED."""
    result = log_rows(logs)
    need(not set(reused) & set(result), 'check-coverage', 'reused checks also ran: %s' % sorted(
        set(reused) & set(result)))
    result.update({name: 'REUSED' for name in reused})
    validate_coverage(spec, result, reused)
    return result


def validate_coverage(spec, result, reused=()):
    """Every required check, each PASS, SKIP where the configuration allows it, or REUSED where the
    job's pull request evidence covered it."""
    need(isinstance(result, dict), 'check-coverage')
    required = set(required_checks(spec))
    need(set(result) == required, 'check-coverage', 'missing %s, unexpected %s' % (
        sorted(required - set(result)), sorted(set(result) - required)))
    for name, status in sorted(result.items()):
        need(status == 'PASS' or (status == 'SKIP' and name in spec.get('optional_skips', [])) or
             (status == 'REUSED' and name in reused), 'check-result', '%s %s' % (status, name))


def validate_scoped_coverage(spec, result):
    """A pull request job's results: the checks its scoped selection ran, each a required check of
    the job, PASS or a permitted SKIP. A check it did not run is absent, and covers nothing."""
    need(isinstance(result, dict), 'check-coverage')
    required = set(required_checks(spec))
    need(set(result) <= required, 'check-coverage', 'unexpected %s' % sorted(set(result) - required))
    for name, status in sorted(result.items()):
        need(status == 'PASS' or (status == 'SKIP' and name in spec.get('optional_skips', [])),
             'check-result', '%s %s' % (status, name))


def environment_of(workflow):
    """The SHA-256 of each configured environment variable's value in this step, null when unset.
    The plan compares it before the checks run; what is recorded is the checks' own (below)."""
    names = workflow.get('pull_request_reuse', {}).get('environment', [])
    return {name: digest(os.environ[name].encode()) if name in os.environ else None for name in names}


ENVIRONMENT_LINE = re.compile(r'^environment: (.*)$', re.M)


def log_environment(workflow, logs, sha):
    """The environment the checks ran with, from the environment: line check.sh prints in each log
    where JPDEV_BIND_ENV names the configured variables: the manifest check.sh read must be the
    committed dev.json, and each configured variable is there, once, with its value's SHA-256 (null
    when unset). A log without the line, two logs that differ, or another set of names refuses
    (check-environment): the flags of the step that ran the checks are bound, not the capture's."""
    names = workflow['pull_request_reuse'].get('environment', [])
    lines = []
    for _, path in logs:
        found = ENVIRONMENT_LINE.findall(Path(path).read_text(encoding='utf-8'))
        need(len(found) == 1, 'check-environment', '%s has %d environment: lines' % (path, len(found)))
        lines.append(found[0].strip())
    need(lines and len(set(lines)) == 1, 'check-environment', 'the logs ran with different environments')
    pairs = [token.split('=', 1) for token in lines[0].split(' ')]
    need(all(len(pair) == 2 for pair in pairs) and pairs[0][0] == 'manifest', 'check-environment',
         'the environment: line cannot be read')
    need(pairs[0][1] == digest(git('show', sha + ':dev.json', code='check-environment')), 'check-environment',
         'the checks read another manifest than the committed dev.json')
    found = dict(pairs[1:])
    need(len(found) == len(pairs) - 1 and set(found) == set(names), 'check-environment',
         'the checks bound %s, the configuration names %s (set JPDEV_BIND_ENV)' % (sorted(found), sorted(names)))
    need(all(value == 'unset' or DIGEST.fullmatch(value) for value in found.values()), 'check-environment',
         'a value is not a digest')
    return {name: None if found[name] == 'unset' else found[name] for name in names}


def evidence(source, workflow_key, workflow, job_key, run_id, attempt, event, branch, runtime_identity,
             check_results, identity=None, ref=None, extra=None):
    """The one shape of a job's evidence: what capture writes and what verify expects. Under the
    identity policy it also holds the job's identity record; under pull_request_reuse, extra holds
    the environment and either what was reused (a branch run) or the pull request (its run)."""
    result = dict(source, workflow=workflow_key, workflow_path=workflow['path'],
                  workflow_name=workflow['name'], job=job_key, run_id=run_id, attempt=attempt,
                  event=event, ref=ref or 'refs/heads/' + branch, runtime=runtime_identity,
                  coverage=check_results)
    if identity is not None:
        result['identity'] = identity
    result.update(extra or {})
    return result


def capture_context(config, source, workflow_key, job_key):
    """The configured workflow and job this capture or plan runs in, and the ref it runs on: the
    trusted branch, or, on a pull_request run of a workflow that configures pull_request_reuse, the
    pull request's merge ref into the trusted branch. The running workflow file is the configured
    one, at the commit being recorded."""
    workflow = config['workflows'].get(workflow_key)
    need(workflow is not None, 'capture-job', 'no workflow %s' % workflow_key)
    spec = workflow['jobs'].get(job_key)
    need(spec is not None, 'capture-job', 'no job %s' % job_key)
    event = os.environ.get('GITHUB_EVENT_NAME')
    if event == 'pull_request':
        need('pull_request_reuse' in workflow, 'capture-event', 'the workflow configures no pull_request_reuse')
        ref = os.environ.get('GITHUB_REF', '')
        need(PULL_MERGE_REF.fullmatch(ref) and os.environ.get('GITHUB_BASE_REF') == config['branch'],
             'capture-ref', 'a pull request into %s, at its merge ref' % config['branch'])
    else:
        ref = 'refs/heads/' + config['branch']
        need(os.environ.get('GITHUB_REF') == ref, 'capture-ref')
        need(event in workflow['events'], 'capture-event')
    need(os.environ.get('GITHUB_JOB') == spec['job_id'], 'capture-job')
    need(os.environ.get('GITHUB_WORKFLOW_REF') == '%s/%s@%s' % (source['repository'],
                                                                workflow['path'], ref) and
         os.environ.get('GITHUB_WORKFLOW_SHA') == source['source'], 'capture-workflow')
    run_id, attempt = int(os.environ['GITHUB_RUN_ID']), int(os.environ['GITHUB_RUN_ATTEMPT'])
    need(positive(run_id) and positive(attempt), 'capture-run')
    return workflow, spec, event, ref, run_id, attempt


def job_runtime(config, source, workflow_key, spec):
    """This runner's runtime for the job's profile and, under the identity policy, its identity."""
    profile = config['profiles'][spec['profile']]
    identity, variant = None, None
    if 'identity' in config:
        context = identity_context(config, source['source'])
        identity, variant = job_identity(context, profile, workflow_key), context['variants'][profile['variant']]
    return runtime(profile, variant), identity


def merge_parents(sha):
    """The pull request merge commit's tree and parents, base first, then the pull request head."""
    parents = git('rev-parse', sha + '^@', code='capture-merge').decode().split()
    need(len(parents) == 2 and all(SHA.fullmatch(parent) for parent in parents), 'capture-merge',
         'the checkout is not a merge commit with its two parents (fetch-depth 2 at least)')
    tree = git('rev-parse', sha + '^{tree}', code='capture-merge').decode().strip()
    return tree, parents


def read_plan(path, source, workflow_key, job_key, attempt):
    """The reuse decision this job's reuse step wrote: the checks it covered and their provenance,
    or nothing on a fresh decision. The plan is the same job's, for the same commit and attempt."""
    if path is None:
        return None
    plan = json.loads(Path(path).read_text(encoding='utf-8'))
    need(isinstance(plan, dict) and plan.get('schema') == PLAN_SCHEMA and
         (plan.get('source'), plan.get('workflow'), plan.get('job'), plan.get('attempt')) ==
         (source['source'], workflow_key, job_key, attempt) and plan.get('decision') in ('reuse', 'fresh'),
         'capture-plan', 'the reuse plan is not this job\'s')
    return plan['reused'] if plan['decision'] == 'reuse' else None


def capture(config, source, workflow_key, job_key, logs, plan_path=None):
    workflow, spec, event, ref, run_id, attempt = capture_context(config, source, workflow_key, job_key)
    runtime_identity, identity = job_runtime(config, source, workflow_key, spec)
    extra = None
    if event == 'pull_request':
        # The pull request's evidence: the checks its scoped selection ran, the merge commit's tree
        # and parents, and the environment; never evidence for a release by itself.
        need(plan_path is None, 'capture-plan', 'a pull request run plans no reuse')
        tree, parents = merge_parents(source['source'])
        results = log_rows(logs)
        validate_scoped_coverage(spec, results)
        extra = {'pull_request': {'number': int(PULL_MERGE_REF.fullmatch(ref).group(1)), 'base': parents[0],
                                  'head': parents[1], 'tree': tree},
                 'environment': log_environment(workflow, logs, source['source'])}
    else:
        reused = read_plan(plan_path, source, workflow_key, job_key, attempt)
        need(reused is None or 'pull_request_reuse' in workflow, 'capture-plan')
        results = coverage(spec, logs, reused['checks'] if reused else ())
        if 'pull_request_reuse' in workflow:
            extra = {'environment': log_environment(workflow, logs, source['source']), 'reused': reused}
    return evidence(source, workflow_key, workflow, job_key, run_id, attempt, event, config['branch'],
                    runtime_identity, results, identity, ref, extra)


# --- the release's verification -----------------------------------------------------------------

def run_identity(run, source, workflow, workflow_id, branch):
    repository = (source['repository'], source['repository_id'])
    need((run['repository']['full_name'], run['repository']['id']) == repository and
         (run['head_repository']['full_name'], run['head_repository']['id']) == repository,
         'run-repository', 'run %s' % run.get('id'))
    need(run['head_sha'] == source['source'], 'run-sha', 'run %s is at %s' % (run.get('id'), run['head_sha']))
    need(run['workflow_id'] == workflow_id and run['path'] == workflow['path'], 'run-workflow',
         'run %s' % run.get('id'))
    need(run['head_branch'] == branch and run['event'] in workflow['events'] and
         run['pull_requests'] == [], 'run-trigger', 'run %s' % run.get('id'))
    need(positive(run['id']) and positive(run['run_attempt']), 'run-identity')


def attempt_order(run):
    return (timestamp(run['run_started_at']), timestamp(run['created_at']), run['id'])


def select(api, source, workflow, workflow_id, branch, trail=None):
    """The newest applicable attempt, never an older green one.

    Applicable: a run of this workflow at the exact commit, on the trusted branch, by a trusted
    event; only an applicable run is evidence. Every run of this repository on the trusted branch
    at the commit, whatever its trigger, takes part in the scan: none may be unfinished, and none
    whose latest attempt started after the selected one's may have failed. The selected run is the
    applicable one whose latest attempt started last, whatever its result."""
    runs = api.pages('actions/workflows/%d/runs?head_sha=%s' % (workflow_id, source['source']),
                     'workflow_runs')
    applicable = [run for run in runs
                  if run.get('head_branch') == branch and run.get('event') in workflow['events']]
    if trail is not None:
        trail['runs_at_commit'] = len(runs)
        trail['applicable'] = len(applicable)
        newest = sorted(runs, key=lambda run: str(run.get('run_started_at')), reverse=True)
        trail['candidates'] = [{key: run.get(key) for key in ('id', 'run_attempt', 'event', 'head_branch',
                                                              'status', 'conclusion', 'run_started_at')}
                               for run in newest[:20]]
    need(applicable, 'run-missing', 'no %s run of %s at %s' % ('/'.join(workflow['events']),
                                                               workflow['path'], source['source']))
    for run in applicable:
        run_identity(run, source, workflow, workflow_id, branch)
    # A schedule, workflow_run or repository_dispatch run on the trusted branch is scanned. A pull
    # request or merge queue run, another branch's run or another head repository's run tests
    # other code or can be started from outside: never evidence, and never a block either.
    scanned = [run for run in runs if run.get('head_branch') == branch and
               not str(run.get('event')).startswith(OUTSIDE_EVENT_PREFIX) and
               run.get('event') not in OUTSIDE_EVENTS and
               (run.get('head_repository') or {}).get('id') == source['repository_id']]
    unfinished = [run['id'] for run in scanned if run['status'] != 'completed']
    need(not unfinished, 'run-unfinished', 'runs %s have not completed' % unfinished)
    chosen = max(applicable, key=attempt_order)
    newer = ['%s (%s, %s)' % (run['id'], run['event'], run['conclusion']) for run in scanned
             if attempt_order(run) > attempt_order(chosen) and run['conclusion'] != 'success']
    need(not newer, 'run-newer-failed', 'runs %s started after run %s and did not succeed' % (
        newer, chosen['id']))
    return chosen


def selection(run):
    return (run['id'], run['run_attempt'], run['status'], run['conclusion'])


def tag_object(api, name, sha, grammar=TAG):
    """The annotated tag's object id, after checking that the tag peels to the release commit."""
    need(isinstance(name, str) and grammar.fullmatch(name), 'tag-syntax')
    ref = api.get('git/ref/tags/' + quote(name, safe=''))
    need(ref['ref'] == 'refs/tags/' + name and ref['object']['type'] == 'tag', 'tag-annotated')
    original = ref['object']['sha']
    obj = ref['object']
    for _ in range(5):
        if obj['type'] != 'tag':
            break
        obj = api.get('git/tags/' + obj['sha'])['object']
    need(obj['type'] == 'commit' and obj['sha'] == sha, 'tag-commit',
         '%s names %s %s' % (name, obj['type'], obj['sha']))
    return original


def check_jobs(jobs, run, workflow, branch):
    names = [job['name'] for job in jobs]
    expected = [spec['name'] for spec in workflow['jobs'].values()] + workflow.get('skipped_jobs', [])
    need(len(names) == len(set(names)), 'job-coverage', 'a job name is repeated')
    need(set(names) == set(expected), 'job-coverage', 'missing %s, unexpected %s' % (
        sorted(set(expected) - set(names)), sorted(set(names) - set(expected))))
    by_name = {job['name']: job for job in jobs}
    for job in jobs:
        need(job['run_id'] == run['id'] and job['run_attempt'] == run['run_attempt'] and
             job['head_sha'] == run['head_sha'] and job['head_branch'] == branch and
             job['workflow_name'] == workflow['name'], 'job-identity', job['name'])
        need(job['status'] == 'completed', 'job-result', '%s is %s' % (job['name'], job['status']))
    for spec in workflow['jobs'].values():
        job = by_name[spec['name']]
        need(job['conclusion'] != 'skipped', 'job-skipped', spec['name'])
        need(job['conclusion'] == 'success', 'job-result', '%s: %s' % (spec['name'], job['conclusion']))
        need(job['labels'] == spec['labels'] and
             (job['runner_group_id'], job['runner_group_name']) == HOSTED, 'job-runner', spec['name'])
    for name in workflow.get('skipped_jobs', []):
        need(by_name[name]['conclusion'] == 'skipped', 'job-not-skipped',
             '%s: %s' % (name, by_name[name]['conclusion']))
    return by_name


def nesting_within(data, limit):
    """Whether no array or object in the JSON text nests deeper than limit (strings skipped)."""
    depth, in_string, escaped = 0, False, False
    for byte in data:
        if in_string:
            if escaped:
                escaped = False
            elif byte == 0x5c:
                escaped = True
            elif byte == 0x22:
                in_string = False
        elif byte == 0x22:
            in_string = True
        elif byte in (0x5b, 0x7b):
            depth += 1
            if depth > limit:
                return False
        elif byte in (0x5d, 0x7d):
            depth -= 1
    return True


def read_artifact(api, artifact, run, job):
    need(artifact['expired'] is False, 'artifact-expired', artifact['name'])
    size = artifact['size_in_bytes']
    need(type(size) is int and 0 < size <= ARTIFACT_LIMIT, 'artifact-size', artifact['name'])
    origin = artifact['workflow_run']
    need(origin['id'] == run['id'] and origin['head_sha'] == run['head_sha'] and
         origin['head_branch'] == run['head_branch'] and
         origin['repository_id'] == run['repository']['id'] and
         origin['head_repository_id'] == run['repository']['id'], 'artifact-run', artifact['name'])
    need(timestamp(job['started_at']) <= timestamp(artifact['created_at']) <=
         timestamp(job['completed_at']), 'artifact-interval', artifact['name'])
    need(positive(artifact['id']), 'artifact-run', artifact['name'])
    raw = api.get('actions/artifacts/%d/zip' % artifact['id'], binary=True)
    need(len(raw) == size and artifact['digest'] == 'sha256:' + digest(raw), 'artifact-digest',
         artifact['name'])
    # A malformed or unreadable archive or document is one refusal, unavailable-or-malformed, and
    # is told apart from a well-formed archive of another shape (artifact-entries) before that
    # check. The archive is read, never extracted, and no declared size is trusted.
    with zipfile.ZipFile(io.BytesIO(raw)) as archive:
        entries = archive.infolist()
        need(all(entry.compress_type in (zipfile.ZIP_STORED, zipfile.ZIP_DEFLATED) for entry in entries),
             'unavailable-or-malformed', artifact['name'] + ': a compression other than stored or deflated')
        need(sum(entry.file_size for entry in entries) <= EVIDENCE_LIMIT, 'unavailable-or-malformed',
             '%s: declared over %d bytes' % (artifact['name'], EVIDENCE_LIMIT))
        need(len(entries) == 1 and entries[0].filename == 'evidence.json' and not entries[0].is_dir(),
             'artifact-entries', artifact['name'])
        with archive.open(entries[0]) as member:
            data = member.read(EVIDENCE_LIMIT + 1)
    need(len(data) <= EVIDENCE_LIMIT, 'unavailable-or-malformed',
         '%s: over %d bytes when read' % (artifact['name'], EVIDENCE_LIMIT))
    need(nesting_within(data, EVIDENCE_DEPTH), 'unavailable-or-malformed',
         '%s: nesting deeper than %d' % (artifact['name'], EVIDENCE_DEPTH))
    payload = json.loads(data)
    need(isinstance(payload, dict), 'unavailable-or-malformed',
         artifact['name'] + ': evidence.json is not an object')
    return payload


def check_runtime(profile, recorded, here, job_key):
    """Toolchain identity. The record's shape, the platform, the tool set and every pin, in the
    record or on this runner, refuse here (a pin that does not match is a configuration fact, not
    an image roll). For "release-runner", a record that differs from what this runner resolves is
    returned as the drift (its detail, and each differing key with its recorded value and this
    runner's), None when it is the same: verify refuses it only after every other check has
    passed."""
    need(isinstance(recorded, dict) and set(recorded) == {'runner', 'tools'} and
         isinstance(recorded['runner'], dict) and isinstance(recorded['tools'], dict),
         'runtime-identity', job_key + ': no runtime record')
    runner, tools = recorded['runner'], recorded['tools']
    for key, value in profile['platform'].items():
        if key != '$comment':
            need(runner.get(key) == value, 'runtime-identity',
                 '%s: %s is %s, not %s' % (job_key, key, runner.get(key), value))
    need(set(tools) == set(profile['tools']), 'runtime-identity', job_key + ': tool set')
    for tool, pin in sorted(profile.get('pins', {}).items()):
        need(tools[tool] == pin, 'runtime-identity', '%s: %s is %r, pinned %r' % (
            job_key, tool, tools[tool], pin))
    if profile['compare'] == 'release-runner' and recorded != here:
        for tool, pin in sorted(profile.get('pins', {}).items()):
            need(here['tools'].get(tool) == pin, 'runtime-identity',
                 "%s: this runner's %s is %r, pinned %r" % (job_key, tool, here['tools'].get(tool), pin))
        # A drift is a record of the same shape with other values; another set of runner keys, or a
        # value that is not text, is a malformed record and refuses here.
        need(set(runner) == set(here['runner']) and
             all(isinstance(value, str) for value in list(runner.values()) + list(tools.values())),
             'runtime-identity', job_key + ": a runtime record of another shape than this runner's")
        values = {part: {key: {'recorded': recorded[part][key], 'release_runner': here[part][key]}
                         for key in sorted(recorded[part]) if recorded[part][key] != here[part][key]}
                  for part in ('runner', 'tools')}
        changed = list(values['runner']) + list(values['tools'])
        return '%s: differs from this runner in %s' % (job_key, changed), values
    return None


def resolve_workflow(api, workflow):
    resolved = api.get('actions/workflows/' + quote(Path(workflow['path']).name, safe=''))
    need(resolved['path'] == workflow['path'] and resolved['name'] == workflow['name'] and
         positive(resolved['id']) and resolved['state'] == 'active', 'workflow-identity',
         '%s is %s (%s)' % (workflow['path'], resolved.get('name'), resolved.get('state')))
    return resolved


# --- pull-request-to-branch reuse --------------------------------------------------------------

def pull_request_of(api, config, source):
    """The commit through the API (its tree and parents) and the one pull request of this
    repository merged into the trusted branch as this commit. A fork's pull request is never
    reused."""
    sha = source['source']
    commit = api.get('git/commits/' + sha)
    need(commit['sha'] == sha and commit['parents'], 'reuse-pull-request', 'commit %s' % sha)
    pulls = api.get('commits/%s/pulls?per_page=100' % sha)
    need(isinstance(pulls, list) and len(pulls) < 100, 'reuse-pull-request', 'the pull request listing')
    merged = [pull for pull in pulls if pull['merge_commit_sha'] == sha and pull['merged_at']]
    need(len(merged) == 1, 'reuse-pull-request', '%d pull requests were merged as %s' % (len(merged), sha))
    pull = merged[0]
    need(pull['state'] == 'closed' and pull['base']['ref'] == config['branch'] and
         pull['base']['repo']['id'] == source['repository_id'] and positive(pull['number']) and
         SHA.fullmatch(pull['head']['sha']), 'reuse-pull-request',
         'pull request %s is not merged into %s here' % (pull.get('number'), config['branch']))
    need((pull['head']['repo'] or {}).get('id') == source['repository_id'], 'reuse-pull-request',
         'pull request %s comes from another repository' % pull['number'])
    bind_head_branch(api, config, source, pull)
    return pull, commit


def listing(api, suffix):
    """Every element of a plain-list API listing: pages of up to 100 until an empty one (a short
    page is not taken for the end), ten pages at most."""
    rows = []
    for page in range(1, 11):
        separator = '&' if '?' in suffix else '?'
        part = api.get(suffix + separator + 'per_page=100&page=' + str(page))
        need(isinstance(part, list) and len(part) <= 100, 'pagination-shape', suffix)
        if not part:
            return rows
        rows.extend(part)
    raise Refusal('pagination-cap', suffix)


# A timeline event that changes, or tried to change, a pull request's base: base_ref_changed,
# automatic_base_change_succeeded, automatic_base_change_failed, and any other event so named.
BASE_EVENT = re.compile(r'base_change|base_ref')


def bind_head_branch(api, config, source, pull):
    """Records that survive the merge and that the pull request's code cannot write, binding its
    head branch to it alone: the head branch has had exactly one pull request ever, the merged one,
    from this repository (a run of another one executes another base's workflow at the same head),
    and the merged one's timeline has no base change of any kind. What cannot be read refuses."""
    owner = source['repository'].split('/')[0]
    try:
        pulls = listing(api, 'pulls?head=%s:%s&state=all' % (owner, quote(pull['head']['ref'], safe='')))
        listed = [(other['number'], (other['head']['repo'] or {}).get('id')) for other in pulls]
        events = listing(api, 'issues/%d/timeline' % pull['number'])
        names = [event.get('event') for event in events]
        need(all(isinstance(name, str) or name is None for name in names), 'unavailable-or-malformed')
        changed = sorted({name for name in names if name and BASE_EVENT.search(name)})
    except Refusal as error:
        raise Refusal('reuse-pull-request', 'the head branch\'s pull requests or the timeline could not be read: '
                      '%s' % error.code) from error
    except MALFORMED as error:
        raise Refusal('reuse-pull-request', 'the head branch\'s pull requests or the timeline could not be read: '
                      '%s' % type(error).__name__) from error
    others = sorted(number for number, _ in listed if number != pull['number'])
    need(not others, 'reuse-pull-request',
         'head branch %s has or had other pull requests %s' % (pull['head']['ref'], others))
    need(listed == [(pull['number'], source['repository_id'])], 'reuse-pull-request',
         'head branch %s does not list pull request %d once, from this repository' % (
             pull['head']['ref'], pull['number']))
    need(not changed, 'reuse-pull-request', 'the base of pull request %d was changed: %s' % (pull['number'], changed))


def bind_run(run, config, source, pull):
    """The run belongs to the merged pull request: at its final head, from this repository, a
    pull_request run created after the pull request was opened (with the head branch bound above,
    no other pull request it can belong to). The API's own association, cleared once a pull request
    merges, must name exactly that pull request into the trusted branch when it is not empty."""
    try:
        associated = [(item['number'], item['base']['ref'], item['base']['repo']['id'])
                      for item in run['pull_requests']]
        opened, created = timestamp(pull['created_at']), timestamp(run['created_at'])
    except (Refusal,) + MALFORMED as error:
        raise Refusal('reuse-pull-request', 'run %s: its pull requests or times cannot be read' % run.get('id')) from error
    need(run['head_sha'] == pull['head']['sha'] and run['head_repository']['id'] == source['repository_id'] and
         run['event'] == 'pull_request', 'reuse-pull-request', 'run %s is not a run of the head' % run.get('id'))
    need(created >= opened, 'reuse-pull-request', 'run %s was created before pull request %d was opened' % (
        run.get('id'), pull['number']))
    need(associated in ([], [(pull['number'], config['branch'], source['repository_id'])]), 'reuse-pull-request',
         'run %s is associated with %s, not pull request %d into %s' % (
             run.get('id'), associated, pull['number'], config['branch']))


def admit_pull_request(api, config, source, key, job_key, now):
    """The evidence of one job of the merged pull request's newest run, admitted for this commit,
    or a reuse-* refusal: no backward search, no older attempt. A refusal that is not reuse-* (the
    API, an artifact) is reuse-unavailable."""
    try:
        return admitted_pull_request(api, config, source, key, job_key, now)
    except Refusal as error:
        if error.code.startswith('reuse-'):
            raise
        raise Refusal('reuse-unavailable', '%s: %s' % (error.code, error.detail)) from error
    except MALFORMED as error:
        raise Refusal('reuse-unavailable', type(error).__name__) from error


def admitted_pull_request(api, config, source, key, job_key, now):
    workflow, branch = config['workflows'][key], config['branch']
    spec, allowed = workflow['jobs'][job_key], workflow['pull_request_reuse']['checks']
    pull, commit = pull_request_of(api, config, source)
    number, head, head_ref = pull['number'], pull['head']['sha'], pull['head']['ref']
    resolved = resolve_workflow(api, workflow)

    def newest():
        runs = api.pages('actions/workflows/%d/runs?head_sha=%s' % (resolved['id'], head), 'workflow_runs')
        applicable = [run for run in runs if run.get('event') == 'pull_request' and
                      run.get('head_branch') == head_ref and
                      (run.get('head_repository') or {}).get('id') == source['repository_id']]
        need(applicable, 'reuse-run-missing', 'no pull_request run of %s at %s' % (workflow['path'], head))
        unfinished = [run['id'] for run in applicable if run['status'] != 'completed']
        need(not unfinished, 'reuse-unfinished', 'pull request runs %s have not completed' % unfinished)
        # The newest attempt, whatever its result: never an older green one.
        return max(applicable, key=attempt_order)

    chosen = newest()
    need(chosen['conclusion'] == 'success', 'reuse-result', 'the newest pull request run %s attempt %s: %s' % (
        chosen['id'], chosen['run_attempt'], chosen['conclusion']))
    run = api.get('actions/runs/%d' % chosen['id'])
    need(run['repository']['id'] == source['repository_id'] and
         run['head_repository']['id'] == source['repository_id'] and run['head_sha'] == head and
         run['head_branch'] == head_ref and run['event'] == 'pull_request' and
         run['workflow_id'] == resolved['id'] and run['path'] == workflow['path'] and
         positive(run['id']) and positive(run['run_attempt']), 'reuse-evidence', 'run %s' % run.get('id'))
    need(selection(run) == selection(chosen), 'reuse-race', 'run %d' % run['id'])
    bind_run(run, config, source, pull)
    age = (now - timestamp(run['run_started_at'])).total_seconds()
    need(0 <= age <= config['max_age_hours'] * 3600, 'reuse-age',
         'pull request run %d started %.1f hours ago; the limit is %d' % (run['id'], age / 3600,
                                                                          config['max_age_hours']))
    jobs = api.pages('actions/runs/%d/attempts/%d/jobs' % (run['id'], run['run_attempt']), 'jobs')
    matching = [job for job in jobs if job['name'] == spec['name']]
    need(len(matching) == 1, 'reuse-job', '%d jobs named %s' % (len(matching), spec['name']))
    job = matching[0]
    need(job['run_id'] == run['id'] and job['run_attempt'] == run['run_attempt'] and job['head_sha'] == head and
         job['workflow_name'] == workflow['name'] and job['labels'] == spec['labels'] and
         (job['runner_group_id'], job['runner_group_name']) == HOSTED, 'reuse-job', spec['name'])
    need(job['status'] == 'completed' and job['conclusion'] == 'success', 'reuse-result',
         '%s: %s' % (spec['name'], job['conclusion']))
    name = 'ci-evidence-%s-%s-%d' % (key, job_key, run['run_attempt'])
    artifacts = [artifact for artifact in api.pages('actions/runs/%d/artifacts' % run['id'], 'artifacts')
                 if artifact['name'] == name]
    need(len(artifacts) == 1, 'reuse-artifact', '%d artifacts named %s' % (len(artifacts), name))
    payload = read_artifact(api, artifacts[0], run, job)

    # What the job says it checked out is held to the API: a merge commit of this pull request's
    # head onto the commit's own first parent, with the commit's tree.
    merge, recorded = payload.get('source'), payload.get('pull_request')
    need(isinstance(merge, str) and SHA.fullmatch(merge) and isinstance(recorded, dict) and
         set(recorded) == {'number', 'base', 'head', 'tree'} and recorded['number'] == number and
         recorded['head'] == head, 'reuse-evidence', job_key + ': the pull request record')
    merged = api.get('git/commits/' + merge)
    need(merged['sha'] == merge and [parent['sha'] for parent in merged['parents']] == [recorded['base'], head] and
         merged['tree']['sha'] == recorded['tree'], 'reuse-evidence',
         '%s: the merge commit %s is not that pull request\'s' % (job_key, merge))
    need(recorded['tree'] == commit['tree']['sha'], 'reuse-tree', '%s: the pull request tested tree %s, %s is %s' % (
        job_key, recorded['tree'], source['source'], commit['tree']['sha']))
    need(recorded['base'] == commit['parents'][0]['sha'], 'reuse-base',
         '%s: the pull request was tested on %s, %s follows %s' % (
             job_key, recorded['base'], source['source'], commit['parents'][0]['sha']))
    need(payload.get('inputs') == source['inputs'], 'reuse-inputs', job_key)
    try:
        validate_scoped_coverage(spec, payload.get('coverage'))
    except Refusal as error:
        raise Refusal('reuse-coverage', '%s: %s' % (job_key, error.detail)) from error
    environment = payload.get('environment')
    need(isinstance(environment, dict) and set(environment) == set(
        workflow['pull_request_reuse'].get('environment', [])), 'reuse-evidence', job_key + ': the environment')
    identity = None
    if 'identity' in config:
        identity = job_identity(identity_context(config, source['source']),
                                config['profiles'][spec['profile']], key)
        need(payload.get('identity') == identity, 'reuse-runtime', job_key + ': the identity record')
    base = {'schema': SCHEMA, 'repository': source['repository'], 'repository_id': source['repository_id'],
            'source': merge, 'inputs': source['inputs']}
    expected = evidence(base, key, workflow, job_key, run['id'], run['run_attempt'], 'pull_request', branch,
                        payload.get('runtime'), payload.get('coverage'), identity,
                        'refs/pull/%d/merge' % number, {'pull_request': recorded, 'environment': environment})
    need(payload == expected, 'reuse-evidence', job_key + ': the evidence is not of that run, job and shape')

    # Still the newest attempt, unchanged, after collection.
    fields = ('id', 'run_attempt', 'status', 'conclusion', 'run_started_at', 'updated_at')
    after = api.get('actions/runs/%d' % run['id'])
    need({field: after.get(field) for field in fields} == {field: run.get(field) for field in fields} and
         selection(newest()) == selection(run), 'reuse-race', 'run %d' % run['id'])
    return {'pull_request': number, 'head': head, 'merge': merge, 'base': recorded['base'],
            'tree': recorded['tree'], 'run_id': run['id'], 'attempt': run['run_attempt'], 'job_id': job['id'],
            'artifact': {'id': artifacts[0]['id'], 'name': name, 'digest': artifacts[0]['digest']},
            'run_started_at': run['run_started_at'], 'runtime': payload['runtime'], 'environment': environment,
            'covered': sorted(name for name, status in payload['coverage'].items()
                              if status == 'PASS' and name in allowed)}


def plan(api, config, source, key, job_key, now):
    """The reuse decision of a branch job before its checks: reuse, with the checks the merged pull
    request's newest run passed that this job may take, or fresh, with the reason. Only a first
    attempt of a push run is planned: a re-run or a dispatch, the fresh route, runs the whole list."""
    record = {'schema': PLAN_SCHEMA, 'decision': 'fresh', 'refusal': None, 'source': source['source'],
              'workflow': key, 'job': job_key, 'attempt': None, 'covered': [], 'reused': None,
              'checked_at': now.isoformat()}
    try:
        workflow, spec, event, _, _, attempt = capture_context(config, source, key, job_key)
        record['attempt'] = attempt
        need('pull_request_reuse' in workflow, 'reuse-off', 'the workflow configures no pull_request_reuse')
        need(event == 'push' and attempt == 1, 'reuse-event',
             'only the first attempt of a push is planned; this is %s attempt %d' % (event, attempt))
        admitted = admit_pull_request(api, config, source, key, job_key, now)
        here, _ = job_runtime(config, source, key, spec)
        need(admitted['runtime'] == here, 'reuse-runtime', 'the pull request job ran with another image or tools')
        need(admitted['environment'] == environment_of(workflow), 'reuse-environment',
             'the pull request job ran with another environment')
        required = set(required_checks(spec))
        covered = [name for name in admitted['covered'] if name in required]
        need(covered, 'reuse-coverage', 'the pull request run passed none of this job\'s reusable checks')
        record.update(decision='reuse', covered=covered,
                      reused=dict({field: admitted[field] for field in REUSED if field != 'checks'},
                                  checks=covered))
    except (Refusal,) + MALFORMED as error:
        record.update(decision='fresh', covered=[], reused=None, refusal=refusal_of(error))
    return record


def check_reused(workflow, payload, job_key):
    """A branch job's record of its environment and of what it reused: the shape, and the checks,
    each one the configuration lets a pull request cover. The checks it reused, or none."""
    reuse = workflow['pull_request_reuse']
    environment, reused = payload.get('environment'), payload.get('reused')
    need('reused' in payload, 'evidence-identity', job_key + ': no reused member')
    need(isinstance(environment, dict) and set(environment) == set(reuse.get('environment', [])) and
         all(value is None or (isinstance(value, str) and DIGEST.fullmatch(value))
             for value in environment.values()), 'evidence-identity', job_key + ': the environment')
    if reused is None:
        return ()
    need(isinstance(reused, dict) and set(reused) == set(REUSED) and isinstance(reused['checks'], list) and
         reused['checks'] and len(set(reused['checks'])) == len(reused['checks']) and
         set(reused['checks']) <= set(reuse['checks']), 'reuse-coverage',
         job_key + ': the reused record or its checks')
    return reused['checks']


def readmit(api, config, source, key, job_key, now, run, payload):
    """At the release, the pull request evidence a branch job reused, admitted again: the same
    selection, run, job and artifact, still the newest attempt, still fresh enough, still covering
    each reused check, with the branch job's own image, tools and environment."""
    reused = payload['reused']
    need(run['event'] == 'push' and run['run_attempt'] == 1, 'reuse-event',
         'run %d attempt %d (%s) recorded a reuse' % (run['id'], run['run_attempt'], run['event']))
    admitted = admit_pull_request(api, config, source, key, job_key, now)
    changed = sorted(field for field in REUSED if field != 'checks' and admitted[field] != reused[field])
    need(not changed, 'reuse-changed', '%s: %s differ from what the branch job reused' % (job_key, changed))
    need(set(reused['checks']) <= set(admitted['covered']), 'reuse-coverage',
         '%s: %s were not passed by the pull request run' % (
             job_key, sorted(set(reused['checks']) - set(admitted['covered']))))
    need(admitted['runtime'] == payload['runtime'], 'reuse-runtime',
         job_key + ': the pull request job ran with another image or tools')
    need(admitted['environment'] == payload['environment'], 'reuse-environment',
         job_key + ': the pull request job ran with another environment')
    return dict(reused, run_started_at=admitted['run_started_at'])


def verify_workflow(api, config, source, key, now, here, trail, drift, identity=None):
    """identity, under the policy: the context at the commit and the runtimes collected per variant
    and profile."""
    workflow, branch = config['workflows'][key], config['branch']
    resolved = resolve_workflow(api, workflow)
    trail.update(workflow_id=resolved['id'], path=workflow['path'], name=workflow['name'],
                 events=workflow['events'])
    chosen = select(api, source, workflow, resolved['id'], branch, trail)
    trail.update(run_id=chosen['id'], attempt=chosen['run_attempt'], event=chosen['event'],
                 status=chosen['status'], conclusion=chosen['conclusion'],
                 run_started_at=chosen['run_started_at'])
    run = api.get('actions/runs/%d' % chosen['id'])
    run_identity(run, source, workflow, resolved['id'], branch)
    need((run['id'], run['run_attempt']) == (chosen['id'], chosen['run_attempt']), 'run-race')
    need(run['status'] == 'completed' and run['conclusion'] == 'success', 'run-result',
         'run %d attempt %d: %s %s' % (run['id'], run['run_attempt'], run['status'], run['conclusion']))
    age = (now - timestamp(run['run_started_at'])).total_seconds()
    need(0 <= age <= config['max_age_hours'] * 3600, 'run-age',
         'run %d started %.1f hours ago; the limit is %d' % (run['id'], age / 3600, config['max_age_hours']))
    trail['age_hours'] = round(age / 3600, 2)
    # Reusable workflows the run resolved, each with the commit it ran (an audit record only).
    trail['referenced_workflows'] = run.get('referenced_workflows', [])

    jobs = api.pages('actions/runs/%d/attempts/%d/jobs' % (run['id'], run['run_attempt']), 'jobs')
    by_name = check_jobs(jobs, run, workflow, branch)
    artifacts = api.pages('actions/runs/%d/artifacts' % run['id'], 'artifacts')
    trail['jobs'] = {}
    for job_key, spec in workflow['jobs'].items():
        job = by_name[spec['name']]
        name = 'ci-evidence-%s-%s-%d' % (key, job_key, run['run_attempt'])
        matching = [artifact for artifact in artifacts if artifact['name'] == name]
        need(len(matching) == 1, 'artifact-coverage', '%d artifacts named %s' % (len(matching), name))
        payload = read_artifact(api, matching[0], run, job)
        expected = evidence(source, key, workflow, job_key, run['id'], run['run_attempt'], run['event'],
                            branch, payload.get('runtime'), payload.get('coverage'),
                            payload.get('identity') if identity else None, None,
                            {'environment': payload.get('environment'), 'reused': payload.get('reused')}
                            if 'pull_request_reuse' in workflow else None)
        for field in ('schema', 'repository', 'repository_id', 'source', 'workflow', 'workflow_path',
                      'workflow_name', 'job', 'run_id', 'attempt', 'event', 'ref'):
            need(payload.get(field) == expected[field], 'evidence-identity',
                 '%s: %s is %r' % (job_key, field, payload.get(field)))
        recorded_inputs = payload.get('inputs')
        need(isinstance(recorded_inputs, dict) and recorded_inputs == source['inputs'],
             'input-identity', '%s: %s' % (job_key, sorted(
                 path for path in set(source['inputs']) | set(recorded_inputs or {})
                 if (recorded_inputs or {}).get(path) != source['inputs'].get(path))))
        reused = check_reused(workflow, payload, job_key) if 'pull_request_reuse' in workflow else ()
        validate_coverage(spec, payload.get('coverage'), reused)
        profile = config['profiles'][spec['profile']]
        variant = None
        if identity:
            # A missing or unexpected identity refuses here, before the runtime is compared: it is
            # never a drift, and never revalidated.
            check_identity(identity['context'], profile, payload, key, job_key)
            identity['groups'].setdefault((profile['variant'], spec['profile']), []).append(
                ('%s/%s' % (key, job_key), payload['runtime']))
            variant = identity['context']['variants'][profile['variant']]
        need(payload == expected, 'evidence-identity', job_key + ': unexpected members')
        # A check this job took from the merged pull request's run stands only if that evidence is
        # admitted again now (a refusal, never a drift).
        reused_trail = readmit(api, config, source, key, job_key, now, run, payload) if reused else None
        # The runtime identity is the job's last check, so that any other refusal of the job wins
        # over it; a drift from this runner is only collected, for the end of verify.
        if profile['compare'] == 'release-runner' and spec['profile'] not in here:
            here[spec['profile']] = runtime(profile, variant)
        differs = check_runtime(profile, payload.get('runtime'), here.get(spec['profile']), job_key)
        if differs:
            drift.append((differs[0], dict(workflow=key, job=job_key, **differs[1])))
        trail['jobs'][job_key] = {
            'name': spec['name'], 'job_id': job['id'], 'result': job['conclusion'],
            'labels': job['labels'], 'started_at': job['started_at'],
            'completed_at': job['completed_at'],
            'artifact': {'id': matching[0]['id'], 'name': name, 'digest': matching[0]['digest']},
            'runtime': payload['runtime'], 'coverage': payload['coverage']}
        if 'pull_request_reuse' in workflow:
            trail['jobs'][job_key]['reused'] = reused_trail
    trail['skipped_jobs'] = {name: by_name[name]['conclusion']
                             for name in workflow.get('skipped_jobs', [])}

    # The attempt read is still the run's latest, and the run still the newest applicable one.
    fields = ('id', 'run_attempt', 'status', 'conclusion', 'run_started_at', 'updated_at')
    after = api.get('actions/runs/%d' % run['id'])
    need({field: after.get(field) for field in fields} == {field: run.get(field) for field in fields},
         'attempt-changed', 'run %d' % run['id'])
    latest = select(api, source, workflow, resolved['id'], branch)
    need(selection(latest) == selection(run), 'latest-changed', 'run %d attempt %d' % (
        latest['id'], latest['run_attempt']))
    return resolved['id'], run


def runner_bound_jobs(config):
    """The job keys, in configuration order, whose profile compares with the release runner."""
    jobs = []
    for workflow in config['workflows'].values():
        for job_key, spec in workflow['jobs'].items():
            if config['profiles'][spec['profile']]['compare'] == 'release-runner' and job_key not in jobs:
                jobs.append(job_key)
    return jobs


def verify(api, config, source, release_tag, now, record):
    """A publication is held to its tag; a validation-only run reads no tag: its commit is the
    trusted branch's tip the dispatch ran for, and its decision is never a publication's."""
    branch, publication = config['branch'], record['mode'] == 'publication'
    grammar = tag_grammar(config)
    repository = api.get('')
    need((repository['full_name'], repository['id']) == (source['repository'], source['repository_id']),
         'repository-identity', '%s (%s)' % (repository.get('full_name'), repository.get('id')))
    need(repository['default_branch'] == branch, 'branch-default',
         'the default branch is %s, the configuration trusts %s' % (repository['default_branch'], branch))
    if publication:
        first_tag = tag_object(api, release_tag, source['source'], grammar)
        record['tag_object'] = first_tag
    identity = None
    if 'identity' in config:
        identity = {'context': identity_context(config, source['source']), 'groups': {}}
        record['identity'] = dict(identity['context']['record'],
                                  statement='a reported hosted-runner image identity, not a pinned VM image',
                                  release_workflow=release_workflow_actions(source), groups={})
    here, chosen, drift = {}, {}, []
    for key in config['workflows']:
        record['workflows'][key] = {}
        chosen[key] = verify_workflow(api, config, source, key, now, here, record['workflows'][key],
                                      drift, identity)
    if identity:
        # Identity is compared per variant and profile, never across them. Jobs of a "pins" profile
        # (a leg the release runner cannot resolve) must have reported one identity between them;
        # a "release-runner" profile's jobs are each compared with this runner at the end.
        for (variant, name), members in sorted(identity['groups'].items()):
            same = all(runtime_identity == members[0][1] for _, runtime_identity in members)
            record['identity']['groups']['%s/%s' % (variant, name)] = {
                'compare': config['profiles'][name]['compare'], 'jobs': [job for job, _ in members],
                'runtime': members[0][1] if same else None}
            need(same or config['profiles'][name]['compare'] != 'pins', 'identity-changed',
                 'the %s jobs of variant %s report different identities: %s' % (
                     name, variant, '; '.join('%s %s' % (job, runtime_identity['runner'].get('ImageVersion'))
                                              for job, runtime_identity in members)))
    # Collection takes time: every workflow's selection, then the tag, once more at the end.
    for key, (workflow_id, run) in chosen.items():
        final = select(api, source, config['workflows'][key], workflow_id, branch)
        need(selection(final) == selection(run), 'final-latest-changed', key)
    if publication:
        need(tag_object(api, release_tag, source['source'], grammar) == first_tag, 'tag-changed')
    # Last of all, after every other check has passed: the release runner's drift from the
    # evidence's image or tools.
    if drift:
        raise Drift('; '.join(detail for detail, _ in drift), runner_bound_jobs(config),
                    [values for _, values in drift])


# Malformed or missing API data or files: a refusal like any other, never a success.
MALFORMED = (KeyError, IndexError, ValueError, TypeError, AttributeError, OSError, EOFError,
             NotImplementedError, RecursionError, zipfile.BadZipFile, zlib.error)


def refusal_of(error):
    if isinstance(error, Refusal):
        return {'code': error.code, 'detail': error.detail}
    detail = type(error).__name__
    if isinstance(error, KeyError) and error.args:
        detail += ' ' + repr(error.args[0])[:80]
    return {'code': 'unavailable-or-malformed', 'detail': detail}


def decide(api, config, source, release_tag, now, record):
    """The decision is reuse only for a publication whose every check of verify passed; a
    validation-only run that passes is validated, which no publication takes. A publication whose
    one refusal is the release runner's drift (Drift: every other check has passed) decides
    revalidate where the configuration allows it: the release-runner jobs' evidence is not taken,
    and the release workflow must run the check list dev.json declares, strictly, on its own runner
    before anything is published. Every other refusal, a validation-only run's drift included,
    refuses."""
    try:
        need(record.get('mode') in PASSED, 'mode-unknown', str(record.get('mode')))
        passed = PASSED[record['mode']]
        verify(api, config, source, release_tag, now, record)
    except (Refusal,) + MALFORMED as error:
        if isinstance(error, Drift) and record['mode'] == 'publication' and revalidation_allowed(config):
            record['decision'], record['refusal'] = 'revalidate', None
            record['revalidation'] = {'reason': error.detail, 'jobs': error.jobs,
                                      'differences': error.differences}
        else:
            record['decision'], record['refusal'] = 'refuse', refusal_of(error)
    else:
        record['decision'], record['refusal'] = passed, None
    return record


def write_outputs(record):
    """The decision and mode as step outputs, for the jobs after the gate: a publishing job
    proceeds on decision "reuse", or on "revalidate" once the check list has passed strictly on the
    release runner. Both values are from fixed sets."""
    name = os.environ.get('GITHUB_OUTPUT')
    if name:
        with open(name, 'a', encoding='utf-8') as output:
            output.write('decision=%s\nmode=%s\n' % (record['decision'], record['mode'] or ''))


def run_verify(args):
    now = dt.datetime.now(dt.timezone.utc)
    output = Path(args.output or 'release-evidence.json')
    record = {'schema': SCHEMA, 'decision': 'refuse', 'refusal': None, 'revalidation': None,
              'identity': None, 'mode': None, 'event': os.environ.get('GITHUB_EVENT_NAME', ''),
              'tag': os.environ.get('RELEASE_TAG', ''), 'checked_at': now.isoformat(),
              'workflows': {}}
    try:
        config, source = source_context(args.config)
        record.update(repository=source['repository'], repository_id=source['repository_id'],
                      source=source['source'], inputs=source['inputs'], branch=config['branch'],
                      max_age_hours=config['max_age_hours'],
                      fresh_at_release=config.get('fresh_at_release', []),
                      tag_prerelease=tag_grammar(config) is TAG_PRERELEASE,
                      dispatch=config.get('dispatch', {}).get('validates', 'none'),
                      revalidation_allowed=revalidation_allowed(config))
        record['mode'] = release_mode(config)
    except (Refusal,) + MALFORMED as error:
        record['refusal'] = refusal_of(error)
    else:
        if record['mode'] != 'publication':
            # No tag is read or stood for: the commit is the tip the dispatch ran for.
            record['tag'] = None
        decide(API(source['repository']), config, source, record['tag'], now, record)
    text = json.dumps(record, indent=2, sort_keys=True)
    print(text)
    try:
        output.write_text(text + '\n', encoding='utf-8')
        write_outputs(record)
    except OSError:
        print('release-evidence: refuse: the decision record or the step outputs could not be written',
              file=sys.stderr)
        return 1
    if record['decision'] == 'validated':
        print('release-evidence: validated, validation-only and never a publication decision: %s at %s '
              '(decision record: %s)' % (', '.join(
                  '%s run %s attempt %s' % (key, value['run_id'], value['attempt'])
                  for key, value in record['workflows'].items()), record['source'], output),
              file=sys.stderr)
        return 0
    if record['decision'] == 'revalidate':
        print('release-evidence: revalidate: %s; every other check passed, and only the check list run '
              'strictly on the release runner may admit this publication (decision record: %s)' % (
                  record['revalidation']['reason'], output), file=sys.stderr)
        return 0
    if record['decision'] != 'reuse':
        refusal = record['refusal']
        print('release-evidence: refuse: %s%s (decision record: %s)' % (
            refusal['code'], ': ' + refusal['detail'] if refusal['detail'] else '', output),
            file=sys.stderr)
        return 1
    print('release-evidence: reuse: %s (decision record: %s)' % (', '.join(
        '%s run %s attempt %s' % (key, value['run_id'], value['attempt'])
        for key, value in record['workflows'].items()), output), file=sys.stderr)
    return 0


def run_capture(args):
    try:
        config, source = source_context(args.config)
        result = capture(config, source, args.workflow, args.job,
                         [('check', path) for path in args.check_log] +
                         [('setup', path) for path in args.setup_log], args.reuse_plan)
        Path(args.output or 'evidence.json').write_text(json.dumps(result, sort_keys=True) + '\n',
                                                        encoding='utf-8')
        return 0
    except (Refusal,) + MALFORMED as error:
        refusal = refusal_of(error)
        print('release-evidence: capture refused: %s%s' % (
            refusal['code'], ': ' + refusal['detail'] if refusal['detail'] else ''), file=sys.stderr)
        return 1


def run_reuse(args):
    """The reuse decision, written whatever it is; a fresh decision is never a failure. Only a
    record or step outputs that cannot be written fail the step."""
    now = dt.datetime.now(dt.timezone.utc)
    output = Path(args.output or 'reuse-plan.json')
    try:
        config, source = source_context(args.config)
    except (Refusal,) + MALFORMED as error:
        record = {'schema': PLAN_SCHEMA, 'decision': 'fresh', 'refusal': refusal_of(error),
                  'source': os.environ.get('GITHUB_SHA', ''), 'workflow': args.workflow, 'job': args.job,
                  'attempt': None, 'covered': [], 'reused': None, 'checked_at': now.isoformat()}
    else:
        record = plan(API(source['repository']), config, source, args.workflow, args.job, now)
    text = json.dumps(record, indent=2, sort_keys=True)
    print(text)
    try:
        output.write_text(text + '\n', encoding='utf-8')
        name = os.environ.get('GITHUB_OUTPUT')
        if name:
            # Fixed values: a decision, and check names from the configuration.
            with open(name, 'a', encoding='utf-8') as handle:
                handle.write('decision=%s\ncovered=%s\n' % (record['decision'], ','.join(record['covered'])))
    except OSError:
        print('release-evidence: reuse: the plan or the step outputs could not be written', file=sys.stderr)
        return 1
    if record['decision'] == 'reuse':
        print('release-evidence: reuse: %s from pull request %s run %s attempt %s; every other check runs '
              '(plan: %s)' % (','.join(record['covered']), record['reused']['pull_request'],
                              record['reused']['run_id'], record['reused']['attempt'], output), file=sys.stderr)
    else:
        print('release-evidence: fresh: %s%s; every check runs (plan: %s)' % (
            record['refusal']['code'], ': ' + record['refusal']['detail'] if record['refusal']['detail'] else '',
            output), file=sys.stderr)
    return 0


def main(argv=None):
    parser = argparse.ArgumentParser(prog='release_evidence.py', description=__doc__.splitlines()[0])
    parser.add_argument('mode', choices=['capture', 'verify', 'reuse'])
    parser.add_argument('--config', default='.github/ci-evidence.json')
    parser.add_argument('--workflow', help='capture, reuse: the workflow key of the configuration')
    parser.add_argument('--job', help='capture, reuse: the job key (one per matrix leg)')
    parser.add_argument('--check-log', action='append', default=[])
    parser.add_argument('--setup-log', action='append', default=[])
    parser.add_argument('--reuse-plan', help='capture on the branch: the plan the job\'s reuse step wrote')
    parser.add_argument('--output', help='capture: evidence.json; verify: release-evidence.json; '
                                         'reuse: reuse-plan.json')
    args = parser.parse_args(argv)
    return {'capture': run_capture, 'verify': run_verify, 'reuse': run_reuse}[args.mode](args)


if __name__ == '__main__':
    sys.exit(main())
