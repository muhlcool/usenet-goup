package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecurityMiddleware(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	h := securityMiddleware(ok)
	type tc struct {
		name, method, path, host, site, mode, origin, ctype string
		body                                                string
		want                                                int
	}
	cases := []tc{
		{"same-origin json", "POST", "/api/run", listenAddr, "same-origin", "cors", "http://" + listenAddr, "application/json", "{}", 200},
		{"cross-site no-cors text/plain", "POST", "/api/run", listenAddr, "cross-site", "no-cors", "https://evil.example", "text/plain", "{}", 403},
		{"dns rebinding host", "GET", "/api/config", "evil.example:8095", "same-origin", "cors", "", "", "", 403},
		{"img shutdown", "GET", "/api/shutdown", listenAddr, "cross-site", "no-cors", "", "", "", 403},
		{"old browser text/plain", "POST", "/api/run", listenAddr, "", "", "", "text/plain", "{}", 415},
		{"foreign origin", "POST", "/api/run", listenAddr, "", "", "https://evil.example", "application/json", "{}", 403},
		{"link navigation to UI", "GET", "/", listenAddr, "cross-site", "navigate", "", "", "", 200},
		{"typed URL", "GET", "/", "localhost:8095", "none", "navigate", "", "", "", 200},
		{"post without body", "POST", "/api/cache/clear", listenAddr, "same-origin", "cors", "http://" + listenAddr, "", "", 200},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, "http://"+c.host+c.path, strings.NewReader(c.body))
		req.Host = c.host
		for k, v := range map[string]string{"Sec-Fetch-Site": c.site, "Sec-Fetch-Mode": c.mode, "Origin": c.origin, "Content-Type": c.ctype} {
			if v != "" {
				req.Header.Set(k, v)
			}
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s: got %d want %d", c.name, rec.Code, c.want)
		}
	}
}

func TestSanitizeRequest(t *testing.T) {
	var r PipelineRequest
	r.PartSizeMB, r.Par2Pct, r.PasswordLength = -5, 500, 2
	r.Server.Connections = 9999
	sanitizeRequest(&r)
	if r.PartSizeMB < 1 || r.Par2Pct != 100 || r.PasswordLength < 8 || r.Server.Connections > 100 {
		t.Fatalf("%+v", r)
	}
}

func TestRequiredWorkSpace(t *testing.T) {
	if got := requiredWorkSpace(10<<30, 10); got < 11<<30 || got > 12<<30 {
		t.Fatalf("got %d", got)
	}
}
