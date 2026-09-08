const localTargetInput = document.getElementById("localTarget");
const toggleGatewayBtn = document.getElementById("toggleGatewayBtn");
const singleTargetBox = document.getElementById("singleTargetBox");
const gatewayTargetBox = document.getElementById("gatewayTargetBox");
const gatewayRoutes = document.getElementById("gatewayRoutes");
const logEl = document.getElementById("log");

let isGatewayMode = false;

toggleGatewayBtn.addEventListener("click", () => {
  isGatewayMode = !isGatewayMode;
  if (isGatewayMode) {
    singleTargetBox.classList.add("hidden");
    gatewayTargetBox.classList.remove("hidden");
    toggleGatewayBtn.textContent = "Single URL Mode";
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
    toggleGatewayBtn.textContent = "Gateway Mode (Routes)";
  }
});

function getLocalTarget() {
  if (isGatewayMode) {
    return gatewayRoutes.value.trim();
  }
  return localTargetInput.value.trim();
}

function setTargetInputsDisabled(disabled) {
  localTargetInput.disabled = disabled;
  gatewayRoutes.disabled = disabled;
  toggleGatewayBtn.disabled = disabled;
}

// REQUEST LOG pattern:  "METHOD /path → http://host/path  STATUS (Xms)"
const REQUEST_RE = /^(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)\s+(\S+)\s+→\s+(\S+)\s+(\d{3})\s+\(([^)]+)\)$/;
const METHOD_COLOR = { GET:"#61afef", POST:"#98c379", PUT:"#e5c07b", PATCH:"#e5c07b",
                       DELETE:"#e06c75", HEAD:"#56b6c2", OPTIONS:"#c678dd" };

function statusClass(code) {
  const n = parseInt(code, 10);
  if (n >= 500) return "log-5xx";
  if (n >= 400) return "log-4xx";
  if (n >= 300) return "log-3xx";
  return "log-2xx";
}

function log(line, isErr) {
  const div = document.createElement("div");
  div.className = "log-row";
  const time = new Date().toLocaleTimeString("en-GB", { hour12: false });
  const ts = document.createElement("span");
  ts.className = "log-ts";
  ts.textContent = time;
  div.appendChild(ts);

  const m = REQUEST_RE.exec(line);
  if (m && !isErr) {
    const [, method, path, upstream, status, dur] = m;
    const mSpan = document.createElement("span");
    mSpan.className = "log-method";
    mSpan.style.color = METHOD_COLOR[method] || "#abb2bf";
    mSpan.textContent = method;
    const pSpan = document.createElement("span");
    pSpan.className = "log-path";
    pSpan.textContent = path;
    const sep = document.createElement("span");
    sep.className = "log-sep";
    sep.textContent = " → ";
    const uSpan = document.createElement("span");
    uSpan.className = "log-upstream";
    uSpan.textContent = upstream;
    const sSpan = document.createElement("span");
    sSpan.className = "log-status " + statusClass(status);
    sSpan.textContent = status;
    const dSpan = document.createElement("span");
    dSpan.className = "log-dur";
    dSpan.textContent = dur;
    div.append(mSpan, " ", pSpan, sep, uSpan, " ", sSpan, " ", dSpan);
  } else {
    const msg = document.createElement("span");
    msg.className = isErr ? "log-err" : "log-info";
    msg.textContent = line;
    div.appendChild(msg);
  }

  logEl.appendChild(div);
  // Keep at most 500 rows to avoid unbounded growth
  while (logEl.children.length > 500) logEl.removeChild(logEl.firstChild);
  logEl.scrollTop = logEl.scrollHeight;
}


// ---------- Local network mode ----------
const lanPort = document.getElementById("lanPort");
const lanCors = document.getElementById("lanCors");
const lanBtn = document.getElementById("lanBtn");
const lanDot = document.getElementById("lanDot");
const lanUrlBox = document.getElementById("lanUrlBox");
const lanUrl = document.getElementById("lanUrl");
let lanActive = false;

lanBtn.addEventListener("click", async () => {
  if (!lanActive) {
    const localTarget = getLocalTarget();
    const portSpec = lanPort.value.trim();
    if (!localTarget || !portSpec) {
      log("local app address / routes and port(s) are both required", true);
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
    } catch (err) {
      log(String(err), true);
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
  }
});

// ---------- Public internet mode ----------
const relayInput = document.getElementById("relayAddr");
const subInput = document.getElementById("subdomain");
const pubCors = document.getElementById("pubCors");
const pubBtn = document.getElementById("pubBtn");
const pubDot = document.getElementById("pubDot");
const pubUrlBox = document.getElementById("pubUrlBox");
const pubUrl = document.getElementById("pubUrl");
let pubActive = false;

pubBtn.addEventListener("click", async () => {
  if (!pubActive) {
    const relayAddr = relayInput.value.trim();
    const localTarget = getLocalTarget();
    const subdomain = subInput.value.trim();
    if (!relayAddr || !localTarget) {
      log("relay address and local app address / routes are required", true);
      return;
    }
    pubBtn.disabled = true;
    try {
      const assigned = await window.go.main.App.StartTunnel(
        relayAddr,
        subdomain,
        localTarget,
        pubCors.checked
      );
      pubActive = true;
      pubDot.classList.add("live");
      pubBtn.textContent = "Stop public tunnel";
      pubBtn.classList.add("stop");
      relayInput.disabled = subInput.disabled = pubCors.disabled = true;
      setTargetInputsDisabled(true);
      pubUrl.textContent = `subdomain: ${assigned}`;
      pubUrlBox.classList.remove("hidden");
    } catch (err) {
      log(String(err), true);
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
  }
});

window.runtime.EventsOn("log", (line) => log(line));
