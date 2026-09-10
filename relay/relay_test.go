package main

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
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

func TestSinglePortWebSocketTunnelWithAuth(t *testing.T) {
	reg := newRegistry()
	testSecret := "abc1234567890def"

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_control" || r.Header.Get("Upgrade") == "websocket" {
			handleWebSocketControl(w, r, reg, "", testSecret)
			return
		}

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

	// 1. Try to register with invalid secret
	clientConnBad, err := wsClientDial(server.URL)
	if err != nil {
		t.Fatalf("ws dial failed: %v", err)
	}
	defer clientConnBad.Close()

	err = clientConnBad.WriteEnvelope(Envelope{
		Type:      "register",
		Subdomain: "mytest",
		Secret:    "wrong-secret",
	})
	if err != nil {
		t.Fatalf("bad register write failed: %v", err)
	}

	ackBad, err := clientConnBad.ReadEnvelope()
	if err != nil {
		t.Fatalf("bad read ack failed: %v", err)
	}
	if ackBad.Type != "error" || !strings.Contains(ackBad.Message, "unauthorized") {
		t.Fatalf("expected unauthorized error, got: %+v", ackBad)
	}

	// 2. Register with correct secret
	clientConn, err := wsClientDial(server.URL)
	if err != nil {
		t.Fatalf("ws dial failed: %v", err)
	}
	defer clientConn.Close()

	err = clientConn.WriteEnvelope(Envelope{
		Type:      "register",
		Subdomain: "mytest",
		Secret:    testSecret,
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

	// 3. Client responds to requests
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

		err = clientConn.WriteEnvelope(Envelope{
			Type:   "response",
			ID:     reqEnv.ID,
			Status: 200,
			Body:   "hello from authenticated client",
		})
		if err != nil {
			t.Errorf("client write resp failed: %v", err)
		}
	}()

	resp, err := http.Get(server.URL + "/api/hello")
	if err != nil {
		t.Fatalf("public get failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello from authenticated client" {
		t.Fatalf("unexpected body: %q", string(body))
	}

	wg.Wait()
}

func TestFragmentedWebSocketFrames(t *testing.T) {
	// Tests RFC 6455 frame reassembly where fin=false followed by continuation frame
	reg := newRegistry()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleWebSocketControl(w, r, reg, "", "")
	}))
	defer server.Close()

	// Connect raw TCP to emulate fragmented proxy frames
	u, _ := url.Parse(server.URL)
	conn, err := net.Dial("tcp", u.Host)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer conn.Close()

	keyBytes := make([]byte, 16)
	rand.Read(keyBytes)
	key := base64.StdEncoding.EncodeToString(keyBytes)

	req := fmt.Sprintf("GET /_control HTTP/1.1\r\n"+
		"Host: %s\r\n"+
		"Upgrade: websocket\r\n"+
		"Connection: Upgrade\r\n"+
		"Sec-WebSocket-Key: %s\r\n"+
		"Sec-WebSocket-Version: 13\r\n\r\n", u.Host, key)
	conn.Write([]byte(req))

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: "GET"})
	if err != nil || resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("handshake failed: %v, status: %d", err, resp.StatusCode)
	}

	// Prepare registration JSON
	msgData, _ := json.Marshal(Envelope{
		Type:      "register",
		Subdomain: "fragmented",
	})

	// Split msgData in half
	half := len(msgData) / 2
	part1 := msgData[:half]
	part2 := msgData[half:]

	// Write frame 1: FIN=0, Opcode=Binary (0x2), masked
	mask1 := []byte{1, 2, 3, 4}
	maskedPart1 := make([]byte, len(part1))
	for i := range part1 {
		maskedPart1[i] = part1[i] ^ mask1[i%4]
	}
	frame1Hdr := []byte{
		0x02,                     // FIN=0, Opcode=0x2
		0x80 | byte(len(part1)), // MASK=1, len
	}
	conn.Write(frame1Hdr)
	conn.Write(mask1)
	conn.Write(maskedPart1)

	// Write frame 2: FIN=1, Opcode=Continuation (0x0), masked
	mask2 := []byte{5, 6, 7, 8}
	maskedPart2 := make([]byte, len(part2))
	for i := range part2 {
		maskedPart2[i] = part2[i] ^ mask2[i%4]
	}
	frame2Hdr := []byte{
		0x80 | 0x00,              // FIN=1, Opcode=0x0
		0x80 | byte(len(part2)), // MASK=1, len
	}
	conn.Write(frame2Hdr)
	conn.Write(mask2)
	conn.Write(maskedPart2)

	// Server should reassemble both frames into the complete registration envelope!
	wsClient := &wsControlConn{
		conn:     conn,
		br:       br,
		isClient: true,
		closed:   make(chan struct{}),
	}
	ack, err := wsClient.ReadEnvelope()
	if err != nil {
		t.Fatalf("server failed to read reassembled frame: %v", err)
	}
	if ack.Type != "registered" || ack.Subdomain != "fragmented" {
		t.Fatalf("unexpected ack: %+v", ack)
	}
}
