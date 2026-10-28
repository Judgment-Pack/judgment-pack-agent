#!/usr/bin/env python3
"""Trusted exact-source Actions evidence, shared with repository-owned adoptions.

Only capture and verify are public commands. API fixtures belong in tests, never CLI inputs.
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


class Refusal(Exception):
    pass


def need(condition, code):
    if not condition:
        raise Refusal(code)


def digest(data):
    return hashlib.sha256(data).hexdigest()


def command(argv, limit=8 * 1024 * 1024):
    # Spool to our TMPDIR, never accumulate unbounded remote output in memory or log stderr.
    with tempfile.TemporaryFile() as output:
        try:
            result = subprocess.run(argv, stdout=output, stderr=subprocess.DEVNULL,
                                    timeout=45, check=False)
        except (OSError, subprocess.TimeoutExpired) as error:
            raise Refusal('command-unavailable') from error
        need(result.returncode == 0, 'command-failed')
        need(output.tell() <= limit, 'response-size')
        output.seek(0)
        return output.read()


class API:
    def __init__(self, repository):
        self.base = 'repos/' + repository

    def get(self, suffix, binary=False):
        raw = command(['gh', 'api', '--hostname', 'github.com', '-H',
                       'Accept: application/vnd.github+json', '-H',
                       'X-GitHub-Api-Version: 2026-03-10', self.base + '/' + suffix])
        return raw if binary else json.loads(raw)

    def pages(self, suffix, key):
        rows, count = [], None
        for page in range(1, 11):
            separator = '&' if '?' in suffix else '?'
            value = self.get(suffix + separator + 'per_page=100&page=' + str(page))
            total, part = value.get('total_count'), value.get(key)
            need(type(total) is int and 0 <= total < 1000, 'pagination-cap')
            need(isinstance(part, list) and len(part) <= 100, 'pagination-shape')
            need(count is None or count == total, 'pagination-changed')
            count = total
            rows.extend(part)
            need(len(rows) <= count, 'pagination-count')
            if len(rows) == count:
                need(len({item['id'] for item in rows}) == count, 'pagination-duplicate')
                return rows
            need(len(part) == 100, 'pagination-partial')
        raise Refusal('pagination-cap')


def timestamp(value):
    need(isinstance(value, str), 'timestamp')
    return dt.datetime.fromisoformat(value.replace('Z', '+00:00'))


def source_context(config_path):
    sha = os.environ['GITHUB_SHA']
    need(re.fullmatch('[0-9a-f]{40}', sha), 'source-sha')
    need(command(['git', 'rev-parse', 'HEAD']).decode().strip() == sha, 'checkout-sha')
    command(['git', 'diff', '--quiet', 'HEAD', '--'])
    config_bytes = command(['git', 'show', sha + ':' + config_path])
    need(Path(config_path).read_bytes() == config_bytes, 'config-source')
    config = json.loads(config_bytes)
    need(config['schema'] == 1 and config['max_age_hours'] == 48, 'config-version')
    need(config['repository'] == os.environ['GITHUB_REPOSITORY'], 'context-repository')
    repository_id = int(os.environ['GITHUB_REPOSITORY_ID'])
    need(repository_id > 0, 'context-repository-id')
    paths = set(config['inputs'] + [config_path])
    paths.update(workflow['path'] for workflow in config['workflows'].values())
    hashes = {path: digest(command(['git', 'show', sha + ':' + path])) for path in sorted(paths)}
    return config, {'schema': 1, 'repository': config['repository'],
                    'repository_id': repository_id, 'source': sha, 'inputs': hashes}


def runtime(config, profile):
    keys = ['ImageOS', 'ImageVersion', 'RUNNER_OS', 'RUNNER_ARCH']
    runner = {key: os.environ.get(key, '') for key in keys}
    need(all(runner.values()), 'runner-identity')
    need(runner['RUNNER_OS'] == 'Linux' and runner['RUNNER_ARCH'] == 'X64', 'runner-platform')
    runner['os_release'] = digest(Path('/etc/os-release').read_bytes())
    tools = {name: command(argv, 65536).decode().strip()
             for name, argv in config['profiles'][profile].items()}
    need(all(tools.values()), 'tool-identity')
    return {'runner': runner, 'tools': tools}


def required_checks(config, spec):
    manifest = json.loads(Path('dev.json').read_text())
    checks = spec.get('checks', [])
    if checks == 'all':
        checks = [row['name'] for row in manifest['checks']]
        if 'parity' in manifest:
            checks.append('parity')
    result = list(checks)
    if spec.get('setup'):
        result += ['setup:' + row['name'] for row in manifest['setup']]
    need(len(result) == len(set(result)), 'check-config')
    return result


def coverage(config, spec, logs):
    result = {}
    for kind, path in logs:
        text = Path(path).read_text()
        need('infrastructure failure:' not in text, 'check-infrastructure')
        for status, name in re.findall(r'^(PASS|SKIP|FAIL|NOT RUN)\s+(\S+)(?:\s|$)', text, re.M):
            key = ('setup:' if kind == 'setup' else '') + name
            need(key not in result, 'check-duplicate')
            result[key] = status
    validate_coverage(config, spec, result)
    return result


def validate_coverage(config, spec, result):
    need(set(result) == set(required_checks(config, spec)), 'check-coverage')
    for name, status in result.items():
        need(status == 'PASS' or (status == 'SKIP' and name in spec.get('optional_skips', [])),
             'check-result')


def capture(config, source, workflow_key, job_key, logs):
    workflow = config['workflows'][workflow_key]
    spec = workflow['jobs'][job_key]
    need(os.environ['GITHUB_REF'] == 'refs/heads/main', 'capture-ref')
    need(os.environ['GITHUB_EVENT_NAME'] in workflow['events'], 'capture-event')
    job = spec['job_id']
    need(os.environ['GITHUB_JOB'] == job, 'capture-job')
    return dict(source, workflow=workflow['path'], workflow_blob=command([
        'git', 'rev-parse', source['source'] + ':' + workflow['path']]).decode().strip(),
        run_id=int(os.environ['GITHUB_RUN_ID']), attempt=int(os.environ['GITHUB_RUN_ATTEMPT']),
        event=os.environ['GITHUB_EVENT_NAME'], ref='refs/heads/main', job=job_key,
        runtime=runtime(config, spec['profile']), coverage=coverage(config, spec, logs))


def run_identity(run, source, workflow, workflow_id):
    need(run['repository']['full_name'] == source['repository'] and
         run['repository']['id'] == source['repository_id'], 'run-repository')
    need(run['head_repository']['full_name'] == source['repository'] and
         run['head_repository']['id'] == source['repository_id'], 'run-head-repository')
    need(run['head_sha'] == source['source'], 'run-sha')
    need(run['workflow_id'] == workflow_id and run['path'] == workflow['path'], 'run-workflow')
    need(run['head_branch'] == 'main' and run['event'] in workflow['events'] and
         not run.get('pull_requests'), 'run-trigger')
    need(type(run['id']) is int and run['id'] > 0 and
         type(run['run_attempt']) is int and run['run_attempt'] > 0, 'run-identity')


def select(api, source, workflow, workflow_id):
    runs = api.pages('actions/workflows/' + str(workflow_id) + '/runs?head_sha=' + source['source'],
                     'workflow_runs')
    # PRs and other refs are not applicable. Their absence never establishes trusted-main evidence.
    applicable = [run for run in runs if run.get('head_branch') == 'main' and
                  run.get('event') in workflow['events']]
    need(bool(applicable), 'run-missing')
    for run in applicable:
        run_identity(run, source, workflow, workflow_id)
    return max(applicable, key=lambda run: (timestamp(run['created_at']), run['id']))


def tag(api, name, sha):
    need(re.fullmatch(r'v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)', name), 'tag-syntax')
    ref = api.get('git/ref/tags/' + quote(name, safe=''))
    need(ref['ref'] == 'refs/tags/' + name and ref['object']['type'] == 'tag', 'tag-annotated')
    original = ref['object']['sha']
    obj = ref['object']
    for _ in range(5):
        if obj['type'] != 'tag':
            break
        obj = api.get('git/tags/' + obj['sha'])['object']
    need(obj['type'] == 'commit' and obj['sha'] == sha, 'tag-source')
    return original


def read_artifact(api, artifact, run, job):
    need(not artifact['expired'], 'artifact-expired')
    need(type(artifact['size_in_bytes']) is int and 0 < artifact['size_in_bytes'] <= 1048576,
         'artifact-size')
    need(artifact['workflow_run']['id'] == run['id'] and
         artifact['workflow_run']['head_sha'] == run['head_sha'] and
         artifact['workflow_run']['head_branch'] == 'main', 'artifact-run')
    need(timestamp(job['started_at']) <= timestamp(artifact['created_at']) <=
         timestamp(job['completed_at']), 'artifact-interval')
    raw = api.get('actions/artifacts/' + str(artifact['id']) + '/zip', binary=True)
    need(len(raw) == artifact['size_in_bytes'] and
         artifact['digest'] == 'sha256:' + digest(raw), 'artifact-digest')
    with zipfile.ZipFile(io.BytesIO(raw)) as archive:
        entries = archive.infolist()
        need(len(entries) == 1 and entries[0].filename == 'evidence.json' and
             entries[0].file_size <= 262144 and not entries[0].is_dir(), 'artifact-entries')
        return json.loads(archive.read(entries[0]))


def verify_workflow(api, config, source, key, now):
    workflow = config['workflows'][key]
    resolved = api.get('actions/workflows/' + quote(Path(workflow['path']).name, safe=''))
    need(resolved['path'] == workflow['path'] and type(resolved['id']) is int and
         resolved['id'] > 0 and resolved['state'] == 'active', 'workflow-identity')
    chosen = select(api, source, workflow, resolved['id'])
    run = api.get('actions/runs/' + str(chosen['id']))
    run_identity(run, source, workflow, resolved['id'])
    need((run['id'], run['run_attempt']) == (chosen['id'], chosen['run_attempt']), 'run-race')
    print(json.dumps({'selection': {'workflow_id': resolved['id'], 'workflow': workflow['path'],
                                  'run_id': run['id'], 'attempt': run['run_attempt'],
                                  'source': source, 'status': run['status'],
                                  'conclusion': run['conclusion']}}, sort_keys=True), file=sys.stderr)
    need(run['status'] == 'completed' and run['conclusion'] == 'success', 'run-result')
    age = (now - timestamp(run['run_started_at'])).total_seconds()
    need(0 <= age <= config['max_age_hours'] * 3600, 'run-age')
    jobs = api.pages('actions/runs/%s/attempts/%s/jobs' % (run['id'], run['run_attempt']), 'jobs')
    names = [job['name'] for job in jobs]
    expected_names = [spec['name'] for spec in workflow['jobs'].values()] + workflow.get('skipped', [])
    need(len(names) == len(set(names)) and set(names) == set(expected_names), 'job-coverage')
    by_name = {job['name']: job for job in jobs}
    artifacts = api.pages('actions/runs/' + str(run['id']) + '/artifacts', 'artifacts')
    reports = []
    for job_key, spec in workflow['jobs'].items():
        job = by_name[spec['name']]
        need(job['run_id'] == run['id'] and job['head_sha'] == source['source'] and
             job['head_branch'] == 'main' and job['status'] == 'completed' and
             job['conclusion'] == 'success' and job['labels'] == ['ubuntu-latest'], 'job-result')
        name = 'ci-evidence-%s-%s-%s' % (key, job_key, run['run_attempt'])
        matching = [artifact for artifact in artifacts if artifact['name'] == name]
        need(len(matching) == 1, 'artifact-coverage')
        payload = read_artifact(api, matching[0], run, job)
        validate_coverage(config, spec, payload['coverage'])
        expected = dict(source, workflow=workflow['path'], workflow_blob=command([
            'git', 'rev-parse', source['source'] + ':' + workflow['path']]).decode().strip(),
            run_id=run['id'], attempt=run['run_attempt'], event=run['event'],
            ref='refs/heads/main', job=job_key, runtime=runtime(config, spec['profile']),
            coverage=payload['coverage'])
        need(payload == expected, 'input-identity')
        reports.append({'job': job_key, 'job_id': job['id'], 'result': job['conclusion'],
                        'coverage': payload['coverage'], 'runtime': payload['runtime'],
                        'started_at': job['started_at'], 'completed_at': job['completed_at']})
    for name in workflow.get('skipped', []):
        job = by_name[name]
        need(job['run_id'] == run['id'] and job['status'] == 'completed' and
             job['conclusion'] == 'skipped', 'job-skip')
        reports.append({'name': name, 'result': 'skipped', 'reason': 'PR-only job on trusted main'})
    after = api.get('actions/runs/' + str(run['id']))
    need(after == run, 'attempt-changed')
    latest = select(api, source, workflow, resolved['id'])
    need((latest['id'], latest['run_attempt'], latest['status'], latest['conclusion']) ==
         (run['id'], run['run_attempt'], run['status'], run['conclusion']), 'latest-changed')
    return {'workflow_id': resolved['id'], 'workflow': workflow['path'],
            'workflow_blob': command(['git', 'rev-parse', source['source'] + ':' + workflow['path']]).decode().strip(),
            'event': run['event'], 'ref': 'refs/heads/main', 'run_id': run['id'], 'attempt': run['run_attempt'], 'jobs': reports}


def verify(api, config, source, release_tag, now):
    first_tag = tag(api, release_tag, source['source'])
    reports = [verify_workflow(api, config, source, key, now) for key in config['workflows']]
    # Recheck every workflow after all artifact collection; cross-workflow collection can take time.
    for key, report in zip(config['workflows'], reports):
        final = select(api, source, config['workflows'][key], report['workflow_id'])
        need((final['id'], final['run_attempt'], final['status'], final['conclusion']) ==
             (report['run_id'], report['attempt'], 'completed', 'success'), 'final-latest-changed')
    need(tag(api, release_tag, source['source']) == first_tag, 'tag-changed')
    return dict(source, decision='reuse', tag=release_tag, tag_object=first_tag, workflows=reports)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('mode', choices=['capture', 'verify'])
    parser.add_argument('--config', default='.github/ci-evidence.json')
    parser.add_argument('--workflow')
    parser.add_argument('--job')
    parser.add_argument('--check-log', action='append', default=[])
    parser.add_argument('--setup-log', action='append', default=[])
    parser.add_argument('--output', default='evidence.json')
    args = parser.parse_args(argv)
    try:
        config, source = source_context(args.config)
        if args.mode == 'capture':
            result = capture(config, source, args.workflow, args.job,
                             [('check', path) for path in args.check_log] +
                             [('setup', path) for path in args.setup_log])
            Path(args.output).write_text(json.dumps(result, sort_keys=True) + '\n')
        else:
            result = verify(API(source['repository']), config, source,
                            os.environ['RELEASE_TAG'], dt.datetime.now(dt.timezone.utc))
            print(json.dumps(result, sort_keys=True))
        return 0
    except (Refusal, KeyError, ValueError, TypeError, OSError, zipfile.BadZipFile) as error:
        print('ci-evidence: refuse: ' + (str(error) if isinstance(error, Refusal) else
                                       'unavailable-or-malformed-evidence'), file=sys.stderr)
        return 1


if __name__ == '__main__':
    sys.exit(main())
