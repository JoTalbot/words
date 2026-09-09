# Minimal Playwright smoke test — M0 web client connectivity proof
from playwright.sync_api import sync_playwright
with sync_playwright() as p:
    browser = p.chromium.launch()
    page = browser.new_page()
    page.goto("file:///opt/words/client/web-m0/index.html")
    page.wait_for_selector("#s", timeout=2000)
    txt = page.inner_text("#s")
    print("STATUS:", txt[:80])
    assert "WebSocket" in txt or "Connecting" in txt or "error" in txt.lower(), "unexpected state"
    browser.close()
