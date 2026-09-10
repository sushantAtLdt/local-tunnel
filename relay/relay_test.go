package main

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func wsClientDial(serverURL string) (ControlConn, error) {
	u, err := url.Parse(serverURL)
	if err != nil {
		return nil, err
	}
	conn, err := net.Dial("tcp", u.Host)
	if err != nil {
		return nil, err
	}

	keyBytes := make([]byte, 16)
	rand.Read(keyBytes)
	key := base64.StdEncoding.EncodeToString(keyBytes)

	req := fmt.Sprintf("GET /_control HTTP/1.1\r\n"+
		"Host: %s\r\n"+
		"Upgrade: websocket\r\n"+
		"Connection: Upgrade\r\n"+
		"Sec-WebSocket-Key: %s\r\n"+
		"Sec-WebSocket-Version: 13\r\n\r\n", u.Host, key)

	if _, err := conn.Write([]byte(req)); err != nil {
		conn.Close()
		return nil, err
	}

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: "GET"})
	if err != nil {
		conn.Close()
		return nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		conn.Close()
		return nil, fmt.Errorf("bad status: %d", resp.StatusCode)
	}

	ws := &wsControlConn{
		conn:     conn,
		br:       br,
		isClient: true,
		closed:   make(chan struct{}),
	}
	return ws, nil
}

func TestHealthCheckAndStatusPage(t *testing.T) {
	reg := newRegistry()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("OK"))
			return
		}
		if reg.count() == 0 {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("Relay Running"))
			return
		}
		http.NotFound(w, r)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	// Test /healthz
	resp, err := http.Get(server.URL + "/healthz")
	if err != nil {
		t.Fatalf("healthz get failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	// Test /health
	resp, err = http.Get(server.URL + "/health")
	if err != nil {
		t.Fatalf("health get failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	// Test / when 0 tunnels active
	resp, err = http.Get(server.URL + "/")
	if err != nil {
		t.Fatalf("root get failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Relay Running") {
		t.Errorf("expected status page, got: %s", string(body))
	}
}

func TestSinglePortWebSocketTunnel(t *testing.T) {
	reg := newRegistry()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_control" || r.Header.Get("Upgrade") == "websocket" {
			handleWebSocketControl(w, r, reg, "")
			return
		}

		// Routing logic matching runPublicServer
		var targetSubdomain string
		if strings.HasPrefix(r.URL.Path, "/t/") {
			rest := strings.TrimPrefix(r.URL.Path, "/t/")
			parts := strings.SplitN(rest, "/", 2)
			if len(parts) > 0 {
				targetSubdomain = parts[0]
			}
		}

		var tun *tunnel
		var ok bool
		if targetSubdomain != "" {
			tun, ok = reg.get(targetSubdomain)
		} else {
			tun, ok = reg.getOnly()
		}

		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}

		id := "req-1"
		respCh := make(chan Envelope, 1)
		tun.pendingMu.Lock()
		tun.pending[id] = respCh
		tun.pendingMu.Unlock()

		tun.send(Envelope{
			Type:   "request",
			ID:     id,
			Method: r.Method,
			Path:   r.URL.Path,
		})

		select {
		case res := <-respCh:
			w.WriteHeader(res.Status)
			w.Write([]byte(res.Body))
		case <-time.After(2 * time.Second):
			http.Error(w, "timeout", http.StatusGatewayTimeout)
		}
	})

	server := httptest.NewServer(handler)
	defer server.Close()

	// Connect client via WebSocket to server.URL/_control
	clientConn, err := wsClientDial(server.URL)
	if err != nil {
		t.Fatalf("ws dial failed: %v", err)
	}
	defer clientConn.Close()

	// Register tunnel
	err = clientConn.WriteEnvelope(Envelope{
		Type:      "register",
		Subdomain: "mytest",
	})
	if err != nil {
		t.Fatalf("register write failed: %v", err)
	}

	ack, err := clientConn.ReadEnvelope()
	if err != nil {
		t.Fatalf("read ack failed: %v", err)
	}
	if ack.Type != "registered" || ack.Subdomain != "mytest" {
		t.Fatalf("unexpected ack: %+v", ack)
	}

	// Now client listens for requests in goroutine
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		reqEnv, err := clientConn.ReadEnvelope()
		if err != nil {
			t.Errorf("client read req failed: %v", err)
			return
		}
		if reqEnv.Type != "request" {
			t.Errorf("expected request envelope, got: %+v", reqEnv)
			return
		}

		// Send response
		err = clientConn.WriteEnvelope(Envelope{
			Type:   "response",
			ID:     reqEnv.ID,
			Status: 200,
			Body:   "hello from local client",
		})
		if err != nil {
			t.Errorf("client write resp failed: %v", err)
		}
	}()

	// Send public HTTP request to server.URL/api/hello (tests single-tunnel auto-routing!)
	resp, err := http.Get(server.URL + "/api/hello")
	if err != nil {
		t.Fatalf("public get failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello from local client" {
		t.Fatalf("unexpected body: %q", string(body))
	}

	wg.Wait()
}
