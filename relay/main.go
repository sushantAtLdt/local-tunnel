// relay.go — the public-facing half of the tunnel.
// Deploy this ONE binary on any cheap VPS, Render, Koyeb, Railway, or Fly.io.
// It has zero third-party dependencies — pure Go standard library.
//
// Capabilities:
//  1. Single-port mode (Render, Koyeb, Railway): multiplexes WebSocket control
//     connections and public HTTP traffic on the same port ($PORT or :8080).
//  2. Dual-port mode (traditional VPS): accepts plain TCP control connections
//     on :7000 and public HTTP traffic on :8080.
//  3. Static Hex Authentication Key: generates/reads a static hex secret and prints
//     it to startup logs; required by clients to register tunnels.
//  4. Cloud health checks (/healthz, /health, and friendly status page).
//  5. Subdomain & cloud routing (wildcard DNS, /t/<subdomain>/ path prefix,
//     X-Tunnel header, ?_tunnel= query param, or single-tunnel auto-routing).
//
// Build:   go build -o relay main.go
// Run VPS: ./relay -public :8080 -control :7000 -domain tunnel.example.com
// Run Cloud (single port): PORT=8080 ./relay
package main

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Envelope is the single message type exchanged on the control connection.
type Envelope struct {
	Type      string              `json:"type"` // register | registered | error | request | response
	ID        string              `json:"id,omitempty"`
	Subdomain string              `json:"subdomain,omitempty"`
	URL       string              `json:"url,omitempty"`
	Secret    string              `json:"secret,omitempty"` // static hex auth key
	Method    string              `json:"method,omitempty"`
	Path      string              `json:"path,omitempty"`
	Headers   map[string][]string `json:"headers,omitempty"`
	Body      string              `json:"body,omitempty"` // base64
	Status    int                 `json:"status,omitempty"`
	Message   string              `json:"message,omitempty"`
}

// ControlConn abstracts the control channel between relay and client.
// It can be backed by raw TCP or an RFC 6455 WebSocket stream.
type ControlConn interface {
	ReadEnvelope() (Envelope, error)
	WriteEnvelope(Envelope) error
	Close() error
}

// ── Raw TCP framing (legacy 4-byte big-endian length + JSON) ────────────────

func writeRawFrame(w io.Writer, e Envelope) error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	hdr := make([]byte, 4)
	binary.BigEndian.PutUint32(hdr, uint32(len(data)))
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

func readRawFrame(r io.Reader) (Envelope, error) {
	var e Envelope
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return e, err
	}
	n := binary.BigEndian.Uint32(hdr)
	if n > 32<<20 { // 32MB frame cap
		return e, fmt.Errorf("frame too large: %d bytes", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return e, err
	}
	err := json.Unmarshal(buf, &e)
	return e, err
}

type tcpControlConn struct {
	conn    net.Conn
	br      *bufio.Reader
	writeMu sync.Mutex
}

func (c *tcpControlConn) ReadEnvelope() (Envelope, error) {
	return readRawFrame(c.br)
}

func (c *tcpControlConn) WriteEnvelope(e Envelope) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return writeRawFrame(c.conn, e)
}

func (c *tcpControlConn) Close() error {
	return c.conn.Close()
}

// ── RFC 6455 WebSocket implementation (Zero dependencies) ──────────────────

const (
	wsOpContinuation = 0x0
	wsOpText         = 0x1
	wsOpBinary       = 0x2
	wsOpClose        = 0x8
	wsOpPing         = 0x9
	wsOpPong         = 0xA
	wsGUID           = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
)

func wsReadFrame(r io.Reader) (bool, byte, []byte, error) {
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return false, 0, nil, err
	}
	fin := (hdr[0] & 0x80) != 0
	opcode := hdr[0] & 0x0F
	masked := (hdr[1] & 0x80) != 0
	payloadLen := uint64(hdr[1] & 0x7F)

	if payloadLen == 126 {
		ext := make([]byte, 2)
		if _, err := io.ReadFull(r, ext); err != nil {
			return false, 0, nil, err
		}
		payloadLen = uint64(binary.BigEndian.Uint16(ext))
	} else if payloadLen == 127 {
		ext := make([]byte, 8)
		if _, err := io.ReadFull(r, ext); err != nil {
			return false, 0, nil, err
		}
		payloadLen = binary.BigEndian.Uint64(ext)
	}

	if payloadLen > 32<<20 {
		return false, 0, nil, fmt.Errorf("ws payload too large: %d", payloadLen)
	}

	var maskKey []byte
	if masked {
		maskKey = make([]byte, 4)
		if _, err := io.ReadFull(r, maskKey); err != nil {
			return false, 0, nil, err
		}
	}

	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(r, payload); err != nil {
		return false, 0, nil, err
	}

	if masked {
		for i := uint64(0); i < payloadLen; i++ {
			payload[i] ^= maskKey[i%4]
		}
	}

	return fin, opcode, payload, nil
}

func wsWriteFrame(w io.Writer, masked bool, opcode byte, payload []byte) error {
	var buf []byte
	b0 := byte(0x80) | (opcode & 0x0F) // FIN = 1
	payloadLen := len(payload)

	var maskBit byte
	if masked {
		maskBit = 0x80
	}

	if payloadLen < 126 {
		buf = append(buf, b0, maskBit|byte(payloadLen))
	} else if payloadLen <= 65535 {
		buf = append(buf, b0, maskBit|126, byte(payloadLen>>8), byte(payloadLen))
	} else {
		buf = append(buf, b0, maskBit|127)
		lenBytes := make([]byte, 8)
		binary.BigEndian.PutUint64(lenBytes, uint64(payloadLen))
		buf = append(buf, lenBytes...)
	}

	if masked {
		maskKey := make([]byte, 4)
		if _, err := rand.Read(maskKey); err != nil {
			return err
		}
		buf = append(buf, maskKey...)
		maskedPayload := make([]byte, payloadLen)
		for i := 0; i < payloadLen; i++ {
			maskedPayload[i] = payload[i] ^ maskKey[i%4]
		}
		buf = append(buf, maskedPayload...)
	} else {
		buf = append(buf, payload...)
	}

	_, err := w.Write(buf)
	return err
}

// wsReadMessage reassembles fragmented frames (continuation frames) until FIN=true.
func wsReadMessage(r io.Reader, w io.Writer, writeMu *sync.Mutex, isClient bool) ([]byte, error) {
	var msgBuf []byte

	for {
		fin, opcode, payload, err := wsReadFrame(r)
		if err != nil {
			return nil, err
		}

		switch opcode {
		case wsOpPing:
			writeMu.Lock()
			_ = wsWriteFrame(w, isClient, wsOpPong, payload)
			writeMu.Unlock()
			continue
		case wsOpPong:
			continue
		case wsOpClose:
			writeMu.Lock()
			_ = wsWriteFrame(w, isClient, wsOpClose, nil)
			writeMu.Unlock()
			return nil, io.EOF
		case wsOpText, wsOpBinary:
			msgBuf = append(msgBuf[:0], payload...)
		case wsOpContinuation:
			msgBuf = append(msgBuf, payload...)
		default:
			continue
		}

		if fin {
			if len(msgBuf) == 0 {
				continue
			}
			result := make([]byte, len(msgBuf))
			copy(result, msgBuf)
			return result, nil
		}
	}
}

type wsControlConn struct {
	conn      net.Conn
	br        *bufio.Reader
	writeMu   sync.Mutex
	isClient  bool
	closeOnce sync.Once
	closed    chan struct{}
}

func (c *wsControlConn) ReadEnvelope() (Envelope, error) {
	data, err := wsReadMessage(c.br, c.conn, &c.writeMu, c.isClient)
	if err != nil {
		return Envelope{}, err
	}
	var e Envelope
	err = json.Unmarshal(data, &e)
	return e, err
}

func (c *wsControlConn) WriteEnvelope(e Envelope) error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return wsWriteFrame(c.conn, c.isClient, wsOpBinary, data)
}

func (c *wsControlConn) Close() error {
	c.closeOnce.Do(func() {
		close(c.closed)
		c.writeMu.Lock()
		_ = wsWriteFrame(c.conn, c.isClient, wsOpClose, nil)
		c.writeMu.Unlock()
	})
	return c.conn.Close()
}

func (c *wsControlConn) startPingLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-c.closed:
				return
			case <-ticker.C:
				c.writeMu.Lock()
				err := wsWriteFrame(c.conn, c.isClient, wsOpPing, []byte("ping"))
				c.writeMu.Unlock()
				if err != nil {
					return
				}
			}
		}
	}()
}

// ── Tunnel & Registry ───────────────────────────────────────────────────────

type tunnel struct {
	subdomain string
	ctrl      ControlConn

	pendingMu sync.Mutex
	pending   map[string]chan Envelope
}

func (t *tunnel) send(e Envelope) error {
	return t.ctrl.WriteEnvelope(e)
}

type registry struct {
	mu     sync.RWMutex
	byName map[string]*tunnel
}

func newRegistry() *registry { return &registry{byName: make(map[string]*tunnel)} }

func (r *registry) add(t *tunnel) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byName[t.subdomain] = t
}

func (r *registry) remove(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.byName, name)
}

func (r *registry) get(name string) (*tunnel, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.byName[name]
	return t, ok
}

func (r *registry) count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.byName)
}

func (r *registry) getOnly() (*tunnel, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.byName) == 1 {
		for _, t := range r.byName {
			return t, true
		}
	}
	return nil, false
}

func randomSubdomain() string {
	b := make([]byte, 5)
	rand.Read(b)
	return strings.ToLower(base64.RawURLEncoding.EncodeToString(b))
}

// ── Authentication Secret Initialization ────────────────────────────────────

func getOrInitSecret(cliSecret string) string {
	if cliSecret != "" {
		return strings.TrimSpace(cliSecret)
	}
	if env := os.Getenv("RELAY_SECRET"); env != "" {
		return strings.TrimSpace(env)
	}
	if env := os.Getenv("AUTH_KEY"); env != "" {
		return strings.TrimSpace(env)
	}

	secretFile := ".relay_secret"
	if data, err := os.ReadFile(secretFile); err == nil {
		s := strings.TrimSpace(string(data))
		if len(s) >= 8 {
			return s
		}
	}

	// Generate 16 random bytes (32 hex characters)
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		log.Fatalf("failed to generate random secret: %v", err)
	}
	s := hex.EncodeToString(b)
	_ = os.WriteFile(secretFile, []byte(s+"\n"), 0600)
	return s
}

// ── Control connection lifecycle ───────────────────────────────────────────

func handleControl(ctrl ControlConn, reg *registry, requestHost, baseDomain, relaySecret string, isHTTPS bool) {
	defer ctrl.Close()

	first, err := ctrl.ReadEnvelope()
	if err != nil || first.Type != "register" {
		ctrl.WriteEnvelope(Envelope{Type: "error", Message: "expected register frame first"})
		return
	}

	// Authenticate client using hex secret key
	if relaySecret != "" {
		if first.Secret != relaySecret {
			log.Printf("tunnel registration rejected: invalid auth key")
			ctrl.WriteEnvelope(Envelope{Type: "error", Message: "unauthorized: invalid or missing relay auth key"})
			return
		}
	}

	name := strings.ToLower(strings.TrimSpace(first.Subdomain))
	if name == "" {
		name = randomSubdomain()
	}
	if _, exists := reg.get(name); exists {
		ctrl.WriteEnvelope(Envelope{Type: "error", Message: "subdomain already in use"})
		return
	}

	t := &tunnel{subdomain: name, ctrl: ctrl, pending: make(map[string]chan Envelope)}
	reg.add(t)
	defer reg.remove(name)

	scheme := "http"
	if isHTTPS {
		scheme = "https"
	}

	publicURL := ""
	if baseDomain != "" {
		publicURL = fmt.Sprintf("%s://%s.%s", scheme, name, baseDomain)
	} else if requestHost != "" {
		cleanHost := strings.Split(requestHost, ":")[0]
		if strings.Contains(cleanHost, "onrender.com") || strings.Contains(cleanHost, "koyeb.app") || strings.Contains(cleanHost, "railway.app") || strings.Contains(cleanHost, "fly.dev") {
			publicURL = fmt.Sprintf("%s://%s", scheme, requestHost)
		} else {
			publicURL = fmt.Sprintf("%s://%s.%s", scheme, name, cleanHost)
		}
	}

	ctrl.WriteEnvelope(Envelope{Type: "registered", Subdomain: name, URL: publicURL})
	log.Printf("tunnel registered: %s (url: %s)", name, publicURL)

	for {
		e, err := ctrl.ReadEnvelope()
		if err != nil {
			log.Printf("tunnel %s disconnected: %v", name, err)
			return
		}
		if e.Type != "response" {
			continue
		}
		t.pendingMu.Lock()
		ch, ok := t.pending[e.ID]
		if ok {
			delete(t.pending, e.ID)
		}
		t.pendingMu.Unlock()
		if ok {
			ch <- e
		}
	}
}

func handleWebSocketControl(w http.ResponseWriter, r *http.Request, reg *registry, baseDomain, relaySecret string) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		http.Error(w, "expected websocket upgrade", http.StatusBadRequest)
		return
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		http.Error(w, "missing Sec-WebSocket-Key", http.StatusBadRequest)
		return
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "server does not support hijacking", http.StatusInternalServerError)
		return
	}
	conn, bufrw, err := hj.Hijack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	h := sha1.New()
	h.Write([]byte(key + wsGUID))
	acceptKey := base64.StdEncoding.EncodeToString(h.Sum(nil))

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
	wsConn.startPingLoop(25 * time.Second)

	isHTTPS := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	handleControl(wsConn, reg, r.Host, baseDomain, relaySecret, isHTTPS)
}

func runControlServer(addr string, reg *registry, baseDomain, relaySecret string) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("control listen: %v", err)
	}
	log.Printf("control server listening on %s (raw TCP)", addr)
	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("control accept: %v", err)
			continue
		}
		tcpConn := &tcpControlConn{
			conn: conn,
			br:   bufio.NewReader(conn),
		}
		go handleControl(tcpConn, reg, "", baseDomain, relaySecret, false)
	}
}

// ── Public HTTP routing ────────────────────────────────────────────────────

func subdomainFromHost(host, baseDomain string) string {
	host = strings.Split(host, ":")[0] // strip port
	if baseDomain != "" && strings.HasSuffix(host, "."+baseDomain) {
		return strings.TrimSuffix(host, "."+baseDomain)
	}
	parts := strings.Split(host, ".")
	return parts[0]
}

func runPublicServer(addr string, reg *registry, baseDomain, relaySecret string) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		// 1. Health check endpoints for cloud providers (Render, Koyeb, Railway, etc.)
		if r.URL.Path == "/healthz" || r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("OK"))
			return
		}

		// 2. Control connection over WebSocket on public port
		if r.URL.Path == "/_control" || (r.Header.Get("Upgrade") == "websocket" && (r.URL.Path == "/" || r.URL.Path == "/_control")) {
			handleWebSocketControl(w, r, reg, baseDomain, relaySecret)
			return
		}

		// 3. Determine target tunnel subdomain & path
		targetPath := r.URL.RequestURI()
		var targetSubdomain string

		// Option A: Path prefix routing (/t/<subdomain>/...)
		if strings.HasPrefix(r.URL.Path, "/t/") {
			rest := strings.TrimPrefix(r.URL.Path, "/t/")
			parts := strings.SplitN(rest, "/", 2)
			if len(parts) > 0 && parts[0] != "" {
				targetSubdomain = parts[0]
				trimmed := "/"
				if len(parts) == 2 {
					trimmed = "/" + parts[1]
				}
				if r.URL.RawQuery != "" {
					targetPath = trimmed + "?" + r.URL.RawQuery
				} else {
					targetPath = trimmed
				}
			}
		}

		// Option B: Request header routing (X-Tunnel or X-Subdomain)
		if targetSubdomain == "" {
			if sub := r.Header.Get("X-Tunnel"); sub != "" {
				targetSubdomain = sub
			} else if sub := r.Header.Get("X-Subdomain"); sub != "" {
				targetSubdomain = sub
			}
		}

		// Option C: Query parameter routing (?_tunnel=...)
		if targetSubdomain == "" {
			if sub := r.URL.Query().Get("_tunnel"); sub != "" {
				targetSubdomain = sub
			}
		}

		// Option D: Host header routing (wildcard DNS or host label)
		if targetSubdomain == "" {
			hostSub := subdomainFromHost(r.Host, baseDomain)
			if hostSub != "" {
				if _, exists := reg.get(hostSub); exists {
					targetSubdomain = hostSub
				}
			}
		}

		// Option E: Fallback to single active tunnel if only 1 tunnel is connected
		var t *tunnel
		var ok bool
		if targetSubdomain != "" {
			t, ok = reg.get(targetSubdomain)
		} else {
			t, ok = reg.getOnly()
		}

		if !ok {
			// If no tunnels are active and this is a browser/root probe, show friendly status page
			if reg.count() == 0 {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`<!DOCTYPE html><html><head><title>LocalTunnel Relay</title><style>body{font-family:system-ui,-apple-system,sans-serif;max-width:540px;margin:80px auto;padding:24px;color:#222;line-height:1.6}.badge{display:inline-block;padding:4px 10px;border-radius:12px;background:#e6f4ea;color:#137333;font-size:13px;font-weight:600}code{background:#f1f3f4;padding:2px 6px;border-radius:4px;font-size:13px}</style></head><body><span class="badge">Relay Running</span><h1>LocalTunnel Relay</h1><p>Relay is running and ready for client connections.</p><p>Health check: <code>/healthz</code> &nbsp;·&nbsp; Control endpoint: <code>/_control</code></p></body></html>`))
				return
			}
			http.Error(w, "no tunnel registered for this host/subdomain", http.StatusNotFound)
			return
		}

		bodyBytes, _ := io.ReadAll(r.Body)
		id := randomSubdomain() + randomSubdomain()

		respCh := make(chan Envelope, 1)
		t.pendingMu.Lock()
		t.pending[id] = respCh
		t.pendingMu.Unlock()

		err := t.send(Envelope{
			Type:    "request",
			ID:      id,
			Method:  r.Method,
			Path:    targetPath,
			Headers: r.Header,
			Body:    base64.StdEncoding.EncodeToString(bodyBytes),
		})
		if err != nil {
			t.pendingMu.Lock()
			delete(t.pending, id)
			t.pendingMu.Unlock()
			http.Error(w, "tunnel write failed: "+err.Error(), http.StatusBadGateway)
			return
		}

		select {
		case resp := <-respCh:
			for k, vals := range resp.Headers {
				for _, v := range vals {
					w.Header().Add(k, v)
				}
			}
			status := resp.Status
			if status == 0 {
				status = 200
			}
			w.WriteHeader(status)
			if resp.Body != "" {
				decoded, _ := base64.StdEncoding.DecodeString(resp.Body)
				w.Write(decoded)
			}
		case <-time.After(30 * time.Second):
			t.pendingMu.Lock()
			delete(t.pending, id)
			t.pendingMu.Unlock()
			http.Error(w, "tunnel response timeout", http.StatusGatewayTimeout)
		}
	}

	log.Printf("public server listening on %s (HTTP + WebSocket control)", addr)
	if err := http.ListenAndServe(addr, http.HandlerFunc(handler)); err != nil {
		log.Fatalf("public listen: %v", err)
	}
}

// ── Main entry point ───────────────────────────────────────────────────────

func main() {
	envPort := os.Getenv("PORT")
	defaultPublic := ":8080"
	defaultControl := ":7000"

	// If running on Render, Koyeb, Railway, etc., PORT is defined.
	// Default to single-port mode on :$PORT.
	if envPort != "" {
		defaultPublic = ":" + envPort
		defaultControl = ""
	}

	publicAddr := flag.String("public", defaultPublic, "address for public HTTP traffic and WebSocket control")
	controlAddr := flag.String("control", defaultControl, "address for tunnel client control connections (raw TCP; empty for single-port mode)")
	baseDomain := flag.String("domain", "", "base domain for subdomain routing, e.g. tunnel.example.com")
	authSecret := flag.String("secret", "", "static hex authentication secret required to register tunnels (or set RELAY_SECRET / AUTH_KEY)")
	flag.Parse()

	secretKey := getOrInitSecret(*authSecret)
	fmt.Println("============================================================")
	fmt.Printf("LOCAL TUNNEL RELAY AUTH KEY (HEX SECRET):\n  %s\n", secretKey)
	fmt.Println("Enter this Auth Key in your client app to connect.")
	fmt.Println("============================================================")

	reg := newRegistry()

	// Start raw TCP control server only if configured and different from public port
	if *controlAddr != "" && *controlAddr != *publicAddr {
		go runControlServer(*controlAddr, reg, *baseDomain, secretKey)
	} else {
		log.Printf("running in single-port mode on %s", *publicAddr)
	}

	runPublicServer(*publicAddr, reg, *baseDomain, secretKey)
}
