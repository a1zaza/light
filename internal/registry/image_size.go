package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"time"

	distribution "github.com/distribution/distribution/v3"
	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

// inspectImageContents includes the root and descendant manifests, configs and
// compressed layers reachable from this image. Subject backlinks and referrers
// are not image contents. Each digest contributes bytes at most once.
func inspectImageContents(ctx context.Context, repo distribution.Repository, manifests distribution.ManifestService, root digest.Digest, manifest distribution.Manifest) (int64, string, error) {
	const maxObjects = 100000
	counted := map[digest.Digest]bool{}
	visitedManifests := map[digest.Digest]bool{}
	var total int64
	var created time.Time
	add := func(dgst digest.Digest, size int64) error {
		if counted[dgst] {
			return nil
		}
		if size < 0 || size > math.MaxInt64-total {
			return errors.New("镜像大小超过统计范围")
		}
		if len(counted) >= maxObjects {
			return errors.New("镜像引用数量超过统计上限")
		}
		counted[dgst] = true
		total += size
		return nil
	}
	var visit func(digest.Digest, distribution.Manifest, int) error
	visit = func(dgst digest.Digest, m distribution.Manifest, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if visitedManifests[dgst] {
			return nil
		}
		if depth > 64 {
			return errors.New("镜像索引嵌套超过 64 层")
		}
		if len(visitedManifests) >= maxObjects {
			return errors.New("镜像引用数量超过统计上限")
		}
		visitedManifests[dgst] = true
		var err error
		if m == nil {
			m, err = manifests.Get(ctx, dgst)
			if err != nil {
				return fmt.Errorf("无法读取镜像子清单 %s：%w", dgst, err)
			}
		}
		_, raw, err := m.Payload()
		if err != nil {
			return err
		}
		if err = add(dgst, int64(len(raw))); err != nil {
			return err
		}
		var index struct {
			Manifests []v1.Descriptor `json:"manifests"`
			Config    v1.Descriptor   `json:"config"`
		}
		if err = json.Unmarshal(raw, &index); err != nil {
			return err
		}
		if index.Manifests != nil {
			for _, child := range index.Manifests {
				if err = visit(child.Digest, nil, depth+1); err != nil {
					return err
				}
			}
			return nil
		}
		for _, ref := range m.References() {
			if counted[ref.Digest] {
				continue
			}
			descriptor, err := repo.Blobs(ctx).Stat(ctx, ref.Digest)
			if err != nil {
				return fmt.Errorf("无法读取镜像内容 %s：%w", ref.Digest, err)
			}
			if err = add(ref.Digest, descriptor.Size); err != nil {
				return err
			}
		}
		// An index has no image config of its own. Use the latest valid
		// platform build time, comparing instants rather than timestamp strings.
		if index.Config.Digest != "" && index.Config.Size <= 1<<20 {
			reader, e := repo.Blobs(ctx).Open(ctx, index.Config.Digest)
			if e == nil {
				data, e := io.ReadAll(io.LimitReader(reader, (1<<20)+1))
				reader.Close()
				if e == nil && len(data) <= 1<<20 {
					var config struct {
						Created string `json:"created"`
					}
					if json.Unmarshal(data, &config) == nil {
						if t, e := time.Parse(time.RFC3339Nano, config.Created); e == nil && !t.IsZero() && t.After(created) {
							created = t
						}
					}
				}
			}
		}
		return nil
	}
	if err := visit(root, manifest, 0); err != nil {
		return 0, "", err
	}
	timestamp := ""
	if !created.IsZero() {
		timestamp = created.UTC().Format(time.RFC3339Nano)
	}
	return total, timestamp, nil
}
