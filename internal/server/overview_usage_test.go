package server

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func (f *fixture) overviewUsed() uint64 {
	f.t.Helper()
	w := f.request("GET", "/api/admin/overview", "", "", true)
	f.status(w, 200)
	var result struct {
		Used uint64 `json:"used"`
	}
	f.decode(w, &result)
	return result.Used
}
func TestOverviewExcludesEmptyCacheAndUnfinishedUploads(t *testing.T) {
	f := newFixture(t)
	if used := f.overviewUsed(); used != 0 {
		t.Fatalf("空仓库占用=%d", used)
	}
	if _, err := os.Stat(filepath.Join(f.store.Dir, "hub-cache")); !os.IsNotExist(err) {
		t.Fatal("统计创建了缓存目录")
	}
	if err := f.reg.ConfigureHub(enabledHubConfig(), nil); err != nil {
		t.Fatal(err)
	}
	meta, err := os.ReadFile(filepath.Join(f.store.Dir, "hub-cache", "metadata", "state.json"))
	if err != nil || len(meta) == 0 {
		t.Fatal("缺少用于复现的空缓存元数据")
	}
	if used := f.overviewUsed(); used != 0 {
		t.Fatalf("空缓存元数据被当作镜像统计：%d", used)
	}
	ctx := context.Background()
	repo, err := f.reg.Repository(ctx, "usage/partial")
	if err != nil {
		t.Fatal(err)
	}
	upload, err := repo.Blobs(ctx).Create(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer upload.Close()
	defer upload.Cancel(ctx)
	if _, err = upload.Write([]byte("unfinished upload payload")); err != nil {
		t.Fatal(err)
	}
	if used := f.overviewUsed(); used != 0 {
		t.Fatalf("未完成上传被当作已提交镜像统计：%d", used)
	}
}
func TestOverviewCountsCommittedContentOnceAcrossTagsAndRepositories(t *testing.T) {
	f := newFixture(t)
	name := "usage/shared"
	token := f.token(name, true)
	_, raw, _ := f.image(name, "one", token, "amd64")
	config := []byte(`{"architecture":"amd64","os":"linux","rootfs":{"type":"layers","diff_ids":[]}}`)
	layer := []byte("shared layer contents, bytes remain intact")
	expected := uint64(len(config) + len(layer) + len(raw))
	if used := f.overviewUsed(); used != expected {
		t.Fatalf("镜像内容占用=%d，预期=%d", used, expected)
	}
	f.image(name, "two", token, "amd64")
	other := "usage/another"
	f.image(other, "same", f.token(other, true), "amd64")
	if used := f.overviewUsed(); used != expected {
		t.Fatalf("多标签或跨仓库链接重复计数：%d，预期=%d", used, expected)
	}
}
func TestRegistryLoginRequiresRobotCredentials(t *testing.T) {
	f := newFixture(t)
	if !f.store.Authenticate("admin", "test-password-12345") {
		t.Fatal("管理员 fixture 认证失败")
	}
	for _, tc := range []struct {
		name, user, password string
		status               int
	}{
		{"administrator", "admin", "test-password-12345", 401},
		{"robot", f.username, f.secret, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "https://registry.test/auth/token?service=light-registry", nil)
			r.SetBasicAuth(tc.user, tc.password)
			w := httptest.NewRecorder()
			f.server.ServeHTTP(w, r)
			f.status(w, tc.status)
		})
	}
}
