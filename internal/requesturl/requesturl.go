// Package requesturl derives the current request origin without a configured public URL.
package requesturl

import (
	"net"
	"net/http"
	"net/url"
	"strings"
)

// Scheme supports HTTPS termination by a reverse proxy. The proxy must overwrite
// X-Forwarded-Proto and preserve Host; X-Forwarded-Host is never consulted.
func Scheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto == "https" || proto == "http" {
		return proto
	}
	return "http"
}
func Origin(r *http.Request) string { return (&url.URL{Scheme: Scheme(r), Host: r.Host}).String() }
func authority(host, scheme string) string {
	host = strings.ToLower(host)
	if h, p, err := net.SplitHostPort(host); err == nil && ((scheme == "https" && p == "443") || (scheme == "http" && p == "80")) {
		return strings.Trim(h, "[]")
	}
	return strings.Trim(host, "[]")
}

// SameOrigin accepts any deployment hostname, while still rejecting cross-site writes.
func SameOrigin(r *http.Request) bool {
	raw := r.Header.Get("Origin")
	origin := raw != ""
	if !origin {
		raw = r.Header.Get("Referer")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Scheme != Scheme(r) {
		return false
	}
	if origin && (u.Path != "" || u.RawQuery != "" || u.Fragment != "") {
		return false
	}
	return authority(u.Host, u.Scheme) == authority(r.Host, u.Scheme)
}
