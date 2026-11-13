package registry

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	distribution "github.com/distribution/distribution/v3"
	driver "github.com/distribution/distribution/v3/registry/storage/driver"
	"github.com/opencontainers/go-digest"
	"light/internal/jsonfile"
	"light/internal/store"
)

func protectionFixture(t *testing.T) (*Registry, digest.Digest) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = st.Init("admin", "protection-test-password"); err != nil {
		st.Close()
		t.Fatal(err)
	}
	reg, err := New(context.Background(), st, 0)
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.Close(); st.Close() })
	repo, err := reg.Repository(context.Background(), "test/pending")
	if err != nil {
		t.Fatal(err)
	}
	desc, err := repo.Blobs(context.Background()).Put(context.Background(), "application/octet-stream", []byte("unfinished push content"))
	if err != nil {
		t.Fatal(err)
	}
	return reg, desc.Digest
}
func ageProtectionFixtureBlobs(t *testing.T, reg *Registry) {
	t.Helper()
	old := time.Now().Add(-48 * time.Hour)
	err := reg.Driver.Walk(context.Background(), "/docker/registry/v2/blobs", func(info driver.FileInfo) error {
		if !info.IsDir() && strings.HasSuffix(info.Path(), "/data") {
			// Only synthetic fixture contents, never the running data directory.
			return os.Chtimes(filepath.Join(reg.Store.Dir, "registry", strings.TrimPrefix(info.Path(), "/")), old, old)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestUploadProtectionFreshLeaseRestartExpiryAndGC(t *testing.T) {
	reg, d := protectionFixture(t)
	ctx := context.Background()
	protected, err := reg.protectedBlobs(ctx)
	if err != nil || !protected[d] {
		t.Fatal("fresh blob unprotected", err)
	}
	ageProtectionFixtureBlobs(t, reg)
	protected, err = reg.protectedBlobs(ctx)
	if err != nil || protected[d] {
		t.Fatal("old unleased blob protected", err)
	}
	if err = reg.protectUpload(d); err != nil {
		t.Fatal(err)
	}
	resumed := &Registry{Store: reg.Store, Driver: reg.Driver, UploadProtectionWindow: DefaultUploadProtectionWindow}
	if err = resumed.initUploadProtection(ctx); err != nil {
		t.Fatal(err)
	}
	protected, err = resumed.protectedBlobs(ctx)
	if err != nil || !protected[d] {
		t.Fatal("durable lease missing after restart", err)
	}
	if err = reg.GC(ctx, false); err != nil {
		t.Fatal(err)
	}
	if _, err = reg.Namespace.BlobStatter().Stat(ctx, d); err != nil {
		t.Fatal("leased content collected", err)
	}
	reg.uploadLeases.Blobs[d] = time.Now().Add(-time.Hour)
	if err = jsonfile.Write(reg.uploadLeasePath(), reg.uploadLeases); err != nil {
		t.Fatal(err)
	}
	if err = reg.GC(ctx, false); err != nil {
		t.Fatal(err)
	}
	if _, err = reg.Namespace.BlobStatter().Stat(ctx, d); !errors.Is(err, distribution.ErrBlobUnknown) {
		t.Fatal("expired unreferenced content not collected", err)
	}
}
func TestUploadProtectionLegacyMigrationIsDurableAndDoesNotExtendOnRestart(t *testing.T) {
	reg, d := protectionFixture(t)
	ageProtectionFixtureBlobs(t, reg)
	if err := os.Remove(reg.uploadLeasePath()); err != nil {
		t.Fatal(err)
	}
	next := &Registry{Store: reg.Store, Driver: reg.Driver, UploadProtectionWindow: DefaultUploadProtectionWindow}
	if err := next.initUploadProtection(context.Background()); err != nil {
		t.Fatal(err)
	}
	protected, err := next.protectedBlobs(context.Background())
	if err != nil || !protected[d] {
		t.Fatal("legacy in-progress content not protected", err)
	}
	again := &Registry{Store: reg.Store, Driver: reg.Driver, UploadProtectionWindow: DefaultUploadProtectionWindow}
	if err = again.initUploadProtection(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !again.uploadLeases.LegacyUntil.Equal(next.uploadLeases.LegacyUntil) {
		t.Fatal("restart extended migration protection")
	}
}
func TestUploadProtectionPersistenceFailureBlocksCollection(t *testing.T) {
	reg, d := protectionFixture(t)
	ageProtectionFixtureBlobs(t, reg)
	path := reg.uploadLeasePath()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := reg.protectUpload(d); err == nil {
		t.Fatal("lease write unexpectedly succeeded")
	}
	if _, err := reg.protectedBlobs(context.Background()); err == nil {
		t.Fatal("collection allowed with uncertain protection ledger")
	}
}
func TestStorageDigestMappingForCollectorPaths(t *testing.T) {
	d := digest.FromString("fixture")
	for _, path := range []string{
		"/docker/registry/v2/blobs/sha256/" + d.Encoded()[:2] + "/" + d.Encoded(),
		"/docker/registry/v2/blobs/sha256/" + d.Encoded()[:2] + "/" + d.Encoded() + "/data",
		"/docker/registry/v2/repositories/team/app/_layers/sha256/" + d.Encoded() + "/link",
	} {
		if got, ok := storagePathDigest(path); !ok || got != d {
			t.Fatalf("unrecognized collector path %s", path)
		}
	}
}
