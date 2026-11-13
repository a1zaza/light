package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	driver "github.com/distribution/distribution/v3/registry/storage/driver"
	"light/internal/jsonfile"
	"light/internal/maintenance"
)

func (f *fixture) startMaintenance(action, previewID string) maintenance.Job {
	f.t.Helper()
	raw, _ := json.Marshal(map[string]any{"action": action, "previewId": previewID})
	w := f.request("POST", "/api/admin/maintenance", string(raw), "", true)
	f.status(w, 202)
	var response struct {
		Job maintenance.Job `json:"job"`
	}
	f.decode(w, &response)
	return response.Job
}
func (f *fixture) waitMaintenance(id string) maintenance.Job {
	f.t.Helper()
	until := time.Now().Add(5 * time.Second)
	for time.Now().Before(until) {
		state := f.server.Maintenance.Snapshot()
		for _, job := range state.Jobs {
			if job.ID == id && job.Status != "queued" && job.Status != "running" && state.Active == nil {
				return job
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	f.t.Fatal("maintenance job did not finish")
	return maintenance.Job{}
}
func (f *fixture) finishMaintenance(job maintenance.Job) maintenance.Job {
	f.t.Helper()
	done := f.waitMaintenance(job.ID)
	if done.Status != "completed" {
		f.t.Fatalf("job failed: %#v", done)
	}
	return done
}

func TestMaintenancePreviewRevalidationAndSharedData(t *testing.T) {
	f := newFixture(t)
	name := "maintenance/app"
	token := f.token(name, true)
	image, _, shared := f.image(name, "latest", token, "amd64")
	orphan := f.blob(name, token, []byte("orphaned uploaded bytes"))
	preview := f.finishMaintenance(f.startMaintenance("gc-preview", ""))
	if preview.Items == 0 || preview.Bytes == 0 {
		t.Fatal("preview did not include orphaned content")
	}
	// A preview must not modify a single referenced or unreferenced file.
	f.status(f.request("GET", "/v2/"+name+"/blobs/"+orphan, "", token, false), 200)
	f.status(f.request("GET", "/v2/"+name+"/manifests/"+image, "", token, false), 200)
	state := f.request("GET", "/api/admin/maintenance", "", "", true)
	f.status(state, 200)
	if strings.Contains(state.Body.String(), `"plan"`) || strings.Contains(state.Body.String(), "/docker/registry/") {
		t.Fatal("internal cleanup paths escaped the management API")
	}
	// New data between preview and execute invalidates the plan before deletion.
	another := f.blob(name, token, []byte("new orphan after the preview"))
	failed := f.waitMaintenance(f.startMaintenance("gc-execute", preview.ID).ID)
	if failed.Status != "failed" || !strings.Contains(failed.Error, "重新预览") {
		t.Fatalf("stale plan accepted: %#v", failed)
	}
	f.status(f.request("GET", "/v2/"+name+"/blobs/"+orphan, "", token, false), 200)
	preview = f.finishMaintenance(f.startMaintenance("gc-preview", ""))
	f.finishMaintenance(f.startMaintenance("gc-execute", preview.ID))
	for _, dgst := range []string{orphan, another} {
		f.status(f.request("GET", "/v2/"+name+"/blobs/"+dgst, "", token, false), 404)
		f.status(f.request("HEAD", "/v2/"+name+"/blobs/"+dgst, "", token, false), 404)
	}
	f.status(f.request("GET", "/v2/"+name+"/blobs/"+shared, "", token, false), 200)
	f.status(f.request("GET", "/v2/"+name+"/manifests/"+image, "", token, false), 200)
	replay := f.request("POST", "/api/admin/maintenance", `{"action":"gc-execute","previewId":"`+preview.ID+`"}`, "", true)
	f.status(replay, 409)
	f.status(f.request("POST", "/api/admin/maintenance", `{"action":"gc-execute"}`, "", true), 409)
}
func TestMaintenanceDrainsRequestsAndPreservesControlPlane(t *testing.T) {
	f := newFixture(t)
	f.reg.Gate.RLock()
	locked := true
	defer func() {
		if locked {
			f.reg.Gate.RUnlock()
		}
	}()
	job := f.startMaintenance("gc-preview", "")
	if !f.reg.Maintaining() {
		t.Fatal("maintenance admission not closed")
	}
	f.status(f.request("POST", "/api/admin/maintenance", `{"action":"uploads-preview"}`, "", true), 409)
	unavailable := f.request("GET", "/v2/", "", "", false)
	f.status(unavailable, 503)
	if unavailable.Header().Get("Retry-After") != "5" {
		t.Fatal("missing retry interval")
	}
	f.status(f.request("GET", "/api/public/repositories", "", "", false), 503)
	for _, path := range []string{"/healthz", "/readyz", "/api/session", "/api/admin/maintenance"} {
		f.status(f.request("GET", path, "", "", true), 200)
	}
	f.reg.Gate.RUnlock()
	locked = false
	f.finishMaintenance(job)
	if f.reg.Maintaining() {
		t.Fatal("mirror access not resumed")
	}
	f.status(f.request("POST", "/api/admin/maintenance", `{"action":"reindex"}`, "", false), 401)
	// CSRF remains required for all online maintenance actions.
	req := httptest.NewRequest("POST", "https://registry.test/api/admin/maintenance", strings.NewReader(`{"action":"gc-preview"}`))
	req.AddCookie(f.cookie)
	req.Header.Set("Origin", "https://registry.test")
	out := httptest.NewRecorder()
	f.server.ServeHTTP(out, req)
	f.status(out, 403)
}
func TestMaintenanceShutdownAndRestartRecovery(t *testing.T) {
	f := newFixture(t)
	f.reg.Gate.RLock()
	job := f.startMaintenance("gc-preview", "")
	f.server.Close()
	f.reg.Gate.RUnlock()
	done := f.waitMaintenance(job.ID)
	if done.Status != "interrupted" || f.reg.Maintaining() {
		t.Fatal("shutdown did not interrupt queued job")
	}
	path := filepath.Join(f.store.Dir, "jobs", "maintenance.json")
	raw := map[string]any{"schemaVersion": 1, "jobs": []map[string]any{{"id": "interrupted-job", "kind": "gc", "mode": "execute", "status": "running", "stage": "cleaning", "actor": "admin", "createdAt": time.Now().UTC(), "items": 0, "files": 0, "bytes": 0}}}
	if err := jsonfile.Write(path, raw); err != nil {
		t.Fatal(err)
	}
	restored := maintenance.New(f.store, f.reg)
	defer restored.Close()
	state := restored.Snapshot()
	if state.Active != nil || len(state.Jobs) != 1 || state.Jobs[0].Status != "interrupted" {
		t.Fatalf("job was not recovered safely: %#v", state)
	}
}
func TestUploadCleanupProtectsRecentActivity(t *testing.T) {
	f := newFixture(t)
	name := "maintenance/uploads"
	token := f.token(name, true)
	old := time.Now().Add(-10 * 24 * time.Hour)
	upload := func(recent bool) string {
		start := f.request("POST", "/v2/"+name+"/blobs/uploads/", "", token, false)
		f.status(start, 202)
		location := start.Header().Get("Location")
		u, _ := url.Parse(location)
		uuid := filepath.Base(u.Path)
		root := "/docker/registry/v2/repositories/" + name + "/_uploads/" + uuid
		if err := f.reg.Driver.PutContent(context.Background(), root+"/startedat", []byte(old.UTC().Format(time.RFC3339))); err != nil {
			t.Fatal(err)
		}
		paths := []string{root}
		err := f.reg.Driver.Walk(context.Background(), root, func(info driver.FileInfo) error { paths = append(paths, info.Path()); return nil })
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range paths {
			local := filepath.Join(f.store.Dir, "registry", strings.TrimPrefix(path, "/"))
			if err = os.Chtimes(local, old, old); err != nil {
				t.Fatal(err)
			}
		}
		if recent {
			req := httptest.NewRequest("PATCH", "https://registry.test"+location, strings.NewReader("recent upload data"))
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/octet-stream")
			out := httptest.NewRecorder()
			f.server.ServeHTTP(out, req)
			f.status(out, 202)
			location = out.Header().Get("Location")
		}
		return location
	}
	abandoned := upload(false)
	recent := upload(true)
	preview := f.finishMaintenance(f.startMaintenance("uploads-preview", ""))
	if preview.Items != 1 {
		t.Fatalf("expected only abandoned upload, got %#v", preview)
	}
	u, _ := url.Parse(abandoned)
	root := "/docker/registry/v2/repositories/" + name + "/_uploads/" + filepath.Base(u.Path)
	if _, err := f.reg.Driver.Stat(context.Background(), root); err != nil {
		t.Fatal("preview removed the upload:", err)
	}
	f.finishMaintenance(f.startMaintenance("uploads-execute", preview.ID))
	f.status(f.request("GET", abandoned, "", token, false), 404)
	f.status(f.request("GET", recent, "", token, false), 204)
}

func TestMaintenanceCompletedPreviewSurvivesRestart(t *testing.T) {
	f := newFixture(t)
	name := "maintenance/persist"
	token := f.token(name, true)
	orphan := f.blob(name, token, []byte("persisted preview orphan"))
	preview := f.finishMaintenance(f.startMaintenance("gc-preview", ""))
	f.server.Maintenance.Close()
	f.server.Maintenance = maintenance.New(f.store, f.reg)
	f.finishMaintenance(f.startMaintenance("gc-execute", preview.ID))
	f.status(f.request("GET", "/v2/"+name+"/blobs/"+orphan, "", token, false), 404)
}

type panicDeleteDriver struct{ driver.StorageDriver }

func (d panicDeleteDriver) Delete(context.Context, string) error { panic("synthetic cleanup failure") }
func TestMaintenanceFailureRestoresAccess(t *testing.T) {
	f := newFixture(t)
	name := "maintenance/failure"
	token := f.token(name, true)
	orphan := f.blob(name, token, []byte("preserve on driver failure"))
	preview := f.finishMaintenance(f.startMaintenance("gc-preview", ""))
	original := f.reg.Driver
	f.reg.Driver = panicDeleteDriver{original}
	done := f.waitMaintenance(f.startMaintenance("gc-execute", preview.ID).ID)
	f.reg.Driver = original
	if done.Status != "failed" || !strings.Contains(done.Error, "synthetic cleanup failure") {
		t.Fatalf("unexpected failure handling: %#v", done)
	}
	if f.reg.Maintaining() {
		t.Fatal("failed cleanup left mirror admission closed")
	}
	f.status(f.request("GET", "/v2/"+name+"/blobs/"+orphan, "", token, false), 200)
}

func TestMaintenanceLedgerFailureDoesNotStartCleanup(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(f.store.Dir, "jobs", "maintenance.json")
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	f.status(f.request("POST", "/api/admin/maintenance", `{"action":"gc-preview"}`, "", true), 409)
	state := f.server.Maintenance.Snapshot()
	if state.Active != nil || state.Error == "" || f.reg.Maintaining() {
		t.Fatalf("failed ledger write started a job: %#v", state)
	}
	f.status(f.request("GET", "/readyz", "", "", false), 200)
}

func TestMaintenanceExpiredPreviewRequiresNewPreview(t *testing.T) {
	f := newFixture(t)
	preview := f.finishMaintenance(f.startMaintenance("gc-preview", ""))
	f.server.Maintenance.Close()
	path := filepath.Join(f.store.Dir, "jobs", "maintenance.json")
	var disk struct {
		SchemaVersion int               `json:"schemaVersion"`
		Jobs          []maintenance.Job `json:"jobs"`
	}
	if err := jsonfile.Read(path, &disk); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	disk.Jobs[0].ExpiresAt = &past
	if err := jsonfile.Write(path, disk); err != nil {
		t.Fatal(err)
	}
	f.server.Maintenance = maintenance.New(f.store, f.reg)
	f.status(f.request("POST", "/api/admin/maintenance", `{"action":"gc-execute","previewId":"`+preview.ID+`"}`, "", true), 409)
	if f.reg.Maintaining() {
		t.Fatal("expired preview blocked mirror admission")
	}
}
