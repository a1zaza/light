// Package hubproxy implements an authenticated, read-only Docker Hub cache.
package hubproxy

import (
	"errors"
	"light/internal/config"
	"time"
)

const TTL = 7 * 24 * time.Hour
const maxManifest = 4 << 20
const maxEntries = 1024
const maxBlobs = 16384

type Config struct {
	Enabled      bool
	MaxBytes     int64
	MaxBlobBytes int64
	Concurrency  int
	Timeout      time.Duration
	Reserve      uint64
}

func DefaultConfig() Config {
	return FromSettings(config.Default(), 0)
}
func FromSettings(s config.Settings, reserve uint64) Config {
	return Config{Enabled: s.HubEnabled, MaxBytes: s.HubCacheMaxBytes, MaxBlobBytes: s.HubMaxBlobBytes, Concurrency: s.HubFetchConcurrency, Timeout: time.Duration(s.HubFetchTimeoutSeconds) * time.Second, Reserve: reserve}
}
func (c Config) Validate() error {
	if c.MaxBytes <= 0 || c.MaxBlobBytes <= 0 || c.MaxBlobBytes > 20<<30 || c.Concurrency < 1 || c.Concurrency > 32 || c.Timeout < time.Second || c.Timeout > time.Hour {
		return errors.New("Hub 缓存容量须大于零，单 blob 上限须为 1 字节–20 GiB，并发须为 1–32，超时须为 1s–1h")
	}
	return nil
}
