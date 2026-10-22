(() => {
  const state = document.getElementById('stream-state');
  const telemetry = document.getElementById('telemetry-view');
  if (telemetry) {
    const stream = new EventSource('/api/v1/events?topics=hosts.telemetry&host_id=' + encodeURIComponent(telemetry.dataset.hostId));
    let history = [], timer, lastStream, lastSequence;
    const graph = document.getElementById('cpu-history');
    const update = sample => {
      if (!sample || sample.host_id !== telemetry.dataset.hostId || sample.schema_version !== 1) {
        state.textContent = 'Unavailable · displayed observations are stale'; return;
      }
      state.textContent = sample.freshness === 'live' ? 'Live updates connected' : 'Measurements ' + sample.freshness;
      if (sample.stream_id !== lastStream || sample.sequence !== lastSequence) {
        const at = Date.parse(sample.observed_at);
        const value = sample.freshness === 'live' ? sample.cpu?.utilization : null;
        if (Number.isFinite(at)) history.push({at, value, gap: sample.stream_id !== lastStream, interval: sample.interval_seconds * 1000});
        lastStream = sample.stream_id; lastSequence = sample.sequence;
        const end = Date.now(), start = end - 300000;
        history = history.filter(point => point.at >= start).slice(-1201);
        let path = '', previous;
        for (const point of history) {
          if (!Number.isFinite(point.value)) { previous = null; continue; }
          const x = Math.max(0, Math.min(600, (point.at - start) / 500));
          const y = 80 - Math.max(0, Math.min(1, point.value)) * 80;
          const gap = !previous || point.gap || point.at - previous.at > Math.max(3000, 3 * point.interval);
          path += (gap ? 'M' : 'L') + x.toFixed(2) + ',' + y.toFixed(2);
          previous = point;
        }
        const line = document.createElementNS('http://www.w3.org/2000/svg', 'path');
        line.setAttribute('d', path); line.setAttribute('fill', 'none'); line.setAttribute('stroke', 'currentColor'); line.setAttribute('stroke-width', '2');
        graph.replaceChildren(line);
      }
      clearTimeout(timer);
      timer = setTimeout(() => htmx.trigger('#telemetry-status', 'refresh'), 100);
    };
    stream.addEventListener('snapshot', event => { try { update(JSON.parse(event.data)['hosts.telemetry']); } catch (_) {} });
    stream.addEventListener('host.telemetry', event => { try { update(JSON.parse(event.data)); } catch (_) {} });
    stream.onerror = () => { state.textContent = 'Disconnected · displayed observations are stale'; lastStream = null; };
    window.addEventListener('pagehide', () => { clearTimeout(timer); stream.close(); });
    return;
  }
  const target = document.getElementById('task-status') ? '#task-status' : document.getElementById('cluster-status') ? '#cluster-status' : '#network-status';
  const topics = document.getElementById('cluster-status') ? 'cluster.status,cluster.requests,tasks' : 'network.status,tasks';
  const stream = new EventSource('/api/v1/events?topics=' + topics);
  let timer;
  const availability = [];
  document.body.addEventListener('htmx:afterSwap', () => availability.forEach(update => update()));
  const refresh = () => {
    clearTimeout(timer);
    timer = setTimeout(() => htmx.trigger(target, 'refresh'), 150);
  };
  stream.addEventListener('snapshot', () => {
    state.textContent = 'Live updates connected';
    refresh();
  });
  stream.addEventListener('resource.updated', refresh);
  stream.onerror = () => { state.textContent = 'Disconnected · displayed observations may be stale'; };
  document.body.addEventListener('htmx:beforeSwap', event => {
    if (event.detail.target.id === 'receipt') {
      event.detail.shouldSwap = true;
      event.detail.isError = false;
    }
  });
  for (const form of document.querySelectorAll('form[data-command]')) {
    const field = name => form.querySelector(`input[name=${name}]`);
    const button = form.querySelector('button');
    const disabled = button.disabled;
    const label = button.textContent;
    let pendingNotice;
    let pending = false;
    const inputs = [...form.querySelectorAll('input[name]')];
    const lock = () => inputs.filter(input => input.type !== 'hidden').forEach(input => { input.readOnly = true; });
    const key = ['nixmox', field('target_id').value, field('type').value, field('task_id')?.value || field('request_id')?.value || ''].join('.');
    try {
      const saved = JSON.parse(sessionStorage.getItem(key));
      if (saved && /^cmd_[a-f0-9]{32}$/.test(saved.id)) {
        pending = true;
        field('id').value = saved.id;
        if (saved.values && typeof saved.values === 'object') {
          for (const input of inputs) if (typeof saved.values[input.name] === 'string') input.value = saved.values[input.name];
        }
        if (field('expected_version') && /^[1-9][0-9]*$/.test(saved.version)) {
          field('expected_version').value = saved.version;
        }
        button.disabled = false;
        button.textContent = 'Retry pending ' + label.toLowerCase();
        pendingNotice = document.createElement('p');
        pendingNotice.textContent = 'Pending command ' + saved.id + (saved.version ? ' · reviewed version ' + saved.version : '');
        form.append(pendingNotice);
        lock();
      }
    } catch (_) {}
    const requiresUnjoined = ['cluster.create', 'cluster.join'].includes(field('type').value);
    const reason = document.createElement('p');
    if (requiresUnjoined) form.append(reason);
    const updateAvailability = () => {
      if (!requiresUnjoined) return;
      const membership = document.getElementById('cluster-status')?.dataset.membership;
      button.disabled = !pending && membership !== 'unjoined';
      reason.textContent = button.disabled ? 'Requires current status from an unjoined node.' : '';
    };
    availability.push(updateAvailability);
    updateAvailability();
    form.addEventListener('htmx:beforeRequest', () => {
      pending = true;
      const values = Object.fromEntries(inputs.map(input => [input.name, input.value]));
      try { sessionStorage.setItem(key, JSON.stringify({id: field('id').value, version: field('expected_version')?.value, values})); } catch (_) {}
      lock();
    });
    const reset = () => {
      // An uncertain response must retain the exact ID and reviewed version.
      const bytes = crypto.getRandomValues(new Uint8Array(16));
      field('id').value = 'cmd_' + Array.from(bytes, b => b.toString(16).padStart(2, '0')).join('');
      if (field('expected_version')) field('expected_version').value = field('expected_version').defaultValue;
      button.disabled = disabled;
      pending = false;
      button.textContent = label;
      pendingNotice?.remove();
      for (const input of inputs) input.readOnly = false;
      try { sessionStorage.removeItem(key); } catch (_) {}
      updateAvailability();
    };
    form.addEventListener('commandAccepted', () => {
      reset();
      refresh();
    });
    form.addEventListener('htmx:afterRequest', event => {
      const status = event.detail.xhr.status;
      if (status >= 400 && status < 500) reset();
    });
  }
  window.addEventListener('pagehide', () => { clearTimeout(timer); stream.close(); });
})();
