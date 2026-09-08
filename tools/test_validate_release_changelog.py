"""Tests for release CHANGELOG.md validation."""

from __future__ import annotations

import tempfile
import unittest
from pathlib import Path

from tools.validate_release_changelog import validate


class ReleaseChangelogTests(unittest.TestCase):
    def validate_text(self, version: str, text: str) -> list[str]:
        with tempfile.TemporaryDirectory() as temporary:
            changelog = Path(temporary) / "CHANGELOG.md"
            changelog.write_text(text, encoding="utf-8")
            return validate(version, changelog)

    def test_rejects_non_semver_version(self) -> None:
        self.assertEqual(
            self.validate_text("v1.2.3", "## Unreleased\n"),
            ["version is not exact SemVer: v1.2.3"],
        )

    def test_rejects_semver_prerelease_with_leading_zero_numeric_identifier(self) -> None:
        self.assertEqual(
            self.validate_text("1.2.3-rc.01", "## Unreleased\n"),
            ["version is not exact SemVer: 1.2.3-rc.01"],
        )

    def test_prerelease_accepts_single_unreleased_heading(self) -> None:
        self.assertEqual(self.validate_text("1.2.3-rc.1", "## Unreleased\n"), [])

    def test_prerelease_requires_unambiguous_unreleased_without_version_entry(self) -> None:
        self.assertEqual(
            self.validate_text("1.2.3-rc.1", "## Unreleased\n## Unreleased\n"),
            ["prerelease changelog must contain exactly one ## Unreleased heading"],
        )

    def test_stable_accepts_version_heading_date_and_link(self) -> None:
        text = "## [1.2.3] - 2026-09-08\n\n[1.2.3]: https://example.invalid/1.2.3\n"
        self.assertEqual(self.validate_text("1.2.3", text), [])

    def test_accepts_semver_build_metadata_when_exact_entry_exists(self) -> None:
        text = "## [1.2.3+build.7]\n[1.2.3+build.7]: https://example.invalid\n"
        self.assertEqual(self.validate_text("1.2.3+build.7", text), [])

    def test_stable_rejects_missing_link(self) -> None:
        self.assertEqual(
            self.validate_text("1.2.3", "## [1.2.3]\n"),
            ["changelog must contain exactly one [1.2.3]: link definition"],
        )

    def test_stable_rejects_duplicate_heading(self) -> None:
        text = "## [1.2.3]\n## [1.2.3] - 2026-09-08\n[1.2.3]: https://example.invalid\n"
        self.assertEqual(
            self.validate_text("1.2.3", text),
            ["changelog must contain exactly one ## [1.2.3] heading"],
        )

    def test_stable_rejects_duplicate_link(self) -> None:
        text = "## [1.2.3]\n[1.2.3]: https://one.invalid\n[1.2.3]: https://two.invalid\n"
        self.assertEqual(
            self.validate_text("1.2.3", text),
            ["changelog must contain exactly one [1.2.3]: link definition"],
        )

    def test_stable_rejects_heading_with_non_date_suffix(self) -> None:
        text = "## [1.2.3] - candidate\n[1.2.3]: https://example.invalid\n"
        self.assertEqual(
            self.validate_text("1.2.3", text),
            ["changelog must contain exactly one ## [1.2.3] heading"],
        )

    def test_prerelease_accepts_exact_version_entry(self) -> None:
        text = "## [1.2.3-rc.1]\n[1.2.3-rc.1]: https://example.invalid\n"
        self.assertEqual(self.validate_text("1.2.3-rc.1", text), [])

    def test_stable_does_not_accept_unreleased_only(self) -> None:
        self.assertEqual(
            self.validate_text("1.2.3", "## Unreleased\n"),
            [
                "changelog must contain exactly one ## [1.2.3] heading",
                "changelog must contain exactly one [1.2.3]: link definition",
            ],
        )


if __name__ == "__main__":
    unittest.main()
