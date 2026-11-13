package registry

import (
	"context"
	"errors"
	"light/internal/hubproxy"
	"net"
	"net/http"
	"strings"
	"time"

	distribution "github.com/distribution/distribution/v3"
	"github.com/distribution/distribution/v3/registry/auth"
	"github.com/opencontainers/go-digest"
)

// proxyRead runs under Gate; writes and native tag listings always use the original App.
func (reg *Registry) proxyRead(w http.ResponseWriter, r *http.Request, cache *hubproxy.Cache) bool {
	if r.Method != "GET" && r.Method != "HEAD" {
		return false
	}
	kind, name, ref := "", "", ""
	for _, k := range []string{"manifests", "blobs"} {
		marker := "/" + k + "/"
		i := strings.LastIndex(r.URL.Path, marker)
		if i > len("/v2/") {
			name = r.URL.Path[len("/v2/"):i]
			ref = r.URL.Path[i+len(marker):]
			kind = k
			break
		}
	}
	if kind == "" || ref == "" || strings.Contains(ref, "/") {
		return false
	}
	access := auth.Access{Resource: auth.Resource{Type: "repository", Name: name}, Action: "pull"}
	if _, err := reg.Auth.Authorized(r, access); err != nil {
		challenge{[]auth.Access{access}}.SetHeaders(r, w)
		protocolError(w, 401, "UNAUTHORIZED", "需要镜像仓库认证")
		return true
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	own, err := reg.hasNative(ctx, name, kind, ref)
	if err != nil {
		protocolError(w, 503, "UNAVAILABLE", "自有镜像读取失败，未尝试回源")
		return true
	}
	if own {
		return false
	}
	cl, err := reg.Auth.parse(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if err != nil || cl.Subject == "anonymous" {
		challenge{[]auth.Access{access}}.SetHeaders(r, w)
		protocolError(w, 401, "UNAUTHORIZED", "代理镜像需要机器人认证")
		return true
	}
	clientIP, _, _ := net.SplitHostPort(r.RemoteAddr)
	if reg.ClientIP != nil {
		clientIP = reg.ClientIP(r)
	}
	if kind == "manifests" && !cache.Admit(cl.Subject, clientIP) {
		w.Header().Set("Retry-After", "60")
		protocolError(w, 429, "TOOMANYREQUESTS", "代理请求速率已达上限")
		return true
	}
	w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
	cache.Serve(w, r, name, kind, ref)
	return true
}
func (reg *Registry) hasNative(ctx context.Context, name, kind, ref string) (bool, error) {
	repo, err := reg.Repository(ctx, name)
	if err != nil {
		return false, err
	}
	if kind == "blobs" {
		d, e := digest.Parse(ref)
		if e != nil {
			return false, e
		}
		if _, e = repo.Blobs(ctx).Stat(ctx, d); e == nil {
			return true, nil
		} else if !errors.Is(e, distribution.ErrBlobUnknown) {
			return false, e
		}
		// Native references protect missing layers from being masked by Hub.
		used, e := nativeReference(ctx, repo, d)
		if e != nil {
			return false, e
		}
		if used {
			return false, errors.New("自有镜像引用内容缺失")
		}
		return false, nil
	}
	d, err := digest.Parse(ref)
	byTag := err != nil
	if byTag {
		desc, e := repo.Tags(ctx).Get(ctx, ref)
		if e != nil {
			var unknown distribution.ErrTagUnknown
			var noRepo distribution.ErrRepositoryUnknown
			if errors.As(e, &unknown) || errors.As(e, &noRepo) {
				return false, nil
			}
			return false, e
		}
		d = desc.Digest
	}
	ms, err := repo.Manifests(ctx)
	if err != nil {
		return false, err
	}
	_, err = ms.Get(ctx, d)
	if err == nil {
		return true, nil
	}
	var missing distribution.ErrManifestUnknownRevision
	if !byTag && errors.As(err, &missing) {
		used, e := nativeReference(ctx, repo, d)
		if e != nil {
			return false, e
		}
		if used {
			return false, errors.New("自有清单损坏")
		}
		return false, nil
	}
	return false, err
}

// Inspect tagged roots as well as untagged manifests; the upstream enumerator
// intentionally skips broken revision links, so tags must be checked separately.
func nativeReference(ctx context.Context, repo distribution.Repository, target digest.Digest) (bool, error) {
	ms, err := repo.Manifests(ctx)
	if err != nil {
		return false, err
	}
	seen := map[digest.Digest]bool{}
	found := false
	visit := func(d digest.Digest) error {
		if seen[d] {
			return nil
		}
		seen[d] = true
		if len(seen) > 10000 {
			return errors.New("自有引用检查超过预算")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		m, e := ms.Get(ctx, d)
		if e != nil {
			return e
		}
		for _, desc := range m.References() {
			if desc.Digest == target {
				found = true
			}
		}
		return nil
	}
	tags, err := repo.Tags(ctx).All(ctx)
	if err != nil {
		var unknown distribution.ErrRepositoryUnknown
		if !errors.As(err, &unknown) {
			return false, err
		}
	}
	for _, tag := range tags {
		desc, e := repo.Tags(ctx).Get(ctx, tag)
		if e != nil {
			return false, e
		}
		if desc.Digest == target {
			return true, nil
		}
		if e = visit(desc.Digest); e != nil {
			return false, e
		}
	}
	en, ok := ms.(distribution.ManifestEnumerator)
	if !ok {
		return false, errors.New("无法检查自有引用")
	}
	if err = en.Enumerate(ctx, visit); err != nil {
		return false, err
	}
	return found, nil
}

// HubCache is retained across enable/disable and policy changes; native App is
// never recreated. All publication is protected against concurrent reads/close.
func (reg *Registry) HubCache() *hubproxy.Cache {
	reg.hubMu.RLock()
	defer reg.hubMu.RUnlock()
	return reg.hub
}

// ConfigureHub prepares disabled storage first, commits durable settings, then
// publishes policy. A failed commit leaves the running configuration unchanged.
func (reg *Registry) ConfigureHub(cfg hubproxy.Config, commit func() error) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	reg.hubMu.Lock()
	defer reg.hubMu.Unlock()
	if reg.closed {
		return errors.New("实例正在关闭")
	}
	if reg.hub == nil && cfg.Enabled {
		prepared := cfg
		prepared.Enabled = false
		cache, err := hubproxy.New(reg.hubContext, reg.Store.Dir, prepared)
		if err != nil {
			return err
		}
		reg.hub = cache
	}
	if commit != nil {
		if err := commit(); err != nil {
			return err
		}
	}
	if reg.hub != nil {
		return reg.hub.Configure(cfg)
	}
	return nil
}
