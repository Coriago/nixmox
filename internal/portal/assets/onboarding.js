(() => {
  "use strict";
  const $ = id => document.getElementById(id);
  let writer = "", schema = {}, session = null, timer = null, epoch = 0;
  const error = e => { $("onboarding-error").textContent = e?.message || ""; };
  async function request(path, method = "GET", body) {
    const response = await fetch(path, {method, headers: {"Content-Type": "application/json"}, body: body === undefined ? undefined : JSON.stringify(body)});
    if (!response.ok) throw new Error(await response.text());
    return response.json();
  }
  const endpoint = () => `/api/v1/hosts/${encodeURIComponent(writer)}/onboarding`;
  function invalidate() {
    epoch++;
    $("onboarding-review").hidden = true;
    $("onboarding-confirm").value = "";
  }
  function busy(value) {
    $("onboarding-writer").disabled = value;
    $("onboarding-preview-button").disabled = value;
    $("onboarding-start-button").disabled = value;
    for (const id of ["onboarding-mode", "onboarding-load-configurations", "onboarding-existing-review", "onboarding-authorize-recipient"]) $(id).disabled = value;
  }
  function renderFields() {
    const fields = $("onboarding-fields"); fields.replaceChildren();
    for (const [name, field] of Object.entries(schema)) {
      const label = document.createElement("label"); label.textContent = name + " ";
      let input;
      if (field.type === "multiselect" || field.type === "disk") {
        input = document.createElement("select");
        if (field.type === "multiselect") {
          input.multiple = true;
          for (const value of field.values || []) {
            const option = new Option(value, value);
            option.selected = (session.values?.[name] ?? field.default ?? []).includes(value);
            input.add(option);
          }
        } else {
          input.add(new Option("Select a whole drive", ""));
          for (const disk of session.info.disks) {
            if (!disk.whole || disk.installer) continue;
            const label = `${disk.path} · ${disk.model || "Unknown model"} · ${(disk.size / 1073741824).toFixed(1)} GiB · serial ${disk.serial || "unavailable"}`;
            input.add(new Option(label, disk.path));
          }
          input.value = session.values?.[name] ?? field.default ?? "";
          input.required = true;
        }
      } else {
        input = document.createElement("input"); input.type = "text";
        input.value = session.values?.[name] ?? field.default ?? "";
        if (field.type === "hostname") input.pattern = "[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?";
        if (field.type === "username") input.pattern = "[a-z_][a-z0-9_-]{0,31}";
      }
      input.name = name; input.required ||= !!field.required;
      input.addEventListener("input", invalidate);
      input.addEventListener("change", invalidate);
      label.append(input); fields.append(label);
    }
    $("onboarding-target").textContent = `${session.node_id} · ${session.address}`;
    $("onboarding-network").textContent = session.info.network.message;
    $("onboarding-recipient").textContent = "Installer public age recipient: " + session.info.recipient;
    $("onboarding-configure").hidden = false;
    const drives = $("onboarding-existing-drive"); drives.replaceChildren(new Option("Select a whole drive", ""));
    for (const disk of session.info.disks) {
      if (disk.whole && !disk.installer) drives.add(new Option(`${disk.path} · ${disk.model || "Unknown model"} · serial ${disk.serial || "unavailable"}`, disk.path));
    }
    drives.value = session.plan?.drive?.path || "";
    $("onboarding-revision").value = session.revision || "";
    $("onboarding-configuration").replaceChildren(new Option("Load configurations first", ""));
    $("onboarding-authorize-recipient").checked = !!session.authorize_recipient;
    $("onboarding-mode").value = session.existing ? "existing" : "generate";
    showMode();
  }
  function showMode() {
    const existing = $("onboarding-mode").value === "existing";
    $("onboarding-existing").hidden = !existing;
    $("onboarding-form").hidden = existing;
  }
  $("onboarding-mode").onchange = () => { invalidate(); showMode(); };
  $("onboarding-revision").oninput = () => {
    invalidate(); $("onboarding-configuration").replaceChildren(new Option("Load configurations first", ""));
  };
  for (const id of ["onboarding-configuration", "onboarding-existing-drive", "onboarding-authorize-recipient"]) $(id).onchange = invalidate;
  $("onboarding-load-configurations").onclick = async () => {
    error(null); invalidate(); busy(true);
    const current = epoch;
    try {
      const result = await request(endpoint() + "/configurations", "POST", {revision: $("onboarding-revision").value.trim()});
      if (current !== epoch) return;
      $("onboarding-revision").value = result.revision;
      const list = $("onboarding-configuration"); list.replaceChildren(new Option("Select a configuration", ""));
      for (const name of result.configurations) list.add(new Option(name, name));
      if (result.configurations.includes(session.plan?.hostname)) list.value = session.plan.hostname;
    } catch(e) { error(e); } finally { busy(false); }
  };
  $("onboarding-existing").onsubmit = async event => {
    event.preventDefault(); error(null); invalidate(); busy(true);
    const current = epoch;
    try {
      const result = await request(endpoint() + "/select", "POST", {session: session.id, revision: $("onboarding-revision").value.trim(), hostname: $("onboarding-configuration").value, drive: $("onboarding-existing-drive").value, authorize_recipient: $("onboarding-authorize-recipient").checked});
      if (current !== epoch) return;
      session = result; review();
    } catch(e) { error(e); } finally { busy(false); }
  };
  async function selectInstaller(peer) {
    error(null); invalidate(); busy(true);
    const current = ++epoch;
    try {
      const result = await request(endpoint() + "/inspect", "POST", {node_id: peer.node_id, address: peer.address});
      if (current !== epoch) return;
      session = result; renderFields(); $("onboarding-configure").scrollIntoView({behavior: "smooth"});
    } catch (e) {error(e);} finally {busy(false);}
  }
  async function refresh() {
    if (!writer) return;
    const current = epoch;
    try {
      const [peers, sessions] = await Promise.all([request("/api/v1/discovery"), request(endpoint() + "/sessions")]);
      if (current !== epoch) return;
      const list = $("onboarding-installers"); list.replaceChildren();
      const completed = new Set(sessions.filter(s => s.completed).map(s => s.node_id));
      for (const peer of peers.filter(p => !p.cluster && p.installer && !completed.has(p.node_id))) {
        const row = document.createElement("p"); row.textContent = `${peer.node_id} · ${peer.address} `;
        const button = document.createElement("button"); button.textContent = "Set up host";
        button.onclick = () => selectInstaller(peer); row.append(button); list.append(row);
      }
      if (!list.childElementCount) list.textContent = "No live installers found. Boot the nixmox installer and enable discovery.";
      const history = $("onboarding-sessions"); history.replaceChildren();
      for (const saved of sessions) {
        const row = document.createElement("p"); row.textContent = `${saved.plan?.hostname || saved.node_id} · ${saved.phase} `;
        if (saved.completed) {
          const link = document.createElement("a"); link.href = "/hosts/" + encodeURIComponent(saved.plan.hostname); link.textContent = "Open installed host"; row.append(link);
        } else {
          const button = document.createElement("button"); button.textContent = "Open setup";
          button.onclick = () => {epoch++; session = saved; renderFields(); if (session.digest) review(); if (session.job_id) monitor();}; row.append(button);
        }
        history.append(row);
      }
      if (!history.childElementCount) history.textContent = "No setup sessions yet.";
      const params = new URLSearchParams(location.search);
      if (!session && params.has("node")) {
        const peer = peers.find(p => p.node_id === params.get("node") && p.address === params.get("address"));
        if (peer) await selectInstaller(peer);
        window.history.replaceState(null, "", "/add-host");
      }
    } catch (e) {error(e);}
  }
  function review() {
    $("onboarding-path").textContent = session.result.path;
    $("onboarding-preview").textContent = session.result.content;
    $("onboarding-review").hidden = false;
    $("onboarding-confirm").value = "";
    $("onboarding-start-button").textContent = session.installed ? "Resume reboot and join checks" : session.authorize_recipient ? "Authorize key, push, and install — erase drive" : session.existing ? "Install selected revision — erase drive" : session.revision ? "Retry recorded revision and erase / install" : "Push configuration and erase / install";
    $("onboarding-review-action").textContent = session.authorize_recipient ? `Authorize ${session.info.recipient} in the reviewed SOPS files, commit and push, then install ${session.plan.hostname}. Existing recipients and host configuration are preserved. The confirmed drive will be erased.` : session.revision ? `Install ${session.plan.hostname} at commit ${session.revision}. No Git changes or push. The installer will erase the confirmed drive, install, and reboot.` : "The configuration and SOPS recipient updates will be committed and pushed together. The installer will pull that commit, erase the selected drive, install, and reboot.";
    $("onboarding-form").querySelectorAll("input,select,button").forEach(el => {el.disabled = !!session.revision;});
  }
  $("onboarding-form").onsubmit = async event => {
    event.preventDefault(); error(null); busy(true); invalidate();
    const current = epoch;
    const values = {}, data = new FormData(event.target);
    for (const [name, field] of Object.entries(schema)) values[name] = field.type === "multiselect" ? data.getAll(name) : data.get(name);
    try {
      const result = await request(endpoint() + "/preview", "POST", {session: session.id, values});
      if (current !== epoch) return;
      session = result; review();
    } catch(e) {error(e);} finally {busy(false);}
  };
  $("onboarding-start").onsubmit = async event => {
    event.preventDefault(); error(null);
    if (!session || $("onboarding-confirm").value !== session.plan.drive.path) {error(new Error("Type the exact selected drive path to confirm erasure.")); return;}
    const confirmation = session.installed ? `Resume reboot and cluster join checks for ${session.plan.hostname}?` : `Erase ${session.plan.drive.path} on ${session.node_id} and install ${session.plan.hostname}? All data on that drive will be lost.`;
    if (!confirm(confirmation)) return;
    busy(true);
    try {
      const result = await request(endpoint() + "/start", "POST", {session: session.id, digest: session.digest, confirm: $("onboarding-confirm").value, alternative_network: $("onboarding-network-plan").checked});
      session = result.session; invalidate(); monitor();
    } catch(e) {error(e); busy(false);}
  };
  async function monitor() {
    clearTimeout(timer);
    const current = epoch, id = session.id;
    $("onboarding-progress").hidden = false;
    try {
      const result = await request(endpoint() + "/sessions/" + encodeURIComponent(id));
      if (current !== epoch || session.id !== id) return;
      session = result.session;
      $("onboarding-phase").textContent = session.phase + (result.job.error ? ": " + result.job.error : "");
      $("onboarding-log").textContent = result.log;
      $("onboarding-task").textContent = "View task " + result.job.id;
      $("onboarding-task").href = `/hosts/${encodeURIComponent(writer)}/jobs/${encodeURIComponent(result.job.id)}`;
      if (result.job.state === "failed") {busy(false); review(); error(new Error(result.job.error)); await refresh();}
      else if (session.completed) {busy(false); invalidate(); $("onboarding-configure").hidden = true; await refresh();}
      else {busy(true); timer = setTimeout(monitor, 1500);}
    } catch(e) {error(e); timer = setTimeout(monitor, 3000);}
  }
  async function chooseWriter() {
    epoch++; clearTimeout(timer); session = null; invalidate(); error(null);
    $("onboarding-configure").hidden = true; $("onboarding-progress").hidden = true;
    writer = $("onboarding-writer").value;
    if (!writer) return;
    try {
      const config = await request(endpoint()); schema = config.form;
      $("onboarding-repository").textContent = `${config.repository} · ${config.branch} · ${config.sops ? "SOPS recipients will be updated" : "No SOPS files configured"}`;
      await refresh();
    } catch(e) {error(e);}
  }
  $("onboarding-writer").onchange = chooseWriter;
  $("onboarding-refresh").onclick = refresh;
  async function init() {
    try {
      const candidates = await request("/api/v1/onboarding/coordinators");
      const select = $("onboarding-writer");
      for (const candidate of candidates) {
        const option = new Option(candidate.node_id + (candidate.ready ? "" : " — " + candidate.error), candidate.node_id);
        option.disabled = !candidate.ready; select.add(option);
      }
      const ready = candidates.find(c => c.ready);
      if (!ready) throw new Error("No cluster node is ready to prepare hosts. Configure services.nixmox.addHost, Git write access, and an authorized ageKeyFile when using SOPS.");
      select.value = ready.node_id; await chooseWriter();
    } catch(e) {error(e); $("onboarding-installers").textContent = "Repository preparation must be configured first.";}
  }
  window.addEventListener("pagehide", () => {epoch++; clearTimeout(timer);});
  init();
})();
