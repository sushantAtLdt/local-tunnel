const localTargetInput = document.getElementById("localTarget");
const logEl = document.getElementById("log");

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
    const localTarget = localTargetInput.value.trim();
    const port = parseInt(lanPort.value.trim(), 10);
    if (!localTarget || !port) {
      log("local app address and a port are both required", true);
      return;
    }
    lanBtn.disabled = true;
    try {
      const url = await window.go.main.App.StartLAN(localTarget, port, lanCors.checked);
      lanActive = true;
      lanDot.classList.add("live");
      lanBtn.textContent = "Stop sharing";
      lanBtn.classList.add("stop");
      lanPort.disabled = lanCors.disabled = true;
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
    const localTarget = localTargetInput.value.trim();
    const subdomain = subInput.value.trim();
    if (!relayAddr || !localTarget) {
      log("relay address and local app address are both required", true);
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
    pubUrlBox.classList.add("hidden");
  }
});

window.runtime.EventsOn("log", (line) => log(line));
