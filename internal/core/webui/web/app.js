const $ = (id) => document.getElementById(id);
const qsa = (sel, root = document) => Array.from(root.querySelectorAll(sel));

const state = {
  route: "dashboard",
  devices: [],
  subnets: [],
  logs: [],
  lastDevicesTs: 0,
  selectedIP: "",
  probe: null,
  // small live-only buffers (sparklines / main chart)
  series: {
    online: [],
    hashrate: [],
    scan: [],
    asic: [],
  },
  // devices filters
  q: "",
  online: "all",
  asic: "asic",
  vendor: "all",
  model: "all",
  dashRefreshMs: 10000,
  lastDashSampleTs: 0,
  lastDashDrawTs: 0,
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

function logLine(level, msg) {
  const now = new Date();
  state.logs.unshift({ ts: now.toISOString(), level, msg });
  if (state.logs.length > 400) state.logs.length = 400;
  renderLogs();
}

function setConn(kind) {
  const el = $("conn");
  if (!el) return;
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

function setRoute(name) {
  state.route = name || "dashboard";
  ["dashboard", "devices", "device", "discovery", "creds", "settings"].forEach((p) => {
    const page = $(`page_${p}`);
    if (page) page.classList.toggle("hidden", p !== state.route);
  });
  qsa(".sb-item").forEach((b) => {
    b.classList.toggle("sb-active", b.getAttribute("data-route") === state.route);
  });
  renderTopbar();
  renderDashboard();
}

function computeFleetSummary(devices) {
  const total = devices.length;
  const online = devices.filter((d) => d.online).length;
  const offline = total - online;
  const asicOnly = devices.filter((d) => Number(d.confidence || 0) >= 60).length;
  const hashrateTHS = devices.reduce((a, d) => a + Number(d.hashrate_ths || 0), 0);
  return { total, online, offline, asicOnly, hashrateTHS };
}

function renderTopbar() {
  const sum = computeFleetSummary(state.devices || []);
  const tbName = $("tb_name");
  const tbSub = $("tb_sub");
  const tbHash = $("tb_hash");
  const tbStatus = $("tb_status");
  if (tbName) tbName.textContent = state.route === "device" ? "Device Details" : state.route === "dashboard" ? "Fleet Dashboard" : state.route[0].toUpperCase() + state.route.slice(1);
  if (tbSub) tbSub.textContent = `${sum.online}/${sum.total} online • last update ${state.lastDevicesTs ? new Date(state.lastDevicesTs).toLocaleTimeString() : "—"}`;
  if (tbHash) tbHash.textContent = fmtTHS(sum.hashrateTHS) || "—";
  if (tbStatus) tbStatus.textContent = sum.offline ? "WARN" : "OK";
}

function seriesPush(key, val, max = 60) {
  const arr = state.series[key];
  if (!arr) return;
  arr.push(Number(val || 0));
  if (arr.length > max) arr.splice(0, arr.length - max);
}

function drawSpark(canvas, values, color) {
  if (!canvas) return;
  const ctx = canvas.getContext("2d");
  if (!ctx) return;
  const w = canvas.width;
  const h = canvas.height;
  ctx.clearRect(0, 0, w, h);
  const arr = (values || []).slice();
  if (!arr.length) return;
  let min = Math.min(...arr);
  let max = Math.max(...arr);
  if (min === max) {
    min -= 1;
    max += 1;
  }
  const pad = 2;
  const xStep = (w - pad * 2) / Math.max(1, arr.length - 1);
  const y = (v) => pad + (h - pad * 2) * (1 - (v - min) / (max - min));

  ctx.lineWidth = 2;
  ctx.strokeStyle = color;
  ctx.beginPath();
  arr.forEach((v, i) => {
    const x = pad + i * xStep;
    const yy = y(v);
    if (i === 0) ctx.moveTo(x, yy);
    else ctx.lineTo(x, yy);
  });
  ctx.stroke();

  const grad = ctx.createLinearGradient(0, 0, 0, h);
  grad.addColorStop(0, `${color}55`);
  grad.addColorStop(1, `${color}00`);
  ctx.fillStyle = grad;
  ctx.lineTo(pad + (arr.length - 1) * xStep, h - pad);
  ctx.lineTo(pad, h - pad);
  ctx.closePath();
  ctx.fill();
}

function setRing(circle, pct) {
  if (!circle) return;
  const r = Number(circle.getAttribute("r") || "0");
  if (!r) return;
  const c = 2 * Math.PI * r;
  const p = Math.max(0, Math.min(100, Number(pct || 0)));
  circle.style.strokeDasharray = `${c} ${c}`;
  circle.style.strokeDashoffset = `${c * (1 - p / 100)}`;
}

function drawMainChart() {
  const c = $("main_chart");
  if (!c) return;
  const ctx = c.getContext("2d");
  if (!ctx) return;
  const w = c.width;
  const h = c.height;
  ctx.clearRect(0, 0, w, h);

  const metric = ($("chart_metric") && $("chart_metric").value) || "hash";
  let seriesKey = "hashrate";
  let color = "#7c8cff";
  if (metric === "online") {
    seriesKey = "online";
    color = "#4ade80";
  } else if (metric === "scans") {
    seriesKey = "scan";
    color = "#fbbf24";
  }
  const arr = (state.series[seriesKey] || []).slice();
  if (!arr.length) {
    ctx.fillStyle = "rgba(255,255,255,0.45)";
    ctx.font = "12px Inter, system-ui, sans-serif";
    ctx.fillText("No data yet (live only)", 12, 22);
    return;
  }

  ctx.strokeStyle = "rgba(255,255,255,0.07)";
  ctx.lineWidth = 1;
  for (let i = 1; i <= 4; i++) {
    const yy = Math.round((h * i) / 5);
    ctx.beginPath();
    ctx.moveTo(0, yy);
    ctx.lineTo(w, yy);
    ctx.stroke();
  }

  let min = Math.min(...arr);
  let max = Math.max(...arr);
  if (min === max) {
    min -= 1;
    max += 1;
  }
  const padX = 10;
  const padY = 10;
  const xStep = (w - padX * 2) / Math.max(1, arr.length - 1);
  const y = (v) => padY + (h - padY * 2) * (1 - (v - min) / (max - min));

  ctx.lineWidth = 2;
  ctx.strokeStyle = color;
  ctx.beginPath();
  arr.forEach((v, i) => {
    const x = padX + i * xStep;
    const yy = y(v);
    if (i === 0) ctx.moveTo(x, yy);
    else ctx.lineTo(x, yy);
  });
  ctx.stroke();

  const grad = ctx.createLinearGradient(0, 0, 0, h);
  grad.addColorStop(0, `${color}35`);
  grad.addColorStop(1, `${color}00`);
  ctx.fillStyle = grad;
  ctx.lineTo(padX + (arr.length - 1) * xStep, h - padY);
  ctx.lineTo(padX, h - padY);
  ctx.closePath();
  ctx.fill();
}

function renderDashboard() {
  if (state.route !== "dashboard") return;
  const sum = computeFleetSummary(state.devices || []);

  if ($("kpi_online")) $("kpi_online").textContent = String(sum.online);
  if ($("kpi_total")) $("kpi_total").textContent = String(sum.total);
  if ($("kpi_offline")) $("kpi_offline").textContent = String(sum.offline);
  if ($("kpi_online_sub")) $("kpi_online_sub").textContent = `${sum.offline} offline • ${sum.total} total`;
  if ($("kpi_hash")) $("kpi_hash").textContent = fmtTHS(sum.hashrateTHS) || "0 TH/s";
  if ($("kpi_scan")) {
    const sc = Number(state.scanScanning || 0);
    const en = Number(state.scanEnabled || 0);
    $("kpi_scan").textContent = sc ? `scanning ${sc}/${en || "?"}` : "idle";
  }

  // Throttle sparkline redraw to reduce CPU stalls on large fleets
  const now = Date.now();
  if (now-state.lastDashDrawTs >= (state.dashRefreshMs || 10000)) {
    drawSpark($("sp_hash"), state.series.hashrate, "#7c8cff");
    state.lastDashDrawTs = now;
  }

  const onlinePct = sum.total ? Math.round((100 * sum.online) / sum.total) : 0;
  setRing($("ring_online"), onlinePct);
  if ($("kpi_online_pct")) $("kpi_online_pct").textContent = `${onlinePct}%`;

  const scanPct = Math.max(0, Math.min(100, Number(state.lastScanAvg || 0)));
  setRing($("ring_scan"), scanPct);
  if ($("kpi_scan_pct")) $("kpi_scan_pct").textContent = `${scanPct}%`;

  // offline live list
  const list = $("offline_list");
  if (list) {
    const off = (state.devices || [])
      .filter((d) => !d.online)
      .slice()
      .sort((a, b) => (b.last_seen || "").localeCompare(a.last_seen || ""))
      .slice(0, 24);
    list.innerHTML = off.length
      ? off
          .map((d) => {
            const model = d.model || d.vendor || "";
            const worker = d.worker || "";
            const last = d.last_seen ? new Date(d.last_seen).toLocaleTimeString() : "";
            return `<div class="rowline"><span class="ip">${d.ip || ""}</span><span class="m">${model}</span><span class="w">${worker}</span><span class="t">${last}</span></div>`;
          })
          .join("")
      : `<div class="empty">No offline devices</div>`;
  }
}

function applyDeviceFilters(devices) {
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
    const hay = `${d.ip} ${d.mac} ${d.vendor} ${d.model} ${d.worker}`.toLowerCase();
    return hay.includes(q);
  });
}

function renderDevices(devices) {
  // filters population
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

  const rows = applyDeviceFilters(devices);
  const tbody = $("tbody");
  if (!tbody) return;
  tbody.innerHTML = "";
  rows
    .slice()
    .sort((a, b) => (a.ip || "").localeCompare(b.ip || ""))
    .forEach((d) => {
      const tr = document.createElement("tr");
      const dotCls = d.online ? "ok" : "bad";
      const status = d.online ? "online" : "offline";
      const fans = Array.isArray(d.fans_rpm) ? d.fans_rpm : [];
      const maxRPM = fans.length ? Math.max(...fans.map((x) => Number(x || 0))) : 0;
      const avgRPM = fans.length ? Math.round(fans.reduce((a, x) => a + Number(x || 0), 0) / fans.length) : 0;
      // "how much fans spin from maximum": use max observed vs typical max (7000) for quick glance
      const ref = 7000;
      const pct = maxRPM > 0 ? Math.max(0, Math.min(100, Math.round((maxRPM * 100) / ref))) : 0;
      const barCls = pct >= 95 ? "bad" : pct >= 80 ? "warn" : "ok";
      const fansTitle = fans.length ? `max ${maxRPM} rpm • avg ${avgRPM} rpm` : "no fan data";
      const fansHTML = `<div class="fanbar ${barCls}" title="${fansTitle}"><div class="fanfill" style="width:${pct}%"></div></div>`;
      const auth = (d.auth_status || "").toLowerCase();
      const authCls = auth === "ok" ? "ok" : auth === "trying" ? "warn" : auth === "fail" ? "bad" : "idle";
      const authHintParts = [];
      if (d.auth_cred_name) authHintParts.push(`cred=${d.auth_cred_name}`);
      if (d.auth_error) authHintParts.push(`err=${d.auth_error}`);
      if (d.auth_updated) authHintParts.push(`at=${new Date(d.auth_updated).toLocaleTimeString()}`);
      const authTitle = authHintParts.join(" • ");
      const authHTML = `<span class="authdot ${authCls}" title="${authTitle}" data-ip="${d.ip || ""}"></span>`;

      tr.innerHTML = `
        <td><span class="status"><span class="dot ${dotCls}"></span>${status}</span></td>
        <td><a class="iplink" href="/ui/open/${encodeURIComponent(d.ip || "")}" target="_blank" rel="noreferrer">${d.ip || ""}</a></td>
        <td>${d.mac || ""}</td>
        <td>${d.model || ""}</td>
        <td>${fmtTHS(d.hashrate_ths)}</td>
        <td>${d.worker || ""}</td>
        <td>${authHTML}</td>
        <td>${fmtUptime(d.uptime_s)}</td>
        <td>${fansHTML}</td>
        <td>${fmtTs(d.first_seen)}</td>
        <td>${fmtTs(d.last_seen)}</td>
      `;
      tr.style.cursor = "pointer";
      tr.addEventListener("click", () => openDevice(d.ip));
      const iplink = tr.querySelector(".iplink");
      if (iplink) {
        iplink.addEventListener("click", (e) => e.stopPropagation());
      }
      // prevent row click when clicking auth dot (still opens device)
      const dot = tr.querySelector(".authdot");
      if (dot) {
        dot.addEventListener("click", (e) => {
          e.stopPropagation();
          openDevice(d.ip);
        });
      }
      tbody.appendChild(tr);
    });

  // series + dashboard
  const sum = computeFleetSummary(devices || []);
  state.lastDevicesTs = Date.now();
  // sample hashrate series at dashboard interval only (like Grafana refresh)
  const now = Date.now();
  if (!state.lastDashSampleTs || (now - state.lastDashSampleTs) >= (state.dashRefreshMs || 10000)) {
    seriesPush("hashrate", sum.hashrateTHS);
    state.lastDashSampleTs = now;
  }
  renderTopbar();
  renderDashboard();
}

function renderDeviceDetails(d) {
  if (!d) return;
  const st = d.online ? "ONLINE" : "OFFLINE";
  if ($("dev_status")) $("dev_status").textContent = st;
  if ($("dev_ip")) $("dev_ip").textContent = `${d.ip || ""} • ${d.mac || ""}`;
  if ($("dev_hash")) $("dev_hash").textContent = fmtTHS(d.hashrate_ths) || "—";
  if ($("dev_worker")) $("dev_worker").textContent = `${d.vendor || ""} • ${d.model || ""} • ${d.worker || ""}`.replace(/\s+•\s+•/g, " • ").replace(/^ • /, "").replace(/ • $/, "");
  if ($("dev_uptime")) $("dev_uptime").textContent = fmtUptime(d.uptime_s) || "—";
  if ($("dev_fw")) $("dev_fw").textContent = `${d.firmware || ""}` || "—";

  // fans
  const hostFans = $("dev_fans");
  if (hostFans) {
    const fans = Array.isArray(d.fans_rpm) ? d.fans_rpm : [];
    hostFans.innerHTML = fans.length
      ? fans
          .slice(0, 10)
          .map((rpm, idx) => {
            const n = Number(rpm || 0);
            const cls = n > 0 ? "fan" : "fan stop";
            const dur = n > 0 ? Math.max(0.25, Math.min(1.6, 6000 / Math.max(600, n))).toFixed(2) : "0";
            return `<div class="fanbox"><span class="${cls}" style="--dur:${dur}s" title="${n} rpm"></span><span class="fanrpm">fan${idx + 1}: ${n} rpm</span></div>`;
          })
          .join("")
      : `<div class="empty">No fan data yet (needs cgminer stats)</div>`;
  }

  // temps
  const hostTemps = $("dev_temps");
  if (hostTemps) {
    const temps = Array.isArray(d.temps_c) ? d.temps_c : [];
    hostTemps.innerHTML = temps.length
      ? temps
          .slice(0, 24)
          .map((t, idx) => {
            const v = Number(t || 0);
            const cls = v >= 85 ? "hot" : v >= 75 ? "warm" : "";
            return `<span class="temp ${cls}" title="temp${idx + 1}">${v.toFixed(1)}°C</span>`;
          })
          .join("")
      : `<div class="empty">No temperature data yet</div>`;
  }
}

async function openDevice(ip) {
  if (!ip) return;
  state.selectedIP = ip;
  setRoute("device");
  try {
    const d = await fetchJSON(`/api/devices/${encodeURIComponent(ip)}`);
    renderDeviceDetails(d);

    // Auto probe on open if important fields are missing.
    const need =
      d &&
      d.online &&
      (!d.mac || !d.worker || !d.firmware || !Array.isArray(d.fans_rpm) || d.fans_rpm.length === 0);
    if (need) {
      // fire-and-forget; UI will update probe_out and device fields
      if ($("device_probe")) $("device_probe").click();
    }
  } catch {
    // ignore
  }
  if ($("probe_out")) $("probe_out").textContent = "{}";
  if ($("probe_status")) {
    $("probe_status").textContent = "idle";
    $("probe_status").classList.remove("pill-ok", "pill-bad");
    $("probe_status").classList.add("pill-warn");
  }
}

function renderSubnets(subnets) {
  const tbody = $("subnets_tbody");
  if (!tbody) return;
  tbody.innerHTML = "";
  subnets
    .slice()
    .sort((a, b) => (a.cidr || "").localeCompare(b.cidr || ""))
    .forEach((s) => {
      const tr = document.createElement("tr");
      if (s.scanning) tr.classList.add("row-active");
      const status = s.scanning ? "scanning" : "idle";
      const prog = s.scanning ? `${s.progress || 0}%` : s.last_scan_at ? "done" : "—";
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
  state.lastScanAvg = avg;
  state.scanScanning = scanning.length;
  state.scanEnabled = enabled.length;
  const bar = $("scan_bar");
  const txt = $("scan_text");
  if (!bar || !txt) return;
  bar.style.width = `${avg}%`;
  if (!scanning.length) {
    txt.textContent = enabled.length ? `idle • ${enabled.length} pools enabled` : "idle";
    if ($("pools_enabled")) $("pools_enabled").textContent = String(enabled.length);
    if ($("pools_scanning")) $("pools_scanning").textContent = "0";
    renderDashboard();
    return;
  }
  const cur = scanning[0];
  const label = (cur.note ? `${cur.note} • ` : "") + (cur.cidr || "");
  txt.textContent = `scanning ${scanning.length}/${enabled.length} • ${label}`;
  if ($("pools_enabled")) $("pools_enabled").textContent = String(enabled.length);
  if ($("pools_scanning")) $("pools_scanning").textContent = `${scanning.length}`;
  renderDashboard();
}

function renderCreds(rows) {
  const tbody = $("creds_tbody");
  if (!tbody) return;
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

function renderLogs() {
  const host = $("logs");
  if (!host) return;
  const q = (($("log_q") && $("log_q").value) || "").trim().toLowerCase();
  const rows = (state.logs || []).filter((l) => {
    if (!q) return true;
    return `${l.level} ${l.msg} ${l.ts}`.toLowerCase().includes(q);
  });
  host.innerHTML = rows
    .slice(0, 120)
    .map((l) => {
      const cls = l.level === "warn" ? "warn" : l.level === "error" ? "err" : "info";
      const ts = new Date(l.ts).toLocaleTimeString();
      return `<div class="log ${cls}"><span class="ts">${ts}</span><span class="lvl">${l.level}</span><span class="msg">${l.msg}</span></div>`;
    })
    .join("");
}

async function fetchJSON(url, opt) {
  const res = await fetch(url, { cache: "no-store", ...(opt || {}) });
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  return await res.json();
}

async function fetchText(url) {
  const res = await fetch(url, { cache: "no-store" });
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  return await res.text();
}

function initControls() {
  qsa(".sb-item").forEach((b) => b.addEventListener("click", () => setRoute(b.getAttribute("data-route") || "dashboard")));
  if ($("toggle_sidebar")) {
    $("toggle_sidebar").addEventListener("click", () => document.body.classList.toggle("sb-collapsed"));
  }

  // devices filters
  if ($("q")) {
    $("q").addEventListener("input", (e) => {
      state.q = e.target.value || "";
      renderDevices(state.devices);
    });
  }
  if ($("online")) {
    $("online").addEventListener("change", (e) => {
      state.online = e.target.value;
      renderDevices(state.devices);
    });
  }
  if ($("asic")) {
    $("asic").addEventListener("change", (e) => {
      state.asic = e.target.value;
      renderDevices(state.devices);
    });
  }
  if ($("vendor")) {
    $("vendor").addEventListener("change", (e) => {
      state.vendor = e.target.value;
      renderDevices(state.devices);
    });
  }
  if ($("model")) {
    $("model").addEventListener("change", (e) => {
      state.model = e.target.value;
      renderDevices(state.devices);
    });
  }
  if ($("refresh")) {
    $("refresh").addEventListener("click", async () => {
      try {
        state.devices = await fetchJSON("/api/devices");
        renderDevices(state.devices);
        logLine("info", "Devices refreshed");
      } catch {
        logLine("error", "Failed to refresh devices");
      }
    });
  }

  // dashboard controls
  if ($("scan_all")) {
    $("scan_all").addEventListener("click", async () => {
      logLine("info", "Scan discovery: start");
      await fetch("/api/subnets/scan_all", { method: "POST" });
    });
  }
  if ($("stop_all")) {
    $("stop_all").addEventListener("click", async () => {
      logLine("info", "Stop scans: requested");
      await fetch("/api/subnets/stop_all", { method: "POST" });
    });
  }
  if ($("log_q")) $("log_q").addEventListener("input", () => renderLogs());
  if ($("dash_refresh")) {
    // initialize from default select value
    const initV = Number($("dash_refresh").value || 10000);
    state.dashRefreshMs = initV > 0 ? initV : 10000;
    $("dash_refresh").addEventListener("change", (e) => {
      const v = Number(e.target.value || 10000);
      state.dashRefreshMs = v > 0 ? v : 10000;
      // force redraw on next render
      state.lastDashDrawTs = 0;
      state.lastDashSampleTs = 0;
    });
  }

  // device details
  if ($("device_back")) $("device_back").addEventListener("click", () => setRoute("devices"));
  if ($("device_probe")) {
    $("device_probe").addEventListener("click", async () => {
      if (!state.selectedIP) return;
      const st = $("probe_status");
      if (st) {
        st.textContent = "probing…";
        st.classList.remove("pill-ok", "pill-bad");
        st.classList.add("pill-warn");
      }
      try {
        const res = await fetchJSON(`/api/devices/${encodeURIComponent(state.selectedIP)}/probe`, { method: "POST" });
        state.probe = res;
        if ($("probe_out")) $("probe_out").textContent = JSON.stringify(res, null, 2);
        if (st) {
          st.textContent = res.ok ? `ok • ${res.used_cred || ""}`.trim() : "failed";
          st.classList.remove("pill-warn");
          st.classList.add(res.ok ? "pill-ok" : "pill-bad");
        }
        // refresh device after enrichment
        const d = await fetchJSON(`/api/devices/${encodeURIComponent(state.selectedIP)}`);
        renderDeviceDetails(d);
      } catch {
        if ($("probe_out")) $("probe_out").textContent = "{\"ok\":false}";
        if (st) {
          st.textContent = "failed";
          st.classList.remove("pill-warn");
          st.classList.add("pill-bad");
        }
      }
    });
  }

  // discovery add subnet + preview
  if ($("add_subnet")) {
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
      logLine("info", `Pool added: ${cidr}`);
    });
  }

  const preview = async (val, outEl) => {
    val = (val || "").trim();
    if (!val) {
      outEl.textContent = "";
      return;
    }
    try {
      const p = await fetchJSON(`/api/cidr/preview?cidr=${encodeURIComponent(val)}`);
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
  if ($("cidr")) {
    $("cidr").addEventListener("input", () => {
      if (cidrTimer) clearTimeout(cidrTimer);
      cidrTimer = setTimeout(async () => {
        if ($("cidr_preview")) await preview($("cidr").value, $("cidr_preview"));
      }, 150);
    });
  }

  // discovery actions
  if ($("subnets_tbody")) {
    $("subnets_tbody").addEventListener("click", async (e) => {
      const btn = e.target.closest("button");
      if (!btn) return;
      const id = btn.getAttribute("data-id");
      const act = btn.getAttribute("data-act");
      if (!id || !act) return;
      if (act === "del") {
        await fetch(`/api/subnets/${id}`, { method: "DELETE" });
        logLine("warn", `Pool deleted: ${id}`);
      } else if (act === "scan") {
        await fetch(`/api/subnets/${id}/scan`, { method: "POST" });
        logLine("info", `Pool scan: start (${id})`);
      } else if (act === "stop") {
        await fetch(`/api/subnets/${id}/stop`, { method: "POST" });
        logLine("info", `Pool scan: stop (${id})`);
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
      logLine("info", `Pool ${enabled ? "enabled" : "disabled"}: ${id}`);
    });
  }

  // settings
  if ($("save_settings")) {
    $("save_settings").addEventListener("click", async () => {
      const cur = await fetchJSON("/api/settings");
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
      logLine("info", "Settings saved");
    });
  }
  if ($("exit_app")) {
    $("exit_app").addEventListener("click", async () => {
      if (!confirm("Exit MonA now? (This will stop scanning and free ports)")) return;
      logLine("warn", "Exit requested");
      await fetch("/api/admin/exit", { method: "POST" });
    });
  }

  // stored creds (encrypted)
  let editingID = "";
  const setEditing = (id, label) => {
    editingID = id || "";
    if ($("cred_editing")) $("cred_editing").textContent = label || (editingID ? `editing ${editingID}` : "creating new");
  };
  const clearForm = () => {
    if ($("cred_name")) $("cred_name").value = "";
    if ($("cred_vendor")) $("cred_vendor").value = "antminer";
    if ($("cred_firmware")) $("cred_firmware").value = "";
    if ($("cred_user")) $("cred_user").value = "";
    if ($("cred_pass")) $("cred_pass").value = "";
    if ($("cred_pri")) $("cred_pri").value = "0";
    if ($("cred_note")) $("cred_note").value = "";
    if ($("cred_enabled")) $("cred_enabled").checked = true;
    setEditing("", "creating new");
  };
  const renderStoredCreds = (rows) => {
    const tb = $("stored_creds_tbody");
    if (!tb) return;
    tb.innerHTML = "";
    (rows || []).forEach((c) => {
      const tr = document.createElement("tr");
      tr.innerHTML = `
        <td>${c.name || ""}</td>
        <td>${c.vendor || ""}</td>
        <td>${c.firmware || ""}</td>
        <td>${c.enabled ? "on" : "off"}</td>
        <td>${c.priority ?? 0}</td>
        <td>${c.note || ""}</td>
        <td>
          <button class="btn btn-sm" data-act="edit" data-id="${c.id}">Edit</button>
          <button class="btn btn-sm" data-act="del" data-id="${c.id}">Delete</button>
        </td>
      `;
      tb.appendChild(tr);
    });
  };
  const refreshStoredCreds = async () => {
    try {
      state.storedCreds = await fetchJSON("/api/creds");
      renderStoredCreds(state.storedCreds);
    } catch {
      // ignore
    }
  };
  if ($("cred_save")) {
    $("cred_save").addEventListener("click", async () => {
      const payload = {
        name: ($("cred_name").value || "").trim(),
        vendor: ($("cred_vendor").value || "").trim(),
        firmware: ($("cred_firmware").value || "").trim(),
        enabled: !!$("cred_enabled").checked,
        priority: Number(($("cred_pri").value || "0").trim()) || 0,
        note: ($("cred_note").value || "").trim(),
        username: ($("cred_user").value || "").trim(),
        password: ($("cred_pass").value || "").trim(),
      };
      if (!payload.name || !payload.vendor) return;
      if (!editingID) {
        await fetchJSON("/api/creds", { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify(payload) });
      } else {
        await fetchJSON(`/api/creds/${encodeURIComponent(editingID)}`, {
          method: "PATCH",
          headers: { "content-type": "application/json" },
          body: JSON.stringify(payload),
        });
      }
      clearForm();
      await refreshStoredCreds();
    });
  }
  if ($("cred_clear")) $("cred_clear").addEventListener("click", () => clearForm());
  if ($("stored_creds_tbody")) {
    $("stored_creds_tbody").addEventListener("click", async (e) => {
      const btn = e.target.closest("button");
      if (!btn) return;
      const id = btn.getAttribute("data-id");
      const act = btn.getAttribute("data-act");
      if (!id || !act) return;
      if (act === "del") {
        await fetch(`/api/creds/${encodeURIComponent(id)}`, { method: "DELETE" });
        await refreshStoredCreds();
        if (editingID === id) clearForm();
      }
      if (act === "edit") {
        const rows = state.storedCreds || [];
        const c = rows.find((x) => x.id === id);
        if (!c) return;
        $("cred_name").value = c.name || "";
        $("cred_vendor").value = c.vendor || "antminer";
        $("cred_firmware").value = c.firmware || "";
        $("cred_pri").value = String(c.priority ?? 0);
        $("cred_note").value = c.note || "";
        $("cred_enabled").checked = !!c.enabled;
        $("cred_user").value = "";
        $("cred_pass").value = "";
        setEditing(id, `editing ${c.name} • re-enter username/password to change`);
      }
    });
  }
  clearForm();
  // initial load
  refreshStoredCreds();
}

function connectSSEDevices() {
  setConn("warn");
  const es = new EventSource("/api/stream/devices");
  es.addEventListener("devices", (e) => {
    try {
      state.devices = JSON.parse(e.data);
      renderDevices(state.devices);
      setConn("ok");
    } catch {
      // ignore
    }
  });
  es.onerror = async () => {
    setConn("bad");
    es.close();
    logLine("warn", "SSE devices offline; polling until reconnect");
    for (;;) {
      try {
        state.devices = await fetchJSON("/api/devices");
        renderDevices(state.devices);
      } catch {
        // ignore
      }
      await new Promise((r) => setTimeout(r, 2000));
      try {
        connectSSEDevices();
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
  setRoute("dashboard");
  logLine("info", "UI started");

  try {
    const ver = await fetchText("/api/version");
    if ($("ver")) $("ver").textContent = ver.trim();
  } catch {
    // ignore
  }

  try {
    state.devices = await fetchJSON("/api/devices");
    renderDevices(state.devices);
  } catch {
    // ignore
  }

  try {
    state.subnets = await fetchJSON("/api/subnets");
    renderSubnets(state.subnets);
    renderScanSummary();
  } catch {
    // ignore
  }

  // no default creds UI (we rely on encrypted stored creds)

  connectSSEDevices();
  connectSSESubnets();

  // settings form
  try {
    const s = await fetchJSON("/api/settings");
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
      const st = await fetchJSON("/api/status");
      const emb = st.embedded_nats ? "embedded" : "external";

      // settings pill
      const el = $("nats_status");
      if (el) {
        if (st.nats_connected) {
          el.textContent = `nats: connected (${emb})`;
          el.classList.remove("pill-warn", "pill-bad");
          el.classList.add("pill-ok");
        } else {
          el.textContent = `nats: disconnected (${emb})`;
          el.classList.remove("pill-ok");
          el.classList.add("pill-bad");
        }
      }

      // dashboard core widget
      const core = $("core_nats");
      if (core) {
        core.textContent = st.nats_connected ? `nats: ok (${emb})` : `nats: down (${emb})`;
        core.classList.remove("pill-ok", "pill-warn", "pill-bad");
        core.classList.add(st.nats_connected ? "pill-ok" : "pill-bad");
      }
      if ($("core_embedded")) $("core_embedded").textContent = st.embedded_nats ? "enabled" : "disabled";
      if ($("core_started")) $("core_started").textContent = st.started_at ? fmtTs(st.started_at) : "—";
      if ($("core_uptime")) $("core_uptime").textContent = st.uptime_s ? fmtUptime(st.uptime_s) : "—";
    } catch {
      // ignore
    }
  }, 1500);
}

main();

