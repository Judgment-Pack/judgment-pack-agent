"""The Go module's release holds (agent#63).

Go resolves github.com/Judgment-Pack/judgment-pack-agent/go by a go/<version> tag, which the release
workflow makes (docs/releasing.md). A reader of a release's notes has to be told that the module is
part of the release, so every notes file from v0.3.0 on names it.
"""

import re
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
RELEASES = ROOT / "docs" / "releases"
MODULE = "github.com/Judgment-Pack/judgment-pack-agent/go"
FIRST_WITH_GO_MODULE = (0, 3, 0)


def version(path):
    match = re.fullmatch(r"v(\d+)\.(\d+)\.(\d+)\.md", path.name)
    return tuple(int(part) for part in match.groups()) if match else None


class GoModuleReleaseTests(unittest.TestCase):
    def test_the_modules_path_is_the_one_the_notes_and_the_ci_check_name(self):
        declared = re.search(r"^module (\S+)$", (ROOT / "go" / "go.mod").read_text(), re.MULTILINE)
        self.assertIsNotNone(declared)
        self.assertEqual(declared.group(1), MODULE)

    def test_the_release_notes_from_v0_3_0_on_name_the_go_module(self):
        notes = [path for path in RELEASES.glob("v*.md") if version(path) is not None and version(path) >= FIRST_WITH_GO_MODULE]
        self.assertIn(RELEASES / "v0.3.0.md", notes)
        for path in sorted(notes):
            with self.subTest(notes=path.name):
                self.assertIn(MODULE, path.read_text(), f"{path.name} must name the Go module {MODULE} (docs/releasing.md)")

    def test_the_release_workflow_triggers_on_release_tags_alone_and_tags_the_go_module(self):
        workflow = (ROOT / ".github" / "workflows" / "release.yml").read_text()
        trigger = re.search(r"^on:\n  push:\n    tags:\n((?:      .*\n)+)", workflow, re.MULTILINE)
        self.assertIsNotNone(trigger)
        patterns = [line.strip() for line in trigger.group(1).splitlines() if line.strip().startswith("- ")]
        self.assertEqual(patterns, ['- "v[0-9]+.[0-9]+.[0-9]+"'])
        self.assertIn('go_tag="go/${RELEASE_TAG}"', workflow)


if __name__ == "__main__":
    unittest.main()
