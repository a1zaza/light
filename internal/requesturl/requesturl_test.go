package requesturl

import (
	"net/http/httptest"
	"testing"
)

func TestSameOriginAndProxyScheme(t *testing.T) {
	for _, tc := range []struct {
		name, target, origin, referrer, proto string
		want                                  bool
	}{
		{"same host", "http://localhost:5174/api/session", "http://localhost:5174", "", "", true},
		{"https proxy", "http://images.example/api/session", "https://images.example", "", "https", true},
		{"default https port", "https://images.example:443/api/session", "https://images.example", "", "", true},
		{"ipv6", "http://[::1]:5080/api/session", "http://[::1]:5080", "", "", true},
		{"referrer fallback", "https://images.example/api/session", "", "https://images.example/console?page=1", "", true},
		{"different domain", "https://images.example/api/session", "https://other.example", "", "", false},
		{"different port", "http://localhost:5080/api/session", "http://localhost:5174", "", "", false},
		{"scheme mismatch", "http://images.example/api/session", "https://images.example", "", "", false},
		{"null origin", "https://images.example/api/session", "null", "https://images.example/", "", false},
		{"missing origin", "https://images.example/api/session", "", "", "", false},
		{"origin with path", "https://images.example/api/session", "https://images.example/path", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", tc.target, nil)
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("Referer", tc.referrer)
			r.Header.Set("X-Forwarded-Proto", tc.proto)
			if got := SameOrigin(r); got != tc.want {
				t.Fatalf("same-origin=%t want=%t", got, tc.want)
			}
		})
	}
}
