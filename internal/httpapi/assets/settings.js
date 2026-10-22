"use strict";
(() => {
  const $ = id => document.getElementById(id), node = $("settings").dataset.node;
  const notice = text => { $("settings-notice").textContent = text; };
	const commandID = () => "cmd_" + [...crypto.getRandomValues(new Uint8Array(16))].map(v => v.toString(16).padStart(2,"0")).join("");
  let status, credentials = [], repositories = [], busy = false;
  async function request(path, options) {
    const response = await fetch(path, options), data = await response.json();
    if (!response.ok) { const error = Error(data.error?.message || "Request unavailable; retain the original command ID."); error.status = response.status; error.code = data.error?.code; throw error; }
    return data;
  }
  function option(select, value, label) { const o = document.createElement("option"); o.value = value; o.textContent = label; select.append(o); }
  function choices(select, rows, label, empty) {
    const old = select.value; select.replaceChildren(); if (empty) option(select, "", empty);
    for (const row of rows) option(select, row.id, label(row));
    if ([...select.options].some(o => o.value === old)) select.value = old;
  }
  function task(receipt) {
    const link = document.createElement("a"); link.href = "/ui/tasks/" + encodeURIComponent(receipt.task_id); link.textContent = "Open task " + receipt.task_id;
    $("settings-task").replaceChildren(link); notice("Accepted. Task results show check scope and any missing prerequisites.");
  }
  async function command(value) {
    sessionStorage.setItem("nixmox.settings.command", JSON.stringify(value)); $("retry-settings").hidden = false;
    const receipt = await request("/api/v1/commands", {method:"POST", headers:{"Content-Type":"application/json"}, body:JSON.stringify(value)});
    sessionStorage.removeItem("nixmox.settings.command"); $("retry-settings").hidden = true; task(receipt);
  }
  function submit(type, target, payload, versions = {}) {
    return command({id:commandID(), type, target_id:target, payload, expected_versions:versions});
  }
  async function safely(action) { if (busy) return; busy = true; try { await action(); } catch (error) { notice(error.message); } finally { busy = false; } }
  function renderCredentials() {
    const list = $("credentials-list"); list.replaceChildren();
    for (const c of credentials) {
      const section = document.createElement("section"), heading = document.createElement("h3"), detail = document.createElement("pre");
      heading.textContent = c.name + " · " + c.kind;
      detail.textContent = c.replicas.map(r => `${r.node_id}: ${r.source} · ${r.state}`).join("\n") + "\nBindings: " + JSON.stringify(c.scope);
      section.append(heading, detail);
      const repository = document.createElement("select");
      if (c.kind.startsWith("git-")) { for (const r of repositories) option(repository, r.id, r.name); section.append(repository); }
      const check = document.createElement("button"); check.textContent = "Test access";
      const holder = c.replicas.find(r => r.state === "available"); check.disabled = !holder;
      check.onclick = () => safely(() => submit("credential.check", holder.node_id, {credential_id:c.id, ...(c.kind.startsWith("git-") ? {repository_id:repository.value} : {})}));
      const remove = document.createElement("button"); remove.textContent = "Remove";
      remove.disabled = !holder || c.replicas.some(r => r.source !== "managed");
      remove.onclick = () => safely(async () => { if (confirm(`Remove ${c.name}? Active bindings prevent removal.`)) await submit("credential.remove", holder.node_id, {credential_id:c.id}, {["credential:"+c.id]:c.version}); });
      section.append(check, remove); list.append(section);
    }
    const signer = $("cache-select").elements.credential;
    choices(signer, credentials.filter(c => c.kind === "nix-cache-signing-key"), c => c.name);
    choices($("repository-credential").elements.credential, credentials.filter(c => c.kind.startsWith("git-")), c => c.name, "Public remote");
  }
  async function refresh() {
    const results = await Promise.allSettled([request("/api/v1/credentials?limit=500"), request("/api/v1/hosts?limit=500"), request("/api/v1/repositories?limit=500"), request("/api/v1/cache/status")]);
    if (results[2].status === "fulfilled") { repositories = results[2].value.items; choices($("repository-credential").elements.repository, repositories, r => r.name); }
    if (results[0].status === "fulfilled") { credentials = results[0].value.items; renderCredentials(); }
    if (results[1].status === "fulfilled") choices($("credential-import").elements.node, results[1].value.items.filter(h => h.membership === "full" && h.reachability === "online"), h => `${h.name || h.id} · ${h.id}`);
    if (results[3].status === "fulfilled") {
      status = results[3].value; $("cache-status").textContent = JSON.stringify(status, null, 2);
      $("cache-select").querySelector("button").disabled = status.declared;
      $("cache-select").elements.credential.disabled = status.declared;
    }
    const failure = results.find(r => r.status === "rejected"); if (failure) notice(failure.reason.message);
  }
  $("credential-import").onsubmit = event => { event.preventDefault(); safely(async () => {
    const f = event.target.elements, file = f.file.files[0];
    if (file && file.size > 1048576) throw Error("Secret exceeds 1 MiB.");
    if (!file && !f.value.value) throw Error("Provide a hidden value or secret file.");
    const metadata = {name:f.name.value, kind:f.kind.value, storage_node_id:f.node.value};
    let previous = JSON.parse(sessionStorage.getItem("nixmox.settings.import") || "null");
    if (previous && Object.keys(metadata).some(key => previous[key] !== metadata[key])) throw Error("An import outcome is uncertain. Restore its original name, kind and node before retrying.");
    metadata.id = previous?.id || commandID();
    sessionStorage.setItem("nixmox.settings.import", JSON.stringify(metadata));
    const body = new FormData(); body.append("metadata", JSON.stringify(metadata)); body.append("value", file || f.value.value);
    try {
      const receipt = await request("/api/v1/credentials/import", {method:"POST", body});
      sessionStorage.removeItem("nixmox.settings.import"); task(receipt);
    } catch (error) {
      if (error.status >= 400 && error.status < 500 && error.code !== "COMMAND_CONFLICT") sessionStorage.removeItem("nixmox.settings.import");
      throw error;
    } finally { f.value.value = ""; f.file.value = ""; }
  }); };
  $("cache-select").onsubmit = event => { event.preventDefault(); safely(() => submit("cache.credential.set", node, {credential_id:event.target.elements.credential.value}, {["settings:cache:"+node]:status.settings_version})); };
  $("cache-locate").onsubmit = event => { event.preventDefault(); safely(async () => { $("cache-locations").textContent = JSON.stringify(await request("/api/v1/cache/locations?path="+encodeURIComponent(event.target.elements.path.value)), null, 2); }); };
  $("repository-credential").onsubmit = event => { event.preventDefault(); safely(() => {
    const f = event.target.elements, r = repositories.find(r => r.id === f.repository.value);
    return submit("repository.credential.set", node, {repository_id:r.id, credential_ref:f.credential.value || null}, {["repository:"+r.id]:r.version});
  }); };
  $("retry-settings").hidden = !sessionStorage.getItem("nixmox.settings.command");
  $("retry-settings").onclick = () => safely(() => command(JSON.parse(sessionStorage.getItem("nixmox.settings.command"))));
  const stream = new EventSource("/api/v1/events?topics=cache.status,tasks");
  stream.addEventListener("snapshot", () => { if (!busy) safely(refresh); });
  stream.onerror = () => notice("Status stream disconnected; reconnect will refresh the snapshot.");
  safely(refresh);
})();
