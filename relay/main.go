// relay.go — the public-facing half of the tunnel.
// Deploy this ONE binary on any cheap VPS with a public IP.
// It has zero third-party dependencies — pure Go standard library.
//
// Two jobs:
//  1. Accept persistent control connections from tunnel clients (plain TCP).
//  2. Accept public HTTP traffic and forward it to the right client based on
//     the subdomain in the Host header, then relay the response back.
//
// Build:   go build -o relay main.go
// Run:     ./relay -public :8080 -control :7000 -domain tunnel.example.com
package main

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Envelope is the single message type exchanged on the control connection.
type Envelope struct {
	Type      string              `json:"type"` // register | registered | error | request | response
	ID        string              `json:"id,omitempty"`
	Subdomain string              `json:"subdomain,omitempty"`
	Method    string              `json:"method,omitempty"`
	Path      string              `json:"path,omitempty"`
	Headers   map[string][]string `json:"headers,omitempty"`
	Body      string              `json:"body,omitempty"` // base64
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

type tunnel struct {
	subdomain string
	conn      net.Conn
	writeMu   sync.Mutex

	pendingMu sync.Mutex
	pending   map[string]chan Envelope
}

func (t *tunnel) send(e Envelope) error {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	return writeFrame(t.conn, e)
}

type registry struct {
	mu      sync.RWMutex
	byName  map[string]*tunnel
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

func randomSubdomain() string {
	b := make([]byte, 5)
	rand.Read(b)
	return strings.ToLower(base64.RawURLEncoding.EncodeToString(b))
}

func handleControlConn(conn net.Conn, reg *registry) {
	defer conn.Close()
	br := bufio.NewReader(conn)

	first, err := readFrame(br)
	if err != nil || first.Type != "register" {
		writeFrame(conn, Envelope{Type: "error", Message: "expected register frame first"})
		return
	}

	name := strings.ToLower(strings.TrimSpace(first.Subdomain))
	if name == "" {
		name = randomSubdomain()
	}
	if _, exists := reg.get(name); exists {
		writeFrame(conn, Envelope{Type: "error", Message: "subdomain already in use"})
		return
	}

	t := &tunnel{subdomain: name, conn: conn, pending: make(map[string]chan Envelope)}
	reg.add(t)
	defer reg.remove(name)

	writeFrame(conn, Envelope{Type: "registered", Subdomain: name})
	log.Printf("tunnel registered: %s", name)

	// Everything after this is a response to a request we forwarded.
	for {
		e, err := readFrame(br)
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

func runControlServer(addr string, reg *registry) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("control listen: %v", err)
	}
	log.Printf("control server listening on %s", addr)
	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("control accept: %v", err)
			continue
		}
		go handleControlConn(conn, reg)
	}
}

func subdomainFromHost(host, baseDomain string) string {
	host = strings.Split(host, ":")[0] // strip port
	if baseDomain != "" && strings.HasSuffix(host, "."+baseDomain) {
		return strings.TrimSuffix(host, "."+baseDomain)
	}
	// Fallback: no wildcard DNS configured — treat first label as the name
	// e.g. myapp.localtest.me, or just "myapp" if that's literally the host.
	parts := strings.Split(host, ".")
	return parts[0]
}

func runPublicServer(addr string, reg *registry, baseDomain string) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		name := subdomainFromHost(r.Host, baseDomain)
		t, ok := reg.get(name)
		if !ok {
			http.Error(w, "no tunnel registered for \""+name+"\"", http.StatusNotFound)
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
			Path:    r.URL.RequestURI(),
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

	log.Printf("public server listening on %s", addr)
	if err := http.ListenAndServe(addr, http.HandlerFunc(handler)); err != nil {
		log.Fatalf("public listen: %v", err)
	}
}

func main() {
	publicAddr := flag.String("public", ":8080", "address for public HTTP traffic")
	controlAddr := flag.String("control", ":7000", "address for tunnel client control connections")
	baseDomain := flag.String("domain", "", "base domain for subdomain routing, e.g. tunnel.example.com (requires wildcard DNS *.tunnel.example.com -> this server). Leave empty to route by first host label instead.")
	flag.Parse()

	reg := newRegistry()
	go runControlServer(*controlAddr, reg)
	runPublicServer(*publicAddr, reg, *baseDomain)
}
