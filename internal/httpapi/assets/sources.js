/* The editor owns its buffers. No event-stream refresh replaces this component. */
(() => {
  "use strict";
  const $ = id => document.getElementById(id);
  const root = $("sources");
  if (!root) return;
  let repository, bundle, draft, files = [], values = {}, busy = false, generation = 0;
  const edited = new Set(), regenerated = new Map(), resolved = new Set();
  const notice = message => { $("source-notice").textContent = message; };
	$("adopt-host").onsubmit = event => { event.preventDefault(); run(async () => { const h = await selectedHost(); const form = event.target.elements; const result = await command("host.adopt", {host_id:h.id, candidate_id:form.candidate_id.value, confirm_host:form.confirm_host.value}, {["host:"+h.id]:h.version}, h.id); $("candidate-result").textContent = JSON.stringify(result,null,2); }); };
  const error = e => notice(e.message || String(e));
  async function request(path, body) {
    const response = await fetch("/api/v1" + path, {method: body ? "POST" : "GET", headers: body ? {"Content-Type": "application/json"} : {}, body: body ? JSON.stringify(body) : undefined, signal: AbortSignal.timeout(35000)});
    const result = await response.json();
    if (!response.ok) throw new Error(result.error?.code + ": " + result.error?.message);
    return result;
  }
  const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
  async function command(type, payload, expected = {}, target = root.dataset.node) {
    const random = [...crypto.getRandomValues(new Uint8Array(16))].map(v => v.toString(16).padStart(2, "0")).join("");
    const envelope = {id: "cmd_" + random, type, target_id: target, expected_versions: expected, payload};
    // Persist before sending. An uncertain response retains the exact envelope.
    const key = "nixmox.command." + envelope.id;
    sessionStorage.setItem(key, JSON.stringify(envelope));
    const receipt = await request("/commands", envelope);
    sessionStorage.removeItem(key);
    notice("Task " + receipt.task_id + " · " + type);
    const deadline = Date.now() + 190000;
    while (Date.now() < deadline) {
      const task = await request("/tasks/" + receipt.task_id);
      if (task.progress?.downloads && task.progress?.builds) { const p=task.progress; $("build-progress").textContent = task.phase + " · " + p.freshness + "\nDownloads: " + p.downloads.bytes_received + " / " + (p.downloads.bytes_total ?? "unknown") + " bytes\nBuilds: " + p.builds.completed_derivations + " / " + (p.builds.total_derivations ?? "unknown") + " known · " + p.builds.active_derivations + " active"; }
      if (task.state === "succeeded") return task.result;
      if (["failed", "cancelled"].includes(task.state) || task.state === "blocked" && !task.recheck_at && !task.waiting_for?.length) throw new Error(task.error?.message || "Task " + task.state + ": " + receipt.task_id);
      await pause(300);
    }
    throw new Error("Task is still running. Follow it on the Tasks page: " + receipt.task_id);
  }
  function button(label, action) { const b = document.createElement("button"); b.type = "button"; b.textContent = label; b.addEventListener("click", () => run(action)); return b; }
  async function run(action) {
    if (busy) return;
    busy = true;
    for (const b of root.querySelectorAll("button")) b.disabled = true;
    try { await action(); } catch (e) { error(e); } finally { busy = false; for (const b of root.querySelectorAll("button")) b.disabled = false; }
  }
  async function repositories() {
    const list = $("repository-list"); list.replaceChildren();
    let cursor = "";
    do {
      const page = await request("/repositories?limit=500&cursor=" + encodeURIComponent(cursor));
      for (const r of page.items) { const row = document.createElement("p"); row.append(button(r.name, () => openRepository(r.id)), document.createTextNode(" · " + r.remote.url)); list.append(row); }
      cursor = page.next_cursor;
    } while (cursor);
  }
  async function openRepository(id) {
    repository = await request("/repositories/" + id);
    $("repository-detail").hidden = false;
    $("repository-name").textContent = repository.name;
    $("repository-pin").textContent = repository.upstream.branch + " · " + repository.upstream.commit + " · fetched " + (repository.upstream.fetched_at || "unknown");
    $("reference-list").replaceChildren();
    for (const ref of repository.refs) { const row = document.createElement("p"); row.textContent = ref.name + " · " + ref.head + " · ahead " + ref.ahead + " / behind " + ref.behind; $("reference-list").append(row); }
    const catalog = await request("/configurations?repository_id=" + id);
    $("configuration-list").replaceChildren();
    for (const cfg of catalog.items) { const row = document.createElement("p"); row.textContent = cfg.name + " · " + cfg.readiness + " · " + cfg.inspected_commit; row.append(button("Assign to selected host", async () => {const h=await selectedHost();await command("host.assign",{host_id:h.id,repository_id:repository.id,configuration:cfg.id},{["host:"+h.id]:h.version,["configuration:"+cfg.id]:cfg.version},h.id);await hostDetails();notice("Assigned "+cfg.name+" to "+h.name);})); $("configuration-list").append(row); }
    notice("Repository " + repository.name);
  }
  async function loadTemplate(id, commit) {
    bundle = await request("/repositories/" + repository.id + "/templates/" + encodeURIComponent(id) + "?commit=" + commit);
    renderFields();
  }
  function renderFields() {
    const form = $("configuration-fields"); form.replaceChildren();
    for (const field of bundle.template.fields) {
      const label = document.createElement("label"); label.textContent = field.label || field.name;
      let input;
      if (["select", "multiselect"].includes(field.type)) {
        input = document.createElement("select"); input.multiple = field.type === "multiselect";
        for (const choice of field.choices) { const option = new Option(choice, choice); input.add(option); }
      } else { input = document.createElement("input"); input.type = field.type === "boolean" ? "checkbox" : field.type === "integer" ? "number" : "text"; }
      input.name = field.name; input.required = field.required; input.dataset.type = field.type;
      const value = values[field.name] ?? field.default ?? (field.type === "multiselect" ? [] : field.type === "boolean" ? false : field.type === "integer" ? 0 : "");
      if (input.type === "checkbox") input.checked = value;
      else if (input.multiple) for (const option of input.options) option.selected = value.includes(option.value);
      else input.value = value;
      label.append(input); form.append(label);
      input.addEventListener("input", preview);
    }
  }
  function readValues() {
    const result = {};
    for (const input of $("configuration-fields").elements) {
      const type = input.dataset.type;
      result[input.name] = type === "boolean" ? input.checked : type === "integer" ? Number(input.value) : type === "multiselect" ? [...input.selectedOptions].map(o => o.value) : input.value;
    }
    return result;
  }
  function preview() {
    if (!globalThis.nixmoxRender || !bundle) return;
    values = readValues();
    const result = JSON.parse(globalThis.nixmoxRender(JSON.stringify({template: bundle.template, templates: bundle.templates, values})));
    if (result.error) { $("draft-state").textContent = result.error; return; }
    const next = new Map(result.files.map(f => [f.path, f]));
    for (const old of files) if (edited.has(old.path)) {
      const fresh = next.get(old.path);
      if (!fresh || fresh.content !== old.content) regenerated.set(old.path, fresh?.content ?? "");
      next.set(old.path, old);
    }
    files = [...next.values()].sort((a,b) => a.path.localeCompare(b.path));
    updateFiles(); $("draft-state").textContent = "Unsaved preview · " + bundle.inspected_commit;
  }
  function updateFiles() {
    const selected = $("file-choice").value;
    $("file-choice").replaceChildren(...files.map(f => new Option(f.path, f.path)));
    if (files.some(f => f.path === selected)) $("file-choice").value = selected;
    showFile();
  }
  function showFile() {
    const file = files.find(f => f.path === $("file-choice").value);
    const editor = $("file-editor");
    if (file && editor.value !== file.content) { const cursor = editor.selectionStart, scroll = editor.scrollTop; editor.value = file.content; editor.setSelectionRange(cursor, cursor); editor.scrollTop = scroll; }
    $("regenerated-review").hidden = !file || !regenerated.has(file.path);
    $("regenerated-text").textContent = file ? regenerated.get(file.path) || "" : "";
  }
  async function newConfiguration() {
    const revision = ++generation;
    const set = await request("/repositories/" + repository.id + "/templates");
    if (revision !== generation) return;
    const choices = set.items.filter(t => t.kind === "configuration");
    if (!choices.length) throw new Error("Add a configuration template under .nixmox/templates in the repository.");
    draft = undefined; files = []; values = {}; edited.clear(); regenerated.clear(); resolved.clear();
    $("configuration-editor").hidden = false;
    $("template-choice").disabled = false;
    $("template-choice").replaceChildren(...choices.map(t => new Option(t.label || t.id, t.id)));
    if (choices.length > 1) { $("template-choice").prepend(new Option("Choose a template", "")); $("template-choice").value = ""; }
    $("template-choice").onchange = () => run(async () => { values = {}; files = []; edited.clear(); regenerated.clear(); await loadTemplate($("template-choice").value, set.inspected_commit); preview(); });
    if (choices.length === 1) { await loadTemplate(choices[0].id, set.inspected_commit); preview(); }
  }
  async function openDraft(id) {
    const current = await request("/drafts/" + encodeURIComponent(id));
    repository = await request("/repositories/" + current.repository_id);
    draft = current; values = current.values; files = current.files; edited.clear(); regenerated.clear(); resolved.clear();
    for (const [path, manual] of Object.entries(current.manually_edited)) if (manual) edited.add(path);
    for (const [path, text] of Object.entries(current.regenerated)) regenerated.set(path, text);
    await loadTemplate(current.template_id, current.base_commit);
    $("configuration-editor").hidden = false;
    $("template-choice").replaceChildren(new Option(bundle.template.label, bundle.template.id)); $("template-choice").disabled = true;
    updateFiles();
    $("draft-state").textContent = current.id + " · version " + current.version + " · " + current.validation.state + (current.commit ? " · committed " + current.commit.commit : "");
    const diff = await request("/drafts/" + id + "/diff");
    $("draft-diff").textContent = diff.files.map(f => f.path + "\nBefore:\n" + f.before + "\nAfter:\n" + f.after).join("\n\n");
  }
  async function save() {
    if (!bundle) throw new Error("Choose a template first.");
    const captured = files.map(f => ({...f})), manual = new Set(edited);
    if (!draft) {
      const result = await command("configuration.generate", {repository_id: repository.id, template_id: bundle.template.id, base_commit: bundle.inspected_commit, values: readValues()});
      draft = await request("/drafts/" + result.draft_id);
    }
    for (const file of captured) if (manual.has(file.path)) {
      const saved = draft.files.find(f => f.path === file.path);
      if (saved?.content === file.content && !resolved.has(file.path)) continue;
      await command("configuration.draft.edit", {draft_id: draft.id, file: {path: file.path, content: file.content}}, {["draft:" + draft.id]: draft.version}, draft.owner_node_id);
      draft = await request("/drafts/" + draft.id);
    }
    const changed = readValues();
    if (JSON.stringify(changed) !== JSON.stringify(draft.values)) {
      await command("configuration.draft.edit", {draft_id: draft.id, values: changed}, {["draft:" + draft.id]: draft.version}, draft.owner_node_id);
      draft = await request("/drafts/" + draft.id);
      for (const file of captured) if (resolved.has(file.path)) {
        await command("configuration.draft.edit", {draft_id: draft.id, file: {path: file.path, content: file.content}}, {["draft:" + draft.id]: draft.version}, draft.owner_node_id);
        draft = await request("/drafts/" + draft.id);
      }
    }
    await openDraft(draft.id); notice("Draft saved.");
  }
  $("import-repository").onsubmit = event => { event.preventDefault(); const p = Object.fromEntries(new FormData(event.currentTarget)); run(async () => { await command("repository.import", p); await repositories(); }); };
  async function hosts() { const selected=$("target-host").value;const page=await request("/hosts?limit=500");$("target-host").replaceChildren(...page.items.map(h=>new Option(h.name+" · "+h.membership+" · "+h.reachability,h.id)));if(page.items.some(h=>h.id===selected))$("target-host").value=selected;await hostDetails(); }
  async function selectedHost(){const id=$("target-host").value;if(!id)throw new Error("Select a target host.");return request("/hosts/"+id);}
  async function hostDetails(){const h=await selectedHost();$("host-details").textContent=h.environment+" · "+h.health.status+" · "+(h.configuration?h.configuration.name:"No configuration assigned")+" · observed "+(h.last_seen||"unknown");}
  let deploymentTask, deploymentDiff, deploymentCursor, rollbackGeneration, deploymentHost, watchTimer;
  async function submitDeployment(type,payload,expected,target) {
    const random=[...crypto.getRandomValues(new Uint8Array(16))].map(v=>v.toString(16).padStart(2,"0")).join("");
    const envelope={id:"cmd_"+random,type,target_id:target,expected_versions:expected,payload};
    const key="nixmox.command."+envelope.id;
    sessionStorage.setItem(key,JSON.stringify(envelope));
    const receipt=await request("/commands",envelope);sessionStorage.removeItem(key);return receipt;
  }
  async function diffRows(cursor="") {
    if(!deploymentDiff)return;
    const query=new URLSearchParams({kind:$("diff-kind").value,q:$("diff-query").value,limit:"25",cursor});
    const page=await request("/diffs/"+deploymentDiff.id+"/changes?"+query);
    $("diff-rows").replaceChildren();
    for(const row of page.items){const box=document.createElement("details"),label=document.createElement("summary"),paths=document.createElement("pre");
      const versions=items=>items.map(v=>v.version||"unversioned").join(", ")||"—";
      label.textContent=row.kind+" · "+row.name+" · "+versions(row.old)+" → "+versions(row.new);
      paths.textContent="Previous outputs\n"+row.old.map(v=>v.store_path).join("\n")+"\nCandidate outputs\n"+row.new.map(v=>v.store_path).join("\n");box.append(label,paths);$("diff-rows").append(box);}
    deploymentCursor=page.next_cursor;$("diff-next").hidden=!deploymentCursor;
  }
  async function showDiff(id) {
    deploymentDiff=await request("/diffs/"+id);const d=deploymentDiff,s=d.summary;$("package-diff").hidden=false;
    $("diff-summary").textContent=s.added+" added · "+s.removed+" removed · "+s.version_changed+" version changed · "+s.output_changed+" output changed · "+s.ambiguous+" ambiguous · "+s.unchanged+" unchanged";
    $("diff-baseline").textContent=(d.stale?"Stale comparison · ":"")+d.baseline.output_path+" → "+d.candidate_output+" · baseline observed "+d.baseline.observed_at+" · closure size "+d.closure_size.before_bytes+" → "+d.closure_size.after_bytes+" bytes (not download size)";
    await diffRows();
  }
  async function watchDeployment(id) {
    clearTimeout(watchTimer);
    try {
      deploymentTask=await request("/tasks/"+id);const t=deploymentTask;
      $("deployment-panel").hidden=false;$("deploy-status").textContent=t.id+" · "+t.state+" · "+t.phase+(t.error?" · "+t.error.message:"");
      $("deploy-result").textContent=JSON.stringify(t.result||{source:t.source,progress:t.progress,effects:t.effects},null,2);
      $("deploy-cancel").hidden=!t.cancellation.allowed||["failed","cancelled","succeeded"].includes(t.state);
      $("deploy-approve").hidden=!(t.state==="blocked"&&t.phase==="review"&&t.approval?.state==="pending");
      if(t.approval&&deploymentDiff?.id!==t.approval.diff_id)await showDiff(t.approval.diff_id);
      if(["succeeded","failed","cancelled"].includes(t.state)){sessionStorage.removeItem("nixmox.deployment.task");return;}
      watchTimer=setTimeout(()=>watchDeployment(id),1500);
    } catch(e){error(e);watchTimer=setTimeout(()=>watchDeployment(id),5000);}
  }
  async function deploymentOptions(generation) {
    deploymentHost=await selectedHost();rollbackGeneration=generation||null;
    $("deployment-panel").hidden=false;$("deploy-auto").checked=false;$("deploy-mode").value="switch";
    $("deploy-selection").textContent=rollbackGeneration?"Rollback "+deploymentHost.name+" to generation "+generation.number+" · "+generation.output_path:"Apply latest to "+deploymentHost.name+" · "+(deploymentHost.configuration?.name||"No assigned configuration");
  }
  $("apply-host").onclick=()=>run(()=>deploymentOptions());
  $("deploy-start").onclick=()=>run(async()=>{
    if(!deploymentHost)throw new Error("Choose Apply latest or a generation first.");
    const h=deploymentHost,p={mode:$("deploy-mode").value,auto_approve:$("deploy-auto").checked};if(rollbackGeneration)p.generation_id=rollbackGeneration.id;
    const receipt=await submitDeployment(rollbackGeneration?"deployment.rollback":"deployment.apply",p,{["host:"+h.id]:h.version},h.id);
    sessionStorage.setItem("nixmox.deployment.task",receipt.task_id);await watchDeployment(receipt.task_id);
  });
  $("deploy-approve").onclick=()=>run(async()=>{
    const t=deploymentTask;if(!t?.approval||t.approval.diff_id!==deploymentDiff?.id)throw new Error("Refresh the current review.");
    await command("deployment.approve",{task_id:t.id,diff_id:deploymentDiff.id,diff_version:deploymentDiff.version},{},t.target_id);await watchDeployment(t.id);
  });
  $("deploy-cancel").onclick=()=>run(async()=>{const t=deploymentTask;if(!t)return;await submitDeployment("task.cancel",{task_id:t.id},{["task:"+t.id]:t.version},t.target_id);await watchDeployment(t.id);});
  $("diff-search").onclick=()=>run(()=>diffRows());$("diff-next").onclick=()=>run(()=>diffRows(deploymentCursor));
  $("host-generations").onclick=()=>run(async()=>{
    const h=await selectedHost(),page=await request("/hosts/"+h.id+"/generations?limit=500");$("deployment-panel").hidden=false;$("generation-list").replaceChildren();
    for(const g of page.items){const row=document.createElement("p");row.textContent="Generation "+g.number+" · "+(g.running?"running · ":"")+(g.boot?"next boot · ":"")+g.output_path+" · "+(g.available?"available":"unavailable");row.append(button("Review rollback",()=>deploymentOptions(g)));$("generation-list").append(row);}
  });
  const savedDeployment=sessionStorage.getItem("nixmox.deployment.task");if(savedDeployment)watchDeployment(savedDeployment);
  $("target-host").onchange=()=>run(hostDetails);
  $("refresh-hosts").onclick=()=>run(hosts);
  for(const [id,verb] of [["preview-host","preview"],["build-host","build"]])$(id).onclick=()=>run(async()=>{const h=await selectedHost();const result=await command("deployment."+verb,{host_id:h.id},{["host:"+h.id]:h.version},h.id);$("candidate-result").textContent=result.id?"Candidate "+result.id+"\nSource "+result.source.commit+"\nOutput "+result.output_path+"\nNAR hash "+result.nar_hash:JSON.stringify(result,null,2);if(result.id&&verb==="build")$("candidate-result").after(button("Compare candidate packages",async()=>{const d=await command("deployment.diff",{host_id:h.id,candidate_id:result.id},{},h.id);$("deployment-panel").hidden=false;await showDiff(d.id);}));notice("Completed "+verb+" for "+h.name);});
  $("fetch-repository").onclick = () => run(async () => { await command("repository.fetch", {repository_id: repository.id}); await openRepository(repository.id); });
  $("new-configuration").onclick = () => run(newConfiguration);
  $("open-draft").onsubmit = event => { event.preventDefault(); const id = new FormData(event.currentTarget).get("draft_id"); run(() => openDraft(id)); };
  $("configuration-fields").onsubmit = event => event.preventDefault();
  $("file-choice").onchange = showFile;
  $("file-editor").oninput = event => { const file = files.find(f => f.path === $("file-choice").value); if (file) { file.content = event.target.value; edited.add(file.path); $("draft-state").textContent = "Unsaved file edits"; } };
  $("replace-generated").onclick = () => { const path = $("file-choice").value, file = files.find(f => f.path === path); if (file && regenerated.has(path)) { file.content = regenerated.get(path); edited.add(path); resolved.add(path); regenerated.delete(path); showFile(); } };
  $("keep-edits").onclick = () => { const path = $("file-choice").value; regenerated.delete(path); resolved.add(path); showFile(); };
  $("save-draft").onclick = () => run(save);
  $("validate-draft").onclick = () => run(async () => { await save(); await command("configuration.validate", {draft_id: draft.id}, {}, draft.owner_node_id); await openDraft(draft.id); });
  $("commit-draft").onclick = () => run(async () => { if (regenerated.size) throw new Error("Review regenerated text before committing."); await save(); await command("configuration.commit", {draft_id: draft.id}, draft.expected_versions, draft.owner_node_id); await openDraft(draft.id); await openRepository(repository.id); });
  (async () => { try { const go = new Go(); const bytes = await (await fetch("/assets/renderer.wasm")).arrayBuffer(); const wasm = await WebAssembly.instantiate(bytes, go.importObject); go.run(wasm.instance).catch(error); await repositories(); await hosts(); notice("Choose a repository or import one."); } catch (e) { error(e); } })();
})();
