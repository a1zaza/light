package storageutil

import (
	"context"
	"errors"
	"math"

	distribution "github.com/distribution/distribution/v3"
	"github.com/opencontainers/go-digest"
)

// BlobBytes counts committed content through Distribution's public namespace
// API. Repository links, metadata and unfinished uploads are not image blobs.
func BlobBytes(ctx context.Context, ns distribution.Namespace) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	var total uint64
	err := ns.Blobs().Enumerate(ctx, func(d digest.Digest) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		desc, err := ns.BlobStatter().Stat(ctx, d)
		if err != nil {
			return err
		}
		if desc.Size < 0 || uint64(desc.Size) > math.MaxUint64-total {
			return errors.New("镜像内容大小无效或超过统计上限")
		}
		total += uint64(desc.Size)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return total, nil
}
