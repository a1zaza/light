package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"light/internal/registry"
)

func TestLogicalSizeIncludesAllPlatformsAndDeduplicatesContent(t *testing.T) {
	f := newFixture(t)
	name := "size/multi"
	token := f.token(name, true)
	amd, amdRaw, _ := f.image(name, "amd64", token, "amd64")
	arm, armRaw, _ := f.image(name, "arm64", token, "arm64")
	var amdDoc, armDoc struct {
		Config v1.Descriptor   `json:"config"`
		Layers []v1.Descriptor `json:"layers"`
	}
	if err := json.Unmarshal(amdRaw, &amdDoc); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(armRaw, &armDoc); err != nil {
		t.Fatal(err)
	}
	shared := amdDoc.Layers[0].Size
	wantSingle := int64(len(amdRaw)) + amdDoc.Config.Size + shared
	wantChildren := int64(len(amdRaw)+len(armRaw)) + amdDoc.Config.Size + armDoc.Config.Size + shared
	children := []v1.Descriptor{{MediaType: v1.MediaTypeImageManifest, Digest: digest.Digest(amd), Size: int64(len(amdRaw)), Platform: &v1.Platform{OS: "linux", Architecture: "amd64"}}, {MediaType: v1.MediaTypeImageManifest, Digest: digest.Digest(arm), Size: int64(len(armRaw)), Platform: &v1.Platform{OS: "linux", Architecture: "arm64"}}}
	publishIndex := func(tag, media string, descriptors []v1.Descriptor) (string, []byte) {
		raw, err := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": media, "manifests": descriptors})
		if err != nil {
			t.Fatal(err)
		}
		return f.manifest(name, tag, token, media, raw), raw
	}
	check := func(ref string, want int64) {
		t.Helper()
		w := f.request("GET", "/api/admin/image?name="+name+"&ref="+ref, "", "", true)
		f.status(w, 200)
		var image registry.Image
		f.decode(w, &image)
		if image.Size != want {
			t.Fatalf("%s logical size=%d want=%d", ref, image.Size, want)
		}
	}
	check("amd64", wantSingle)
	ociDigest, ociRaw := publishIndex("latest", v1.MediaTypeImageIndex, children)
	check("latest", int64(len(ociRaw))+wantChildren)
	_, dockerRaw := publishIndex("docker-list", "application/vnd.docker.distribution.manifest.list.v2+json", children)
	check("docker-list", int64(len(dockerRaw))+wantChildren)
	// Nested DAG: amd64 appears both inside the child index and as a direct child.
	nestedChildren := []v1.Descriptor{{MediaType: v1.MediaTypeImageIndex, Digest: digest.Digest(ociDigest), Size: int64(len(ociRaw))}, children[0], children[0]}
	_, nestedRaw := publishIndex("nested", v1.MediaTypeImageIndex, nestedChildren)
	check("nested", int64(len(nestedRaw)+len(ociRaw))+wantChildren)
	f.manifest(name, "stable", token, v1.MediaTypeImageIndex, ociRaw)
	list := f.request("GET", "/api/admin/repository?name="+name, "", "", true)
	f.status(list, 200)
	var response struct {
		Items []registry.Image `json:"items"`
	}
	f.decode(list, &response)
	found := map[string]bool{}
	for _, image := range response.Items {
		if image.Tag == "latest" || image.Tag == "stable" {
			found[image.Tag] = true
			if image.Size != int64(len(ociRaw))+wantChildren {
				t.Fatalf("alias %s has incorrect size: %d", image.Tag, image.Size)
			}
		}
	}
	if len(found) != 2 {
		t.Fatal("alias tags were not retained in list response")
	}
	// The public detail API uses exactly the same size definition.
	f.visibility(name, true, 0)
	public := f.request("GET", "/api/public/image?name="+name+"&ref=latest", "", "", false)
	f.status(public, 200)
	var image registry.Image
	f.decode(public, &image)
	if image.Size != int64(len(ociRaw))+wantChildren {
		t.Fatal("public and admin logical sizes differ")
	}
}

func TestLogicalSizeDoesNotCountRepeatedLayersTwice(t *testing.T) {
	f := newFixture(t)
	name := "size/repeated"
	token := f.token(name, true)
	_, original, _ := f.image(name, "base", token, "amd64")
	var doc map[string]any
	if err := json.Unmarshal(original, &doc); err != nil {
		t.Fatal(err)
	}
	layer := doc["layers"].([]any)[0]
	doc["layers"] = []any{layer, layer}
	raw, _ := json.Marshal(doc)
	f.manifest(name, "repeated", token, v1.MediaTypeImageManifest, raw)
	image, err := f.reg.Image(context.Background(), name, "repeated")
	if err != nil {
		t.Fatal(err)
	}
	want := int64(len(raw)) + int64(doc["config"].(map[string]any)["size"].(float64)) + int64(layer.(map[string]any)["size"].(float64))
	if image.Size != want {
		t.Fatalf("repeated layer counted twice: %d want %d", image.Size, want)
	}
}

func TestLogicalSizeRejectsUnavailableChildInsteadOfReportingPartialSize(t *testing.T) {
	f := newFixture(t)
	name := "size/incomplete"
	token := f.token(name, true)
	child, raw, _ := f.image(name, "amd64", token, "amd64")
	index, _ := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": v1.MediaTypeImageIndex, "manifests": []v1.Descriptor{{MediaType: v1.MediaTypeImageManifest, Digest: digest.Digest(child), Size: int64(len(raw))}}})
	f.manifest(name, "latest", token, v1.MediaTypeImageIndex, index)
	repo, err := f.reg.Repository(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	manifests, err := repo.Manifests(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Simulate corrupt local storage in the temporary fixture, never real data.
	if err = manifests.Delete(context.Background(), digest.Digest(child)); err != nil {
		t.Fatal(err)
	}
	if _, err = f.reg.Image(context.Background(), name, "latest"); err == nil {
		t.Fatal("missing child silently produced a partial logical size")
	}
}
