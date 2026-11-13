package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	"light/internal/registry"
	"light/internal/store"
)

type fixture struct {
	t        *testing.T
	store    *store.Store
	reg      *registry.Registry
	server   *Server
	cookie   *http.Cookie
	csrf     string
	username string
	secret   string
	robotID  string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = st.Init("admin", "test-password-12345"); err != nil {
		t.Fatal(err)
	}
	reg, err := registry.New(context.Background(), st, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Most lifecycle fixtures model mature content. Upload protection has
	// dedicated regression fixtures using the production 24-hour default.
	reg.UploadProtectionWindow = 0
	f := &fixture{t: t, store: st, reg: reg, server: New(st, reg)}
	t.Cleanup(func() { f.server.Close(); reg.Close(); st.Close() })
	response := f.request("POST", "/api/session", `{"username":"admin","password":"test-password-12345"}`, "", false)
	f.status(response, 200)
	f.cookie = response.Result().Cookies()[0]
	var sess map[string]string
	f.decode(response, &sess)
	f.csrf = sess["csrf"]
	response = f.request("POST", "/api/admin/robots", `{"name":"测试 CI"}`, "", true)
	f.status(response, 201)
	var created struct {
		Robot  store.Robot `json:"robot"`
		Secret string      `json:"secret"`
	}
	f.decode(response, &created)
	f.username = created.Robot.Username
	f.secret = created.Secret
	f.robotID = created.Robot.ID
	return f
}
func (f *fixture) request(method, path, body, token string, admin bool) *httptest.ResponseRecorder {
	f.t.Helper()
	if strings.HasPrefix(path, "https://") {
		u, err := url.Parse(path)
		if err != nil {
			f.t.Fatal(err)
		}
		path = u.RequestURI()
	}
	r := httptest.NewRequest(method, "https://registry.test"+path, strings.NewReader(body))
	r.RemoteAddr = "192.0.2.10:1234"
	r.Header.Set("Origin", "https://registry.test")
	r.Header.Set("Accept", "application/vnd.oci.image.manifest.v1+json, application/vnd.oci.image.index.v1+json")
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if admin {
		r.AddCookie(f.cookie)
		r.Header.Set("X-CSRF-Token", f.csrf)
	}
	w := httptest.NewRecorder()
	f.server.ServeHTTP(w, r)
	return w
}
func (f *fixture) status(w *httptest.ResponseRecorder, want int) {
	f.t.Helper()
	if w.Code != want {
		f.t.Fatalf("status=%d want=%d body=%s", w.Code, want, w.Body.String())
	}
}
func (f *fixture) decode(w *httptest.ResponseRecorder, out any) {
	f.t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
		f.t.Fatal(err)
	}
}
func (f *fixture) token(name string, robot bool) string {
	f.t.Helper()
	r := httptest.NewRequest("GET", "https://registry.test/auth/token?service=light-registry&scope="+url.QueryEscape("repository:"+name+":pull,push"), nil)
	if robot {
		r.SetBasicAuth(f.username, f.secret)
	}
	w := httptest.NewRecorder()
	f.server.ServeHTTP(w, r)
	f.status(w, 200)
	var data map[string]any
	f.decode(w, &data)
	return data["token"].(string)
}
func (f *fixture) blob(name, token string, raw []byte) string {
	f.t.Helper()
	start := f.request("POST", "/v2/"+name+"/blobs/uploads/", "", token, false)
	f.status(start, 202)
	// Exercise an upload across two chunk requests with the signed Location state.
	loc := start.Header().Get("Location")
	if u, err := url.Parse(loc); err != nil || u.IsAbs() || !strings.HasPrefix(loc, "/v2/") {
		f.t.Fatalf("expected relative upload Location, got %q", loc)
	}
	half := len(raw) / 2
	send := func(method, path string, b []byte) *httptest.ResponseRecorder {
		u, _ := url.Parse(path)
		r := httptest.NewRequest(method, "https://registry.test"+u.RequestURI(), bytes.NewReader(b))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/octet-stream")
		w := httptest.NewRecorder()
		f.server.ServeHTTP(w, r)
		return w
	}
	chunk := send("PATCH", loc, raw[:half])
	f.status(chunk, 202)
	loc = chunk.Header().Get("Location")
	chunk = send("PATCH", loc, raw[half:])
	f.status(chunk, 202)
	loc = chunk.Header().Get("Location")
	dgst := digest.FromBytes(raw).String()
	u, _ := url.Parse(loc)
	q := u.Query()
	q.Set("digest", dgst)
	u.RawQuery = q.Encode()
	done := send("PUT", u.String(), nil)
	f.status(done, 201)
	return dgst
}
func (f *fixture) manifest(name, ref, token, media string, raw []byte) string {
	f.t.Helper()
	r := httptest.NewRequest("PUT", "https://registry.test/v2/"+name+"/manifests/"+ref, bytes.NewReader(raw))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", media)
	w := httptest.NewRecorder()
	f.server.ServeHTTP(w, r)
	f.status(w, 201)
	want := digest.FromBytes(raw).String()
	if got := w.Header().Get("Docker-Content-Digest"); got != want {
		f.t.Fatalf("digest %s want %s", got, want)
	}
	return want
}
func (f *fixture) image(name, tag, token, arch string) (string, []byte, string) {
	conf := []byte(fmt.Sprintf(`{"architecture":%q,"os":"linux","rootfs":{"type":"layers","diff_ids":[]}}`, arch))
	layer := []byte("shared layer contents, bytes remain intact")
	cd := f.blob(name, token, conf)
	ld := f.blob(name, token, layer)
	raw := []byte(fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":%q,"size":%d},"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar","digest":%q,"size":%d}]}`, cd, len(conf), ld, len(layer)))
	dgst := f.manifest(name, tag, token, "application/vnd.oci.image.manifest.v1+json", raw)
	return dgst, raw, ld
}
func (f *fixture) visibility(name string, public bool, revision int64) {
	f.t.Helper()
	w := f.request("PATCH", "/api/admin/repository?name="+url.QueryEscape(name), fmt.Sprintf(`{"description":"测试镜像","public":%t,"revision":%d}`, public, revision), "", true)
	f.status(w, 200)
}
func (f *fixture) preview(name, tag string) map[string]any {
	f.t.Helper()
	body, _ := json.Marshal(map[string]string{"name": name, "tag": tag})
	w := f.request("POST", "/api/admin/delete-preview", string(body), "", true)
	f.status(w, 200)
	var p map[string]any
	f.decode(w, &p)
	return p
}

func TestRegistryLifecycleAndAuthorization(t *testing.T) {
	f := newFixture(t)
	name := "personal/app"
	token := f.token(name, true)
	first, raw1, layer := f.image(name, "amd64", token, "amd64")
	second, raw2, _ := f.image(name, "arm64", token, "arm64")
	index := []byte(fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":%q,"size":%d,"platform":{"os":"linux","architecture":"amd64"}},{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":%q,"size":%d,"platform":{"os":"linux","architecture":"arm64"}}]}`, first, len(raw1), second, len(raw2)))
	indexDigest := f.manifest(name, "latest", token, "application/vnd.oci.image.index.v1+json", index)
	f.status(f.request("GET", "/v2/"+name+"/manifests/"+first, "", token, false), 200)
	f.status(f.request("GET", "/v2/"+name+"/manifests/latest", "", f.token(name, false), false), 401)
	f.status(f.request("GET", "/api/public/repository?name="+name, "", "", false), 404)
	f.visibility(name, true, 0)
	anonymous := f.token(name, false)
	f.status(f.request("GET", "/v2/"+name+"/manifests/latest", "", anonymous, false), 200)
	listing := f.request("GET", "/api/public/repositories", "", "", false)
	f.status(listing, 200)
	if !strings.Contains(listing.Body.String(), name) {
		t.Fatal("public repository missing")
	}
	detail := f.request("GET", "/api/public/repository?name="+name, "", "", false)
	f.status(detail, 200)
	if !strings.Contains(detail.Body.String(), "linux/arm64") {
		t.Fatal("multi-platform detail missing")
	}
	f.status(f.request("POST", "/v2/"+name+"/blobs/uploads/", "", anonymous, false), 401)
	f.status(f.request("GET", "/v2/_catalog", "", anonymous, false), 403)
	rangeReq := httptest.NewRequest("GET", "https://registry.test/v2/"+name+"/blobs/"+layer, nil)
	rangeReq.Header.Set("Authorization", "Bearer "+anonymous)
	rangeReq.Header.Set("Range", "bytes=0-5")
	rangeRes := httptest.NewRecorder()
	f.server.ServeHTTP(rangeRes, rangeReq)
	f.status(rangeRes, 206)
	if rangeRes.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("protocol handler overrode no-store cache policy")
	}
	if rangeRes.Body.String() != "shared" {
		t.Fatal(rangeRes.Body.String())
	}
	f.visibility(name, false, 1)
	f.status(f.request("GET", "/v2/"+name+"/manifests/latest", "", anonymous, false), 401)
	f.status(f.request("GET", "/api/public/repository?name="+name, "", "", false), 404)
	// Removing a child tag preserves content referenced by the index.
	childPreview := f.preview(name, "amd64")
	body, _ := json.Marshal(map[string]any{"id": childPreview["id"]})
	f.status(f.request("POST", "/api/admin/delete", string(body), "", true), 200)
	f.status(f.request("GET", "/v2/"+name+"/manifests/"+first, "", token, false), 200)
	old := f.preview(name, "latest")
	f.manifest(name, "stable", token, "application/vnd.oci.image.index.v1+json", index)
	body, _ = json.Marshal(map[string]any{"id": old["id"]})
	f.status(f.request("POST", "/api/admin/delete", string(body), "", true), 409)
	current := f.preview(name, "latest")
	body, _ = json.Marshal(map[string]any{"id": current["id"]})
	f.status(f.request("POST", "/api/admin/delete", string(body), "", true), 200)
	f.status(f.request("GET", "/v2/"+name+"/manifests/latest", "", token, false), 404)
	f.status(f.request("GET", "/v2/"+name+"/manifests/stable", "", token, false), 200)
	f.status(f.request("GET", "/v2/"+name+"/manifests/"+indexDigest, "", token, false), 200)
	// No uploads run during this direct GC call. The CLI additionally acquires the instance lock.
	if err := f.reg.GC(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	f.status(f.request("GET", "/v2/"+name+"/blobs/"+layer, "", token, false), 200)
	f.status(f.request("GET", "/v2/"+name+"/manifests/"+second, "", token, false), 200)
	// Signed access tokens stop working after their parent robot is revoked.
	f.status(f.request("DELETE", "/api/admin/robot?id="+f.robotID, "", "", true), 200)
	f.status(f.request("GET", "/v2/"+name+"/manifests/"+first, "", token, false), 401)
}
func TestSessionAndRobotSecurity(t *testing.T) {
	f := newFixture(t)
	// Admin APIs must not reveal the hash or accept robot authentication.
	w := f.request("GET", "/api/admin/robots", "", "", true)
	f.status(w, 200)
	if strings.Contains(w.Body.String(), `"hash"`) || strings.Contains(w.Body.String(), f.secret) {
		t.Fatal("robot secret leaked")
	}
	token := f.token("another/new-repository", true)
	f.status(f.request("GET", "/api/admin/robots", "", token, false), 401)
	r := httptest.NewRequest("POST", "https://registry.test/api/admin/robots", strings.NewReader(`{"name":"evil"}`))
	r.AddCookie(f.cookie)
	r.Header.Set("X-CSRF-Token", f.csrf)
	r.Header.Set("Origin", "https://evil.test")
	out := httptest.NewRecorder()
	f.server.ServeHTTP(out, r)
	f.status(out, 403)
	r = httptest.NewRequest("POST", "https://registry.test/api/admin/robots", strings.NewReader(`{"name":"a","name":"b"}`))
	r.AddCookie(f.cookie)
	r.Header.Set("Origin", "https://registry.test")
	r.Header.Set("Accept", "application/vnd.oci.image.manifest.v1+json, application/vnd.oci.image.index.v1+json")
	r.Header.Set("X-CSRF-Token", f.csrf)
	out = httptest.NewRecorder()
	f.server.ServeHTTP(out, r)
	f.status(out, 400)
	f.status(f.request("DELETE", "/v2/another/new-repository/manifests/sha256:"+strings.Repeat("a", 64), "", token, false), 403)
	// Invalid credentials must not silently become anonymous credentials.
	r = httptest.NewRequest("GET", "https://registry.test/auth/token", nil)
	r.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(f.username+":wrong")))
	out = httptest.NewRecorder()
	f.server.ServeHTTP(out, r)
	f.status(out, 401)
	// An unscoped login token is valid for /v2/, but cannot pull repository data.
	r = httptest.NewRequest("GET", "https://registry.test/auth/token?service=light-registry", nil)
	r.SetBasicAuth(f.username, f.secret)
	out = httptest.NewRecorder()
	f.server.ServeHTTP(out, r)
	var loginToken map[string]any
	f.decode(out, &loginToken)
	f.status(f.request("GET", "/v2/", "", loginToken["token"].(string), false), 200)
	f.status(f.request("GET", "/v2/another/new-repository/tags/list", "", loginToken["token"].(string), false), 401)
	// Expiration and disable are checked on each subsequent protocol request.
	f.status(f.request("PATCH", "/api/admin/robot?id="+f.robotID, `{"enabled":false}`, "", true), 200)
	f.status(f.request("GET", "/v2/", "", token, false), 401)
	f.status(f.request("PATCH", "/api/admin/robot?id="+f.robotID, `{"enabled":true}`, "", true), 200)
	if err := f.store.Change(func(st *store.State) error {
		past := time.Now().Add(-time.Hour)
		st.Robots[0].ExpiresAt = &past
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f.status(f.request("GET", "/v2/", "", token, false), 401)
	// Password changes invalidate every old admin session.
	f.status(f.request("POST", "/api/admin/password", `{"username":"owner","oldPassword":"test-password-12345","password":"new-password-67890"}`, "", true), 200)
	f.status(f.request("GET", "/api/admin/robots", "", "", true), 401)
}
func TestRestartPreservesImagesAndRobotCredentials(t *testing.T) {
	f := newFixture(t)
	token := f.token("persist/app", true)
	dgst, _, _ := f.image("persist/app", "latest", token, "amd64")
	if _, err := store.Open(f.store.Dir); err == nil {
		t.Fatal("concurrent writer acquired lock")
	}
	f.reg.Close()
	f.store.Close()
	st, err := store.Open(f.store.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	reg, err := registry.New(context.Background(), st, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()
	srv := New(st, reg)
	defer srv.Close()
	req := httptest.NewRequest("GET", "https://registry.test/v2/persist/app/manifests/"+dgst, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.oci.image.manifest.v1+json")
	res := httptest.NewRecorder()
	srv.ServeHTTP(res, req)
	f.status(res, 200)
	raw, _ := io.ReadAll(res.Result().Body)
	if digest.FromBytes(raw).String() != dgst {
		t.Fatal("manifest bytes changed after restart")
	}
	if _, ok := st.AuthenticateRobot(f.username, f.secret); !ok {
		t.Fatal("robot was not persisted")
	}
	req = httptest.NewRequest("GET", "https://registry.test/api/session", nil)
	req.AddCookie(f.cookie)
	res = httptest.NewRecorder()
	srv.ServeHTTP(res, req)
	f.status(res, 401)
}

func TestUploadIntegrityLimitsAndCrossRepositoryAccess(t *testing.T) {
	f := newFixture(t)
	source := "private/source"
	target := "private/target"
	token := f.token(source, true)
	_, _, layer := f.image(source, "latest", token, "amd64")
	targetToken := f.token(target, true)
	// Robot tokens are unrestricted across repositories, including cross-repository mounts.
	req := httptest.NewRequest("GET", "https://registry.test/auth/token?service=light-registry&scope="+url.QueryEscape("repository:"+target+":pull,push")+"&scope="+url.QueryEscape("repository:"+source+":pull"), nil)
	req.SetBasicAuth(f.username, f.secret)
	out := httptest.NewRecorder()
	f.server.ServeHTTP(out, req)
	f.status(out, 200)
	var credentials map[string]any
	f.decode(out, &credentials)
	both := credentials["token"].(string)
	mount := f.request("POST", "/v2/"+target+"/blobs/uploads/?mount="+url.QueryEscape(layer)+"&from="+url.QueryEscape(source), "", both, false)
	f.status(mount, 201)
	f.status(f.request("GET", "/v2/"+target+"/blobs/"+layer, "", targetToken, false), 200)
	// A public layer in source does not authorize the same path in target.
	f.visibility(source, true, 0)
	anon := f.token(source, false)
	f.status(f.request("GET", "/v2/"+target+"/blobs/"+layer, "", anon, false), 401)
	f.status(f.request("POST", "/v2/"+target+"/blobs/uploads/?mount="+url.QueryEscape(layer)+"&from="+url.QueryEscape(source), "", anon, false), 401)
	start := f.request("POST", "/v2/"+target+"/blobs/uploads/", "", targetToken, false)
	f.status(start, 202)
	u, _ := url.Parse(start.Header().Get("Location"))
	q := u.Query()
	badDigest := digest.FromBytes([]byte("different bytes")).String()
	q.Set("digest", badDigest)
	u.RawQuery = q.Encode()
	req = httptest.NewRequest("PUT", "https://registry.test"+u.RequestURI(), strings.NewReader("wrong contents"))
	req.Header.Set("Authorization", "Bearer "+targetToken)
	req.Header.Set("Content-Type", "application/octet-stream")
	out = httptest.NewRecorder()
	f.server.ServeHTTP(out, req)
	f.status(out, 400)
	f.status(f.request("GET", "/v2/"+target+"/blobs/"+badDigest, "", targetToken, false), 404)
	// A size limit is enforced before consuming known-length input.
	f.reg.MaxUploadBytes = 4
	req = httptest.NewRequest("PATCH", "https://registry.test"+start.Header().Get("Location"), strings.NewReader("larger than limit"))
	req.Header.Set("Authorization", "Bearer "+targetToken)
	out = httptest.NewRecorder()
	f.server.ServeHTTP(out, req)
	f.status(out, 413)
	fresh := f.request("POST", "/v2/"+target+"/blobs/uploads/", "", targetToken, false)
	f.status(fresh, 202)
	req = httptest.NewRequest("PATCH", "https://registry.test"+fresh.Header().Get("Location"), strings.NewReader("chunked body exceeds limit"))
	req.ContentLength = -1
	req.Header.Set("Authorization", "Bearer "+targetToken)
	req.Header.Set("Content-Type", "application/octet-stream")
	out = httptest.NewRecorder()
	f.server.ServeHTTP(out, req)
	f.status(out, 413)
}

func TestSubjectReferenceProtectsDeletionAndGC(t *testing.T) {
	f := newFixture(t)
	name := "subject/app"
	token := f.token(name, true)
	subject, _, layer := f.image(name, "latest", token, "amd64")
	conf := []byte(`{}`)
	cd := f.blob(name, token, conf)
	// The selected Distribution version preserves raw subject fields but its References method omits them.
	// The application explicitly inspects that field for safe deletion.
	raw := []byte(fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","artifactType":"application/example","subject":{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":%q,"size":1},"config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":%q,"size":2},"layers":[]}`, subject, cd))
	f.manifest(name, "attestation", token, "application/vnd.oci.image.manifest.v1+json", raw)
	p := f.preview(name, "latest")
	body, _ := json.Marshal(map[string]any{"id": p["id"]})
	f.status(f.request("POST", "/api/admin/delete", string(body), "", true), 200)
	f.status(f.request("GET", "/v2/"+name+"/manifests/"+subject, "", token, false), 200)
	if err := f.reg.GC(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	f.status(f.request("GET", "/v2/"+name+"/blobs/"+layer, "", token, false), 200)
}

func TestUploadCancellationRemainsAvailableWhenDiskIsReserved(t *testing.T) {
	f := newFixture(t)
	name := "cancel/app"
	token := f.token(name, true)
	start := f.request("POST", "/v2/"+name+"/blobs/uploads/", "", token, false)
	f.status(start, 202)
	location := start.Header().Get("Location")
	f.reg.Reserve = ^uint64(0)
	f.status(f.request("DELETE", location, "", token, false), 204)
	f.status(f.request("GET", location, "", token, false), 404)
	// Stored content deletion still requires an administrator and a fresh preview.
	f.status(f.request("DELETE", "/v2/"+name+"/blobs/sha256:"+strings.Repeat("a", 64), "", token, false), 403)
}

func TestRequestBasedOriginsAndAuthenticationRealm(t *testing.T) {
	f := newFixture(t)
	for _, tc := range []struct {
		name, requestURL, origin, proto string
		secure                          bool
	}{
		{"plain-http", "http://192.0.2.30:5080", "http://192.0.2.30:5080", "", false},
		{"another-domain", "https://images.example.org:9443", "https://images.example.org:9443", "", true},
		{"https-proxy", "http://mirror.example.org:8443", "https://mirror.example.org:8443", "https", true},
		{"default-port", "https://registry.example.org:443", "https://registry.example.org", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", tc.requestURL+"/api/session", strings.NewReader(`{"username":"admin","password":"test-password-12345"}`))
			req.Header.Set("Origin", tc.origin)
			req.Header.Set("X-Forwarded-Proto", tc.proto)
			req.Header.Set("X-Forwarded-Host", "untrusted.example")
			out := httptest.NewRecorder()
			f.server.ServeHTTP(out, req)
			f.status(out, 200)
			cookies := out.Result().Cookies()
			if len(cookies) != 1 || cookies[0].Secure != tc.secure {
				t.Fatalf("unexpected session cookie security: %#v", cookies)
			}
			req = httptest.NewRequest("GET", tc.requestURL+"/v2/", nil)
			req.Header.Set("X-Forwarded-Proto", tc.proto)
			req.Header.Set("X-Forwarded-Host", "untrusted.example")
			out = httptest.NewRecorder()
			f.server.ServeHTTP(out, req)
			f.status(out, 401)
			want := tc.requestURL
			if tc.proto == "https" {
				want = strings.Replace(want, "http://", "https://", 1)
			}
			realm := out.Header().Get("WWW-Authenticate")
			if !strings.Contains(realm, `realm="`+want+`/auth/token"`) || strings.Contains(realm, "untrusted.example") {
				t.Fatalf("unexpected authentication realm: %s", realm)
			}
		})
	}
	// Origin protection uses the actual Host and does not trust forwarded hostname overrides.
	req := httptest.NewRequest("POST", "http://mirror.example.org/api/session", strings.NewReader(`{"username":"admin","password":"test-password-12345"}`))
	req.Header.Set("Origin", "https://untrusted.example")
	req.Header.Set("X-Forwarded-Host", "untrusted.example")
	req.Header.Set("X-Forwarded-Proto", "https")
	out := httptest.NewRecorder()
	f.server.ServeHTTP(out, req)
	f.status(out, 403)
}

func TestRepositoryNameIsImmutableAndPullAddressRemainsValid(t *testing.T) {
	f := newFixture(t)
	name := "immutable/team/app"
	renamed := "immutable/team/renamed"
	token := f.token(name, true)
	dgst, _, _ := f.image(name, "latest", token, "amd64")
	update := f.request("PATCH", "/api/admin/repository?name="+name, `{"description":"更新说明","public":false,"revision":0}`, "", true)
	f.status(update, 200)
	rename := f.request("PATCH", "/api/admin/repository?name="+name, `{"name":"`+renamed+`","description":"尝试改名","public":false,"revision":1}`, "", true)
	f.status(rename, 400)
	metadata := f.store.Snapshot().Repositories[name]
	if metadata.Name != name || metadata.Description != "更新说明" || metadata.Revision != 1 {
		t.Fatalf("rejected rename modified metadata: %#v", metadata)
	}
	if _, exists := f.store.Snapshot().Repositories[renamed]; exists {
		t.Fatal("rename created metadata under a new name")
	}
	existing := f.request("GET", "/v2/"+name+"/manifests/latest", "", token, false)
	f.status(existing, 200)
	if existing.Header().Get("Docker-Content-Digest") != dgst {
		t.Fatal("original pull reference changed")
	}
	f.status(f.request("GET", "/v2/"+renamed+"/manifests/latest", "", f.token(renamed, true), false), 404)
}
