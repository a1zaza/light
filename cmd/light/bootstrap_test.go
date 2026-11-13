package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"light/internal/store"
)

func initialPassword(t *testing.T, output string) string {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		if value, ok := strings.CutPrefix(line, "管理员初始密码："); ok && len(value) == 32 {
			return value
		}
	}
	t.Fatal("未输出符合预期的初始密码")
	return ""
}
func TestAutomaticAdministratorIsDurableAndPrintedOnce(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if st != nil {
			st.Close()
		}
	}()
	var output bytes.Buffer
	if err = initializeAdministrator(st, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "管理员用户名：admin") {
		t.Fatal("未显示管理员名称")
	}
	password := initialPassword(t, output.String())
	if !st.Ready() || !st.Authenticate("admin", password) {
		t.Fatal("输出凭据无法登录持久化账号")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "management.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(password)) {
		t.Fatal("管理快照保存了明文密码")
	}
	// Audit events record the action, not the initial credential.
	entries, err := filepath.Glob(filepath.Join(dir, "logs", "audit-*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("缺少初始化审计事件")
	}
	for _, name := range entries {
		content, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(content, []byte(password)) {
			t.Fatal("审计记录泄露了初始密码")
		}
	}
	revision := st.Snapshot().Revision
	output.Reset()
	if err = initializeAdministrator(st, &output); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 || st.Snapshot().Revision != revision {
		t.Fatal("重复启动修改账号或再次输出凭据")
	}
	if err = st.Password("owner", password, "fixture-rotated-password"); err != nil {
		t.Fatal(err)
	}
	existing := *st.Snapshot().Account
	st.Close()
	st, err = store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = initializeAdministrator(st, &output); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 || *st.Snapshot().Account != existing || !st.Authenticate("owner", "fixture-rotated-password") {
		t.Fatal("重启覆盖了用户修改的账号")
	}
}
func TestAutomaticAdministratorDoesNotPrintBeforePersistence(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	path := filepath.Join(st.Dir, "management.json")
	// Replace only this fixture's destination with a directory to force rename
	// failure deterministically, independently of the test runner's permissions.
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err = initializeAdministrator(st, &output); err == nil {
		t.Fatal("持久化失败未阻止初始化")
	}
	if output.Len() != 0 || st.Snapshot().Account != nil || st.Ready() {
		t.Fatal("持久化失败仍发布凭据或账号")
	}
}

type closedOutput struct{}

func (closedOutput) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func TestAutomaticAdministratorOutputFailureDoesNotResetAccount(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err = initializeAdministrator(st, closedOutput{}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal("输出失败未报告")
	}
	account := st.Snapshot().Account
	if account == nil || !st.Ready() {
		t.Fatal("输出前未持久化账号")
	}
	var output bytes.Buffer
	if err = initializeAdministrator(st, &output); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 || *st.Snapshot().Account != *account {
		t.Fatal("输出失败后的重试重置了账号")
	}
}
func TestOfflineCommandsDoNotInitializeAdministrator(t *testing.T) {
	for _, command := range []string{"check", "gc", "purge-uploads"} {
		t.Run(command, func(t *testing.T) {
			dir := t.TempDir()
			if err := run([]string{command, "-data", dir, "-dry-run"}); err == nil || !strings.Contains(err.Error(), "请先启动 light serve") {
				t.Fatal("离线命令未提示启动初始化")
			}
			st, err := store.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			if st.Snapshot().Account != nil {
				t.Fatal("离线命令自动创建了管理员")
			}
		})
	}
}

func TestAutomaticAdministratorHonorsInstanceLock(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err = run([]string{"serve", "-data", st.Dir}); err == nil || !strings.Contains(err.Error(), "占用") {
		t.Fatal("启动未持有独占锁")
	}
	if st.Snapshot().Account != nil {
		t.Fatal("竞争实例创建了管理员")
	}
}
