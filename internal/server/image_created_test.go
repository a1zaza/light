package server

import (
	"encoding/json"
	"testing"

	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"light/internal/registry"
)

func TestImageCreatedUsesLatestPlatformConfigAcrossNestedIndexes(t *testing.T) {
	f := newFixture(t)
	name := "created/multi"
	token := f.token(name, true)
	platform := func(tag, arch, created string) v1.Descriptor {
		t.Helper()
		config, _ := json.Marshal(map[string]any{"architecture": arch, "os": "linux", "created": created, "rootfs": map[string]any{"type": "layers", "diff_ids": []string{}}})
		cd := f.blob(name, token, config)
		raw, _ := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": v1.MediaTypeImageManifest, "config": v1.Descriptor{MediaType: v1.MediaTypeImageConfig, Digest: digest.Digest(cd), Size: int64(len(config))}, "layers": []v1.Descriptor{}})
		d := f.manifest(name, tag, token, v1.MediaTypeImageManifest, raw)
		return v1.Descriptor{MediaType: v1.MediaTypeImageManifest, Digest: digest.Digest(d), Size: int64(len(raw)), Platform: &v1.Platform{OS: "linux", Architecture: arch}}
	}
	index := func(tag, media string, children ...v1.Descriptor) v1.Descriptor {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": media, "manifests": children})
		d := f.manifest(name, tag, token, media, raw)
		return v1.Descriptor{MediaType: media, Digest: digest.Digest(d), Size: int64(len(raw))}
	}
	// The lexicographically earlier UTC timestamp represents the later instant.
	amd := platform("amd64", "amd64", "2026-11-12T10:30:00+08:00")
	arm := platform("arm64", "arm64", "2026-11-12T03:00:00.123456789Z")
	missing := platform("missing", "amd64", "")
	invalid := platform("invalid", "arm64", "not-a-time")
	zero := platform("zero", "amd64", "0001-01-01T00:00:00Z")
	oci := index("latest", v1.MediaTypeImageIndex, amd, arm, missing, invalid, zero)
	index("docker-list", "application/vnd.docker.distribution.manifest.list.v2+json", arm, amd)
	index("nested", v1.MediaTypeImageIndex, oci, amd, oci)
	index("empty-time", v1.MediaTypeImageIndex, missing, invalid, zero)
	// A second alias shares the same computed timestamp in list responses.
	raw, _ := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": v1.MediaTypeImageIndex, "manifests": []v1.Descriptor{amd, arm, missing, invalid, zero}})
	f.manifest(name, "stable", token, v1.MediaTypeImageIndex, raw)
	expected := map[string]string{"amd64": "2026-11-12T02:30:00Z", "arm64": "2026-11-12T03:00:00.123456789Z", "latest": "2026-11-12T03:00:00.123456789Z", "stable": "2026-11-12T03:00:00.123456789Z", "nested": "2026-11-12T03:00:00.123456789Z", "docker-list": "2026-11-12T03:00:00.123456789Z", "missing": "", "invalid": "", "zero": "", "empty-time": ""}
	for _, tc := range []struct{ tag, declared, want string }{
		{"declared", "2026-11-11T10:00:00+08:00", "2026-11-11T02:00:00Z"},
		{"invalid-declared", "invalid", "2026-11-12T03:00:00.123456789Z"},
		{"zero-declared", "0001-01-01T00:00:00Z", "2026-11-12T03:00:00.123456789Z"},
	} {
		raw, _ := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": v1.MediaTypeImageIndex, "manifests": []v1.Descriptor{amd, arm}, "annotations": map[string]string{v1.AnnotationCreated: tc.declared}})
		f.manifest(name, tc.tag, token, v1.MediaTypeImageIndex, raw)
		expected[tc.tag] = tc.want
	}
	f.visibility(name, true, 0)
	for _, base := range []string{"admin", "public"} {
		admin := base == "admin"
		for ref, want := range expected {
			w := f.request("GET", "/api/"+base+"/image?name="+name+"&ref="+ref, "", "", admin)
			f.status(w, 200)
			var img registry.Image
			f.decode(w, &img)
			if img.Created != want {
				t.Fatalf("%s/%s created=%q want=%q", base, ref, img.Created, want)
			}
		}
		w := f.request("GET", "/api/"+base+"/repository?name="+name, "", "", admin)
		f.status(w, 200)
		var list struct {
			Items []registry.Image `json:"items"`
		}
		f.decode(w, &list)
		if len(list.Items) != len(expected) {
			t.Fatalf("missing tags in %s list", base)
		}
		for _, img := range list.Items {
			if img.Created != expected[img.Tag] {
				t.Fatalf("%s list tag %s created=%q", base, img.Tag, img.Created)
			}
		}
	}
}
