#!/usr/bin/env python3
"""Generate the resolved dependency inventory; metadata refresh uses only local versioned evidence."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]


def digest(data):
    return hashlib.sha256(data).hexdigest()


def stream(data):
    decoder, result, pos = json.JSONDecoder(), [], 0
    while pos < len(data):
        while pos < len(data) and data[pos].isspace():
            pos += 1
        if pos < len(data):
            item, pos = decoder.raw_decode(data, pos)
            result.append(item)
    return result


def run(args, root, env):
    result = subprocess.run(args, cwd=root, env=env, stdin=subprocess.DEVNULL,
                            capture_output=True, text=True, timeout=180)
    if result.returncode:
        raise ValueError('command failed (%d): %s\n%s' % (result.returncode, ' '.join(args), result.stderr[:8192]))
    return result.stdout


def resolve(root, config):
    env = dict(os.environ, GOOS=config['goos'], GOARCH=config['goarch'], CGO_ENABLED=config['cgo'],
               GOFLAGS='-mod=readonly -p=2', GOTOOLCHAIN='local', GOAMD64='v1', GOEXPERIMENT='')
    version = run(['go', 'env', 'GOVERSION'], root / 'go', env).strip()
    if version != config['toolchain']:
        raise ValueError('inventory requires %s, found %s' % (config['toolchain'], version))
    modules = stream(run(['go', 'list', '-m', '-json', 'all'], root / 'go', env))
    packages = stream(run(['go', 'list', '-deps', '-json', '-tags=' + ','.join(config['tags']), './...'], root / 'go', env))
    if any(p.get('Error') or p.get('Incomplete') for p in packages):
        raise ValueError('package closure incomplete')
    return modules, packages


def npm_entries(lock):
    if lock.get('lockfileVersion') != 3 or not isinstance(lock.get('packages'), dict):
        raise ValueError('requires npm lockfileVersion3 packages')
    return {k: v for k, v in lock['packages'].items() if k and 'node_modules/' in k and not v.get('link')}


def npm_name(location):
    return location.rsplit('node_modules/', 1)[1]


def sums(root):
    return {p + '@' + v: s for p, v, s in (line.split() for line in (root / 'go/go.sum').read_text().splitlines())}


def binding(module, checksums):
    actual = module.get('Replace', module)
    version = actual.get('Version')
    if not version:
        # Local replacements cannot borrow versioned upstream metadata.
        return {'path': actual['Path'], 'version': None, 'sum': None}
    return {'path': actual['Path'], 'version': version,
            'sum': checksums.get(actual['Path'] + '@' + version),
            'go_mod_sum': checksums.get(actual['Path'] + '@' + version + '/go.mod')}


def unknown(reason):
    return 'unknown: ' + reason


def license_from_file(path):
    text = path.read_text(errors='replace')
    apache = 'Apache License' in text and 'Version 2.0, January 2004' in text
    mit = 'Permission is hereby granted, free of charge, to any person obtaining a copy' in text
    if apache and mit:
        return unknown('multiple license texts require manual identification')
    if apache:
        return 'Apache-2.0'
    if 'Permission is hereby granted, free of charge, to any person obtaining a copy' in text:
        return 'MIT'
    return unknown('versioned license text requires manual identification')


def refresh(root, modules, lock):
    checksums = sums(root)
    snapshot = {'version': 1, 'go': {}, 'npm': {}}
    for module in modules:
        if module.get('Main'):
            continue
        key = module['Path'] + '@' + module.get('Version', '(local)')
        actual = module.get('Replace', module)
        record = {'binding': binding(module, checksums), 'repository': unknown('no cached versioned origin URL'),
                  'publishing_owner': unknown('Go module metadata does not declare publishing owner'),
                  'license': unknown('no versioned license file available locally'), 'evidence': []}
        directory = Path(actual['Dir']) if actual.get('Dir') and actual.get('Version') else None
        if directory:
            licenses = sorted([p for p in directory.iterdir() if p.is_file() and p.name.upper().startswith(('LICENSE', 'COPYING'))])
            if licenses:
                identified = {license_from_file(path) for path in licenses}
                record['license'] = next(iter(identified)) if len(identified) == 1 else unknown('multiple versioned license files require manual identification')
                record['evidence'] += [{'file': path.name, 'sha256': digest(path.read_bytes())} for path in licenses]
        gomod = actual.get('GoMod')
        if gomod and actual.get('Version'):
            info = Path(gomod).with_suffix('.info')
            if info.exists():
                data = json.loads(info.read_text())
                origin = data.get('Origin', {})
                if origin.get('URL'):
                    record['repository'] = origin['URL']
                    record['repository_owner'] = unknown('origin URL is not an ownership assertion')
                    record['evidence'].append({'file': 'module-version.info', 'sha256': digest(info.read_bytes()), 'origin': origin})
        snapshot['go'][key] = record
    for location, item in npm_entries(lock).items():
        record = {'binding': {'version': item['version'], 'integrity': item.get('integrity')},
                  'license': item.get('license') or unknown('lock entry has no license'),
                  'repository': unknown('versioned package.json unavailable'),
                  'publishing_owner': unknown('versioned package metadata does not declare publishing owner'),
                  'evidence': []}
        path = root / location / 'package.json'
        if path.exists():
            raw = path.read_bytes(); package = json.loads(raw)
            if package.get('name') != npm_name(location) or package.get('version') != item['version']:
                raise ValueError('installed package identity differs from lock: ' + location)
            record['evidence'].append({'file': 'package.json', 'sha256': digest(raw)})
            repository = package.get('repository')
            if isinstance(repository, dict):
                repository = repository.get('url')
            if isinstance(repository, str) and repository:
                record['repository'] = repository
            if package.get('license') and isinstance(package['license'], str):
                if item.get('license') and item['license'] != package['license']:
                    raise ValueError('lock/package license differs: ' + location)
                record['license'] = package['license']
            # author and repository namespace do not identify the publishing account.
            if package.get('maintainers'):
                record['maintainers_declared'] = package['maintainers']
                record['publishing_owner'] = unknown('declared maintainers do not establish publishing ownership')
        snapshot['npm'][location] = record
    (root / 'scripts/dependencies-metadata.json').write_text(json.dumps(snapshot, indent=2, sort_keys=True) + '\n')
    return snapshot


def cell(value):
    if not isinstance(value, str):
        value = json.dumps(value, sort_keys=True, separators=(',', ':'))
    return value.replace('|', '&#124;').replace('\n', ' ')


def table(headers, rows):
    return ['| ' + ' | '.join(headers) + ' |', '| ' + ' | '.join(['---'] * len(headers)) + ' |'] + [
        '| ' + ' | '.join(cell(v) for v in row) + ' |' for row in rows]


def render(root, modules, packages, lock, metadata, config):
    checksums = sums(root)
    selected = {m['Path'] + '@' + m.get('Version', '(local)'): m for m in modules if not m.get('Main')}
    entries = npm_entries(lock)
    if metadata.get('version') != 1 or set(metadata['go']) != set(selected) or set(metadata['npm']) != set(entries):
        raise ValueError('metadata dependency set stale; run python3 scripts/gen-dependencies.py --refresh-metadata')
    closure = {}
    for package in packages:
        module = package.get('Module')
        if module and not module.get('Main'):
            closure.setdefault(module['Path'], []).append(package['ImportPath'])
    go_rows = []
    for key, module in sorted(selected.items()):
        record = metadata['go'][key]
        if record['binding'] != binding(module, checksums):
            raise ValueError('metadata version/sum stale: ' + key)
        membership = closure.get(module['Path'], [])
        go_rows.append([module['Path'], module.get('Version', '(local replacement)'),
                        'indirect' if module.get('Indirect') else 'direct',
                        {k: module['Replace'][k] for k in ('Path', 'Version', 'Sum') if k in module['Replace']} if module.get('Replace') else 'none',
                        ('in configured package closure (%d packages)' % len(membership)) if membership else 'module graph only',
                        record['license'], record['publishing_owner'], record['repository']])
    npm_rows = []
    direct = {}
    for location, value in lock['packages'].items():
        if location == '' or location.startswith('packages/') and 'node_modules/' not in location:
            for field in ('dependencies', 'devDependencies', 'optionalDependencies', 'peerDependencies'):
                for name in value.get(field, {}):
                    direct.setdefault(name, set()).add(field)
    for location, item in sorted(entries.items()):
        record = metadata['npm'][location]
        if record['binding'] != {'version': item['version'], 'integrity': item.get('integrity')}:
            raise ValueError('metadata version/integrity stale: ' + location)
        flags = ['development' if item.get('dev') else 'runtime graph']
        flags += [name for name in ('optional', 'peer', 'devOptional') if item.get(name)]
        declaration = ','.join(sorted(direct.get(npm_name(location), []))) or 'transitive'
        constraints = {k: item[k] for k in ('os', 'cpu', 'libc', 'peerDependencies', 'peerDependenciesMeta') if k in item}
        npm_rows.append([location, item['version'], ', '.join(flags), declaration, constraints,
                         record['license'], record['publishing_owner'], record['repository']])
    inputs = ['go/go.mod', 'go/go.sum', 'package-lock.json', 'scripts/dependencies-config.json', 'scripts/dependencies-metadata.json']
    lines = ['# Dependency inventory', '', 'Generated with `python3 scripts/gen-dependencies.py`. Refresh local metadata explicitly with `--refresh-metadata`, inspect the evidence, then regenerate.', '',
             'Scope: every selected third-party Go module from `go list -m -json all` and every non-link `node_modules/` installation entry in the root npm v3 lockfile. Duplicate locations/versions remain separate. The root project and workspace link records are excluded as first-party identities. npm development, optional and peer entries are included, including other-platform entries.', '',
             'This is an inventory, not vulnerability clearance, a license compliance verdict or an SBOM attestation. Publishing ownership is distinct from authorship, declared maintainers and repository ownership. Unknown values state the evidence gap; no owner is inferred from a package prefix or repository URL.', '',
             '## Configured native package closure', '',
             '`' + json.dumps(config, sort_keys=True) + '`', '',
             'The closure is the union returned by `go list -deps -json -tags= ./...` over production packages, including repository command packages, excluding tests. It records package reachability for this configuration; it does not prove every file or symbol is linked. This published library has no universal host final binary. Other operating systems, architectures, tags, CGO settings and host entry points need a new closure. A module outside this closure remains in the resolved graph and may support another platform or tests.', '',
             '## Frameworks and roles', '', 'The Codex adapter consumes a host-supplied agent capability (`packages/harness/engines/codex/index.ts`); this package does not bundle a separate Codex SDK framework.', '']
    for item in config['roles']:
        lines.append('- `%s`: %s Source: `%s`.' % (item['dependency'], item['role'], item['source']))
    lines += ['', 'Go framework transitive JSON and utility libraries remain individually visible below (including Sonic, json-iterator, jsonschema, ordered-map, UUID and template utilities). Framework roles are source annotations, not package-tag inference.', '', '## Go resolved modules', '']
    lines += table(['Module', 'Selected version', 'Relation', 'Replacement', 'Native membership', 'License', 'Publishing owner', 'Repository'], go_rows)
    lines += ['', '## Exact native third-party package closure', '']
    for module, names in sorted(closure.items()):
        lines += ['- `%s`: %s' % (module, ', '.join('`'+n+'`' for n in sorted(set(names))))]
    lines += ['', '## npm resolved installation entries', '', 'Native membership: npm packages are not part of this Go package closure. Optional platform binaries are lock entries, not evidence of a shipped host binary. Runtime graph flags describe lockfile classification; a consumer resolves its own installation graph. Peer requirements/optional peer metadata and platform restrictions are shown verbatim.', '']
    lines += table(['Installed location', 'Version', 'Lock flags', 'Workspace declaration', 'Platform/peer metadata', 'License', 'Publishing owner', 'Repository'], npm_rows)
    lines += ['', '## Reproducibility inputs', '', 'Metadata records bind exact module/version/sum or npm location/version/integrity; evidence SHA-256 values refer to local versioned package manifests, module origin records or license text. Missing local package metadata is retained as unknown. Refresh uses no registry query. CI regeneration reads the checked-in metadata snapshot and resolves the graph/closure with the configured toolchain.', '']
    lines += ['- `%s`: `%s`' % (p, digest((root / p).read_bytes())) for p in inputs]
    lines += ['', 'Selected third-party modules: %d. npm installation entries: %d.' % (len(selected), len(entries)), '']
    return '\n'.join(lines)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', type=Path, default=ROOT)
    parser.add_argument('--refresh-metadata', action='store_true')
    args = parser.parse_args()
    try:
        root = args.root.resolve()
        config = json.loads((root / 'scripts/dependencies-config.json').read_text())
        lock = json.loads((root / 'package-lock.json').read_text())
        modules, packages = resolve(root, config)
        metadata = refresh(root, modules, lock) if args.refresh_metadata else json.loads((root / 'scripts/dependencies-metadata.json').read_text())
        body = render(root, modules, packages, lock, metadata, config)
        (root / 'docs/dependencies.md').write_text(body)
        print('dependencies: inventory generated')
    except (OSError, ValueError, KeyError, subprocess.SubprocessError) as error:
        parser.exit(1, 'dependencies: ' + str(error) + '\n')


if __name__ == '__main__':
    main()
