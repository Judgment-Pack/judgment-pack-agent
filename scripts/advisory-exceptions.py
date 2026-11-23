#!/usr/bin/env python3
"""Hold every standing advisory exception to its conditions at this commit.

A standing exception is one the maintainer recorded by name in
docs/security/dependency-policy.md ("Standing exception: <advisory>"), with its approval link and
its three numbered conditions, bound to conditions instead of to a date. Each holds only while,
from go/, for every target in TARGETS:

1. no package of its module is in `go list -deps -test ./...`;
2. no package of the agent's module imports one, in its imports, test imports or external test
   imports, nor in a file the target's build leaves out (IgnoredGoFiles: other systems, build tags);
3. govulncheck (-test), under the Go release dev.json pins, reports no finding of the advisory at
   any level.

Any failed condition fails this check, and so does unavailable evidence: a Go release other than the
pin, a failed or unreadable `go list` or govulncheck run, a govulncheck run that names no
vulnerability database, its update time, or the pinned Go. The exception then lapses and comes back
to the maintainer.
"""
import json
import os
from pathlib import Path
import re
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
POLICY = 'docs/security/dependency-policy.md'
GOVULNCHECK = 'golang.org/x/vuln/cmd/govulncheck@v1.1.4'
# The release publishes the Go module for hosts to build, and builds no binary of its own, so the
# closure is held on every first-class target a host may build it for.
TARGETS = [(goos, goarch) for goos in ('linux', 'darwin', 'windows') for goarch in ('amd64', 'arm64')]
# Advisory -> the module the exception keeps out of the build, and the maintainer's approval.
STANDING = {'GO-2026-5781': {'module': 'rsc.io/pdf',
                             'approval': 'https://github.com/Judgment-Pack/judgment-pack-agent/issues/192'}}
SECTION = re.compile(r'^## Standing exception: (GO-\d{4}-\d+)\b', re.M)
PACKAGE_FIELDS = 'ImportPath,Dir,Imports,TestImports,XTestImports,IgnoredGoFiles'


class Unavailable(Exception):
    """The evidence a condition needs could not be had; never a pass."""


def policy_sections(text):
    """Advisory -> its section's text, for each `## Standing exception:` heading."""
    found = {}
    matches = list(SECTION.finditer(text))
    for index, match in enumerate(matches):
        end = matches[index + 1].start() if index + 1 < len(matches) else len(text)
        following = re.search(r'^## ', text[match.end():end], re.M)
        found[match.group(1)] = text[match.start():match.end() + following.start() if following else end]
    return found


def section_problems(advisory, section, approval):
    problems = []
    if approval not in section:
        problems.append('%s: its policy section does not link its approval (%s)' % (advisory, approval))
    for number in (1, 2, 3):
        if not re.search(r'^%d\. \S' % number, section, re.M):
            problems.append('%s: its policy section does not state condition %d' % (advisory, number))
    return problems


def in_module(path, module):
    return path == module or path.startswith(module + '/')


def closure_hits(listing, module):
    """Packages of the module in `go list -deps -test` output (one path per line, with test variants)."""
    hits = set()
    for line in listing.splitlines():
        path = line.strip().split(' ', 1)[0]
        if path.endswith('.test'):
            path = path[:-len('.test')]
        if in_module(path, module):
            hits.add(path)
    return sorted(hits)


TOKEN = re.compile(r'''\s+|//[^\n]*|/\*.*?\*/|"(?:[^"\\\n]|\\.)*"|`[^`]*`|[A-Za-z_][A-Za-z0-9_]*|.''', re.S)


def go_file_imports(text):
    """The import paths of a Go file's import declarations, read from its header only."""
    tokens = [t for t in TOKEN.findall(text) if not t.isspace() and not t.startswith(('//', '/*'))]
    if len(tokens) < 2 or tokens[0] != 'package':
        raise ValueError('not a Go file: no package clause')
    paths, index = [], 2
    while index < len(tokens) and tokens[index] in (';',):
        index += 1
    while index < len(tokens) and tokens[index] == 'import':
        index += 1
        group = index < len(tokens) and tokens[index] == '('
        if group:
            index += 1
        while index < len(tokens):
            token = tokens[index]
            if group and token == ')':
                index += 1
                break
            if token[:1] in '"`':
                paths.append(json.loads(token) if token[0] == '"' else token[1:-1])
                index += 1
                if not group:
                    break
            else:
                index += 1  # an alias, ".", "_" or ";"
        while index < len(tokens) and tokens[index] == ';':
            index += 1
    return paths


def import_hits(packages, module, read=lambda path: Path(path).read_text(encoding='utf-8')):
    """(importer, imported) pairs from `go list -json` package records, ignored files included."""
    hits = set()
    for package in packages:
        for field in ('Imports', 'TestImports', 'XTestImports'):
            for imported in package.get(field) or []:
                if in_module(imported, module):
                    hits.add((package['ImportPath'], imported))
        for name in package.get('IgnoredGoFiles') or []:
            if not name.endswith('.go'):
                continue
            path = os.path.join(package.get('Dir') or '', name)
            try:
                imported = go_file_imports(read(path))
            except (OSError, UnicodeDecodeError, ValueError) as error:
                raise Unavailable('cannot read the imports of %s: %s' % (path, error)) from None
            for each in imported:
                if in_module(each, module):
                    hits.add((package['ImportPath'] + ' (' + name + ')', each))
    return sorted(hits)


def stream(text):
    decoder, index, items = json.JSONDecoder(), 0, []
    while True:
        while index < len(text) and text[index].isspace():
            index += 1
        if index >= len(text):
            return items
        item, index = decoder.raw_decode(text, index)
        items.append(item)


def vulncheck_hits(text, advisory, pin):
    """Findings of the advisory in govulncheck -format json output, at any level."""
    try:
        messages = stream(text)
    except ValueError as error:
        raise Unavailable('govulncheck output is not JSON messages: %s' % error) from None
    config = [m['config'] for m in messages if isinstance(m, dict) and 'config' in m]
    if len(config) != 1 or not config[0].get('db'):
        raise Unavailable('govulncheck output names no vulnerability database')
    if not config[0].get('db_last_modified'):
        raise Unavailable('govulncheck output names no database update time')
    if config[0].get('go_version') != pin:
        raise Unavailable('govulncheck ran under %s; dev.json pins %s' % (config[0].get('go_version'), pin))
    levels = []
    for message in messages:
        finding = message.get('finding') if isinstance(message, dict) else None
        if finding and finding.get('osv') == advisory:
            frame = (finding.get('trace') or [{}])[0]
            levels.append('symbol' if frame.get('function') else 'package' if frame.get('package') else 'module')
    return sorted(set(levels))


def run(args, cwd, env):
    try:
        result = subprocess.run(args, cwd=cwd, env=env, stdin=subprocess.DEVNULL, capture_output=True,
                                text=True, timeout=1500)
    except (OSError, subprocess.SubprocessError) as error:
        raise Unavailable('%s could not run: %s' % (' '.join(args[:3]), error)) from None
    if result.returncode != 0:
        detail = (result.stderr.strip().splitlines() or ['no detail'])[-1]
        raise Unavailable('%s exited %d: %s' % (' '.join(args[:3]), result.returncode, detail))
    return result.stdout


def check(root):
    """Problems, one line each; none means every standing exception holds."""
    sections = policy_sections((root / POLICY).read_text())
    problems = ['%s has no standing exception section in %s' % (a, POLICY) for a in sorted(set(STANDING) - set(sections))]
    problems += ['%s names a standing exception for %s that this check does not hold' % (POLICY, a)
                 for a in sorted(set(sections) - set(STANDING))]
    for advisory in sorted(set(STANDING) & set(sections)):
        problems += section_problems(advisory, sections[advisory], STANDING[advisory]['approval'])
    if problems:
        return problems
    pin = 'go' + json.loads((root / 'dev.json').read_text())['toolchains']['go']['version']
    base = dict(os.environ, GOFLAGS='-mod=readonly -buildvcs=false', GOTOOLCHAIN='local', CGO_ENABLED='1')
    go = root / 'go'
    try:
        version = run(['go', 'env', 'GOVERSION'], go, base).strip()
        if version != pin:
            raise Unavailable('the check runs under %s; dev.json pins %s' % (version, pin))
        for goos, goarch in TARGETS:
            env = dict(base, GOOS=goos, GOARCH=goarch)
            target = goos + '/' + goarch
            closure = run(['go', 'list', '-deps', '-test', './...'], go, env)
            try:
                packages = stream(run(['go', 'list', '-test', '-json=' + PACKAGE_FIELDS, './...'], go, env))
            except ValueError as error:
                raise Unavailable('go list -json output for %s is not JSON: %s' % (target, error)) from None
            if not all(isinstance(p, dict) and isinstance(p.get('ImportPath'), str) for p in packages):
                raise Unavailable('go list -json output for %s holds a record that is not a package' % target)
            for advisory, entry in sorted(STANDING.items()):
                problems += ['%s: %s is in the %s build or test closure' % (advisory, path, target)
                             for path in closure_hits(closure, entry['module'])]
                problems += ['%s: %s imports %s (%s)' % (advisory, importer, imported, target)
                             for importer, imported in import_hits(packages, entry['module'])]
        vulns = run(['go', 'run', GOVULNCHECK, '-test', '-format', 'json', './...'], go, base)
        for advisory in sorted(STANDING):
            problems += ['%s: govulncheck reports a %s-level finding' % (advisory, level)
                         for level in vulncheck_hits(vulns, advisory, pin)]
    except Unavailable as error:
        problems.append('unavailable evidence: %s' % error)
    return sorted(set(problems), key=problems.index)


def main():
    problems = check(ROOT)
    for problem in problems:
        print('advisory exceptions: ' + problem, file=sys.stderr)
    if problems:
        print('advisory exceptions: refused; a standing exception lapses and comes back to the maintainer (%s)' % POLICY)
        return 1
    print('advisory exceptions: standing exceptions hold: %s' % ', '.join(sorted(STANDING)))
    return 0


if __name__ == '__main__':
    sys.exit(main())
