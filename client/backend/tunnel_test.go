package backend

import (
	"crypto/sha1"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDialRelayAddressParsing(t *testing.T) {
	// Start a test HTTP server with WebSocket upgrade
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_control" && strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			key := r.Header.Get("Sec-WebSocket-Key")
			h := sha1.New()
			h.Write([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
			acceptKey := base64.StdEncoding.EncodeToString(h.Sum(nil))

			hj, _ := w.(http.Hijacker)
			conn, bufrw, _ := hj.Hijack()
			resp := "HTTP/1.1 101 Switching Protocols\r\n" +
				"Upgrade: websocket\r\n" +
				"Connection: Upgrade\r\n" +
				"Sec-WebSocket-Accept: " + acceptKey + "\r\n\r\n"
			bufrw.WriteString(resp)
			bufrw.Flush()
			conn.Close()
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	// 1. Dial with ws://
	ctrl, _, err := dialRelay("ws://" + server.Listener.Addr().String())
	if err != nil {
		t.Fatalf("failed ws:// dial: %v", err)
	}
	ctrl.Close()

	// 2. Dial with http://
	ctrl, _, err = dialRelay(server.URL)
	if err != nil {
		t.Fatalf("failed http:// dial: %v", err)
	}
	ctrl.Close()

	// 3. Dial without scheme to host:port
	ctrl, _, err = dialRelay(server.Listener.Addr().String())
	if err != nil {
		t.Fatalf("failed bare host:port dial: %v", err)
	}
	ctrl.Close()
}

func TestClientEndToEnd(t *testing.T) {
	// 1. Setup local target server (simulating user's local app e.g. localhost:3000)
	localApp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-App", "local-target")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("response from local app"))
	}))
	defer localApp.Close()

	// 2. Setup mock relay server with WebSocket control and public request forwarding
	type mockRelay struct {
		ctrl ControlConn
	}
	relayState := &mockRelay{}

	relayServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_control" {
			key := r.Header.Get("Sec-WebSocket-Key")
			h := sha1.New()
			h.Write([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
			acceptKey := base64.StdEncoding.EncodeToString(h.Sum(nil))

			hj, _ := w.(http.Hijacker)
			conn, bufrw, _ := hj.Hijack()
			resp := "HTTP/1.1 101 Switching Protocols\r\n" +
				"Upgrade: websocket\r\n" +
				"Connection: Upgrade\r\n" +
				"Sec-WebSocket-Accept: " + acceptKey + "\r\n\r\n"
			bufrw.WriteString(resp)
			bufrw.Flush()

			wsConn := &wsControlConn{
				conn:     conn,
				br:       bufrw.Reader,
				isClient: false,
				closed:   make(chan struct{}),
			}
			relayState.ctrl = wsConn

			// Expect registration envelope
			reg, err := wsConn.ReadEnvelope()
			if err != nil {
				return
			}
			if reg.Type == "register" {
				wsConn.WriteEnvelope(Envelope{
					Type:      "registered",
					Subdomain: "testtunnel",
					URL:       "http://testtunnel.relay.test",
				})
			}
			return
		}
		http.NotFound(w, r)
	}))
	defer relayServer.Close()

	// 3. Start client
	c := New()
	assigned, err := c.Start(Config{
		RelayAddr:   relayServer.URL,
		Subdomain:   "testtunnel",
		LocalTarget: localApp.URL,
		InjectCORS:  true,
	})
	if err != nil {
		t.Fatalf("client start failed: %v", err)
	}
	if assigned != "http://testtunnel.relay.test" {
		t.Fatalf("unexpected assigned URL: %s", assigned)
	}
	defer c.Stop()

	// Wait briefly for serve loop to initialize
	time.Sleep(50 * time.Millisecond)

	// 4. Simulate relay receiving a public HTTP request and sending it to client
	if relayState.ctrl == nil {
		t.Fatalf("relay control connection is nil")
	}

	reqID := "req-42"
	err = relayState.ctrl.WriteEnvelope(Envelope{
		Type:   "request",
		ID:     reqID,
		Method: "GET",
		Path:   "/test-path",
	})
	if err != nil {
		t.Fatalf("relay write request failed: %v", err)
	}

	// 5. Read response back from client
	respEnv, err := relayState.ctrl.ReadEnvelope()
	if err != nil {
		t.Fatalf("relay read response failed: %v", err)
	}

	if respEnv.Type != "response" || respEnv.ID != reqID {
		t.Fatalf("unexpected response envelope: %+v", respEnv)
	}
	if respEnv.Status != 200 {
		t.Fatalf("expected status 200, got %d", respEnv.Status)
	}

	decodedBody, _ := base64.StdEncoding.DecodeString(respEnv.Body)
	if string(decodedBody) != "response from local app" {
		t.Fatalf("unexpected body: %s", string(decodedBody))
	}

	// Verify CORS header was injected
	if len(respEnv.Headers["Access-Control-Allow-Origin"]) == 0 {
		t.Fatalf("expected CORS headers to be injected")
	}
}
