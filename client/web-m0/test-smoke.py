#!/usr/bin/env python3
"""Fail-closed real-browser transport smoke against an explicitly selected server.

WORDARENA_SERVER_URL=http://127.0.0.1:18110 python3 client/web-m0/test-smoke.py
Creates one disposable en match unless BOTH WORDARENA_MATCH_ID and
WORDARENA_TOKEN are supplied. Use an isolated instance, never a load/live target.
Requires Playwright + Chromium. No token, URL query or raw request is logged.
"""
import functools
import http.server
import json
import os
import pathlib
import socket
import threading
import urllib.parse
import urllib.request
from contextlib import contextmanager

from playwright.sync_api import sync_playwright

HERE = pathlib.Path(__file__).resolve().parent


def server_base(value):
    u = urllib.parse.urlsplit(value)
    if (u.scheme not in ("http", "https", "ws", "wss") or not u.hostname
            or u.username or u.password or u.path not in ("", "/")
            or u.query or u.fragment):
        raise ValueError("WORDARENA_SERVER_URL must be an explicit HTTP(S)/WS(S) base URL")
    scheme = {"ws": "http", "wss": "https"}.get(u.scheme, u.scheme)
    return urllib.parse.urlunsplit((scheme, u.netloc, "", "", ""))


class QuietHandler(http.server.SimpleHTTPRequestHandler):
    def log_message(self, *_args):
        pass  # Query parameters contain an ephemeral seat credential.


@contextmanager
def serve_page():
    handler = functools.partial(QuietHandler, directory=str(HERE))
    with http.server.ThreadingHTTPServer(("127.0.0.1", 0), handler) as server:
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            yield f"http://127.0.0.1:{server.server_port}/index.html"
        finally:
            server.shutdown()
            thread.join(timeout=5)


def main():
    base = server_base(os.environ.get("WORDARENA_SERVER_URL", ""))
    match_id, token = os.environ.get("WORDARENA_MATCH_ID", ""), os.environ.get("WORDARENA_TOKEN", "")
    if bool(match_id) != bool(token):
        raise ValueError("Supply both match ID and token, or neither")
    if not token:
        req = urllib.request.Request(base + "/v1/matches", data=b'{"language":"en"}',
                                     headers={"Content-Type": "application/json"})
        with urllib.request.urlopen(req, timeout=10) as response:
            match = json.load(response)
        match_id, token = str(match["match_id"]), match["tokens"][0]

    with serve_page() as page_url, sync_playwright() as p:
        browser = p.chromium.launch()
        try:
            page = browser.new_page(viewport={"width": 1100, "height": 500})
            errors = []
            page.on("pageerror", lambda error: errors.append(str(error)))

            def visit(server, seat_token, state):
                query = urllib.parse.urlencode({"match_id": match_id, "token": seat_token, "server": server})
                page.goto(page_url + "?" + query)
                page.wait_for_selector(f'#s[data-state="{state}"]', timeout=10000)

            visit(base, token, "open")
            page.wait_for_function("Number(document.querySelector('#frames').dataset.count) > 0", timeout=10000)
            assert not errors, "Uncaught browser exception"
            if os.environ.get("WORDARENA_SCREENSHOT"):
                path = pathlib.Path(os.environ["WORDARENA_SCREENSHOT"])
                path.parent.mkdir(parents=True, exist_ok=True)
                page.screenshot(path=str(path))
            print("PASS real WebSocket OPEN + nonempty binary frame on selected server")

            visit(base, "invalid-seat-token", "error")
            assert page.locator("#frames").get_attribute("data-count") == "0"
            print("PASS invalid seat token cannot yield transport success")

            visit("javascript:alert(1)", token, "error")
            assert "Configuration error" in page.inner_text("#s")
            print("PASS non-network scheme refused before opening a socket")

            # Hold an unlistening ephemeral port so no unrelated service can
            # acquire it between selection and the negative connection test.
            with socket.socket() as unused:
                unused.bind(("127.0.0.1", 0))
                visit(f"http://127.0.0.1:{unused.getsockname()[1]}", token, "error")
            assert page.locator("#frames").get_attribute("data-count") == "0"
            print("PASS unreachable endpoint cannot yield transport success")

            page.goto(page_url)
            page.wait_for_selector('#s[data-state="error"]', timeout=3000)
            assert "Provide match_id" in page.inner_text("#s")
            assert not errors, "Uncaught browser exception"
            print("PASS missing credentials fail closed; WEB SMOKE: 5/5")
        finally:
            browser.close()


if __name__ == "__main__":
    main()
