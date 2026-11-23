#!/usr/bin/env python3
"""Hold every standing advisory exception to its conditions at this commit.

A standing exception is one the maintainer recorded by name in
docs/security/dependency-policy.md ("Standing exception: <advisory>"), bound to conditions instead
of to a date. Each holds only while, from go/:

1. no package of its module is in `go list -deps -test ./...`;
2. no package of the agent's module imports one, in its imports, test imports or external test
   imports;
3. govulncheck, under the Go release dev.json pins, reports no finding of the advisory at any level.

Any failed condition fails this check, and so does unavailable evidence: a Go release other than the
pin, a failed or unreadable `go list` or govulncheck run, or a govulncheck run that names no
vulnerability database. The exception then lapses and comes back to the maintainer.
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
# Advisory -> the module the exception keeps out of the build. Each needs its own policy section.
STANDING = {'GO-2026-5781': 'rsc.io/pdf'}
SECTION = re.compile(r'^## Standing exception: (GO-\d{4}-\d+)\b', re.M)


class Unavailable(Exception):
    """The evidence a condition needs could not be had; never a pass."""


def policy_sections(text):
    return SECTION.findall(text)


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


def import_hits(packages, module):
    """(importer, imported) pairs from `go list -json` package records."""
    hits = set()
    for package in packages:
        for field in ('Imports', 'TestImports', 'XTestImports'):
            for imported in package.get(field) or []:
                if in_module(imported, module):
                    hits.add((package['ImportPath'], imported))
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


def vulncheck_hits(text, advisory):
    """Findings of the advisory in govulncheck -format json output, at any level."""
    try:
        messages = stream(text)
    except ValueError as error:
        raise Unavailable('govulncheck output is not JSON messages: %s' % error) from None
    config = [m['config'] for m in messages if isinstance(m, dict) and 'config' in m]
    if len(config) != 1 or not config[0].get('db'):
        raise Unavailable('govulncheck output names no vulnerability database')
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
    named = policy_sections((root / POLICY).read_text())
    problems = ['%s has no standing exception section in %s' % (a, POLICY) for a in sorted(set(STANDING) - set(named))]
    problems += ['%s names a standing exception for %s that this check does not hold' % (POLICY, a)
                 for a in sorted(set(named) - set(STANDING))]
    if problems:
        return problems
    pin = 'go' + json.loads((root / 'dev.json').read_text())['toolchains']['go']['version']
    env = dict(os.environ, GOFLAGS='-mod=readonly -buildvcs=false', GOTOOLCHAIN='local', CGO_ENABLED='1')
    go = root / 'go'
    try:
        version = run(['go', 'env', 'GOVERSION'], go, env).strip()
        if version != pin:
            raise Unavailable('the check runs under %s; dev.json pins %s' % (version, pin))
        closure = run(['go', 'list', '-deps', '-test', './...'], go, env)
        packages = stream(run(['go', 'list', '-test', '-json=ImportPath,Imports,TestImports,XTestImports', './...'], go, env))
        vulns = run(['go', 'run', GOVULNCHECK, '-format', 'json', './...'], go, env)
        for advisory, module in sorted(STANDING.items()):
            problems += ['%s: %s is in the build or test closure' % (advisory, path) for path in closure_hits(closure, module)]
            problems += ['%s: %s imports %s' % (advisory, importer, imported) for importer, imported in import_hits(packages, module)]
            problems += ['%s: govulncheck reports a %s-level finding' % (advisory, level) for level in vulncheck_hits(vulns, advisory)]
    except (Unavailable, ValueError) as error:
        problems.append('unavailable evidence: %s' % error)
    return problems


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
