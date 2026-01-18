const $ = (id) => document.getElementById(id);

const state = {
  devices: [],
  subnets: [],
  q: "",
  online: "all",
  asic: "asic",
  vendor: "all",
  model: "all",
  page: "devices",
};

function fmtTs(iso) {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString();
}

function fmtUptime(s) {
  const n = Number(s || 0);
  if (!n) return "";
  const d = Math.floor(n / 86400);
  const h = Math.floor((n % 86400) / 3600);
  const m = Math.floor((n % 3600) / 60);
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  return `${m}m`;
}

function fmtTHS(v) {
  const n = Number(v || 0);
  if (!n) return "";
  if (n >= 1000) return `${(n / 1000).toFixed(2)} PH/s`;
  return `${n.toFixed(2)} TH/s`;
}

function showPage(name) {
  state.page = name;
  ["devices", "discovery", "settings", "creds", "mikrotik"].forEach((p) => {
    const tab = $(`tab_${p}`);
    const page = $(`page_${p}`);
    const active = p === name;
    tab.classList.toggle("tab-active", active);
    page.classList.toggle("hidden", !active);
  });
}

function applyFilters(devices) {
  const q = state.q.trim().toLowerCase();
  const online = state.online;
  const asic = state.asic;
  const vendor = state.vendor;
  const model = state.model;
  return devices.filter((d) => {
    if (online === "online" && !d.online) return false;
    if (online === "offline" && d.online) return false;
    if (asic === "asic" && Number(d.confidence || 0) < 60) return false;
    if (vendor !== "all" && (d.vendor || "") !== vendor) return false;
    if (model !== "all" && (d.model || "") !== model) return false;
    if (!q) return true;
    const hay = `${d.ip} ${d.mac} ${d.shard_id} ${d.vendor} ${d.model} ${d.worker}`.toLowerCase();
    return hay.includes(q);
  });
}

function render(devices) {
  // populate vendor/model filters
  const vendors = new Set();
  const models = new Set();
  devices.forEach((d) => {
    if (d.vendor) vendors.add(d.vendor);
    if (d.model) models.add(d.model);
  });
  const vSel = $("vendor");
  const mSel = $("model");
  if (vSel && mSel) {
    const keepV = state.vendor;
    const keepM = state.model;
    vSel.innerHTML = `<option value="all">vendor: all</option>` + [...vendors].sort().map((v) => `<option value="${v}">${v}</option>`).join("");
    mSel.innerHTML = `<option value="all">model: all</option>` + [...models].sort().map((m) => `<option value="${m}">${m}</option>`).join("");
    vSel.value = [...vendors].includes(keepV) ? keepV : "all";
    mSel.value = [...models].includes(keepM) ? keepM : "all";
    state.vendor = vSel.value;
    state.model = mSel.value;
  }

  const rows = applyFilters(devices);
  const total = devices.length;
  const on = devices.filter((d) => d.online).length;
  const off = total - on;

  $("stat_total").textContent = String(total);
  $("stat_on").textContent = String(on);
  $("stat_off").textContent = String(off);

  const tbody = $("tbody");
  tbody.innerHTML = "";

  rows
    .sort((a, b) => (a.ip || "").localeCompare(b.ip || ""))
    .forEach((d) => {
      const tr = document.createElement("tr");
      const dotCls = d.online ? "ok" : "bad";
      const status = d.online ? "online" : "offline";
      tr.innerHTML = `
        <td><span class="status"><span class="dot ${dotCls}"></span>${status}</span></td>
        <td>${d.ip || ""}</td>
        <td>${d.mac || ""}</td>
        <td>${d.vendor || ""}</td>
        <td>${d.model || ""}</td>
        <td>${d.worker || ""}</td>
        <td>${fmtUptime(d.uptime_s)}</td>
        <td>${fmtTHS(d.hashrate_ths)}</td>
        <td>${d.shard_id || ""}</td>
        <td>${fmtTs(d.first_seen)}</td>
        <td>${fmtTs(d.last_seen)}</td>
      `;
      tbody.appendChild(tr);
    });
}

function renderSubnets(subnets) {
  const tbody = $("subnets_tbody");
  tbody.innerHTML = "";
  subnets
    .slice()
    .sort((a, b) => (a.cidr || "").localeCompare(b.cidr || ""))
    .forEach((s) => {
      const tr = document.createElement("tr");
      if (s.scanning) tr.classList.add("row-active");
      const status = s.scanning ? "scanning" : "idle";
      const prog = s.scanning ? `${s.progress || 0}%` : (s.last_scan_at ? "done" : "—");
      const pct = Number(s.progress || 0);
      tr.innerHTML = `
        <td><input class="subnet-on" type="checkbox" data-act="toggle" data-id="${s.id}" ${s.enabled ? "checked" : ""} /></td>
        <td>${s.cidr || ""}</td>
        <td>${s.note || ""}</td>
        <td>${status}</td>
        <td>
          <div class="subnet-progress">
            <div class="pbar"><div class="pfill" style="width:${Math.max(0, Math.min(100, pct))}%"></div></div>
            <div style="min-width:42px">${prog}</div>
          </div>
        </td>
        <td>${s.last_scan_at ? fmtTs(s.last_scan_at) : ""}</td>
        <td>
          <button class="btn btn-sm" data-act="scan" data-id="${s.id}">Scan</button>
          <button class="btn btn-sm" data-act="stop" data-id="${s.id}">Stop</button>
          <button class="btn btn-sm" data-act="del" data-id="${s.id}">Delete</button>
        </td>
      `;
      tbody.appendChild(tr);
    });
}

function renderScanSummary() {
  const subs = state.subnets || [];
  const scanning = subs.filter((s) => s.scanning);
  const enabled = subs.filter((s) => s.enabled);
  const avg = scanning.length ? Math.round(scanning.reduce((a, s) => a + Number(s.progress || 0), 0) / scanning.length) : 0;
  const bar = $("scan_bar");
  const txt = $("scan_text");
  if (!bar || !txt) return;
  bar.style.width = `${avg}%`;
  if (!scanning.length) {
    txt.textContent = enabled.length ? `idle • ${enabled.length} pools enabled` : "idle";
    return;
  }
  const cur = scanning[0];
  const label = (cur.note ? `${cur.note} • ` : "") + (cur.cidr || "");
  txt.textContent = `scanning ${scanning.length}/${enabled.length} • ~${avg}% • ${label}`;
}

async function fetchDevices() {
  const res = await fetch("/api/devices", { cache: "no-store" });
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  return await res.json();
}

async function fetchSubnets() {
  const res = await fetch("/api/subnets", { cache: "no-store" });
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  return await res.json();
}

async function fetchDefaultCreds() {
  const res = await fetch("/api/creds/defaults", { cache: "no-store" });
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  return await res.json();
}

function renderCreds(rows) {
  const tbody = $("creds_tbody");
  tbody.innerHTML = "";
  (rows || []).forEach((c) => {
    const tr = document.createElement("tr");
    tr.innerHTML = `
      <td>${c.vendor || ""}</td>
      <td>${c.username || ""}</td>
      <td>${c.password || ""}</td>
      <td>${c.note || ""}</td>
    `;
    tbody.appendChild(tr);
  });
}

function setConn(kind) {
  const el = $("conn");
  el.classList.remove("pill-ok", "pill-warn", "pill-bad");
  if (kind === "ok") {
    el.textContent = "realtime";
    el.classList.add("pill-ok");
  } else if (kind === "bad") {
    el.textContent = "offline";
    el.classList.add("pill-bad");
  } else {
    el.textContent = "connecting…";
    el.classList.add("pill-warn");
  }
}

function initControls() {
  $("tab_devices").addEventListener("click", () => showPage("devices"));
  $("tab_discovery").addEventListener("click", () => showPage("discovery"));
  $("tab_settings").addEventListener("click", () => showPage("settings"));
  $("tab_creds").addEventListener("click", () => showPage("creds"));
  $("tab_mikrotik").addEventListener("click", () => showPage("mikrotik"));

  $("q").addEventListener("input", (e) => {
    state.q = e.target.value || "";
    render(state.devices);
  });
  $("online").addEventListener("change", (e) => {
    state.online = e.target.value;
    render(state.devices);
  });
  $("asic").addEventListener("change", (e) => {
    state.asic = e.target.value;
    render(state.devices);
  });
  if ($("vendor")) {
    $("vendor").addEventListener("change", (e) => {
      state.vendor = e.target.value;
      render(state.devices);
    });
  }
  if ($("model")) {
    $("model").addEventListener("change", (e) => {
      state.model = e.target.value;
      render(state.devices);
    });
  }
  $("refresh").addEventListener("click", async () => {
    try {
      state.devices = await fetchDevices();
      render(state.devices);
    } catch {
      // ignore
    }
  });

  $("add_subnet").addEventListener("click", async () => {
    const cidr = ($("cidr").value || "").trim();
    const note = ($("cidr_note").value || "").trim();
    if (!cidr) return;
    await fetch("/api/subnets", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ cidr, note }),
    });
    $("cidr").value = "";
    if ($("cidr_note")) $("cidr_note").value = "";
  });

  $("subnets_tbody").addEventListener("click", async (e) => {
    const btn = e.target.closest("button");
    if (!btn) return;
    const id = btn.getAttribute("data-id");
    const act = btn.getAttribute("data-act");
    if (!id || !act) return;
    if (act === "del") {
      await fetch(`/api/subnets/${id}`, { method: "DELETE" });
    } else if (act === "scan") {
      await fetch(`/api/subnets/${id}/scan`, { method: "POST" });
    } else if (act === "stop") {
      await fetch(`/api/subnets/${id}/stop`, { method: "POST" });
    }
  });

  $("subnets_tbody").addEventListener("change", async (e) => {
    const el = e.target;
    if (!el || el.getAttribute("data-act") !== "toggle") return;
    const id = el.getAttribute("data-id");
    const enabled = !!el.checked;
    await fetch(`/api/subnets/${id}`, {
      method: "PATCH",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ enabled }),
    });
  });

  $("save_settings").addEventListener("click", async () => {
    const cur = await fetch("/api/settings", { cache: "no-store" }).then((r) => r.json());
    cur.nats_url = ($("set_nats_url").value || "").trim();
    cur.nats_prefix = ($("set_nats_prefix").value || "").trim();
    cur.http_addr = ($("set_http_addr").value || "").trim();
    cur.embedded_nats = cur.embedded_nats || {};
    cur.embedded_nats.enabled = $("set_embedded_nats").checked;
    cur.embedded_nats.host = ($("set_embedded_host").value || "").trim();
    cur.embedded_nats.port = Number(($("set_embedded_port").value || "").trim()) || 0;
    cur.embedded_nats.http_port = Number(($("set_embedded_http_port").value || "").trim()) || 0;
    cur.embedded_nats.store_dir = ($("set_embedded_store").value || "").trim();
    cur.try_default_creds = $("set_try_defaults").checked;
    await fetch("/api/settings", {
      method: "PUT",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(cur),
    });
  });

  const preview = async (val, outEl) => {
    val = (val || "").trim();
    if (!val) {
      outEl.textContent = "";
      return;
    }
    try {
      const p = await fetch(`/api/cidr/preview?cidr=${encodeURIComponent(val)}`, { cache: "no-store" }).then((r) => r.json());
      if (!p.valid) {
        outEl.textContent = `Invalid: ${p.error || ""}`.trim();
        return;
      }
      if (!p.total_hosts) {
        outEl.textContent = `Ok, but has 0 usable hosts`;
        return;
      }
      const samples = (p.samples || []).join(", ");
      outEl.textContent = `Will scan ${p.total_hosts} hosts: ${p.first} → ${p.last}${samples ? ` (e.g. ${samples})` : ""}`;
    } catch {
      outEl.textContent = "";
    }
  };

  let cidrTimer = null;
  $("cidr").addEventListener("input", () => {
    if (cidrTimer) clearTimeout(cidrTimer);
    cidrTimer = setTimeout(async () => {
      await preview($("cidr").value, $("cidr_preview"));
    }, 150);
  });

  // Devices page: run scan for all Discovery pools
  if ($("scan_all")) {
    $("scan_all").addEventListener("click", async () => {
      await fetch("/api/subnets/scan_all", { method: "POST" });
    });
  }
  if ($("stop_all")) {
    $("stop_all").addEventListener("click", async () => {
      await fetch("/api/subnets/stop_all", { method: "POST" });
    });
  }

  $("exit_app").addEventListener("click", async () => {
    if (!confirm("Exit MonA now? (This will stop scanning and free ports)")) return;
    await fetch("/api/admin/exit", { method: "POST" });
  });
}

function connectSSE() {
  setConn("warn");
  const es = new EventSource("/api/stream/devices");
  es.addEventListener("devices", (e) => {
    try {
      state.devices = JSON.parse(e.data);
      render(state.devices);
      setConn("ok");
    } catch {
      // ignore bad frames
    }
  });
  es.onerror = async () => {
    setConn("bad");
    es.close();
    // fallback to polling until SSE is back
    for (;;) {
      try {
        state.devices = await fetchDevices();
        render(state.devices);
        setConn("bad");
      } catch {
        // ignore
      }
      await new Promise((r) => setTimeout(r, 2000));
      try {
        connectSSE();
        return;
      } catch {
        // continue polling
      }
    }
  };
}

function connectSSESubnets() {
  const es = new EventSource("/api/stream/subnets");
  es.addEventListener("subnets", (e) => {
    try {
      state.subnets = JSON.parse(e.data);
      renderSubnets(state.subnets);
      renderScanSummary();
    } catch {
      // ignore
    }
  });
}

async function main() {
  initControls();
  showPage("devices");
  try {
    const ver = await fetch("/api/version", { cache: "no-store" }).then((r) => r.text());
    $("ver").textContent = `ASIC Fleet Monitor (${ver.trim()})`;
  } catch {
    // ignore
  }
  try {
    state.devices = await fetchDevices();
    render(state.devices);
  } catch {
    // ignore
  }
  try {
    state.subnets = await fetchSubnets();
    renderSubnets(state.subnets);
    renderScanSummary();
  } catch {
    // ignore
  }
  try {
    const creds = await fetchDefaultCreds();
    renderCreds(creds);
  } catch {
    // ignore
  }
  connectSSE();
  connectSSESubnets();

  try {
    const s = await fetch("/api/settings", { cache: "no-store" }).then((r) => r.json());
    $("set_nats_url").value = s.nats_url || "";
    $("set_nats_prefix").value = s.nats_prefix || "";
    $("set_http_addr").value = s.http_addr || "";
    $("set_embedded_nats").checked = !!(s.embedded_nats && s.embedded_nats.enabled);
    $("set_embedded_host").value = (s.embedded_nats && s.embedded_nats.host) || "";
    $("set_embedded_port").value = (s.embedded_nats && s.embedded_nats.port) || "";
    $("set_embedded_http_port").value = (s.embedded_nats && s.embedded_nats.http_port) || "";
    $("set_embedded_store").value = (s.embedded_nats && s.embedded_nats.store_dir) || "";
    $("set_try_defaults").checked = !!s.try_default_creds;
  } catch {
    // ignore
  }

  // status poll (nats connectivity)
  setInterval(async () => {
    try {
      const st = await fetch("/api/status", { cache: "no-store" }).then((r) => r.json());
      const el = $("nats_status");
      const emb = st.embedded_nats ? "embedded" : "external";
      if (st.nats_connected) {
        el.textContent = `nats: connected (${emb})`;
        el.classList.remove("pill-warn", "pill-bad");
        el.classList.add("pill-ok");
      } else {
        el.textContent = `nats: disconnected (${emb})`;
        el.classList.remove("pill-ok");
        el.classList.add("pill-bad");
      }
    } catch {
      // ignore
    }
  }, 1500);
}

main();

