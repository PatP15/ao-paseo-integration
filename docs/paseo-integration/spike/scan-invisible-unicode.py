#!/usr/bin/env python3
"""Scan a source tree for invisible / bidi / tag codepoints used to smuggle
instructions past human review into an LLM's context.

Reproduces the AO x Paseo supply-chain audit finding recorded in
docs/paseo-integration/VULNERABILITIES.md. Read-only.

Usage:
    ./scan-invisible-unicode.py <tree> [<tree> ...]

Exit status: 0 if every tree is clean, 1 if any hit was found.

The codepoint of record is the U+E0000-E007F TAG block: it is invisible in
every editor and terminal, survives copy-paste, and is decoded as ordinary
ASCII by several tokenizers, which makes it the canonical invisible-prompt
vector. A leading U+FEFF is treated as a benign BOM.
"""

import os
import sys
from collections import Counter

SUSPECT: dict[int, str] = {}


def _add(lo: int, hi: int, why: str) -> None:
    for cp in range(lo, hi + 1):
        SUSPECT[cp] = why


_add(0x200B, 0x200D, "zero-width")
_add(0x200E, 0x200F, "bidi mark")
_add(0x202A, 0x202E, "bidi embed/override")
_add(0x2060, 0x2064, "invisible operator")
_add(0x2066, 0x2069, "bidi isolate")
_add(0xE0000, 0xE007F, "UNICODE TAG (invisible prompt vector)")
SUSPECT[0xFEFF] = "ZWNBSP/BOM"
SUSPECT[0x00AD] = "soft hyphen"
SUSPECT[0x034F] = "combining grapheme joiner"
SUSPECT[0x180E] = "mongolian vowel separator"

SKIP_DIR = {
    ".git", "node_modules", "dist", "build", ".next", "out",
    "coverage", "vendor", "testdata", "__pycache__",
}
SKIP_EXT = {
    ".png", ".jpg", ".jpeg", ".gif", ".svg", ".ico", ".webp", ".pdf",
    ".woff", ".woff2", ".ttf", ".otf", ".eot", ".zip", ".gz", ".tgz",
    ".bz2", ".xz", ".tar", ".mp3", ".mp4", ".wav", ".mov", ".icns",
    ".jar", ".class", ".wasm", ".so", ".dylib", ".dll", ".a", ".o",
    ".bin", ".node", ".pyc", ".asar", ".lock",
}
SKIP_NAME = {
    "package-lock.json", "go.sum", "flake.lock",
    "pnpm-lock.yaml", "yarn.lock", "Cargo.lock",
}
MAX_BYTES = 6_000_000


def scan(root: str) -> list[tuple[str, int, int, str, str]]:
    hits: list[tuple[str, int, int, str, str]] = []
    files = 0
    total = 0
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames[:] = [d for d in dirnames if d not in SKIP_DIR]
        for name in filenames:
            if name in SKIP_NAME or os.path.splitext(name)[1].lower() in SKIP_EXT:
                continue
            path = os.path.join(dirpath, name)
            try:
                raw = open(path, "rb").read()
            except OSError:
                continue
            if b"\x00" in raw[:4096] or len(raw) > MAX_BYTES:
                continue
            try:
                text = raw.decode("utf-8")
            except UnicodeDecodeError:
                continue
            files += 1
            total += len(raw)
            if not any(ord(ch) > 0x7F for ch in text):
                continue
            for i, ch in enumerate(text):
                cp = ord(ch)
                why = SUSPECT.get(cp)
                if why is None or (cp == 0xFEFF and i == 0):
                    continue
                hits.append((
                    os.path.relpath(path, root), i, cp, why,
                    repr(text[max(0, i - 40):i + 40]),
                ))
    print(f"=== {root}: {files} text files, {total / 1e6:.1f} MB ===")
    if not hits:
        print("  CLEAN - zero invisible/bidi/tag codepoints.\n")
        return hits
    print(f"  {len(hits)} hit(s):")
    for why, n in Counter(h[3] for h in hits).most_common():
        print(f"    {n:5d}  {why}")
    print()
    for path, off, cp, why, ctx in hits:
        print(f"  {path}  off={off}  U+{cp:04X} ({why})\n      {ctx[:150]}")
    print()
    return hits


def main() -> int:
    if len(sys.argv) < 2:
        print(__doc__)
        return 2
    return 1 if any(scan(t) for t in sys.argv[1:]) else 0


if __name__ == "__main__":
    sys.exit(main())
