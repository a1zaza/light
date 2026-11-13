package registry

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	driver "github.com/distribution/distribution/v3/registry/storage/driver"
	"github.com/distribution/reference"
	"github.com/opencontainers/go-digest"
	"light/internal/jsonfile"
)

const DefaultUploadProtectionWindow = 24 * time.Hour
const maxUploadLeases = 32768

type uploadLeaseState struct {
	SchemaVersion int                         `json:"schemaVersion"`
	LegacyUntil   time.Time                   `json:"legacyUntil"`
	Blobs         map[digest.Digest]time.Time `json:"blobs"`
}

func (reg *Registry) uploadLeasePath() string {
	return filepath.Join(reg.Store.Dir, "jobs", "upload-leases.json")
}
func (reg *Registry) initUploadProtection(ctx context.Context) error {
	state := uploadLeaseState{}
	err := jsonfile.Read(reg.uploadLeasePath(), &state)
	if errors.Is(err, context.Canceled) {
		return err
	}
	if errors.Is(err, os.ErrNotExist) {
		state = uploadLeaseState{SchemaVersion: 1, Blobs: map[digest.Digest]time.Time{}}
		// Existing deployments may have paused pushes which predate this ledger.
		if _, e := reg.Driver.Stat(ctx, "/docker/registry/v2/repositories"); e == nil {
			state.LegacyUntil = time.Now().UTC().Add(reg.UploadProtectionWindow)
		} else {
			var missing driver.PathNotFoundError
			if !errors.As(e, &missing) {
				return e
			}
		}
		if err = jsonfile.Write(reg.uploadLeasePath(), state); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if state.SchemaVersion != 1 || state.Blobs == nil || len(state.Blobs) > maxUploadLeases {
		return errors.New("上传保护记录损坏")
	}
	for d, until := range state.Blobs {
		if d.Validate() != nil || until.IsZero() {
			return errors.New("上传保护记录包含无效摘要或时间")
		}
	}
	reg.uploadLeases = state
	return nil
}

// A durable lease precedes blob publication/reuse, including old shared content
// whose mtime cannot indicate that a new push has started. A failed write rejects
// the request and blocks collection rather than risking unprotected publication.
func (reg *Registry) protectUpload(d digest.Digest) error {
	if reg.UploadProtectionWindow <= 0 {
		return nil
	}
	reg.leaseMu.Lock()
	defer reg.leaseMu.Unlock()
	now := time.Now().UTC()
	until := now.Add(reg.UploadProtectionWindow)
	if reg.uploadLeases.Blobs[d].After(until.Add(-time.Minute)) {
		return nil
	}
	next := uploadLeaseState{SchemaVersion: 1, LegacyUntil: reg.uploadLeases.LegacyUntil, Blobs: map[digest.Digest]time.Time{}}
	for k, v := range reg.uploadLeases.Blobs {
		if v.After(now) {
			next.Blobs[k] = v
		}
	}
	if len(next.Blobs) >= maxUploadLeases {
		if _, exists := next.Blobs[d]; !exists {
			return errors.New("上传保护记录已达容量上限，请稍后重试")
		}
	}
	next.Blobs[d] = until
	if err := jsonfile.Write(reg.uploadLeasePath(), next); err != nil {
		reg.leaseError = err
		return err
	}
	reg.uploadLeases = next
	reg.leaseError = nil
	return nil
}
func (reg *Registry) pushLeaseDigest(r *http.Request) (digest.Digest, bool) {
	const marker = "/blobs/"
	i := strings.LastIndex(r.URL.Path, marker)
	if i < 0 || !strings.HasPrefix(r.URL.Path, "/v2/") {
		return "", false
	}
	name := strings.TrimPrefix(r.URL.Path[:i], "/v2/")
	if _, e := reference.WithName(name); e != nil {
		return "", false
	}
	tail := r.URL.Path[i+len(marker):]
	raw := ""
	switch {
	case r.Method == http.MethodHead && !strings.Contains(tail, "/"):
		raw = tail
	case r.Method == http.MethodPost && tail == "uploads/":
		raw = r.URL.Query().Get("mount")
	case r.Method == http.MethodPut && strings.HasPrefix(tail, "uploads/") && !strings.Contains(strings.TrimPrefix(tail, "uploads/"), "/"):
		raw = r.URL.Query().Get("digest")
	}
	d, err := digest.Parse(raw)
	if err != nil {
		return "", false
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	claims, err := reg.Auth.parse(token)
	if err != nil || claims.Subject == "anonymous" {
		return "", false
	}
	for _, scope := range claims.Access {
		if scope.Name == name {
			for _, action := range scope.Actions {
				if action == "push" {
					return d, true
				}
			}
		}
	}
	return "", false
}

// Only used to interpret paths supplied by the upstream filesystem driver and
// collector. Deletions remain delegated to Distribution's APIs/Vacuum.
func storagePathDigest(path string) (digest.Digest, bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) > 0 && (parts[len(parts)-1] == "data" || parts[len(parts)-1] == "link") {
		parts = parts[:len(parts)-1]
	}
	for _, distance := range []int{2, 3} {
		if len(parts) < distance {
			continue
		}
		d := digest.Digest(parts[len(parts)-distance] + ":" + parts[len(parts)-1])
		if d.Validate() == nil {
			return d, true
		}
	}
	return "", false
}

// A short pause drains HTTP requests, not multi-request Docker push transactions.
// Protect recent content and durable writer leases in both deletion and GC.
func (reg *Registry) protectedBlobs(ctx context.Context) (map[digest.Digest]bool, error) {
	protected := map[digest.Digest]bool{}
	if reg.UploadProtectionWindow <= 0 {
		return protected, nil
	}
	now := time.Now().UTC()
	reg.leaseMu.Lock()
	if reg.leaseError != nil {
		err := reg.leaseError
		reg.leaseMu.Unlock()
		return nil, fmt.Errorf("上传保护记录不可用，暂停回收：%w", err)
	}
	legacy := reg.uploadLeases.LegacyUntil.After(now)
	for d, until := range reg.uploadLeases.Blobs {
		if until.After(now) {
			protected[d] = true
		}
	}
	reg.leaseMu.Unlock()
	const root = "/docker/registry/v2/blobs"
	if _, err := reg.Driver.Stat(ctx, root); err != nil {
		var missing driver.PathNotFoundError
		if errors.As(err, &missing) {
			return protected, nil
		}
		return nil, err
	}
	err := reg.Driver.Walk(ctx, root, func(info driver.FileInfo) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(info.Path(), "/data") && (legacy || !info.ModTime().Before(now.Add(-reg.UploadProtectionWindow))) {
			d, ok := storagePathDigest(info.Path())
			if !ok {
				return errors.New("无法识别存储内容摘要，暂停回收")
			}
			protected[d] = true
		}
		return nil
	})
	return protected, err
}
