"""The Go module's release holds (agent#63).

Go resolves github.com/Judgment-Pack/judgment-pack-agent/go by a go/<version> tag, which the release
workflow makes (docs/releasing.md). A reader of a release's notes has to be told that the module is
part of the release, so every notes file from v0.3.0 on names it.
"""

import re
import importlib.util
import os
import shutil
import subprocess
import tempfile
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


SPEC = importlib.util.spec_from_file_location("upgrade_guides", ROOT / "scripts/check-upgrade-guides.py")
UPGRADE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(UPGRADE)


class HostUpgradeGuideTests(unittest.TestCase):
    def fixture(self, root, tag="v1.2.3"):
        notes = root / "docs/releases" / f"{tag}.md"
        guide = root / "docs/upgrade" / f"{tag}.md"
        notes.parent.mkdir(parents=True, exist_ok=True)
        guide.parent.mkdir(parents=True, exist_ok=True)
        notes.write_text(f"# {tag}\n\nRelease scope.\n\n[host upgrade guide](../upgrade/{tag}.md)\n")
        if tuple(map(int, tag[1:].split('.'))) >= (0, 11, 0):
            notes.write_text(notes.read_text() + f"\n[dependency inventory](https://github.com/Judgment-Pack/judgment-pack-agent/blob/{tag}/docs/dependencies.md)\n")
            (root / "docs/dependencies.md").write_text("# Dependency inventory\n\nFixture dependencies.\n")
        guide.write_text(f"# {tag}\n\n## Host changes\n\nReplace the retired input with its canonical form.\n")
        return notes, guide

    def test_all_notes_and_locally_available_release_tags_have_matching_guides(self):
        # Local refs are additional evidence, never a claim that shallow refs are complete.
        self.assertEqual(UPGRADE.validate(ROOT, UPGRADE.local_release_tags(ROOT)), [])
        self.assertEqual(UPGRADE.validate(ROOT, tag="v0.10.0"), [])

    def test_missing_empty_heading_only_mismatched_guide_and_wrong_link_fail(self):
        with tempfile.TemporaryDirectory(prefix="upgrade-guide-fixture-") as directory:
            root = Path(directory)
            notes, guide = self.fixture(root)
            self.assertEqual(UPGRADE.validate(root), [])
            good_notes, good_guide = notes.read_text(), guide.read_text()
            for label, body in [("missing", None), ("empty", ""), ("whitespace", " \n"),
                                ("headings", "# v1.2.3\n\n## Host changes\n"),
                                ("mismatch", "# v2.0.0\n\nSome migration.\n")]:
                with self.subTest(fault=label):
                    guide.unlink(missing_ok=True)
                    if body is not None:
                        guide.write_text(body)
                    self.assertTrue(UPGRADE.validate(root))
            guide.write_text(good_guide)
            for body in [good_notes.replace("../upgrade/v1.2.3.md", "../upgrade/v1.2.4.md"),
                         "# v1.2.3\n\nNo host guide link.\n"]:
                notes.write_text(body)
                self.assertTrue(UPGRADE.validate(root))
            notes.write_text(good_notes)
            self.assertEqual(UPGRADE.validate(root), [])

    def test_dependency_inventory_link_uses_actual_tag_and_preserves_historical_notes(self):
        with tempfile.TemporaryDirectory(prefix="inventory-link-fixture-") as directory:
            root = Path(directory)
            notes, _ = self.fixture(root, "v0.11.0")
            self.assertEqual(UPGRADE.validate(root, tag="v0.11.0"), [])
            good = notes.read_text()
            for target in ("main", "v0.10.0"):
                notes.write_text(good.replace("blob/v0.11.0/", f"blob/{target}/"))
                self.assertTrue(any("must link to https://" in p for p in UPGRADE.validate(root, tag="v0.11.0")))
            notes.write_text(good)
            (root / "docs/dependencies.md").unlink()
            self.assertIn("docs/dependencies.md is missing", UPGRADE.validate(root, tag="v0.11.0"))
        with tempfile.TemporaryDirectory(prefix="inventory-historical-fixture-") as directory:
            root = Path(directory)
            self.fixture(root, "v0.10.0")
            self.assertEqual(UPGRADE.validate(root, tag="v0.10.0"), [])

    def test_tag_outside_notes_inventory_is_checked_without_ref_discovery(self):
        with tempfile.TemporaryDirectory(prefix="upgrade-tag-fixture-") as directory:
            root = Path(directory)
            self.fixture(root)
            problems = UPGRADE.validate(root, tag="v9.8.7")
            self.assertIn("docs/releases/v9.8.7.md is missing", problems)
            self.assertIn("docs/upgrade/v9.8.7.md is missing", problems)
            self.assertTrue(UPGRADE.validate(root, tags=["v9.8.7"]))
            for invalid in ["../v1.2.3", "v01.2.3", "v1.2.3-rc1", "v1.2.3\n"]:
                self.assertTrue(UPGRADE.validate(root, tag=invalid))

    def test_actual_release_admission_command_refuses_missing_guidance(self):
        workflow = (ROOT / ".github/workflows/release.yml").read_text()
        admit = workflow.split("  verify:", 1)[0]
        match = re.search(r"      - name: Require matching host upgrade guidance\n        shell: bash\n        run: ([^\n]+)", admit)
        self.assertIsNotNone(match)
        command = match.group(1)
        self.assertEqual(command, 'python3 scripts/check-upgrade-guides.py --tag "${RELEASE_TAG}"')
        with tempfile.TemporaryDirectory(prefix="upgrade-admission-fixture-") as directory:
            root = Path(directory)
            notes, guide = self.fixture(root)
            (root / "scripts").mkdir()
            shutil.copyfile(ROOT / "scripts/check-upgrade-guides.py", root / "scripts/check-upgrade-guides.py")
            env = {**os.environ, "RELEASE_TAG": "v1.2.3", "PYTHONDONTWRITEBYTECODE": "1"}
            def run():
                return subprocess.run(["bash", "-eu", "-c", command], cwd=root, env=env,
                                      capture_output=True, text=True, timeout=20)
            result = run()
            self.assertEqual(result.returncode, 0, result.stderr)
            guide.unlink()
            result = run()
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("docs/upgrade/v1.2.3.md is missing", result.stderr)
            # No synthetic Git refs or notes for this actual event tag: still refuse.
            env["RELEASE_TAG"] = "v9.8.7"
            result = run()
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("docs/upgrade/v9.8.7.md is missing", result.stderr)

    def test_guide_and_pattern_local_file_links_resolve(self):
        documents = list((ROOT / "docs/upgrade").glob("*.md"))
        documents += [ROOT / "docs/defensive-patterns.md", ROOT / "docs/postmortem/README.md",
                      ROOT / "docs/postmortem/0001-reasoning-omission.md"]
        for path in documents:
            for target in re.findall(r"\[[^]\n]+\]\(([^)\s]+)\)", path.read_text()):
                if "://" not in target and not target.startswith("#"):
                    with self.subTest(document=path.name, target=target):
                        self.assertTrue((path.parent / target.split("#", 1)[0]).is_file())


if __name__ == "__main__":
    unittest.main()
