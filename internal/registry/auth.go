package registry

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/distribution/distribution/v3/registry/auth"
	"github.com/distribution/reference"
	"light/internal/requesturl"
	"light/internal/store"
)

type Scope struct {
	Name    string   `json:"name"`
	Actions []string `json:"actions"`
}
type Claims struct {
	Issuer   string  `json:"iss"`
	Audience string  `json:"aud"`
	Subject  string  `json:"sub"`
	Expires  int64   `json:"exp"`
	Issued   int64   `json:"iat"`
	Access   []Scope `json:"access"`
}
type Controller struct {
	Store *store.Store
	key   []byte
}

func init() {
	if err := auth.Register("light", func(options map[string]any) (auth.AccessController, error) {
		c, ok := options["controller"].(*Controller)
		if !ok {
			return nil, errors.New("缺少 light 认证配置")
		}
		return c, nil
	}); err != nil {
		panic(err)
	}
}
func (c *Controller) sign(cl Claims) string {
	raw, _ := json.Marshal(cl)
	head := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	body := head + "." + base64.RawURLEncoding.EncodeToString(raw)
	h := hmac.New(sha256.New, c.key)
	h.Write([]byte(body))
	return body + "." + base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
func (c *Controller) parse(token string) (Claims, error) {
	var cl Claims
	p := strings.Split(token, ".")
	if len(p) != 3 {
		return cl, errors.New("令牌格式错误")
	}
	header, _ := base64.RawURLEncoding.DecodeString(p[0])
	if string(header) != `{"alg":"HS256","typ":"JWT"}` {
		return cl, errors.New("签名算法错误")
	}
	sig, err := base64.RawURLEncoding.DecodeString(p[2])
	if err != nil {
		return cl, err
	}
	h := hmac.New(sha256.New, c.key)
	h.Write([]byte(p[0] + "." + p[1]))
	if !hmac.Equal(sig, h.Sum(nil)) {
		return cl, errors.New("令牌签名错误")
	}
	raw, err := base64.RawURLEncoding.DecodeString(p[1])
	if err != nil {
		return cl, err
	}
	if err = json.Unmarshal(raw, &cl); err != nil {
		return cl, err
	}
	if cl.Issuer != "light" || cl.Audience != "light-registry" || cl.Expires <= time.Now().Unix() || cl.Issued > time.Now().Unix()+30 {
		return cl, errors.New("令牌失效")
	}
	if cl.Subject != "anonymous" && !c.Store.RobotActive(cl.Subject) {
		return cl, errors.New("机器人令牌已失效")
	}
	return cl, nil
}

type challenge struct {
	scopes []auth.Access
}

func (e challenge) Error() string { return "需要镜像仓库认证" }
func (e challenge) SetHeaders(r *http.Request, w http.ResponseWriter) {
	grouped := map[string][]string{}
	for _, a := range e.scopes {
		if a.Type == "repository" {
			grouped[a.Name] = append(grouped[a.Name], a.Action)
		}
	}
	names := []string{}
	for n := range grouped {
		names = append(names, n)
	}
	sort.Strings(names)
	parts := []string{}
	for _, n := range names {
		parts = append(parts, "repository:"+n+":"+strings.Join(grouped[n], ","))
	}
	w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm=%q,service="light-registry",scope=%q`, requesturl.Origin(r)+"/auth/token", strings.Join(parts, " ")))
}
func (c *Controller) Authorized(r *http.Request, access ...auth.Access) (*auth.Grant, error) {
	if IsUploadCancellation(r) {
		normalized := make([]auth.Access, len(access))
		copy(normalized, access)
		for i := range normalized {
			if normalized[i].Action == "delete" {
				normalized[i].Action = "push"
			}
		}
		access = normalized
	}
	deny := challenge{scopes: access}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if token == "" || token == r.Header.Get("Authorization") {
		return nil, deny
	}
	cl, err := c.parse(token)
	if err != nil {
		return nil, deny
	}
	resources := []auth.Resource{}
	for _, a := range access {
		if a.Type != "repository" || a.Action == "delete" {
			return nil, deny
		}
		allowed := false
		for _, s := range cl.Access {
			if s.Name == a.Name {
				for _, v := range s.Actions {
					if v == a.Action {
						allowed = true
					}
				}
			}
		}
		if !allowed {
			return nil, deny
		}
		if cl.Subject == "anonymous" && (a.Action != "pull" || !c.Store.Public(a.Name)) {
			return nil, deny
		}
		resources = append(resources, a.Resource)
	}
	return &auth.Grant{User: auth.UserInfo{Name: cl.Subject}, Resources: resources}, nil
}
func (c *Controller) Token(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		w.WriteHeader(405)
		return
	}
	if len(r.URL.RawQuery) > 8192 {
		w.WriteHeader(400)
		return
	}
	if svc := r.URL.Query().Get("service"); svc != "" && svc != "light-registry" {
		w.WriteHeader(400)
		return
	}
	subject := "anonymous"
	if r.Header.Get("Authorization") != "" {
		user, pass, ok := r.BasicAuth()
		robot, valid := c.Store.AuthenticateRobot(user, pass)
		if !ok || !valid {
			w.Header().Set("WWW-Authenticate", `Basic realm="light"`)
			w.WriteHeader(401)
			json.NewEncoder(w).Encode(map[string]string{"error": "机器人凭据无效或已过期"})
			return
		}
		subject = robot.ID
		c.Store.TouchRobot(robot.ID)
	}
	scopes := []Scope{}
	for _, param := range r.URL.Query()["scope"] {
		for _, scope := range strings.Fields(param) {
			p := strings.Split(scope, ":")
			if len(p) != 3 || p[0] != "repository" {
				w.WriteHeader(400)
				return
			}
			if _, err := reference.WithName(p[1]); err != nil {
				w.WriteHeader(400)
				return
			}
			actions := []string{}
			for _, action := range strings.Split(p[2], ",") {
				if (action == "pull" && (subject != "anonymous" || c.Store.Public(p[1]))) || (action == "push" && subject != "anonymous") {
					actions = append(actions, action)
				}
			}
			scopes = append(scopes, Scope{p[1], actions})
		}
	}
	now := time.Now()
	token := c.sign(Claims{"light", "light-registry", subject, now.Add(5 * time.Minute).Unix(), now.Unix(), scopes})
	json.NewEncoder(w).Encode(map[string]any{"token": token, "access_token": token, "expires_in": 300, "issued_at": now.UTC().Format(time.RFC3339)})
}
