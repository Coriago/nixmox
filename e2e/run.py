#!/usr/bin/env python3
"""Disposable, real-GitHub three-node E2E. See README.md for assertions and phases."""
import argparse
import contextlib
import json
import os
from pathlib import Path
import shlex
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
import uuid

HTTP = urllib.request.build_opener(urllib.request.ProxyHandler({}))
NODES = (1, 2, 3)


def require(condition, message):
    if not condition:
        raise AssertionError(message)


def poll(label, check, timeout=180):
    deadline = time.monotonic() + timeout
    last = None
    while time.monotonic() < deadline:
        try:
            result = check()
            if result:
                return result
        except (OSError, ValueError, AssertionError, subprocess.SubprocessError) as error:
            last = error
        time.sleep(1)
    raise TimeoutError(f"{label}: timed out after {timeout}s; last error: {last}")


def free_port(kind=socket.SOCK_STREAM):
    with socket.socket(socket.AF_INET, kind) as sock:
        sock.bind(('127.0.0.1', 0))
        return sock.getsockname()[1]


def log_delta(previous, current):
    # Job responses contain a bounded tail. Its length stops growing even as
    # the command continues to emit output.
    overlap = min(len(previous), len(current))
    while overlap and previous[-overlap:] != current[:overlap]:
        overlap -= 1
    return current[overlap:]


class Suite:
    def __init__(self, work, resume=False):
        self.work = work.resolve()
        self.work.mkdir(parents=True, exist_ok=True)
        self.manifest = self.work / 'run.json'
        self.log = (self.work / 'runner.log').open('a', buffering=1)
        if resume:
            self.state = json.loads(self.manifest.read_text())
            require(self.state['version'] == 1, 'unsupported run manifest')
        else:
            require(not self.manifest.exists(), 'work directory already contains a run')
            self.state = {'version': 1, 'phase': 'new', 'network_port': free_port(socket.SOCK_DGRAM),
                          'vms': {str(i): os.environ[f'NIXMOX_E2E_VM{i}'] for i in NODES}}
            self.save()
        self.say(f'Artifacts: {self.work}')

    def save(self):
        temporary = self.manifest.with_suffix('.tmp')
        temporary.write_text(json.dumps(self.state, indent=2) + '\n')
        temporary.replace(self.manifest)

    def say(self, message):
        line = f'[{time.strftime("%H:%M:%S")}] {message}'
        print(line, flush=True)
        print(line, file=self.log)

    def stage(self, name):
        self.say(f'=== {name} ===')
        self.state['stage'] = name
        self.save()

    def artifact(self, name, data):
        (self.work / name).write_text(json.dumps(data, indent=2) + '\n')

    def api(self, node, path, body=None, method=None, timeout=35):
        request = urllib.request.Request(f'http://127.0.0.1:{8080+node}{path}',
            data=None if body is None else json.dumps(body).encode(),
            method=method or ('GET' if body is None else 'POST'),
            headers={'Content-Type': 'application/json'})
        with HTTP.open(request, timeout=timeout) as response:
            return json.load(response)

    def ssh_args(self, node):
        return ['sshpass', '-e', 'ssh', '-p', str(2220+node),
                '-o', 'StrictHostKeyChecking=no', '-o', f'UserKnownHostsFile={self.work}/known_hosts',
                '-o', 'ConnectTimeout=5', '-o', 'LogLevel=ERROR', 'root@127.0.0.1']

    def ssh(self, node, script, timeout=40, quiet=False):
        self.say(f'node{node}$ {script}')
        result = subprocess.run(self.ssh_args(node) + [script], env=dict(os.environ, SSHPASS='nixos'),
                                capture_output=True, text=True, timeout=timeout)
        if result.stdout.strip() and not quiet:
            self.say(result.stdout.rstrip())
        require(result.returncode == 0, f'node{node} SSH exited {result.returncode}: {result.stderr}')
        return result.stdout.strip()

    def qmp(self, node, command):
        with socket.socket(socket.AF_UNIX) as sock:
            sock.settimeout(5)
            sock.connect(str(self.work / f'node{node}.qmp'))
            stream = sock.makefile('rwb', buffering=0)
            json.loads(stream.readline())
            for operation in ('qmp_capabilities', command):
                stream.write(json.dumps({'execute': operation}).encode() + b'\n')
                while True:
                    response = json.loads(stream.readline())
                    if 'error' in response:
                        raise RuntimeError(response)
                    if 'return' in response:
                        break
            return response['return']

    def running(self, node):
        try:
            return bool(self.qmp(node, 'query-status'))
        except (OSError, ValueError):
            return False

    def start(self, node):
        require(not self.running(node), f'node{node} is already running')
        # Any listener, including an unrelated daemon, is a conflict. Never use it.
        for port in (8080+node, 2220+node):
            with socket.socket() as check:
                check.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
                check.bind(('0.0.0.0', port))
        (self.work / f'node{node}.qmp').unlink(missing_ok=True)
        name = f'node{node}'
        command = [f'{self.state["vms"][str(node)]}/bin/run-{name}-vm',
                   '-netdev', f'socket,id=cluster,mcast=230.0.0.1:{self.state["network_port"]},localaddr=127.0.0.1',
                   '-device', f'virtio-net-pci,netdev=cluster,mac=52:54:00:12:34:{node:02x}',
                   '-qmp', f'unix:{self.work}/{name}.qmp,server=on,wait=off']
        self.say(shlex.join(command))
        with (self.work / f'{name}.console.log').open('ab') as console:
            process = subprocess.Popen(command, cwd=self.work, stdin=subprocess.DEVNULL,
                stdout=console, stderr=subprocess.STDOUT, start_new_session=True,
                env=dict(os.environ, NIX_DISK_IMAGE=str(self.work / f'{name}.qcow2')))
        self.state.setdefault('pids', {})[str(node)] = process.pid
        self.save()
        poll(f'{name} QEMU startup', lambda: self.running(node))
        self.ready(node)

    def ready(self, node):
        poll(f'node{node} HTTP readiness', lambda: self.api(node, '/healthz')['status'] == 'ok')
        require(self.api(node, '/api/v1/host')['hostname'] == f'node{node}', 'wrong VM on forwarded port')
        poll(f'node{node} SSH readiness', lambda: self.ssh(node, 'true') == '')

    def stop(self, node, hard=False):
        self.say(f'{"Power cut" if hard else "Shutdown"}: node{node}')
        if self.running(node):
            if hard:
                self.qmp(node, 'quit')
            else:
                self.ssh(node, 'systemctl poweroff --no-block')
            poll(f'node{node} powered off', lambda: not self.running(node), 60)
        poll(f'node{node} HTTP disconnected', lambda: self.unreachable(node), 30)

    def unreachable(self, node):
        try:
            self.api(node, '/healthz', timeout=2)
            return False
        except (OSError, ValueError):
            return True

    def hosts(self, observer):
        return {h['node_id']: h for h in self.api(observer, '/api/v1/hosts')}

    def membership(self, observers, members, offline=()):
        expected = {f'node{i}' for i in members}
        for node in observers:
            def check():
                hosts = self.hosts(node)
                return (set(hosts) == expected and
                        all(h['online'] == (int(name[4:]) not in offline) for name, h in hosts.items()))
            poll(f'node{node} membership {sorted(expected)}, offline={offline}', check)

    def diagnostics(self, node):
        return self.api(node, '/api/v1/cluster/diagnostics')

    def topology(self, nodes, size, replicas):
        # R=1 streams need only exist on one server. R=3 must have all three copies.
        def check():
            seen = set()
            leaders = set()
            synced = set()
            meta_current = size == 1
            for node in nodes:
                data = self.diagnostics(node)
                if size == 1:
                    require(data['mode'] == 'standalone' and not data.get('meta'), data)
                else:
                    meta = data.get('meta', {})
                    require(data['mode'] == 'raft' and meta.get('cluster_size') == size, data)
                    require(meta.get('leader'), data)
                    leaders.add(meta['leader'])
                    if meta['leader'] == f'node{node}':
                        peers = meta.get('replicas', [])
                        require(len(peers) == size-1 and all(p.get('current') and not p.get('offline') for p in peers), data)
                        meta_current = True
                for stream in data['streams']:
                    require(stream['config']['num_replicas'] == replicas, stream)
                    if replicas == 3:
                        cluster = stream.get('cluster', {})
                        require(cluster.get('leader') and len(cluster.get('replicas', [])) == 2, stream)
                        # Followers do not track other followers' catch-up status.
                        # Only the group's leader can establish this assertion.
                        if cluster['leader'] == f'node{node}':
                            require(all(p.get('current') and not p.get('offline') for p in cluster['replicas']), stream)
                            synced.add(stream['name'])
                    seen.add(stream['name'])
                if replicas == 3:
                    require(len(data['streams']) == 2, data)
            require(len(leaders) <= 1, leaders)
            return (meta_current and seen == {'KV_hosts', 'KV_applylog'} and
                    (replicas != 3 or synced == seen))
        poll(f'Raft size={size}, stream replicas={replicas}', check, 240)
        for node in nodes:
            self.artifact(f'node{node}-topology-{size}-{replicas}.json', self.diagnostics(node))
        self.say(f'PASS: metadata mode={"standalone" if size == 1 else "raft"}, members={size}, stream replicas={replicas}')

    def quorum_probe(self, node, writable=True):
        """Publish to a non-host key in the existing R=3 hosts stream, requiring its commit ACK.

        SSH -W reaches loopback NATS without exposing a client port on the LAN.
        A PONG before and after a rejected write rules out transport failure.
        The nonce key expires by the hosts bucket TTL and is ignored by WatchHosts.
        """
        token = uuid.uuid4().hex
        inbox = f'_INBOX.e2e{token}'
        local, remote = socket.socketpair()
        process = subprocess.Popen(self.ssh_args(node)[:-1] + ['-W', '127.0.0.1:4222', 'root@127.0.0.1'],
            stdin=remote, stdout=remote, stderr=subprocess.PIPE, env=dict(os.environ, SSHPASS='nixos'))
        remote.close()
        buffer = b''
        def line(deadline):
            nonlocal buffer
            while b'\r\n' not in buffer:
                local.settimeout(max(.01, deadline-time.monotonic()))
                chunk = local.recv(65536)
                require(chunk, 'NATS transport closed')
                buffer += chunk
            value, buffer = buffer.split(b'\r\n', 1)
            return value
        def receive(deadline, want_pong=False):
            while time.monotonic() < deadline:
                header = line(deadline)
                if header == b'PING':
                    local.sendall(b'PONG\r\n')
                elif header == b'PONG' and want_pong:
                    return True
                elif header.startswith(b'-ERR'):
                    raise AssertionError(header)
                elif header.startswith(b'MSG '):
                    body = line(deadline)
                    require(len(body) == int(header.split()[-1]), 'unexpected NATS payload framing')
                    return json.loads(body)
            raise TimeoutError('NATS reply deadline')
        try:
            local.sendall(f'CONNECT {{"verbose":false}}\r\nSUB {inbox} 1\r\nPING\r\n'.encode())
            require(receive(time.monotonic()+10, True) is True, 'NATS not responsive')
            local.sendall(f'PUB $KV.hosts.e2e.{token} {inbox} {len(token)}\r\n{token}\r\n'.encode())
            reply = None
            try:
                reply = receive(time.monotonic()+6)
            except TimeoutError:
                pass
            if writable:
                require(isinstance(reply, dict) and reply.get('stream') == 'KV_hosts' and reply.get('seq', 0) > 0,
                        f'cluster write did not commit: {reply}')
            else:
                require(reply is None or (isinstance(reply, dict) and reply.get('error', {}).get('code') == 503),
                        f'write unexpectedly committed or failed for another reason: {reply}')
            local.sendall(b'PING\r\n')
            require(receive(time.monotonic()+5, True) is True, 'NATS transport failed during write probe')
            self.artifact(f'quorum-node{node}-{token}.json', {'writable': writable, 'reply': reply})
            self.say(f'PASS: node{node} cluster write {"committed" if writable else "blocked without quorum"}; transport responsive')
        finally:
            local.close()
            process.terminate()
            process.communicate(timeout=10)

    def wait_job(self, observer, target, job, timeout=3600):
        identity = job['id']
        path = f'/api/v1/hosts/node{target}/jobs/{identity}'
        previous = []
        def check():
            nonlocal previous
            result = self.api(observer, path)
            self.artifact(f'node{target}-{identity}.json', result)
            lines = result.get('log', [])
            if lines != previous:
                # Print new output while retaining the complete response on disk.
                for output in log_delta(previous, lines):
                    self.say(f'node{target} [{identity}] {output}')
                previous = lines
            if result['state'] in ('applied', 'failed'):
                full_log = self.ssh(target, f'cat -- {shlex.quote(result["log_path"])}', quiet=True)
                (self.work / f'node{target}-{identity}.log').write_text(full_log + '\n')
            if result['state'] == 'failed':
                raise RuntimeError(f'node{target} job failed: {result}')
            return result if result['state'] == 'applied' else None
        return poll(f'node{target} job {identity}', check, timeout)

    def command(self, observer, target, name, script, wait=True):
        job = self.api(observer, f'/api/v1/hosts/node{target}/jobs',
            {'kind': 'command', 'name': name, 'command': '/run/current-system/sw/bin/bash', 'args': ['-euc', script]})
        self.say(f'Queued via node{observer} on node{target}: {name} ({job["id"]})')
        return self.wait_job(observer, target, job) if wait else job

    def snapshot(self, label):
        for node in NODES:
            if not self.running(node):
                continue
            for name, path in [('hosts','/api/v1/hosts'), ('diagnostics','/api/v1/cluster/diagnostics'),
                               ('bootstrap','/api/v1/bootstrap'), ('jobs','/api/v1/jobs'),
                               ('applylog',f'/api/v1/hosts/node{node}/applylog')]:
                try:
                    self.artifact(f'{label}-node{node}-{name}.json', self.api(node, path, timeout=5))
                except Exception as error:
                    self.say(f'Snapshot node{node}/{name}: {error}')
            try:
                result = self.ssh(node, 'journalctl -u nixmox --no-pager -n 300', timeout=15, quiet=True)
                (self.work / f'{label}-node{node}.journal.log').write_text(result)
            except Exception as error:
                self.say(f'Journal snapshot: {error}')

    def form_cluster(self):
        self.stage('1.1 Fresh standalone VMs and discovery')
        for node in NODES:
            self.start(node)
            root_bytes = int(self.ssh(node, 'df -B1 --output=size /').split()[-1])
            require(root_bytes >= 12 * 1024**3, 'boot partition did not grow to provide guest build space')
            require(self.api(node, '/api/v1/bootstrap')['status'] == 'unclustered', 'bootstrap is already joined')
            self.topology([node], 1, 1)
        poll('bidirectional LAN discovery', lambda: all(
            {p['node_id'] for p in self.api(i, '/api/v1/discovery')} == {f'node{j}' for j in NODES if j != i}
            for i in NODES))
        for node in NODES:
            require(self.api(node, '/api/v1/bootstrap')['status'] == 'unclustered', 'discovery joined implicitly')
        self.stage('1.2 Create, peer-address join, then cluster-initiated discovered-node add')
        self.api(1, '/api/v1/cluster/create', {'name': 'github-e2e'})
        self.topology([1], 1, 1)
        self.api(2, '/api/v1/cluster/join/initiate', {'seed': '192.168.42.1:8080'})
        self.membership([1, 2], [1, 2])
        self.topology([1, 2], 2, 1)
        require(self.api(3, '/api/v1/bootstrap')['status'] == 'unclustered', 'node3 joined before addition')
        peer = poll('node3 discovery on node1', lambda: next((p for p in self.api(1, '/api/v1/discovery')
                     if p['node_id'] == 'node3' and p['cluster'] == ''), None))
        require(peer['address'] == '192.168.42.3:8080', peer)
        self.api(1, '/api/v1/cluster/add', {'node_id': peer['node_id'], 'address': peer['address'], 'confirm': 'node3'})
        self.membership(NODES, NODES)
        self.topology(NODES, 3, 3)
        for node in NODES:
            self.quorum_probe(node)

    def activated(self, node):
        self.command(node, node, 'Verify GitHub activation',
            'test "$(cat /etc/nixmox-e2e-generation)" = github-main-e2e-v1; '
            'test "$(cat /var/lib/nixmox-e2e/activated)" = github-main-e2e-v1; '
            'test "$(/run/current-system/sw/bin/hello)" = "Hello, world!"; systemctl is-active nixmox-e2e-proof; '
            'test "$(readlink -f /run/current-system)" = "$(readlink -f /nix/var/nix/profiles/system)"; '
            'test "$(readlink -f /run/current-system)" != "$(cat /var/lib/nixmox/e2e-before)"')

    def deploy(self):
        self.stage('1.3 Pull the same real GitHub main revision on all nodes')
        revisions = set()
        for node in NODES:
            self.command(node, node, 'Verify original generation',
                'test ! -e /etc/nixmox-e2e-generation; test ! -e /run/current-system/sw/bin/hello; '
                'readlink -f /run/current-system > /var/lib/nixmox/e2e-before')
            config = self.api(node, '/api/v1/gitops')
            require(config['repo_url'] == 'https://github.com/Coriago/nixmox.git' and config['branch'] == 'main', config)
            pull = self.api(node, '/api/v1/gitops/pull', {})
            self.wait_job(node, node, pull)
            sha = self.api(node, '/api/v1/gitops')['last_pulled_sha']
            require(len(sha) == 40, sha)
            revisions.add(sha)
        require(len(revisions) == 1, f'main moved during pull; rerun with a stable revision: {revisions}')
        self.state['revision'] = revisions.pop()
        self.save()
        self.say(f'GitHub main revision: {self.state["revision"]}')
        self.stage('1.4 Single-node apply through another node')
        job = self.api(2, '/api/v1/hosts/node1/actions/apply', {})
        result = self.wait_job(2, 1, job)
        require(result['git_sha'] == self.state['revision'] and result['mode'] == 'switch', result)
        require(any('dir=e2e' in arg and '#node1' in arg for arg in result['args']), result)
        self.activated(1)
        for node in (2, 3):
            self.command(node, node, 'Verify single apply isolation', 'test ! -e /etc/nixmox-e2e-generation; test ! -e /run/current-system/sw/bin/hello')
        self.stage('1.5 Serial rollout; verify each node selected its own configuration')
        baseline = {node: {j['id'] for j in self.api(node, '/api/v1/jobs')} for node in NODES}
        rollout_logs = {}
        last_status = None
        self.api(1, '/api/v1/rollout', {'strategy': 'serial'})
        def completed():
            nonlocal last_status
            status = self.api(1, '/api/v1/rollout')
            self.artifact('rollout.json', status)
            if status != last_status:
                self.say(f'Rollout: {status}')
                last_status = status
            for node in NODES:
                for job in self.api(node, '/api/v1/jobs'):
                    if job['id'] in baseline[node] or job['kind'] != 'apply':
                        continue
                    value = self.api(node, f'/api/v1/jobs/{job["id"]}')
                    lines = value.get('log', [])
                    previous = rollout_logs.get(job['id'], [])
                    if lines != previous:
                        self.artifact(f'node{node}-{job["id"]}.json', value)
                        for output in log_delta(previous, lines):
                            self.say(f'node{node} [{job["id"]}] {output}')
                        rollout_logs[job['id']] = lines
            if status['state'] == 'failed':
                raise RuntimeError(status)
            return status if status['state'] == 'succeeded' else None
        rollout = poll('serial rollout', completed, 10800)
        require(set(rollout['completed']) == {'node1', 'node2', 'node3'}, rollout)
        for node in NODES:
            self.activated(node)
            records = self.api(node, f'/api/v1/hosts/node{node}/applylog')
            successful = [r for r in records if r['git_sha'] == self.state['revision'] and r['status'] == 'success']
            require(successful, records)
            require(any(r['gen_after'] > r['gen_before'] for r in successful), records)
            applied = self.api(node, f'/api/v1/jobs/{successful[0]["job_id"]}')
            require(any(f'#node{node}' in arg and 'dir=e2e' in arg for arg in applied['args']), applied)
            self.artifact(f'node{node}-{applied["id"]}.json', applied)
            full_log = self.ssh(node, f'cat -- {shlex.quote(applied["log_path"])}', quiet=True)
            (self.work / f'node{node}-{applied["id"]}.log').write_text(full_log + '\n')
            self.artifact(f'node{node}-deployment-records.json', records)
            history = self.api(node, '/api/v1/jobs')
            self.ssh(node, 'systemctl restart nixmox')
            self.ready(node)
            self.ssh(node, 'actual=$(readlink -f /proc/$(systemctl show -p MainPID --value nixmox)/exe); '
                'expected=$(systemctl show -p ExecStart --value nixmox); expected=${expected#*path=}; '
                'expected=${expected%% *}; test "$actual" = "$expected"')
            for old in history:
                current = self.api(node, f'/api/v1/jobs/{old["id"]}')
                require(current['state'] == old['state'] and current.get('log'), 'job history lost on restart')
        self.topology(NODES, 3, 3)

    def remote_tasks(self, observer=2, target=3):
        nonce = uuid.uuid4().hex
        before = self.hosts(observer)[f'node{target}']['uptime']
        job = self.command(observer, target, 'Cross-node live-log probe',
            f"printf '{nonce}\\n'; sleep 15; uname -n; printf 'finished\\n'", wait=False)
        path = f'/api/v1/hosts/node{target}/jobs/{job["id"]}'
        def live():
            value = self.api(observer, path)
            return value['state'] == 'building' and nonce in value.get('log', [])
        poll('remote output visible before completion', live, 12)
        queued = self.command(observer, target, 'Cross-node FIFO probe', 'uname -n', wait=False)
        require(self.api(observer, f'/api/v1/hosts/node{target}/jobs/{queued["id"]}')['state'] == 'pending', 'task did not queue')
        self.wait_job(observer, target, job)
        self.wait_job(observer, target, queued)
        poll('fresh remote heartbeat, not a cached view', lambda: self.hosts(observer)[f'node{target}']['uptime'] > before)
        self.command(observer, target, 'Check job destination', f'test "$(uname -n)" = node{target}')
        failed = self.command(observer, target, 'Expected command failure', "printf 'expected stderr\\n' >&2; exit 7", wait=False)
        failure = poll('remote failure history', lambda: (value if (value := self.api(observer,
            f'/api/v1/hosts/node{target}/jobs/{failed["id"]}'))['state'] == 'failed' else None))
        require(failure['exit_code'] == 7 and 'expected stderr' in failure['log'], failure)
        self.artifact('expected-failed-task.json', failure)

    def availability(self):
        self.stage('1.6 Original seed shutdown; remaining pair stays writable')
        old_boot = self.ssh(1, 'cat /proc/sys/kernel/random/boot_id')
        self.stop(1)
        self.membership([2, 3], NODES, offline=[1])
        for node in (2, 3):
            self.quorum_probe(node)
        self.remote_tasks()
        self.stage('1.7 Restart the same disk, reconnect, preserve deployment and history')
        self.start(1)
        require(self.ssh(1, 'cat /proc/sys/kernel/random/boot_id') != old_boot, 'VM did not reboot')
        self.activated(1)
        records = self.api(1, '/api/v1/hosts/node1/applylog')
        require(any(r['git_sha'] == self.state['revision'] and r['status'] == 'success' for r in records),
                'deployment history lost on VM restart')
        self.membership(NODES, NODES)
        self.topology(NODES, 3, 3)
        self.quorum_probe(1)
        self.snapshot('part1')
        self.state['phase'] = 'part1-passed'
        self.save()
        self.say('PASS PART 1: three-node deployed cluster is ready for interactive use')

    def quorum_loss(self):
        self.stage('2.1 Preflight: healthy three-member Raft and R=3 streams')
        self.membership(NODES, NODES)
        self.topology(NODES, 3, 3)
        self.quorum_probe(1)
        self.stage('2.2 Abrupt loss of two VMs; surviving member must not de-escalate')
        self.stop(2, hard=True)
        self.stop(3, hard=True)
        def disconnected():
            hosts = self.hosts(1)
            # The survivor cannot refresh its own replicated heartbeat either.
            return (set(hosts) == {'node1', 'node2', 'node3'} and
                    not hosts['node2']['online'] and not hosts['node3']['online'])
        poll('offline peers remain in membership after quorum loss', disconnected)
        # Observe longer than the replica scaler interval, not just an election timeout.
        deadline = time.monotonic() + 75
        while time.monotonic() < deadline:
            data = self.diagnostics(1)
            require(data['mode'] == 'raft' and data['meta']['cluster_size'] == 3, data)
            require(len(data['streams']) == 2 and all(s['config']['num_replicas'] == 3 for s in data['streams']), data)
            self.quorum_probe(1, writable=False)
            time.sleep(3)
        self.artifact('lost-quorum.json', self.diagnostics(1))
        require(self.api(1, '/healthz')['status'] == 'ok', 'local API froze with quorum')
        require(self.api(1, '/api/v1/bootstrap')['status'] == 'clustered', 'identity silently reset')
        self.snapshot('quorum-lost')
        self.stage('2.3 Restore one member to regain quorum, then restore the third')
        self.start(2)
        self.membership([1, 2], NODES, offline=[3])
        self.quorum_probe(1)
        self.remote_tasks(observer=1, target=2)
        self.start(3)
        self.membership(NODES, NODES)
        self.topology(NODES, 3, 3)

    def leave(self, observer, target, remaining):
        # hostAction requires form confirmation, unlike JSON cluster endpoints.
        request = urllib.request.Request(f'http://127.0.0.1:{8080+observer}/api/v1/hosts/node{target}/actions/leave',
            data=f'confirm=node{target}'.encode(), headers={'Content-Type':'application/x-www-form-urlencoded'})
        with HTTP.open(request, timeout=90) as response:
            result = json.load(response)
        require(result['status'] == 'unclustered' and not result.get('warnings'), result)
        self.artifact(f'leave-node{target}-{uuid.uuid4().hex[:8]}.json', result)
        poll(f'node{target} standalone', lambda: self.api(target, '/api/v1/bootstrap')['status'] == 'unclustered')
        self.topology([target], 1, 1)
        self.ssh(target, 'systemctl restart nixmox')
        self.ready(target)
        require(self.api(target, '/api/v1/bootstrap')['status'] == 'unclustered', 'removed node rejoined on restart')
        self.topology([target], 1, 1)
        if remaining:
            self.membership(remaining, remaining)
            self.topology(remaining, len(remaining), 1)
            self.quorum_probe(remaining[0])

    def teardown(self):
        self.stage('2.4 Graceful 3 -> 2 -> 1; R=3 -> R=1 and finally no metadata Raft')
        self.leave(1, 3, [1, 2])
        self.leave(1, 2, [1])
        self.quorum_probe(1)
        self.stage('2.5 Rejoin a removed node; escalate to Raft again, then remove it')
        self.api(2, '/api/v1/cluster/join/initiate', {'seed':'192.168.42.1:8080'})
        self.membership([1, 2], [1, 2])
        self.topology([1, 2], 2, 1)
        self.quorum_probe(1)
        self.remote_tasks(observer=1, target=2)
        self.leave(1, 2, [1])
        self.stage('2.6 Delete the final cluster and verify each node can create a fresh one')
        self.leave(1, 1, [])
        for node in NODES:
            self.api(node, '/api/v1/cluster/create', {'name':f'fresh-node{node}'})
            require(self.api(node, '/api/v1/bootstrap')['cluster_name'] == f'fresh-node{node}', 'old identity reused')
            self.membership([node], [node])
            self.topology([node], 1, 1)
            self.quorum_probe(node)
            self.command(node, node, 'Fresh cluster local task', f'test "$(uname -n)" = node{node}')
            self.leave(node, node, [])
        self.snapshot('part2')
        self.state['phase'] = 'part2-passed'
        self.save()
        self.say('PASS PART 2: quorum loss/recovery, graceful descaling, rejoin, and fresh-cluster reuse')

    def instructions(self):
        self.say('Portals: http://localhost:8081 http://localhost:8082 http://localhost:8083')
        self.say('SSH: ssh -p 2221 root@localhost (2222/2223 for peers; password nixos)')
        self.say(f'Resume teardown: nix run .#github-e2e -- --part 2 --work-dir {shlex.quote(str(self.work))}')
        self.say(f'Stop retained VMs: nix run .#github-e2e -- --stop --work-dir {shlex.quote(str(self.work))}')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--part', choices=['all','1','2'], default='all')
    parser.add_argument('--work-dir', type=Path)
    parser.add_argument('--keep-alive', action='store_true', default=os.getenv('NIXMOX_E2E_KEEP_ALIVE') == '1')
    parser.add_argument('--stop', action='store_true', help='stop only VMs owned by this run')
    args = parser.parse_args()
    resume = args.part == '2' or args.stop
    if resume and args.work_dir is None:
        parser.error('--part 2 and --stop require --work-dir')
    work = args.work_dir or Path(tempfile.mkdtemp(prefix='nixmox-cluster-e2e.'))
    suite = Suite(work, resume)
    passed = False
    try:
        if args.stop:
            return
        if args.part != '2':
            suite.form_cluster()
            suite.deploy()
            suite.availability()
        else:
            require(suite.state['phase'] == 'part1-passed', 'Part 2 requires a completed Part 1 run')
        if args.part != '1':
            suite.quorum_loss()
            suite.teardown()
        passed = True
    except (Exception, KeyboardInterrupt) as error:
        suite.say(f'FAIL at {suite.state.get("stage")}: {error}')
        suite.snapshot('failure')
        raise
    finally:
        retain = not args.stop and (args.keep_alive or (passed and args.part == '1'))
        if retain:
            suite.instructions()
        else:
            for node in NODES:
                with contextlib.suppress(Exception):
                    suite.stop(node, hard=True)
        suite.say(f'Artifacts retained: {suite.work}')


if __name__ == '__main__':
    main()
