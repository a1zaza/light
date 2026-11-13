// Package storageutil treats a missing enumeration root as an empty traversal.
// This avoids Distribution's walker formatting away the missing-path type.
package storageutil

import (
	"context"
	"errors"
	driver "github.com/distribution/distribution/v3/registry/storage/driver"
)

type Driver struct{ driver.StorageDriver }

func (d Driver) Walk(ctx context.Context, path string, f driver.WalkFn, options ...func(*driver.WalkOptions)) error {
	if _, err := d.StorageDriver.Stat(ctx, path); err != nil {
		var missing driver.PathNotFoundError
		if errors.As(err, &missing) {
			return nil
		}
		return err
	}
	return d.StorageDriver.Walk(ctx, path, f, options...)
}
