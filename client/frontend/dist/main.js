// ─── DOM refs ───────────────────────────────────────────────────
const localTargetInput  = document.getElementById("localTarget");
const toggleGatewayBtn  = document.getElementById("toggleGatewayBtn");
const singleTargetBox   = document.getElementById("singleTargetBox");
const gatewayTargetBox  = document.getElementById("gatewayTargetBox");
const gatewayRoutes     = document.getElementById("gatewayRoutes");
const logBody           = document.getElementById("log");
const logEmpty          = document.getElementById("logEmpty");
const reqCountEl        = document.getElementById("reqCount");
const clearBtn          = document.getElementById("clearBtn");

let isGatewayMode = false;
let reqCount = 0;

// ─── Gateway Mode toggle ─────────────────────────────────────────
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

// ─── Clear log ───────────────────────────────────────────────────
clearBtn.addEventListener("click", () => {
  logBody.innerHTML = "";
  logBody.appendChild(logEmpty);
  logEmpty.classList.remove("hidden");
  reqCount = 0;
  reqCountEl.textContent = "0 requests";
});

// ─── Log helpers ─────────────────────────────────────────────────
function nowTime() {
  return new Date().toLocaleTimeString("en-GB", { hour12: false });
}

function methodClass(m) {
  return "m-" + (["GET","POST","PUT","PATCH","DELETE","HEAD","OPTIONS"].includes(m) ? m : "default");
}

function statusClass(code) {
  const n = parseInt(code, 10);
  if (n >= 500) return "s-5xx";
  if (n >= 400) return "s-4xx";
  if (n >= 300) return "s-3xx";
  return "s-2xx";
}

function durClass(durStr) {
  // Mark slow if > 1000ms
  const ms = parseInt(durStr, 10);
  return ms > 1000 ? "slow" : "";
}

/** Adds a row to the log table.
 *  For request lines (REQ|…) it fills all columns.
 *  For system/error lines it spans the path column with the message.
 */
function appendRow(type, cells = {}) {
  // Hide empty state
  logEmpty.classList.add("hidden");

  const row = document.createElement("div");
  row.className = "log-row" + (type === "err" ? " row-err" : type === "sys" ? " row-sys" : "");

  // Time
  const tEl = document.createElement("span");
  tEl.className = "col-time";
  tEl.textContent = nowTime();

  if (type === "req") {
    const { method, path, status, upstream, duration } = cells;

    // Method badge
    const mEl = document.createElement("span");
    mEl.className = "method-badge " + methodClass(method);
    mEl.textContent = method;

    // Path (with title tooltip for long paths)
    const pEl = document.createElement("span");
    pEl.className = "col-path";
    pEl.textContent = path;
    pEl.title = path;

    // Upstream host
    const uEl = document.createElement("span");
    uEl.className = "col-upstream";
    uEl.textContent = upstream;
    uEl.title = upstream;

    // Status badge
    const sEl = document.createElement("span");
    sEl.className = "status-badge " + statusClass(status);
    sEl.textContent = status;

    // Duration
    const dEl = document.createElement("span");
    dEl.className = "col-dur " + durClass(duration);
    dEl.textContent = duration;

    row.append(tEl, mEl, pEl, uEl, sEl, dEl);

    // Update counter
    reqCount++;
    reqCountEl.textContent = reqCount + (reqCount === 1 ? " request" : " requests");
  } else {
    // System / error — span across method+path+upstream+status+dur columns
    const msgEl = document.createElement("span");
    msgEl.className = "col-path";
    msgEl.style.gridColumn = "2 / -1";
    msgEl.textContent = cells.message || "";
    row.style.gridTemplateColumns = "70px 1fr";
    row.append(tEl, msgEl);
  }

  logBody.appendChild(row);

  // Keep at most 500 rows
  const rows = logBody.querySelectorAll(".log-row");
  if (rows.length > 500) rows[0].remove();

  logBody.scrollTop = logBody.scrollHeight;
}

/** Called by the Wails "log" event. Parses the incoming line and routes it. */
function handleLog(line, isErr = false) {
  if (!line) return;

  // Request line: "REQ|METHOD|PATH|STATUS|UPSTREAM_HOST|DURATION"
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

  // System / error message
  appendRow(isErr ? "err" : "sys", { message: line });
}

// ─── LAN mode ────────────────────────────────────────────────────
const lanPort   = document.getElementById("lanPort");
const lanCors   = document.getElementById("lanCors");
const lanBtn    = document.getElementById("lanBtn");
const lanDot    = document.getElementById("lanDot");
const lanUrlBox = document.getElementById("lanUrlBox");
const lanUrl    = document.getElementById("lanUrl");
let lanActive = false;

lanBtn.addEventListener("click", async () => {
  if (!lanActive) {
    const localTarget = getLocalTarget();
    const portSpec    = lanPort.value.trim();
    if (!localTarget || !portSpec) {
      handleLog("local app address / routes and port(s) are both required", true);
      return;
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
      lanUrlBox.classList.remove("hidden");
      handleLog("LAN sharing started at " + url);
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
    lanUrlBox.classList.add("hidden");
    handleLog("LAN sharing stopped");
  }
});

// ─── Public tunnel mode ──────────────────────────────────────────
const relayInput = document.getElementById("relayAddr");
const subInput   = document.getElementById("subdomain");
const pubCors    = document.getElementById("pubCors");
const pubBtn     = document.getElementById("pubBtn");
const pubDot     = document.getElementById("pubDot");
const pubUrlBox  = document.getElementById("pubUrlBox");
const pubUrl     = document.getElementById("pubUrl");
let pubActive = false;

pubBtn.addEventListener("click", async () => {
  if (!pubActive) {
    const relayAddr   = relayInput.value.trim();
    const localTarget = getLocalTarget();
    const subdomain   = subInput.value.trim();
    if (!relayAddr || !localTarget) {
      handleLog("relay address and local app address / routes are required", true);
      return;
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
      pubUrl.textContent = "subdomain: " + assigned;
      pubUrlBox.classList.remove("hidden");
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
    pubUrlBox.classList.add("hidden");
    handleLog("Public tunnel stopped");
  }
});

// ─── Wails event listener ────────────────────────────────────────
window.runtime.EventsOn("log", (line) => handleLog(line));
