"""Real Chromium test; run through the pinned development shell with a built binary."""
import json
import re
from pathlib import Path
import socket
import subprocess
import sys
import tempfile
import time
import urllib.request

from playwright.sync_api import sync_playwright, expect


def ports(count):
    sockets = [socket.socket() for _ in range(count)]
    try:
        for connection in sockets:
            connection.bind(("127.0.0.1", 0))
        return [connection.getsockname()[1] for connection in sockets]
    finally:
        for connection in sockets:
            connection.close()


with tempfile.TemporaryDirectory(prefix="nixmox-browser-") as directory:
    work = Path(directory)
    http, machine, client, route, leaf = ports(5)
    origin = f"http://127.0.0.1:{http}"
    config = work / "config.json"
    config.write_text(json.dumps({
        "schema_version": 1, "data_dir": str(work / "state"),
        "http": {"port": http}, "machine": {"port": machine},
        "nats": {"client_port": client, "route_port": route, "leaf_port": leaf},
        "telemetry": {"host": {"fast_interval_ms": 250, "filesystem_interval_ms": 1000}},
    }))
    process = None
    log = (work / "daemon.log").open("w")

    def start():
        global process
        process = subprocess.Popen([sys.argv[1], "serve", "--config", str(config)], stdout=log, stderr=log)
        for _ in range(100):
            if process.poll() is not None:
                raise RuntimeError((work / "daemon.log").read_text())
            try:
                with urllib.request.urlopen(origin + "/api/v1/identity", timeout=1) as response:
                    return json.load(response)
            except OSError:
                time.sleep(0.1)
        raise RuntimeError("daemon did not start")

    try:
        identity = start()
        with sync_playwright() as playwright:
            browser = playwright.chromium.launch(headless=True)
            context = browser.new_context(viewport={"width": 1100, "height": 900})
            # The console must work without external assets or requests.
            context.route("**/*", lambda route: route.continue_() if route.request.url.startswith(origin + "/") else route.abort())
            page = context.new_page()
            errors = []
            page.on("pageerror", lambda error: errors.append(str(error)))
            page.goto(origin)
            page.get_by_role("button", name="Check network").click()
            expect(page.locator("#receipt")).to_contain_text("Accepted command")
            page.get_by_role("link", name="Host telemetry", exact=True).click()
            expect(page.locator("#stream-state")).to_contain_text("Live updates connected")
            expect(page.locator("#telemetry-status")).to_contain_text("Memory")
            expect(page.locator("#cpu-history path")).to_have_attribute("d", re.compile("L"))
            page.set_viewport_size({"width": 420, "height": 760})
            expect(page.locator("#telemetry-status")).to_contain_text("CPU usage")
            measured = 0
            for cell in page.locator("#telemetry-status td").all():
                bounds = cell.bounding_box()
                if bounds is not None:  # A live htmx replacement can detach a row.
                    measured += 1
                    assert bounds["x"] >= 0 and bounds["x"] + bounds["width"] <= 420
            assert measured >= 3
            process.terminate()
            process.wait(timeout=10)
            expect(page.locator("#stream-state")).to_contain_text("Disconnected", timeout=10000)
            frozen = page.locator("#telemetry-status").inner_text()
            page.wait_for_timeout(1200)
            assert page.locator("#telemetry-status").inner_text() == frozen
            assert start()["node_id"] == identity["node_id"]
            expect(page.locator("#stream-state")).to_contain_text("Live updates connected", timeout=15000)
            expect(page.locator("#telemetry-status")).to_contain_text("Measurements: live")
            assert not errors, errors
            page.screenshot(path="/tmp/nixmox-telemetry-browser.png", full_page=True)
            context.close()
            browser.close()
        print("PASS: Chromium form admission, local assets, live telemetry, resize, frozen disconnect and restart recovery")
    finally:
        if process is not None and process.poll() is None:
            process.terminate()
            process.wait(timeout=10)
        log.close()
