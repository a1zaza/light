package server

import (
	"errors"
	"light/internal/config"
	"light/internal/hubproxy"
	"light/internal/store"
	"net/http"
)

type settingsResponse struct {
	Settings  config.Settings `json:"settings"`
	Effective config.Settings `json:"effective"`
}

func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, "GET", "PATCH") {
		return
	}
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	if r.Method == "PATCH" {
		var in config.Settings
		if err := decode(w, r, &in); err != nil {
			fail(w, 400, err.Error())
			return
		}
		normalized, err := in.Normalize()
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		if normalized.Revision != s.Store.Settings().Revision {
			fail(w, 409, store.ErrConflict.Error())
			return
		}
		if err = s.Registry.ConfigureHub(hubproxy.FromSettings(normalized, s.Registry.Reserve), func() error { return s.Store.UpdateSettings(normalized) }); err != nil {
			status := 503
			if errors.Is(err, store.ErrConflict) {
				status = 409
			}
			fail(w, status, err.Error())
			return
		}
		active := s.Store.Settings()
		s.activeSettings.Store(&active)
		record(s, r, "修改系统设置", "应用配置已生效")
	}
	saved := s.Store.Settings()
	reply(w, 200, settingsResponse{saved, *s.activeSettings.Load()})
}
