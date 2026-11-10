#!/usr/bin/env python3
"""Compare the tools a CI job's evidence recorded with its profile's pins (#338).

    python3 scripts/recorded-tools.py --workflow ci --job check --evidence evidence.json

The release gate (scripts/dev/release_evidence.py, the devkit's, vendored unchanged) holds every
tool a job records to its profile's pin, but only at the release, in the `CI at the tag` job. A pin
that no longer matches what the job's own setup installs (the npm a Node release bundles, the pip a
Python release bundles, a package name as the tool's command spells it) would pass main CI and refuse
there, after the tag. Main CI runs this after each capture, on the record the capture wrote, so the
difference fails main instead.

Exit 0 when the record holds exactly the profile's tools, each at its pin; 1 otherwise, or when the
evidence or the configuration cannot be read. It reads no environment variable and runs no tool:
the record is the capture's.
"""
import argparse
import json
from pathlib import Path
import sys

FIX = ('A re-run installs the same tool again: fix the pin, or the toolchain the job sets up, on main, '
       'and release a new version (docs/ci-evidence.md#the-fresh-route).')


def differences(config, workflow, job, record):
    """What differs between the record's tools and the job's profile: [] when nothing does."""
    spec = config['workflows'][workflow]['jobs'][job]
    profile = config['profiles'][spec['profile']]
    if (record.get('workflow'), record.get('job')) != (workflow, job):
        return ['the evidence is %s/%s, not %s/%s' % (record.get('workflow'), record.get('job'), workflow, job)]
    tools = record['runtime']['tools']
    if not isinstance(tools, dict) or not all(isinstance(value, str) for value in tools.values()):
        raise TypeError("the record's tools are not a map of names to text")
    found = []
    if set(tools) != set(profile['tools']):
        found.append('records the tools %s, profile %s records %s' % (
            sorted(tools), spec['profile'], sorted(profile['tools'])))
    for tool, pin in sorted(profile['pins'].items()):
        if tool in tools and tools[tool] != pin:
            found.append('%s is %r, pinned %r' % (tool, tools[tool], pin))
    return found


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument('--config', default='.github/ci-evidence.json')
    parser.add_argument('--workflow', required=True)
    parser.add_argument('--job', required=True)
    parser.add_argument('--evidence', required=True)
    args = parser.parse_args(argv)
    try:
        config = json.loads(Path(args.config).read_text(encoding='utf-8'))
        record = json.loads(Path(args.evidence).read_text(encoding='utf-8'))
        found = differences(config, args.workflow, args.job, record)
    except (OSError, ValueError, KeyError, TypeError, AttributeError) as error:
        print('::error::recorded-tools: %s: could not compare the recorded tools: %s: %s' % (
            args.job, type(error).__name__, error))
        return 1
    if found:
        for line in found:
            print('::error::recorded-tools: %s: %s. %s' % (args.job, line, FIX))
        return 1
    print('recorded-tools: %s: %d tools at their pins' % (args.job, len(record['runtime']['tools'])))
    return 0


if __name__ == '__main__':
    sys.exit(main())
