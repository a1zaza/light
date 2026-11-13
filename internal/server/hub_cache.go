package server

import (
	"light/internal/hubproxy"
	"net/http"
)

func (s *Server) hubCache(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, "GET", "POST") {
		return
	}
	cache := s.Registry.HubCache()
	if cache == nil {
		if r.Method == "POST" {
			fail(w, 409, "Docker Hub 代理未启用")
			return
		}
		reply(w, 200, hubproxy.Status{Entries: []hubproxy.Entry{}})
		return
	}
	if r.Method == "POST" {
		var in struct {
			Action string `json:"action"`
		}
		if err := decode(w, r, &in); err != nil {
			fail(w, 400, err.Error())
			return
		}
		if in.Action != "expired" && in.Action != "all" {
			fail(w, 400, "不支持的缓存操作")
			return
		}
		// This endpoint has a bounded body and requires the existing admin session,
		// same-origin and CSRF checks. Cleanup is separate from native GC.
		actor, _ := r.Context().Value(actorKey).(string)
		clientIP := s.clientIP(r)
		if err := cache.StartClean(in.Action == "all", func(err error) {
			result := in.Action
			if err != nil {
				result += "：失败"
			}
			s.Store.Record(actor, "完成 Docker Hub 缓存清理", result, clientIP)
		}); err != nil {
			fail(w, 503, err.Error())
			return
		}
		s.Store.Record(actor, "清理 Docker Hub 缓存", in.Action, s.clientIP(r))
		reply(w, 202, cache.Status())
		return
	}
	reply(w, 200, cache.Status())
}
