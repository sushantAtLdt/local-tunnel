package main

import (
	"context"
	"fmt"
	"sync"

	"client/backend"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// App is bound to the frontend. Every exported method is callable from JS.
type App struct {
	ctx context.Context

	mu        sync.Mutex
	client    *backend.Client // public-internet tunnel (via relay)
	active    bool
	lan       *backend.LANServer // local-network sharing (no relay)
	lanActive bool
}

func NewApp() *App {
	return &App{}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

// StartTunnel begins forwarding relayAddr <-> localTarget under the given
// subdomain (empty = relay picks one). Returns the assigned subdomain.
func (a *App) StartTunnel(relayAddr, subdomain, localTarget string, injectCORS bool) (string, error) {
	a.mu.Lock()
	if a.active {
		a.mu.Unlock()
		return "", fmt.Errorf("a tunnel is already running — stop it first")
	}
	a.mu.Unlock()

	c := backend.New()
	c.Log = func(line string) {
		wailsRuntime.EventsEmit(a.ctx, "log", line)
	}

	assigned, err := c.Start(backend.Config{
		RelayAddr:   relayAddr,
		Subdomain:   subdomain,
		LocalTarget: localTarget,
		InjectCORS:  injectCORS,
	})
	if err != nil {
		return "", err
	}

	a.mu.Lock()
	a.client = c
	a.active = true
	a.mu.Unlock()

	return assigned, nil
}

// StopTunnel closes the current tunnel, if any.
func (a *App) StopTunnel() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.client != nil {
		a.client.Stop()
	}
	a.active = false
	a.client = nil
	wailsRuntime.EventsEmit(a.ctx, "log", "tunnel stopped")
}

// IsActive reports whether a public tunnel is currently running.
func (a *App) IsActive() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.active
}

// StartLAN shares localTarget on the local network (no relay, no internet
// needed) at 0.0.0.0:port. Returns the URL other devices on the same
// WiFi/network should use.
func (a *App) StartLAN(localTarget string, port int, injectCORS bool) (string, error) {
	a.mu.Lock()
	if a.lanActive {
		a.mu.Unlock()
		return "", fmt.Errorf("already sharing on the local network — stop it first")
	}
	if a.lan == nil {
		a.lan = backend.NewLAN()
	}
	lan := a.lan
	a.mu.Unlock()

	url, err := lan.Start(localTarget, port, injectCORS)
	if err != nil {
		return "", err
	}

	a.mu.Lock()
	a.lanActive = true
	a.mu.Unlock()

	wailsRuntime.EventsEmit(a.ctx, "log", fmt.Sprintf("sharing on local network at %s", url))
	return url, nil
}

// StopLAN stops local-network sharing, if active.
func (a *App) StopLAN() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.lan != nil {
		a.lan.Stop()
	}
	a.lanActive = false
	wailsRuntime.EventsEmit(a.ctx, "log", "local network sharing stopped")
}

// IsLANActive reports whether local-network sharing is currently running.
func (a *App) IsLANActive() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lanActive
}
