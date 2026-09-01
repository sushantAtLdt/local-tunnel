// Package backend contains the tunnel client logic, kept separate from the
// GUI (app.go) so it's plain, testable Go with no Wails dependency.
package backend

import (
	"bufio"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type Envelope struct {
	Type      string              `json:"type"`
	ID        string              `json:"id,omitempty"`
	Subdomain string              `json:"subdomain,omitempty"`
	Method    string              `json:"method,omitempty"`
	Path      string              `json:"path,omitempty"`
	Headers   map[string][]string `json:"headers,omitempty"`
	Body      string              `json:"body,omitempty"`
	Status    int                 `json:"status,omitempty"`
	Message   string              `json:"message,omitempty"`
}

func writeFrame(w io.Writer, e Envelope) error {
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

func readFrame(r io.Reader) (Envelope, error) {
	var e Envelope
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return e, err
	}
	n := binary.BigEndian.Uint32(hdr)
	if n > 32<<20 {
		return e, fmt.Errorf("frame too large: %d bytes", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return e, err
	}
	err := json.Unmarshal(buf, &e)
	return e, err
}

// Config describes one tunnel session.
type Config struct {
	RelayAddr   string // host:port of the relay's control port, e.g. "1.2.3.4:7000"
	Subdomain   string // desired subdomain; empty = let relay assign one
	LocalTarget string // e.g. "http://127.0.0.1:3000"
	InjectCORS  bool   // add permissive CORS headers to every response
}

// Client runs one active tunnel. Not safe for concurrent Start calls.
type Client struct {
	Log func(string) // called with human-readable status/log lines; may be nil

	mu        sync.Mutex
	conn      net.Conn
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
// assigned subdomain via the returned channel as soon as registration
// succeeds (or an error if it fails).
func (c *Client) Start(cfg Config) (string, error) {
	conn, err := net.Dial("tcp", cfg.RelayAddr)
	if err != nil {
		return "", fmt.Errorf("could not reach relay: %w", err)
	}
	c.mu.Lock()
	c.conn = conn
	c.stopped = false
	c.mu.Unlock()

	if err := writeFrame(conn, Envelope{Type: "register", Subdomain: cfg.Subdomain}); err != nil {
		conn.Close()
		return "", err
	}

	br := bufio.NewReader(conn)
	first, err := readFrame(br)
	if err != nil {
		conn.Close()
		return "", fmt.Errorf("no response from relay: %w", err)
	}
	if first.Type == "error" {
		conn.Close()
		return "", fmt.Errorf("relay rejected registration: %s", first.Message)
	}
	if first.Type != "registered" {
		conn.Close()
		return "", fmt.Errorf("unexpected relay response: %s", first.Type)
	}

	c.mu.Lock()
	c.assigned = first.Subdomain
	c.mu.Unlock()

	c.logf("tunnel live: subdomain %q -> %s", first.Subdomain, cfg.LocalTarget)

	go c.serveLoop(br, conn, cfg)

	return first.Subdomain, nil
}

func (c *Client) serveLoop(br *bufio.Reader, conn net.Conn, cfg Config) {
	var writeMu sync.Mutex
	send := func(e Envelope) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return writeFrame(conn, e)
	}

	for {
		e, err := readFrame(br)
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
		go c.handleRequest(e, cfg, send)
	}
}

func (c *Client) handleRequest(e Envelope, cfg Config, send func(Envelope) error) {
	bodyBytes, _ := base64.StdEncoding.DecodeString(e.Body)
	url := strings.TrimRight(cfg.LocalTarget, "/") + e.Path

	req, err := http.NewRequest(e.Method, url, strings_NewReader(bodyBytes))
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
		send(Envelope{Type: "response", ID: e.ID, Status: 502, Body: b64(fmt.Sprintf("local app unreachable: %v", err))})
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	headers := map[string][]string{}
	for k, v := range resp.Header {
		headers[k] = v
	}
	if cfg.InjectCORS {
		headers["Access-Control-Allow-Origin"] = []string{"*"}
		headers["Access-Control-Allow-Methods"] = []string{"GET, POST, PUT, PATCH, DELETE, OPTIONS"}
		headers["Access-Control-Allow-Headers"] = []string{"*"}
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
	if c.conn != nil {
		c.conn.Close()
	}
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func strings_NewReader(b []byte) io.Reader {
	if len(b) == 0 {
		return nil
	}
	return strings.NewReader(string(b))
}
