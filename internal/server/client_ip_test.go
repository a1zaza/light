package server

import (
	"net/http/httptest"
	"testing"
)

func TestPrivateClientIPHeaderAndFallback(t *testing.T) {
	f := newFixture(t)
	for _, tc := range []struct {
		name, header string
		values       []string
		want         string
	}{
		{"disabled", "", []string{"198.51.100.2"}, "192.0.2.10"},
		{"missing", "X-Light-Client-IP", nil, "192.0.2.10"},
		{"valid", "X-Light-Client-IP", []string{"198.51.100.2"}, "198.51.100.2"},
		{"ipv6", "X-Light-Client-IP", []string{"2001:db8::2"}, "2001:db8::2"},
		{"mapped", "X-Light-Client-IP", []string{"::ffff:198.51.100.2"}, "198.51.100.2"},
		{"empty", "X-Light-Client-IP", []string{""}, "192.0.2.10"},
		{"invalid", "X-Light-Client-IP", []string{"not-an-ip"}, "192.0.2.10"},
		{"chain", "X-Light-Client-IP", []string{"198.51.100.2, 198.51.100.3"}, "192.0.2.10"},
		{"duplicate", "X-Light-Client-IP", []string{"198.51.100.2", "198.51.100.3"}, "192.0.2.10"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value := *f.server.activeSettings.Load()
			value.RateLimitIPHeader = tc.header
			f.server.activeSettings.Store(&value)
			req := httptest.NewRequest("GET", "https://registry.test/api/session", nil)
			req.RemoteAddr = "192.0.2.10:1234"
			for _, value := range tc.values {
				req.Header.Add("X-Light-Client-IP", value)
			}
			if got := f.server.clientIP(req); got != tc.want {
				t.Fatalf("client IP=%q want=%q", got, tc.want)
			}
		})
	}
}
func TestProxyClientsHaveIndependentLimits(t *testing.T) {
	f := newFixture(t)
	value := *f.server.activeSettings.Load()
	value.RateLimitIPHeader = "X-Light-Client-IP"
	f.server.activeSettings.Store(&value)
	request := func(client string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "https://registry.test/auth/token", nil)
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("X-Light-Client-IP", client)
		w := httptest.NewRecorder()
		f.server.ServeHTTP(w, r)
		return w
	}
	for i := 0; i < 120; i++ {
		f.status(request("198.51.100.1"), 200)
	}
	f.status(request("198.51.100.1"), 429)
	f.status(request("198.51.100.2"), 200)
}
func TestRateHeaderNameValidation(t *testing.T) {
	for _, name := range []string{"X Bad", "X-Light-IP\r\nInjected", "X-Light-IP:"} {
		if _, err := ValidateRateLimitIPHeader(name); err == nil {
			t.Fatalf("accepted %q", name)
		}
	}
	if got, err := ValidateRateLimitIPHeader("x-light-client-ip"); err != nil || got != "X-Light-Client-Ip" {
		t.Fatal(got, err)
	}
}
