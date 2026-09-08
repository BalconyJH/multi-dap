"""Tests for the repository language policy validator."""

from __future__ import annotations

import tempfile
import unittest
from pathlib import Path

from tools.validate_repository_language import find_violations, is_relevant_text_file


class RepositoryLanguageTests(unittest.TestCase):
    def write(self, directory: Path, name: str, content: bytes) -> None:
        path = directory / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(content)

    def test_relevant_text_file_selection(self) -> None:
        self.assertTrue(is_relevant_text_file("docs/readme.md"))
        self.assertTrue(is_relevant_text_file("go.mod"))
        self.assertTrue(is_relevant_text_file("go.sum"))
        self.assertTrue(is_relevant_text_file(".github/CODEOWNERS"))
        self.assertTrue(is_relevant_text_file("editors/vscode/.vscodeignore"))
        self.assertTrue(is_relevant_text_file("LICENSE"))
        self.assertFalse(is_relevant_text_file("assets/example.png"))
        self.assertFalse(is_relevant_text_file("bin/multi-dap.exe"))

    def test_accepts_ascii_and_latin_script_text(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            self.write(directory, "README.md", "caf\u00e9 \u00c5ngstr\u00f6m\n".encode("utf-8"))
            self.assertEqual(find_violations(directory, ["README.md"]), [])

    def test_rejects_non_latin_letters_with_location(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            self.write(directory, "README.md", "ok\n\u0410\n".encode("utf-8"))
            self.assertEqual(
                find_violations(directory, ["README.md"]),
                ["README.md:2:1: non-Latin letter U+0410"],
            )

    def test_rejects_letters_from_multiple_non_latin_scripts(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            self.write(directory, "README.md", "\u0391 \u0410\n".encode("utf-8"))
            self.assertEqual(
                find_violations(directory, ["README.md"]),
                [
                    "README.md:1:1: non-Latin letter U+0391",
                    "README.md:1:3: non-Latin letter U+0410",
                ],
            )

    def test_rejects_bidirectional_override(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            self.write(directory, "README.md", "safe\u202etext\n".encode("utf-8"))
            self.assertEqual(
                find_violations(directory, ["README.md"]),
                ["README.md:1:5: disallowed control or format character U+202E"],
            )

    def test_rejects_directional_isolates(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            self.write(directory, "README.md", "\u2066safe\u2069\n".encode("utf-8"))
            self.assertEqual(
                find_violations(directory, ["README.md"]),
                [
                    "README.md:1:1: disallowed control or format character U+2066",
                    "README.md:1:6: disallowed control or format character U+2069",
                ],
            )

    def test_accepts_tab_carriage_return_and_line_feed(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            self.write(directory, "README.md", b"alpha\tbeta\r\n")
            self.assertEqual(find_violations(directory, ["README.md"]), [])

    def test_rejects_nul_before_decoding(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            self.write(directory, "README.md", b"safe\x00text")
            self.assertEqual(
                find_violations(directory, ["README.md"]), ["README.md: contains a NUL byte"]
            )

    def test_rejects_invalid_utf8(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            self.write(directory, "README.md", b"\xff")
            violations = find_violations(directory, ["README.md"])
            self.assertEqual(len(violations), 1)
            self.assertTrue(violations[0].startswith("README.md:1: invalid UTF-8"))

    def test_ignores_non_text_files(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            self.write(directory, "assets/example.bin", "\u0410".encode("utf-8"))
            self.assertEqual(find_violations(directory, ["assets/example.bin"]), [])

    def test_rejects_non_latin_letters_in_binary_path(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            name = "assets/\u0410.bin"
            self.write(directory, name, b"binary")
            self.assertEqual(
                find_violations(directory, [name]),
                [f"{name}:path:8: non-Latin letter U+0410"],
            )


if __name__ == "__main__":
    unittest.main()
