// Package config defines persisted application settings, independent of image data.
package config

import (
	"errors"
	"net/http"

	"golang.org/x/net/http/httpguts"
)

type Settings struct {
	Revision               int64  `json:"revision"`
	RateLimitIPHeader      string `json:"rateLimitIPHeader"`
	HubEnabled             bool   `json:"hubEnabled"`
	HubCacheMaxBytes       int64  `json:"hubCacheMaxBytes"`
	HubMaxBlobBytes        int64  `json:"hubMaxBlobBytes"`
	HubFetchConcurrency    int    `json:"hubFetchConcurrency"`
	HubFetchTimeoutSeconds int64  `json:"hubFetchTimeoutSeconds"`
}

func Default() Settings {
	return Settings{Revision: 1, HubCacheMaxBytes: 10 << 30, HubMaxBlobBytes: 20 << 30, HubFetchConcurrency: 4, HubFetchTimeoutSeconds: 1800}
}
func Header(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if len(value) > 128 || !httpguts.ValidHeaderFieldName(value) {
		return "", errors.New("限流 IP Header 名称不合法，最长 128 字节")
	}
	return http.CanonicalHeaderKey(value), nil
}
func (s Settings) Normalize() (Settings, error) {
	header, err := Header(s.RateLimitIPHeader)
	if err != nil {
		return s, err
	}
	s.RateLimitIPHeader = header
	if s.Revision < 1 {
		return s, errors.New("设置版本无效")
	}
	// Keep all byte values exactly representable by the browser's integer type.
	if s.HubCacheMaxBytes < 1 || s.HubCacheMaxBytes > 1<<50 {
		return s, errors.New("代理缓存容量须为 1 字节–1 PiB")
	}
	if s.HubMaxBlobBytes < 1 || s.HubMaxBlobBytes > 20<<30 {
		return s, errors.New("单 blob 上限须为 1 字节–20 GiB")
	}
	if s.HubFetchConcurrency < 1 || s.HubFetchConcurrency > 32 {
		return s, errors.New("回源并发须为 1–32")
	}
	if s.HubFetchTimeoutSeconds < 1 || s.HubFetchTimeoutSeconds > 3600 {
		return s, errors.New("回源超时须为 1–3600 秒")
	}
	return s, nil
}
