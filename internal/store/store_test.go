package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFailedWriteAndRevisionConflictPreserveState(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Init("admin", "synthetic-test-password"); err != nil {
		t.Fatal(err)
	}
	before := s.Snapshot()
	path := filepath.Join(s.Dir, "management.json")
	backup := path + ".backup"
	if err = os.Rename(path, backup); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err = s.Change(func(st *State) error { st.Secret = "should-not-be-committed"; return nil }); err == nil {
		t.Fatal("write succeeded over a directory")
	}
	if got := s.Snapshot(); got.Secret != before.Secret || got.Revision != before.Revision {
		t.Fatal("failed write changed live state")
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(backup, path); err != nil {
		t.Fatal(err)
	}
	if err = s.Change(func(st *State) error { st.Secret = "not-committed"; return ErrConflict }); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if s.Snapshot().Secret != before.Secret {
		t.Fatal("conflicted transaction changed live state")
	}
}
func TestCorruptManagementFailsClosed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "management.json"), []byte(`{"schemaVersion":1,"schemaVersion":2}`), 0600); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(dir); err == nil {
		s.Close()
		t.Fatal("corrupt administration file was accepted")
	}
}
func TestAuditTailRecoveryAndMiddleCorruption(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.Record("admin", "test", "", "")
	s.Close()
	paths, err := filepath.Glob(filepath.Join(dir, "logs", "*.jsonl"))
	if err != nil || len(paths) != 1 {
		t.Fatal(paths, err)
	}
	path := paths[0]
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"incomplete":`)
	f.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.AuditError() != "" {
		t.Fatal(s.AuditError())
	}
	s.Record("admin", "after-recovery", "", "")
	items, err := s.Audits()
	if err != nil || len(items) != 2 {
		t.Fatal(items, err)
	}
	s.Close()
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "incomplete") {
		t.Fatal("incomplete tail retained")
	}
	if err = os.WriteFile(path, append([]byte("corrupt-middle\n"), raw...), 0600); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.AuditError() == "" {
		t.Fatal("audit corruption not reported")
	}
	s.Record("admin", "must-not-append", "", "")
	after, _ := os.ReadFile(path)
	if strings.Contains(string(after), "must-not-append") {
		t.Fatal("appended to corrupted audit")
	}
}

func TestEightCharacterPasswordBoundary(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Init("admin", "1234567"); err == nil {
		t.Fatal("accepted a seven-character initial password")
	}
	if err = s.Init("admin", "initial8"); err != nil {
		t.Fatalf("rejected an eight-character initial password: %v", err)
	}
	if err = s.Password("admin", "initial8", "1234567"); err == nil {
		t.Fatal("accepted a seven-character replacement password")
	}
	if err = s.Password("admin", "initial8", "changed8"); err != nil {
		t.Fatalf("rejected an eight-character replacement password: %v", err)
	}
	if !s.Authenticate("admin", "changed8") || s.Authenticate("admin", "initial8") {
		t.Fatal("password replacement did not take effect")
	}
}

func TestAuthenticatedAccountProofKeepsVerifiedRevision(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Init("admin", "original-test-password"); err != nil {
		t.Fatal(err)
	}
	verified, ok := s.AuthenticateAccount("admin", "original-test-password")
	if !ok {
		t.Fatal("initial authentication failed")
	}
	if err = s.Password("admin", "original-test-password", "updated-test-password"); err != nil {
		t.Fatal(err)
	}
	if verified.Revision == s.Snapshot().Account.Revision {
		t.Fatal("old password proof silently became current")
	}
	if _, ok = s.AuthenticateAccount("admin", "original-test-password"); ok {
		t.Fatal("old password accepted after rotation")
	}
	current, ok := s.AuthenticateAccount("admin", "updated-test-password")
	if !ok || current.Revision != s.Snapshot().Account.Revision {
		t.Fatal("new password revision incorrect")
	}
}
