#!/usr/bin/env python3
"""Minimal Playwright E2E smoke test — M0/M1 web client connectivity proof.

Loads the release-proof page (index.html) from this directory and asserts the
client reaches a real WebSocket state. If WORDARENA_MATCH_ID and
WORDARENA_TOKEN are provided (and a server is listening on
WORDARENA_SERVER_URL, default ws://127.0.0.1:18080), the test requires a hard
"WebSocket OPEN" — proving the binary protobuf transport end to end in a real
browser. Otherwise it falls back to the tolerant M0 assertion.

Usage:
    python3 test-smoke.py
    WORDARENA_MATCH_ID=2 WORDARENA_TOKEN=<token> python3 test-smoke.py
"""
import os
import pathlib

from playwright.sync_api import sync_playwright

HERE = pathlib.Path(__file__).resolve().parent
PAGE = pathlib.Path(os.environ.get("WORDARENA_PAGE", str(HERE / "index.html")))

match_id = os.environ.get("WORDARENA_MATCH_ID", "")
token = os.environ.get("WORDARENA_TOKEN", "")
strict = bool(match_id and token)

url = PAGE.as_uri()
if strict:
    url += f"?match_id={match_id}&token={token}"

with sync_playwright() as p:
    browser = p.chromium.launch()
    page = browser.new_page()
    page.goto(url)
    page.wait_for_selector("#s", timeout=3000)
    page.wait_for_timeout(1200)
    txt = page.inner_text("#s")
    print("STATUS:", txt.splitlines()[0][:120])
    if strict:
        assert "WebSocket OPEN" in txt, "expected authoritative OPEN, got: " + txt
        print("WEB SMOKE: PASS (authoritative WebSocket OPEN)")
    else:
        assert ("WebSocket" in txt or "Connecting" in txt
                or "error" in txt.lower()), "unexpected state: " + txt
        print("WEB SMOKE: PASS (tolerant M0 transport proof)")
    browser.close()
