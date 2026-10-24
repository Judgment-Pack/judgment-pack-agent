#!/usr/bin/env python3
"""Offline host-upgrade documentation admission, reused by CI and the release tag gate."""
import argparse
import re
import subprocess
from pathlib import Path

VERSION = re.compile(r"v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)", re.ASCII)
ROOT = Path(__file__).resolve().parent.parent


def local_release_tags(root):
    result = subprocess.run(
        ["git", "for-each-ref", "--format=%(refname:strip=2)", "refs/tags/"],
        cwd=root, text=True, capture_output=True, check=True,
    )
    return [name[3:] if name.startswith("go/") else name for name in result.stdout.splitlines()
            if VERSION.fullmatch(name[3:] if name.startswith("go/") else name)]


def validate(root, tags=(), tag=None):
    """Check the notes inventory plus supplied tag evidence; never infer absent tags."""
    versions = {p.stem for p in (root / "docs/releases").glob("v*.md") if VERSION.fullmatch(p.stem)}
    versions.update(tags)
    if tag is not None:
        versions.add(tag)
    problems = []
    if not versions:
        problems.append("no release notes or release tags supplied")
    for name in sorted(versions):
        if not VERSION.fullmatch(name):
            problems.append(f"invalid release tag: {name}")
            continue
        bodies = {}
        for directory in ("releases", "upgrade"):
            path = root / "docs" / directory / f"{name}.md"
            label = f"docs/{directory}/{name}.md"
            if not path.is_file():
                problems.append(f"{label} is missing")
                continue
            body = path.read_text(encoding="utf-8")
            lines = body.splitlines()
            if not body.strip():
                problems.append(f"{label} is empty")
            elif not lines or re.sub(r"^#+ +", "", lines[0]).rstrip() != name:
                problems.append(f"{label} must name {name} on its first line")
            elif not any(line.strip() and not line.lstrip().startswith("#") for line in lines[1:]):
                problems.append(f"{label} has no guidance beyond headings")
            bodies[directory] = body
        notes = bodies.get("releases", "")
        expected = f"../upgrade/{name}.md"
        links = re.findall(r"\[[^]\n]+\]\(([^)\s]+)\)", notes)
        if expected not in links:
            problems.append(f"docs/releases/{name}.md must link to {expected}")
    return problems


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=ROOT)
    parser.add_argument("--tag", help="actual release tag; validated even outside the notes inventory")
    args = parser.parse_args()
    # Admission has the actual event tag, independently of local ref discovery.
    # Ordinary CI additionally checks every locally available v/go-v tag. A shallow
    # checkout's missing refs are not evidence that historical tags do not exist.
    try:
        tags = [] if args.tag is not None else local_release_tags(args.root)
        problems = validate(args.root, tags, args.tag)
    except (OSError, UnicodeError, subprocess.CalledProcessError) as error:
        parser.exit(1, f"upgrade guides: evidence could not be read: {error}\n")
    if problems:
        parser.exit(1, "\n".join(problems) + "\n")
    print("upgrade guides: notes inventory and supplied tag evidence validated")
    if args.tag is None:
        print("upgrade guides: local refs do not establish complete historical tag coverage; release admission checks --tag")


if __name__ == "__main__":
    main()
