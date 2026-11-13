// Package maintenance executes serialized jobs inside the running application.
// It never invokes Docker, a shell, or a second registry process.
package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"light/internal/jsonfile"
	"light/internal/registry"
	"light/internal/store"
)

type Job struct {
	ID             string                `json:"id"`
	Kind           string                `json:"kind"`
	Mode           string                `json:"mode"`
	Status         string                `json:"status"`
	Stage          string                `json:"stage"`
	Actor          string                `json:"actor"`
	CreatedAt      time.Time             `json:"createdAt"`
	StartedAt      *time.Time            `json:"startedAt,omitempty"`
	FinishedAt     *time.Time            `json:"finishedAt,omitempty"`
	ExpiresAt      *time.Time            `json:"expiresAt,omitempty"`
	OlderThanHours int                   `json:"olderThanHours,omitempty"`
	Items          int                   `json:"items"`
	Files          int                   `json:"files"`
	Bytes          int64                 `json:"bytes"`
	Error          string                `json:"error,omitempty"`
	PreviewID      string                `json:"previewId,omitempty"`
	Consumed       bool                  `json:"consumed,omitempty"`
	Plan           *registry.CleanupPlan `json:"plan,omitempty"`
}
type diskState struct {
	SchemaVersion int   `json:"schemaVersion"`
	Jobs          []Job `json:"jobs"`
}
type Snapshot struct {
	Active *Job   `json:"active"`
	Jobs   []Job  `json:"jobs"`
	Error  string `json:"error"`
}
type Manager struct {
	mu      sync.Mutex
	reg     *registry.Registry
	st      *store.Store
	path    string
	jobs    []Job
	active  string
	failure string
	closing bool
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

func New(st *store.Store, reg *registry.Registry) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{reg: reg, st: st, path: filepath.Join(st.Dir, "jobs", "maintenance.json"), jobs: []Job{}, ctx: ctx, cancel: cancel}
	var disk diskState
	err := jsonfile.Read(m.path, &disk)
	if errors.Is(err, os.ErrNotExist) {
		return m
	}
	if err != nil || disk.SchemaVersion != 1 {
		m.failure = "维护记录损坏或版本不支持，请离线检查 jobs/maintenance.json"
		return m
	}
	m.jobs = disk.Jobs
	changed := false
	now := time.Now().UTC()
	for i := range m.jobs {
		j := &m.jobs[i]
		if j.Status == "queued" || j.Status == "running" {
			j.Status = "interrupted"
			j.Stage = "finished"
			j.Error = "上次维护因实例停止而中断，请重新预览"
			j.FinishedAt = &now
			j.Plan = nil
			changed = true
		}
	}
	if changed {
		if err = m.persistLocked(); err != nil {
			m.failure = err.Error()
		}
	}
	return m
}
func (m *Manager) persistLocked() error { return jsonfile.Write(m.path, diskState{1, m.jobs}) }
func redacted(j Job) Job                { j.Plan = nil; return j }
func (m *Manager) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := Snapshot{Jobs: []Job{}, Error: m.failure}
	for i := len(m.jobs) - 1; i >= 0; i-- {
		j := redacted(m.jobs[i])
		out.Jobs = append(out.Jobs, j)
		if j.ID == m.active {
			out.Active = &j
		}
	}
	return out
}
func (m *Manager) Start(kind, mode, previewID string, hours int, actor, ip string) (Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing {
		return Job{}, errors.New("实例正在停止")
	}
	if m.failure != "" {
		return Job{}, errors.New(m.failure)
	}
	if m.active != "" {
		return Job{}, errors.New("已有维护任务正在运行")
	}
	if kind != "gc" && kind != "uploads" && kind != "reindex" {
		return Job{}, errors.New("不支持的维护类型")
	}
	if mode != "preview" && mode != "execute" {
		return Job{}, errors.New("不支持的维护模式")
	}
	if kind == "reindex" && mode != "execute" {
		return Job{}, errors.New("索引重建不需要预览")
	}
	if kind == "uploads" && (hours < 24 || hours > 87600) {
		return Job{}, errors.New("上传闲置时间需为 24–87600 小时")
	}
	var expected *registry.CleanupPlan
	previewIndex := -1
	if mode == "execute" && kind != "reindex" {
		for i, j := range m.jobs {
			if j.ID == previewID && j.Kind == kind && j.Mode == "preview" && j.Status == "completed" && !j.Consumed && j.ExpiresAt != nil && j.ExpiresAt.After(time.Now()) && j.Plan != nil {
				expected = j.Plan
				previewIndex = i
				hours = j.OlderThanHours
				break
			}
		}
		if expected == nil {
			return Job{}, errors.New("预览已失效，请重新预览")
		}
	}
	if !m.reg.BeginMaintenance() {
		return Job{}, errors.New("存储维护中，请稍后重试")
	}
	job := Job{ID: store.Random(18), Kind: kind, Mode: mode, Status: "queued", Stage: "waiting", Actor: actor, CreatedAt: time.Now().UTC(), OlderThanHours: hours, PreviewID: previewID}
	previous := append([]Job(nil), m.jobs...)
	if previewIndex >= 0 {
		m.jobs[previewIndex].Consumed = true
		m.jobs[previewIndex].Plan = nil
	}
	for i := range m.jobs {
		j := &m.jobs[i]
		if (mode == "preview" && j.Kind == kind) || (j.ExpiresAt != nil && !j.ExpiresAt.After(time.Now())) {
			j.Plan = nil
			if j.Mode == "preview" {
				j.Consumed = true
			}
		}
	}
	m.jobs = append(m.jobs, job)
	if len(m.jobs) > 12 {
		m.jobs = m.jobs[len(m.jobs)-12:]
	}
	if err := m.persistLocked(); err != nil {
		m.jobs = previous
		m.failure = "维护记录写入失败：" + err.Error()
		m.reg.EndMaintenance()
		return Job{}, err
	}
	m.active = job.ID
	m.st.Record(actor, "发起存储维护", kind+"/"+mode, ip)
	m.wg.Add(1)
	go m.run(job, expected, ip)
	return redacted(job), nil
}
func (m *Manager) update(id string, fn func(*Job)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.jobs {
		if m.jobs[i].ID == id {
			fn(&m.jobs[i])
			if err := m.persistLocked(); err != nil {
				m.failure = "维护记录写入失败：" + err.Error()
				return err
			}
			return nil
		}
	}
	return errors.New("维护任务不存在")
}
func (m *Manager) run(job Job, expected *registry.CleanupPlan, ip string) {
	defer m.wg.Done()
	defer func() { m.mu.Lock(); m.reg.EndMaintenance(); m.active = ""; m.mu.Unlock() }()
	locked := false
	defer func() {
		if locked {
			m.reg.Gate.Unlock()
		}
	}()
	defer func() {
		if value := recover(); value != nil {
			_ = m.update(job.ID, func(j *Job) {
				now := time.Now().UTC()
				j.Status = "failed"
				j.Stage = "finished"
				j.FinishedAt = &now
				j.Error = fmt.Sprintf("维护任务异常：%v", value)
			})
			m.st.Record(job.Actor, "存储维护异常", job.Kind+"/"+job.Mode, ip)
		}
	}()

	// TryLock makes draining cancellable on SIGTERM instead of leaving a worker
	// waiting forever behind a slow transfer.
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	var err error
	for !m.reg.Gate.TryLock() {
		select {
		case <-m.ctx.Done():
			err = m.ctx.Err()
		case <-ticker.C:
		}
		if err != nil {
			break
		}
	}
	if err == nil {
		locked = true
	}
	if err == nil {
		err = m.ctx.Err()
	}
	if err == nil {
		err = m.update(job.ID, func(j *Job) {
			now := time.Now().UTC()
			j.StartedAt = &now
			j.Status = "running"
			j.Stage = "scanning"
			if j.Mode == "execute" {
				j.Stage = "cleaning"
			}
		})
	}
	var plan registry.CleanupPlan
	if err == nil {
		switch {
		case job.Kind == "reindex":
			err = m.reg.Rebuild(m.ctx)
		case job.Mode == "preview":
			cutoff := time.Time{}
			if job.Kind == "uploads" {
				cutoff = time.Now().UTC().Add(-time.Duration(job.OlderThanHours) * time.Hour)
			}
			plan, err = m.reg.PlanCleanup(m.ctx, job.Kind, cutoff)
		default:
			plan = *expected
			err = m.reg.ExecuteCleanup(m.ctx, plan)
		}
	}
	if err == nil && job.Mode == "preview" {
		raw, e := json.Marshal(plan)
		if e != nil {
			err = e
		} else if len(raw) > 2<<20 {
			err = errors.New("预览结果超过容量上限，请使用离线维护")
		}
	}
	if err != nil && m.ctx.Err() != nil {
		err = m.ctx.Err()
	}
	status := "completed"
	if err != nil {
		status = "failed"
		if errors.Is(err, context.Canceled) {
			status = "interrupted"
		}
	}
	finishErr := m.update(job.ID, func(j *Job) {
		now := time.Now().UTC()
		j.FinishedAt = &now
		j.Status = status
		j.Stage = "finished"
		j.Items = len(plan.Targets)
		j.Files = plan.Files
		j.Bytes = plan.Bytes
		if err != nil {
			j.Error = err.Error()
		} else if j.Mode == "preview" {
			expires := now.Add(30 * time.Minute)
			j.ExpiresAt = &expires
			j.Plan = &plan
		}
	})
	if finishErr != nil {
		status = "failed"
	}
	m.st.Record(job.Actor, "存储维护结束", job.Kind+"/"+job.Mode+"/"+status, ip)
}
func (m *Manager) Close() { m.mu.Lock(); m.closing = true; m.mu.Unlock(); m.cancel(); m.wg.Wait() }
