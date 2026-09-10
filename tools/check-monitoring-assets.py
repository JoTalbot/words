#!/usr/bin/env python3
"""Validate the monitoring assets against the metrics the service emits.

A Grafana dashboard silently renders an empty panel when it references a metric
that does not exist, which is the worst possible failure mode for monitoring:
the graph is green, the number is zero, and nobody notices. This script parses
the metric names out of the Go source and fails if the dashboard or the
monitoring README references anything the service does not emit.

Usage:
    python3 tools/check-monitoring-assets.py
"""

from __future__ import annotations

import json
import re
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent

TELEMETRY_SRC = REPO_ROOT / "server" / "cmd" / "game" / "telemetry.go"
DASHBOARD = REPO_ROOT / "infra" / "monitoring" / "grafana" / "dashboards" / "wordarena.json"
PROM_TARGETS = REPO_ROOT / "infra" / "monitoring" / "prometheus" / "wordarena.yml"
MONITORING_README = REPO_ROOT / "infra" / "monitoring" / "README.md"

failures: list[str] = []
checks = 0


def fail(message: str) -> None:
    failures.append(message)
    print(f"FAIL  {message}")


def ok(message: str) -> None:
    global checks
    checks += 1
    print(f"PASS  {message}")


def metric_names_from_source() -> set[str]:
    """Extract the emitted metric names from the Prometheus text handler."""
    if not TELEMETRY_SRC.is_file():
        fail(f"missing {TELEMETRY_SRC.relative_to(REPO_ROOT)}")
        return set()
    src = TELEMETRY_SRC.read_text(encoding="utf-8")
    names = set(re.findall(r'writeMetric\(\s*"(wordarena_[a-z0-9_]+)"', src))
    if not names:
        fail("no writeMetric(...) calls found in telemetry.go")
    return names


def metrics_referenced_by(text: str) -> set[str]:
    """Every wordarena_* identifier mentioned in a PromQL expression or table."""
    return set(re.findall(r"wordarena_[a-z0-9_]+", text))


def check_dashboard(emitted: set[str]) -> None:
    if not DASHBOARD.is_file():
        fail(f"missing {DASHBOARD.relative_to(REPO_ROOT)}")
        return
    try:
        dash = json.loads(DASHBOARD.read_text(encoding="utf-8"))
    except json.JSONDecodeError as exc:
        fail(f"dashboard is not valid JSON: {exc}")
        return
    ok("dashboard parses as JSON")

    for key in ("title", "uid", "schemaVersion", "panels"):
        if key not in dash:
            fail(f"dashboard is missing the top-level key '{key}'")
    if dash.get("uid") != "wordarena-m1":
        fail(f"dashboard uid is {dash.get('uid')!r}, want 'wordarena-m1'")
    else:
        ok("dashboard uid is stable ('wordarena-m1')")

    panels = dash.get("panels", [])
    if not panels:
        fail("dashboard has no panels")
        return
    ok(f"dashboard has {len(panels)} panel(s)")

    # Referenced metrics must exist, or the panel is silently empty.
    referenced: set[str] = set()
    for panel in panels:
        for target in panel.get("targets", []):
            referenced |= metrics_referenced_by(target.get("expr", ""))
    unknown = sorted(referenced - emitted)
    if unknown:
        fail(f"dashboard references metrics the service does not emit: {unknown}")
    else:
        ok(f"all {len(referenced)} dashboard metrics are emitted by the service")

    # Every emitted metric should be surfaced somewhere, otherwise operators
    # cannot see a signal the code already produces.
    missing = sorted(emitted - referenced)
    if missing:
        fail(f"metrics emitted but never plotted: {missing}")
    else:
        ok("every emitted metric appears on the dashboard")

    # Panel ids must be unique and grid positions must not overlap.
    ids = [p.get("id") for p in panels]
    if len(set(ids)) != len(ids):
        fail(f"panel ids are not unique: {ids}")
    else:
        ok("panel ids are unique")

    occupied: set[tuple[int, int]] = set()
    overlap = False
    for panel in panels:
        g = panel.get("gridPos", {})
        x, y = int(g.get("x", 0)), int(g.get("y", 0))
        w, h = int(g.get("w", 0)), int(g.get("h", 0))
        for cx in range(x, x + max(w, 1)):
            for cy in range(y, y + max(h, 1)):
                if (cx, cy) in occupied:
                    fail(f"panel {panel.get('id')} overlaps at cell ({cx},{cy})")
                    overlap = True
                occupied.add((cx, cy))
        if x + w > 24:
            fail(f"panel {panel.get('id')} exceeds the 24-column grid (x+w={x + w})")
    if not overlap:
        ok(f"panel grid positions do not overlap ({len(occupied)} cells)")


def check_prometheus_config() -> None:
    if not PROM_TARGETS.is_file():
        fail(f"missing {PROM_TARGETS.relative_to(REPO_ROOT)}")
        return
    lines = PROM_TARGETS.read_text(encoding="utf-8").splitlines()
    # Strip YAML comments: this file legitimately documents the scrape path in
    # prose while remaining a pure target list.
    body = "\n".join(l for l in lines if not l.lstrip().startswith("#"))
    if "job" not in body:
        fail("prometheus target file has no job label")
    else:
        ok("prometheus target file sets a job label")
    if "/metrics/prometheus" in body and "metrics_path" not in body:
        fail("prometheus target file looks like it hardcodes a metrics path")
    else:
        ok("prometheus target file stays a pure file_sd target list")


def check_readme(emitted: set[str]) -> None:
    if not MONITORING_README.is_file():
        fail(f"missing {MONITORING_README.relative_to(REPO_ROOT)}")
        return
    referenced = metrics_referenced_by(MONITORING_README.read_text(encoding="utf-8"))
    unknown = sorted(referenced - emitted)
    if unknown:
        fail(f"monitoring README references unknown metrics: {unknown}")
    else:
        ok(f"all {len(referenced)} metrics named in the README are emitted")


def metrics_from_live(url: str) -> set[str] | None:
    """Fetch /metrics/prometheus from a running service and parse the names.

    This catches the failure the static check cannot: a metric that the Go
    source declares but the running build does not actually expose.
    """
    import urllib.request

    target = url.rstrip("/") + "/metrics/prometheus"
    try:
        with urllib.request.urlopen(target, timeout=15) as resp:  # noqa: S310
            body = resp.read().decode("utf-8", errors="replace")
    except Exception as exc:  # noqa: BLE001
        fail(f"could not fetch {target}: {exc}")
        return None
    names = set(re.findall(r"^(wordarena_[a-z0-9_]+)", body, re.MULTILINE))
    # Fall back to the TYPE lines if the sample lines are absent.
    names |= set(re.findall(r"^# TYPE (wordarena_[a-z0-9_]+)", body, re.MULTILINE))
    if not names:
        fail(f"no wordarena_* metrics found at {target}")
        return None
    ok(f"live service exposes {len(names)} metric(s) at /metrics/prometheus")
    return names


def main() -> int:
    live_url = ""
    args = [a for a in sys.argv[1:]]
    if args and args[0] == "--live":
        if len(args) < 2:
            print("usage: check-monitoring-assets.py [--live <base-url>]")
            return 2
        live_url = args[1]

    print("Word Arena monitoring-asset validation")
    print(f"repo root: {REPO_ROOT}\n")

    emitted = metric_names_from_source()
    if emitted:
        ok(f"parsed {len(emitted)} metric names from server/cmd/game/telemetry.go")

    check_dashboard(emitted)
    check_prometheus_config()
    check_readme(emitted)

    if live_url:
        live = metrics_from_live(live_url)
        if live is not None:
            unknown = sorted(set(re.findall(r"wordarena_[a-z0-9_]+",
                                            DASHBOARD.read_text(encoding="utf-8"))) - live)
            if unknown:
                fail(f"dashboard references metrics the live service does not expose: {unknown}")
            else:
                ok("every dashboard metric is present in the live exposition")
            missing_live = sorted(live - emitted)
            if missing_live:
                fail(f"live service exposes metrics missing from the source scan: {missing_live}")

    print(f"\n{checks} check(s) passed, {len(failures)} failed")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
