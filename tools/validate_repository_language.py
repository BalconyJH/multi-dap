"""Validate that relevant repository text is UTF-8 and Latin-script only."""

from __future__ import annotations

import argparse
import subprocess
import sys
import unicodedata
from pathlib import Path
from typing import Iterable


TEXT_EXTENSIONS = frozenset(
    {
        ".go",
        ".h",
        ".hpp",
        ".c",
        ".cc",
        ".cpp",
        ".css",
        ".html",
        ".ini",
        ".js",
        ".jsx",
        ".json",
        ".lock",
        ".md",
        ".mod",
        ".ps1",
        ".py",
        ".sh",
        ".sql",
        ".sum",
        ".toml",
        ".ts",
        ".tsx",
        ".txt",
        ".xml",
        ".yaml",
        ".yml",
    }
)
TEXT_FILENAMES = frozenset(
    {
        "CODEOWNERS",
        "Dockerfile",
        "LICENSE",
        "Makefile",
        ".editorconfig",
        ".gitattributes",
        ".gitignore",
        ".vscodeignore",
    }
)
ALLOWED_CONTROL_CHARACTERS = frozenset({"\t", "\n", "\r"})


def character_violation(character: str) -> str | None:
    """Describe a disallowed repository character, if any."""
    category = unicodedata.category(character)
    if category.startswith("C") and character not in ALLOWED_CONTROL_CHARACTERS:
        return f"disallowed control or format character U+{ord(character):04X}"
    if category.startswith("L") and "LATIN" not in unicodedata.name(character, ""):
        return f"non-Latin letter U+{ord(character):04X}"
    return None


def is_relevant_text_file(relative_path: str) -> bool:
    """Return whether a Git path is in the repository's text policy scope."""
    path = Path(relative_path)
    return path.suffix.lower() in TEXT_EXTENSIONS or path.name in TEXT_FILENAMES


def repository_files(repository: Path) -> list[str]:
    """List tracked plus untracked, non-ignored files from Git."""
    result = subprocess.run(
        ["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard"],
        cwd=repository,
        check=False,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    if result.returncode:
        detail = result.stderr.decode("utf-8", "replace").strip()
        raise ValueError("git ls-files failed" + (": " + detail if detail else ""))
    return [path for path in result.stdout.decode("utf-8", "strict").split("\x00") if path]


def find_violations(repository: Path, paths: Iterable[str] | None = None) -> list[str]:
    """Return policy violations with stable path, line, and column locations."""
    candidates = repository_files(repository) if paths is None else list(paths)
    violations: list[str] = []
    for relative_path in sorted(candidates):
        for column, character in enumerate(relative_path, start=1):
            violation = character_violation(character)
            if violation is not None:
                violations.append(f"{relative_path}:path:{column}: {violation}")
        if not is_relevant_text_file(relative_path):
            continue
        file_path = repository / relative_path
        if not file_path.is_file():
            continue
        data = file_path.read_bytes()
        if b"\x00" in data:
            violations.append(f"{relative_path}: contains a NUL byte")
            continue
        try:
            text = data.decode("utf-8", "strict")
        except UnicodeDecodeError as error:
            violations.append(
                f"{relative_path}:{error.start + 1}: invalid UTF-8 ({error.reason})"
            )
            continue
        for offset, character in enumerate(text):
            violation = character_violation(character)
            if violation is None:
                continue
            line = text.count("\n", 0, offset) + 1
            column = offset - text.rfind("\n", 0, offset)
            violations.append(f"{relative_path}:{line}:{column}: {violation}")
    return violations


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--repository", type=Path, default=Path.cwd(), help="Git repository to inspect"
    )
    arguments = parser.parse_args(argv)
    repository = arguments.repository.resolve()
    try:
        violations = find_violations(repository)
    except (OSError, ValueError) as error:
        print(f"repository language validation failed: {error}", file=sys.stderr)
        return 2
    if violations:
        print("repository language validation failed:", file=sys.stderr)
        print("\n".join(violations), file=sys.stderr)
        return 1
    print("repository language validation passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
