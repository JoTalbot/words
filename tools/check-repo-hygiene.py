#!/usr/bin/env python3
"""Guard the repository against tracked build output and oversized blobs.

The Unity/Go pipeline writes binaries, APKs, license caches and JSONL telemetry
into the working tree, and a single accidental `git add -A` of one of those is
irreversible in practice: a 14 MB ARM build artifact committed once (server/game,
from 384ffea) stays in every clone forever and no later deletion shrinks the
history. This checker makes that class of mistake a CI failure rather than a
permanent cost, and it fails closed: anything over the size limit that is not
named in ALLOWED_LARGE is a finding.
"""

from __future__ import annotations

import os
import subprocess
import sys

SIZE_LIMIT = 1 * 1024 * 1024  # 1 MiB

# Exact repo-relative paths that are legitimately large. Add to this list only
# with a reason; the point is that growth requires a deliberate edit.
ALLOWED_LARGE = [
    # Compiled dictionary source data. These are inputs, not build output: the
    # dictionary service reads them and they are versioned with the snapshot
    # they belong to (dictionary/NOTICE.md).
    "server/internal/dictionary/data/ru.words",
    "server/internal/dictionary/data/uk.words",
]

# Path fragments that are build or runtime output in this repository.
ARTIFACT_FRAGMENTS = (
    "*.jsonl",
    "*.apk",
    "*.aab",
    "*.so",
    "*.dylib",
    "*.dll",
    "*.exe",
    "*.class",
    "*.jar",
    "server/game",
    "server/migrate",
    "server/wordarena-server",
    "Builds/",
    "Library/",
    "Temp/",
    "obj/",
    "bin/Debug/",
)


def git_lines(*args: str) -> list[str]:
    proc = subprocess.run(
        ("git", *args),
        capture_output=True,
        text=True,
        check=False,
    )
    if proc.returncode != 0:
        detail = proc.stderr.strip() or "git failed"
        if "not a git repository" in detail:
            print("error: run this from inside the repository", file=sys.stderr)
            sys.exit(2)
        print(f"error: git {args[0]}: {detail}", file=sys.stderr)
        sys.exit(2)
    return [line for line in proc.stdout.splitlines() if line.strip()]


def matches(name: str, pattern: str) -> bool:
    if pattern.startswith("*"):
        return name.endswith(pattern[1:])
    if pattern.endswith("/"):
        return pattern in name
    return name == pattern


def tracked_files(root: str) -> list[str]:
    return sorted(git_lines("-C", root, "ls-files"))


def collect(root: str) -> tuple[int, list[str]]:
    failures: list[str] = []
    names = tracked_files(root)
    allowed = set(ALLOWED_LARGE)

    for name in names:
        path = os.path.join(root, name)
        if not os.path.isfile(path):
            failures.append(f"FAIL {name}: tracked but missing from the worktree")
            continue

        for pattern in ARTIFACT_FRAGMENTS:
            if matches(name, pattern):
                failures.append(f"FAIL {name}: matches artifact pattern {pattern}")

        try:
            size = os.path.getsize(path)
        except OSError as exc:  # unreadable path is a finding, not a crash
            failures.append(f"FAIL {name}: stat failed ({exc})")
            continue

        if size > SIZE_LIMIT and name not in allowed:
            failures.append(
                f"FAIL {name}: {size / 1048576:.2f} MiB exceeds the "
                f"1 MiB tracked-file limit (add it to ALLOWED_LARGE only if it "
                f"is genuine source data)"
            )

        with open(path, "rb") as handle:
            head = handle.read(2)
            if head == b"#!":
                mode = os.stat(path).st_mode
                if not mode & 0o111:
                    failures.append(f"FAIL {name}: shebang without the executable bit")
            if name.endswith((".py", ".sh")):
                handle.seek(0)
                if b"\r\n" in handle.read(65536):
                    failures.append(f"FAIL {name}: CRLF line endings")

    return len(names), failures


def main() -> int:
    root = subprocess.run(
        ("git", "rev-parse", "--show-toplevel"),
        capture_output=True,
        text=True,
        check=False,
    ).stdout.strip()
    if not root:
        print("error: run this from inside the repository", file=sys.stderr)
        return 2
    checked, failures = collect(root)
    for line in failures:
        print(line)
    print(f"hygiene: {checked} checked, {len(failures)} failed")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
