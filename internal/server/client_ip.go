package server

import (
	"net/http"
	"net/netip"
	"strings"

	"light/internal/config"
)

// Empty disables forwarded identity. Enabling a header is an explicit deployment
// trust decision: the upstream proxy must overwrite it and shield the backend.
func ValidateRateLimitIPHeader(value string) (string, error) { return config.Header(value) }

func (s *Server) clientIP(r *http.Request) string {
	header := s.activeSettings.Load().RateLimitIPHeader
	if header != "" {
		values := r.Header.Values(header)
		if len(values) == 1 {
			if address, err := netip.ParseAddr(strings.TrimSpace(values[0])); err == nil && address.Zone() == "" {
				return address.Unmap().String()
			}
		}
	}
	return ip(r)
}
