package server

import (
	"context"
	"fmt"
	"github.com/opencontainers/go-digest"
	"io"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/distribution/distribution/v3/registry/storage"
	driver "github.com/distribution/distribution/v3/registry/storage/driver"
	"light/internal/registry"
	"light/internal/store"
)

func TestPasswordRotationDoesNotUpgradeOldPasswordProof(t *testing.T) {
	replacement, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	if err = replacement.Init("admin", "new-password-12345"); err != nil {
		t.Fatal(err)
	}
	next := replacement.Snapshot().Account
	next.Revision = 2
	for attempt := 0; attempt < 4; attempt++ {
		f := newFixture(t)
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			done <- f.request("POST", "/api/session", `{"username":"admin","password":"test-password-12345"}`, "", false)
		}()
		time.Sleep(5 * time.Millisecond)
		if err = f.store.Change(func(st *store.State) error { st.Account = next; return nil }); err != nil {
			t.Fatal(err)
		}
		response := <-done
		if response.Code == 401 {
			continue
		}
		f.status(response, 200)
		req := httptest.NewRequest("GET", "https://registry.test/api/admin/robots", nil)
		req.AddCookie(response.Result().Cookies()[0])
		out := httptest.NewRecorder()
		f.server.ServeHTTP(out, req)
		f.status(out, 401)
	}
}

func TestPendingUploadSurvivesDeletionAndGC(t *testing.T) {
	f := newFixture(t)
	f.reg.UploadProtectionWindow = registry.DefaultUploadProtectionWindow
	a, b := "protected/source", "protected/pending"
	ta, tb := f.token(a, true), f.token(b, true)
	_, _, layer := f.image(a, "latest", ta, "amd64")
	f.blob(b, tb, []byte("shared layer contents, bytes remain intact"))
	f.deleteTag(a, "latest")
	f.status(f.request("HEAD", "/v2/"+b+"/blobs/"+layer, "", tb, false), 200)
	if err := f.reg.GC(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	f.status(f.request("GET", "/v2/"+b+"/blobs/"+layer, "", tb, false), 200)
	// Publishing the pending image must still succeed after both cleanup paths.
	f.image(b, "complete", tb, "amd64")
}

func TestRecentAnonymousReadDoesNotCreateWriterLease(t *testing.T) {
	f := newFixture(t)
	f.reg.UploadProtectionWindow = registry.DefaultUploadProtectionWindow
	name := "protected/read"
	token := f.token(name, true)
	_, _, layer := f.image(name, "latest", token, "amd64")
	f.visibility(name, true, 0)
	rawBefore, err := os.ReadFile(filepath.Join(f.store.Dir, "jobs", "upload-leases.json"))
	if err != nil {
		t.Fatal(err)
	}
	f.status(f.request("HEAD", "/v2/"+name+"/blobs/"+layer, "", f.token(name, false), false), 200)
	rawAfter, err := os.ReadFile(filepath.Join(f.store.Dir, "jobs", "upload-leases.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(rawBefore) != string(rawAfter) {
		t.Fatal("anonymous pull changed upload leases")
	}
}

type securityCountingDriver struct {
	driver.StorageDriver
	reads int
}

func (d *securityCountingDriver) Reader(ctx context.Context, path string, offset int64) (io.ReadCloser, error) {
	if strings.HasSuffix(path, "/data") {
		d.reads++
	}
	return d.StorageDriver.Reader(ctx, path, offset)
}
func TestPublicRepositoryLoadsOnlyFilteredPageContents(t *testing.T) {
	f := newFixture(t)
	name := "safe/public-page"
	token := f.token(name, true)
	for i := 0; i < 25; i++ {
		f.image(name, fmt.Sprintf("tag-%02d", i), token, fmt.Sprintf("arch-%02d", i))
	}
	f.visibility(name, true, 0)
	counter := &securityCountingDriver{StorageDriver: f.reg.Driver}
	ns, err := storage.NewRegistry(context.Background(), counter, storage.EnableDelete)
	if err != nil {
		t.Fatal(err)
	}
	f.reg.Namespace = ns
	response := f.request("GET", "/api/public/repository?name="+name+"&q=tag-00&page=1", "", "", false)
	f.status(response, 200)
	var body struct {
		Items []registry.Image `json:"items"`
		Total int              `json:"total"`
		Page  int              `json:"page"`
	}
	f.decode(response, &body)
	if len(body.Items) != 1 || body.Total != 1 || counter.reads != 3 {
		t.Fatalf("unexpected filtered page: items=%d total=%d content reads=%d", len(body.Items), body.Total, counter.reads)
	}
	counter.reads = 0
	response = f.request("GET", "/api/public/repository?name="+name+"&page=2", "", "", false)
	f.status(response, 200)
	f.decode(response, &body)
	if len(body.Items) != 5 || body.Total != 25 || body.Page != 2 || counter.reads != 15 {
		t.Fatalf("unexpected second page: items=%d total=%d page=%d reads=%d", len(body.Items), body.Total, body.Page, counter.reads)
	}
	// Digest searches still work, without reading all manifest payloads.
	selected := body.Items[0]
	response = f.request("GET", "/api/public/repository?name="+name+"&q="+selected.Digest, "", "", false)
	f.status(response, 200)
	f.decode(response, &body)
	if body.Total != 1 || body.Items[0].Digest != selected.Digest {
		t.Fatal("digest search changed")
	}
}
func TestPublicQueryHasRateAndConcurrencyBudgets(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 60; i++ {
		f.status(f.request("GET", "/api/public/repositories", "", "", false), 200)
	}
	f.status(f.request("GET", "/api/public/repositories", "", "", false), 429)
	for i := 0; i < cap(f.server.publicSlots); i++ {
		f.server.publicSlots <- struct{}{}
	}
	f.status(f.request("GET", "/api/public/repository?name=none", "", "", false), 429)
	for len(f.server.publicSlots) > 0 {
		<-f.server.publicSlots
	}
	f.status(f.request("GET", "/readyz", "", "", false), 200)
}

func ageServerFixtureBlobs(t *testing.T, f *fixture) {
	t.Helper()
	old := time.Now().Add(-48 * time.Hour)
	if err := f.reg.Driver.Walk(context.Background(), "/docker/registry/v2/blobs", func(info driver.FileInfo) error {
		if !info.IsDir() && strings.HasSuffix(info.Path(), "/data") {
			return os.Chtimes(filepath.Join(f.store.Dir, "registry", strings.TrimPrefix(info.Path(), "/")), old, old)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
func (f *fixture) scopedToken(scopes ...string) string {
	f.t.Helper()
	q := url.Values{"service": {"light-registry"}, "scope": scopes}
	req := httptest.NewRequest("GET", "https://registry.test/auth/token?"+q.Encode(), nil)
	req.SetBasicAuth(f.username, f.secret)
	out := httptest.NewRecorder()
	f.server.ServeHTTP(out, req)
	f.status(out, 200)
	var data struct {
		Token string `json:"token"`
	}
	f.decode(out, &data)
	return data.Token
}
func TestOldBlobWriterHeadAndMountRenewDurableProtection(t *testing.T) {
	for _, mode := range []string{"writer-head", "mount", "pull-only"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			a, b := "protected/old", "protected/new"
			token := f.token(a, true)
			root, _, layer := f.image(a, "latest", token, "amd64")
			ageServerFixtureBlobs(t, f)
			f.reg.UploadProtectionWindow = registry.DefaultUploadProtectionWindow
			switch mode {
			case "writer-head":
				f.status(f.request("HEAD", "/v2/"+a+"/blobs/"+layer, "", token, false), 200)
			case "mount":
				both := f.scopedToken("repository:"+a+":pull", "repository:"+b+":pull,push")
				f.status(f.request("POST", "/v2/"+b+"/blobs/uploads/?mount="+url.QueryEscape(layer)+"&from="+url.QueryEscape(a), "", both, false), 201)
			case "pull-only":
				pull := f.scopedToken("repository:" + a + ":pull")
				f.status(f.request("HEAD", "/v2/"+a+"/blobs/"+layer, "", pull, false), 200)
			}
			f.deleteTag(a, "latest")
			f.globalBlob(root, false)
			f.globalBlob(layer, mode != "pull-only")
			if err := f.reg.GC(context.Background(), false); err != nil {
				t.Fatal(err)
			}
			f.globalBlob(layer, mode != "pull-only")
		})
	}
}
func TestUploadProtectionFailurePreventsBlobCompletion(t *testing.T) {
	f := newFixture(t)
	f.reg.UploadProtectionWindow = registry.DefaultUploadProtectionWindow
	name := "protected/fail"
	token := f.token(name, true)
	raw := []byte("must not publish without a durable lease")
	start := f.request("POST", "/v2/"+name+"/blobs/uploads/", "", token, false)
	f.status(start, 202)
	req := httptest.NewRequest("PATCH", "https://registry.test"+start.Header().Get("Location"), strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Authorization", "Bearer "+token)
	chunk := httptest.NewRecorder()
	f.server.ServeHTTP(chunk, req)
	f.status(chunk, 202)
	ledger := filepath.Join(f.store.Dir, "jobs", "upload-leases.json")
	if err := os.Remove(ledger); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(ledger, 0700); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(chunk.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	d := digest.FromBytes(raw)
	q.Set("digest", d.String())
	u.RawQuery = q.Encode()
	f.status(f.request("PUT", u.String(), "", token, false), 503)
	f.globalBlob(d.String(), false)
}
