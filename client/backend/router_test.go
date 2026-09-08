package backend

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestParseRouteRules_SingleAndMulti(t *testing.T) {
	// 1. Single target
	rt1, err := ParseRouteRules("http://127.0.0.1:3000")
	if err != nil {
		t.Fatalf("ParseRouteRules single target failed: %v", err)
	}
	if rt1.DefaultRoute == nil || rt1.DefaultRoute.Target.String() != "http://127.0.0.1:3000" {
		t.Errorf("expected default route http://127.0.0.1:3000, got %v", rt1.DefaultRoute)
	}

	// 2. Multi-line rules
	input := `
		# Microservices context paths
		/auth  -> http://127.0.0.1:8084
		/login -> :8082
		/user  -> :8085
		/otp   -> :8800
		/core  -> :8083
		/      -> :3000
	`
	rt2, err := ParseRouteRules(input)
	if err != nil {
		t.Fatalf("ParseRouteRules multi-line failed: %v", err)
	}
	if len(rt2.Routes) != 5 {
		t.Fatalf("expected 5 prefixed routes, got %d", len(rt2.Routes))
	}
	if rt2.DefaultRoute == nil || rt2.DefaultRoute.Target.Host != "127.0.0.1:3000" {
		t.Fatalf("expected default route to host 127.0.0.1:3000, got %v", rt2.DefaultRoute)
	}

	// Test Match
	tests := []struct {
		path     string
		wantHost string
		wantPath string
	}{
		{"/auth/verify", "127.0.0.1:8084", "/auth/verify"},
		{"/login/submit", "127.0.0.1:8082", "/login/submit"},
		{"/user/profile", "127.0.0.1:8085", "/user/profile"},
		{"/otp/send", "127.0.0.1:8800", "/otp/send"},
		{"/core/status", "127.0.0.1:8083", "/core/status"},
		{"/dashboard", "127.0.0.1:3000", "/dashboard"},
	}

	for _, tt := range tests {
		route, destURL := rt2.Match(tt.path)
		if route == nil {
			t.Errorf("Match(%q) returned nil route", tt.path)
			continue
		}
		if route.Target.Host != tt.wantHost {
			t.Errorf("Match(%q) target host = %q, want %q", tt.path, route.Target.Host, tt.wantHost)
		}
		expectedDest := "http://" + tt.wantHost + tt.wantPath
		if destURL != expectedDest {
			t.Errorf("Match(%q) destURL = %q, want %q", tt.path, destURL, expectedDest)
		}
	}
}

func TestLANServer_ContextPathRoutingGateway(t *testing.T) {
	// Start mock servers for login, auth, and default frontend
	loginServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("login-service:" + r.URL.Path))
	}))
	defer loginServer.Close()

	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("auth-service:" + r.URL.Path))
	}))
	defer authServer.Close()

	defaultServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("default-ui:" + r.URL.Path))
	}))
	defer defaultServer.Close()

	rules := `/login -> ` + loginServer.URL + `
/auth  -> ` + authServer.URL + `
/      -> ` + defaultServer.URL

	lan := NewLAN()
	urlMsg, err := lan.Start(rules, "19095", true)
	if err != nil {
		t.Fatalf("lan.Start failed: %v", err)
	}
	defer lan.Stop()
	t.Logf("LAN Gateway running: %s", urlMsg)

	time.Sleep(50 * time.Millisecond)

	// Test 1: Hit /login/check
	resp1, err := http.Get("http://127.0.0.1:19095/login/check")
	if err != nil {
		t.Fatalf("GET /login/check failed: %v", err)
	}
	b1, _ := io.ReadAll(resp1.Body)
	resp1.Body.Close()
	if string(b1) != "login-service:/login/check" {
		t.Errorf("got %q, want 'login-service:/login/check'", string(b1))
	}
	if resp1.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("missing CORS header on /login/check")
	}

	// Test 2: Hit /auth/token
	resp2, err := http.Get("http://127.0.0.1:19095/auth/token")
	if err != nil {
		t.Fatalf("GET /auth/token failed: %v", err)
	}
	b2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if string(b2) != "auth-service:/auth/token" {
		t.Errorf("got %q, want 'auth-service:/auth/token'", string(b2))
	}

	// Test 3: Hit /index.html (default route)
	resp3, err := http.Get("http://127.0.0.1:19095/index.html")
	if err != nil {
		t.Fatalf("GET /index.html failed: %v", err)
	}
	b3, _ := io.ReadAll(resp3.Body)
	resp3.Body.Close()
	if string(b3) != "default-ui:/index.html" {
		t.Errorf("got %q, want 'default-ui:/index.html'", string(b3))
	}

	// Test 4: Preflight OPTIONS on /auth/token
	req, _ := http.NewRequest("OPTIONS", "http://127.0.0.1:19095/auth/token", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "authorization, content-type, language")
	respOpt, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("OPTIONS /auth/token failed: %v", err)
	}
	respOpt.Body.Close()
	if respOpt.StatusCode != http.StatusNoContent {
		t.Errorf("OPTIONS status = %d, want 204", respOpt.StatusCode)
	}
	if respOpt.Header.Get("Access-Control-Allow-Origin") != "http://localhost:3000" {
		t.Errorf("OPTIONS origin = %q, want 'http://localhost:3000'", respOpt.Header.Get("Access-Control-Allow-Origin"))
	}
	if len(respOpt.Header["Access-Control-Allow-Origin"]) != 1 {
		t.Errorf("OPTIONS has multiple Access-Control-Allow-Origin headers: %v", respOpt.Header["Access-Control-Allow-Origin"])
	}

	// Test 5: Actual React POST request with Origin & custom headers
	postReq, _ := http.NewRequest("POST", "http://127.0.0.1:19095/auth/token", nil)
	postReq.Header.Set("Origin", "http://localhost:3000")
	postReq.Header.Set("Content-Type", "application/json")
	postReq.Header.Set("language", "EN")
	postResp, err := http.DefaultClient.Do(postReq)
	if err != nil {
		t.Fatalf("POST /auth/token failed: %v", err)
	}
	postResp.Body.Close()
	if postResp.Header.Get("Access-Control-Allow-Origin") != "http://localhost:3000" {
		t.Errorf("POST origin = %q, want 'http://localhost:3000'", postResp.Header.Get("Access-Control-Allow-Origin"))
	}
	if len(postResp.Header["Access-Control-Allow-Origin"]) != 1 {
		t.Errorf("POST has duplicate Access-Control-Allow-Origin headers: %v", postResp.Header["Access-Control-Allow-Origin"])
	}
	if postResp.Header.Get("Access-Control-Allow-Credentials") != "true" {
		t.Errorf("POST missing Access-Control-Allow-Credentials: true")
	}
}
