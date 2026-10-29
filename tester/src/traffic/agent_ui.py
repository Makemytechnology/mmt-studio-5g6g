# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# Standalone dashboard for the tester-owned Traffic Agent.
#
# Shipped as a single-file inline HTML page served at GET /. No dependency on
# the tester's main UI — this runs on the DN host and is independent.

AGENT_UI_HTML = """<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<title>SA Traffic Agent</title>
<meta name="viewport" content="width=device-width,initial-scale=1">
<style>
  :root {
    --bg:#0f1216; --panel:#181d24; --border:#272e38; --text:#e6e6e6;
    --muted:#8a94a6; --accent:#54b8ff; --ok:#4ade80; --warn:#fbbf24; --bad:#f87171;
    --mono:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;
  }
  * { box-sizing:border-box; }
  html,body { margin:0; padding:0; background:var(--bg); color:var(--text);
              font:14px/1.4 system-ui,-apple-system,Segoe UI,Roboto,sans-serif; }
  header { background:var(--panel); border-bottom:1px solid var(--border);
           padding:12px 20px; display:flex; align-items:center; gap:16px; }
  header h1 { margin:0; font-size:16px; letter-spacing:.02em; }
  .badge { padding:2px 8px; border-radius:10px; font-size:11px;
           background:var(--border); color:var(--muted); }
  .badge.ok { background:#052e18; color:var(--ok); }
  .badge.warn { background:#3b2705; color:var(--warn); }
  .badge.bad  { background:#3b0f0f; color:var(--bad); }
  main { padding:20px; max-width:1200px; margin:0 auto;
         display:grid; gap:16px; grid-template-columns:1fr 1fr; }
  .card { background:var(--panel); border:1px solid var(--border); border-radius:8px;
          padding:12px 14px; }
  .card.full { grid-column:1/-1; }
  .card h2 { margin:0 0 8px; font-size:13px; font-weight:600;
             color:var(--muted); text-transform:uppercase; letter-spacing:.08em; }
  table { width:100%; border-collapse:collapse; font-family:var(--mono); font-size:12px; }
  th,td { text-align:left; padding:6px 8px; border-bottom:1px solid var(--border);
          vertical-align:top; }
  th { color:var(--muted); font-weight:500; font-size:11px;
       text-transform:uppercase; letter-spacing:.06em; }
  tr:last-child td { border-bottom:none; }
  .kv { display:grid; grid-template-columns:130px 1fr; row-gap:4px; column-gap:10px;
        font-family:var(--mono); font-size:12px; }
  .kv div:nth-child(odd) { color:var(--muted); }
  .token-bar { background:#2a1a0a; border:1px solid #5a3c12; border-radius:6px;
               padding:8px 12px; margin:10px 20px 0; color:#f6c483;
               display:flex; align-items:center; gap:10px; }
  .token-bar input { flex:1; background:#1a1308; color:var(--text); border:1px solid #5a3c12;
                     border-radius:4px; padding:4px 8px; font-family:var(--mono); }
  .token-bar button { background:#3b2705; color:#fbbf24; border:1px solid #5a3c12;
                      border-radius:4px; padding:4px 12px; cursor:pointer; }
  .muted { color:var(--muted); }
  .status-running   { color:var(--accent); }
  .status-completed { color:var(--ok); }
  .status-error     { color:var(--bad); }
  .empty { color:var(--muted); font-style:italic; padding:8px; text-align:center; }
  .num { text-align:right; }
  footer { margin:24px 0 40px; text-align:center; color:var(--muted); font-size:11px; }
  a { color:var(--accent); }
</style>
</head>
<body>
<header>
  <h1>SA Traffic Agent</h1>
  <span id="hostname" class="badge">…</span>
  <span id="authBadge" class="badge">auth ?</span>
  <span id="iperfBadge" class="badge">iperf3 ?</span>
  <span style="flex:1"></span>
  <span id="refreshBadge" class="muted" style="font-size:11px;">—</span>
</header>
<div id="tokenBar" class="token-bar" hidden>
  This agent requires an <b>X-Agent-Token</b>.
  <input id="tokenInput" type="password" placeholder="paste token here" autocomplete="off">
  <button onclick="saveToken()">Use</button>
</div>
<main>

  <div class="card">
    <h2>Capabilities</h2>
    <div id="caps" class="kv"><div>loading…</div></div>
  </div>

  <div class="card">
    <h2>Endpoints</h2>
    <div class="kv">
      <div>Agent base</div>            <div id="epBase">…</div>
      <div>Healthz</div>               <div><code>GET /api/traffic/healthz</code></div>
      <div>Capabilities</div>          <div><code>GET /api/traffic/capabilities</code></div>
      <div>Start session</div>         <div><code>POST /api/traffic/start</code> (role=client|server)</div>
      <div>Get session</div>           <div><code>GET /api/traffic/sessions/{id}</code></div>
      <div>Stop session</div>          <div><code>POST /api/traffic/sessions/{id}/stop</code></div>
      <div>DN NIC counters</div>       <div><code>GET /api/traffic/dn-stats</code></div>
    </div>
  </div>

  <div class="card full">
    <h2>Active sessions</h2>
    <table id="tblSessions"><thead>
      <tr><th>Session</th><th>Role</th><th>Proto</th>
          <th>Src</th><th>Dst</th><th>Status</th>
          <th class="num">Throughput kbps</th></tr>
    </thead><tbody><tr><td colspan="7" class="empty">—</td></tr></tbody></table>
  </div>

  <div class="card full">
    <h2>Interface counters (/proc/net/dev)</h2>
    <table id="tblIfaces"><thead>
      <tr><th>Iface</th>
          <th class="num">RX bytes</th><th class="num">RX pkts</th>
          <th class="num">RX drop</th><th class="num">RX err</th>
          <th class="num">TX bytes</th><th class="num">TX pkts</th>
          <th class="num">TX drop</th><th class="num">TX err</th></tr>
    </thead><tbody><tr><td colspan="9" class="empty">—</td></tr></tbody></table>
  </div>

</main>
<footer>
  sa_tester traffic agent — tester-owned, independent of sa_core &nbsp;·&nbsp;
  reload page to re-read token · ui auto-refreshes every 3s
</footer>

<script>
const LS_KEY = 'sa_agent_token';
let token = sessionStorage.getItem(LS_KEY) || '';
let authRequired = false;

function saveToken() {
  token = document.getElementById('tokenInput').value.trim();
  sessionStorage.setItem(LS_KEY, token);
  document.getElementById('tokenBar').hidden = !!token;
  refreshAll();
}

async function api(path) {
  const h = {};
  if (token) h['X-Agent-Token'] = token;
  try {
    const r = await fetch(path, { headers: h });
    if (r.status === 401) {
      document.getElementById('tokenBar').hidden = false;
      return { _err: 401 };
    }
    return await r.json();
  } catch (e) {
    return { _err: String(e) };
  }
}

function fmt(n) {
  if (n == null || isNaN(n)) return '—';
  n = Number(n);
  if (n >= 1e9) return (n/1e9).toFixed(2)+'G';
  if (n >= 1e6) return (n/1e6).toFixed(2)+'M';
  if (n >= 1e3) return (n/1e3).toFixed(1)+'k';
  return String(n);
}
function esc(s) { return String(s ?? '').replace(/[&<>\"]/g, c =>
  ({'&':'&amp;','<':'&lt;','>':'&gt;','\"':'&quot;'}[c])); }

async function loadCaps() {
  const d = await api('/api/traffic/capabilities');
  if (d._err) return;
  authRequired = !!d.auth_required;
  document.getElementById('hostname').textContent = d.hostname || 'unknown';
  document.getElementById('iperfBadge').textContent = d.iperf3 || 'iperf3 missing';
  document.getElementById('iperfBadge').className = 'badge ' + (d.iperf3 ? 'ok' : 'bad');
  document.getElementById('authBadge').textContent = authRequired ? 'auth required' : 'auth open';
  document.getElementById('authBadge').className   = 'badge ' + (authRequired ? 'warn' : 'ok');
  if (authRequired && !token) document.getElementById('tokenBar').hidden = false;
  document.getElementById('epBase').textContent = location.origin;
  const el = document.getElementById('caps');
  el.innerHTML =
    `<div>hostname</div><div>${esc(d.hostname)}</div>`+
    `<div>protocols</div><div>${esc((d.protocols||[]).join(', '))}</div>`+
    `<div>roles</div><div>${esc((d.roles||[]).join(', '))}</div>`+
    `<div>iperf3</div><div>${esc(d.iperf3||'missing')}</div>`+
    `<div>ping</div><div>${d.ping ? 'yes' : 'no'}</div>`+
    `<div>auth</div><div>${authRequired ? 'X-Agent-Token required' : 'disabled'}</div>`;
}

async function loadSessions() {
  const d = await api('/api/traffic/active');
  const tb = document.querySelector('#tblSessions tbody');
  if (d._err === 401) {
    tb.innerHTML = '<tr><td colspan=\"7\" class=\"empty\">token required</td></tr>';
    return;
  }
  const items = (d && d.items) || [];
  if (!items.length) {
    tb.innerHTML = '<tr><td colspan=\"7\" class=\"empty\">no active sessions</td></tr>';
    return;
  }
  tb.innerHTML = items.map(s => `
    <tr>
      <td>${esc(s.id)}</td>
      <td>${esc(s.direction || s.role || '')}</td>
      <td>${esc(s.protocol)}</td>
      <td>${esc(s.src)}</td>
      <td>${esc(s.dst)}</td>
      <td class=\"status-${esc(s.status)}\">${esc(s.status)}</td>
      <td class=\"num\">—</td>
    </tr>`).join('');
}

async function loadIfaces() {
  const d = await api('/api/traffic/dn-stats');
  const tb = document.querySelector('#tblIfaces tbody');
  if (d._err === 401) {
    tb.innerHTML = '<tr><td colspan=\"9\" class=\"empty\">token required</td></tr>';
    return;
  }
  const items = (d && d.interfaces) || [];
  if (!items.length) {
    tb.innerHTML = '<tr><td colspan=\"9\" class=\"empty\">no interfaces</td></tr>';
    return;
  }
  tb.innerHTML = items.map(i => `
    <tr>
      <td>${esc(i.iface)}</td>
      <td class=\"num\">${fmt(i.rx_bytes)}</td>
      <td class=\"num\">${fmt(i.rx_packets)}</td>
      <td class=\"num\">${fmt(i.rx_dropped)}</td>
      <td class=\"num\">${fmt(i.rx_errors)}</td>
      <td class=\"num\">${fmt(i.tx_bytes)}</td>
      <td class=\"num\">${fmt(i.tx_packets)}</td>
      <td class=\"num\">${fmt(i.tx_dropped)}</td>
      <td class=\"num\">${fmt(i.tx_errors)}</td>
    </tr>`).join('');
}

async function refreshAll() {
  await loadCaps();
  await Promise.all([loadSessions(), loadIfaces()]);
  document.getElementById('refreshBadge').textContent =
    'last: ' + new Date().toLocaleTimeString();
}

refreshAll();
setInterval(refreshAll, 3000);
</script>
</body>
</html>
"""
