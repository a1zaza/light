// Package registry isolates the Distribution integration and its lifecycle.
package registry

import (
	"context"
	"crypto/sha256"
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
	"sync/atomic"
	"time"

	distribution "github.com/distribution/distribution/v3"
	"github.com/distribution/distribution/v3/configuration"
	"github.com/distribution/distribution/v3/registry/handlers"
	"github.com/distribution/distribution/v3/registry/storage"
	driver "github.com/distribution/distribution/v3/registry/storage/driver"
	"github.com/distribution/distribution/v3/registry/storage/driver/filesystem"
	"github.com/distribution/reference"
	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"golang.org/x/sys/unix"
	"light/internal/jsonfile"
	"light/internal/store"
)

type Registry struct {
	maintaining            atomic.Bool
	UploadProtectionWindow time.Duration
	leaseMu                sync.Mutex
	uploadLeases           uploadLeaseState
	leaseError             error
	catalogMu              sync.Mutex
	catalog                []Summary
	catalogAt              time.Time
	catalogError           string
	MaxUploadBytes         int64
	IdleTimeout            time.Duration

	App       *handlers.App
	Namespace distribution.Namespace
	Driver    driver.StorageDriver
	Auth      *Controller
	Gate      sync.RWMutex
	Store     *store.Store
	uploads   chan struct{}
	Reserve   uint64
}

func New(ctx context.Context, s *store.Store, reserve uint64) (reg *Registry, err error) {
	defer func() {
		if p := recover(); p != nil {
			reg = nil
			err = fmt.Errorf("Registry 初始化失败：%v", p)
		}
	}()
	root := filepath.Join(s.Dir, "registry")
	if err = os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	master := s.Snapshot().Secret
	bearerKey := sha256.Sum256([]byte("light:bearer:v1:" + master))
	c := &Controller{Store: s, key: bearerKey[:]}
	cfg := &configuration.Configuration{Version: configuration.CurrentVersion, Storage: configuration.Storage{
		"filesystem":  configuration.Parameters{"rootdirectory": root},
		"delete":      configuration.Parameters{"enabled": true},
		"maintenance": configuration.Parameters{"uploadpurging": map[any]any{"enabled": false}},
	}, Auth: configuration.Auth{"light": configuration.Parameters{"controller": c}}}
	cfg.HTTP.Secret = store.Hash("light:upload:v1:" + master)
	cfg.HTTP.RelativeURLs = true
	cfg.Validation.Manifests.URLs.Allow = []string{"^$"}
	app := handlers.NewApp(ctx, cfg)
	fs, err := filesystem.FromParameters(map[string]any{"rootdirectory": root})
	if err != nil {
		app.Shutdown()
		return nil, err
	}
	ns, err := storage.NewRegistry(ctx, fs, storage.EnableDelete)
	if err != nil {
		app.Shutdown()
		return nil, err
	}
	reg = &Registry{App: app, Namespace: ns, Driver: fs, Auth: c, Store: s, uploads: make(chan struct{}, 4), Reserve: reserve, MaxUploadBytes: 20 << 30, IdleTimeout: 5 * time.Minute, UploadProtectionWindow: DefaultUploadProtectionWindow}
	if err = reg.initUploadProtection(ctx); err != nil {
		app.Shutdown()
		return nil, err
	}
	return reg, nil
}
func (reg *Registry) Close() error { return reg.App.Shutdown() }
func protocolError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]string{{"code": code, "message": message}}})
}
func (reg *Registry) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if reg.Maintaining() {
		w.Header().Set("Retry-After", "5")
		protocolError(w, 503, "UNAVAILABLE", "存储维护中，请稍后重试")
		return
	}
	if !reg.Store.Ready() {
		protocolError(w, 503, "UNAVAILABLE", "实例尚未初始化或持久化状态待确认")
		return
	}
	// The protocol must not offer direct deletion or a complete catalog to robots/anonymous clients.
	if (r.Method == http.MethodDelete && !IsUploadCancellation(r)) || r.URL.Path == "/v2/_catalog" {
		protocolError(w, 403, "DENIED", "请使用管理员控制台执行此操作")
		return
	}
	writing := r.Method != "GET" && r.Method != "HEAD"
	if writing && reg.MaxUploadBytes > 0 && r.ContentLength > reg.MaxUploadBytes {
		protocolError(w, 413, "SIZE_INVALID", "上传请求超过正文大小上限")
		return
	}
	if writing {
		select {
		case reg.uploads <- struct{}{}:
			defer func() { <-reg.uploads }()
		default:
			protocolError(w, 429, "TOOMANYREQUESTS", "上传并发已满，请稍后重试")
			return
		}
		var st unix.Statfs_t
		if err := unix.Statfs(reg.Store.Dir, &st); !IsUploadCancellation(r) && (err != nil || st.Bavail*uint64(st.Bsize) < reg.Reserve) {
			protocolError(w, 507, "UNAVAILABLE", "可用空间低于预留阈值")
			return
		}
	}
	reg.Gate.RLock()
	defer reg.Gate.RUnlock()
	if reg.Maintaining() {
		w.Header().Set("Retry-After", "5")
		protocolError(w, 503, "UNAVAILABLE", "存储维护中，请稍后重试")
		return
	}
	if d, ok := reg.pushLeaseDigest(r); ok {
		if err := reg.protectUpload(d); err != nil {
			protocolError(w, 503, "UNAVAILABLE", "上传保护记录无法持久化，请稍后重试")
			return
		}
	}
	limitExceeded := false
	// An idle deadline is refreshed on every read, without imposing a total upload budget.
	if writing && r.Body != nil {
		defer http.NewResponseController(w).SetReadDeadline(time.Time{})
		if reg.MaxUploadBytes > 0 {
			r.Body = http.MaxBytesReader(w, r.Body, reg.MaxUploadBytes)
		}
		r.Body = &idleBody{ReadCloser: r.Body, controller: http.NewResponseController(w), timeout: reg.IdleTimeout, limitExceeded: &limitExceeded}
	}
	rec := &responseStatus{ResponseWriter: w, timeout: reg.IdleTimeout, limitExceeded: &limitExceeded}
	defer http.NewResponseController(w).SetWriteDeadline(time.Time{})
	reg.App.ServeHTTP(rec, r)
	if writing && rec.status >= 200 && rec.status < 300 && strings.Contains(r.URL.Path, "/manifests/") {
		reg.Invalidate()
	}
}

type responseStatus struct {
	limitExceeded *bool
	http.ResponseWriter
	status  int
	timeout time.Duration
}

func (w *responseStatus) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *responseStatus) WriteHeader(status int) {
	w.Header().Set("Cache-Control", "no-store")
	if w.limitExceeded != nil && *w.limitExceeded {
		status = 413
	}
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *responseStatus) Write(data []byte) (int, error) {
	w.Header().Set("Cache-Control", "no-store")
	if w.status == 0 {
		w.status = 200
	}
	if w.timeout > 0 {
		_ = http.NewResponseController(w.ResponseWriter).SetWriteDeadline(time.Now().Add(w.timeout))
	}
	return w.ResponseWriter.Write(data)
}

type idleBody struct {
	limitExceeded *bool
	timeout       time.Duration
	io.ReadCloser
	controller *http.ResponseController
}

func (b *idleBody) Read(p []byte) (int, error) {
	_ = b.controller.SetReadDeadline(time.Now().Add(b.timeout))
	n, err := b.ReadCloser.Read(p)
	var max *http.MaxBytesError
	if errors.As(err, &max) && b.limitExceeded != nil {
		*b.limitExceeded = true
	}
	return n, err
}
func (b *idleBody) Close() error {
	_ = b.controller.SetReadDeadline(time.Time{})
	return b.ReadCloser.Close()
}
func (reg *Registry) Repository(ctx context.Context, name string) (distribution.Repository, error) {
	named, err := reference.WithName(name)
	if err != nil {
		return nil, errors.New("仓库名称不合法")
	}
	return reg.Namespace.Repository(ctx, named)
}
func (reg *Registry) Names(ctx context.Context) ([]string, error) {
	result := []string{}
	last := ""
	for {
		batch := make([]string, 100)
		n, err := reg.Namespace.Repositories(ctx, batch, last)
		result = append(result, batch[:n]...)
		if err == io.EOF || n == 0 {
			break
		}
		if err != nil {
			return nil, err
		}
		last = batch[n-1]
	}
	return result, nil
}

type Image struct {
	Tag        string          `json:"tag,omitempty"`
	Digest     string          `json:"digest"`
	MediaType  string          `json:"mediaType"`
	Size       int64           `json:"size"`
	Platforms  []string        `json:"platforms"`
	Created    string          `json:"created,omitempty"`
	References []v1.Descriptor `json:"references"`
	Raw        json.RawMessage `json:"raw,omitempty"`
}
type Summary struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Public      bool      `json:"public"`
	Revision    int64     `json:"revision"`
	Tags        int       `json:"tags"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

func (reg *Registry) scan(ctx context.Context) ([]Summary, error) {
	names, err := reg.Names(ctx)
	if err != nil {
		return nil, err
	}
	state := reg.Store.Snapshot()
	out := []Summary{}
	for _, name := range names {
		meta := state.Repositories[name]

		repo, err := reg.Repository(ctx, name)
		if err != nil {
			return nil, err
		}
		tags, err := repo.Tags(ctx).All(ctx)
		if err != nil {
			var unknown distribution.ErrRepositoryUnknown
			if !errors.As(err, &unknown) {
				return nil, err
			}
		}
		if len(tags) == 0 {
			ms, e := repo.Manifests(ctx)
			if e != nil {
				return nil, e
			}
			en, ok := ms.(distribution.ManifestEnumerator)
			if !ok {
				return nil, errors.New("内核无法枚举镜像")
			}
			found := false
			stop := errors.New("found")
			e = en.Enumerate(ctx, func(digest.Digest) error { found = true; return stop })
			var missing driver.PathNotFoundError
			if e != nil && !errors.Is(e, stop) && !errors.As(e, &missing) {
				return nil, e
			}
			if !found {
				continue
			}
		}
		out = append(out, Summary{name, meta.Description, meta.Public, meta.Revision, len(tags), meta.UpdatedAt})
	}
	return out, nil
}
func (reg *Registry) Image(ctx context.Context, name, ref string) (Image, error) {
	repo, err := reg.Repository(ctx, name)
	if err != nil {
		return Image{}, err
	}
	dgst, err := digest.Parse(ref)
	tag := ""
	if err != nil {
		desc, e := repo.Tags(ctx).Get(ctx, ref)
		if e != nil {
			return Image{}, e
		}
		dgst = desc.Digest
		tag = ref
	}
	ms, err := repo.Manifests(ctx)
	if err != nil {
		return Image{}, err
	}
	m, err := ms.Get(ctx, dgst)
	if err != nil {
		return Image{}, err
	}
	mt, raw, err := m.Payload()
	if err != nil {
		return Image{}, err
	}
	var doc struct {
		Manifests   []v1.Descriptor   `json:"manifests"`
		Config      v1.Descriptor     `json:"config"`
		Layers      []v1.Descriptor   `json:"layers"`
		Annotations map[string]string `json:"annotations"`
	}
	if err = json.Unmarshal(raw, &doc); err != nil {
		return Image{}, err
	}

	size, created, err := inspectImageContents(ctx, repo, ms, dgst, m)
	if err != nil {
		return Image{}, err
	}
	// The index's own declared creation time takes precedence over the
	// platform config fallback, even when a platform was built later.
	if doc.Manifests != nil {
		if declared, e := time.Parse(time.RFC3339Nano, doc.Annotations[v1.AnnotationCreated]); e == nil && !declared.IsZero() {
			created = declared.UTC().Format(time.RFC3339Nano)
		}
	}
	image := Image{Tag: tag, Digest: dgst.String(), MediaType: mt, Platforms: []string{}, References: m.References(), Raw: raw, Size: size, Created: created}
	for _, d := range doc.Manifests {
		if d.Platform != nil {
			p := d.Platform.OS + "/" + d.Platform.Architecture
			if d.Platform.Variant != "" {
				p += "/" + d.Platform.Variant
			}
			image.Platforms = append(image.Platforms, p)
		}
	}
	if len(doc.Manifests) == 0 && doc.Config.Digest != "" && doc.Config.Size <= 1<<20 {
		reader, e := repo.Blobs(ctx).Open(ctx, doc.Config.Digest)
		if e == nil {
			data, e := io.ReadAll(io.LimitReader(reader, (1<<20)+1))
			reader.Close()
			if e == nil && len(data) <= 1<<20 {
				var conf struct {
					Architecture string `json:"architecture"`
					OS           string `json:"os"`
				}
				if json.Unmarshal(data, &conf) == nil {
					if conf.OS != "" {
						image.Platforms = append(image.Platforms, conf.OS+"/"+conf.Architecture)
					}
				}
			}
		}
	}
	return image, nil
}
func (reg *Registry) ImageTags(ctx context.Context, name, search string) ([]string, error) {
	repo, err := reg.Repository(ctx, name)
	if err != nil {
		return nil, err
	}
	tags, err := repo.Tags(ctx).All(ctx)
	if err != nil {
		return nil, err
	}
	sort.Strings(tags)
	q := strings.ToLower(search)
	if q == "" {
		return tags, nil
	}
	filtered := []string{}
	for _, tag := range tags {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if strings.Contains(strings.ToLower(tag), q) {
			filtered = append(filtered, tag)
			continue
		}
		desc, e := repo.Tags(ctx).Get(ctx, tag)
		if e != nil {
			return nil, e
		}
		if strings.Contains(desc.Digest.String(), q) {
			filtered = append(filtered, tag)
		}
	}
	return filtered, nil
}
func (reg *Registry) Images(ctx context.Context, name string) ([]Image, error) {
	tags, err := reg.ImageTags(ctx, name, "")
	if err != nil {
		return nil, err
	}
	return reg.ImagesForTags(ctx, name, tags)
}
func (reg *Registry) ImagesForTags(ctx context.Context, name string, tags []string) ([]Image, error) {
	repo, err := reg.Repository(ctx, name)
	if err != nil {
		return nil, err
	}
	out := []Image{}

	byDigest := map[digest.Digest]Image{}
	for _, tag := range tags {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		descriptor, err := repo.Tags(ctx).Get(ctx, tag)
		if err != nil {
			return nil, err
		}
		img, ok := byDigest[descriptor.Digest]
		if !ok {
			img, err = reg.Image(ctx, name, descriptor.Digest.String())
			if err != nil {
				return nil, err
			}
			img.Raw = nil
			byDigest[descriptor.Digest] = img
		}
		img.Tag = tag
		out = append(out, img)
	}
	return out, nil
}

func (reg *Registry) Disk() (used, free, total uint64, err error) {
	var st unix.Statfs_t
	if err = unix.Statfs(reg.Store.Dir, &st); err != nil {
		return
	}
	free = st.Bavail * uint64(st.Bsize)
	total = st.Blocks * uint64(st.Bsize)
	err = filepath.WalkDir(filepath.Join(reg.Store.Dir, "registry"), func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.Type().IsRegular() {
			i, e := d.Info()
			if e != nil {
				return e
			}
			used += uint64(i.Size())
		}
		return nil
	})
	return
}

// GC requires exclusive access to registry storage, either Gate ownership or the offline instance lock.
func (reg *Registry) GC(ctx context.Context, dry bool) error {
	plan, err := reg.PlanCleanup(ctx, "gc", time.Time{})
	if err != nil || dry {
		return err
	}
	return reg.ExecuteCleanup(ctx, plan)
}
func IsRegistryPath(path string) bool {
	return path == "/v2/" || path == "/v2" || strings.HasPrefix(path, "/v2/")
}

// Derived catalog data is never consulted by the authorization controller.
func (reg *Registry) Invalidate() {
	reg.catalogMu.Lock()
	defer reg.catalogMu.Unlock()
	reg.catalogAt = time.Time{}
}
func (reg *Registry) refreshCatalog(ctx context.Context) error {
	items, err := reg.scan(ctx)
	if err != nil {
		return err
	}
	reg.catalog = items
	reg.catalogAt = time.Now().UTC()
	err = jsonfile.Write(filepath.Join(reg.Store.Dir, "cache", "catalog.json"), map[string]any{"schemaVersion": 1, "updatedAt": reg.catalogAt, "items": items})
	reg.catalogError = ""
	if err != nil {
		reg.catalogError = err.Error()
	}
	return nil
}
func (reg *Registry) Rebuild(ctx context.Context) error {
	reg.catalogMu.Lock()
	defer reg.catalogMu.Unlock()
	return reg.refreshCatalog(ctx)
}
func (reg *Registry) CatalogStatus() (time.Time, string) {
	if !reg.catalogMu.TryLock() {
		return time.Time{}, ""
	}
	defer reg.catalogMu.Unlock()
	return reg.catalogAt, reg.catalogError
}
func (reg *Registry) List(ctx context.Context, public bool) ([]Summary, error) {
	reg.catalogMu.Lock()
	defer reg.catalogMu.Unlock()
	if reg.catalogAt.IsZero() || time.Since(reg.catalogAt) > 30*time.Second {
		if err := reg.refreshCatalog(ctx); err != nil {
			return nil, err
		}
	}
	st := reg.Store.Snapshot()
	out := []Summary{}
	for _, item := range reg.catalog {
		meta := st.Repositories[item.Name]
		if public && !meta.Public {
			continue
		}
		item.Description = meta.Description
		item.Public = meta.Public
		item.Revision = meta.Revision
		item.UpdatedAt = meta.UpdatedAt
		out = append(out, item)
	}
	return out, nil
}

// Upload cancellation removes an unfinished upload, not a manifest or a stored blob.
func IsUploadCancellation(r *http.Request) bool {
	if r.Method != http.MethodDelete || !strings.HasPrefix(r.URL.Path, "/v2/") {
		return false
	}
	i := strings.LastIndex(r.URL.Path, "/blobs/uploads/")
	if i < 0 {
		return false
	}
	tail := r.URL.Path[i+len("/blobs/uploads/"):]
	return tail != "" && !strings.Contains(tail, "/")
}
