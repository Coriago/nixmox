(() => {
  const state = document.getElementById('stream-state');
  const target = document.getElementById('task-status') ? '#task-status' : '#network-status';
  const stream = new EventSource('/api/v1/events?topics=network.status,tasks');
  let timer;
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
    const key = ['nixmox', field('target_id').value, field('type').value, field('task_id')?.value || ''].join('.');
    try {
      const saved = JSON.parse(sessionStorage.getItem(key));
      if (saved && /^cmd_[a-f0-9]{32}$/.test(saved.id)) {
        field('id').value = saved.id;
        if (field('expected_version') && /^[1-9][0-9]*$/.test(saved.version)) {
          field('expected_version').value = saved.version;
        }
        button.disabled = false;
        button.textContent = 'Retry pending ' + label.toLowerCase();
        pendingNotice = document.createElement('p');
        pendingNotice.textContent = 'Pending command ' + saved.id + (saved.version ? ' · reviewed version ' + saved.version : '');
        form.append(pendingNotice);
      }
    } catch (_) {}
    form.addEventListener('htmx:beforeRequest', () => {
      try { sessionStorage.setItem(key, JSON.stringify({id: field('id').value, version: field('expected_version')?.value})); } catch (_) {}
    });
    const reset = () => {
      // An uncertain response must retain the exact ID and reviewed version.
      const bytes = crypto.getRandomValues(new Uint8Array(16));
      field('id').value = 'cmd_' + Array.from(bytes, b => b.toString(16).padStart(2, '0')).join('');
      if (field('expected_version')) field('expected_version').value = field('expected_version').defaultValue;
      button.disabled = disabled;
      button.textContent = label;
      pendingNotice?.remove();
      try { sessionStorage.removeItem(key); } catch (_) {}
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
