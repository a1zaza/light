package server

import (
	"context"
	"github.com/distribution/distribution/v3/registry/storage"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"light/internal/hubproxy"
)

func TestHubProxyNativePriorityAndPrivateSource(t *testing.T) {
	f := newFixture(t)
	name := "library/alpine"
	token := f.token(name, true)
	dgst, _, _ := f.image(name, "own", token, "amd64")
	// A paused cache lets us assert native routing without network access.
	path := filepath.Join(f.store.Dir, "hub-cache", "metadata", "state.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := f.reg.ConfigureHub(enabledHubConfig(), nil); err != nil {
		t.Fatal(err)
	}
	f.status(f.request("GET", "/v2/"+name+"/manifests/own", "", token, false), 200)
	f.status(f.request("GET", "/v2/"+name+"/manifests/"+dgst, "", token, false), 200)
	// Native pushes still work with the proxy enabled and paused.
	f.image(name, "second", token, "amd64")
	f.status(f.request("GET", "/v2/"+name+"/manifests/missing", "", token, false), 503)
	f.status(f.request("GET", "/v2/"+name+"/manifests/missing", "", "", false), 401)
	// Publicity applies only to native tags, even with a valid anonymous token.
	f.visibility(name, true, 0)
	anonymous := f.token(name, false)
	f.status(f.request("GET", "/v2/"+name+"/manifests/own", "", anonymous, false), 200)
	f.status(f.request("GET", "/v2/"+name+"/manifests/missing", "", anonymous, false), 401)
	f.status(f.request("GET", "/v2/"+name+"/blobs/sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "", anonymous, false), 401)
	f.status(f.request("GET", "/api/admin/hub-cache", "", "", false), 401)
	f.status(f.request("GET", "/api/admin/hub-cache", "", "", true), 200)
	f.status(f.request("POST", "/api/admin/hub-cache", `{"action":"all"}`, "", false), 401)
	// Revocation is checked on every request, before the cache is consulted.
	f.status(f.request("DELETE", "/api/admin/robot?id="+f.robotID, "", "", true), 200)
	f.status(f.request("GET", "/v2/"+name+"/manifests/own", "", token, false), 401)
}
func TestHubCacheDisabled(t *testing.T) {
	f := newFixture(t)
	f.status(f.request("GET", "/api/admin/hub-cache", "", "", true), 200)
	f.status(f.request("POST", "/api/admin/hub-cache", `{"action":"expired"}`, "", true), 409)
}

func TestHubProxyMissingNativeLayerIsNotReplaced(t *testing.T) {
	f := newFixture(t)
	name := "library/alpine"
	token := f.token(name, true)
	_, _, layer := f.image(name, "native", token, "amd64")
	if err := f.reg.ConfigureHub(enabledHubConfig(), nil); err != nil {
		t.Fatal(err)
	}
	// Fault injection only in this temporary fixture, through the public Vacuum API.
	if err := storage.NewVacuum(context.Background(), f.reg.Driver).RemoveBlob(layer); err != nil {
		t.Fatal(err)
	}
	w := f.request("GET", "/v2/"+name+"/blobs/"+layer, "", token, false)
	f.status(w, 503)
	if !strings.Contains(w.Body.String(), "自有镜像读取失败") {
		t.Fatalf("原生损坏进入了代理：%s", w.Body)
	}
}

func enabledHubConfig() hubproxy.Config {
	cfg := hubproxy.DefaultConfig()
	cfg.Enabled = true
	return cfg
}
