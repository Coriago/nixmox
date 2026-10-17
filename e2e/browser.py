#!/usr/bin/env python3
"""Browser acceptance against the running E2E cluster, plus stream edge cases."""
import json
import os
from pathlib import Path
import sys
from playwright.sync_api import sync_playwright

url, artifacts = sys.argv[1], Path(sys.argv[2])
with sync_playwright() as p:
    browser = p.chromium.launch(executable_path=os.environ['NIXMOX_E2E_CHROMIUM'],
                               headless=True, args=['--no-sandbox'])
    page = browser.new_page(viewport={'width': 1440, 'height': 1000})
    errors = []
    page.on('pageerror', lambda error: errors.append(str(error)))
    page.add_init_script('''(() => {
      const Native = EventSource;
      window.EventSource = class extends Native {
        constructor(...args) { super(...args); window.testStream = this; }
      };
    })();''')
    page.goto(url + '/')
    page.wait_for_selector('#connection.online')
    page.wait_for_function('document.querySelectorAll("nav[data-live] .host-nav-item").length === 3')
    assert page.locator('nav[data-live] .serving-node').count() == 1
    assert page.locator('nav[data-live] [data-node="node2"] .serving-node').inner_text() == 'This UI'
    assert page.locator('tbody[data-live="/partials/hosts"] tr').count() == 3
    page.evaluate('''() => {
      const selector = '[data-uptime-host="node1"]';
      let previous = document.querySelector(selector).textContent;
      window.uptimeTransitions = [];
      window.uptimeObserver = new MutationObserver(() => {
        const text = document.querySelector(selector)?.textContent;
        if (text && text !== previous) {
          uptimeTransitions.push({time: performance.now(), text}); previous = text;
        }
      });
      uptimeObserver.observe(document.querySelector('tbody[data-live="/partials/hosts"]'),
                             {subtree:true, childList:true, characterData:true});
    }''')
    # The runner starts a chatty task just before this page opens. Observe both
    # bursty NATS updates and quiet periods through the actual SSE connection.
    page.wait_for_timeout(8500)
    transitions = page.evaluate('uptimeTransitions')
    assert 6 <= len(transitions) <= 10, transitions
    intervals = [b['time'] - a['time'] for a, b in zip(transitions, transitions[1:])]
    assert all(800 <= gap <= 1250 for gap in intervals), transitions
    (artifacts / 'browser-uptime.json').write_text(json.dumps(transitions, indent=2))
    page.screenshot(path=str(artifacts / 'browser-desktop.png'), full_page=True)

    # Inject controlled offline/reboot reports at the browser's SSE boundary;
    # the VM suite separately performs real machine power loss and reboot.
    page.evaluate('''() => {
      uptimeObserver.disconnect(); testStream.close();
      window.reportUptime = (seconds, online) => {
        const tbody = document.querySelector('tbody[data-live="/partials/hosts"]');
        const copy = tbody.cloneNode(true);
        const clock = copy.querySelector('[data-uptime-host="node1"]');
        clock.dataset.uptime = seconds; clock.dataset.online = online;
        testStream.dispatchEvent(new MessageEvent('fragments',
          {data: JSON.stringify({'/partials/hosts':copy.innerHTML})}));
      };
      reportUptime(12, false);
    }''')
    clock = page.locator('[data-uptime-host="node1"]')
    assert clock.inner_text() == '12s'
    page.wait_for_timeout(2200)
    assert clock.inner_text() == '12s', 'offline uptime kept increasing'
    page.evaluate('reportUptime(2, true)')
    assert clock.inner_text() == '2s', 'reboot did not reset uptime'
    page.wait_for_timeout(1200)
    assert clock.inner_text() in ('3s', '4s'), 'rebooted uptime did not resume'

    # Browser loss/recovery must be visible, preserve stale data, and freeze clocks.
    page.goto(url + '/')
    page.wait_for_selector('#connection.online')
    page.context.set_offline(True)
    page.wait_for_selector('#connection-warning:not([hidden])')
    assert 'node2' in page.locator('#connection-message').inner_text()
    assert page.locator('#alternate-hosts a').count() == 2
    frozen = clock.inner_text()
    page.wait_for_timeout(1500)
    assert clock.inner_text() == frozen
    page.locator('#reconnect').click()
    assert page.locator('#connection-warning').is_visible()
    page.context.set_offline(False)
    page.wait_for_selector('#connection.online')
    assert page.locator('#connection-warning').is_hidden()

    # Real remote telemetry must arrive independently of the 10-second heartbeat.
    page.goto(url + '/hosts/node1')
    recipient = page.locator('[data-age-recipient]')
    assert recipient.input_value().startswith('age1')
    page.evaluate("Object.defineProperty(navigator, 'clipboard', {value: undefined, configurable: true})")
    page.locator('[data-copy-age]').click()
    assert 'Selected' in page.locator('[data-copy-status]').inner_text()
    assert recipient.evaluate('(el) => el.selectionEnd - el.selectionStart') == len(recipient.input_value())
    page.evaluate("Object.defineProperty(navigator, 'clipboard', {value: {writeText: async text => {window.copiedRecipient = text}}, configurable: true})")
    page.locator('[data-copy-age]').click()
    page.wait_for_function("document.querySelector('[data-copy-status]').textContent.includes('Copied')")
    assert page.evaluate('copiedRecipient') == recipient.input_value()
    page.locator('details > summary').click()
    page.wait_for_selector('[data-telemetry-at]')
    page.evaluate('''() => {
      window.telemetryTransitions = [];
      let previous = document.querySelector('[data-telemetry-at]').dataset.telemetryAt;
      new MutationObserver(() => {
        const sample = document.querySelector('[data-telemetry-at]')?.dataset.telemetryAt;
        if (sample && sample !== previous) {
          telemetryTransitions.push({time:performance.now(), sample}); previous = sample;
        }
      }).observe(document.querySelector('[data-live$="/resources"]'), {childList:true, subtree:true});
    }''')
    inventory_before = page.locator('[data-inventory-at]').get_attribute('data-inventory-at')
    page.wait_for_timeout(8500)
    telemetry = page.evaluate('telemetryTransitions')
    assert 3 <= len(telemetry) <= 6, telemetry
    gaps = [b['time'] - a['time'] for a, b in zip(telemetry, telemetry[1:])]
    assert all(1000 <= gap <= 3500 for gap in gaps), telemetry
    inventory_after = page.locator('[data-inventory-at]').get_attribute('data-inventory-at')
    # Crossing the slow refresh boundary is valid, but it must not follow each fast sample.
    if inventory_after != inventory_before:
        assert int(inventory_after) - int(inventory_before) >= 29000
    (artifacts / 'browser-telemetry.json').write_text(json.dumps(telemetry, indent=2))

    job = page.evaluate('''async () => {
      const response = await fetch('/api/v1/hosts/node1/jobs', {
        method:'POST',headers:{'Content-Type':'application/json','Idempotency-Key':'browser-log-'+Date.now()},
        body:JSON.stringify({kind:'command',name:'Browser log stream',command:'/run/current-system/sw/bin/bash',
          args:['-euc', 'for i in {1..16}; do printf "stream-%02d <tag> café 🔧\\n" "$i"; sleep 0.6; done']})
      });
      if (!response.ok) throw new Error(await response.text());
      return response.json();
    }''')
    page.goto(url + '/hosts/node1/jobs/' + job['id'])
    page.wait_for_selector('#connection.online')
    assert page.locator('body').get_attribute('data-host') == 'node2'
    page.wait_for_function('document.querySelector("[data-log]").textContent.includes("stream-02")')
    page.context.set_offline(True)
    page.wait_for_selector('#connection-warning:not([hidden])')
    page.wait_for_timeout(2200)
    page.locator('#reconnect').click()
    assert page.locator('#connection-warning').is_visible()
    page.context.set_offline(False)
    page.wait_for_selector('#connection.online')
    page.wait_for_function('document.querySelector("[data-log]").textContent.includes("stream-16 <tag> café 🔧")', timeout=30000)
    output = page.locator('[data-log]').inner_text()
    for i in range(1,17):
        assert output.count(f'stream-{i:02d} <tag> café 🔧') == 1, output
    assert page.locator('[data-log] tag').count() == 0, 'log output was interpreted as HTML'
    (artifacts / 'browser-log-stream.txt').write_text(output)
    page.screenshot(path=str(artifacts / 'browser-log-stream.png'), full_page=True)

    page.set_viewport_size({'width': 390, 'height': 844})
    for path in ['/', '/hosts/node1', '/jobs']:
        page.goto(url + path)
        page.wait_for_selector('#connection.online')
        page.wait_for_function('document.querySelectorAll("nav[data-live] .host-nav-item").length === 3')
        assert page.evaluate('document.documentElement.scrollWidth <= innerWidth'), path + ' overflows'
    page.screenshot(path=str(artifacts / 'browser-mobile.png'), full_page=True)
    assert not errors, errors
    print('PASS: browser sidebar, serving-node identity, SSE, steady 1 Hz uptime under burst updates, offline/reboot clocks, disconnect/reconnect, split telemetry, incremental log streaming/recovery, mobile layout')
    import runpy
    onboarding = runpy.run_path(os.environ.get('NIXMOX_E2E_ONBOARDING_BROWSER',
                                               str(Path(__file__).with_name('onboarding_browser.py'))))
    onboarding['check_onboarding'](page, url, artifacts)
    assert not errors, errors
    browser.close()
