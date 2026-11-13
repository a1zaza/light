package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/opencontainers/go-digest"
	"sort"
	"time"

	"github.com/distribution/distribution/v3/registry/storage"
	driver "github.com/distribution/distribution/v3/registry/storage/driver"
	"light/internal/store"
)

type CleanupTarget struct {
	Path   string    `json:"path"`
	Bytes  int64     `json:"bytes"`
	Files  int       `json:"files"`
	Latest time.Time `json:"latest"`
}
type CleanupPlan struct {
	Kind        string          `json:"kind"`
	Cutoff      time.Time       `json:"cutoff,omitempty"`
	Targets     []CleanupTarget `json:"targets"`
	Fingerprint string          `json:"fingerprint"`
	Files       int             `json:"files"`
	Bytes       int64           `json:"bytes"`
}

// Inspect target directories through the driver's API rather than reconstructing
// Distribution's on-disk layout. Reads never alter the registry.
func inspectTarget(ctx context.Context, d driver.StorageDriver, path string) (CleanupTarget, error) {
	info, err := d.Stat(ctx, path)
	if err != nil {
		return CleanupTarget{}, err
	}
	t := CleanupTarget{Path: path, Latest: info.ModTime()}
	include := func(info driver.FileInfo) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if info.ModTime().After(t.Latest) {
			t.Latest = info.ModTime()
		}
		if !info.IsDir() {
			t.Bytes += info.Size()
			t.Files++
		}
		return nil
	}
	if info.IsDir() {
		err = d.Walk(ctx, path, include)
	} else {
		err = include(info)
	}
	return t, err
}

type planningDriver struct {
	driver.StorageDriver
	targets   map[string]CleanupTarget
	cutoff    time.Time
	protected map[digest.Digest]bool
}

func (d *planningDriver) PutContent(context.Context, string, []byte) error {
	return errors.New("维护预览不允许写入存储")
}
func (d *planningDriver) Writer(context.Context, string, bool) (driver.FileWriter, error) {
	return nil, errors.New("维护预览不允许写入存储")
}
func (d *planningDriver) Move(context.Context, string, string) error {
	return errors.New("维护预览不允许移动存储")
}
func (d *planningDriver) Delete(ctx context.Context, path string) error {
	if dgst, ok := storagePathDigest(path); ok && d.protected[dgst] {
		return nil
	}
	target, err := inspectTarget(ctx, d.StorageDriver, path)
	if err != nil {
		return err
	}
	// The upstream purger uses creation age. Also require no recent activity, so
	// an old upload resumed by a client is not classified as abandoned.
	if !d.cutoff.IsZero() && !target.Latest.Before(d.cutoff) {
		return nil
	}
	d.targets[path] = target
	return nil
}
func (reg *Registry) cleanupWith(ctx context.Context, kind string, d driver.StorageDriver, cutoff time.Time) error {
	// The fixed filesystem driver has no repository root until the first upload.
	if _, err := d.Stat(ctx, "/docker/registry/v2/repositories"); err != nil {
		var missing driver.PathNotFoundError
		if errors.As(err, &missing) {
			return nil
		}
		return err
	}

	switch kind {
	case "gc":
		return storage.MarkAndSweep(ctx, d, reg.Namespace, storage.GCOpts{Quiet: true, RemoveUntagged: false})
	case "uploads":
		_, errs := storage.PurgeUploads(ctx, d, cutoff, true)
		return errors.Join(errs...)
	default:
		return errors.New("不支持的清理类型")
	}
}

// PlanCleanup requires exclusive Gate ownership. Destructive driver calls are
// intercepted, so the plan comes from the exact same upstream collector as execution.
func (reg *Registry) PlanCleanup(ctx context.Context, kind string, cutoff time.Time) (CleanupPlan, error) {
	protected := map[digest.Digest]bool{}
	if kind == "gc" {
		var err error
		protected, err = reg.protectedBlobs(ctx)
		if err != nil {
			return CleanupPlan{}, err
		}
	}
	d := &planningDriver{StorageDriver: reg.Driver, targets: map[string]CleanupTarget{}, cutoff: cutoff, protected: protected}
	if err := reg.cleanupWith(ctx, kind, d, cutoff); err != nil {
		return CleanupPlan{}, err
	}
	p := CleanupPlan{Kind: kind, Cutoff: cutoff, Targets: []CleanupTarget{}}
	for _, target := range d.targets {
		p.Targets = append(p.Targets, target)
		p.Bytes += target.Bytes
		p.Files += target.Files
	}
	sort.Slice(p.Targets, func(i, j int) bool { return p.Targets[i].Path < p.Targets[j].Path })
	raw, _ := json.Marshal(p)
	p.Fingerprint = store.Hash(string(raw))
	return p, nil
}

type executingDriver struct {
	driver.StorageDriver
	protected map[digest.Digest]bool
	targets   map[string]CleanupTarget
	cutoff    time.Time
}

func (d *executingDriver) Delete(ctx context.Context, path string) error {
	if dgst, ok := storagePathDigest(path); ok && d.protected[dgst] {
		return nil
	}
	expected, ok := d.targets[path]
	if !ok {
		if !d.cutoff.IsZero() {
			target, err := inspectTarget(ctx, d.StorageDriver, path)
			if err != nil {
				return err
			}
			if !target.Latest.Before(d.cutoff) {
				return nil
			}
		}
		return errors.New("清理范围已变化，请重新预览")
	}
	current, err := inspectTarget(ctx, d.StorageDriver, path)
	if err != nil {
		return err
	}
	if current.Path != expected.Path || current.Bytes != expected.Bytes || current.Files != expected.Files || !current.Latest.Equal(expected.Latest) {
		return errors.New("清理文件已变化，请重新预览")
	}
	if err = d.StorageDriver.Delete(ctx, path); err != nil {
		return err
	}
	delete(d.targets, path)
	return nil
}

// ExecuteCleanup requires exclusive Gate ownership, and revalidates the plan
// before the first destructive action. An in-flight upload cannot be collected.
func (reg *Registry) ExecuteCleanup(ctx context.Context, plan CleanupPlan) error {
	fresh, err := reg.PlanCleanup(ctx, plan.Kind, plan.Cutoff)
	if err != nil {
		return err
	}
	if fresh.Fingerprint != plan.Fingerprint {
		return errors.New("存储内容已变化，请重新预览后执行")
	}
	targets := map[string]CleanupTarget{}
	for _, target := range fresh.Targets {
		targets[target.Path] = target
	}
	protected := map[digest.Digest]bool{}
	if plan.Kind == "gc" {
		protected, err = reg.protectedBlobs(ctx)
		if err != nil {
			return err
		}
	}
	d := &executingDriver{StorageDriver: reg.Driver, targets: targets, cutoff: plan.Cutoff, protected: protected}
	if err = reg.cleanupWith(ctx, plan.Kind, d, plan.Cutoff); err != nil {
		return err
	}
	if len(d.targets) != 0 {
		return fmt.Errorf("还有 %d 个清理项目未完成，请重新预览", len(d.targets))
	}
	reg.Invalidate()
	return nil
}

// Admission is closed before draining Gate, keeping the control plane responsive.
func (reg *Registry) BeginMaintenance() bool { return reg.maintaining.CompareAndSwap(false, true) }
func (reg *Registry) EndMaintenance()        { reg.maintaining.Store(false) }
func (reg *Registry) Maintaining() bool      { return reg.maintaining.Load() }
