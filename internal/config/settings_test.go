package config

import "testing"

func TestNormalizeBounds(t *testing.T) {
	base := Default()
	base.RateLimitIPHeader = "x-light-client-ip"
	got, err := base.Normalize()
	if err != nil || got.RateLimitIPHeader != "X-Light-Client-Ip" {
		t.Fatal(got, err)
	}
	for name, change := range map[string]func(*Settings){
		"header":            func(s *Settings) { s.RateLimitIPHeader = "X-IP\r\nBad" },
		"capacity":          func(s *Settings) { s.HubCacheMaxBytes = 0 },
		"capacity-overflow": func(s *Settings) { s.HubCacheMaxBytes = 1 << 51 },
		"blob":              func(s *Settings) { s.HubMaxBlobBytes = 21 << 30 },
		"concurrency":       func(s *Settings) { s.HubFetchConcurrency = 33 },
		"timeout":           func(s *Settings) { s.HubFetchTimeoutSeconds = 3601 },
		"revision":          func(s *Settings) { s.Revision = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			s := Default()
			change(&s)
			if _, err := s.Normalize(); err == nil {
				t.Fatal("invalid settings accepted")
			}
		})
	}
}
