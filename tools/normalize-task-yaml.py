"""Rewrite agent/tasks/*.yml so every file is loadable and stays shaped.

Companion to tools/check-task-yaml.py, which is the gate that runs in CI. This is
the repair tool: an agent that writes a task with an unquoted ": " inside a
description can fix the whole directory with

    python3 tools/normalize-task-yaml.py

It is idempotent by construction rather than by a stack of special cases: a file
that already loads and whose lists contain only strings is left untouched, so a
second run is a no-op and the tool can never re-fold its own output. Silently
reformatting a healthy file is exactly what CI must not do, which is also why
this tool is never invoked by a workflow.

Transforms applied to a file that needs them:
  - a multi-line plain scalar becomes a folded block scalar (`>-`);
  - a single-line scalar that YAML would read as a mapping (because it contains
    ": ", or starts with an indicator character) becomes a quoted scalar.
No wording changes.
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

try:
    import yaml
except ImportError:
    print("error: PyYAML is required (install pyyaml)", file=sys.stderr)
    sys.exit(2)

ROOT = Path(__file__).resolve().parents[1]
TASKS = ROOT / "agent" / "tasks"
CONT_RE = re.compile(r"^\s{4,}\S")
NEW_RE = re.compile(r"^\s*(-\s|[A-Za-z_][\w-]*:)")
SPECIAL_FIRST = "\"'&*!|>%@`"
WIDTH = 94


def wrap(indent: str, text: str) -> list[str]:
    out: list[str] = []
    cur = ""
    for word in text.split(" "):
        if cur and len(cur) + len(word) + 1 > WIDTH:
            out.append(f"{indent}{cur}")
            cur = word
        else:
            cur = f"{cur} {word}".strip()
    if cur:
        out.append(f"{indent}{cur}")
    return out


def quote(text: str) -> str:
    return '"' + text.replace("\\", "\\\\").replace('"', '\\"') + '"'


def needs_quoting(body: str) -> bool:
    if body[:1] in "[{":  # flow collections are values; quoting them corrupts
        return False
    return bool(re.search(r":\s", body)) or body[:1] in SPECIAL_FIRST or body.endswith(":")


def needs_fix(text: str) -> bool:
    """True when the file cannot load, or a protocol list lost its string shape.

    Deliberately the same test tools/check-task-yaml.py applies, and no stricter:
    a nested structure an author meant to write (a list of mappings under some
    other key) is not this tool's business, and refusing to touch a file because
    of one would block the repair it does need.
    """
    try:
        doc = yaml.safe_load(text)
    except yaml.YAMLError:
        return True
    if not isinstance(doc, dict):
        return True
    for key in ("acceptance", "validation"):
        value = doc.get(key)
        if isinstance(value, list) and any(not isinstance(item, str) for item in value):
            return True
    return False


def normalize(text: str) -> str:
    lines = text.split("\n")
    out: list[str] = []
    i = 0
    while i < len(lines):
        ln = lines[i]
        item = re.match(r"^(\s*)-\s+(\S.*)$", ln)
        key = None if item else re.match(r"^(\s*)([A-Za-z_][\w-]*):\s+(\S.*)$", ln)
        if not item and not key:
            out.append(ln)
            i += 1
            continue

        match = item or key
        indent = match.group(1)
        prefix = "- " if item else f"{key.group(2)}: "
        body = match.group(2) if item else match.group(3)

        cont: list[str] = []
        j = i + 1
        while j < len(lines) and CONT_RE.match(lines[j]) and not NEW_RE.match(lines[j]):
            cont.append(lines[j].strip())
            j += 1
        body = " ".join([body] + cont).strip()

        if cont:
            out.append(f"{indent}{prefix}>-")
            out.extend(wrap(indent + "  ", body))
        elif needs_quoting(body):
            out.append(f"{indent}{prefix}{quote(body)}")
        else:
            out.append(ln)
        i = j
    return "\n".join(out)


def main() -> int:
    changed: list[str] = []
    for path in sorted(TASKS.glob("*.yml")):
        original = path.read_text(encoding="utf-8")
        if not needs_fix(original):
            continue
        fixed = normalize(original)
        try:
            yaml.safe_load(fixed)
        except yaml.YAMLError as exc:  # never leave a directory half rewritten
            print(f"error: {path.name} still does not load after rewriting: {exc}", file=sys.stderr)
            return 1
        if needs_fix(fixed):
            print(f"error: {path.name} rewritten but still misshapen", file=sys.stderr)
            return 1
        if fixed != original:
            if not fixed.endswith("\n") and original.endswith("\n"):
                fixed += "\n"
            path.write_text(fixed, encoding="utf-8")
            changed.append(path.name)
    print(f"normalized {len(changed)} file(s)" + (": " + ", ".join(changed) if changed else ""))
    return 0


if __name__ == "__main__":
    sys.exit(main())
