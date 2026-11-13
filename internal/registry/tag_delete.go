package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	distribution "github.com/distribution/distribution/v3"
	"github.com/distribution/distribution/v3/registry/storage"
	"github.com/distribution/reference"
	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"light/internal/store"
)

type DeletePreview struct {
	Name        string `json:"name"`
	Tag         string `json:"tag"`
	Digest      string `json:"digest"`
	Bytes       int64  `json:"bytes"`
	Blobs       int    `json:"blobs"`
	Fingerprint string `json:"fingerprint"`
	manifests   []digest.Digest
	blobs       []digest.Digest
	links       []digest.Digest
}
type deleteNode struct {
	Content  []digest.Digest
	Refs     []digest.Digest
	Children []digest.Digest
}
type deleteSnapshot struct {
	Name  string
	Tags  map[string]digest.Digest
	Nodes map[digest.Digest]deleteNode
	Links []digest.Digest
}

// Pause requests before waiting for exclusive storage access, so new pulls and
// uploads cannot race the reference scan or starve deletion. Health stays available.
func (reg *Registry) LockDeletion(ctx context.Context) (func(), error) {
	if !reg.BeginMaintenance() {
		return nil, errors.New("存储维护正在进行，请稍后重试")
	}
	timer := time.NewTicker(10 * time.Millisecond)
	defer timer.Stop()
	for !reg.Gate.TryLock() {
		select {
		case <-ctx.Done():
			reg.EndMaintenance()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return func() { reg.Gate.Unlock(); reg.EndMaintenance() }, nil
}

// Preview requires exclusive Gate ownership. Only this tag's image graph is a
// deletion candidate; unrelated untagged manifests and other repositories survive.
func (reg *Registry) Preview(ctx context.Context, name, tag string) (DeletePreview, error) {
	p := DeletePreview{Name: name, Tag: tag}
	named, err := reference.WithName(name)
	if err != nil {
		return p, err
	}
	if _, err = reference.WithTag(named, tag); err != nil {
		return p, err
	}
	names, err := reg.Names(ctx)
	if err != nil {
		return p, err
	}
	snapshots := []deleteSnapshot{}
	var target *deleteSnapshot
	for _, n := range names {
		repo, e := reg.Repository(ctx, n)
		if e != nil {
			return p, e
		}
		ms, e := repo.Manifests(ctx)
		if e != nil {
			return p, e
		}
		en, ok := ms.(distribution.ManifestEnumerator)
		if !ok {
			return p, errors.New("内核不支持清单扫描")
		}
		s := deleteSnapshot{Name: n, Tags: map[string]digest.Digest{}, Nodes: map[digest.Digest]deleteNode{}}
		e = en.Enumerate(ctx, func(d digest.Digest) error {
			if len(s.Nodes) >= 100000 {
				return errors.New("清单数量超过安全扫描上限")
			}
			m, e := ms.Get(ctx, d)
			if e != nil {
				return e
			}
			_, raw, e := m.Payload()
			if e != nil {
				return e
			}
			var doc struct {
				Manifests []v1.Descriptor `json:"manifests"`
				Subject   *v1.Descriptor  `json:"subject"`
			}
			if e = json.Unmarshal(raw, &doc); e != nil {
				return e
			}
			node := deleteNode{}
			for _, ref := range m.References() {
				node.Refs = append(node.Refs, ref.Digest)
				node.Content = append(node.Content, ref.Digest)
			}
			if doc.Subject != nil {
				node.Refs = append(node.Refs, doc.Subject.Digest)
			}
			for _, ref := range doc.Manifests {
				node.Children = append(node.Children, ref.Digest)
			}
			s.Nodes[d] = node
			return nil
		})
		if e != nil {
			return p, e
		}
		tags, e := repo.Tags(ctx).All(ctx)
		var unknown distribution.ErrRepositoryUnknown
		if e != nil && !errors.As(e, &unknown) {
			return p, e
		}
		for _, t := range tags {
			d, e := repo.Tags(ctx).Get(ctx, t)
			if e != nil {
				return p, e
			}
			s.Tags[t] = d.Digest
		}
		// Other repositories may have finished uploading blobs before publishing a
		// manifest. Their layer links protect that content too.
		if n != name {
			en, ok := repo.Blobs(ctx).(distribution.ManifestEnumerator)
			if !ok {
				return p, errors.New("内核不支持镜像层扫描")
			}
			if e = en.Enumerate(ctx, func(d digest.Digest) error { s.Links = append(s.Links, d); return nil }); e != nil {
				return p, e
			}
			sort.Slice(s.Links, func(i, j int) bool { return s.Links[i] < s.Links[j] })
		}
		snapshots = append(snapshots, s)
	}
	for i := range snapshots {
		if snapshots[i].Name == name {
			target = &snapshots[i]
			break
		}
	}
	if target == nil {
		return p, errors.New("仓库不存在")
	}
	root, ok := target.Tags[tag]
	if !ok {
		return p, errors.New("标签不存在")
	}
	p.Digest = root.String()
	candidates := map[digest.Digest]bool{}
	order := []digest.Digest{}
	var visit func(digest.Digest, int) error
	visit = func(d digest.Digest, depth int) error {
		if candidates[d] {
			return nil
		}
		if depth > 64 {
			return errors.New("索引嵌套超过安全上限")
		}
		node, ok := target.Nodes[d]
		if !ok {
			return fmt.Errorf("清单缺失：%s", d)
		}
		candidates[d] = true
		for _, c := range node.Children {
			if err := visit(c, depth+1); err != nil {
				return err
			}
		}
		order = append(order, d)
		return nil
	}
	if err = visit(root, 0); err != nil {
		return p, err
	}
	pinned := map[digest.Digest]bool{}
	queue := []digest.Digest{}
	for t, d := range target.Tags {
		if t != tag {
			queue = append(queue, d)
		}
	}
	for d := range target.Nodes {
		if !candidates[d] {
			queue = append(queue, d)
		}
	}
	for len(queue) > 0 {
		d := queue[0]
		queue = queue[1:]
		if pinned[d] {
			continue
		}
		pinned[d] = true
		if node, ok := target.Nodes[d]; ok {
			queue = append(queue, node.Refs...)
		}
	}
	removed := map[digest.Digest]bool{}
	content := map[digest.Digest]bool{}
	for i := len(order) - 1; i >= 0; i-- {
		d := order[i]
		if !pinned[d] {
			removed[d] = true
			p.manifests = append(p.manifests, d)
			content[d] = true
			for _, r := range target.Nodes[d].Content {
				content[r] = true
			}
		}
	}
	global := map[digest.Digest]bool{}
	local := map[digest.Digest]bool{}
	for _, s := range snapshots {
		for d, node := range s.Nodes {
			if s.Name == name && removed[d] {
				continue
			}
			global[d] = true
			if s.Name == name {
				local[d] = true
			}
			for _, r := range node.Refs {
				global[r] = true
				if s.Name == name {
					local[r] = true
				}
			}
		}
		for _, d := range s.Links {
			global[d] = true
		}
	}
	repo, err := reg.Repository(ctx, name)
	if err != nil {
		return p, err
	}
	protected, err := reg.protectedBlobs(ctx)
	if err != nil {
		return p, err
	}
	for d := range content {
		if protected[d] {
			continue
		}
		// Unlink only content no longer used in this repository. Global blobs
		// stay intact when another repository still refers to them.
		if !local[d] {
			if _, e := repo.Blobs(ctx).Stat(ctx, d); e == nil {
				p.links = append(p.links, d)
			} else if !errors.Is(e, distribution.ErrBlobUnknown) {
				return p, e
			}
		}
		if !global[d] {
			desc, e := reg.Namespace.BlobStatter().Stat(ctx, d)
			if e != nil {
				return p, e
			}
			p.blobs = append(p.blobs, d)
			p.Bytes += desc.Size
		}
	}
	sort.Slice(p.blobs, func(i, j int) bool { return p.blobs[i] < p.blobs[j] })
	sort.Slice(p.links, func(i, j int) bool { return p.links[i] < p.links[j] })
	p.Blobs = len(p.blobs)
	raw, err := json.Marshal(struct {
		Snapshots               []deleteSnapshot
		Tag                     string
		Manifests, Blobs, Links []digest.Digest
	}{snapshots, tag, p.manifests, p.blobs, p.links})
	if err != nil {
		return p, err
	}
	p.Fingerprint = store.Hash(string(raw))
	return p, nil
}

// Delete returns tagDeleted separately: cleanup failure must not hide a
// successfully removed tag. An interrupted cleanup safely leaves extra content.
func (reg *Registry) Delete(ctx context.Context, p DeletePreview) (tagDeleted bool, err error) {
	repo, err := reg.Repository(ctx, p.Name)
	if err != nil {
		return false, err
	}
	ms, err := repo.Manifests(ctx)
	if err != nil {
		return false, err
	}
	if err = repo.Tags(ctx).Untag(ctx, p.Tag); err != nil {
		return false, err
	}
	defer reg.Invalidate()
	for _, d := range p.manifests {
		if err = ms.Delete(ctx, d); err != nil {
			return true, err
		}
	}
	vacuum := storage.NewVacuum(ctx, reg.Driver)
	for _, d := range p.links {
		if err = vacuum.RemoveLayer(p.Name, d); err != nil {
			return true, err
		}
	}
	for _, d := range p.blobs {
		if err = vacuum.RemoveBlob(d.String()); err != nil {
			return true, err
		}
	}
	return true, nil
}
