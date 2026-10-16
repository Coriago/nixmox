(() => {
  "use strict";
  const $ = (s) => document.querySelector(s);
  document.addEventListener("click", async (event) => {
    const button = event.target.closest("[data-copy-age]");
    if (!button) return;
    const section = button.closest(".age-recipient");
    const input = section.querySelector("[data-age-recipient]");
    const status = section.querySelector("[data-copy-status]");
    try {
      if (!navigator.clipboard) throw new Error("Clipboard unavailable");
      await navigator.clipboard.writeText(input.value);
      status.textContent = " Copied";
    } catch (_) {
      input.focus(); input.select();
      status.textContent = " Selected — press Ctrl+C or ⌘C to copy.";
    }
  });
  const uptimeClocks = new Map();
  let live = false;
  function uptimeLabel(value) {
    let seconds = Math.max(0, Math.floor(value));
    const days = Math.floor(seconds / 86400); seconds %= 86400;
    const hours = Math.floor(seconds / 3600); seconds %= 3600;
    const minutes = Math.floor(seconds / 60); seconds %= 60;
    return (days ? days + "d " : "") + (hours || days ? hours + "h " : "") +
      (minutes || hours || days ? minutes + "m " : "") + seconds + "s";
  }
  function renderUptimes(tick = false) {
    const now = performance.now();
    document.querySelectorAll("[data-uptime-host]").forEach((el) => {
      const sample = Number(el.dataset.uptime), online = el.dataset.online.trim() === "true";
      if (!Number.isFinite(sample)) return;
      let clock = uptimeClocks.get(el.dataset.uptimeHost);
      if (!clock) {
        clock = { sample, online, base: sample, at: now, display: sample };
        uptimeClocks.set(el.dataset.uptimeHost, clock);
      } else if (sample !== clock.sample || online !== clock.online) {
        const predicted = clock.base + (now - clock.at) / 1000;
        const reboot = sample < clock.sample - 2;
        if (!online || online !== clock.online || reboot || Math.abs(sample - predicted) > 3) {
          clock.base = sample; clock.at = now;
          if (!online || reboot) clock.display = sample;
        }
        clock.sample = sample; clock.online = online;
      }
      if (tick && online && live) clock.display = clock.base + (now - clock.at) / 1000;
      // Replacing a fragment restores the previous display, so a burst of
      // deployment events cannot change the one-second visual cadence.
      el.textContent = uptimeLabel(clock.display);
      el.title = !online ? "Last reported uptime; host offline" :
        live ? "Estimated between host reports" : "Last displayed uptime; reconnecting";
    });
  }
  renderUptimes();
  const uptimeTimer = setInterval(() => renderUptimes(true), 1000);
  const notice = (message, error = false) => {
    const n = $("#notice");
    n.textContent = message;
    n.classList.toggle("error", error);
  };
  const pendingCommands = new Map();
  function pendingID(key) {
    if (!pendingCommands.has(key)) pendingCommands.set(key, commandID());
    return pendingCommands.get(key);
  }
  async function request(path, method = "GET", body) {
    const key = JSON.stringify([method, path, body]);
    const response = await fetch(path, {
      method,
      headers: {
        ...(body === undefined ? {} : { "Content-Type": "application/json" }),
        ...(method === "GET" ? {} : { "Idempotency-Key": pendingID(key) }),
      },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    const text = await response.text();
    if (response.headers.get("X-Nixmox-Command-State") !== "unknown") pendingCommands.delete(key);
    if (!response.ok) throw new Error(text || response.statusText);
    try {
      return JSON.parse(text);
    } catch {
      return text;
    }
  }
  function commandID() {
    return "ui-" + Array.from(crypto.getRandomValues(new Uint8Array(16)),
      (b) => b.toString(16).padStart(2, "0")).join("");
  }
  document.body.addEventListener("htmx:configRequest", (e) => {
    if (e.detail.verb !== "get") {
      const key = JSON.stringify([e.detail.verb, e.detail.path, e.detail.parameters]);
      e.detail.headers["Idempotency-Key"] = pendingID(key);
    }
  });
  document
    .querySelectorAll("nav a")
    .forEach((a) =>
      a.classList.toggle("active", a.pathname === location.pathname),
    );
  document.body.addEventListener("htmx:afterRequest", (e) => {
    const xhr = e.detail.xhr;
    const id = e.detail.requestConfig.headers["Idempotency-Key"];
    if (xhr.status && xhr.getResponseHeader("X-Nixmox-Command-State") !== "unknown") {
      for (const [key, value] of pendingCommands) if (value === id) pendingCommands.delete(key);
    }
    if (!xhr.status) notice("Connection lost. Command outcome is unknown; retrying the same action will reuse its command ID.", true);
    else if (xhr.status >= 400) notice(xhr.responseText, true);
    else if (e.detail.requestConfig.verb !== "get") {
      try {
        const j = JSON.parse(e.detail.xhr.responseText);
        const match = e.detail.requestConfig.path.match(/hosts\/([^/]+)\//);
        if (j.id) {
          taskNotice(
            j,
            match ? decodeURIComponent(match[1]) : document.body.dataset.host,
          );
          return;
        }
      } catch {}
      notice("Request accepted. See Tasks for progress.");
    }
  });
  const fragments = [...document.querySelectorAll("[data-live]")];
  let connectionIsLive;
  let stream, lastEvent = performance.now();
  function connectionState(connected) {
    live = connected;
    if (connectionIsLive === connected) return;
    connectionIsLive = connected;
    $("#connection").textContent = connected ? "● Live" : "Disconnected · reconnecting…";
    $("#connection").className = connected ? "badge online" : "badge warn";
    $("#connection-warning").hidden = connected;
    if (connected) return;
    $("#connection-message").textContent = "Lost live connection to " + document.body.dataset.host;
    const alternatives = $("#alternate-hosts");
    alternatives.replaceChildren();
    document.querySelectorAll(".host-nav-item[data-console-address]").forEach((host) => {
      if (host.dataset.node === document.body.dataset.host || host.dataset.online.trim() !== "true" || !host.dataset.consoleAddress) return;
      try {
        const url = new URL("http://" + host.dataset.consoleAddress);
        if (url.username || url.password || url.pathname !== "/") return;
        const a = document.createElement("a");
        a.href = url.href;
        a.textContent = "Open " + host.dataset.node + " console";
        a.rel = "noopener noreferrer";
        alternatives.append(a);
      } catch {}
    });
    if (alternatives.children.length) {
      const hint = document.createElement("p");
      hint.className = "hint";
      hint.textContent = "Other hosts were online at last contact. Their advertised addresses must be reachable from your browser; sign-in may be required.";
      alternatives.prepend(hint);
    }
    renderUptimes();
  }
  function received() { lastEvent = performance.now(); connectionState(true); }
  const connectionTimer = setInterval(() => {
    if (performance.now() - lastEvent > 20000) {
      connectionState(false);
      connect();
    }
  }, 1000);
  $("#reconnect").onclick = connect;
  addEventListener("offline", () => { stream?.close(); connectionState(false); });
  addEventListener("online", connect);
  function connect() {
    stream?.close();
    if (!navigator.onLine) { connectionState(false); return; }
    const query = new URLSearchParams();
    fragments.forEach((el) => query.append("partial", el.dataset.live));
    const log = $("[data-log]");
    if (log) query.set("log", log.dataset.log);
    stream = new EventSource("/api/v1/events?" + query);
    lastEvent = performance.now();
    stream.onopen = received;
    stream.onerror = () => connectionState(false);
    stream.addEventListener("heartbeat", received);
    let logOffset = -1, decoder = new TextDecoder();
    stream.addEventListener("log-error", (event) => {
      received();
      if ($("#log-status")) $("#log-status").textContent = JSON.parse(event.data);
    });
    stream.addEventListener("log", (event) => {
      received();
      if (!log) return;
      const chunk = JSON.parse(event.data);
      if (!chunk.reset && chunk.next <= logOffset) return;
      if (!chunk.reset && logOffset >= 0 && chunk.offset !== logOffset) {
        connect(); return;
      }
      const bottom = log.scrollHeight - log.scrollTop - log.clientHeight < 24;
      if (chunk.reset || logOffset < 0) { log.textContent = ""; decoder = new TextDecoder(); }
      const bytes = Uint8Array.from(atob(chunk.data || ""), (c) => c.charCodeAt(0));
      const text = decoder.decode(bytes, {stream:true});
      log.textContent = (log.textContent + text).slice(-256000);
      logOffset = chunk.next;
      if (bottom) log.scrollTop = log.scrollHeight;
      $("#log-status").textContent = "Live output";
    });
    stream.addEventListener("fragment-errors", (event) => {
      received();
      const errors = JSON.parse(event.data);
      fragments.forEach((el) => {
        if (errors[el.dataset.live]) {
          el.textContent = "Live data unavailable: " + errors[el.dataset.live];
        }
      });
    });
    stream.addEventListener("fragments", (event) => {
      received();
      const changes = JSON.parse(event.data);
      fragments.forEach((el) => {
        if (Object.hasOwn(changes, el.dataset.live)) {
          const logs = [...el.querySelectorAll("pre")].map((p) => ({
            top: p.scrollTop,
            bottom: p.scrollHeight - p.scrollTop - p.clientHeight < 24,
          }));
          el.innerHTML = changes[el.dataset.live];
          el.querySelectorAll("pre").forEach((p, i) => {
            if (logs[i])
              p.scrollTop = logs[i].bottom ? p.scrollHeight : logs[i].top;
          });
          htmx.process(el);
        }
      });
      renderUptimes();
    });
  }
  connect();
  addEventListener("pagehide", () => { stream.close(); clearInterval(uptimeTimer); clearInterval(connectionTimer); });
  document.querySelectorAll("[data-api]").forEach((form) =>
    form.addEventListener("submit", async (e) => {
      e.preventDefault();
      if (form.dataset.confirm && !confirm(form.dataset.confirm)) return;
      const body = Object.fromEntries(new FormData(form));
      form
        .querySelectorAll("input[type=number]")
        .forEach((i) => (body[i.name] = Number(i.value)));
      const button = form.querySelector("button");
      button.disabled = true;
      try {
        const result = await request(form.dataset.api, "POST", body);
        if (result.id) taskNotice(result, document.body.dataset.host);
        else notice("Rollout started.");
      } catch (error) {
        notice(error.message, true);
      } finally {
        button.disabled = false;
      }
    }),
  );
  document.querySelectorAll("[data-fetch]").forEach(async (el) => {
    try {
      el.textContent = JSON.stringify(await request(el.dataset.fetch), null, 2);
    } catch (e) {
      el.textContent = e.message;
    }
  });
  const host = encodeURIComponent(document.body.dataset.host);
  if ($("#variables-form")) {
    const path = "/api/v1/hosts/" + host + "/variables";
    const syncSlider = () => {
      try {
        const v = JSON.parse($("#variables").value).max_fan_speed;
        if (typeof v === "number") {
          $("#fan").value = v;
          $("#fan-value").textContent = v + "%";
        }
      } catch {}
    };
    request(path)
      .then((v) => {
        $("#variables").value = JSON.stringify(v, null, 2);
        syncSlider();
      })
      .catch((e) => notice(e.message, true));
    $("#variables").addEventListener("input", syncSlider);
    $("#fan").addEventListener("input", () => {
      try {
        const v = JSON.parse($("#variables").value);
        v.max_fan_speed = Number($("#fan").value);
        $("#variables").value = JSON.stringify(v, null, 2);
        syncSlider();
      } catch {
        notice("Correct the JSON before using the slider.", true);
      }
    });
    $("#variables-form").addEventListener("submit", async (e) => {
      e.preventDefault();
      try {
        await request(path, "PUT", JSON.parse($("#variables").value));
        notice("Inputs saved. Apply to rebuild with these values.");
      } catch (error) {
        notice(error.message, true);
      }
    });
  }
  const taskHost = $("#task-host");
  if (taskHost) {
    const local = document.body.dataset.host;
    const addHost = (id, online = true) => {
      if (!id || [...taskHost.options].some((o) => o.value === id)) return;
      const option = new Option(id + (online ? "" : " (offline)"), id);
      option.disabled = !online;
      taskHost.add(option);
    };
    addHost(local);
    request("/api/v1/hosts")
      .then((hosts) =>
        hosts.forEach((h) =>
          addHost(h.node_id || h.hostname, h.online !== false),
        ),
      )
      .catch((e) => notice(e.message, true));
    const taskPath = () =>
      "/api/v1/hosts/" + encodeURIComponent(taskHost.value) + "/jobs";
    const showJob = (j) => {
      location.href =
        "/hosts/" +
        encodeURIComponent(taskHost.value) +
        "/jobs/" +
        encodeURIComponent(j.id);
    };
    $("#task-history").onclick = () => {
      location.href = "/hosts/" + encodeURIComponent(taskHost.value) + "/jobs";
    };
    $("#dry-build").onclick = async () => {
      try {
        showJob(await request(taskPath(), "POST", { kind: "dry-build" }));
      } catch (e) {
        notice(e.message, true);
      }
    };
    $("#custom-task").onsubmit = async (e) => {
      e.preventDefault();
      const button = e.target.querySelector("button");
      button.disabled = true;
      try {
        const body = Object.fromEntries(new FormData(e.target));
        body.kind = "command";
        body.args = JSON.parse(body.args);
        if (
          !Array.isArray(body.args) ||
          body.args.some((a) => typeof a !== "string")
        )
          throw new Error("Arguments must be a JSON array of strings");
        showJob(await request(taskPath(), "POST", body));
      } catch (e) {
        notice(e.message, true);
      } finally {
        button.disabled = false;
      }
    };
  }
  let lastDiscovery;
  async function discovery() {
    const list = $("#discovered");
    if (!list) return;
    try {
      const [peers, state] = await Promise.all([
        request("/api/v1/discovery"),
        request("/api/v1/bootstrap"),
      ]);
      const signature = JSON.stringify([
        peers.map(({ node_id, address, cluster }) => ({
          node_id,
          address,
          cluster,
        })),
        state.status,
      ]);
      if (signature === lastDiscovery) return;
      lastDiscovery = signature;
      list.replaceChildren();
      if (!peers.length)
        list.textContent =
          "No advertisements. Enable discovery on both nodes and allow UDP 8099.";
      peers.forEach((peer) => {
        const row = document.createElement("p");
        row.textContent = `${peer.node_id} · ${peer.address} · ${peer.cluster || "unjoined"} `;
        if (!peer.cluster && state.status === "clustered") {
          const add = document.createElement("button");
          add.textContent = "Add to this cluster";
          add.onclick = async () => {
            if (
              !confirm(
                `Add ${peer.node_id} at ${peer.address} to this cluster? Verify this address first.`,
              )
            )
              return;
            try {
              await request("/api/v1/cluster/add", "POST", {
                node_id: peer.node_id,
                address: peer.address,
                confirm: peer.node_id,
              });
              location.href = "/";
            } catch (e) {
              notice(e.message, true);
            }
          };
          row.append(add);
        } else if (peer.cluster && state.status === "unclustered") {
          const join = document.createElement("button");
          join.textContent = "Join this cluster";
          join.onclick = async () => {
            if (
              !confirm(
                `Join cluster ${peer.cluster} at ${peer.address}? Verify this address first.`,
              )
            )
              return;
            try {
              await request("/api/v1/cluster/join/initiate", "POST", {
                seed: peer.address,
              });
              location.href = "/";
            } catch (e) {
              notice(e.message, true);
            }
          };
          row.append(join);
        }
        if (!peer.cluster && $("#provision")) {
          const install = document.createElement("button");
          install.textContent = "Prepare installation";
          install.onclick = () => {
            const address = new URL("http://" + peer.address).hostname;
            $("#provision input[name=target]").value = "root@" + address;
            $("#provision").scrollIntoView({ behavior: "smooth" });
            $("#provision input[name=flake]").focus();
          };
          row.append(install);
        }
        list.append(row);
      });
    } catch (e) {
      notice(e.message, true);
    }
  }
  $("#discover-refresh")?.addEventListener("click", discovery);
  discovery();
  $("#preview-button")?.addEventListener("click", async () => {
    try {
      const p = await request("/api/v1/gitops/preview");
      $("#preview").textContent =
        `${p.base} → ${p.target}\n\n${p.commits}\n\n${p.files}\n\n${p.summary}`;
    } catch (e) {
      notice(e.message, true);
    }
  });

  function taskNotice(j, host) {
    notice("Task queued. ");
    const a = document.createElement("a");
    a.href =
      "/hosts/" +
      encodeURIComponent(host) +
      "/jobs/" +
      encodeURIComponent(j.id);
    a.textContent = "Watch progress →";
    $("#notice").append(a);
  }
  document.addEventListener("click", async (e) => {
    const b = e.target.closest("[data-host-action]");
    if (!b) return;
    b.disabled = true;
    try {
      const host = b.dataset.node,
        base = "/api/v1/hosts/" + encodeURIComponent(host);
      const j = await request(
        base + (b.dataset.hostAction === "preview" ? "/jobs" : "/actions/pull"),
        "POST",
        b.dataset.hostAction === "preview" ? { kind: "preview" } : {},
      );
      taskNotice(j, host);
    } catch (e) {
      notice(e.message, true);
    } finally {
      b.disabled = false;
    }
  });
  const historyHost = $("#history-host");
  if (historyHost) {
    const match = location.pathname.match(/^\/hosts\/([^/]+)\/jobs/);
    const selected = match ? decodeURIComponent(match[1]) : "";
    const add = (id, online = true) => {
      if (!id || [...historyHost.options].some((o) => o.value === id)) return;
      historyHost.add(new Option(id + (online ? "" : " (offline)"), id));
    };
    add(document.body.dataset.host);
    add(selected);
    historyHost.value = selected;
    const filter = () => {
      const id = historyHost.value;
      $("#jobs-table").dataset.live = id
        ? "/partials/hosts/" + encodeURIComponent(id) + "/jobs"
        : "/partials/jobs";
      $("#jobs-table").innerHTML =
        '<tr><td colspan="5">Loading tasks…</td></tr>';
      if (id && taskHost) {
        taskHost.value = id;
      }
      connect();
    };
    filter();
    historyHost.onchange = filter;
    request("/api/v1/hosts")
      .then((hosts) =>
        hosts.forEach((h) => add(h.node_id || h.hostname, h.online !== false)),
      )
      .catch((e) => notice(e.message, true));
  }
  let mediaState;
  async function refreshMedia() {
    if (!$("#media-status")) return;
    try {
      mediaState = await request("/api/v1/media");
      $("#media-status").textContent = mediaState.image
        ? "Image ready: " + mediaState.image
        : (mediaState.image_error || "Prepare an installer image first.") +
          " Source: " +
          mediaState.flake;
      $("#media-build").disabled = !!mediaState.active;
      $("#media-task").replaceChildren();
      if (mediaState.active) {
        const a = document.createElement("a");
        a.href =
          "/hosts/" +
          encodeURIComponent(mediaState.host) +
          "/jobs/" +
          encodeURIComponent(mediaState.active.id);
        a.textContent =
          (mediaState.active.phase || "Installer task queued") +
          " · Watch progress →";
        $("#media-task").append(a);
      }
      $("#media-write-button").disabled =
        !mediaState.image || !!mediaState.active;
      const select = $("#media-device"),
        previous = select.value;
      if (document.activeElement === select) return;
      select.replaceChildren(new Option("Choose a USB drive…", ""));
      mediaState.devices.forEach((d) => {
        const o = new Option(
          `${d.path} · ${d.model} · ${d.serial} · ${(d.size / 2 ** 30).toFixed(1)} GiB${d.reason ? " · " + d.reason : ""}`,
          d.identity,
        );
        o.disabled = !!d.reason;
        select.add(o);
      });
      if ([...select.options].some((o) => o.value === previous && !o.disabled))
        select.value = previous;
    } catch (e) {
      $("#media-status").textContent = e.message;
      $("#media-write-button").disabled = true;
    }
  }
  $("#media-refresh")?.addEventListener("click", refreshMedia);
  $("#media-build")?.addEventListener("click", async (e) => {
    e.target.disabled = true;
    try {
      taskNotice(
        await request("/api/v1/media/build", "POST", {
          ssh_key: $("#media-ssh-key").value,
        }),
        document.body.dataset.host,
      );
    } catch (e) {
      notice(e.message, true);
    } finally {
      e.target.disabled = false;
    }
  });
  $("#media-write")?.addEventListener("submit", async (e) => {
    e.preventDefault();
    const d = mediaState?.devices.find(
      (d) => d.identity === $("#media-device").value,
    );
    if (!d) {
      notice("Select an eligible USB drive.", true);
      return;
    }
    if ($("#media-confirm").value !== d.path) {
      notice("Type the exact device path to confirm erasure.", true);
      return;
    }
    if (
      !confirm(`Erase ALL data on ${d.path}, ${d.model}, serial ${d.serial}?`)
    )
      return;
    $("#media-write-button").disabled = true;
    try {
      const j = await request("/api/v1/media/write", "POST", {
        device: d.path,
        identity: d.identity,
        confirm: $("#media-confirm").value,
      });
      location.href =
        "/hosts/" +
        encodeURIComponent(document.body.dataset.host) +
        "/jobs/" +
        encodeURIComponent(j.id);
    } catch (e) {
      notice(e.message, true);
      refreshMedia();
    }
  });
  refreshMedia();
  const discoveryTimer = setInterval(() => {
    if (!document.hidden) discovery();
  }, 10000);
  const mediaTimer = setInterval(() => {
    if (!document.hidden) refreshMedia();
  }, 10000);
  addEventListener("pagehide", () => {
    clearInterval(discoveryTimer);
    clearInterval(mediaTimer);
  });
})();
