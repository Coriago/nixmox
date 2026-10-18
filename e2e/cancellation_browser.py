#!/usr/bin/env python3
"""Exercise task cancellation controls against an actual daemon handler."""
import json
import os
from pathlib import Path
import sys
import time
from playwright.sync_api import sync_playwright, expect

url, artifacts = sys.argv[1], Path(sys.argv[2])
artifacts.mkdir(parents=True, exist_ok=True)
with sync_playwright() as p:
    browser = p.chromium.launch(executable_path=os.environ['NIXMOX_E2E_CHROMIUM'], headless=True, args=['--no-sandbox'])
    page = browser.new_page()
    errors = []
    page.on('pageerror', lambda error: errors.append(str(error)))
    def submit(script):
        response = page.request.post(url + '/api/v1/hosts/local/jobs', data={
            'kind': 'command', 'command': 'sh', 'args': ['-c', script]})
        assert response.status == 202, response.text()
        return response.json()
    running = submit('echo ready; sleep 60')
    queued = submit('echo must-not-run')
    page.goto(url + '/jobs')
    queued_button = page.locator('[data-cancel-job$="/' + queued['id'] + '/cancel"]')
    # Dismissing confirmation must leave the task queued.
    page.once('dialog', lambda dialog: dialog.dismiss())
    queued_button.click()
    assert page.request.get(url + '/api/v1/jobs/' + queued['id']).json()['state'] == 'pending'
    page.once('dialog', lambda dialog: dialog.accept())
    with page.expect_response(lambda r: r.url.endswith(queued["id"] + "/cancel") and r.request.method == "POST") as response:
        queued_button.click()
    assert response.value.status == 202, response.value.text()
    assert page.request.get(url + '/api/v1/jobs/' + queued['id']).json()['state'] == 'cancelled'
    page.goto(url + '/hosts/local/jobs/' + running['id'])
    page.once('dialog', lambda dialog: dialog.accept())
    page.locator('[data-cancel-job]').click()
    deadline = time.monotonic() + 5
    while True:
        result = page.request.get(url + '/api/v1/jobs/' + running['id']).json()
        if result['state'] == 'cancelled':
            break
        assert time.monotonic() < deadline, result
        time.sleep(.05)
    expect(page.locator('[data-cancel-job]')).to_have_count(0)
    expect(page.locator('#connection')).to_have_class('badge online')
    assert 'cancelled' in page.locator('body').inner_text()
    assert 'Cancellation requested' in page.request.get(url + '/api/v1/jobs/' + running['id'] + '/log').text()
    page.screenshot(path=str(artifacts / 'cancelled-task.png'), full_page=True)
    (artifacts / 'cancelled-task.json').write_text(json.dumps(result, indent=2))
    assert not errors, errors
    browser.close()
