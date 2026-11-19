package hubproxy

import (
	"context"
	"path/filepath"

	"github.com/distribution/distribution/v3/registry/storage"
	"github.com/distribution/distribution/v3/registry/storage/driver/filesystem"
	"light/internal/storageutil"
)

func (c *Cache) BlobBytes(ctx context.Context) (uint64, error) {
	c.gate.RLock()
	defer c.gate.RUnlock()
	return storageutil.BlobBytes(ctx, c.namespace)
}

// StoredBlobBytes inspects retained data when the proxy has not been enabled in
// this process. Creating a namespace does not create an App, run recovery, or
// fetch images. Cache JSON snapshots are deliberately excluded from blob usage.
func StoredBlobBytes(ctx context.Context, dataDir string) (uint64, error) {
	fs, err := filesystem.FromParameters(map[string]any{"rootdirectory": filepath.Join(dataDir, "hub-cache", "registry")})
	if err != nil {
		return 0, err
	}
	ns, err := storage.NewRegistry(ctx, storageutil.Driver{StorageDriver: fs}, storage.EnableDelete)
	if err != nil {
		return 0, err
	}
	return storageutil.BlobBytes(ctx, ns)
}
