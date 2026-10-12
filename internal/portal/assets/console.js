(() => {
  "use strict";
  const $ = (s) => document.querySelector(s);
  const notice = (message, error = false) => {
    const n = $("#notice");
    n.textContent = message;
    n.classList.toggle("error", error);
  };
  async function request(path, method = "GET", body) {
    const response = await fetch(path, {
      method,
      headers: body === undefined ? {} : { "Content-Type": "application/json" },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    const text = await response.text();
    if (!response.ok) throw new Error(text || response.statusText);
    try {
      return JSON.parse(text);
    } catch {
      return text;
    }
  }
  document
    .querySelectorAll("nav a")
    .forEach((a) =>
      a.classList.toggle("active", a.pathname === location.pathname),
    );
  document.body.addEventListener("htmx:afterRequest", (e) => {
    if (e.detail.xhr.status >= 400) notice(e.detail.xhr.responseText, true);
    else if (e.detail.requestConfig.verb !== "get")
      notice("Request accepted. See Tasks for progress.");
  });
  // Upgrade existing fragments to one SSE connection and remove polling.
  document.querySelectorAll('[hx-get][hx-trigger^="every"]').forEach((el) => {
    el.dataset.live = el.getAttribute("hx-get");
    el.removeAttribute("hx-trigger");
    el.removeAttribute("hx-get");
    htmx.process(el);
  });
  const fragments = [...document.querySelectorAll("[data-live]")];
  const query = new URLSearchParams();
  fragments.forEach((el) => query.append("partial", el.dataset.live));
  const stream = new EventSource("/api/v1/events?" + query);
  stream.onopen = () => {
    $("#connection").textContent = "● Live";
    $("#connection").className = "badge online";
  };
  stream.onerror = () => {
    $("#connection").textContent = "Reconnecting…";
    $("#connection").className = "badge warn";
  };
  stream.addEventListener("fragments", (event) => {
    const changes = JSON.parse(event.data);
    fragments.forEach((el) => {
      if (Object.hasOwn(changes, el.dataset.live)) {
        el.innerHTML = changes[el.dataset.live];
        htmx.process(el);
      }
    });
  });
  addEventListener("pagehide", () => stream.close());
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
        notice(result.id ? "Started task " + result.id : "Rollout started.");
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
  async function discovery() {
    const list = $("#discovered");
    if (!list) return;
    list.replaceChildren();
    try {
      const peers = await request("/api/v1/discovery");
      if (!peers.length)
        list.textContent =
          "No advertisements. Enable discovery on both nodes and allow UDP 8099.";
      peers.forEach((peer) => {
        const row = document.createElement("p");
        row.textContent = `${peer.node_id} · ${peer.address} · ${peer.cluster || "unjoined"} `;
        if (peer.cluster) {
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
})();
