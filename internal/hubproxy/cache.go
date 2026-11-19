package hubproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	distribution "github.com/distribution/distribution/v3"
	"github.com/distribution/distribution/v3/registry/storage"
	driver "github.com/distribution/distribution/v3/registry/storage/driver"
	"github.com/distribution/distribution/v3/registry/storage/driver/filesystem"
	"github.com/distribution/reference"
	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"golang.org/x/sys/unix"
	"light/internal/jsonfile"
	"light/internal/storageutil"
)

type manifest struct {
	Name      string        `json:"name"`
	Ref       string        `json:"ref"`
	Digest    digest.Digest `json:"digest"`
	MediaType string        `json:"mediaType"`
	Raw       []byte        `json:"raw"`
	CheckedAt time.Time     `json:"checkedAt"`
}
type blob struct {
	Size     int64     `json:"size"`
	CachedAt time.Time `json:"cachedAt"`
}
type state struct {
	Version   int                    `json:"version"`
	Manifests map[string]manifest    `json:"manifests"`
	Blobs     map[digest.Digest]blob `json:"blobs"`
}
type bucket struct {
	At    time.Time
	Count int
}
type Cache struct {
	cfg           Config
	dir           string
	client        *http.Client
	driver        driver.StorageDriver
	repo          distribution.Repository
	namespace     distribution.Namespace
	ctx           context.Context
	cancel        context.CancelFunc
	wg            sync.WaitGroup
	closeOnce     sync.Once
	gate          sync.RWMutex // Cleanup waits for all active requests before removing objects.
	mu            sync.Mutex
	state         state
	failure       string
	reserved      int64
	downloading   int
	locks         map[digest.Digest]chan struct{}
	rates         map[string]bucket
	admissions    chan struct{}
	activeFetches int
	now           func() time.Time
	lastClean     time.Time
	lastError     string
	cleaning      bool
	stopping      bool
}

func New(parent context.Context, dir string, cfg Config) (*Cache, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	root := filepath.Join(dir, "hub-cache")
	c := &Cache{cfg: cfg, dir: root, client: safeClient(), ctx: ctx, cancel: cancel, locks: map[digest.Digest]chan struct{}{}, rates: map[string]bucket{}, admissions: make(chan struct{}, 64), now: time.Now}
	c.state = state{Version: 1, Manifests: map[string]manifest{}, Blobs: map[digest.Digest]blob{}}
	fs, err := filesystem.FromParameters(map[string]any{"rootdirectory": filepath.Join(root, "registry")})
	if err != nil {
		cancel()
		return nil, err
	}
	c.driver = storageutil.Driver{StorageDriver: fs}
	ns, err := storage.NewRegistry(ctx, c.driver, storage.EnableDelete)
	if err != nil {
		cancel()
		return nil, err
	}
	c.namespace = ns
	named, _ := reference.WithName("cache/blobs")
	c.repo, err = ns.Repository(ctx, named)
	if err != nil {
		cancel()
		return nil, err
	}
	if err = jsonfile.Read(c.path(), &c.state); err != nil && !errors.Is(err, os.ErrNotExist) {
		c.failure = "缓存元数据损坏，代理已暂停"
	}
	if c.failure == "" {
		if err = c.validateState(); err != nil {
			c.failure = err.Error()
		}
	}
	// A failed cache never prevents native registry access.
	if c.failure == "" {
		if err = c.reconcile(ctx, ns); err != nil {
			c.failure = "缓存恢复失败：" + err.Error()
		}
	}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = c.Clean(false)
			}
		}
	}()
	return c, nil
}
func (c *Cache) path() string { return filepath.Join(c.dir, "metadata", "state.json") }
func (c *Cache) validateState() error {
	if c.state.Version != 1 || c.state.Manifests == nil || c.state.Blobs == nil || len(c.state.Manifests) > maxEntries || len(c.state.Blobs) > maxBlobs {
		return errors.New("缓存元数据结构无效")
	}
	for d, b := range c.state.Blobs {
		if validDigest(d.String()) != nil || b.Size < 0 || b.Size > 20<<30 || b.CachedAt.IsZero() || b.CachedAt.After(c.now().Add(time.Minute)) {
			return errors.New("缓存 blob 记录无效")
		}
	}
	for key, m := range c.state.Manifests {
		normalized, nameErr := Normalize(m.Name)
		if key != m.Name+"@"+m.Ref || nameErr != nil || normalized != m.Name || validDigest(m.Digest.String()) != nil || m.Digest != digest.FromBytes(m.Raw) || !validType(m.MediaType) || m.CheckedAt.IsZero() || m.CheckedAt.After(c.now().Add(time.Minute)) {
			return errors.New("缓存清单记录无效")
		}
		if _, err := descriptors(m.Raw); err != nil {
			return err
		}
	}
	return nil
}
func (c *Cache) reconcile(ctx context.Context, ns distribution.Namespace) error {
	// Remove interrupted uploads and uncommitted global blobs using public storage APIs.
	if _, errs := storage.PurgeUploads(ctx, c.driver, c.now(), true); len(errs) > 0 {
		for _, e := range errs {
			var missing driver.PathNotFoundError
			if !errors.As(e, &missing) {
				return e
			}
		}
	}
	var orphan []digest.Digest
	if err := ns.Blobs().Enumerate(ctx, func(d digest.Digest) error {
		if _, ok := c.state.Blobs[d]; !ok {
			orphan = append(orphan, d)
		}
		return nil
	}); err != nil {
		var missing driver.PathNotFoundError
		if !errors.As(err, &missing) {
			return err
		}
	}
	vacuum := storage.NewVacuum(ctx, c.driver)
	for _, d := range orphan {
		if err := c.removeBlob(ctx, vacuum, d); err != nil {
			return err
		}
	}
	for d, b := range c.state.Blobs {
		desc, err := c.repo.Blobs(ctx).Stat(ctx, d)
		if err != nil {
			if !errors.Is(err, distribution.ErrBlobUnknown) {
				return err
			}
			delete(c.state.Blobs, d)
		} else if desc.Size != b.Size {
			return errors.New("缓存 blob 大小与记录不一致")
		}
	}
	return c.saveLocked()
}
func (c *Cache) Close() {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.stopping = true
		c.cancel()
		c.mu.Unlock()
		c.wg.Wait()
		c.gate.Lock()
		defer c.gate.Unlock()
		if tr, ok := c.client.Transport.(interface{ CloseIdleConnections() }); ok {
			tr.CloseIdleConnections()
		}
	})
}
func (c *Cache) saveLocked() error {
	if err := jsonfile.Write(c.path(), c.state); err != nil {
		c.failure = "缓存记录无法持久化，代理已暂停"
		return err
	}
	return nil
}
func validDigest(s string) error {
	d, err := digest.Parse(s)
	if err != nil || d.Algorithm() != digest.SHA256 {
		return errors.New("仅支持 SHA256 内容摘要")
	}
	return nil
}
func validType(s string) bool {
	for _, mt := range strings.Split(accept, ", ") {
		if mt == s {
			return true
		}
	}
	return false
}
func Normalize(name string) (string, error) {
	n, err := reference.WithName(name)
	if err != nil || n.Name() != name || strings.ContainsAny(strings.Split(name, "/")[0], ".:") {
		return "", errors.New("Docker Hub 仓库名称无效")
	}
	if !strings.Contains(name, "/") {
		name = "library/" + name
	}
	return name, nil
}
func descriptors(raw []byte) ([]v1.Descriptor, error) {
	var doc struct {
		SchemaVersion int             `json:"schemaVersion"`
		Manifests     []v1.Descriptor `json:"manifests"`
		Config        v1.Descriptor   `json:"config"`
		Layers        []v1.Descriptor `json:"layers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || doc.SchemaVersion != 2 {
		return nil, errors.New("上游清单结构无效")
	}
	out := append(doc.Manifests, doc.Layers...)
	if doc.Config.Digest != "" {
		out = append(out, doc.Config)
	}
	if len(out) > 4096 {
		return nil, errors.New("上游清单描述符过多")
	}
	for _, d := range out {
		if validDigest(d.Digest.String()) != nil || d.Size < 0 || len(d.URLs) > 0 {
			return nil, errors.New("上游清单描述符无效或包含外部层 URL")
		}
	}
	return out, nil
}
func (c *Cache) allowedBlob(name string, d digest.Digest) (int64, bool) {
	for _, m := range c.state.Manifests {
		if m.Name != name || !c.now().Before(m.CheckedAt.Add(TTL)) {
			continue
		}
		ds, _ := descriptors(m.Raw)
		for _, desc := range ds {
			if desc.Digest == d && !validType(desc.MediaType) {
				return desc.Size, true
			}
		}
	}
	return 0, false
}
func (c *Cache) Admit(identity, ip string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	for k, b := range c.rates {
		if now.Sub(b.At) >= time.Minute {
			delete(c.rates, k)
		}
	}
	keys := []string{"robot:" + identity, "ip:" + ip}
	for _, k := range keys {
		b, ok := c.rates[k]
		if (ok && b.Count >= 60) || (!ok && len(c.rates) >= 4096) {
			return false
		}
	}
	for _, k := range keys {
		b := c.rates[k]
		if b.At.IsZero() {
			b.At = now
		}
		b.Count++
		c.rates[k] = b
	}
	return true
}
func (c *Cache) Serve(w http.ResponseWriter, r *http.Request, name, kind, ref string) {
	select {
	case c.admissions <- struct{}{}:
		defer func() { <-c.admissions }()
	default:
		writeError(w, &Error{429, "TOOMANYREQUESTS", "代理请求并发已满", "5"})
		return
	}
	cfg := c.Configuration()
	if !cfg.Enabled {
		writeError(w, &Error{503, "UNAVAILABLE", "Docker Hub 代理已关闭", ""})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), cfg.Timeout)
	defer cancel()
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()
	r = r.WithContext(ctx)
	// Free expired space before admitting a new fill, rather than rejecting it
	// until the next periodic sweep. Native storage is never involved.
	c.mu.Lock()
	expired := false
	for _, b := range c.state.Blobs {
		if !c.now().Before(b.CachedAt.Add(TTL)) {
			expired = true
			break
		}
	}
	c.mu.Unlock()
	if expired {
		if err := c.Clean(false); err != nil {
			writeError(w, unavailable("过期代理缓存清理失败"))
			return
		}
	}
	c.gate.RLock()
	defer c.gate.RUnlock()
	c.mu.Lock()
	failure := c.failure
	c.mu.Unlock()
	if failure != "" {
		writeError(w, &Error{503, "UNAVAILABLE", failure, ""})
		return
	}
	if c.ctx.Err() != nil {
		writeError(w, unavailable("代理正在关闭"))
		return
	}
	normalized, err := Normalize(name)
	if err != nil {
		writeError(w, &Error{400, "NAME_INVALID", err.Error(), ""})
		return
	}
	switch kind {
	case "manifests":
		err = c.serveManifest(w, r, normalized, ref)
	case "blobs":
		err = c.serveBlob(w, r, normalized, ref)
	default:
		err = &Error{404, "NAME_UNKNOWN", "代理不支持此接口", ""}
	}
	if err != nil {
		writeError(w, err)
	}
}
func writeError(w http.ResponseWriter, err error) {
	e := &Error{502, "UNAVAILABLE", "代理缓存失败", ""}
	var known *Error
	if errors.As(err, &known) {
		e = known
	}
	w.Header().Set("Content-Type", "application/json")
	if e.RetryAfter != "" {
		w.Header().Set("Retry-After", e.RetryAfter)
	}
	w.WriteHeader(e.Status)
	_ = json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]string{{"code": e.Code, "message": e.Message}}})
}
func (c *Cache) serveManifest(w http.ResponseWriter, r *http.Request, name, ref string) error {
	if strings.Contains(ref, ":") {
		if err := validDigest(ref); err != nil {
			return &Error{400, "DIGEST_INVALID", err.Error(), ""}
		}
	} else {
		if _, err := reference.WithTag(mustName(name), ref); err != nil {
			return &Error{400, "TAG_INVALID", "标签无效", ""}
		}
	}
	if !c.acquireFetch() {
		return &Error{429, "TOOMANYREQUESTS", "回源并发已满", "5"}
	}
	defer c.releaseFetch()
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	resp, err := c.request(ctx, name, "manifests", ref, "GET")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxManifest+1))
	if err != nil || len(raw) > maxManifest {
		return unavailable("Docker Hub 清单读取失败或超出大小上限")
	}
	mt := strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
	if !validType(mt) {
		return unavailable("Docker Hub 清单类型不受支持")
	}
	var declared struct {
		MediaType string `json:"mediaType"`
		Config    struct {
			Digest string `json:"digest"`
		} `json:"config"`
		Manifests json.RawMessage `json:"manifests"`
	}
	if json.Unmarshal(raw, &declared) != nil || (declared.MediaType != "" && declared.MediaType != mt) {
		return unavailable("Docker Hub 清单媒体类型不一致")
	}
	index := mt == "application/vnd.oci.image.index.v1+json" || mt == "application/vnd.docker.distribution.manifest.list.v2+json"
	if (index && (len(declared.Manifests) == 0 || string(declared.Manifests) == "null" || declared.Config.Digest != "")) || (!index && (declared.Config.Digest == "" || len(declared.Manifests) > 0)) {
		return unavailable("Docker Hub 清单结构与媒体类型不一致")
	}
	d := digest.FromBytes(raw)
	if (strings.Contains(ref, ":") && d.String() != ref) || (resp.Header.Get("Docker-Content-Digest") != "" && resp.Header.Get("Docker-Content-Digest") != d.String()) {
		return unavailable("Docker Hub 清单摘要不匹配")
	}
	if _, err = descriptors(raw); err != nil {
		return unavailable(err.Error())
	}
	m := manifest{name, ref, d, mt, raw, c.now().UTC()}
	c.mu.Lock()
	key := name + "@" + ref
	// Retain the old digest snapshot for in-flight pulls after a tag changes.
	for k, v := range c.state.Manifests {
		if !c.now().Before(v.CheckedAt.Add(TTL)) {
			delete(c.state.Manifests, k)
		}
	}
	digestKey := name + "@" + d.String()
	oldTag, hadTag := c.state.Manifests[key]
	oldDigest, hadDigest := c.state.Manifests[digestKey]
	additional := 0
	if !hadTag {
		additional++
	}
	if !hadDigest && key != digestKey {
		additional++
	}
	if len(c.state.Manifests)+additional > maxEntries {
		c.mu.Unlock()
		return &Error{507, "UNAVAILABLE", "缓存清单数量已达上限，请清理缓存", ""}
	}
	c.state.Manifests[key] = m
	byDigest := m
	byDigest.Ref = d.String()
	c.state.Manifests[digestKey] = byDigest
	// Enforce the snapshot byte budget before publishing or attempting a write.
	// Roll back in-memory changes on capacity exhaustion; disk errors pause cache.
	encoded, encodeErr := json.MarshalIndent(c.state, "", "  ")
	if encodeErr != nil || len(encoded)+1+(maxBlobs-len(c.state.Blobs))*256 > jsonfile.MaxBytes {
		delete(c.state.Manifests, key)
		delete(c.state.Manifests, digestKey)
		if hadTag {
			c.state.Manifests[key] = oldTag
		}
		if hadDigest {
			c.state.Manifests[digestKey] = oldDigest
		}
		c.mu.Unlock()
		return &Error{507, "UNAVAILABLE", "代理元数据容量不足，请清理代理缓存", ""}
	}
	err = c.saveLocked()
	c.mu.Unlock()
	if err != nil {
		return unavailable("缓存清单持久化失败")
	}
	w.Header().Set("Content-Type", mt)
	w.Header().Set("Content-Length", fmt.Sprint(len(raw)))
	w.Header().Set("Docker-Content-Digest", d.String())
	w.Header().Set("ETag", `"`+d.String()+`"`)
	if r.Method == "GET" {
		_, _ = w.Write(raw)
	} else {
		w.WriteHeader(200)
	}
	return nil
}
func mustName(s string) reference.Named { n, _ := reference.WithName(s); return n }
func (c *Cache) lockBlob(ctx context.Context, d digest.Digest) (func(), error) {
	c.mu.Lock()
	ch := c.locks[d]
	if ch == nil {
		if len(c.locks) >= maxBlobs+64 {
			c.mu.Unlock()
			return nil, &Error{429, "TOOMANYREQUESTS", "缓存任务数量已满", "5"}
		}
		ch = make(chan struct{}, 1)
		c.locks[d] = ch
	}
	c.mu.Unlock()
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (c *Cache) serveBlob(w http.ResponseWriter, r *http.Request, name, ref string) error {
	if err := validDigest(ref); err != nil {
		return &Error{400, "DIGEST_INVALID", err.Error(), ""}
	}
	d := digest.Digest(ref)
	c.mu.Lock()
	expected, ok := c.allowedBlob(name, d)
	c.mu.Unlock()
	if !ok {
		return &Error{404, "BLOB_UNKNOWN", "请先拉取包含此 blob 的清单", ""}
	}
	unlock, err := c.lockBlob(r.Context(), d)
	if err != nil {
		return err
	}
	defer unlock()
	c.mu.Lock()
	b, found := c.state.Blobs[d]
	fresh := found && c.now().Before(b.CachedAt.Add(TTL))
	c.mu.Unlock()
	if fresh {
		desc, e := c.repo.Blobs(r.Context()).Stat(r.Context(), d)
		if e != nil || desc.Size != b.Size {
			return unavailable("缓存内容缺失或损坏，请清理代理缓存")
		}
		if b.Size != expected {
			return unavailable("缓存大小与清单不匹配")
		}
	}
	if !fresh && r.Method == "HEAD" {
		if !c.acquireFetch() {
			return &Error{429, "TOOMANYREQUESTS", "回源并发已满", "5"}
		}
		defer c.releaseFetch()
		resp, e := c.request(r.Context(), name, "blobs", ref, "HEAD")
		if e != nil {
			return e
		}
		defer resp.Body.Close()
		if resp.ContentLength != expected {
			return unavailable("上游 blob 大小与清单不匹配")
		}
		w.Header().Set("Content-Length", fmt.Sprint(expected))
		w.Header().Set("Docker-Content-Digest", ref)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(200)
		return nil
	}
	if !fresh {
		if err = c.fill(r.Context(), name, d, expected, found); err != nil {
			return err
		}
	}
	reader, err := c.repo.Blobs(r.Context()).Open(r.Context(), d)
	if err != nil {
		return unavailable("无法读取代理 blob")
	}
	defer reader.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Docker-Content-Digest", ref)
	w.Header().Set("ETag", `"`+ref+`"`)
	// Do not allow a client's conditional headers to turn strict reads into stale responses.
	clone := r.Clone(r.Context())
	clone.Header = r.Header.Clone()
	clone.Header.Del("If-None-Match")
	clone.Header.Del("If-Modified-Since")
	http.ServeContent(w, clone, "", time.Time{}, contextReadSeeker{ReadSeeker: reader, ctx: r.Context()})
	return nil
}

type contextReadSeeker struct {
	io.ReadSeeker
	ctx context.Context
}

func (r contextReadSeeker) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.ReadSeeker.Read(p)
}
func (c *Cache) fill(ctx context.Context, name string, d digest.Digest, expected int64, old bool) error {
	if !c.acquireFetch() {
		return &Error{429, "TOOMANYREQUESTS", "回源并发已满", "5"}
	}
	defer c.releaseFetch()
	c.mu.Lock()
	if expected > c.cfg.MaxBlobBytes {
		c.mu.Unlock()
		return &Error{413, "SIZE_INVALID", "上游 blob 超过单对象上限", ""}
	}
	var used int64
	for _, b := range c.state.Blobs {
		used += b.Size
	}
	oldSize := c.state.Blobs[d].Size
	if expected > c.cfg.MaxBytes || used-oldSize+c.reserved > c.cfg.MaxBytes-expected || (!old && len(c.state.Blobs) >= maxBlobs) {
		c.mu.Unlock()
		return &Error{507, "UNAVAILABLE", "代理缓存空间或条目预算不足，请清理到期缓存", ""}
	}
	c.reserved += expected
	c.downloading++
	c.mu.Unlock()
	defer func() { c.mu.Lock(); c.reserved -= expected; c.downloading--; c.mu.Unlock() }()
	if old {
		c.mu.Lock()
		delete(c.state.Blobs, d)
		err := c.saveLocked()
		c.mu.Unlock()
		if err != nil {
			return err
		}
		if err = c.removeBlob(ctx, storage.NewVacuum(ctx, c.driver), d); err != nil {
			return err
		}
	}
	if err := c.disk(expected); err != nil {
		return err
	}
	resp, err := c.request(ctx, name, "blobs", d.String(), "GET")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.ContentLength >= 0 && resp.ContentLength != expected {
		return unavailable("Docker Hub blob 长度与清单不匹配")
	}
	writer, err := c.repo.Blobs(ctx).Create(ctx)
	if err != nil {
		return unavailable("无法创建代理缓存上传")
	}
	defer writer.Close()
	defer writer.Cancel(context.WithoutCancel(ctx))
	hashed := d.Algorithm().Digester()
	n, err := io.Copy(io.MultiWriter(writer, hashed.Hash()), io.LimitReader(&diskReader{Reader: resp.Body, cache: c}, expected+1))
	if err != nil || n != expected || hashed.Digest() != d {
		return unavailable("Docker Hub blob 不完整、摘要不符或磁盘空间不足")
	}
	if _, err = writer.Commit(ctx, distribution.Descriptor{Digest: d, Size: expected, MediaType: "application/octet-stream"}); err != nil {
		return unavailable("代理 blob 提交失败")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state.Blobs[d] = blob{expected, c.now().UTC()}
	return c.saveLocked()
}

type diskReader struct {
	io.Reader
	cache   *Cache
	checked int64
}

func (r *diskReader) Read(p []byte) (int, error) {
	if r.checked <= 0 {
		if err := r.cache.disk(0); err != nil {
			return 0, err
		}
		r.checked = 1 << 20
	}
	n, e := r.Reader.Read(p)
	r.checked -= int64(n)
	return n, e
}
func (c *Cache) disk(required int64) error {
	var st unix.Statfs_t
	if err := unix.Statfs(c.dir, &st); err != nil {
		return err
	}
	free := st.Bavail * uint64(st.Bsize)
	reserve := c.Configuration().Reserve
	if free < reserve || uint64(required) > free-reserve {
		return &Error{507, "UNAVAILABLE", "磁盘可用空间低于缓存预留要求", ""}
	}
	return nil
}
func (c *Cache) Clean(all bool) error {
	c.gate.Lock()
	defer c.gate.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failure != "" {
		return errors.New(c.failure)
	}
	var expired []digest.Digest
	for d, b := range c.state.Blobs {
		if all || !c.now().Before(b.CachedAt.Add(TTL)) {
			expired = append(expired, d)
		}
	}
	for _, d := range expired {
		delete(c.state.Blobs, d)
	}
	for k, m := range c.state.Manifests {
		if all || !c.now().Before(m.CheckedAt.Add(TTL)) {
			delete(c.state.Manifests, k)
		}
	}
	if err := c.saveLocked(); err != nil {
		return err
	}
	vacuum := storage.NewVacuum(c.ctx, c.driver)
	for _, d := range expired {
		if err := c.removeBlob(c.ctx, vacuum, d); err != nil {
			c.lastError = "代理缓存文件清理失败"
			c.failure = c.lastError
			return err
		}
	}
	c.locks = map[digest.Digest]chan struct{}{}
	c.lastClean = c.now().UTC()
	c.lastError = ""
	return nil
}

func (c *Cache) removeBlob(ctx context.Context, vacuum storage.Vacuum, d digest.Digest) error {
	if err := c.repo.Blobs(ctx).Delete(ctx, d); err != nil && !errors.Is(err, distribution.ErrBlobUnknown) {
		return err
	}
	return vacuum.RemoveBlob(d.String())
}

// StartClean returns immediately; the worker drains readers before deletion.
func (c *Cache) StartClean(all bool, finished func(error)) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopping || c.cleaning || c.failure != "" {
		return errors.New("缓存正在关闭、清理或已暂停")
	}
	c.cleaning = true
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		err := c.Clean(all)
		c.mu.Lock()
		c.cleaning = false
		if err != nil {
			c.lastError = err.Error()
		}
		c.mu.Unlock()
		finished(err)
	}()
	return nil
}

type Entry struct {
	Name      string        `json:"name"`
	Ref       string        `json:"ref"`
	Digest    digest.Digest `json:"digest"`
	MediaType string        `json:"mediaType"`
	CheckedAt time.Time     `json:"checkedAt"`
	Bytes     int64         `json:"bytes"`
	Complete  bool          `json:"complete"`
	ExpiresAt time.Time     `json:"expiresAt"`
}
type Status struct {
	Cleaning    bool      `json:"cleaning"`
	Enabled     bool      `json:"enabled"`
	Bytes       int64     `json:"bytes"`
	MaxBytes    int64     `json:"maxBytes"`
	Reserved    int64     `json:"reserved"`
	Blobs       int       `json:"blobs"`
	Downloading int       `json:"downloading"`
	Error       string    `json:"error"`
	LastClean   time.Time `json:"lastClean"`
	Entries     []Entry   `json:"entries"`
}

func (c *Cache) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := Status{Cleaning: c.cleaning, Enabled: c.cfg.Enabled, MaxBytes: c.cfg.MaxBytes, Reserved: c.reserved, Blobs: len(c.state.Blobs), Downloading: c.downloading, Error: c.failure, LastClean: c.lastClean, Entries: []Entry{}}
	for _, b := range c.state.Blobs {
		s.Bytes += b.Size
	}

	tagged := map[string]bool{}
	for _, m := range c.state.Manifests {
		if m.Ref != m.Digest.String() {
			tagged[m.Name+"@"+m.Digest.String()] = true
		}
	}
	for _, m := range c.state.Manifests {
		if m.Ref == m.Digest.String() && tagged[m.Name+"@"+m.Ref] {
			continue
		}
		e := Entry{Name: m.Name, Ref: m.Ref, Digest: m.Digest, MediaType: m.MediaType, CheckedAt: m.CheckedAt, Complete: true}
		seen := map[digest.Digest]bool{}
		var visit func(manifest, int)
		visit = func(current manifest, depth int) {
			if depth > 32 {
				e.Complete = false
				return
			}
			ds, _ := descriptors(current.Raw)
			for _, d := range ds {
				if seen[d.Digest] {
					continue
				}
				seen[d.Digest] = true
				if validType(d.MediaType) {
					child, ok := c.state.Manifests[m.Name+"@"+d.Digest.String()]
					if !ok {
						e.Complete = false
						continue
					}
					visit(child, depth+1)
					continue
				}
				b, ok := c.state.Blobs[d.Digest]
				if !ok || !c.now().Before(b.CachedAt.Add(TTL)) {
					e.Complete = false
					continue
				}
				e.Bytes += b.Size
				if e.ExpiresAt.IsZero() || b.CachedAt.Add(TTL).Before(e.ExpiresAt) {
					e.ExpiresAt = b.CachedAt.Add(TTL)
				}
			}
		}
		visit(m, 0)
		s.Entries = append(s.Entries, e)
	}

	sort.Slice(s.Entries, func(i, j int) bool {
		if s.Entries[i].Name == s.Entries[j].Name {
			return s.Entries[i].Ref < s.Entries[j].Ref
		}
		return s.Entries[i].Name < s.Entries[j].Name
	})
	return s
}

func (c *Cache) Configuration() Config { c.mu.Lock(); defer c.mu.Unlock(); return c.cfg }
func (c *Cache) Configure(cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cfg = cfg
	return nil
}
func (c *Cache) acquireFetch() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.activeFetches >= c.cfg.Concurrency {
		return false
	}
	c.activeFetches++
	return true
}
func (c *Cache) releaseFetch() { c.mu.Lock(); c.activeFetches--; c.mu.Unlock() }
