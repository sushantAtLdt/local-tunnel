// Package backend: LAN sharing mode. This needs no relay, no internet, and
// no public IP — it just listens on your machine's LAN-facing interface so
// other devices on the same WiFi/network can reach your local app directly.
package backend

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
)

type LANServer struct {
	mu     sync.Mutex
	srv    *http.Server
	active bool
}

func NewLAN() *LANServer { return &LANServer{} }

// Start reverse-proxies 0.0.0.0:port -> localTarget, and returns the URL
// other devices on the LAN should use.
func (l *LANServer) Start(localTarget string, port int, injectCORS bool) (string, error) {
	l.mu.Lock()
	if l.active {
		l.mu.Unlock()
		return "", fmt.Errorf("already sharing on the local network — stop it first")
	}
	l.mu.Unlock()

	target, err := url.Parse(localTarget)
	if err != nil || target.Host == "" {
		return "", fmt.Errorf("invalid local app address %q", localTarget)
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	if injectCORS {
		proxy.ModifyResponse = func(resp *http.Response) error {
			resp.Header.Set("Access-Control-Allow-Origin", "*")
			resp.Header.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			resp.Header.Set("Access-Control-Allow-Headers", "*")
			return nil
		}
	}

	ln, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		return "", fmt.Errorf("could not bind port %d: %w", port, err)
	}

	srv := &http.Server{Handler: proxy}
	l.mu.Lock()
	l.srv = srv
	l.active = true
	l.mu.Unlock()

	go srv.Serve(ln)

	return fmt.Sprintf("http://%s:%d", LocalIP(), port), nil
}

func (l *LANServer) Stop() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.srv != nil {
		l.srv.Close()
	}
	l.active = false
	l.srv = nil
}

// LocalIP guesses this machine's LAN-facing IPv4 address (the one other
// devices on the same WiFi/router would use to reach it).
func LocalIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "127.0.0.1"
	}
	var fallback string
	for _, a := range addrs {
		ipNet, ok := a.(*net.IPNet)
		if !ok || ipNet.IP.IsLoopback() {
			continue
		}
		ip4 := ipNet.IP.To4()
		if ip4 == nil {
			continue
		}
		s := ip4.String()
		if strings.HasPrefix(s, "192.168.") || strings.HasPrefix(s, "10.") || strings.HasPrefix(s, "172.") {
			return s // prefer private LAN ranges
		}
		fallback = s
	}
	if fallback != "" {
		return fallback
	}
	return "127.0.0.1"
}
