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
	"strconv"
	"strings"
	"sync"
)

type LANServer struct {
	mu        sync.Mutex
	servers   []*http.Server
	listeners []net.Listener
	active    bool
}

func NewLAN() *LANServer { return &LANServer{} }

// ParsePortSpec parses port strings like "9090", "8080-8088", or "8080, 8081, 8082".
func ParsePortSpec(spec string) ([]int, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, fmt.Errorf("port specification is empty")
	}
	parts := strings.Split(spec, ",")
	seen := make(map[int]bool)
	var ports []int

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "-") {
			rangeParts := strings.Split(part, "-")
			if len(rangeParts) != 2 {
				return nil, fmt.Errorf("invalid port range %q", part)
			}
			start, err1 := strconv.Atoi(strings.TrimSpace(rangeParts[0]))
			end, err2 := strconv.Atoi(strings.TrimSpace(rangeParts[1]))
			if err1 != nil || err2 != nil || start < 1 || end > 65535 || start > end {
				return nil, fmt.Errorf("invalid port range %q", part)
			}
			if end-start > 100 {
				return nil, fmt.Errorf("port range too large (maximum 100 ports)")
			}
			for p := start; p <= end; p++ {
				if !seen[p] {
					seen[p] = true
					ports = append(ports, p)
				}
			}
		} else {
			p, err := strconv.Atoi(part)
			if err != nil || p < 1 || p > 65535 {
				return nil, fmt.Errorf("invalid port %q", part)
			}
			if !seen[p] {
				seen[p] = true
				ports = append(ports, p)
			}
		}
	}
	if len(ports) == 0 {
		return nil, fmt.Errorf("no valid ports found in %q", spec)
	}
	return ports, nil
}

func createProxyHandler(target *url.URL, injectCORS bool) http.Handler {
	proxy := httputil.NewSingleHostReverseProxy(target)
	if !injectCORS {
		return proxy
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			origin = "*"
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS, HEAD")

		reqHeaders := r.Header.Get("Access-Control-Request-Headers")
		if reqHeaders != "" {
			w.Header().Set("Access-Control-Allow-Headers", reqHeaders)
		} else {
			w.Header().Set("Access-Control-Allow-Headers", "*")
		}
		w.Header().Set("Access-Control-Expose-Headers", "*")
		w.Header().Set("Access-Control-Max-Age", "86400")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		proxy.ServeHTTP(w, r)
	})
}

// Start reverse-proxies 0.0.0.0:portSpec -> localTarget, and returns the URL(s)
// other devices on the LAN should use.
func (l *LANServer) Start(localTarget string, portSpec string, injectCORS bool) (string, error) {
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

	ports, err := ParsePortSpec(portSpec)
	if err != nil {
		return "", err
	}

	var targetHost string
	var baseTargetPort int
	if host, portStr, err := net.SplitHostPort(target.Host); err == nil {
		targetHost = host
		baseTargetPort, _ = strconv.Atoi(portStr)
	} else {
		targetHost = target.Host
		baseTargetPort = 0
	}

	var listeners []net.Listener
	var servers []*http.Server

	for i, p := range ports {
		currentTarget := *target
		if len(ports) == 1 {
			currentTarget = *target
		} else if baseTargetPort > 0 {
			targetPort := baseTargetPort + i
			currentTarget.Host = net.JoinHostPort(targetHost, strconv.Itoa(targetPort))
		} else {
			currentTarget.Host = net.JoinHostPort(targetHost, strconv.Itoa(p))
		}

		ln, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", p))
		if err != nil {
			for _, l := range listeners {
				l.Close()
			}
			return "", fmt.Errorf("could not bind port %d: %w", p, err)
		}

		srv := &http.Server{Handler: createProxyHandler(&currentTarget, injectCORS)}
		listeners = append(listeners, ln)
		servers = append(servers, srv)
	}

	l.mu.Lock()
	l.listeners = listeners
	l.servers = servers
	l.active = true
	l.mu.Unlock()

	for i := range servers {
		srv := servers[i]
		ln := listeners[i]
		go srv.Serve(ln)
	}

	ip := LocalIP()
	if len(ports) == 1 {
		return fmt.Sprintf("http://%s:%d", ip, ports[0]), nil
	}
	return fmt.Sprintf("http://%s:%d-%d (%d ports shared)", ip, ports[0], ports[len(ports)-1], len(ports)), nil
}

func (l *LANServer) Stop() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, srv := range l.servers {
		srv.Close()
	}
	for _, ln := range l.listeners {
		ln.Close()
	}
	l.servers = nil
	l.listeners = nil
	l.active = false
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

