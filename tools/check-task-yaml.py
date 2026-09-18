#!/usr/bin/env python3
"""Validate the task graph: every agent/tasks/*.yml must parse and be complete.

Task files are the protocol's durable contract (docs/TASK-PROTOCOL.md): a
session reads `acceptance` before starting and writes `validation` before
claiming a task is done. None of that works if a file cannot be loaded, and an
unquoted scalar containing ": " (or a wrapped continuation line) silently turns
a whole file into a parse error. Two such files were authored in this
repository on 2026-09-10 and nobody noticed, because nothing read them.

Statuses are deliberately NOT a closed set: the protocol names five lifecycle
states while real files carry qualified ones (`verified_local_pending_ci`,
`validated_local_live_pending`), and that qualification is useful information.
The checker validates the shape and prints the vocabulary it found, so drift
toward meaningless statuses stays visible instead of being frozen into an enum.
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

try:
    import yaml
except ImportError:
    print(
        "error: PyYAML is required (install pyyaml); refusing to report the task\n"
        "graph as valid without parsing it",
        file=sys.stderr,
    )
    sys.exit(2)

ROOT = Path(__file__).resolve().parents[1]
TASKS = ROOT / "agent" / "tasks"
REQUIRED = ("id", "title", "status", "acceptance", "validation", "risk")
LIST_KEYS = ("acceptance", "validation")
STATUS_RE = re.compile(r"^[a-z][a-z0-9_]*$")


def check_file(path: Path) -> tuple[list[str], str | None]:
    problems: list[str] = []
    try:
        doc = yaml.safe_load(path.read_text(encoding="utf-8"))
    except yaml.YAMLError as exc:
        mark = getattr(exc, "problem_mark", None)
        where = f" (line {mark.line + 1})" if mark is not None else ""
        return [f"{path.name}: not loadable YAML{where}: {exc}"], None
    if not isinstance(doc, dict):
        return [f"{path.name}: expected a mapping at the top level"], None

    for key in REQUIRED:
        if key not in doc:
            problems.append(f"{path.name}: missing required key '{key}'")
    if doc.get("id") != path.stem:
        problems.append(f"{path.name}: id {doc.get('id')!r} does not match the file name")

    status = doc.get("status")
    if not isinstance(status, str) or not STATUS_RE.match(status or ""):
        problems.append(f"{path.name}: status must be a snake_case string, got {status!r}")

    for key in LIST_KEYS:
        value = doc.get(key)
        if value is None:
            continue
        if not isinstance(value, list) or not value:
            problems.append(f"{path.name}: {key} must be a non-empty list")
        elif not all(isinstance(item, str) and item.strip() for item in value):
            problems.append(f"{path.name}: {key} items must be non-empty strings")
    return problems, status


def main() -> int:
    files = sorted(TASKS.glob("*.yml"))
    if not files:
        print(f"error: no task files under {TASKS}", file=sys.stderr)
        return 1
    problems: list[str] = []
    statuses: dict[str, int] = {}
    for path in files:
        found, status = check_file(path)
        problems.extend(found)
        if status:
            statuses[status] = statuses.get(status, 0) + 1
    for line in problems:
        print(f"FAIL {line}")
    print(f"task graph: {len(files)} files, {len(problems)} problems")
    print("statuses: " + ", ".join(f"{k}={v}" for k, v in sorted(statuses.items())))
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
