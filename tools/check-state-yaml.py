#!/usr/bin/env python3
"""Validate the durable resume state file: agent/state/current.yml.

`current.yml` is the handoff contract between autonomous sessions
(docs/AUTONOMOUS-DEVELOPMENT-MASTER.md sections 17 and 18): a new session reads
it to learn what is done, what is open and where to resume. It is YAML, so an
unquoted scalar containing ": " turns it into a parse error, and a repeated key
silently drops one of its two values - both happened to it, and both survived
because the CI gate only reads `agent/tasks/*.yml`. The file a session depends
on most was the one file nothing checked.

This tool checks the three things a resume reader needs: it parses, the keys the
protocol requires are present, and no top-level key is duplicated.
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

try:
    import yaml
except ImportError:  # pragma: no cover - CI installs PyYAML explicitly
    print("error: PyYAML is required; refusing to report the state file as valid", file=sys.stderr)
    sys.exit(2)

ROOT = Path(__file__).resolve().parents[1]
STATE = ROOT / "agent" / "state" / "current.yml"

REQUIRED = (
    "last_verified_commit",
    "session",
    "m2_remaining",
    "facts_non_relitigable",
    "server",
    "resume_from",
)
KEY_RE = re.compile(r"^(\s*)(?:-\s+)?([A-Za-z0-9_.-]+):(\s|$)")


def duplicate_keys(text: str) -> list[str]:
    """Keys repeated within the same mapping, at any depth, which loading hides.

    SafeLoader keeps the last value and discards the earlier one without a word.
    The real case this catches is not a top-level collision: it is
    `session.5.merges_this_session`, written twice while a session's notes were
    appended, so the file claimed `[]` and `["35D PR #54"]` in the same mapping
    and a reader silently saw only the last. Detection is textual on purpose -
    it has to survive the parse it is guarding, and the file that does not parse
    is the one where the extra defect matters most.

    Mapping identity is approximated by indentation, which is what YAML block
    style actually uses: a key belongs to the mapping whose keys sit at its own
    indent, directly under the last shallower key. Sequence entries (- key) are
    tracked by the same rule and reported with their parent path.
    """
    counts: dict[tuple, int] = {}
    # (indent, key) of each open mapping level, outermost first.
    stack: list[tuple[int, str]] = []
    for line in text.splitlines():
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        m = KEY_RE.match(line)
        if not m:
            continue
        indent, key = len(m.group(1)), m.group(2)
        while stack and stack[-1][0] >= indent:
            stack.pop()
        path = tuple(k for _, k in stack) + (key,)
        counts[path] = counts.get(path, 0) + 1
        stack.append((indent, key))
    return sorted(".".join(p) for p, n in counts.items() if n > 1)


def main() -> int:
    if not STATE.is_file():
        print(f"state file: MISSING {STATE.relative_to(ROOT)}", file=sys.stderr)
        return 1
    text = STATE.read_text(encoding="utf-8")

    # Reported before the parse is attempted: a duplicate key is a textual fact
    # about the file, and the file that cannot be parsed is exactly the case
    # where a session most needs to know what else is wrong with it.
    problems: list[str] = []
    dups: list[str] = []
    for path in duplicate_keys(text):
        msg = f"state file: DUPLICATE KEY {path} (the first value is silently dropped)"
        problems.append(msg)
        dups.append(msg)
        print(msg, file=sys.stderr)

    try:
        doc = yaml.safe_load(text)
    except yaml.YAMLError as exc:
        print("state file: PARSE FAILED", file=sys.stderr)
        print(f"  {exc}", file=sys.stderr)
        print(
            "  An unquoted scalar containing ': ' is the usual cause - quote the value.",
            file=sys.stderr,
        )
        return 1

    if not isinstance(doc, dict):
        print(f"state file: the document is a {type(doc).__name__}, want a mapping", file=sys.stderr)
        return 1

    for key in REQUIRED:
        if key not in doc:
            problems.append(f"state file: MISSING KEY {key}")

    session = doc.get("session")
    if isinstance(session, dict):
        for name, entry in session.items():
            if not isinstance(entry, dict) or "what" not in entry:
                problems.append(f"state file: session.{name} must be a mapping with a 'what' key")
    elif "session" in doc:
        problems.append("state file: session must be a mapping")
    for p in problems[len(dups) :]:
        print(p, file=sys.stderr)

    for key in ("m2_remaining", "facts_non_relitigable"):
        value = doc.get(key)
        if value is not None and (not isinstance(value, list) or not value):
            problems.append(f"state file: {key} must be a non-empty list")

    resume = doc.get("resume_from")
    if resume is not None and (
        not isinstance(resume, list) or not resume or not all(isinstance(x, str) and x.strip() for x in resume)
    ):
        problems.append("state file: resume_from must be a non-empty list of non-empty strings")

    if problems:
        return 1

    print(f"state file: ok ({len(doc)} keys, {len(resume or [])} resume items)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
