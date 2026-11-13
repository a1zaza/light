package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/distribution/reference"
	"light/internal/config"
	"light/internal/jsonfile"
	"light/internal/maintenance"
	"light/internal/registry"
	"light/internal/requesturl"
	"light/internal/store"
	"light/web"
)

const cookieName = "light_session"

type session struct {
	Username string
	CSRF     string
	Revision int64
	Expires  time.Time
}
type preview struct {
	Value   registry.DeletePreview
	Session string
	Expires time.Time
}
type limit struct {
	Start time.Time
	Count int
}
type Server struct {
	Maintenance    *maintenance.Manager
	Store          *store.Store
	Registry       *registry.Registry
	mux            *http.ServeMux
	mu             sync.Mutex
	sessions       map[string]session
	previews       map[string]preview
	limits         map[string]limit
	authSlots      chan struct{}
	publicSlots    chan struct{}
	settingsMu     sync.Mutex
	activeSettings atomic.Pointer[config.Settings]
}

func New(st *store.Store, reg *registry.Registry) *Server {
	s := &Server{Maintenance: maintenance.New(st, reg), Store: st, Registry: reg, mux: http.NewServeMux(), sessions: map[string]session{}, previews: map[string]preview{}, limits: map[string]limit{}, authSlots: make(chan struct{}, 8), publicSlots: make(chan struct{}, 4)}
	active := st.Settings()
	s.activeSettings.Store(&active)
	s.mux.HandleFunc("/api/session", s.sessionHandler)
	s.mux.HandleFunc("/api/admin/overview", s.admin(s.overview))
	s.mux.HandleFunc("/api/admin/repositories", s.admin(s.repositories))
	s.mux.HandleFunc("/api/admin/repository", s.admin(s.repository))
	s.mux.HandleFunc("/api/admin/image", s.admin(s.image))
	s.mux.HandleFunc("/api/admin/robots", s.admin(s.robots))
	s.mux.HandleFunc("/api/admin/robot", s.admin(s.robot))
	s.mux.HandleFunc("/api/admin/delete-preview", s.admin(s.deletePreview))
	s.mux.HandleFunc("/api/admin/delete", s.admin(s.deleteImage))
	s.mux.HandleFunc("/api/admin/audit", s.admin(s.audit))
	s.mux.HandleFunc("/api/admin/password", s.admin(s.password))
	s.mux.HandleFunc("/api/admin/settings", s.admin(s.settings))
	s.mux.HandleFunc("/api/admin/maintenance", s.admin(s.maintenance))
	s.mux.HandleFunc("/api/admin/hub-cache", s.admin(s.hubCache))
	reg.ClientIP = s.clientIP
	s.mux.HandleFunc("/api/public/repositories", s.public(s.repositories))
	s.mux.HandleFunc("/api/public/repository", s.public(s.repository))
	s.mux.HandleFunc("/api/public/image", s.public(s.image))
	s.mux.HandleFunc("/auth/token", func(w http.ResponseWriter, r *http.Request) {
		if !s.allow(r, 120) {
			fail(w, 429, "认证请求过于频繁，请稍后重试")
			return
		}
		if s.Registry.Maintaining() {
			w.Header().Set("Retry-After", "5")
			fail(w, 503, "存储维护中，请稍后重试")
			return
		}
		s.Registry.Gate.RLock()
		defer s.Registry.Gate.RUnlock()
		if !s.Store.Ready() {
			fail(w, 503, "实例尚未就绪")
			return
		}
		s.Registry.Auth.Token(w, r)
	})
	s.mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			fail(w, 405, "方法不支持")
			return
		}
		reply(w, 200, map[string]string{"status": "ok"})
	})
	s.mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if !method(w, r, "GET", "HEAD") {
			return
		}
		if !s.Store.Ready() {
			fail(w, 503, "尚未就绪")
			return
		}
		reply(w, 200, map[string]string{"status": "ready"})
	})
	s.mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { fail(w, 404, "接口不存在") })
	s.mux.Handle("/", web.Handler())
	return s
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
	if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/auth/token" {
		w.Header().Set("Cache-Control", "no-store")
		_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(15 * time.Second))
		defer http.NewResponseController(w).SetReadDeadline(time.Time{})
	}
	if registry.IsRegistryPath(r.URL.Path) {
		s.Registry.ServeHTTP(w, r)
		return
	}
	s.mux.ServeHTTP(w, r)
}
func reply(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
func fail(w http.ResponseWriter, status int, message string) {
	reply(w, status, map[string]string{"error": message})
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return errors.New("请求正文超过限制或读取失败")
	}
	return jsonfile.DecodeLimit(raw, v, 64<<10)
}
func method(w http.ResponseWriter, r *http.Request, methods ...string) bool {
	for _, m := range methods {
		if r.Method == m {
			return true
		}
	}
	w.Header().Set("Allow", strings.Join(methods, ", "))
	fail(w, 405, "请求方法不支持")
	return false
}
func ip(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
func (s *Server) allow(r *http.Request, max int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for k, v := range s.limits {
		if now.Sub(v.Start) > time.Minute {
			delete(s.limits, k)
		}
	}
	key := s.clientIP(r) + r.URL.Path
	v := s.limits[key]
	if v.Start.IsZero() {
		if len(s.limits) >= 4096 {
			return false
		}
		v.Start = now
	}
	v.Count++
	s.limits[key] = v
	return v.Count <= max
}
func (s *Server) getSession(r *http.Request) (string, session, bool) {
	cookie, err := r.Cookie(cookieName)
	if err != nil {
		return "", session{}, false
	}
	key := store.Hash(cookie.Value)
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.sessions[key]
	a := s.Store.Snapshot().Account
	if !ok || a == nil || v.Revision != a.Revision || !v.Expires.After(time.Now()) {
		delete(s.sessions, key)
		return "", session{}, false
	}
	return key, v, true
}

func (s *Server) sessionHandler(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, "GET", "POST", "DELETE") {
		return
	}
	if r.Method == "GET" {
		_, sess, ok := s.getSession(r)
		if !ok {
			fail(w, 401, "请先登录")
			return
		}
		reply(w, 200, map[string]string{"username": sess.Username, "csrf": sess.CSRF})
		return
	}
	if !requesturl.SameOrigin(r) {
		fail(w, 403, "请求来源不受信任")
		return
	}
	if r.Method == "DELETE" {
		key, sess, ok := s.getSession(r)
		if !ok || r.Header.Get("X-CSRF-Token") != sess.CSRF {
			fail(w, 403, "会话校验失败")
			return
		}
		s.mu.Lock()
		delete(s.sessions, key)
		s.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: requesturl.Scheme(r) == "https", SameSite: http.SameSiteStrictMode})
		s.Store.Record(sess.Username, "退出登录", "", s.clientIP(r))
		reply(w, 200, map[string]bool{"ok": true})
		return
	}
	if !s.allow(r, 12) {
		fail(w, 429, "登录尝试过多，请稍后重试")
		return
	}
	select {
	case s.authSlots <- struct{}{}:
		defer func() { <-s.authSlots }()
	default:
		fail(w, 429, "登录请求繁忙")
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decode(w, r, &input); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if !s.Store.Ready() {
		fail(w, 503, "实例尚未就绪")
		return
	}
	if len(input.Username) > 64 || len(input.Password) > 256 {
		fail(w, 401, "账号或密码不正确")
		return
	}
	account, authenticated := s.Store.AuthenticateAccount(input.Username, input.Password)
	if !authenticated {
		s.Store.Record(input.Username, "登录失败", "", s.clientIP(r))
		fail(w, 401, "账号或密码不正确")
		return
	}
	token := store.Random(32)
	sess := session{account.Username, store.Random(24), account.Revision, time.Now().Add(12 * time.Hour)}
	s.mu.Lock()
	for k, v := range s.sessions {
		if !v.Expires.After(time.Now()) {
			delete(s.sessions, k)
		}
	}
	if len(s.sessions) >= 256 {
		s.mu.Unlock()
		fail(w, 429, "会话数量已达上限")
		return
	}
	s.sessions[store.Hash(token)] = sess
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: token, Path: "/", MaxAge: 43200, HttpOnly: true, Secure: requesturl.Scheme(r) == "https", SameSite: http.SameSiteStrictMode})
	s.Store.Record(input.Username, "登录成功", "", s.clientIP(r))
	reply(w, 200, map[string]string{"username": sess.Username, "csrf": sess.CSRF})
}

type contextKey string

const actorKey contextKey = "actor"

func (s *Server) admin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, sess, ok := s.getSession(r)
		if !ok {
			fail(w, 401, "请先登录")
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			if !requesturl.SameOrigin(r) || r.Header.Get("X-CSRF-Token") != sess.CSRF {
				fail(w, 403, "请求来源或 CSRF 校验失败")
				return
			}
			if !s.Store.Ready() {
				fail(w, 503, "持久化状态待确认，暂停写入")
				return
			}
		}
		if s.Registry.Maintaining() {
			switch r.URL.Path {
			case "/api/admin/repositories", "/api/admin/repository", "/api/admin/image", "/api/admin/delete-preview", "/api/admin/delete", "/api/admin/overview":
				w.Header().Set("Retry-After", "5")
				fail(w, 503, "存储维护中，请稍后重试")
				return
			}
		}
		next(w, r.WithContext(context.WithValue(r.Context(), actorKey, sess.Username)))
	}
}
func (s *Server) public(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.Registry.Maintaining() {
			w.Header().Set("Retry-After", "5")
			fail(w, 503, "存储维护中，请稍后重试")
			return
		}
		if !s.Store.Ready() {
			fail(w, 503, "实例尚未就绪")
			return
		}
		if !method(w, r, "GET") {
			return
		}
		if !s.allow(r, 60) {
			fail(w, 429, "公开查询过于频繁，请稍后重试")
			return
		}
		select {
		case s.publicSlots <- struct{}{}:
			defer func() { <-s.publicSlots }()
		default:
			fail(w, 429, "公开查询繁忙，请稍后重试")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		s.Registry.Gate.RLock()
		defer s.Registry.Gate.RUnlock()
		next(w, r.WithContext(ctx))
	}
}
func publicRequest(r *http.Request) bool { return strings.HasPrefix(r.URL.Path, "/api/public/") }
func record(s *Server, r *http.Request, action, target string) {
	actor, _ := r.Context().Value(actorKey).(string)
	s.Store.Record(actor, action, target, s.clientIP(r))
}
func page(r *http.Request, length int) (start, end, p int) {
	p, _ = strconv.Atoi(r.URL.Query().Get("page"))
	if p < 1 {
		p = 1
	}
	maxPage := max(1, (length+19)/20)
	if p > maxPage {
		p = maxPage
	}
	start = (p - 1) * 20
	if start > length {
		start = length
	}
	end = min(start+20, length)
	return
}
func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, "GET") {
		return
	}
	s.Registry.Gate.RLock()
	defer s.Registry.Gate.RUnlock()
	list, err := s.Registry.List(r.Context(), false)
	if err != nil {
		fail(w, 500, "读取仓库失败")
		return
	}
	used, free, total, err := s.Registry.Disk()
	if err != nil {
		fail(w, 500, "读取存储空间失败")
		return
	}
	tags, pub := 0, 0
	for _, repo := range list {
		tags += repo.Tags
		if repo.Public {
			pub++
		}
	}
	robots := s.Store.Snapshot().Robots
	reply(w, 200, map[string]any{"repositories": len(list), "tags": tags, "public": pub, "robots": len(robots), "used": used, "free": free, "total": total, "reserve": s.Registry.Reserve, "auditError": s.Store.AuditError(), "version": "0.1.0", "ready": s.Store.Ready()})
}
func (s *Server) repositories(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, "GET") {
		return
	}
	if !publicRequest(r) {
		s.Registry.Gate.RLock()
		defer s.Registry.Gate.RUnlock()
	}
	list, err := s.Registry.List(r.Context(), publicRequest(r))
	if err != nil {
		fail(w, 500, "读取仓库失败")
		return
	}
	q := strings.ToLower(r.URL.Query().Get("q"))
	visibility := r.URL.Query().Get("visibility")
	filtered := []registry.Summary{}
	for _, repo := range list {
		if q != "" && !strings.Contains(strings.ToLower(repo.Name+" "+repo.Description), q) {
			continue
		}
		if visibility == "public" && !repo.Public || visibility == "private" && repo.Public {
			continue
		}
		filtered = append(filtered, repo)
	}
	start, end, p := page(r, len(filtered))
	reply(w, 200, map[string]any{"items": filtered[start:end], "total": len(filtered), "page": p})
}
func (s *Server) checkRepository(w http.ResponseWriter, r *http.Request) (string, bool) {
	name := r.URL.Query().Get("name")
	if _, err := reference.WithName(name); err != nil {
		fail(w, 400, "仓库名称不合法")
		return "", false
	}
	if publicRequest(r) && !s.Store.Public(name) {
		fail(w, 404, "仓库不存在")
		return "", false
	}
	return name, true
}
func (s *Server) repository(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, "GET", "PATCH") {
		return
	}
	name, ok := s.checkRepository(w, r)
	if !ok {
		return
	}
	if r.Method == "PATCH" {
		s.Registry.Gate.Lock()
		defer s.Registry.Gate.Unlock()
		var in struct {
			Description string `json:"description"`
			Public      bool   `json:"public"`
			Revision    int64  `json:"revision"`
		}
		if err := decode(w, r, &in); err != nil || len(in.Description) > 2000 {
			fail(w, 400, "仓库说明最多 2000 字节，请检查输入")
			return
		}
		names, err := s.Registry.Names(r.Context())
		if err != nil {
			fail(w, 500, "读取仓库失败")
			return
		}
		exists := false
		for _, n := range names {
			if n == name {
				exists = true
			}
		}
		if !exists {
			fail(w, 404, "仓库不存在")
			return
		}
		err = s.Store.Change(func(st *store.State) error {
			old := st.Repositories[name]
			if old.Revision != in.Revision {
				return store.ErrConflict
			}
			st.Repositories[name] = store.Repository{Name: name, Description: in.Description, Public: in.Public, Revision: old.Revision + 1, UpdatedAt: time.Now().UTC()}
			return nil
		})
		if err != nil {
			fail(w, 409, err.Error())
			return
		}
		record(s, r, "更新仓库", name)
		reply(w, 200, s.Store.Snapshot().Repositories[name])
		return
	}
	if !publicRequest(r) {
		s.Registry.Gate.RLock()
		defer s.Registry.Gate.RUnlock()
	}
	tags, err := s.Registry.ImageTags(r.Context(), name, r.URL.Query().Get("q"))
	if err != nil {
		fail(w, 404, "仓库不存在或标签暂不可用")
		return
	}
	start, end, p := page(r, len(tags))
	images, err := s.Registry.ImagesForTags(r.Context(), name, tags[start:end])
	if err != nil {
		fail(w, 404, "仓库不存在或标签暂不可用")
		return
	}
	meta := s.Store.Snapshot().Repositories[name]
	meta.Name = name
	reply(w, 200, map[string]any{"repository": meta, "items": images, "total": len(tags), "page": p})
}
func (s *Server) image(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, "GET") {
		return
	}
	name, ok := s.checkRepository(w, r)
	if !ok {
		return
	}
	if !publicRequest(r) {
		s.Registry.Gate.RLock()
		defer s.Registry.Gate.RUnlock()
	}
	img, err := s.Registry.Image(r.Context(), name, r.URL.Query().Get("ref"))
	if err != nil {
		fail(w, 404, "镜像不存在")
		return
	}
	reply(w, 200, img)
}
func safeRobots(robots []store.Robot) []store.Robot {
	for i := range robots {
		robots[i].Hash = ""
	}
	return robots
}
func (s *Server) robots(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, "GET", "POST") {
		return
	}
	if r.Method == "GET" {
		reply(w, 200, map[string]any{"items": safeRobots(s.Store.Snapshot().Robots)})
		return
	}
	var in struct {
		Name      string     `json:"name"`
		ExpiresAt *time.Time `json:"expiresAt"`
	}
	if err := decode(w, r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if len(in.Name) == 0 || len(in.Name) > 128 || in.ExpiresAt != nil && !in.ExpiresAt.After(time.Now()) {
		fail(w, 400, "请输入机器人名称及有效的到期时间")
		return
	}
	id := store.Random(9)
	secret := "light_" + store.Random(32)
	robot := store.Robot{ID: id, Name: in.Name, Username: "robot-" + id, Hash: store.Hash(secret), Enabled: true, CreatedAt: time.Now().UTC(), ExpiresAt: in.ExpiresAt}
	if err := s.Store.Change(func(st *store.State) error {
		if len(st.Robots) >= 1024 {
			return errors.New("机器人数量已达 1024 上限")
		}
		st.Robots = append(st.Robots, robot)
		return nil
	}); err != nil {
		fail(w, 409, err.Error())
		return
	}
	record(s, r, "创建机器人", robot.Name+" ("+robot.ID+")")
	robot.Hash = ""
	reply(w, 201, map[string]any{"robot": robot, "secret": secret})
}
func (s *Server) robot(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, "PATCH", "DELETE") {
		return
	}
	id := r.URL.Query().Get("id")
	var in struct {
		Enabled bool `json:"enabled"`
	}
	if r.Method == "PATCH" {
		if err := decode(w, r, &in); err != nil {
			fail(w, 400, err.Error())
			return
		}
	}
	target := ""
	err := s.Store.Change(func(st *store.State) error {
		for i, robot := range st.Robots {
			if robot.ID == id {
				target = robot.Name + " (" + id + ")"
				if r.Method == "DELETE" {
					st.Robots = append(st.Robots[:i], st.Robots[i+1:]...)
				} else {
					st.Robots[i].Enabled = in.Enabled
				}
				return nil
			}
		}
		return errors.New("机器人不存在")
	})
	if err != nil {
		fail(w, 409, err.Error())
		return
	}
	action := "更新机器人状态"
	if r.Method == "DELETE" {
		action = "撤销机器人"
	}
	record(s, r, action, target)
	reply(w, 200, map[string]bool{"ok": true})
}
func (s *Server) deletePreview(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, "POST") {
		return
	}
	var in struct {
		Name string `json:"name"`
		Tag  string `json:"tag"`
	}
	if err := decode(w, r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	unlock, lockErr := s.Registry.LockDeletion(r.Context())
	if lockErr != nil {
		fail(w, 409, lockErr.Error())
		return
	}
	defer unlock()
	p, err := s.Registry.Preview(r.Context(), in.Name, in.Tag)
	if err != nil {
		fail(w, 400, "无法读取镜像引用："+err.Error())
		return
	}
	key, _, _ := s.getSession(r)
	id := store.Random(18)
	s.mu.Lock()
	for k, v := range s.previews {
		if !v.Expires.After(time.Now()) {
			delete(s.previews, k)
		}
	}
	if len(s.previews) >= 512 {
		s.mu.Unlock()
		fail(w, 429, "删除预览数量过多")
		return
	}
	s.previews[id] = preview{p, key, time.Now().Add(5 * time.Minute)}
	s.mu.Unlock()
	reply(w, 200, map[string]any{"id": id, "preview": p})
}
func (s *Server) deleteImage(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, "POST") {
		return
	}
	var in struct {
		ID string `json:"id"`
	}
	if err := decode(w, r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	key, _, _ := s.getSession(r)
	s.mu.Lock()
	p, ok := s.previews[in.ID]
	delete(s.previews, in.ID)
	s.mu.Unlock()
	if !ok || p.Session != key || !p.Expires.After(time.Now()) {
		fail(w, 409, "预览已失效，请重新预览")
		return
	}
	unlock, lockErr := s.Registry.LockDeletion(r.Context())
	if lockErr != nil {
		fail(w, 409, lockErr.Error())
		return
	}
	defer unlock()
	now, err := s.Registry.Preview(r.Context(), p.Value.Name, p.Value.Tag)
	if err != nil || now.Fingerprint != p.Value.Fingerprint {
		fail(w, 409, "镜像引用已变化，请重新预览")
		return
	}
	deleted, err := s.Registry.Delete(r.Context(), now)
	if err != nil {
		record(s, r, "删除标签清理失败", now.Name+":"+now.Tag)
		if deleted {
			reply(w, 200, map[string]any{"ok": true, "warning": "标签已删除，部分存储清理失败，可稍后通过存储维护回收"})
			return
		}
		fail(w, 409, err.Error())
		return
	}
	record(s, r, "删除标签", now.Name+":"+now.Tag)
	reply(w, 200, map[string]bool{"ok": true})
}
func (s *Server) audit(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, "GET") {
		return
	}
	items, err := s.Store.Audits()
	if err != nil {
		fail(w, 500, "读取审计失败")
		return
	}
	start, end, p := page(r, len(items))
	reply(w, 200, map[string]any{"items": items[start:end], "total": len(items), "page": p})
}
func (s *Server) password(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, "POST") {
		return
	}
	if !s.allow(r, 6) {
		fail(w, 429, "修改尝试过多")
		return
	}
	var in struct {
		Username    string `json:"username"`
		OldPassword string `json:"oldPassword"`
		Password    string `json:"password"`
	}
	if err := decode(w, r, &in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if len(in.OldPassword) > 256 {
		fail(w, 400, "原密码过长")
		return
	}
	if err := s.Store.Password(in.Username, in.OldPassword, in.Password); err != nil {
		fail(w, 400, err.Error())
		return
	}
	record(s, r, "修改管理员账户", in.Username)
	reply(w, 200, map[string]bool{"ok": true})
}
func (s *Server) maintenance(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, "GET", "POST") {
		return
	}
	if r.Method == "POST" {
		var in struct {
			Action         string `json:"action"`
			PreviewID      string `json:"previewId"`
			OlderThanHours int    `json:"olderThanHours"`
		}
		if err := decode(w, r, &in); err != nil {
			fail(w, 400, err.Error())
			return
		}
		kind, mode := "", ""
		switch in.Action {
		case "reindex":
			kind, mode = "reindex", "execute"
		case "gc-preview":
			kind, mode = "gc", "preview"
		case "gc-execute":
			kind, mode = "gc", "execute"
		case "uploads-preview":
			kind, mode = "uploads", "preview"
		case "uploads-execute":
			kind, mode = "uploads", "execute"
		default:
			fail(w, 400, "不支持的维护操作")
			return
		}
		if in.OlderThanHours == 0 {
			in.OlderThanHours = 168
		}
		actor, _ := r.Context().Value(actorKey).(string)
		job, err := s.Maintenance.Start(kind, mode, in.PreviewID, in.OlderThanHours, actor, s.clientIP(r))
		if err != nil {
			fail(w, 409, err.Error())
			return
		}
		reply(w, 202, map[string]any{"job": job})
		return
	}
	at, indexError := s.Registry.CatalogStatus()
	state := s.Maintenance.Snapshot()
	// Preserve visibility into CLI maintenance performed with the same data volume.
	legacy := map[string]any{}
	for key, name := range map[string]string{"gc": "last-gc.json", "uploads": "last-upload-cleanup.json"} {
		var job map[string]any
		err := jsonfile.Read(s.Store.Dir+"/jobs/"+name, &job)
		if err == nil {
			if job["status"] == "running" {
				job["status"] = "interrupted"
			}
			legacy[key] = job
		}
	}
	var hub any
	if cache := s.Registry.HubCache(); cache != nil {
		hub = cache.Status()
	}
	reply(w, 200, map[string]any{"active": state.Active, "jobs": state.Jobs, "error": state.Error, "catalogAt": at, "catalogError": indexError, "legacy": legacy, "hub": hub})
}
func (s *Server) Close() { s.Maintenance.Close() }

const ListenAddress = ":5080"

func HTTPServer(handler http.Handler) *http.Server {
	return &http.Server{Addr: ListenAddress, Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 32 << 10}
}
