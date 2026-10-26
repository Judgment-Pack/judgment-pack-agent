#!/usr/bin/env python3
"""Regenerate explicitly declared documents in disposable source copies (not an OS sandbox)."""
import argparse
import json
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import signal
import subprocess
import sys
import tempfile


class Invalid(ValueError):
    pass


def need(value, message):
    if not value:
        raise Invalid(message)


def relative(value):
    need(isinstance(value, str) and value and '\\' not in value, 'invalid relative path')
    path = PurePosixPath(value)
    need(not path.is_absolute() and all(p not in ('', '.', '..') for p in value.split('/')),
         'path must be a non-root relative child: ' + value)
    return value


def inside(root, value):
    path = root / relative(value)
    for part in [path, *path.parents]:
        if part == root:
            break
        need(not part.is_symlink(), 'symlink path is unsupported: ' + value)
    return path


def config(value):
    # changed_checks.py owns the optional sibling path mapping and its validation.
    need(isinstance(value, dict) and {'version', 'generators'} <= set(value) <= {'version', 'generators', 'changed'} and
         type(value['version']) is int and value['version'] == 1, 'expected version1 generator declaration')
    need(isinstance(value['generators'], list) and value['generators'], 'generators must be nonempty')
    names, outputs = set(), []
    for item in value['generators']:
        need(isinstance(item, dict) and set(item) <= {'name', 'run', 'outputs', 'timeout', 'dependencies'} and
             {'name', 'run', 'outputs'} <= set(item), 'invalid generator members')
        name = item['name']
        need(isinstance(name, str) and re.fullmatch(r'[A-Za-z0-9_.-]+', name) and name not in names,
             'invalid or duplicate generator name')
        names.add(name)
        need(isinstance(item['run'], str) and item['run'].strip(), 'generator run must be nonempty')
        timeout = item.get('timeout', 120)
        need(type(timeout) is int and 1 <= timeout <= 3600, 'timeout must be 1..3600 seconds')
        need(isinstance(item['outputs'], list) and item['outputs'], 'outputs must be nonempty')
        for output in item['outputs']:
            output = relative(output)
            need(not any(output == old or output.startswith(old + '/') or old.startswith(output + '/')
                         for old in outputs), 'overlapping output: ' + output)
            outputs.append(output)
        dependencies = item.get('dependencies', [])
        need(isinstance(dependencies, list) and all(isinstance(d, str) for d in dependencies) and
             len(set(dependencies)) == len(dependencies), 'invalid dependencies')
        for dependency in dependencies:
            relative(dependency)
            need(dependency.split('/')[-1] == 'node_modules', 'only explicit node_modules dependencies supported')
    dependencies = [d for item in value['generators'] for d in item.get('dependencies', [])]
    need(not any(o == d or o.startswith(d + '/') for o in outputs for d in dependencies),
         'output cannot be in a dependency tree')
    return value['generators']


def source_files(root):
    result = subprocess.run(['git', 'ls-files', '--cached', '--others', '--exclude-standard', '-z'],
                            cwd=root, check=True, stdout=subprocess.PIPE, timeout=30)
    paths = []
    excluded = {'.git', '.aws', '.ssh', '.codex', '.jpdev', '.venv', 'node_modules', '__pycache__',
                '.npm-cache', '.gocache', '.xdg-cache', '.cache'}
    scratch = Path(os.environ.get('JPDEV_SCRATCH', tempfile.gettempdir())).resolve()
    for raw in sorted(set(result.stdout.split(b'\0')) - {b''}):
        value = os.fsdecode(raw)
        parts = PurePosixPath(relative(value)).parts
        if any(p in excluded or p == '.env' or p.startswith('.env.') and p != '.env.example' for p in parts):
            continue
        path = inside(root, value)
        if scratch.is_relative_to(root) and (scratch == path or scratch in path.parents):
            continue
        need(path.is_file(), 'source file missing or unsupported: ' + value)
        paths.append(value)
    return paths


def execute(command, cwd, timeout, log):
    with log.open('wb') as stream:
        process = subprocess.Popen(['bash', '-c', command], cwd=cwd, stdin=subprocess.DEVNULL,
                                   stdout=stream, stderr=subprocess.STDOUT, start_new_session=True)
        try:
            status = process.wait(timeout=timeout)
        except subprocess.TimeoutExpired:
            os.killpg(process.pid, signal.SIGKILL)
            process.wait()
            raise Invalid('generator timed out after %ds: %s' % (timeout, command))
    return status


def check(root, declaration='jpdev.json'):
    root = root.resolve()
    generators = config(json.loads(inside(root, declaration).read_text()))
    files = source_files(root)
    base = Path(os.environ.get('TMPDIR', tempfile.gettempdir()))
    need(base.is_dir() and base.resolve() == base, 'scratch TMPDIR must be a real directory')
    with tempfile.TemporaryDirectory(prefix='generated-docs-', dir=base) as temporary:
        temporary = Path(temporary)
        for index, item in enumerate(generators):
            clone = temporary / str(index)
            clone.mkdir()
            for value in files:
                source, target = inside(root, value), clone / value
                target.parent.mkdir(parents=True, exist_ok=True)
                shutil.copy2(source, target)
            for value in item.get('dependencies', []):
                source = root / value
                need(source.is_dir(), 'missing installed dependency: ' + value)
                target = clone / value
                target.parent.mkdir(parents=True, exist_ok=True)
                target.symlink_to(source.resolve(), target_is_directory=True)
            for value in item['outputs']:
                source = inside(root, value)
                need(source.is_file(), 'checked-in output missing: ' + value + '; regenerate: ' + item['run'])
                target = inside(clone, value)
                if target.exists():
                    target.unlink()
            status = execute(item['run'], clone, item.get('timeout', 120), temporary / 'generator.log')
            if status:
                with (temporary / 'generator.log').open('rb') as stream:
                    data = stream.read(8192).decode(errors='replace')
                print(data, file=sys.stderr)
                print('generated-docs: generator %s exited %d; regenerate: %s' %
                      (item['name'], status, item['run']), file=sys.stderr)
                return status if 1 <= status <= 125 else 1
            for value in item['outputs']:
                output = inside(clone, value)
                need(output.is_file(), 'generator output missing: ' + value + '; regenerate: ' + item['run'])
                need(output.read_bytes() == inside(root, value).read_bytes(),
                     'stale ' + value + '; regenerate: ' + item['run'])
            print('generated-docs: %s current (%d files)' % (item['name'], len(item['outputs'])))
    return 0


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', type=Path, default=Path.cwd())
    parser.add_argument('--config', default='jpdev.json')
    args = parser.parse_args()
    try:
        return check(args.root, args.config)
    except (Invalid, OSError, ValueError, subprocess.SubprocessError) as error:
        print('generated-docs: ' + str(error), file=sys.stderr)
        return 1


if __name__ == '__main__':
    sys.exit(main())
