// Package backend contains the tunnel client logic, kept separate from the
// GUI (app.go) so it's plain, testable Go with no Wails dependency.
package backend

import (
	"bufio"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type Envelope struct {
	Type      string              `json:"type"` // register | registered | error | request | response
	ID        string              `json:"id,omitempty"`
	Subdomain string              `json:"subdomain,omitempty"`
	URL       string              `json:"url,omitempty"`
	Method    string              `json:"method,omitempty"`
	Path      string              `json:"path,omitempty"`
	Headers   map[string][]string `json:"headers,omitempty"`
	Body      string              `json:"body,omitempty"`
	Status    int                 `json:"status,omitempty"`
	Message   string              `json:"message,omitempty"`
}

// ControlConn abstracts the control channel between client and relay.
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

// ── RFC 6455 WebSocket Client Implementation ────────────────────────────────

const (
	wsOpText   = 0x1
	wsOpBinary = 0x2
	wsOpClose  = 0x8
	wsOpPing   = 0x9
	wsOpPong   = 0xA
)

func wsReadFrame(r io.Reader) (byte, []byte, error) {
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return 0, nil, err
	}
	opcode := hdr[0] & 0x0F
	masked := (hdr[1] & 0x80) != 0
	payloadLen := uint64(hdr[1] & 0x7F)

	if payloadLen == 126 {
		ext := make([]byte, 2)
		if _, err := io.ReadFull(r, ext); err != nil {
			return 0, nil, err
		}
		payloadLen = uint64(binary.BigEndian.Uint16(ext))
	} else if payloadLen == 127 {
		ext := make([]byte, 8)
		if _, err := io.ReadFull(r, ext); err != nil {
			return 0, nil, err
		}
		payloadLen = binary.BigEndian.Uint64(ext)
	}

	if payloadLen > 32<<20 {
		return 0, nil, fmt.Errorf("ws payload too large: %d", payloadLen)
	}

	var maskKey []byte
	if masked {
		maskKey = make([]byte, 4)
		if _, err := io.ReadFull(r, maskKey); err != nil {
			return 0, nil, err
		}
	}

	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}

	if masked {
		for i := uint64(0); i < payloadLen; i++ {
			payload[i] ^= maskKey[i%4]
		}
	}

	return opcode, payload, nil
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

func wsReadMessage(r io.Reader, w io.Writer, writeMu *sync.Mutex, isClient bool) ([]byte, error) {
	for {
		opcode, payload, err := wsReadFrame(r)
		if err != nil {
			return nil, err
		}
		switch opcode {
		case wsOpText, wsOpBinary:
			return payload, nil
		case wsOpPing:
			writeMu.Lock()
			_ = wsWriteFrame(w, isClient, wsOpPong, payload)
			writeMu.Unlock()
		case wsOpPong:
			continue
		case wsOpClose:
			writeMu.Lock()
			_ = wsWriteFrame(w, isClient, wsOpClose, nil)
			writeMu.Unlock()
			return nil, io.EOF
		default:
			continue
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

// startPingLoop periodically sends WebSocket Ping frames to defeat cloud idle timeouts (Render/Koyeb 100s).
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

func wsClientHandshake(conn net.Conn, host, path string) (*bufio.Reader, error) {
	if path == "" {
		path = "/_control"
	}
	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)

	req := fmt.Sprintf("GET %s HTTP/1.1\r\n"+
		"Host: %s\r\n"+
		"Upgrade: websocket\r\n"+
		"Connection: Upgrade\r\n"+
		"Sec-WebSocket-Key: %s\r\n"+
		"Sec-WebSocket-Version: 13\r\n\r\n", path, host, key)

	if _, err := conn.Write([]byte(req)); err != nil {
		return nil, fmt.Errorf("failed to send ws handshake: %w", err)
	}

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: "GET"})
	if err != nil {
		return nil, fmt.Errorf("failed to read ws handshake response: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("relay rejected ws upgrade (status %d): %s", resp.StatusCode, string(body))
	}
	return br, nil
}

// ── Dialing logic ──────────────────────────────────────────────────────────

func dialRelay(rawAddr string) (ControlConn, string, error) {
	addr := strings.TrimSpace(rawAddr)
	if addr == "" {
		return nil, "", fmt.Errorf("relay address cannot be empty")
	}

	// Clean up common user input variations like tcp///..., trailing slashes
	for strings.Contains(addr, "///") {
		addr = strings.ReplaceAll(addr, "///", "://")
	}
	addr = strings.TrimRight(addr, "/")

	useTLS := false
	useWS := false
	useTCP := false

	lower := strings.ToLower(addr)
	if strings.HasPrefix(lower, "wss://") {
		useTLS = true
		useWS = true
		addr = addr[6:]
	} else if strings.HasPrefix(lower, "https://") {
		useTLS = true
		useWS = true
		addr = addr[8:]
	} else if strings.HasPrefix(lower, "ws://") {
		useWS = true
		addr = addr[5:]
	} else if strings.HasPrefix(lower, "http://") {
		useWS = true
		addr = addr[7:]
	} else if strings.HasPrefix(lower, "tcp://") {
		useTCP = true
		addr = addr[6:]
	}

	// Check if this is a known cloud PaaS domain (Render, Koyeb, Railway, Fly)
	isCloudDomain := strings.Contains(strings.ToLower(addr), "onrender.com") ||
		strings.Contains(strings.ToLower(addr), "koyeb.app") ||
		strings.Contains(strings.ToLower(addr), "railway.app") ||
		strings.Contains(strings.ToLower(addr), "fly.dev")

	if isCloudDomain {
		// Cloud domains always use HTTPS/TLS WebSocket on port 443 externally
		useTLS = true
		useWS = true
		useTCP = false
	} else if !useWS && !useTCP {
		if strings.HasSuffix(addr, ":7000") {
			useTCP = true
		} else if strings.HasSuffix(addr, ":443") || !strings.Contains(addr, ":") {
			useTLS = true
			useWS = true
		} else if strings.HasSuffix(addr, ":80") {
			useWS = true
		} else {
			// e.g. 127.0.0.1:8080 — try WebSocket on single port first
			useWS = true
		}
	}

	if useTCP {
		conn, err := net.DialTimeout("tcp", addr, 15*time.Second)
		if err != nil {
			return nil, "", fmt.Errorf("could not reach relay TCP: %w", err)
		}
		return &tcpControlConn{conn: conn, br: bufio.NewReader(conn)}, addr, nil
	}

	// Parse host, port, path for WebSocket
	parsedURL, err := url.Parse("http://" + addr)
	if err != nil {
		return nil, "", fmt.Errorf("invalid relay address: %w", err)
	}

	host := parsedURL.Hostname()
	port := parsedURL.Port()
	path := parsedURL.Path
	if path == "" || path == "/" {
		path = "/_control"
	}

	if isCloudDomain {
		// External cloud traffic is always port 443 HTTPS. Override any container-internal ports like 8080/7000/10000.
		if port == "" || port == "8080" || port == "7000" || port == "10000" || port == "8000" {
			port = "443"
		}
	} else if port == "" {
		if useTLS {
			port = "443"
		} else {
			port = "80"
		}
	}

	targetAddr := net.JoinHostPort(host, port)
	var conn net.Conn
	if useTLS {
		tlsConfig := &tls.Config{
			ServerName: host,
		}
		conn, err = tls.DialWithDialer(&net.Dialer{Timeout: 15 * time.Second}, "tcp", targetAddr, tlsConfig)
	} else {
		conn, err = net.DialTimeout("tcp", targetAddr, 15*time.Second)
	}

	if err != nil {
		// If connecting to custom port like :8080 failed as WebSocket, and no explicit scheme was given,
		// attempt fallback to raw TCP
		if !useTLS && port != "80" && port != "443" {
			tcpConn, tcpErr := net.DialTimeout("tcp", targetAddr, 10*time.Second)
			if tcpErr == nil {
				return &tcpControlConn{conn: tcpConn, br: bufio.NewReader(tcpConn)}, targetAddr, nil
			}
		}
		return nil, "", fmt.Errorf("could not reach relay (%s): %w", targetAddr, err)
	}

	hostHeader := host
	if (useTLS && port != "443") || (!useTLS && port != "80") {
		hostHeader = targetAddr
	}

	br, err := wsClientHandshake(conn, hostHeader, path)
	if err != nil {
		conn.Close()
		return nil, "", err
	}

	wsConn := &wsControlConn{
		conn:     conn,
		br:       br,
		isClient: true,
		closed:   make(chan struct{}),
	}
	wsConn.startPingLoop(25 * time.Second)

	return wsConn, hostHeader, nil
}

// ── Client Session ─────────────────────────────────────────────────────────

// Config describes one tunnel session.
type Config struct {
	RelayAddr   string // host:port or URL of the relay (e.g. "my-relay.onrender.com", "1.2.3.4:7000")
	Subdomain   string // desired subdomain; empty = let relay assign one
	LocalTarget string // e.g. "http://127.0.0.1:3000"
	InjectCORS  bool   // add permissive CORS headers to every response
}

// Client runs one active tunnel. Not safe for concurrent Start calls.
type Client struct {
	Log func(string) // called with human-readable status/log lines; may be nil

	mu        sync.Mutex
	ctrl      ControlConn
	stopped   bool
	assigned  string
	httpProxy *http.Client
}

func New() *Client {
	return &Client{httpProxy: &http.Client{Timeout: 25 * time.Second}}
}

func (c *Client) logf(format string, args ...any) {
	if c.Log != nil {
		c.Log(fmt.Sprintf(format, args...))
	}
}

// Start connects to the relay and blocks, serving requests, until Stop is
// called or the connection drops. Run it in a goroutine. Returns the
// assigned URL or subdomain as soon as registration succeeds.
func (c *Client) Start(cfg Config) (string, error) {
	ctrl, connectedHost, err := dialRelay(cfg.RelayAddr)
	if err != nil {
		return "", err
	}

	c.mu.Lock()
	c.ctrl = ctrl
	c.stopped = false
	c.mu.Unlock()

	if err := ctrl.WriteEnvelope(Envelope{Type: "register", Subdomain: cfg.Subdomain}); err != nil {
		ctrl.Close()
		return "", fmt.Errorf("failed to send registration: %w", err)
	}

	first, err := ctrl.ReadEnvelope()
	if err != nil {
		ctrl.Close()
		return "", fmt.Errorf("no response from relay: %w", err)
	}
	if first.Type == "error" {
		ctrl.Close()
		return "", fmt.Errorf("relay rejected registration: %s", first.Message)
	}
	if first.Type != "registered" {
		ctrl.Close()
		return "", fmt.Errorf("unexpected relay response: %s", first.Type)
	}

	displayURL := first.URL
	if displayURL == "" {
		if strings.Contains(connectedHost, "onrender.com") || strings.Contains(connectedHost, "koyeb.app") ||
			strings.Contains(connectedHost, "railway.app") || strings.Contains(connectedHost, "fly.dev") {
			displayURL = "https://" + connectedHost
		} else {
			displayURL = first.Subdomain
		}
	}

	c.mu.Lock()
	c.assigned = displayURL
	c.mu.Unlock()

	rt, err := ParseRouteRules(cfg.LocalTarget)
	if err != nil {
		ctrl.Close()
		return "", fmt.Errorf("invalid routing rules: %w", err)
	}

	c.logf("tunnel live: %s -> %s", displayURL, cfg.LocalTarget)

	go c.serveLoop(ctrl, cfg, rt)

	return displayURL, nil
}

func (c *Client) serveLoop(ctrl ControlConn, cfg Config, rt *RouteTable) {
	var writeMu sync.Mutex
	send := func(e Envelope) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return ctrl.WriteEnvelope(e)
	}

	for {
		e, err := ctrl.ReadEnvelope()
		if err != nil {
			c.mu.Lock()
			stopped := c.stopped
			c.mu.Unlock()
			if !stopped {
				c.logf("tunnel disconnected: %v", err)
			}
			return
		}
		if e.Type != "request" {
			continue
		}
		go c.handleRequest(e, cfg, rt, send)
	}
}

func (c *Client) handleRequest(e Envelope, cfg Config, rt *RouteTable, send func(Envelope) error) {
	origin := "*"
	if vals, ok := e.Headers["Origin"]; ok && len(vals) > 0 && vals[0] != "" {
		origin = vals[0]
	}

	if cfg.InjectCORS && strings.EqualFold(e.Method, "OPTIONS") {
		headers := map[string][]string{
			"Access-Control-Allow-Origin":   {origin},
			"Access-Control-Allow-Methods":  {"GET, POST, PUT, PATCH, DELETE, OPTIONS, HEAD"},
			"Access-Control-Allow-Headers":  {"*"},
			"Access-Control-Expose-Headers": {"*"},
			"Access-Control-Max-Age":        {"86400"},
		}
		if origin != "*" {
			headers["Access-Control-Allow-Credentials"] = []string{"true"}
		}
		c.logf("%s %s -> 204 (CORS preflight handled)", e.Method, e.Path)
		send(Envelope{
			Type:    "response",
			ID:      e.ID,
			Status:  204,
			Headers: headers,
		})
		return
	}

	bodyBytes, _ := base64.StdEncoding.DecodeString(e.Body)
	_, destURL := rt.Match(e.Path)

	req, err := http.NewRequest(e.Method, destURL, strings_NewReader(bodyBytes))
	if err != nil {
		send(Envelope{Type: "response", ID: e.ID, Status: 502, Body: b64(fmt.Sprintf("bad request: %v", err))})
		return
	}
	for k, vals := range e.Headers {
		for _, v := range vals {
			req.Header.Add(k, v)
		}
	}

	resp, err := c.httpProxy.Do(req)
	if err != nil {
		c.logf("local request failed: %v", err)
		errHeaders := map[string][]string{}
		if cfg.InjectCORS {
			errHeaders["Access-Control-Allow-Origin"] = []string{origin}
			errHeaders["Access-Control-Allow-Methods"] = []string{"GET, POST, PUT, PATCH, DELETE, OPTIONS, HEAD"}
			errHeaders["Access-Control-Allow-Headers"] = []string{"*"}
			errHeaders["Access-Control-Expose-Headers"] = []string{"*"}
			if origin != "*" {
				errHeaders["Access-Control-Allow-Credentials"] = []string{"true"}
			}
		}
		send(Envelope{Type: "response", ID: e.ID, Status: 502, Headers: errHeaders, Body: b64(fmt.Sprintf("local app unreachable: %v", err))})
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	headers := map[string][]string{}
	for k, v := range resp.Header {
		headers[k] = v
	}
	if cfg.InjectCORS {
		headers["Access-Control-Allow-Origin"] = []string{origin}
		headers["Access-Control-Allow-Methods"] = []string{"GET, POST, PUT, PATCH, DELETE, OPTIONS, HEAD"}
		headers["Access-Control-Allow-Headers"] = []string{"*"}
		headers["Access-Control-Expose-Headers"] = []string{"*"}
		headers["Access-Control-Max-Age"] = []string{"86400"}
		if origin != "*" {
			headers["Access-Control-Allow-Credentials"] = []string{"true"}
		}
	}

	c.logf("%s %s -> %d", e.Method, e.Path, resp.StatusCode)

	send(Envelope{
		Type:    "response",
		ID:      e.ID,
		Status:  resp.StatusCode,
		Headers: headers,
		Body:    base64.StdEncoding.EncodeToString(respBody),
	})
}

// Stop closes the tunnel connection.
func (c *Client) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopped = true
	if c.ctrl != nil {
		c.ctrl.Close()
	}
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func strings_NewReader(b []byte) io.Reader {
	if len(b) == 0 {
		return nil
	}
	return strings.NewReader(string(b))
}
