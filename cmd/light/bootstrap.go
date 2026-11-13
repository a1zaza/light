package main

import (
	"fmt"
	"io"

	"light/internal/store"
)

// initializeAdministrator runs only during serve, under the data directory's
// instance lock. Print the generated secret only after durable initialization.
func initializeAdministrator(st *store.Store, output io.Writer) error {
	if st.Snapshot().Account != nil {
		return nil
	}
	const username = "admin"
	password := store.Random(24)
	if err := st.Init(username, password); err != nil {
		return fmt.Errorf("自动初始化管理员失败：%w", err)
	}
	st.Record(username, "首次启动初始化管理员", "", "")
	if _, err := fmt.Fprintf(output, "\n首次启动，已创建管理员账号。\n管理员用户名：%s\n管理员初始密码：%s\n请登录后在“系统设置”中修改密码。\n\n", username, password); err != nil {
		return fmt.Errorf("管理员已创建，但初始凭据输出失败（重启不会重置账号）：%w", err)
	}
	return nil
}
