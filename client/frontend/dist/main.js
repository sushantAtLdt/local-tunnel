// ── DOM refs ──────────────────────────────────────────────────
const localTargetInput = document.getElementById("localTarget");
const toggleGatewayBtn = document.getElementById("toggleGatewayBtn");
const singleTargetBox  = document.getElementById("singleTargetBox");
const gatewayTargetBox = document.getElementById("gatewayTargetBox");
const gatewayRoutes    = document.getElementById("gatewayRoutes");

const logBody    = document.getElementById("log");
const logEmpty   = document.getElementById("logEmpty");
const reqCountEl = document.getElementById("reqCount");
const clearBtn   = document.getElementById("clearBtn");
const logBadge   = document.getElementById("logBadge");

let isGatewayMode = false;
let reqCount      = 0;
let unreadCount   = 0;

// ── Tab switching ─────────────────────────────────────────────
document.querySelectorAll(".tab").forEach(btn => {
  btn.addEventListener("click", () => {
    const target = btn.dataset.tab;
    document.querySelectorAll(".tab").forEach(t => t.classList.remove("active"));
    document.querySelectorAll(".tab-content").forEach(c => c.classList.remove("active"));
    btn.classList.add("active");
    document.getElementById("tab-" + target).classList.add("active");
    // Clear badge when switching to logs
    if (target === "logs") {
      unreadCount = 0;
      logBadge.style.display = "none";
      logBadge.textContent = "0";
    }
  });
});

// ── Gateway mode toggle ──────────────────────────────────────
toggleGatewayBtn.addEventListener("click", () => {
  isGatewayMode = !isGatewayMode;
  if (isGatewayMode) {
    singleTargetBox.classList.add("hidden");
    gatewayTargetBox.classList.remove("hidden");
    toggleGatewayBtn.textContent = "Single URL";
    toggleGatewayBtn.classList.add("active");
    if (!gatewayRoutes.value.trim()) {
      gatewayRoutes.value = [
        "/dfs-authorization-server  -> http://127.0.0.1:8082",
        "/dfs-login-management      -> http://127.0.0.1:8085",
        "/dfs-user-management       -> http://127.0.0.1:8086",
        "/mfs-user-management       -> http://127.0.0.1:9085",
        "/dfs-otp-management        -> http://127.0.0.1:8084",
        "/contract-management-service -> http://127.0.0.1:8083",
        "/dfs-core                  -> http://127.0.0.1:8800",
        "/escrow-support-service    -> http://127.0.0.1:8092",
        "/escrow-financial-service  -> http://127.0.0.1:8088",
        "/dfs-balance-transfer      -> http://127.0.0.1:9096",
        "/                          -> http://127.0.0.1:3000",
      ].join("\n");
    }
  } else {
    singleTargetBox.classList.remove("hidden");
    gatewayTargetBox.classList.add("hidden");
    toggleGatewayBtn.textContent = "Gateway Mode";
    toggleGatewayBtn.classList.remove("active");
  }
});

function getLocalTarget() {
  return isGatewayMode ? gatewayRoutes.value.trim() : localTargetInput.value.trim();
}

function setTargetInputsDisabled(disabled) {
  localTargetInput.disabled = disabled;
  gatewayRoutes.disabled    = disabled;
  toggleGatewayBtn.disabled = disabled;
}

// ── Clear ────────────────────────────────────────────────────
clearBtn.addEventListener("click", () => {
  logBody.innerHTML = "";
  logBody.appendChild(logEmpty);
  logEmpty.classList.remove("hidden");
  reqCount = unreadCount = 0;
  reqCountEl.textContent = "0 requests";
  logBadge.style.display = "none";
});

// ── Helpers ──────────────────────────────────────────────────
function nowTime() {
  return new Date().toLocaleTimeString("en-GB", { hour12: false });
}

function statusCls(code) {
  const n = parseInt(code, 10);
  if (n >= 500) return "s-5xx";
  if (n >= 400) return "s-4xx";
  if (n >= 300) return "s-3xx";
  return "s-2xx";
}

function methodCls(m) {
  const known = ["GET","POST","PUT","PATCH","DELETE","HEAD","OPTIONS"];
  return "m-" + (known.includes(m) ? m : "SYS");
}

// Bump the badge on the Logs tab when the tab is not active
function bumpBadge() {
  const logsTab = document.querySelector('[data-tab="logs"]');
  if (!logsTab.classList.contains("active")) {
    unreadCount++;
    logBadge.textContent = unreadCount > 99 ? "99+" : unreadCount;
    logBadge.style.display = "";
  }
}

// ── Append a log row ─────────────────────────────────────────
function appendRow(kind, cells) {
  logEmpty.classList.add("hidden");

  const row = document.createElement("div");
  row.className = "log-row" + (kind === "err" ? " r-err" : kind === "sys" ? " r-sys" : "");

  const tEl = document.createElement("span");
  tEl.className = "lc-time";
  tEl.textContent = nowTime();

  if (kind === "req") {
    const { method, path, status, upstream, duration } = cells;

    const mEl = document.createElement("span");
    mEl.className = "method " + methodCls(method);
    mEl.textContent = method;

    const pEl = document.createElement("span");
    pEl.className = "lc-path";
    pEl.textContent = path;
    pEl.title = path;

    const uEl = document.createElement("span");
    uEl.className = "lc-up";
    uEl.textContent = upstream;
    uEl.title = upstream;

    const sEl = document.createElement("span");
    sEl.className = "status " + statusCls(status);
    sEl.textContent = status;

    const ms = parseInt(duration, 10);
    const dEl = document.createElement("span");
    dEl.className = "lc-dur" + (ms > 1000 ? " slow" : "");
    dEl.textContent = duration;

    row.append(tEl, mEl, pEl, uEl, sEl, dEl);

    reqCount++;
    reqCountEl.textContent = reqCount + (reqCount === 1 ? " request" : " requests");
    bumpBadge();
  } else {
    // System / error — span all remaining columns
    const mEl = document.createElement("span");
    mEl.className = "method m-SYS";
    mEl.textContent = kind === "err" ? "ERR" : "SYS";

    const pEl = document.createElement("span");
    pEl.className = "lc-path";
    pEl.style.gridColumn = "3 / -1";
    pEl.textContent = cells.message || "";
    pEl.title = cells.message || "";

    row.append(tEl, mEl, pEl);
  }

  logBody.appendChild(row);
  // Cap at 500 rows
  const rows = logBody.querySelectorAll(".log-row");
  if (rows.length > 500) rows[0].remove();
  logBody.scrollTop = logBody.scrollHeight;
}

// ── Main log handler ─────────────────────────────────────────
// Backend sends: "REQ|METHOD|PATH|STATUS|UPSTREAM_HOST|DURATION"
//           or plain text for system/error messages
function handleLog(line, isErr = false) {
  if (!line) return;

  if (line.startsWith("REQ|")) {
    const parts = line.split("|");
    if (parts.length >= 6) {
      appendRow("req", {
        method:   parts[1],
        path:     parts[2],
        status:   parts[3],
        upstream: parts[4],
        duration: parts[5],
      });
      return;
    }
  }
  appendRow(isErr ? "err" : "sys", { message: line });
}

// ── LAN mode ─────────────────────────────────────────────────
const lanPort   = document.getElementById("lanPort");
const lanCors   = document.getElementById("lanCors");
const lanBtn    = document.getElementById("lanBtn");
const lanDot    = document.getElementById("lanDot");
const lanUrlPill = document.getElementById("lanUrlPill");
const lanUrl    = document.getElementById("lanUrl");
let lanActive = false;

lanBtn.addEventListener("click", async () => {
  if (!lanActive) {
    const localTarget = getLocalTarget();
    const portSpec    = lanPort.value.trim();
    if (!localTarget || !portSpec) {
      handleLog("local app address and port are required", true); return;
    }
    lanBtn.disabled = true;
    try {
      const url = await window.go.main.App.StartLAN(localTarget, portSpec, lanCors.checked);
      lanActive = true;
      lanDot.classList.add("live");
      lanBtn.textContent = "Stop sharing";
      lanBtn.classList.add("stop");
      lanPort.disabled = lanCors.disabled = true;
      setTargetInputsDisabled(true);
      lanUrl.textContent = url;
      lanUrlPill.classList.remove("hidden");
      handleLog("LAN sharing started → " + url);
    } catch (err) {
      handleLog(String(err), true);
    } finally {
      lanBtn.disabled = false;
    }
  } else {
    await window.go.main.App.StopLAN();
    lanActive = false;
    lanDot.classList.remove("live");
    lanBtn.textContent = "Share on local network";
    lanBtn.classList.remove("stop");
    lanPort.disabled = lanCors.disabled = false;
    if (!pubActive) setTargetInputsDisabled(false);
    lanUrlPill.classList.add("hidden");
    handleLog("LAN sharing stopped");
  }
});

// ── Public tunnel mode ───────────────────────────────────────
const relayInput = document.getElementById("relayAddr");
const subInput   = document.getElementById("subdomain");
const pubCors    = document.getElementById("pubCors");
const pubBtn     = document.getElementById("pubBtn");
const pubDot     = document.getElementById("pubDot");
const pubUrlPill = document.getElementById("pubUrlPill");
const pubUrl     = document.getElementById("pubUrl");
let pubActive = false;

pubBtn.addEventListener("click", async () => {
  if (!pubActive) {
    const relayAddr   = relayInput.value.trim();
    const localTarget = getLocalTarget();
    const subdomain   = subInput.value.trim();
    if (!relayAddr || !localTarget) {
      handleLog("relay address and local app address are required", true); return;
    }
    pubBtn.disabled = true;
    try {
      const assigned = await window.go.main.App.StartTunnel(
        relayAddr, subdomain, localTarget, pubCors.checked
      );
      pubActive = true;
      pubDot.classList.add("live");
      pubBtn.textContent = "Stop public tunnel";
      pubBtn.classList.add("stop");
      relayInput.disabled = subInput.disabled = pubCors.disabled = true;
      setTargetInputsDisabled(true);
      pubUrl.textContent = assigned;
      pubUrlPill.classList.remove("hidden");
      handleLog("Public tunnel live — subdomain: " + assigned);
    } catch (err) {
      handleLog(String(err), true);
    } finally {
      pubBtn.disabled = false;
    }
  } else {
    await window.go.main.App.StopTunnel();
    pubActive = false;
    pubDot.classList.remove("live");
    pubBtn.textContent = "Start public tunnel";
    pubBtn.classList.remove("stop");
    relayInput.disabled = subInput.disabled = pubCors.disabled = false;
    if (!lanActive) setTargetInputsDisabled(false);
    pubUrlPill.classList.add("hidden");
    handleLog("Public tunnel stopped");
  }
});

// ── Wails event ──────────────────────────────────────────────
window.runtime.EventsOn("log", (line) => handleLog(line));
