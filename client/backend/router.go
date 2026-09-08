package backend

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"
)

// Route defines a path prefix rule and its corresponding target upstream server.
type Route struct {
	Prefix string
	Target *url.URL
	Proxy  *httputil.ReverseProxy
}

// RouteTable manages path-based routing rules for microservices.
type RouteTable struct {
	Routes       []Route
	DefaultRoute *Route
}

// normalizeTargetURL ensures the target string has a valid http/https scheme and host.
func normalizeTargetURL(targetStr string) (*url.URL, error) {
	targetStr = strings.TrimSpace(targetStr)
	if targetStr == "" {
		return nil, fmt.Errorf("empty target URL")
	}

	// Support shorthand like ":8080" or "8080" -> "http://127.0.0.1:8080"
	if strings.HasPrefix(targetStr, ":") {
		targetStr = "http://127.0.0.1" + targetStr
	} else if !strings.Contains(targetStr, "://") {
		if !strings.Contains(targetStr, "/") && !strings.Contains(targetStr, ":") {
			targetStr = "http://127.0.0.1:" + targetStr
		} else {
			targetStr = "http://" + targetStr
		}
	}

	u, err := url.Parse(targetStr)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("invalid target URL %q", targetStr)
	}
	return u, nil
}

// ParseRouteRules parses either a single URL (e.g. "http://127.0.0.1:3000")
// or multi-line context path rules like:
//
//	/auth  -> http://127.0.0.1:8084
//	/login -> http://127.0.0.1:8082
//	/user  -> :8085
//	/      -> :3000
func ParseRouteRules(input string) (*RouteTable, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("no target address or route rules provided")
	}

	rt := &RouteTable{}
	lines := strings.Split(input, "\n")

	for lineNum, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}

		var prefix, targetStr string
		if strings.Contains(line, "->") {
			parts := strings.SplitN(line, "->", 2)
			prefix = strings.TrimSpace(parts[0])
			targetStr = strings.TrimSpace(parts[1])
		} else if strings.Contains(line, "=>") {
			parts := strings.SplitN(line, "=>", 2)
			prefix = strings.TrimSpace(parts[0])
			targetStr = strings.TrimSpace(parts[1])
		} else if strings.Contains(line, "=") && !strings.Contains(line, "://") {
			parts := strings.SplitN(line, "=", 2)
			prefix = strings.TrimSpace(parts[0])
			targetStr = strings.TrimSpace(parts[1])
		} else {
			// Single target without prefix rule
			prefix = "/"
			targetStr = line
		}

		if prefix == "" {
			prefix = "/"
		}
		if !strings.HasPrefix(prefix, "/") {
			prefix = "/" + prefix
		}
		// Trim trailing slash for non-root prefix
		if len(prefix) > 1 {
			prefix = strings.TrimRight(prefix, "/")
		}

		targetURL, err := normalizeTargetURL(targetStr)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNum+1, err)
		}

		route := Route{
			Prefix: prefix,
			Target: targetURL,
			Proxy:  httputil.NewSingleHostReverseProxy(targetURL),
		}

		if prefix == "/" {
			rt.DefaultRoute = &route
		} else {
			rt.Routes = append(rt.Routes, route)
		}
	}

	if len(rt.Routes) == 0 && rt.DefaultRoute == nil {
		return nil, fmt.Errorf("no valid routes defined")
	}

	// Sort routes by prefix length descending for longest-prefix matching
	sort.Slice(rt.Routes, func(i, j int) bool {
		return len(rt.Routes[i].Prefix) > len(rt.Routes[j].Prefix)
	})

	return rt, nil
}

// Match finds the best matching route for a request path and resolves the destination target URL.
func (rt *RouteTable) Match(reqPath string) (*Route, string) {
	cleanPath := reqPath
	if cleanPath == "" {
		cleanPath = "/"
	}

	for _, route := range rt.Routes {
		if cleanPath == route.Prefix || strings.HasPrefix(cleanPath, route.Prefix+"/") || strings.HasPrefix(cleanPath, route.Prefix+"?") {
			targetURL := *route.Target
			targetPath := strings.TrimRight(targetURL.Path, "/") + cleanPath
			targetURL.Path = targetPath
			return &route, targetURL.String()
		}
	}

	if rt.DefaultRoute != nil {
		targetURL := *rt.DefaultRoute.Target
		targetPath := strings.TrimRight(targetURL.Path, "/") + cleanPath
		targetURL.Path = targetPath
		return rt.DefaultRoute, targetURL.String()
	}

	// Fallback to first route if no default root defined
	if len(rt.Routes) > 0 {
		r := &rt.Routes[0]
		targetURL := *r.Target
		targetPath := strings.TrimRight(targetURL.Path, "/") + cleanPath
		targetURL.Path = targetPath
		return r, targetURL.String()
	}

	return nil, reqPath
}

// CreateGatewayHandler returns a unified HTTP handler for the RouteTable with CORS support.
func CreateGatewayHandler(rt *RouteTable, injectCORS bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if injectCORS {
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
		}

		route, _ := rt.Match(r.URL.Path)
		if route == nil || route.Proxy == nil {
			http.Error(w, "no matching backend route found", http.StatusBadGateway)
			return
		}

		route.Proxy.ServeHTTP(w, r)
	})
}
