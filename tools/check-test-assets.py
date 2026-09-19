#!/usr/bin/env python3
"""Validate the data assets under tests/ against the Go sources that use them.

The test assets in tests/network and tests/load are documentation-adjacent
data: they describe the fault matrix and the load-baseline input parameters.
Their authority, however, lives in the Go tests. If someone edits the Go
constants and forgets the JSON - or edits the JSON to look nicer than the code
- the two silently disagree.

This script parses the Go sources and fails on any drift, so the assets can be
trusted by automation. It has no dependencies beyond the standard library and
exits non-zero on the first mismatch.

Usage:
    python3 tools/check-test-assets.py
"""

from __future__ import annotations

import json
import re
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent

NETSIM_SRC = REPO_ROOT / "server" / "cmd" / "game" / "netsim_test.go"
BENCH_SRC = REPO_ROOT / "server" / "internal" / "match" / "bench_test.go"
FAULT_MATRIX = REPO_ROOT / "tests" / "network" / "fault-matrix.json"
LOAD_PROFILE = REPO_ROOT / "tests" / "load" / "baseline-profile.json"

failures: list[str] = []
checks = 0


def fail(message: str) -> None:
    failures.append(message)
    print(f"FAIL  {message}")


def ok(message: str) -> None:
    global checks
    checks += 1
    print(f"PASS  {message}")


def read(path: Path) -> str:
    if not path.is_file():
        fail(f"missing required file: {path.relative_to(REPO_ROOT)}")
        return ""
    return path.read_text(encoding="utf-8")


def load_json(path: Path) -> dict | None:
    text = read(path)
    if not text:
        return None
    try:
        data = json.loads(text)
    except json.JSONDecodeError as exc:
        fail(f"{path.relative_to(REPO_ROOT)} is not valid JSON: {exc}")
        return None
    if not isinstance(data, dict):
        fail(f"{path.relative_to(REPO_ROOT)} must contain a JSON object")
        return None
    return data


def parse_rtts(source: str) -> list[int] | None:
    match = re.search(
        r"rtts\s*(?::=|=)\s*\[\]time\.Duration\{([^}]*)\}", source
    )
    if not match:
        fail("could not find the rtt slice in server/cmd/game/netsim_test.go")
        return None
    values = re.findall(r"(\d+)\s*\*\s*time\.Millisecond", match.group(1))
    if not values:
        fail("rtt slice found but no <n> * time.Millisecond entries parsed")
        return None
    return [int(v) for v in values]


def parse_losses(source: str) -> list[float] | None:
    match = re.search(r"losses\s*(?::=|=)\s*\[\]float64\{([^}]*)\}", source)
    if not match:
        fail("could not find the loss slice in server/cmd/game/netsim_test.go")
        return None
    values = [v.strip() for v in match.group(1).split(",") if v.strip()]
    if not values:
        fail("loss slice found but no entries parsed")
        return None
    return [float(v) for v in values]


def parse_bench_const(source: str, name: str) -> int | None:
    match = re.search(rf"const\s+{re.escape(name)}\s*=\s*(\d+)", source)
    if not match:
        fail(f"could not find `const {name}` in server/internal/match/bench_test.go")
        return None
    return int(match.group(1))


def check_fault_matrix() -> None:
    source = read(NETSIM_SRC)
    data = load_json(FAULT_MATRIX)
    if not source or data is None:
        return

    src_rtts = parse_rtts(source)
    src_losses = parse_losses(source)

    if src_rtts is not None:
        if data.get("rtt_ms") == src_rtts:
            ok(f"fault-matrix rtt_ms matches Go source {src_rtts}")
        else:
            fail(f"fault-matrix rtt_ms {data.get('rtt_ms')} != Go source {src_rtts}")

    if src_losses is not None:
        if data.get("loss_ratios") == src_losses:
            ok(f"fault-matrix loss_ratios matches Go source {src_losses}")
        else:
            fail(
                f"fault-matrix loss_ratios {data.get('loss_ratios')} != "
                f"Go source {src_losses}"
            )

    # The named profiles are derived data: assert they are exactly the cross
    # product of the two declared axes, in order, so a profile cannot drift
    # away from the matrix it claims to describe.
    if src_rtts is not None and src_losses is not None:
        expected = [
            {"rtt_ms": rtt, "loss": loss} for rtt in src_rtts for loss in src_losses
        ]
        actual = [
            {"rtt_ms": p.get("rtt_ms"), "loss": p.get("loss")}
            for p in data.get("profiles", [])
        ]
        if actual == expected:
            ok(f"fault-matrix profiles are the {len(expected)}-cell cross product")
        else:
            fail(
                "fault-matrix profiles are not the declared cross product; "
                f"expected {expected}, got {actual}"
            )


def check_load_profile() -> None:
    source = read(BENCH_SRC)
    data = load_json(LOAD_PROFILE)
    if not source or data is None:
        return

    params = data.get("parameters")
    if not isinstance(params, dict):
        fail("tests/load/baseline-profile.json has no `parameters` object")
        return

    for json_key, go_const in (("matches", "matches"),
                               ("submissions_per_match", "subsPerMatch")):
        src_value = parse_bench_const(source, go_const)
        if src_value is None:
            continue
        if params.get(json_key) == src_value:
            ok(f"load profile {json_key} == const {go_const} ({src_value})")
        else:
            fail(
                f"load profile {json_key} ({params.get(json_key)!r}) does not "
                f"match Go const {go_const} ({src_value})"
            )

    # Guard against measured results leaking into the asset tree: results
    # belong in docs/LOAD-BASELINE.md.
    forbidden = (
        "submissions_sec_total",
        "submissions_sec_per_match",
        "heap_kib_per_match",
        "measured",
    )
    text = LOAD_PROFILE.read_text(encoding="utf-8")
    leaked = [k for k in forbidden if k in data]
    if leaked:
        fail(f"load profile contains measured results {leaked}; keep them in docs/")
    elif "ns/op" in text:
        fail("load profile contains a measured ns/op value; keep results in docs/")
    else:
        ok("load profile contains parameters only, no measured results")


def main() -> int:
    print("Word Arena test-asset validation")
    print(f"repo root: {REPO_ROOT}\n")

    check_fault_matrix()
    check_load_profile()

    print(f"\n{checks} check(s) passed, {len(failures)} failed")
    if failures:
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
