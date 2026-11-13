package server

import (
	"encoding/json"
	"errors"
	"light/internal/config"
	"light/internal/hubproxy"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestAdminApplicationSettings(t *testing.T) {
	f := newFixture(t)
	f.status(f.request("GET", "/api/admin/settings", "", "", false), 401)
	var initial settingsResponse
	w := f.request("GET", "/api/admin/settings", "", "", true)
	f.status(w, 200)
	f.decode(w, &initial)
	if initial.Settings != config.Default() {
		t.Fatal(initial)
	}
	next := initial.Settings
	next.HubEnabled = true
	next.RateLimitIPHeader = "x-light-ip"
	next.HubFetchConcurrency = 6
	body, _ := json.Marshal(next)
	// Existing session/CSRF policy applies to the new settings endpoint.
	r := httptest.NewRequest("PATCH", "https://registry.test/api/admin/settings", nil)
	r.AddCookie(f.cookie)
	r.Header.Set("Origin", "https://registry.test")
	rec := httptest.NewRecorder()
	f.server.ServeHTTP(rec, r)
	f.status(rec, http.StatusForbidden)
	w = f.request("PATCH", "/api/admin/settings", string(body), "", true)
	f.status(w, 200)
	var saved settingsResponse
	f.decode(w, &saved)
	if !saved.Settings.HubEnabled || saved.Settings.Revision != 2 || !saved.Effective.HubEnabled || saved.Settings.RateLimitIPHeader != "X-Light-Ip" {
		t.Fatal(saved)
	}
	if f.server.activeSettings.Load().RateLimitIPHeader != "X-Light-Ip" || f.reg.HubCache() == nil || !f.reg.HubCache().Configuration().Enabled {
		t.Fatal("配置未即时生效")
	}
	f.status(f.request("PATCH", "/api/admin/settings", string(body), "", true), 409)
	invalid := saved.Settings
	invalid.HubFetchConcurrency = 0
	raw, _ := json.Marshal(invalid)
	f.status(f.request("PATCH", "/api/admin/settings", string(raw), "", true), 400)
	if f.store.Settings().Revision != 2 {
		t.Fatal("failed save changed revision")
	}
	// Disabling preserves the cache instance and immediately rejects new proxy pulls.
	reset := initial.Settings
	reset.Revision = 2
	raw, _ = json.Marshal(reset)
	w = f.request("PATCH", "/api/admin/settings", string(raw), "", true)
	f.status(w, 200)
	f.decode(w, &saved)
	if saved.Effective.HubEnabled || f.reg.HubCache().Configuration().Enabled {
		t.Fatal(saved)
	}

}

func TestLiveSettingsCommitFailureAndClientIP(t *testing.T) {
	f := newFixture(t)
	cfg := hubproxy.DefaultConfig()
	cfg.Enabled = true
	if err := f.reg.ConfigureHub(cfg, nil); err != nil {
		t.Fatal(err)
	}
	cache := f.reg.HubCache()
	changed := cfg
	changed.Enabled = false
	changed.Concurrency = 1
	expected := errors.New("synthetic durable commit failure")
	if err := f.reg.ConfigureHub(changed, func() error { return expected }); !errors.Is(err, expected) {
		t.Fatal(err)
	}
	if f.reg.HubCache() != cache || cache.Configuration() != cfg {
		t.Fatal("持久化失败改变了运行设置")
	}
	// Publish the header through the actual settings endpoint and immediately use
	// it in request attribution, without recreating the server or its native App.
	app := f.reg.App
	value := f.store.Settings()
	value.RateLimitIPHeader = "X-Live-IP"
	body, _ := json.Marshal(value)
	f.status(f.request("PATCH", "/api/admin/settings", string(body), "", true), 200)
	req := httptest.NewRequest("GET", "https://registry.test/auth/token", nil)
	req.RemoteAddr = "192.0.2.10:1234"
	req.Header.Set("X-Live-IP", "198.51.100.10")
	if got := f.server.clientIP(req); got != "198.51.100.10" {
		t.Fatal(got)
	}
	if f.reg.App != app || f.reg.HubCache() != cache {
		t.Fatal("保存重建了 Registry 或 Hub")
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			_ = f.server.clientIP(req)
		}
	}()
	value = f.store.Settings()
	value.RateLimitIPHeader = ""
	body, _ = json.Marshal(value)
	f.status(f.request("PATCH", "/api/admin/settings", string(body), "", true), 200)
	wg.Wait()
	if got := f.server.clientIP(req); got != "192.0.2.10" {
		t.Fatal(got)
	}
}
