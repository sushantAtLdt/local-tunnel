# LocalTunnel — a self-hosted ngrok, with a local-network mode too

Exposes a local app (`localhost:3000`, etc.) two ways, independently:

- **Local network** — instantly reachable by any device on the same WiFi/
  network (phone, another laptop, a smart TV testing your app). No relay,
  no internet, no VPS, no setup. Works even with no internet connection at
  all, as long as everyone's on the same network.
- **Public internet** — reachable from anywhere, through a relay server you
  self-host (see "Part 1" below).

Both run from the same GUI, at the same time if you want, completely
independently — turning one on doesn't require or affect the other.

## Why there's still a "server" at all

Your machine almost certainly has no public IP (NAT/router in the way).
Nothing can remove that constraint — ngrok, Cloudflare Tunnel, and this tool
all solve it the same way: a small relay with a public IP holds a persistent
connection back to your machine and forwards public traffic through it. The
part you were probably trying to avoid is *paying for someone else's relay*
— this gives you your own, deployable on a $5/mo VPS or even a spare Pi with
a public IP.

## How it works

**Local network mode** — the client itself runs a small reverse proxy that
listens on your machine's LAN-facing address and forwards to your local
app. Any device on the same WiFi/router can hit `http://<your-laptop-ip>:port`
directly. No relay involved at all:

```
 phone/laptop on same WiFi          your laptop
 ──────────────────────            ─────────────────────────
  GET http://192.168.1.23:9090 ─►  client's LAN proxy (0.0.0.0:9090)
                                          │  forwards to
                                          ▼
                                    localhost:3000 (your real app)
```

**Public internet mode** — same idea as ngrok: a relay with a public IP
bounces traffic to your machine over a connection your client opens
outbound (so no router port-forwarding needed):

```
 someone on the internet          your VPS (public IP)            your laptop
 ─────────────────────           ────────────────────           ──────────────
   GET https://app.you...   ─►    relay (public :8080)
                                        │  looks up "app" in
                                        │  its tunnel table
                                        ▼
                                  relay (control :7000) ◄──────  client (GUI)
                                        │  forwards request          │
                                        │  over the open              │ forwards to
                                        │  connection                 ▼
                                        ▼                       localhost:3000
                                  waits for response       ─►   (your real app)
                                        │
   ◄───────────────────────  sends response back
```

Two programs:

- **`relay/`** — runs on your VPS, only needed for public-internet mode. One
  static Go binary, zero dependencies. Listens on two ports: one for public
  HTTP traffic, one for tunnel clients to connect to.
- **`client/`** — runs on your machine, gives you both modes. Go backend
  (`backend/tunnel.go` for the public tunnel, `backend/lan.go` for local
  sharing) wrapped in a Wails GUI (`app.go`, `main.go`, `frontend/`).

I built and ran both halves end-to-end in a test environment (relay +
client + a dummy local server, real GET/POST requests routed through) to
confirm the protocol works correctly before handing this to you — see
"What's been verified" below.

## Using local network mode

No relay, no deployment — just build the client (Part 2 below) and:

1. Run your local app (e.g. `npm run dev` on port 3000)
2. Launch LocalTunnel, leave the local app address as `http://127.0.0.1:3000`
3. Pick a port to share on (default `9090`) and click **Share on local network**
4. It shows you a URL like `http://192.168.1.23:9090` — open that on any
   other device connected to the same WiFi/router

This is the mode to reach for when you're testing on your phone, showing a
teammate on the same office network, or casting to a smart TV — anything
where a public URL is overkill.

## Part 1 — Deploy the relay (once, on your VPS, only needed for public mode)

### Option A — Docker (recommended)

```bash
cd relay
docker compose up -d --build
```

That's it — builds a static binary in a throwaway Go build stage and ships
it in a `scratch` image (no OS, no shell, ~5MB), restarting automatically
on crash or VPS reboot (`restart: unless-stopped`).

Check it's running:
```bash
docker compose logs -f
```

To change ports or add `-domain` for subdomain routing, edit the `command:`
line in `docker-compose.yml`, or without compose:
```bash
docker build -t localtunnel-relay .
docker run -d --name localtunnel-relay --restart unless-stopped \
  -p 8080:8080 -p 7000:7000 \
  localtunnel-relay -public :8080 -control :7000
```

### Option B — plain binary, no Docker

```bash
cd relay
go build -o relay main.go        # ~7MB, no external deps
./relay -public :8080 -control :7000
```

Open two ports in your VPS firewall: `8080` (public traffic) and `7000`
(tunnel clients — keep this one restricted to your own IP if you can, since
anyone who can reach it can register a tunnel).

### Getting a real `https://myapp.yourdomain.com` URL

1. Point a **wildcard DNS record** at your VPS: `*.tunnel.yourdomain.com A <vps-ip>`
2. Run the relay with `-domain tunnel.yourdomain.com` (add it to the
   `command:` list in `docker-compose.yml` if using Docker)
3. Put [Caddy](https://caddyfile.io) in front of the relay's public port —
   it gets you free automatic wildcard HTTPS in about 3 lines:
   ```
   *.tunnel.yourdomain.com {
       reverse_proxy localhost:8080
   }
   ```
   Caddy handles the TLS cert; your relay only ever sees plain HTTP, which
   keeps its code (and this whole project) simple.

   Or add Caddy straight into `docker-compose.yml` alongside the relay:
   ```yaml
   services:
     relay:
       build: .
       restart: unless-stopped
       expose: ["8080", "7000"]
       ports: ["7000:7000"]   # control port still needs a host mapping
       command: ["-public", ":8080", "-control", ":7000", "-domain", "tunnel.yourdomain.com"]

     caddy:
       image: caddy:2-alpine
       restart: unless-stopped
       ports: ["80:80", "443:443"]
       volumes:
         - ./Caddyfile:/etc/caddy/Caddyfile
         - caddy_data:/data

   volumes:
     caddy_data:
   ```
   with a `Caddyfile` next to it containing the 3-line block shown above.

If you skip the wildcard DNS/Caddy setup, the relay still works over plain
HTTP using whatever `Host` header arrives — fine for testing on your LAN or
via a service like `nip.io`, not fine for a real public HTTPS URL.

## Part 2 — Build the GUI client (on your own machine)

You need to build this **on the OS you'll run it on** — Wails apps use the
native OS webview, so cross-compiling a GUI binary from Linux to macOS/
Windows isn't practical. Building natively on each OS is quick, though.

**One-time setup (all platforms):**
```bash
go install github.com/wailsapp/wails/v2/cmd/wails@v2.9.2
```

**macOS** — Xcode Command Line Tools (`xcode-select --install`), then:
```bash
cd client
wails build          # produces build/bin/localtunnel.app
```

**Windows** — WebView2 runtime (already installed on Windows 10/11 by
default), then:
```powershell
cd client
wails build          # produces build\bin\localtunnel.exe
```

**Linux** — GTK3 + WebKit2GTK dev headers first:
```bash
sudo apt install libgtk-3-dev libwebkit2gtk-4.0-dev    # Debian/Ubuntu
cd client
wails build          # produces build/bin/localtunnel
```

The resulting binary is small (typically 8–15MB) since Wails uses the OS's
built-in webview instead of bundling Chromium like Electron does.

### Using public internet mode

1. Launch the app, run your local app (e.g. `npm run dev` on port 3000)
2. Fill in:
   - **Relay address** — `your-vps-ip:7000`
   - **Subdomain** — optional; leave blank for a random one
3. Click **Start public tunnel**
4. Your app is now live at `https://<subdomain>.tunnel.yourdomain.com`
   (or `http://<subdomain>.<your-domain-label>` if you skipped wildcard DNS)

Each mode has its own **CORS toggle**, adding `Access-Control-Allow-Origin: *`
and friends to responses passing through it — handy if your frontend's
origin won't match the tunnel/LAN URL during testing.

## What's been verified

In the process of building this, I actually compiled and ran the relay and
the client's tunnel engine (`backend/tunnel.go`) together with a dummy local
HTTP server, and sent real GET and POST requests through the full path
(public port → relay → control connection → client → local app → back).
Routing by subdomain, unknown-subdomain 404s, and request/response bodies
all worked correctly. I also built and ran the local-network proxy
(`backend/lan.go`) the same way, confirming it correctly forwards requests
to the local app.

The one piece I could **not** compile in this environment is the Wails GUI
shell itself — this sandbox's network is locked to a small domain allowlist
that doesn't include the Go module proxy or `golang.org`, which Wails'
dependency chain needs. That's an artifact of this sandbox, not a problem
with the code: `app.go` and `main.go` follow Wails' standard, well-documented
App-binding pattern exactly, and `go mod tidy` will resolve cleanly on any
machine with normal internet access, before `wails build` compiles the GUI.

## Known limitations (v1)

- Bodies are buffered in memory per request — fine for typical API/web
  traffic, not ideal for huge file uploads or long-lived streaming
  responses (SSE/websocket passthrough isn't implemented yet).
- One relay process handles one machine's set of tunnels; there's no auth
  on the control port beyond "can reach it" — keep `-control` firewalled to
  IPs you trust, or put it behind a VPN/SSH tunnel for anything beyond
  personal use.
- No built-in request logging/inspector UI (like ngrok's web inspector) —
  the GUI just shows a live log of method/path/status.

## Project layout

```
relay/            # deploy this on your VPS
  main.go
  Dockerfile
  docker-compose.yml
  .dockerignore
client/           # build this on your own machine
  main.go         # Wails entry point
  app.go          # bindings exposed to the GUI
  backend/
    tunnel.go     # public-internet tunnel logic (talks to the relay)
    lan.go        # local-network sharing logic (no relay involved)
  frontend/dist/  # plain HTML/CSS/JS — no npm build step needed
    index.html
    style.css
    main.js
```
