#!/usr/bin/env python3
"""Fail when a Japanese docs page's heading anchors drift from the English original.

docs/users/ is built twice (English at the root, Japanese under /ja/) and a
translated heading keeps its English anchor explicitly, e.g.
`## データセットを作る { #create-a-dataset }`, so `foo.md#anchor` links and
deep links survive a language switch. Broken anchors are only INFO-level in
mkdocs, so `mkdocs build --strict` will not catch an English-side rename that
leaves a ja anchor pointing at a slug that no longer exists -- this script
does. Run it with `make check-doc-anchors` (also part of `make check`).

For every docs/users/*.md page that has a *.ja.md translation, the English
headings' effective anchors (the explicit `{ #... }` when present, else the
toc slug) must equal the Japanese headings' explicit anchors, in order. A
page with no translation is skipped: it falls back to English by design.
Standard library only.
"""

from __future__ import annotations

import pathlib
import re

DOCS = pathlib.Path(__file__).resolve().parents[1] / "docs" / "users"
HEADING = re.compile(r"^(#{2,4})\s+(.*?)\s*$")
ANCHOR = re.compile(r"\{\s*#([A-Za-z0-9_-]+)\s*\}")
FENCE = re.compile(r"```.*?```", re.DOTALL)


def slugify(text: str) -> str:
    """Python-Markdown toc's default slugify for the headings used here."""
    text = re.sub(r"<[^>]*>", "", text)
    text = re.sub(r"`([^`]*)`", r"\1", text)
    text = re.sub(r"[^\w\s-]", "", text, flags=re.UNICODE).strip().lower()
    return re.sub(r"[-\s]+", "-", text)


def en_anchors(src: str) -> list[str]:
    """Effective anchor of every ##-#### heading: explicit, else the slug."""
    out = []
    for match in HEADING.finditer(FENCE.sub("", src)):
        found = ANCHOR.search(match.group(2))
        out.append(found.group(1) if found else slugify(match.group(2)))
    return out


def ja_headings(src: str) -> tuple[list[str], list[str]]:
    """(explicit anchors, heading texts missing one) for every ##-#### heading."""
    anchors = []
    missing = []
    for match in HEADING.finditer(FENCE.sub("", src)):
        found = ANCHOR.search(match.group(2))
        if found:
            anchors.append(found.group(1))
        else:
            missing.append(match.group(0).strip())
    return anchors, missing


def main() -> int:
    failures = 0
    for en_path in sorted(DOCS.rglob("*.md")):
        if ".ja." in en_path.name:
            continue
        ja_path = en_path.with_name(en_path.stem + ".ja.md")
        if not ja_path.exists():
            continue
        want = en_anchors(en_path.read_text(encoding="utf-8"))
        got, missing = ja_headings(ja_path.read_text(encoding="utf-8"))
        if missing:
            failures += 1
            print(
                f"{ja_path.name}: {len(missing)} heading(s) without an explicit anchor:"
            )
            for heading in missing:
                print(f"  {heading}")
        if got != want:
            failures += 1
            print(f"{ja_path.name}: anchors do not match {en_path.name}:")
            for i, (a, b) in enumerate(zip(got, want)):
                if a != b:
                    print(f"  heading {i + 1}: ja has {a!r}, en expects {b!r}")
            if len(got) != len(want):
                print(f"  anchor count: ja has {len(got)}, en has {len(want)}")
    if failures:
        print(
            "fix the ja anchors (or the en headings) and re-run make check-doc-anchors"
        )
        return 1
    print(
        f"checked {(len(list(DOCS.rglob('*.ja.md'))))} translated pages: anchors in sync"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
