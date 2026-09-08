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
      gatewayRoutes.value = `/auth  -> http://127.0.0.1:8084\n/login -> http://127.0.0.1:8082\n/user  -> http://127.0.0.1:8085\n/otp   -> http://127.0.0.1:8800\n/core  -> http://127.0.0.1:8083\n/      -> http://127.0.0.1:3000`;
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

function log(line, isErr) {
  const div = document.createElement("div");
  const time = new Date().toLocaleTimeString();
  div.textContent = `[${time}] ${line}`;
  if (isErr) div.className = "err";
  logEl.appendChild(div);
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
