package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	driver "github.com/distribution/distribution/v3/registry/storage/driver"
	"testing"

	distribution "github.com/distribution/distribution/v3"
	"github.com/opencontainers/go-digest"
)

func (f *fixture) deleteTag(name, tag string) {
	f.t.Helper()
	p := f.preview(name, tag)
	body, _ := json.Marshal(map[string]any{"id": p["id"]})
	f.status(f.request("POST", "/api/admin/delete", string(body), "", true), 200)
}
func (f *fixture) globalBlob(raw string, exists bool) {
	f.t.Helper()
	_, err := f.reg.Namespace.BlobStatter().Stat(context.Background(), digest.Digest(raw))
	if exists && err != nil {
		f.t.Fatalf("shared content missing %s: %v", raw, err)
	}
	if !exists && err != distribution.ErrBlobUnknown {
		f.t.Fatalf("content not reclaimed %s: %v", raw, err)
	}
}
func TestTagDeletionReclaimsOnlyUnreferencedContent(t *testing.T) {
	f := newFixture(t)
	name := "delete/single"
	token := f.token(name, true)
	first, raw, layer := f.image(name, "latest", token, "amd64")
	f.manifest(name, "stable", token, "application/vnd.oci.image.manifest.v1+json", raw)
	var doc struct {
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	unrelated := f.blob(name, token, []byte("unrelated upload must survive"))
	f.deleteTag(name, "latest")
	f.status(f.request("GET", "/v2/"+name+"/manifests/latest", "", token, false), 404)
	f.status(f.request("GET", "/v2/"+name+"/manifests/stable", "", token, false), 200)
	f.globalBlob(first, true)
	f.globalBlob(layer, true)
	second, _, _ := f.image(name, "arm64", token, "arm64")
	f.deleteTag(name, "stable")
	f.globalBlob(first, false)
	f.globalBlob(doc.Config.Digest, false)
	f.globalBlob(layer, true)
	f.globalBlob(second, true)
	f.deleteTag(name, "arm64")
	f.globalBlob(second, false)
	f.globalBlob(layer, false)
	f.globalBlob(unrelated, true)
}
func TestTagDeletionPreservesCrossRepositoryLayers(t *testing.T) {
	f := newFixture(t)
	a, b := "delete/one", "delete/two"
	ta, tb := f.token(a, true), f.token(b, true)
	da, _, layer := f.image(a, "latest", ta, "amd64")
	db, _, _ := f.image(b, "latest", tb, "arm64")
	f.deleteTag(a, "latest")
	f.globalBlob(da, false)
	f.globalBlob(layer, true)
	f.status(f.request("GET", "/v2/"+b+"/blobs/"+layer, "", tb, false), 200)
	f.status(f.request("GET", "/v2/"+b+"/manifests/latest", "", tb, false), 200)
	f.deleteTag(b, "latest")
	f.globalBlob(layer, false)
	f.globalBlob(db, false)
}
func TestIndexDeletionReclaimsUntaggedChildrenAndKeepsTaggedPlatform(t *testing.T) {
	f := newFixture(t)
	name := "delete/index"
	token := f.token(name, true)
	a, ra, layer := f.image(name, "amd64", token, "amd64")
	b, rb, _ := f.image(name, "arm64", token, "arm64")
	raw := []byte(fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":%q,"size":%d},{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":%q,"size":%d}]}`, a, len(ra), b, len(rb)))
	index := f.manifest(name, "latest", token, "application/vnd.oci.image.index.v1+json", raw)
	f.deleteTag(name, "amd64")
	f.globalBlob(a, true)
	f.deleteTag(name, "latest")
	f.globalBlob(index, false)
	f.globalBlob(a, false)
	f.globalBlob(b, true)
	f.globalBlob(layer, true)
	f.status(f.request("GET", "/v2/"+name+"/manifests/arm64", "", token, false), 200)
	f.deleteTag(name, "arm64")
	f.globalBlob(layer, false)
}

// Failed physical cleanup must report that tag deletion has already succeeded.
type failedTagCleanupDriver struct{ driver.StorageDriver }

func (d failedTagCleanupDriver) Delete(context.Context, string) error {
	return errors.New("synthetic cleanup failure")
}
func TestTagDeletionReportsPartialCleanupAndRestoresAdmission(t *testing.T) {
	f := newFixture(t)
	name := "delete/failure"
	token := f.token(name, true)
	_, _, layer := f.image(name, "latest", token, "amd64")
	p := f.preview(name, "latest")
	original := f.reg.Driver
	f.reg.Driver = failedTagCleanupDriver{original}
	defer func() { f.reg.Driver = original }()
	body, _ := json.Marshal(map[string]any{"id": p["id"]})
	w := f.request("POST", "/api/admin/delete", string(body), "", true)
	f.status(w, 200)
	var result struct {
		OK      bool   `json:"ok"`
		Warning string `json:"warning"`
	}
	f.decode(w, &result)
	if !result.OK || result.Warning == "" {
		t.Fatal("missing partial cleanup warning")
	}
	f.status(f.request("GET", "/v2/"+name+"/manifests/latest", "", token, false), 404)
	f.globalBlob(layer, true)
	if f.reg.Maintaining() {
		t.Fatal("registry admission remained closed")
	}
}

func TestTagDeletionPreservesUntaggedRepositoryAndRejectsChangedTarget(t *testing.T) {
	f := newFixture(t)
	a, b := "delete/moving", "delete/untagged"
	ta, tb := f.token(a, true), f.token(b, true)
	first, raw, layer := f.image(a, "latest", ta, "amd64")
	var doc struct {
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	// Publish directly by digest: this repository has no tags directory at all.
	source, _ := f.reg.Repository(context.Background(), a)
	config, e := source.Blobs(context.Background()).Get(context.Background(), digest.Digest(doc.Config.Digest))
	if e != nil {
		t.Fatal(e)
	}
	f.blob(b, tb, config)
	f.blob(b, tb, []byte("shared layer contents, bytes remain intact"))
	f.manifest(b, first, tb, "application/vnd.oci.image.manifest.v1+json", raw)
	p := f.preview(a, "latest")
	f.image(a, "latest", ta, "arm64")
	body, _ := json.Marshal(map[string]any{"id": p["id"]})
	f.status(f.request("POST", "/api/admin/delete", string(body), "", true), 409)
	f.deleteTag(a, "latest")
	f.globalBlob(first, true)
	f.globalBlob(layer, true)
	f.status(f.request("GET", "/v2/"+b+"/manifests/"+first, "", tb, false), 200)
}
