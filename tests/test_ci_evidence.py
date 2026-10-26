"""Handwritten Actions fixtures; these are not live-service release evidence."""
import base64
import copy
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
