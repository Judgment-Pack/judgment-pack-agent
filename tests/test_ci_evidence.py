"""Handwritten Actions fixtures; these are not live-service release evidence."""
import base64
import copy
import contextlib
import datetime as dt
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch
import zipfile

ROOT = Path(__file__).resolve().parents[1]
HELPER = Path(os.environ.get('CI_EVIDENCE_HELPER', ROOT / 'lib/ci_evidence.py'))
if not HELPER.exists():
    HELPER = ROOT / 'scripts/dev/ci_evidence.py'
spec = importlib.util.spec_from_file_location('ci_evidence', HELPER)
e = importlib.util.module_from_spec(spec)
spec.loader.exec_module(e)
SHA = 'a' * 40
REPO = 'example/project'
NOW = dt.datetime(2026, 10, 25, 12, tzinfo=dt.timezone.utc)
RUNTIME = {'runner': {'ImageOS': 'ubuntu24', 'ImageVersion': '20261020.1',
                      'RUNNER_OS': 'Linux', 'RUNNER_ARCH': 'X64', 'os_release': 'os'},
           'tools': {}}
SOURCE = {'schema': 1, 'repository': REPO, 'repository_id': 12, 'source': SHA,
          'inputs': {'dev.json': 'manifest-hash', '.github/ci-evidence.json': 'config-hash'}}
CONFIG = {'schema': 1, 'repository': REPO, 'max_age_hours': 48, 'inputs': ['dev.json'],
          'profiles': {'base': {}}, 'workflows': {'ci': {
              'path': '.github/workflows/ci.yml', 'events': ['push', 'workflow_dispatch'],
              'jobs': {'test': {'name': 'tests', 'job_id': 'test', 'profile': 'base',
                                'checks': ['unit', 'audit'], 'optional_skips': ['audit']}},
              'skipped': ['PR sample']}}}


def zip_payload(payload, extra=False):
    output = io.BytesIO()
    with zipfile.ZipFile(output, 'w') as archive:
        archive.writestr('evidence.json', json.dumps(payload))
        if extra:
            archive.writestr('../escape', 'refuse')
    return output.getvalue()


def fixture(source=None, config=None, runtime=None, blob='b' * 40):
    source, config, rt = source or SOURCE, config or CONFIG, runtime or RUNTIME
    run = {'id': 100, 'run_attempt': 1, 'created_at': '2026-10-25T10:00:00Z',
           'run_started_at': '2026-10-25T10:00:01Z', 'head_sha': source['source'],
           'head_branch': 'main', 'event': 'push', 'path': '.github/workflows/ci.yml',
           'workflow_id': 9, 'status': 'completed', 'conclusion': 'success', 'pull_requests': [],
           'repository': {'full_name': source['repository'], 'id': source['repository_id']},
           'head_repository': {'full_name': source['repository'], 'id': source['repository_id']}}
    payload = dict(source, workflow='.github/workflows/ci.yml', workflow_blob=blob,
                   run_id=100, attempt=1, event='push', ref='refs/heads/main', job='test',
                   runtime=rt, coverage={'unit': 'PASS', 'audit': 'SKIP'})
    raw = zip_payload(payload)
    job = {'id': 200, 'name': 'tests', 'run_id': 100, 'head_sha': source['source'],
           'head_branch': 'main', 'status': 'completed', 'conclusion': 'success',
           'labels': ['ubuntu-latest'], 'started_at': '2026-10-25T10:00:02Z',
           'completed_at': '2026-10-25T10:05:00Z'}
    skipped = dict(job, id=201, name='PR sample', conclusion='skipped')
    artifact = {'id': 300, 'name': 'ci-evidence-ci-test-1', 'expired': False,
                'size_in_bytes': len(raw), 'digest': 'sha256:' + hashlib.sha256(raw).hexdigest(),
                'created_at': '2026-10-25T10:04:59Z',
                'workflow_run': {'id': 100, 'head_sha': source['source'], 'head_branch': 'main'}}
    return {'actions/workflows/ci.yml': {'id': 9, 'path': '.github/workflows/ci.yml', 'state': 'active'},
            'actions/workflows/9/runs?head_sha=' + source['source'] + '&per_page=100&page=1':
                {'total_count': 1, 'workflow_runs': [run]},
            'actions/runs/100': run,
            'actions/runs/100/attempts/1/jobs?per_page=100&page=1':
                {'total_count': 2, 'jobs': [job, skipped]},
            'actions/runs/100/artifacts?per_page=100&page=1':
                {'total_count': 1, 'artifacts': [artifact]},
            'actions/artifacts/300/zip': raw,
            'git/ref/tags/v1.2.3': {'ref': 'refs/tags/v1.2.3', 'object': {'type': 'tag', 'sha': 't' * 40}},
            'git/tags/' + 't' * 40: {'object': {'type': 'commit', 'sha': source['source']}}}


class FakeAPI(e.API):
    def __init__(self, responses):
        super().__init__(REPO)
        self.responses = copy.deepcopy(responses)
        self.calls = []

    def get(self, suffix, binary=False):
        self.calls.append(suffix)
        value = self.responses[suffix]
        if isinstance(value, Exception):
            raise value
        if callable(value):
            return value()
        return copy.deepcopy(value)


class EvidenceTest(unittest.TestCase):
    def setUp(self):
        self.data = fixture()
        self.config = copy.deepcopy(CONFIG)
        self.commands = patch.object(e, 'command', return_value=('b' * 40 + '\n').encode())
        self.runtime_patch = patch.object(e, 'runtime', return_value=RUNTIME)
        self.commands.start()
        self.runtime_patch.start()
        self.addCleanup(self.commands.stop)
        self.addCleanup(self.runtime_patch.stop)

    @property
    def selected_run(self):
        return self.data['actions/runs/100']

    @property
    def jobs(self):
        return self.data['actions/runs/100/attempts/1/jobs?per_page=100&page=1']['jobs']

    @property
    def artifact(self):
        return self.data['actions/runs/100/artifacts?per_page=100&page=1']['artifacts'][0]

    def payload(self, change, extra=False):
        raw = self.data['actions/artifacts/300/zip']
        with zipfile.ZipFile(io.BytesIO(raw)) as archive:
            value = json.loads(archive.read('evidence.json'))
        change(value)
        raw = zip_payload(value, extra)
        self.data['actions/artifacts/300/zip'] = raw
        self.artifact.update(size_in_bytes=len(raw), digest='sha256:' + hashlib.sha256(raw).hexdigest())

    def decision(self):
        try:
            e.verify(FakeAPI(self.data), self.config, SOURCE, 'v1.2.3', NOW)
            return 'reuse'
        except e.Refusal as error:
            return str(error)
        except Exception as error:
            return 'unexpected:' + type(error).__name__

    def test_exact_main(self):
        self.assertEqual(self.decision(), 'reuse')

    def test_run_identity(self):
        for field, value, reason in [('head_sha', 'c' * 40, 'run-sha'),
                                     ('path', '.github/workflows/other.yml', 'run-workflow'),
                                     ('workflow_id', 10, 'run-workflow'),
                                     ('repository', {'id': 13, 'full_name': REPO}, 'run-repository'),
                                     ('head_repository', {'id': 13, 'full_name': REPO}, 'run-head-repository'),
                                     ('pull_requests', [{'number': 1}], 'run-trigger'),
                                     ('run_attempt', 0, 'run-identity')]:
            with self.subTest(field=field):
                self.data = fixture()
                self.selected_run[field] = value
                self.assertEqual(self.decision(), reason)

    def test_wrong_trigger_or_ref(self):
        for field, value in [('event', 'pull_request'), ('head_branch', 'feature')]:
            with self.subTest(field=field):
                self.data = fixture()
                self.selected_run[field] = value
                self.assertEqual(self.decision(), 'run-missing')

    def test_wrong_workflow_identity(self):
        self.data['actions/workflows/ci.yml']['path'] = '.github/workflows/other.yml'
        self.assertEqual(self.decision(), 'workflow-identity')

    def test_latest_never_latest_green(self):
        for status, conclusion in [('in_progress', None), ('queued', None),
                                   ('completed', 'failure'), ('completed', 'cancelled')]:
            with self.subTest(status=status, conclusion=conclusion):
                self.data = fixture()
                older = dict(self.selected_run, id=99, created_at='2026-10-25T09:00:00Z')
                page = self.data['actions/workflows/9/runs?head_sha=' + SHA + '&per_page=100&page=1']
                page.update(total_count=2, workflow_runs=[older, self.selected_run])
                self.selected_run.update(status=status, conclusion=conclusion)
                self.assertEqual(self.decision(), 'run-result')

    def test_stale_and_future(self):
        for started in ['2026-10-20T10:00:00Z', '2026-10-26T10:00:00Z']:
            self.selected_run['run_started_at'] = started
            self.assertEqual(self.decision(), 'run-age')

    def test_missing_duplicate_unexpected_jobs(self):
        for mode in ['missing', 'duplicate', 'unexpected']:
            with self.subTest(mode=mode):
                self.data = fixture()
                page = self.data['actions/runs/100/attempts/1/jobs?per_page=100&page=1']
                if mode == 'missing':
                    page['jobs'].pop(0)
                elif mode == 'duplicate':
                    page['jobs'].append(dict(self.jobs[0], id=202))
                else:
                    page['jobs'].append(dict(self.jobs[0], id=202, name='unexpected'))
                page['total_count'] = len(page['jobs'])
                self.assertEqual(self.decision(), 'job-coverage')

    def test_job_result_or_runner(self):
        for field, value in [('conclusion', 'skipped'), ('head_sha', 'c' * 40),
                             ('run_id', 99), ('labels', ['self-hosted'])]:
            self.data = fixture()
            self.jobs[0][field] = value
            self.assertEqual(self.decision(), 'job-result')

    def test_explicit_skip(self):
        self.jobs[1]['conclusion'] = 'success'
        self.assertEqual(self.decision(), 'job-skip')

    def test_artifact_coverage(self):
        for duplicate in [False, True]:
            self.data = fixture()
            page = self.data['actions/runs/100/artifacts?per_page=100&page=1']
            page['artifacts'] = [self.artifact, dict(self.artifact, id=301)] if duplicate else []
            page['total_count'] = len(page['artifacts'])
            self.assertEqual(self.decision(), 'artifact-coverage')

    def test_artifact_metadata(self):
        for field, value, reason in [('expired', True, 'artifact-expired'),
                                     ('size_in_bytes', 1048577, 'artifact-size'),
                                     ('workflow_run', {'id': 99}, 'artifact-run'),
                                     ('created_at', '2026-10-25T09:00:00Z', 'artifact-interval'),
                                     ('digest', 'sha256:' + '0' * 64, 'artifact-digest')]:
            self.data = fixture()
            self.artifact[field] = value
            self.assertEqual(self.decision(), reason)

    def test_archive_entries(self):
        self.payload(lambda value: None, extra=True)
        self.assertEqual(self.decision(), 'artifact-entries')

    def test_independent_inputs(self):
        for field, value in [('source', 'c' * 40), ('inputs', {'dev.json': 'changed'}),
                             ('workflow_blob', 'changed'), ('runtime', {'runner': 'changed'}),
                             ('run_id', 99), ('attempt', 2), ('job', 'other'),
                             ('event', 'pull_request'), ('ref', 'refs/tags/v1.2.3')]:
            with self.subTest(field=field):
                self.data = fixture()
                self.payload(lambda payload: payload.update({field: value}))
                self.assertEqual(self.decision(), 'input-identity')

    def test_check_coverage_and_skip(self):
        self.payload(lambda payload: payload.update(coverage={'audit': 'SKIP'}))
        self.assertEqual(self.decision(), 'check-coverage')
        self.data = fixture()
        self.payload(lambda payload: payload['coverage'].update(unit='SKIP'))
        self.assertEqual(self.decision(), 'check-result')

    def test_unavailable_is_not_green(self):
        self.data['actions/workflows/ci.yml'] = e.Refusal('command-failed')
        self.assertEqual(self.decision(), 'command-failed')

    def test_tag_moved_and_annotation(self):
        self.data['git/tags/' + 't' * 40]['object']['sha'] = 'c' * 40
        self.assertEqual(self.decision(), 'tag-source')
        self.data = fixture()
        self.data['git/ref/tags/v1.2.3']['object']['type'] = 'commit'
        self.assertEqual(self.decision(), 'tag-annotated')

    def test_tag_object_changed_during_collection(self):
        count = [0]
        def reference():
            count[0] += 1
            return {'ref': 'refs/tags/v1.2.3', 'object': {'type': 'tag',
                    'sha': ('t' if count[0] == 1 else 'u') * 40}}
        self.data['git/ref/tags/v1.2.3'] = reference
        self.data['git/tags/' + 'u' * 40] = {'object': {'type': 'commit', 'sha': SHA}}
        self.assertEqual(self.decision(), 'tag-changed')

    def test_attempt_race(self):
        value = copy.deepcopy(self.selected_run)
        value['run_attempt'] = 2
        self.data['actions/runs/100'] = value
        self.assertEqual(self.decision(), 'run-race')

    def test_attempt_changed_after_collection(self):
        count = [0]
        original = copy.deepcopy(self.selected_run)
        def run():
            count[0] += 1
            return dict(original, run_attempt=1 if count[0] == 1 else 2)
        self.data['actions/runs/100'] = run
        self.assertEqual(self.decision(), 'attempt-changed')

    def test_newer_run_during_collection(self):
        count = [0]
        original = copy.deepcopy(self.selected_run)
        def page():
            count[0] += 1
            run = original if count[0] == 1 else dict(original, id=101, status='in_progress')
            return {'total_count': 1, 'workflow_runs': [run]}
        self.data['actions/workflows/9/runs?head_sha=' + SHA + '&per_page=100&page=1'] = page
        self.assertEqual(self.decision(), 'latest-changed')

    def test_partial_rerun(self):
        self.selected_run['run_attempt'] = 2
        self.data['actions/runs/100/attempts/2/jobs?per_page=100&page=1'] = {'total_count': 1, 'jobs': [self.jobs[0]]}
        self.assertEqual(self.decision(), 'job-coverage')

    def test_matrix_all_four_legs(self):
        workflow = self.config['workflows']['ci']
        workflow['jobs'] = {str(i): dict(workflow['jobs']['test'], name='shard ' + str(i)) for i in range(1, 5)}
        page = self.data['actions/runs/100/attempts/1/jobs?per_page=100&page=1']
        page['jobs'] = [dict(self.jobs[0], id=210+i, name='shard '+str(i)) for i in range(1, 4)] + [self.jobs[1]]
        page['total_count'] = 4
        self.assertEqual(self.decision(), 'job-coverage')

    def test_valid_matrix_all_four_and_missing_leg(self):
        workflow = self.config['workflows']['ci']
        workflow['jobs'] = {str(i): dict(workflow['jobs']['test'], name='shard ' + str(i)) for i in range(1, 5)}
        original_job = copy.deepcopy(self.jobs[0])
        skipped = copy.deepcopy(self.jobs[1])
        jobs = [dict(original_job, id=210+i, name='shard '+str(i)) for i in range(1, 5)] + [skipped]
        self.data['actions/runs/100/attempts/1/jobs?per_page=100&page=1'] = {'total_count':5, 'jobs':jobs}
        original_artifact = copy.deepcopy(self.artifact)
        with zipfile.ZipFile(io.BytesIO(self.data['actions/artifacts/300/zip'])) as archive:
            original_payload = json.loads(archive.read('evidence.json'))
        artifacts = []
        for i in range(1, 5):
            raw = zip_payload(dict(original_payload, job=str(i)))
            artifacts.append(dict(original_artifact, id=300+i, name='ci-evidence-ci-%d-1'%i,
                                  size_in_bytes=len(raw), digest='sha256:'+hashlib.sha256(raw).hexdigest()))
            self.data['actions/artifacts/%d/zip'%(300+i)] = raw
        self.data['actions/runs/100/artifacts?per_page=100&page=1'] = {'total_count':4,'artifacts':artifacts}
        self.assertEqual(self.decision(), 'reuse')
        self.data['actions/runs/100/attempts/1/jobs?per_page=100&page=1'] = {'total_count':4,'jobs':jobs[1:]}
        self.assertEqual(self.decision(), 'job-coverage')

    def test_final_latest_changed(self):
        count = [0]
        original = copy.deepcopy(self.selected_run)
        def page():
            count[0] += 1
            run = original if count[0] < 3 else dict(original, id=101, status='in_progress')
            return {'total_count':1,'workflow_runs':[run]}
        self.data['actions/workflows/9/runs?head_sha='+SHA+'&per_page=100&page=1'] = page
        self.assertEqual(self.decision(), 'final-latest-changed')

    def test_pagination(self):
        rows = [{'id': i} for i in range(101)]
        api = FakeAPI({'list?per_page=100&page=1': {'total_count':101,'items':rows[:100]},
                       'list?per_page=100&page=2': {'total_count':101,'items':rows[100:]}})
        self.assertEqual(api.pages('list', 'items'), rows)
        self.assertEqual(len(api.calls), 2)

    def test_pagination_refusals(self):
        cases = [('pagination-cap', {'total_count':1000,'items':[]}),
                 ('pagination-shape', {'total_count':1,'items':None}),
                 ('pagination-count', {'total_count':0,'items':[{'id':1}]}),
                 ('pagination-partial', {'total_count':2,'items':[{'id':1}]}),
                 ('pagination-duplicate', {'total_count':2,'items':[{'id':1},{'id':1}]})]
        for reason, page in cases:
            with self.subTest(reason=reason):
                try:
                    FakeAPI({'list?per_page=100&page=1':page,
                             'list?per_page=100&page=2': {'total_count':2,'items':[{'id':2}]}}).pages('list','items')
                    result = 'reuse'
                except e.Refusal as error:
                    result = str(error)
                self.assertEqual(result, reason)

    def test_pagination_oversized_complete_page(self):
        rows = [{'id': i} for i in range(101)]
        api = FakeAPI({'list?per_page=100&page=1': {'total_count':101,'items':rows}})
        with self.assertRaisesRegex(e.Refusal, '^pagination-shape$'):
            api.pages('list', 'items')

    def test_pagination_partial_with_complete_followup(self):
        api = FakeAPI({'list?per_page=100&page=1': {'total_count':2,'items':[{'id':1}]},
                       'list?per_page=100&page=2': {'total_count':2,'items':[{'id':2}]}})
        with self.assertRaisesRegex(e.Refusal, '^pagination-partial$'):
            api.pages('list', 'items')

    def test_pagination_changed(self):
        api = FakeAPI({'list?per_page=100&page=1': {'total_count':101,'items':[{'id':i} for i in range(100)]},
                       'list?per_page=100&page=2': {'total_count':102,'items':[{'id':100}]}})
        with self.assertRaisesRegex(e.Refusal, '^pagination-changed$'):
            api.pages('list','items')


class ScopedRunnerImages(unittest.TestCase):
    """Handwritten API/clock fixtures exercise the full verify entry, not a matcher shortcut."""
    def setUp(self):
        self.scope = {
            'repository': 'Judgment-Pack/judgment-pack-agent', 'repository_id': 1424951648,
            'tag': 'v0.11.0', 'expires_at': '2026-11-07T00:00:00Z',
            'image_versions': ['20261023.163.3', '20261026.180.1'],
            'runner': {'ImageOS': 'ubuntu26', 'RUNNER_OS': 'Linux', 'RUNNER_ARCH': 'X64',
                       'os_release': 'cf72627ff81aef345d0a9f3807eb80f1035e0f80fe2932d12d94efc02fd68104'},
            'approval_record': 'docs/security/v011-runner-image-exception.md',
            'approval_sha256': 'd' * 64}
        self.config = copy.deepcopy(CONFIG)
        self.config.update(repository=self.scope['repository'], runner_image_exception=self.scope)
        self.source = dict(copy.deepcopy(SOURCE), repository=self.scope['repository'],
                           repository_id=1424951648)
        self.source['inputs'][self.scope['approval_record']] = 'd' * 64
        self.producer = {'runner': dict(self.scope['runner'], ImageVersion='20261023.163.3'),
                         'tools': {'node': 'v22.23.3', 'npm': '10.9.9'}}
        self.verifier = {'runner': dict(self.scope['runner'], ImageVersion='20261026.180.1'),
                         'tools': {'node': 'v22.23.3', 'npm': '10.9.9'}}
        self.config['profiles']['base'] = {'node': ['node', '--version'], 'npm': ['npm', '--version']}
        self.release_tag = 'v0.11.0'
        self.build_data()
        self.commands = patch.object(e, 'command', return_value=('b' * 40 + '\n').encode())
        self.runtime_patch = patch.object(e, 'runtime', side_effect=lambda *_: copy.deepcopy(self.verifier))
        self.clock = patch.object(e, 'utc_now', return_value=dt.datetime(2026, 10, 28, tzinfo=dt.timezone.utc))
        self.commands.start()
        self.runtime_patch.start()
        self.clock_mock = self.clock.start()
        self.addCleanup(self.commands.stop)
        self.addCleanup(self.runtime_patch.stop)
        self.addCleanup(self.clock.stop)

    def build_data(self):
        self.data = fixture(self.source, self.config, self.producer)
        ref = self.data.pop('git/ref/tags/v1.2.3')
        ref['ref'] = 'refs/tags/' + self.release_tag
        self.data['git/ref/tags/' + self.release_tag] = ref

    def payload(self, change):
        with zipfile.ZipFile(io.BytesIO(self.data['actions/artifacts/300/zip'])) as archive:
            value = json.loads(archive.read('evidence.json'))
        change(value)
        raw = zip_payload(value)
        self.data['actions/artifacts/300/zip'] = raw
        artifact = self.data['actions/runs/100/artifacts?per_page=100&page=1']['artifacts'][0]
        artifact.update(size_in_bytes=len(raw), digest='sha256:' + hashlib.sha256(raw).hexdigest())

    def decision(self):
        try:
            self.result = e.verify(FakeAPI(self.data), self.config, self.source, self.release_tag, NOW)
            return 'reuse'
        except e.Refusal as error:
            return str(error)

    def test_both_directions_and_equal_images_keep_raw_identities(self):
        for producer in ['20261023.163.3', '20261026.180.1']:
            for verifier in ['20261023.163.3', '20261026.180.1']:
                with self.subTest(producer=producer, verifier=verifier):
                    self.producer['runner']['ImageVersion'] = producer
                    self.verifier['runner']['ImageVersion'] = verifier
                    self.build_data()
                    before = self.data['actions/artifacts/300/zip']
                    self.assertEqual(self.decision(), 'reuse')
                    job = self.result['workflows'][0]['jobs'][0]
                    self.assertEqual(job['runtime'], self.producer)
                    self.assertEqual(job['verifier_runtime'], self.verifier)
                    self.assertEqual(job['image_version_exception_used'], producer != verifier)
                    self.assertEqual(self.result['runner_image_exception'], dict(self.scope, used=producer != verifier))
                    self.assertEqual(self.data['actions/artifacts/300/zip'], before)

    def test_wrong_repository_name_id_or_config_refuses(self):
        for name, ident in [('other/project', 1424951648), (self.scope['repository'], 1424951649)]:
            with self.subTest(repository=name, repository_id=ident):
                self.source.update(repository=name, repository_id=ident)
                self.config['repository'] = name
                self.build_data()
                self.assertEqual(self.decision(), 'image-exception-repository')
        self.source.update(repository=self.scope['repository'], repository_id=1424951648)
        self.config['repository'] = 'other/project'
        self.build_data()
        self.assertEqual(self.decision(), 'image-exception-repository')

    def test_later_or_other_tag_refuses_even_before_expiry(self):
        for tag in ['v0.11.1', 'v0.12.0', 'v0.10.0']:
            with self.subTest(tag=tag):
                self.release_tag = tag
                self.build_data()
                self.assertEqual(self.decision(), 'image-exception-tag')

    def test_malformed_or_widened_scope_refuses(self):
        original = copy.deepcopy(self.scope)
        cases = [None, {}, dict(original, unexpected=True),
                 dict(original, tag='v0.12.0'), dict(original, repository_id=12),
                 dict(original, expires_at='2026-12-01T00:00:00Z'),
                 dict(original, expires_at='not-a-time'),
                 dict(original, image_versions=['20261023.163.3', '*']),
                 dict(original, approval_record='other.md')]
        missing = dict(original)
        del missing['approval_sha256']
        for scope in cases + [missing]:
            with self.subTest(scope=scope):
                self.config['runner_image_exception'] = scope
                self.assertEqual(self.decision(), 'image-exception-config')

    def test_scope_cannot_add_an_image(self):
        self.scope['image_versions'].append('20261030.999.1')
        self.producer['runner']['ImageVersion'] = '20261030.999.1'
        self.build_data()
        self.assertEqual(self.decision(), 'image-exception-config')

    def test_missing_malformed_or_unbound_approval_hash_refuses(self):
        for digest in ['', 'wrong', 'D' * 64, 'e' * 64]:
            with self.subTest(digest=digest):
                self.scope['approval_sha256'] = digest
                self.assertEqual(self.decision(), 'image-exception-approval')
        self.scope['approval_sha256'] = 'd' * 64
        del self.source['inputs'][self.scope['approval_record']]
        self.assertEqual(self.decision(), 'image-exception-approval')

    def test_expiry_is_real_clock_strict_including_matching_images(self):
        self.producer = copy.deepcopy(self.verifier)
        self.build_data()
        for instant in [dt.datetime(2026, 11, 7, tzinfo=dt.timezone.utc),
                        dt.datetime(2026, 11, 7, 0, 0, 1, tzinfo=dt.timezone.utc)]:
            with self.subTest(instant=instant):
                self.clock_mock.return_value = instant
                # NOW is earlier: the run-age argument must not set the approval clock.
                self.assertEqual(self.decision(), 'image-exception-expired')

    def test_collection_crossing_expiry_refuses(self):
        self.clock_mock.side_effect = [dt.datetime(2026, 11, 6, 23, 59, 59, tzinfo=dt.timezone.utc),
                                       dt.datetime(2026, 11, 7, tzinfo=dt.timezone.utc)]
        self.assertEqual(self.decision(), 'image-exception-expired')
        self.assertEqual(self.clock_mock.call_count, 2)

    def test_third_image_in_either_actual_runtime_refuses(self):
        for target in ['producer', 'verifier']:
            with self.subTest(target=target):
                original = copy.deepcopy(getattr(self, target))
                getattr(self, target)['runner']['ImageVersion'] = '20261030.999.1'
                self.build_data()
                self.assertEqual(self.decision(), 'image-exception-image')
                setattr(self, target, original)

    def test_os_platform_arch_and_release_hash_in_either_runtime_refuse(self):
        for target in ['producer', 'verifier']:
            for key, value in [('ImageOS', 'ubuntu24'), ('RUNNER_OS', 'Windows'),
                               ('RUNNER_ARCH', 'ARM64'), ('os_release', 'different')]:
                with self.subTest(target=target, key=key):
                    original = copy.deepcopy(getattr(self, target))
                    getattr(self, target)['runner'][key] = value
                    self.build_data()
                    self.assertEqual(self.decision(), 'image-exception-runner')
                    setattr(self, target, original)

    def test_missing_extra_or_malformed_runtime_members_refuse(self):
        for target in ['producer', 'verifier']:
            original = copy.deepcopy(getattr(self, target))
            for value in [dict(original, extra='value'), {'runner': original['runner']}]:
                with self.subTest(target=target, runtime=value):
                    setattr(self, target, value)
                    self.build_data()
                    self.assertEqual(self.decision(), 'image-exception-runtime')
            for runner in [None, dict(original['runner'], extra='value'),
                           {k: v for k, v in original['runner'].items() if k != 'ImageVersion'}]:
                with self.subTest(target=target, runner=runner):
                    setattr(self, target, dict(original, runner=runner))
                    self.build_data()
                    self.assertEqual(self.decision(), 'image-exception-runner')
            setattr(self, target, original)

    def test_extra_runtime_member_cannot_bypass_scope(self):
        self.producer['unknown'] = 'must not be ignored'
        self.build_data()
        self.assertEqual(self.decision(), 'image-exception-runtime')

    def test_profile_tool_mismatch_still_refuses(self):
        for target in ['producer', 'verifier']:
            with self.subTest(target=target):
                original = copy.deepcopy(getattr(self, target))
                getattr(self, target)['tools']['node'] = 'v22.99.0'
                self.build_data()
                self.assertEqual(self.decision(), 'input-identity')
                setattr(self, target, original)

    def test_every_other_payload_member_stays_exact(self):
        for field, value in [('source', 'c' * 40), ('repository_id', 12),
                             ('inputs', {}), ('workflow', 'other.yml'), ('workflow_blob', 'changed'),
                             ('run_id', 101), ('attempt', 2), ('event', 'pull_request'),
                             ('ref', 'refs/heads/other'), ('job', 'other'), ('unknown', True)]:
            with self.subTest(field=field):
                self.build_data()
                self.payload(lambda payload: payload.update({field: value}))
                self.assertEqual(self.decision(), 'input-identity')
        self.build_data()
        self.payload(lambda payload: payload.pop('source'))
        self.assertEqual(self.decision(), 'input-identity')

    def test_payload_identity_types_stay_exact(self):
        for field, value in [('schema', True), ('schema', 1.0), ('attempt', True),
                             ('attempt', 1.0), ('repository_id', 1424951648.0)]:
            with self.subTest(field=field, value=value):
                self.build_data()
                self.payload(lambda payload: payload.update({field: value}))
                self.assertEqual(self.decision(), 'input-identity')

    def test_coverage_and_latest_attempt_still_refuse(self):
        self.payload(lambda payload: payload['coverage'].update(unit='SKIP'))
        self.assertEqual(self.decision(), 'check-result')
        self.build_data()
        self.payload(lambda payload: payload['coverage'].pop('unit'))
        self.assertEqual(self.decision(), 'check-coverage')
        self.build_data()
        self.data['actions/runs/100']['status'] = 'in_progress'
        self.assertEqual(self.decision(), 'run-result')

    def test_main_verify_reports_exception_and_real_runtime(self):
        # Only API timestamps follow the real CLI age clock; identity expectations stay handwritten.
        now = dt.datetime.now(dt.timezone.utc)
        def iso(seconds):
            return (now + dt.timedelta(seconds=seconds)).isoformat()
        self.data['actions/runs/100'].update(created_at=iso(-600), run_started_at=iso(-599))
        for job in self.data['actions/runs/100/attempts/1/jobs?per_page=100&page=1']['jobs']:
            job.update(started_at=iso(-598), completed_at=iso(-300))
        self.data['actions/runs/100/artifacts?per_page=100&page=1']['artifacts'][0]['created_at'] = iso(-301)
        output = io.StringIO()
        with patch.object(e, 'source_context', return_value=(self.config, self.source)), \
             patch.object(e, 'API', return_value=FakeAPI(self.data)), \
             patch.dict(os.environ, RELEASE_TAG='v0.11.0'), \
             contextlib.redirect_stdout(output), contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(e.main(['verify']), 0)
        report = json.loads(output.getvalue())
        self.assertTrue(report['runner_image_exception']['used'])
        self.assertEqual(report['workflows'][0]['jobs'][0]['verifier_runtime'], self.verifier)

    def test_capture_retains_actual_identity_without_exception_normalization(self):
        with tempfile.TemporaryDirectory(prefix='scoped-image-capture-') as directory:
            log = Path(directory) / 'checks.log'
            log.write_text('PASS unit 1s\nSKIP audit optional\n')
            for image in ['20261023.163.3', '20261026.180.1']:
                self.verifier['runner']['ImageVersion'] = image
                with patch.dict(os.environ, GITHUB_REF='refs/heads/main', GITHUB_EVENT_NAME='push',
                                GITHUB_JOB='test', GITHUB_RUN_ID='100', GITHUB_RUN_ATTEMPT='1'):
                    captured = e.capture(self.config, self.source, 'ci', 'test', [('check', str(log))])
                self.assertEqual(captured['runtime'], self.verifier)
                self.assertNotIn('runner_image_exception', captured)

    def test_repository_config_carries_no_image_exception_after_v011(self):
        # The v0.11.0 scope refuses every other tag; it is not extended (agent#169, 2026-10-30).
        config = json.loads((ROOT / '.github/ci-evidence.json').read_text())
        self.assertNotIn('runner_image_exception', config)

    def test_unconfigured_legacy_matching_and_output_remain_exact(self):
        del self.config['runner_image_exception']
        self.assertEqual(self.decision(), 'input-identity')
        self.producer = copy.deepcopy(self.verifier)
        self.build_data()
        self.assertEqual(self.decision(), 'reuse')
        self.assertNotIn('runner_image_exception', self.result)
        self.assertNotIn('verifier_runtime', self.result['workflows'][0]['jobs'][0])


class SmallBoundaries(unittest.TestCase):
    def outcome(self, fn):
        try:
            fn()
            return 'accepted'
        except e.Refusal as error:
            return str(error)
        except Exception as error:
            return 'unexpected:'+type(error).__name__

    def test_process_and_timestamp_bounds(self):
        self.assertEqual(self.outcome(lambda:e.command([sys.executable,'-c','raise SystemExit(7)'])), 'command-failed')
        self.assertEqual(self.outcome(lambda:e.command([sys.executable,'-c','print("large")'],limit=1)), 'response-size')
        self.assertEqual(self.outcome(lambda:e.timestamp(1)), 'timestamp')

    def test_source_config_matches_committed_bytes_and_version(self):
        root=Path(tempfile.mkdtemp(prefix='ci-evidence-config-'))
        path=root/'config.json'
        cfg=copy.deepcopy(CONFIG)
        original=json.dumps(cfg).encode()
        def git(argv,limit=0):
            if argv[1:3]==['rev-parse','HEAD']:
                return (SHA+'\n').encode()
            if argv[1]=='diff':
                return b''
            return original
        env={'GITHUB_SHA':SHA,'GITHUB_REPOSITORY':REPO,'GITHUB_REPOSITORY_ID':'12'}
        path.write_bytes(original+b'\n')
        with patch.dict(os.environ,env), patch.object(e,'command',side_effect=git):
            self.assertEqual(self.outcome(lambda:e.source_context(str(path))), 'config-source')
            cfg['max_age_hours']=49
            original=json.dumps(cfg).encode()
            path.write_bytes(original)
            self.assertEqual(self.outcome(lambda:e.source_context(str(path))), 'config-version')

    def test_empty_tool_and_duplicate_check_configuration(self):
        cfg=copy.deepcopy(CONFIG)
        cfg['profiles']['base']={'empty':[sys.executable,'-c','pass']}
        with patch.dict(os.environ,ImageOS='ubuntu24',ImageVersion='image',RUNNER_OS='Linux',RUNNER_ARCH='X64'):
            self.assertEqual(self.outcome(lambda:e.runtime(cfg,'base')), 'tool-identity')
        job={'checks':['unit','unit']}
        # Explicit check sets do not read a real repository manifest's check names.
        self.assertEqual(self.outcome(lambda:e.required_checks(cfg,job)), 'check-config')


class CIWorkflowScratch(unittest.TestCase):
    def test_each_job_exports_unique_contained_scratch_before_checks(self):
        workflow = (ROOT / '.github/workflows/ci.yml').read_text()
        jobs = workflow.split('\n  contract:\n', 1)[1]
        contract, check = jobs.split('\n  check:\n', 1)
        for job, body in [('contract', contract), ('check', check)]:
            with self.subTest(job=job), tempfile.TemporaryDirectory(prefix='ci scratch ') as directory:
                before, script = body.split('      - name: Initialize job scratch\n        run: |\n', 1)
                self.assertNotIn('scripts/dev/check.sh', before)
                script = script.split('      - ', 1)[0]
                script = '\n'.join(line[10:] for line in script.splitlines() if line.startswith('          '))
                base = Path(directory).resolve()
                exports = base / 'github-env'
                env = dict(os.environ, RUNNER_TEMP=str(base), GITHUB_ENV=str(exports),
                           GITHUB_RUN_ID='123', GITHUB_RUN_ATTEMPT='2', GITHUB_JOB=job)
                roots = []
                for _ in range(2):
                    exports.write_text('')
                    subprocess.run(['bash', '-eu', '-c', script], env=env, check=True)
                    values = dict(line.split('=', 1) for line in exports.read_text().splitlines())
                    for key in ['JPDEV_SCRATCH', 'TMPDIR']:
                        self.assertIn(key, values, f'{job} must export {key}')
                    scratch, tmp = (Path(values[key]).resolve() for key in ['JPDEV_SCRATCH', 'TMPDIR'])
                    self.assertEqual(scratch.parent, base)
                    self.assertEqual(tmp.parent, scratch)
                    self.assertTrue(tmp.is_dir())
                    child = subprocess.run([sys.executable, '-c',
                                            'import tempfile; print(tempfile.gettempdir())'],
                                           env=dict(env, **values), check=True, text=True,
                                           stdout=subprocess.PIPE)
                    self.assertEqual(Path(child.stdout.strip()).resolve(), tmp)
                    roots.append(scratch)
                self.assertNotEqual(*roots)


class GatewayReceiptCoverage(unittest.TestCase):
    """The actual producer proof remains mandatory in the existing CI evidence gate."""
    def setUp(self):
        self.config = json.loads((ROOT / '.github/ci-evidence.json').read_text())
        self.job = self.config['workflows']['ci']['jobs']['check']
        manifest = (ROOT / 'dev.json').read_text()
        self.manifest = patch.object(e.Path, 'read_text', return_value=manifest)
        self.manifest.start()
        self.addCleanup(self.manifest.stop)

    def test_gateway_receipt_checks_are_required(self):
        required = e.required_checks(self.config, self.job)
        for name in ['gateway-receipt-process', 'gateway-receipts']:
            self.assertIn(name, required)
            self.assertNotIn(name, self.job.get('optional_skips', []))

    def test_gateway_receipt_missing_or_skipped_evidence_is_refused(self):
        passing = dict.fromkeys(e.required_checks(self.config, self.job), 'PASS')
        e.validate_coverage(self.config, self.job, passing)
        for name in ['gateway-receipt-process', 'gateway-receipts']:
            missing = dict(passing)
            del missing[name]
            with self.subTest(name=name, status='missing'):
                with self.assertRaisesRegex(e.Refusal, '^check-coverage$'):
                    e.validate_coverage(self.config, self.job, missing)
            for status in ['SKIP', 'FAIL']:
                with self.subTest(name=name, status=status):
                    with self.assertRaisesRegex(e.Refusal, '^check-result$'):
                        e.validate_coverage(self.config, self.job, dict(passing, **{name: status}))

    def test_gateway_producer_pin_and_runner_are_evidence_inputs(self):
        for path in ['tests/fixtures/gateway-receipts/source.json',
                     'scripts/gateway-receipt-check.mjs',
                     'scripts/gateway-receipt-process.mjs',
                     'tests/fixtures/gateway-receipts/producer_test.go.txt']:
            with self.subTest(path=path):
                self.assertIn(path, self.config['inputs'])


class RealCLI(unittest.TestCase):
    """Exercise command, API pagination/zip decoding and decision through the public CLI."""
    @classmethod
    def setUpClass(cls):
        cls.root = Path(tempfile.mkdtemp(prefix='ci-evidence-fixture-'))
        cls.env = dict(os.environ, GITHUB_REPOSITORY=REPO, GITHUB_REPOSITORY_ID='12',
                       ImageOS='ubuntu24', ImageVersion='20261020.1', RUNNER_OS='Linux', RUNNER_ARCH='X64',
                       RELEASE_TAG='v1.2.3', CI='true')
        (cls.root/'.github/workflows').mkdir(parents=True)
        (cls.root/'bin').mkdir()
        (cls.root/'ci_evidence.py').write_bytes(HELPER.read_bytes())
        cfg = copy.deepcopy(CONFIG)
        cfg['inputs'] = ['dev.json', 'ci_evidence.py']
        (cls.root/'.github/ci-evidence.json').write_text(json.dumps(cfg))
        (cls.root/'.github/workflows/ci.yml').write_text('name: Fixture CI\njobs:\n  test: {}\n')
        (cls.root/'dev.json').write_text('{"checks":[{"name":"unit"},{"name":"audit"}]}\n')
        for argv in [['git','init','-q'],['git','add','.'],['git','-c','user.name=Fixture','-c','user.email=fixture@example.invalid','commit','-qm','fixture']]:
            subprocess.run(argv,cwd=cls.root,check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        sha = subprocess.check_output(['git','rev-parse','HEAD'],cwd=cls.root,text=True).strip()
        cls.env['GITHUB_SHA'] = sha
        hashes = {path:hashlib.sha256((cls.root/path).read_bytes()).hexdigest() for path in
                  ['dev.json','ci_evidence.py','.github/ci-evidence.json','.github/workflows/ci.yml']}
        source = dict(SOURCE,source=sha,inputs=hashes)
        content = (cls.root/'.github/workflows/ci.yml').read_bytes()
        blob = hashlib.sha1(b'blob '+str(len(content)).encode()+b'\0'+content).hexdigest()
        rt = copy.deepcopy(RUNTIME)
        rt['runner']['os_release'] = hashlib.sha256(Path('/etc/os-release').read_bytes()).hexdigest()
        cls.data = fixture(source,cfg,rt,blob)
        # Keep fixture clock current by independently adjusting only API timestamps and ZIP metadata.
        now = dt.datetime.now(dt.timezone.utc)
        def iso(delta):
            return (now+dt.timedelta(seconds=delta)).isoformat().replace('+00:00','Z')
        cls.data['actions/runs/100'].update(created_at=iso(-600),run_started_at=iso(-599))
        for job in cls.data['actions/runs/100/attempts/1/jobs?per_page=100&page=1']['jobs']:
            job.update(started_at=iso(-598),completed_at=iso(-300))
        cls.data['actions/runs/100/artifacts?per_page=100&page=1']['artifacts'][0]['created_at']=iso(-301)
        gh = cls.root/'bin/gh'
        gh.write_text('#!'+sys.executable+'\nimport base64,json,os,sys\ndata=json.load(open(os.environ["FIXTURE_API"]))\nkey=sys.argv[-1].split("/",3)[-1]\nvalue=data[key]\nif isinstance(value,dict) and "_binary" in value:\n sys.stdout.buffer.write(base64.b64decode(value["_binary"]))\nelse:\n print(json.dumps(value))\n')
        gh.chmod(0o755)
        cls.env['PATH'] = str(cls.root/'bin')+os.pathsep+os.environ['PATH']

    def cli(self,data,mode='verify',extra=(),env_changes=None):
        responses = self.root/'responses.json'
        responses.write_text(json.dumps({key:({'_binary':base64.b64encode(value).decode()} if isinstance(value,bytes) else value) for key,value in data.items()}))
        env = dict(self.env,FIXTURE_API=str(responses))
        env.update(env_changes or {})
        return subprocess.run([sys.executable,str(self.root/'ci_evidence.py'),mode,*extra],cwd=self.root,
                              env=env,text=True,stdout=subprocess.PIPE,stderr=subprocess.PIPE,timeout=25)

    def test_cli_valid_and_untrusted_artifact(self):
        result=self.cli(self.data)
        self.assertEqual(result.returncode,0,result.stderr)
        self.assertEqual(json.loads(result.stdout)['decision'],'reuse')
        data=copy.deepcopy(self.data)
        data['actions/runs/100']['conclusion']='failure'
        result=self.cli(data)
        self.assertEqual(result.returncode,1)
        self.assertIn('run-result',result.stderr)

    def test_cli_source_and_runtime_refusals(self):
        cases = [({'GITHUB_SHA':'invalid'},'source-sha'),
                 ({'GITHUB_SHA':'c'*40},'checkout-sha'),
                 ({'GITHUB_REPOSITORY':'other/project'},'context-repository'),
                 ({'GITHUB_REPOSITORY_ID':'0'},'context-repository-id'),
                 ({'ImageVersion':''},'runner-identity'),
                 ({'RUNNER_ARCH':'ARM64'},'runner-platform'),
                 ({'RELEASE_TAG':'v01.2.3'},'tag-syntax')]
        for env,reason in cases:
            with self.subTest(reason=reason):
                result=self.cli(self.data,env_changes=env)
                self.assertEqual(result.returncode,1)
                self.assertIn(reason,result.stderr)

    def test_cli_producer_and_summary_refusals(self):
        log=self.root/'producer.log'
        base={'GITHUB_REF':'refs/heads/main','GITHUB_EVENT_NAME':'push',
              'GITHUB_JOB':'test','GITHUB_RUN_ID':'100','GITHUB_RUN_ATTEMPT':'1'}
        extra=['--workflow','ci','--job','test','--check-log',str(log)]
        log.write_text('PASS unit 1s\nSKIP audit needs AUDIT\n')
        for env,reason in [({'GITHUB_REF':'refs/tags/v1.2.3'},'capture-ref'),
                           ({'GITHUB_EVENT_NAME':'pull_request'},'capture-event'),
                           ({'GITHUB_JOB':'other'},'capture-job')]:
            result=self.cli(self.data,'capture',extra,dict(base,**env))
            self.assertEqual(result.returncode,1)
            self.assertIn(reason,result.stderr)
        for text,reason in [('infrastructure failure: log unavailable\nPASS unit 1s\nSKIP audit optional\n','check-infrastructure'),
                            ('PASS unit 1s\nPASS unit 1s\nSKIP audit optional\n','check-duplicate')]:
            log.write_text(text)
            result=self.cli(self.data,'capture',extra,base)
            self.assertEqual(result.returncode,1)
            self.assertIn(reason,result.stderr)

    def test_cli_absent_and_malformed_api(self):
        data=copy.deepcopy(self.data)
        data['actions/workflows/ci.yml']={}
        result=self.cli(data)
        self.assertEqual(result.returncode,1)
        self.assertIn('unavailable-or-malformed-evidence',result.stderr)

    def test_capture_real_summary_and_optional_skip(self):
        log=self.root/'checks.log'
        log.write_text('== check summary (dev.json) ==\nPASS     unit 1s\nSKIP     audit needs AUDIT\n== 1 passed, 0 failed, 1 skipped, 0 not run ==\n')
        env=dict(self.env,GITHUB_REF='refs/heads/main',GITHUB_EVENT_NAME='push',
                 GITHUB_JOB='test',GITHUB_RUN_ID='100',GITHUB_RUN_ATTEMPT='1')
        result=subprocess.run([sys.executable,str(self.root/'ci_evidence.py'),'capture','--workflow','ci','--job','test','--check-log',str(log)],cwd=self.root,env=env,text=True,stdout=subprocess.PIPE,stderr=subprocess.PIPE)
        self.assertEqual(result.returncode,0,result.stderr)
        self.assertEqual(json.loads((self.root/'evidence.json').read_text())['coverage'],{'unit':'PASS','audit':'SKIP'})
        log.write_text('SKIP unit needs ENV\nSKIP audit needs AUDIT\n')
        result=subprocess.run([sys.executable,str(self.root/'ci_evidence.py'),'capture','--workflow','ci','--job','test','--check-log',str(log)],cwd=self.root,env=env,text=True,stdout=subprocess.PIPE,stderr=subprocess.PIPE)
        self.assertEqual(result.returncode,1)
        self.assertIn('check-result',result.stderr)


if __name__ == '__main__':
    unittest.main()
