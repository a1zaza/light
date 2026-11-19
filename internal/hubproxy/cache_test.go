package hubproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	"light/internal/jsonfile"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type hubFixture struct {
	t                      *testing.T
	c                      *Cache
	dir                    string
	data                   []byte
	dgst                   digest.Digest
	raw                    []byte
	manifests, gets, heads atomic.Int32
	fail                   atomic.Int32
	bad                    atomic.Bool
	wait                   chan struct{}
}

func fixture(t *testing.T) *hubFixture {
	t.Helper()
	f := &hubFixture{t: t, dir: t.TempDir(), data: []byte("verified-light-cache-test")}
	f.dgst = digest.FromBytes(f.data)
	f.raw = []byte(fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":%q,"size":%d},"layers":[]}`, f.dgst, len(f.data)))
	f.open()
	t.Cleanup(func() { f.c.Close() })
	return f
}
func (f *hubFixture) open() {
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.MaxBytes = 1 << 20
	cfg.MaxBlobBytes = 1 << 20
	var err error
	f.c, err = New(context.Background(), f.dir, cfg)
	if err != nil {
		f.t.Fatal(err)
	}
	f.c.client = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Cookie") != "" || strings.Contains(r.Header.Get("Authorization"), "light_") {
			f.t.Error("本地凭据泄露")
		}
		status, body, mt := 200, []byte(`{"token":"hub-token"}`), "application/json"
		if r.URL.Host == authHost {
			if r.URL.Query().Get("scope") != "repository:library/alpine:pull" {
				f.t.Errorf("scope %s", r.URL.RawQuery)
			}
		} else {
			if r.Header.Get("Authorization") != "Bearer hub-token" {
				f.t.Error("缺少上游认证")
			}
			if strings.Contains(r.URL.Path, "/manifests/") {
				f.manifests.Add(1)
				body = f.raw
				mt = "application/vnd.oci.image.manifest.v1+json"
				if fail := f.fail.Load(); fail != 0 {
					status = int(fail)
				}
			} else if strings.Contains(r.URL.Path, "/blobs/") {
				body = f.data
				mt = "application/octet-stream"
				if r.Method == "HEAD" {
					f.heads.Add(1)
				} else {
					f.gets.Add(1)
					if f.wait != nil {
						select {
						case <-f.wait:
						case <-r.Context().Done():
							return nil, r.Context().Err()
						}
					}
				}
				if f.bad.Load() {
					body = bytes.Repeat([]byte("x"), len(f.data))
				}
			} else {
				f.t.Errorf("unexpected path %s", r.URL)
			}
		}
		h := http.Header{"Content-Type": []string{mt}, "Retry-After": []string{"12"}}
		return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body)), Request: r}, nil
	})}
}
func (f *hubFixture) req(method, kind, ref string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://light.test/v2/alpine/"+kind+"/"+ref, nil)
	w := httptest.NewRecorder()
	f.c.Serve(w, r, "alpine", kind, ref)
	return w
}
func requireStatus(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status=%d want=%d body=%s", w.Code, status, w.Body)
	}
}
func TestManifestAlwaysRefreshAndNoStaleFallback(t *testing.T) {
	f := fixture(t)
	for _, method := range []string{"GET", "HEAD", "GET"} {
		w := f.req(method, "manifests", "latest")
		requireStatus(t, w, 200)
		if method == "HEAD" && w.Body.Len() != 0 {
			t.Fatal("HEAD has body")
		}
	}
	if f.manifests.Load() != 3 {
		t.Fatal("清单没有每次回源")
	}
	f.fail.Store(429)
	w := f.req("GET", "manifests", "latest")
	requireStatus(t, w, 429)
	if w.Header().Get("Retry-After") != "12" {
		t.Fatal("丢失限流重试头")
	}
	f.fail.Store(500)
	requireStatus(t, f.req("GET", "manifests", "latest"), 502)
	f.fail.Store(404)
	requireStatus(t, f.req("GET", "manifests", "latest"), 404)
	f.fail.Store(0)
	requireStatus(t, f.req("GET", "manifests", digest.FromString("wrong").String()), 502)
}
func TestBlobFixedTTLRangeRestartAndCleanup(t *testing.T) {
	f := fixture(t)
	requireStatus(t, f.req("GET", "manifests", "latest"), 200)
	requireStatus(t, f.req("HEAD", "blobs", f.dgst.String()), 200)
	if f.gets.Load() != 0 || f.c.Status().Blobs != 0 {
		t.Fatal("HEAD 填充了缓存")
	}
	requireStatus(t, f.req("GET", "blobs", f.dgst.String()), 200)
	first := f.c.state.Blobs[f.dgst].CachedAt
	requireStatus(t, f.req("GET", "manifests", "latest"), 200)
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Range", "bytes=2-6")
	w := httptest.NewRecorder()
	f.c.Serve(w, r, "alpine", "blobs", f.dgst.String())
	requireStatus(t, w, 206)
	if w.Body.String() != string(f.data[2:7]) {
		t.Fatal("Range 错误")
	}
	if f.gets.Load() != 1 || !f.c.state.Blobs[f.dgst].CachedAt.Equal(first) {
		t.Fatal("命中续期或重复下载")
	}
	f.c.Close()
	f.open()
	if f.c.Status().Error != "" {
		t.Fatal(f.c.Status().Error)
	}
	requireStatus(t, f.req("GET", "blobs", f.dgst.String()), 200)
	if f.gets.Load() != 1 {
		t.Fatal("重启缓存未命中")
	}
	// Move only the blob's commit time; keep the fresh manifest association.
	f.c.mu.Lock()
	b := f.c.state.Blobs[f.dgst]
	b.CachedAt = time.Now().Add(-TTL)
	f.c.state.Blobs[f.dgst] = b
	f.c.mu.Unlock()
	requireStatus(t, f.req("GET", "blobs", f.dgst.String()), 200)
	if f.gets.Load() != 2 {
		t.Fatal("过期未重填")
	}
	// Cleanup never touches native data.
	native := filepath.Join(f.dir, "registry", "sentinel")
	if err := os.MkdirAll(filepath.Dir(native), 0700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(native, []byte("native"), 0600)
	if err := f.c.Clean(true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(native); err != nil {
		t.Fatal("清理影响自有数据")
	}
	if f.c.Status().Blobs != 0 {
		t.Fatal("清理失败")
	}
}
func TestBlobConcurrentFillAndBadContent(t *testing.T) {
	f := fixture(t)
	requireStatus(t, f.req("GET", "manifests", "latest"), 200)
	f.bad.Store(true)
	requireStatus(t, f.req("GET", "blobs", f.dgst.String()), 502)
	if f.c.Status().Blobs != 0 {
		t.Fatal("坏内容被发布")
	}
	f.bad.Store(false)
	before := f.gets.Load()
	f.wait = make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := f.req("GET", "blobs", f.dgst.String())
			if w.Code != 200 {
				t.Errorf("并发失败 %d %s", w.Code, w.Body)
			}
		}()
	}
	deadline := time.After(time.Second)
	for f.gets.Load() == before {
		select {
		case <-deadline:
			t.Fatal("回源未开始")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	close(f.wait)
	wg.Wait()
	if f.gets.Load() != before+1 {
		t.Fatalf("并发重复下载 %d", f.gets.Load()-before)
	}
}
func TestColdRangeUnauthorizedDigestAndCapacity(t *testing.T) {
	f := fixture(t)
	requireStatus(t, f.req("GET", "blobs", f.dgst.String()), 404)
	requireStatus(t, f.req("GET", "manifests", "latest"), 200)
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Range", "bytes=1-3")
	w := httptest.NewRecorder()
	f.c.Serve(w, r, "alpine", "blobs", f.dgst.String())
	requireStatus(t, w, 206)
	if w.Body.String() != string(f.data[1:4]) {
		t.Fatal("冷 Range 错误")
	}
	if err := f.c.Clean(true); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, f.req("GET", "manifests", "latest"), 200)
	cfg := f.c.Configuration()
	cfg.MaxBytes = 1
	if err := f.c.Configure(cfg); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, f.req("GET", "blobs", f.dgst.String()), 507)
}
func TestCorruptMetadataPausesOnlyCache(t *testing.T) {
	f := fixture(t)
	f.c.Close()
	if err := os.WriteFile(f.c.path(), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	f.open()
	requireStatus(t, f.req("GET", "manifests", "latest"), 503)
	if f.c.Status().Error == "" {
		t.Fatal("未报告损坏")
	}
}
func TestPolicyAndConfig(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.64.1.1", "::1", "::ffff:127.0.0.1", "2001:db8::1"} {
		if publicIP(netip.MustParseAddr(s)) {
			t.Errorf("unsafe IP %s", s)
		}
	}
	for _, s := range []string{"alpine", "library/alpine", "a/b"} {
		if _, err := Normalize(s); err != nil {
			t.Error(err)
		}
	}
	for _, s := range []string{"evil.example/alpine", "localhost:5000/a", "../a"} {
		if _, err := Normalize(s); err == nil {
			t.Errorf("bad name %s", s)
		}
	}
	f := fixture(t)
	for i := 0; i < 60; i++ {
		if !f.c.Admit("robot", "ip") {
			t.Fatal("过早限流")
		}
	}
	if f.c.Admit("robot", "other-ip") || f.c.Admit("other-robot", "ip") {
		t.Fatal("限流无效")
	}
	cfg := DefaultConfig()
	cfg.Concurrency = 0
	if err := cfg.Validate(); err == nil {
		t.Fatal("配置未校验")
	}
}
func TestMultiarchAndTagUpdate(t *testing.T) {
	f := fixture(t)
	original := append([]byte(nil), f.raw...)
	requireStatus(t, f.req("GET", "manifests", "latest"), 200)
	child := digest.FromBytes(original)
	f.raw = []byte(fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"digest":%q,"size":%d,"mediaType":"application/vnd.oci.image.manifest.v1+json","platform":{"os":"linux","architecture":"amd64"}}]}`, child, len(original)))
	// Match fixture response type for this request by wrapping the transport.
	tr := f.c.client.Transport
	f.c.client.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		resp, e := tr.RoundTrip(r)
		if e == nil && strings.Contains(r.URL.Path, "/manifests/") {
			resp.Header.Set("Content-Type", "application/vnd.oci.image.index.v1+json")
		}
		return resp, e
	})
	w := f.req("GET", "manifests", "latest")
	requireStatus(t, w, 200)
	if !bytes.Equal(w.Body.Bytes(), f.raw) {
		t.Fatal("索引被改写")
	}
	if f.gets.Load() != 0 {
		t.Fatal("预下载了平台层")
	}
	if _, ok := f.c.state.Manifests["library/alpine@"+child.String()]; !ok {
		t.Fatal("tag 变更丢失旧摘要快照")
	}
	// Metadata uses base64 bytes to preserve exact original manifest bytes across JSON indentation.
	var saved state
	if err := jsonfile.Read(f.c.path(), &saved); err != nil {
		t.Fatal(err)
	}
	for _, m := range saved.Manifests {
		if digest.FromBytes(m.Raw) != m.Digest {
			t.Fatal("持久化改变清单字节")
		}
	}
	raw, _ := json.Marshal(saved)
	if len(raw) == 0 {
		t.Fatal("empty")
	}
}

func TestCleanupWaitsForFillAndShutdownCancels(t *testing.T) {
	f := fixture(t)
	requireStatus(t, f.req("GET", "manifests", "latest"), 200)
	f.wait = make(chan struct{})
	done := make(chan int, 1)
	go func() { done <- f.req("GET", "blobs", f.dgst.String()).Code }()
	deadline := time.After(time.Second)
	for f.gets.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("下载未开始")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cleaned := make(chan error, 1)
	if err := f.c.StartClean(true, func(err error) { cleaned <- err }); err != nil {
		t.Fatal(err)
	}
	if !f.c.Status().Cleaning {
		t.Fatal("缺少异步状态")
	}
	select {
	case <-cleaned:
		t.Fatal("在途填充未结束就清理")
	default:
	}
	close(f.wait)
	if status := <-done; status != 200 {
		t.Fatal(status)
	}
	if err := <-cleaned; err != nil {
		t.Fatal(err)
	}
	if f.c.Status().Blobs != 0 {
		t.Fatal("清理未排空")
	}
	// A blocked upstream request must terminate when the cache is closed.
	f.wait = make(chan struct{})
	requireStatus(t, f.req("GET", "manifests", "latest"), 200)
	before := f.gets.Load()
	go func() { done <- f.req("GET", "blobs", f.dgst.String()).Code }()
	deadline = time.After(time.Second)
	for f.gets.Load() == before {
		select {
		case <-deadline:
			t.Fatal("下载未开始")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	closed := make(chan struct{})
	go func() { f.c.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("退出未取消填充")
	}
	if status := <-done; status != 502 {
		t.Fatal(status)
	}
}
func TestExpiredCleanupAndEmptyBlob(t *testing.T) {
	f := fixture(t)
	f.data = []byte{}
	f.dgst = digest.FromBytes(f.data)
	f.raw = []byte(fmt.Sprintf(`{"schemaVersion":2,"config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":%q,"size":0},"layers":[]}`, f.dgst))
	requireStatus(t, f.req("GET", "manifests", "latest"), 200)
	requireStatus(t, f.req("GET", "blobs", f.dgst.String()), 200)
	if err := f.c.Clean(false); err != nil {
		t.Fatal(err)
	}
	if f.c.Status().Blobs != 1 {
		t.Fatal("提前删除")
	}
	f.c.mu.Lock()
	b := f.c.state.Blobs[f.dgst]
	b.CachedAt = time.Now().Add(-TTL)
	f.c.state.Blobs[f.dgst] = b
	f.c.mu.Unlock()
	if err := f.c.Clean(false); err != nil {
		t.Fatal(err)
	}
	if f.c.Status().Blobs != 0 {
		t.Fatal("过期未删除")
	}
}

func TestLiveConfigurationPreservesInFlightFillAndCache(t *testing.T) {
	f := fixture(t)
	requireStatus(t, f.req("GET", "manifests", "latest"), 200)
	f.wait = make(chan struct{})
	done := make(chan int, 1)
	go func() { done <- f.req("GET", "blobs", f.dgst.String()).Code }()
	deadline := time.After(time.Second)
	for f.gets.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("fill did not start")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cfg := f.c.Configuration()
	cfg.Concurrency = 1
	cfg.MaxBytes = 1
	cfg.MaxBlobBytes = 1
	cfg.Timeout = time.Second
	updated := make(chan error, 1)
	go func() { updated <- f.c.Configure(cfg) }()
	select {
	case err := <-updated:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("配置更新等待了在途传输")
	}
	// The already reserved fill occupies the new limit; a manifest request cannot
	// start a second fetch. The original request keeps its original time budget.
	requireStatus(t, f.req("GET", "manifests", "latest"), 429)
	select {
	case status := <-done:
		t.Fatalf("在途请求被提前取消 %d", status)
	case <-time.After(1100 * time.Millisecond):
	}
	cfg.Enabled = false
	if err := f.c.Configure(cfg); err != nil {
		t.Fatal(err)
	}
	before := f.manifests.Load()
	requireStatus(t, f.req("GET", "manifests", "latest"), 503)
	if f.manifests.Load() != before {
		t.Fatal("关闭后仍回源")
	}
	close(f.wait)
	if status := <-done; status != 200 {
		t.Fatal(status)
	}
	if f.c.Status().Blobs != 1 {
		t.Fatal("调整限制清除了未过期缓存")
	}
	cfg.Enabled = true
	if err := f.c.Configure(cfg); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, f.req("GET", "blobs", f.dgst.String()), 200)
	if f.gets.Load() != 1 {
		t.Fatal("重新启用未复用缓存")
	}
	if err := f.c.Clean(true); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, f.req("GET", "manifests", "latest"), 200)
	requireStatus(t, f.req("GET", "blobs", f.dgst.String()), 413)
	cfg.MaxBlobBytes = 1 << 20
	if err := f.c.Configure(cfg); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, f.req("GET", "blobs", f.dgst.String()), 507)
	cfg.MaxBytes = 1 << 20
	if err := f.c.Configure(cfg); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, f.req("GET", "blobs", f.dgst.String()), 200)
}

func TestBlobUsageExcludesSnapshotsAndSupportsRetainedCache(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	used, err := f.c.BlobBytes(ctx)
	if err != nil || used != 0 {
		t.Fatal("空缓存元数据被计入镜像占用", err)
	}
	requireStatus(t, f.req("GET", "manifests", "latest"), 200)
	used, err = f.c.BlobBytes(ctx)
	if err != nil || used != 0 {
		t.Fatal("清单快照被计入 blob 大小", err)
	}
	requireStatus(t, f.req("GET", "blobs", f.dgst.String()), 200)
	requireStatus(t, f.req("GET", "manifests", "alias"), 200)
	used, err = f.c.BlobBytes(ctx)
	if err != nil || used != uint64(len(f.data)) {
		t.Fatalf("缓存内容大小=%d，错误=%v", used, err)
	}
	f.c.Close()
	used, err = StoredBlobBytes(ctx, f.dir)
	if err != nil || used != uint64(len(f.data)) {
		t.Fatalf("未启用代理时未统计保留缓存：%d，错误=%v", used, err)
	}
}
