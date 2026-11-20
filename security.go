package main

import (
	"encoding/json"
	"net/http"
	"strings"
)

// securityMiddleware blockt Anfragen fremder Webseiten (CSRF) und DNS-Rebinding:
// nur Host 127.0.0.1/localhost, keine Cross-Site-Fetches, JSON-Pflicht für Bodies.
func securityMiddleware(next http.Handler) http.Handler {
	allowedHost := map[string]bool{listenAddr: true, "localhost:8095": true}
	allowedOrigin := map[string]bool{"http://" + listenAddr: true, "http://localhost:8095": true}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowedHost[r.Host] {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		switch r.Header.Get("Sec-Fetch-Site") {
		case "", "same-origin", "none":
		default:
			isPageNav := r.Method == http.MethodGet && r.Header.Get("Sec-Fetch-Mode") == "navigate" && !strings.HasPrefix(r.URL.Path, "/api/")
			if !isPageNav {
				http.Error(w, "cross-site request blocked", http.StatusForbidden)
				return
			}
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if o := r.Header.Get("Origin"); o != "" && !allowedOrigin[o] {
				http.Error(w, "cross-origin request blocked", http.StatusForbidden)
				return
			}
			if r.ContentLength != 0 && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				http.Error(w, "application/json required", http.StatusUnsupportedMediaType)
				return
			}
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

func sendJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func sendJSONError(w http.ResponseWriter, status int, msg string) {
	sendJSON(w, status, map[string]string{"error": msg})
}

func requirePOST(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	return true
}
