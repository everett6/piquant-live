package dashboard

// The interface is one page plus one login form, both embedded in the binary so
// a Pi deployment needs no static-file directory and no build step on the
// board. Everything is inline: no CDN, no font download, no script from another
// origin, which is also why the CSP can be as tight as it is.

// loginHTML is the token prompt.
const loginHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>PiQuant sign in</title>
<style>
  :root{color-scheme:dark}
  *{box-sizing:border-box}
  body{margin:0;min-height:100vh;display:grid;place-items:center;
    background:radial-gradient(1200px 600px at 50% -10%,#16324a 0%,#0b1016 55%,#070a0e 100%);
    color:#e6edf3;font:15px/1.5 ui-sans-serif,system-ui,-apple-system,"Segoe UI",Roboto,sans-serif}
  .card{width:min(92vw,380px);padding:32px 28px;border-radius:16px;
    background:rgba(20,27,36,.86);border:1px solid #24303d;
    box-shadow:0 24px 64px rgba(0,0,0,.55)}
  .mark{display:flex;align-items:center;gap:10px;margin-bottom:18px}
  .dot{width:12px;height:12px;border-radius:50%;background:#3ddc97;
    box-shadow:0 0 0 4px rgba(61,220,151,.15)}
  h1{margin:0;font-size:19px;font-weight:650;letter-spacing:.2px}
  p.sub{margin:0 0 22px;color:#8b9bb0;font-size:13px}
  label{display:block;font-size:12px;text-transform:uppercase;letter-spacing:.08em;
    color:#8b9bb0;margin-bottom:8px}
  input{width:100%;padding:12px 14px;border-radius:10px;border:1px solid #2b3949;
    background:#0d131a;color:#e6edf3;font:inherit;font-variant-numeric:tabular-nums}
  input:focus{outline:none;border-color:#3ddc97;box-shadow:0 0 0 3px rgba(61,220,151,.16)}
  button{margin-top:18px;width:100%;padding:12px;border:0;border-radius:10px;
    background:#3ddc97;color:#04150d;font-weight:650;font-size:14px;cursor:pointer}
  button:hover{background:#4ce8a6}
  .err{margin:0 0 16px;padding:10px 12px;border-radius:8px;font-size:13px;
    background:rgba(255,93,93,.12);color:#ff9c9c;border:1px solid rgba(255,93,93,.25)}
  .hint{margin-top:18px;font-size:12px;color:#66788e;line-height:1.55}
  code{background:#0d131a;border:1px solid #24303d;border-radius:6px;padding:1px 6px;
    font:12px ui-monospace,SFMono-Regular,Menlo,monospace;color:#a9c7e8}
</style>
</head>
<body>
<form class="card" method="post" action="/login" autocomplete="off">
  <div class="mark"><span class="dot"></span><h1>PiQuant</h1></div>
  <p class="sub">Read-only research control panel</p>
  {{NOTE}}
  <label for="token">Dashboard token</label>
  <input id="token" name="token" type="password" autofocus required spellcheck="false">
  <button type="submit">Sign in</button>
  <p class="hint">The token is printed by <code>piquant setup</code> and stored in
  <code>~/.config/piquant/secrets</code> on this board. It is never logged.</p>
</form>
</body>
</html>
`

// indexHTML is the control panel. It renders from /api/events (SSE) and falls
// back to /api/status polling when the stream drops.
const indexHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>PiQuant Control Center</title>
<style>
  :root{
    color-scheme:dark;
    --bg:#080b10; --panel:#111823; --panel2:#0d141d; --line:#1f2c3a;
    --text:#e6edf3; --muted:#8496ab; --accent:#3ddc97; --warn:#ffb454;
    --bad:#ff6b6b; --info:#63b3ff;
  }
  *{box-sizing:border-box}
  body{margin:0;background:
      radial-gradient(1100px 520px at 12% -10%,rgba(29,78,120,.35),transparent 60%),
      radial-gradient(900px 500px at 95% 0%,rgba(24,120,90,.16),transparent 55%),
      var(--bg);
    color:var(--text);font:14px/1.55 ui-sans-serif,system-ui,-apple-system,"Segoe UI",Roboto,sans-serif}
  header{display:flex;align-items:center;gap:14px;padding:18px 22px;
    border-bottom:1px solid var(--line);backdrop-filter:blur(6px);
    position:sticky;top:0;background:rgba(8,11,16,.82);z-index:5}
  .brand{display:flex;align-items:center;gap:10px;font-weight:650;letter-spacing:.2px}
  .pulse{width:10px;height:10px;border-radius:50%;background:var(--accent);
    box-shadow:0 0 0 0 rgba(61,220,151,.5);animation:pulse 2.4s infinite}
  @keyframes pulse{0%{box-shadow:0 0 0 0 rgba(61,220,151,.45)}
    70%{box-shadow:0 0 0 9px rgba(61,220,151,0)}100%{box-shadow:0 0 0 0 rgba(61,220,151,0)}}
  .grow{flex:1}
  .meta{color:var(--muted);font-size:12px;font-variant-numeric:tabular-nums}
  .pill{font-size:11px;padding:3px 9px;border-radius:999px;border:1px solid var(--line);
    color:var(--muted);background:var(--panel2)}
  .pill.ok{color:var(--accent);border-color:rgba(61,220,151,.35);background:rgba(61,220,151,.08)}
  .pill.bad{color:var(--bad);border-color:rgba(255,107,107,.35);background:rgba(255,107,107,.08)}
  main{max-width:1240px;margin:0 auto;padding:22px}
  .grid{display:grid;gap:16px;grid-template-columns:repeat(12,1fr)}
  .card{grid-column:span 12;background:linear-gradient(180deg,rgba(255,255,255,.02),transparent),var(--panel);
    border:1px solid var(--line);border-radius:14px;padding:16px 18px}
  .card h2{margin:0 0 12px;font-size:12px;letter-spacing:.1em;text-transform:uppercase;color:var(--muted);font-weight:600}
  @media(min-width:760px){ .span6{grid-column:span 6} .span4{grid-column:span 4} .span8{grid-column:span 8} }
  .stats{display:grid;grid-template-columns:repeat(auto-fit,minmax(140px,1fr));gap:12px}
  .stat{background:var(--panel2);border:1px solid var(--line);border-radius:11px;padding:12px 14px}
  .stat .k{font-size:11px;color:var(--muted);text-transform:uppercase;letter-spacing:.08em}
  .stat .v{font-size:22px;font-weight:650;margin-top:4px;font-variant-numeric:tabular-nums}
  .stat .s{font-size:11px;color:var(--muted);margin-top:2px}
  .bar{height:7px;border-radius:99px;background:#0a1119;border:1px solid var(--line);overflow:hidden;margin-top:8px}
  .bar>i{display:block;height:100%;background:linear-gradient(90deg,#3ddc97,#63b3ff);width:0;transition:width .4s ease}
  .bar.warn>i{background:linear-gradient(90deg,#ffb454,#ff8a5c)}
  table{width:100%;border-collapse:collapse;font-size:13px}
  th{text-align:left;font-weight:600;color:var(--muted);font-size:11px;text-transform:uppercase;
    letter-spacing:.08em;padding:6px 8px;border-bottom:1px solid var(--line)}
  td{padding:8px;border-bottom:1px solid rgba(31,44,58,.55);vertical-align:top}
  tr:last-child td{border-bottom:0}
  .mono{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:12px}
  .dot{display:inline-block;width:8px;height:8px;border-radius:50%;margin-right:7px;background:var(--muted)}
  .dot.ok{background:var(--accent)} .dot.bad{background:var(--bad)} .dot.warn{background:var(--warn)}
  .checks{display:grid;gap:6px}
  .check{display:flex;gap:10px;align-items:baseline;font-size:13px}
  .check .d{color:var(--muted)}
  .logs{max-height:340px;overflow:auto;background:#070b10;border:1px solid var(--line);
    border-radius:10px;padding:10px 12px;font:12px ui-monospace,SFMono-Regular,Menlo,monospace;
    white-space:pre-wrap;word-break:break-word}
  .logs .lvl-ERROR{color:#ff9c9c} .logs .lvl-WARN{color:#ffc978}
  .logs .lvl-INFO{color:#9fd0ff} .logs .lvl-DEBUG{color:#7d8ea3}
  .row{display:flex;gap:8px;flex-wrap:wrap;align-items:center;margin-bottom:10px}
  input,select,button{font:inherit;color:var(--text);background:var(--panel2);
    border:1px solid var(--line);border-radius:8px;padding:7px 10px}
  input:focus,select:focus{outline:none;border-color:var(--accent)}
  button{cursor:pointer} button:hover{border-color:#2d4054}
  .empty{color:var(--muted);font-size:13px;padding:10px 2px}
  .temp{font-variant-numeric:tabular-nums}
  footer{max-width:1240px;margin:8px auto 28px;padding:0 22px;color:#5d6f84;font-size:12px}
  a{color:var(--info);text-decoration:none} a:hover{text-decoration:underline}
</style>
</head>
<body>
<header>
  <div class="brand"><span class="pulse"></span>PiQuant Control Center</div>
  <span id="role" class="pill">connecting</span>
  <span id="stream" class="pill">live</span>
  <div class="grow"></div>
  <span id="host" class="meta"></span>
  <a class="meta" href="/logout">sign out</a>
</header>

<main>
  <div class="grid">
    <section class="card span8">
      <h2>This board</h2>
      <div class="stats">
        <div class="stat"><div class="k">CPU</div><div class="v" id="cpu">-</div>
          <div class="bar"><i id="cpuBar"></i></div></div>
        <div class="stat"><div class="k">Memory</div><div class="v" id="mem">-</div>
          <div class="bar" id="memWrap"><i id="memBar"></i></div></div>
        <div class="stat"><div class="k">Temperature</div><div class="v temp" id="temp">-</div>
          <div class="s" id="tempNote">&nbsp;</div></div>
        <div class="stat"><div class="k">Load</div><div class="v" id="load">-</div>
          <div class="s" id="loadNote">&nbsp;</div></div>
      </div>
      <div class="row" style="margin-top:14px">
        <span class="pill" id="throttle">power: unknown</span>
        <span class="pill" id="disk">disk: -</span>
        <span class="pill" id="uptime">uptime: -</span>
      </div>
    </section>

    <section class="card span4">
      <h2>Setup checks</h2>
      <div class="checks" id="checks"><div class="empty">waiting for the first sample...</div></div>
    </section>

    <section class="card span6">
      <h2>Research engine</h2>
      <div id="engine"><div class="empty">no engine reported yet</div></div>
    </section>

    <section class="card span6">
      <h2>Network peers</h2>
      <div id="peers"><div class="empty">searching the local network...</div></div>
    </section>

    <section class="card span12">
      <h2>Experiments</h2>
      <div id="jobs"><div class="empty">no experiments running</div></div>
    </section>

    <section class="card span12">
      <h2>Logs</h2>
      <div class="row">
        <select id="logFile"></select>
        <select id="logLevel">
          <option value="">all levels</option>
          <option value="ERROR">error</option>
          <option value="WARN">warn</option>
          <option value="INFO">info</option>
        </select>
        <input id="logQuery" placeholder="filter text" spellcheck="false">
        <button id="logLoad" type="button">load</button>
        <span class="meta" id="logMeta"></span>
      </div>
      <div class="logs" id="logBody">Select a log file and press load.</div>
    </section>
  </div>
</main>
<footer id="footer"></footer>

<script>
(function(){
  "use strict";
  var $ = function(id){ return document.getElementById(id); };

  function esc(s){
    return String(s == null ? "" : s).replace(/[&<>"']/g, function(c){
      return {"&":"&amp;","<":"&lt;",">":"&gt;","\"":"&quot;","'":"&#39;"}[c];
    });
  }
  function num(v, d){
    if (v === null || v === undefined || (typeof v === "number" && !isFinite(v))) return "-";
    return Number(v).toFixed(d);
  }
  function bytes(n){
    if (!n && n !== 0) return "-";
    var u = ["B","KiB","MiB","GiB","TiB"], i = 0;
    while (n >= 1024 && i < u.length-1){ n /= 1024; i++; }
    return (i === 0 ? n : n.toFixed(1)) + " " + u[i];
  }
  function setBar(el, pct, warnAt){
    var wrap = el.parentNode;
    el.style.width = Math.max(0, Math.min(100, pct)) + "%";
    if (wrap) wrap.className = "bar" + (pct >= warnAt ? " warn" : "");
  }

  function renderBoard(st){
    var h = st.host || {};
    $("host").textContent = (h.hostname || "?") + "  .  " + (h.model || h.arch || "");
    var role = $("role");
    role.textContent = st.role + "  " + (st.app || "");
    role.className = "pill ok";

    var busy = h.cpu_busy;
    $("cpu").textContent = (busy === null || busy === undefined || isNaN(busy))
      ? "-" : Math.round(busy * 100) + "%";
    setBar($("cpuBar"), busy * 100, 85);

    if (h.mem_total_bytes){
      var used = 1 - h.mem_available_bytes / h.mem_total_bytes;
      $("mem").textContent = Math.round(used * 100) + "%";
      $("mem").title = bytes(h.mem_available_bytes) + " available of " + bytes(h.mem_total_bytes);
      setBar($("memBar"), used * 100, 85);
    }
    $("temp").textContent = h.temp_ok ? num(h.temp_c, 1) + " C" : "n/a";
    $("tempNote").textContent = h.temp_ok ? "SoC" : "no sensor";
    $("load").textContent = num(h.load1, 2);
    $("loadNote").textContent = num(h.load5,2) + " / " + num(h.load15,2) + " over " + (h.cpus||"?") + " cores";

    var th = h.throttle || {};
    var tp = $("throttle");
    if (!th.available){ tp.textContent = "power: no firmware flags"; tp.className = "pill"; }
    else {
      var bad = th.under_voltage || th.throttled;
      tp.textContent = bad ? "power: " + (th.under_voltage ? "under-voltage" : "") +
        (th.under_voltage && th.throttled ? ", " : "") + (th.throttled ? "throttled" : "")
        : "power: healthy";
      tp.className = "pill " + (bad ? "bad" : "ok");
    }
    var d = (h.disks && h.disks[0]) || null;
    $("disk").textContent = d ? "disk: " + num(d.used_percent,0) + "% used, " + bytes(d.free_bytes) + " free" : "disk: -";
    $("uptime").textContent = h.uptime_seconds ? "up " + humanDur(h.uptime_seconds) : "uptime: -";

    var ck = $("checks");
    if (st.checks && st.checks.length){
      ck.innerHTML = st.checks.map(function(c){
        return "<div class=check><span class=dot " + (c.ok ? "ok" : "bad") + "></span><b>" +
          esc(c.name) + "</b>" + (c.detail ? "<span class=d>" + esc(c.detail) + "</span>" : "") + "</div>";
      }).join("");
    } else ck.innerHTML = "<div class=empty>no checks reported</div>";
  }

  function humanDur(s){
    s = Math.floor(s);
    var d = Math.floor(s/86400), h = Math.floor(s%86400/3600), m = Math.floor(s%3600/60);
    if (d) return d + "d " + h + "h";
    if (h) return h + "h " + m + "m";
    return m + "m";
  }

  function renderEngine(st){
    var e = st.engine || {};
    var el = $("engine");
    if (!e.running && !e.message && !e.phase){
      el.innerHTML = "<div class=empty>idle</div>"; return;
    }
    var pct = Math.round((e.progress || 0) * 100);
    el.innerHTML =
      "<div class=stats>" +
      "<div class=stat><div class=k>Phase</div><div class=v>" + esc(e.phase || "idle") + "</div>" +
        "<div class=s>" + esc(e.dataset || "no dataset") + "</div></div>" +
      "<div class=stat><div class=k>Progress</div><div class=v>" + pct + "%</div>" +
        "<div class=bar><i style='width:" + pct + "%'></i></div></div>" +
      "<div class=stat><div class=k>Events</div><div class=v>" + (e.events||0).toLocaleString() + "</div>" +
        "<div class=s>" + (e.rejects||0) + " rejected</div></div>" +
      "</div>" +
      (e.message ? "<div class=empty style='margin-top:10px'>" + esc(e.message) + "</div>" : "");
  }

  function renderPeers(st){
    var ps = st.peers || [];
    var el = $("peers");
    if (!ps.length){ el.innerHTML = "<div class=empty>no peer heard on the local network</div>"; return; }
    el.innerHTML = "<table><thead><tr><th>Role</th><th>Host</th><th>Address</th><th>State</th><th>Auth</th></tr></thead><tbody>" +
      ps.map(function(p){
        var state = p.live ? "<span class='dot ok'></span>live (" + p.latency_ms + " ms)"
                           : "<span class='dot warn'></span>beacon only";
        return "<tr><td>" + esc(p.role) + "</td><td>" + esc(p.name) + "</td><td class=mono>" +
          esc(p.addr) + "</td><td>" + state + "</td><td>" +
          (p.authenticated ? "<span class='pill ok'>verified</span>" : "<span class='pill'>open</span>") +
          (p.error ? "<div class=meta>" + esc(p.error) + "</div>" : "") + "</td></tr>";
      }).join("") + "</tbody></table>";
  }

  function renderJobs(st){
    var js = st.jobs || [];
    var el = $("jobs");
    if (!js.length){ el.innerHTML = "<div class=empty>no experiments running</div>"; return; }
    el.innerHTML = "<table><thead><tr><th>Job</th><th>State</th><th>Progress</th><th>Result</th></tr></thead><tbody>" +
      js.map(function(j){
        return "<tr><td>" + esc(j.name) + "<div class=meta>" + esc(j.id) + "</div></td><td>" +
          esc(j.state) + "</td><td>" + Math.round((j.progress||0)*100) + "%</td><td>" +
          esc(j.result || j.error || "") + "</td></tr>";
      }).join("") + "</tbody></table>";
  }

  function render(st){
    renderBoard(st); renderEngine(st); renderPeers(st); renderJobs(st);
    $("footer").textContent = "PiQuant " + (st.app||"") + "  .  " + (st.name||"") +
      "  .  started " + new Date(st.started_at).toLocaleString() +
      "  .  sample " + new Date(st.now).toLocaleTimeString();
  }

  function fail(){
    $("stream").textContent = "reconnecting";
    $("stream").className = "pill bad";
  }
  function ok(){
    $("stream").textContent = "live";
    $("stream").className = "pill ok";
  }

  // Server-Sent Events keep the page current without a polling loop. When the
  // stream is unavailable the page falls back to /api/status.
  var pollTimer = null;
  function startPolling(){
    if (pollTimer) return;
    pollTimer = setInterval(function(){
      fetch("/api/status", {credentials:"same-origin"})
        .then(function(r){ if (!r.ok) throw new Error("status " + r.status); return r.json(); })
        .then(function(st){ ok(); render(st); })
        .catch(fail);
    }, 4000);
  }
  function startStream(){
    if (!window.EventSource) { startPolling(); return; }
    var es = new EventSource("/api/events");
    es.addEventListener("status", function(ev){
      ok();
      try { render(JSON.parse(ev.data)); } catch (e) {}
    });
    es.onerror = function(){
      // A 401 closes the stream for good; polling surfaces the same state.
      es.close(); fail(); startPolling();
    };
  }

  // Log viewer ----------------------------------------------------------------
  var logsLoaded = false;
  function loadLogs(){
    var file = $("logFile").value;
    var q = $("logQuery").value.trim();
    var level = $("logLevel").value;
    var url = "/api/logs?tail=400&file=" + encodeURIComponent(file) +
      "&level=" + encodeURIComponent(level) + "&q=" + encodeURIComponent(q);
    $("logMeta").textContent = "loading...";
    fetch(url, {credentials:"same-origin"})
      .then(function(r){ return r.json(); })
      .then(function(d){
        if (!logsLoaded){
          logsLoaded = true;
          $("logFile").innerHTML = (d.files||[]).map(function(f){
            return "<option>" + esc(f) + "</option>";
          }).join("") || "<option value=''>no logs</option>";
        }
        $("logMeta").textContent = (d.scanned||0) + " lines scanned, " + (d.matched||0) + " matched";
        var body = $("logBody");
        if (d.error){ body.textContent = d.error; return; }
        if (!d.lines || !d.lines.length){ body.textContent = "no matching lines"; return; }
        body.innerHTML = d.lines.map(function(l){
          return "<div class='" + (l.level ? "lvl-" + esc(l.level) : "") + "'>" + esc(l.text) + "</div>";
        }).join("");
        body.scrollTop = body.scrollHeight;
      })
      .catch(function(e){ $("logMeta").textContent = "failed: " + e.message; });
  }
  $("logLoad").addEventListener("click", loadLogs);
  $("logFile").addEventListener("change", loadLogs);
  $("logLevel").addEventListener("change", loadLogs);
  $("logQuery").addEventListener("keydown", function(e){ if (e.key === "Enter") loadLogs(); });

  startStream();
})();
</script>
</body>
</html>
`
