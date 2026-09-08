package backend

import (
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestParsePortSpec(t *testing.T) {
	tests := []struct {
		input   string
		want    []int
		wantErr bool
	}{
		{"9090", []int{9090}, false},
		{"8080-8084", []int{8080, 8081, 8082, 8083, 8084}, false},
		{"8080, 8082, 8085", []int{8080, 8082, 8085}, false},
		{"8080-8082, 8085", []int{8080, 8081, 8082, 8085}, false},
		{"", nil, true},
		{"invalid", nil, true},
		{"8085-8080", nil, true},
	}

	for _, tt := range tests {
		got, err := ParsePortSpec(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParsePortSpec(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && !reflect.DeepEqual(got, tt.want) {
			t.Errorf("ParsePortSpec(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestLANServer_CORSAndMultiPort(t *testing.T) {
	ts1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("backend-1"))
	}))
	defer ts1.Close()

	lan := NewLAN()
	portSpec := "19080-19081"
	urlMsg, err := lan.Start(ts1.URL, portSpec, true)
	if err != nil {
		t.Fatalf("lan.Start failed: %v", err)
	}
	defer lan.Stop()

	t.Logf("LAN started: %s", urlMsg)

	time.Sleep(50 * time.Millisecond)

	// 1. Test CORS preflight OPTIONS request
	req, err := http.NewRequest("OPTIONS", "http://127.0.0.1:19080/api/data", nil)
	if err != nil {
		t.Fatalf("create request failed: %v", err)
	}
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "authorization, content-type")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("OPTIONS request failed: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("OPTIONS status = %d, want 204", resp.StatusCode)
	}
	if resp.Header.Get("Access-Control-Allow-Origin") != "http://localhost:3000" {
		t.Errorf("Allow-Origin = %q, want 'http://localhost:3000'", resp.Header.Get("Access-Control-Allow-Origin"))
	}
	if resp.Header.Get("Access-Control-Allow-Headers") != "authorization, content-type" {
		t.Errorf("Allow-Headers = %q, want 'authorization, content-type'", resp.Header.Get("Access-Control-Allow-Headers"))
	}

	// 2. Test GET request has CORS headers
	getReq, err := http.NewRequest("GET", "http://127.0.0.1:19080/test", nil)
	if err != nil {
		t.Fatalf("create GET request failed: %v", err)
	}
	getReq.Header.Set("Origin", "http://myfrontend.com")

	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		t.Fatalf("GET request failed: %v", err)
	}
	body, _ := io.ReadAll(getResp.Body)
	getResp.Body.Close()

	if string(body) != "backend-1" {
		t.Errorf("GET body = %q, want 'backend-1'", string(body))
	}
	if getResp.Header.Get("Access-Control-Allow-Origin") != "http://myfrontend.com" {
		t.Errorf("GET Allow-Origin = %q, want 'http://myfrontend.com'", getResp.Header.Get("Access-Control-Allow-Origin"))
	}

	// 3. Test second port in range (19081)
	getReq2, err := http.NewRequest("GET", "http://127.0.0.1:19081/test", nil)
	if err != nil {
		t.Fatalf("create GET request for 19081 failed: %v", err)
	}
	getResp2, err := http.DefaultClient.Do(getReq2)
	if err != nil {
		t.Fatalf("GET 19081 request failed: %v", err)
	}
	getResp2.Body.Close()
	if getResp2.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("GET 19081 Allow-Origin = %q, want '*'", getResp2.Header.Get("Access-Control-Allow-Origin"))
	}
}

func TestLANServer_PortRange8080To8088(t *testing.T) {
	ports, err := ParsePortSpec("8080-8088")
	if err != nil {
		t.Fatalf("unexpected error parsing 8080-8088: %v", err)
	}
	if len(ports) != 9 {
		t.Fatalf("expected 9 ports, got %d", len(ports))
	}
	if ports[0] != 8080 || ports[8] != 8088 {
		t.Errorf("expected range 8080..8088, got %d..%d", ports[0], ports[8])
	}
}
