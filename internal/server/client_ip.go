package server

import (
	"errors"
	"net/http"
	"net/netip"
	"strings"

	"golang.org/x/net/http/httpguts"
)

// Empty disables forwarded identity. Enabling a header is an explicit deployment
// trust decision: the upstream proxy must overwrite it and shield the backend.
func ValidateRateLimitIPHeader(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if !httpguts.ValidHeaderFieldName(value) {
		return "", errors.New("限流 IP Header 名称不合法")
	}
	return http.CanonicalHeaderKey(value), nil
}
func (s *Server) clientIP(r *http.Request) string {
	if s.clientIPHeader != "" {
		values := r.Header.Values(s.clientIPHeader)
		if len(values) == 1 {
			if address, err := netip.ParseAddr(strings.TrimSpace(values[0])); err == nil && address.Zone() == "" {
				return address.Unmap().String()
			}
		}
	}
	return ip(r)
}
