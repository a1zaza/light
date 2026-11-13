package main

import (
	"light/internal/server"
	"light/internal/store"
	"strings"
	"testing"
)

func TestOfflineCommandsRefuseActiveDirectory(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err = st.Init("admin", "synthetic-cli-password"); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"init", "check", "gc", "purge-uploads"} {
		t.Run(command, func(t *testing.T) {
			err := run([]string{command, "-data", st.Dir, "-dry-run"})
			if err == nil || !strings.Contains(err.Error(), "占用") {
				t.Fatalf("offline command did not honor instance lock: %v", err)
			}
		})
	}
}
func TestFixedListenAddress(t *testing.T) {
	if got := server.HTTPServer(nil).Addr; got != ":5080" {
		t.Fatalf("unexpected listener: %s", got)
	}
}

func TestRemovedDeploymentFlagsAreNotAccepted(t *testing.T) {
	for _, flag := range []string{"-address", "-external-url"} {
		t.Run(flag, func(t *testing.T) {
			if err := run([]string{"serve", "-data", t.TempDir(), flag, "unused"}); err == nil {
				t.Fatal("removed deployment flag was accepted")
			}
		})
	}
}

func TestInvalidRateLimitHeaderRejectedBeforeOpeningData(t *testing.T) {
	if err := run([]string{"serve", "-data", t.TempDir(), "-rate-limit-ip-header", "X-Light-IP\r\nInjected"}); err == nil || !strings.Contains(err.Error(), "Header") {
		t.Fatalf("invalid header not rejected: %v", err)
	}
}
