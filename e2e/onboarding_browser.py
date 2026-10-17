"""Focused browser workflow; API boundaries are simulated, no disks are touched."""
import json
from pathlib import Path


def check_onboarding(page, url, artifacts):
    schema = {
        'Name': {'type': 'hostname', 'default': 'newhost'},
        'Username': {'type': 'username', 'default': 'helios'},
        'Modules': {'type': 'multiselect', 'values': ['base', 'desktop'], 'default': ['base']},
        'Drive': {'type': 'disk', 'required': True},
    }
    session = {'id': 'browser-session', 'node_id': 'installer', 'address': '192.0.2.2:8080',
               'info': {'recipient': 'age1public', 'network': {'transferable': True, 'message': 'Wi-Fi profiles will be transferred locally.'},
                        'disks': [{'path': '/dev/nvme0n1', 'whole': True, 'installer': False, 'model': 'Test NVMe', 'serial': 'TEST-123', 'size': 100 * 1024**3}]},
               'values': {}, 'revision': '', 'job_id': '', 'phase': 'Configure host', 'completed': False}
    state = {'starts': 0, 'previews': 0, 'failed': False}

    def api(route):
        path = route.request.url.split('/api/v1/', 1)[1]
        body = route.request.post_data_json if route.request.method == 'POST' else {}
        status, result = 200, {}
        if path == 'onboarding/coordinators':
            result = [{'node_id': 'writer', 'ready': True}, {'node_id': 'reader', 'ready': False, 'error': 'No age identity'}]
        elif path == 'hosts/writer/onboarding':
            result = {'form': schema, 'repository': 'git@example.test:config', 'branch': 'main', 'sops': True}
        elif path == 'discovery':
            result = [{'node_id': 'installer', 'address': session['address'], 'cluster': '', 'installer': True}]
        elif path.endswith('/onboarding/inspect'):
            assert body['node_id'] == 'installer'
            result = session
        elif path.endswith('/onboarding/preview'):
            state['previews'] += 1
            assert body['values']['Modules'] == ['base', 'desktop']
            session.update(values=body['values'], digest='reviewed', result={'path': 'hosts/browser-host.nix', 'content': '{ networking.hostName = "browser-host"; }'},
                           plan={'hostname': 'browser-host', 'drive': {'path': '/dev/nvme0n1'}}, phase='Review configuration and confirm erasure')
            result = session
        elif path.endswith('/onboarding/start'):
            assert body['confirm'] == '/dev/nvme0n1' and body['digest'] == 'reviewed'
            state['starts'] += 1
            session.update(job_id='test-job', revision='a' * 40, phase='Installing locally on the selected disk')
            result = {'session': session, 'job': {'id': 'test-job'}}
            status = 202
        elif '/onboarding/sessions/' in path:
            state['failed'] = state['starts'] == 1
            session['completed'] = state['starts'] == 2
            if session['completed']:
                session['phase'] = 'Installed host is reachable and joined; temporary installer removed'
            result = {'session': session, 'job': {'id': 'test-job', 'state': 'failed' if state['failed'] else 'applied', 'error': 'Simulated install failure' if state['failed'] else ''}, 'log': '<script>untrusted output</script>\nPinned revision ' + 'a' * 40}
        elif path.endswith('/onboarding/sessions'):
            result = [session] if session['job_id'] else []
        else:
            route.fallback()
            return
        route.fulfill(status=status, content_type='application/json', body=json.dumps(result))

    page.route('**/api/v1/**', api)
    page.on('dialog', lambda dialog: dialog.accept())
    page.goto(url + '/add-host')
    page.get_by_role('button', name='Set up host', exact=True).click()
    page.locator('input[name=Name]').fill('browser-host')
    assert page.locator('input[name=Username]').input_value() == 'helios'
    page.locator('select[name=Modules]').select_option(['base', 'desktop'])
    assert 'TEST-123' in page.locator('select[name=Drive]').inner_text()
    page.locator('select[name=Drive]').select_option('/dev/nvme0n1')
    page.get_by_role('button', name='Preview configuration', exact=True).click()
    page.locator('#onboarding-review:not([hidden])').wait_for()
    page.locator('input[name=Username]').fill('changed')
    assert page.locator('#onboarding-review').is_hidden(), 'edited values retained stale install authorization'
    page.get_by_role('button', name='Preview configuration', exact=True).click()
    page.locator('#onboarding-review:not([hidden])').wait_for()
    page.locator('#onboarding-confirm').fill('/dev/wrong')
    page.locator('#onboarding-start-button').click()
    assert state['starts'] == 0
    page.locator('#onboarding-confirm').fill('/dev/nvme0n1')
    page.locator('#onboarding-start-button').click()
    page.get_by_text('Simulated install failure', exact=True).wait_for()
    assert page.locator('#onboarding-log').inner_text().startswith('<script>'), 'logs were interpreted as HTML'
    assert page.locator('#onboarding-log script').count() == 0
    assert page.locator('#onboarding-confirm').input_value() == '', 'retry retained erase confirmation'
    assert page.locator('input[name=Name]').is_disabled(), 'published revision became editable'
    page.locator('#onboarding-confirm').fill('/dev/nvme0n1')
    page.locator('#onboarding-start-button').click()
    page.get_by_role('link', name='Open installed host').wait_for()
    assert state['starts'] == 2 and state['previews'] == 2
    Path(artifacts).mkdir(parents=True, exist_ok=True)
    page.evaluate('window.scrollTo(0, 0)')
    page.screenshot(path=str(Path(artifacts) / 'onboarding-browser.png'), full_page=True)
    page.unroute('**/api/v1/**', api)


if __name__ == '__main__':
    import os
    import sys
    from playwright.sync_api import sync_playwright
    with sync_playwright() as p:
        browser = p.chromium.launch(executable_path=os.environ['NIXMOX_E2E_CHROMIUM'], headless=True, args=['--no-sandbox'])
        page = browser.new_page(viewport={'width': 1280, 'height': 1000})
        errors = []
        page.on('pageerror', lambda error: errors.append(str(error)))
        check_onboarding(page, sys.argv[1], sys.argv[2])
        assert not errors, errors
        browser.close()
