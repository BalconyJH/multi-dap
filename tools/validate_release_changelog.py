"""Validate release-specific CHANGELOG.md policy."""

from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path


SEMVER = re.compile(
    r"^(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)"
    r"(?:-(?:0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*)"
    r"(?:\.(?:0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*))*)?"
    r"(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$"
)


def validate(version: str, changelog: Path) -> list[str]:
    """Return release-policy errors for one exact SemVer version."""
    if not SEMVER.fullmatch(version):
        return [f"version is not exact SemVer: {version}"]
    try:
        text = changelog.read_text(encoding="utf-8", errors="strict")
    except (OSError, UnicodeError) as error:
        return [f"cannot read changelog as UTF-8: {error}"]

    lines = text.splitlines()
    escaped_version = re.escape(version)
    heading = re.compile(rf"^## \[{escaped_version}\](?:\s+-\s+\d{{4}}-\d{{2}}-\d{{2}})?\s*$")
    link = re.compile(rf"^\[{escaped_version}\]:\s+\S+\s*$")
    heading_count = sum(bool(heading.fullmatch(line)) for line in lines)
    link_count = sum(bool(link.fullmatch(line)) for line in lines)

    is_prerelease = "-" in version.split("+", 1)[0]
    if is_prerelease:
        unreleased_count = sum(line == "## Unreleased" for line in lines)
        if heading_count == 0 and unreleased_count == 1:
            return []
        if heading_count == 0 and unreleased_count != 1:
            return ["prerelease changelog must contain exactly one ## Unreleased heading"]
    errors: list[str] = []
    if heading_count != 1:
        errors.append(f"changelog must contain exactly one ## [{version}] heading")
    if link_count != 1:
        errors.append(f"changelog must contain exactly one [{version}]: link definition")
    return errors


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--version", required=True, help="release version in exact SemVer")
    parser.add_argument("--changelog", type=Path, default=Path("CHANGELOG.md"))
    arguments = parser.parse_args(argv)
    errors = validate(arguments.version, arguments.changelog)
    if errors:
        print("release changelog validation failed:", file=sys.stderr)
        print("\n".join(errors), file=sys.stderr)
        return 1
    print("release changelog validation passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
