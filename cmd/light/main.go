package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"
	"light/internal/hubproxy"
	"light/internal/jsonfile"
	"light/internal/registry"
	"light/internal/server"
	"light/internal/store"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) == 0 {
		return errors.New("用法：light init | serve | check | health | gc | purge-uploads；使用 -h 查看选项")
	}
	f := flag.NewFlagSet(args[0], flag.ContinueOnError)
	dir := f.String("data", "/var/lib/light", "数据目录")
	username := f.String("username", "admin", "管理员名称")
	stdin := f.Bool("password-stdin", false, "从标准输入读取一行初始密码")
	dry := f.Bool("dry-run", false, "仅预览 GC")
	execute := f.Bool("execute", false, "确认执行 GC")
	maxUpload := f.Int64("max-upload-bytes", 20<<30, "单次上传请求正文上限，0 表示不限")
	idle := f.Duration("transfer-idle-timeout", 5*time.Minute, "上传/下载空闲超时")
	older := f.Duration("older-than", 7*24*time.Hour, "离线上传清理的最短闲置时间，至少 24h")
	healthURL := f.String("url", "http://127.0.0.1:5080/readyz", "health 检查地址")
	reserve := f.Uint64("reserve-bytes", 1<<30, "拒绝新上传的磁盘预留字节数")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("存在未知参数")
	}
	if args[0] != "init" && args[0] != "serve" && args[0] != "check" && args[0] != "gc" && args[0] != "health" && args[0] != "purge-uploads" {
		return errors.New("未知命令")
	}
	if args[0] == "health" {
		u, err := url.Parse(*healthURL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return errors.New("健康检查地址不合法")
		}
		client := &http.Client{Timeout: 5 * time.Second}
		response, err := client.Get(u.String())
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode != 200 {
			return fmt.Errorf("实例未就绪：%d", response.StatusCode)
		}
		fmt.Println("实例已就绪")
		return nil
	}
	if *maxUpload < 0 || *idle < time.Second || *older < 24*time.Hour {
		return errors.New("上传上限不能为负，空闲超时至少 1s，上传清理年龄至少 24h")
	}
	st, err := store.Open(*dir)
	if err != nil {
		return err
	}
	defer st.Close()
	switch args[0] {
	case "init":
		if st.Snapshot().Account != nil {
			return errors.New("管理员已初始化，不覆盖已有账号")
		}
		var password string
		if *stdin {
			r := bufio.NewReader(io.LimitReader(os.Stdin, 1024))
			value, e := r.ReadString('\n')
			if e != nil && e != io.EOF {
				return e
			}
			password = strings.TrimSuffix(strings.TrimSuffix(value, "\n"), "\r")
		} else {
			if !term.IsTerminal(int(os.Stdin.Fd())) {
				return errors.New("非交互环境请使用 -password-stdin")
			}
			fmt.Fprint(os.Stderr, "设置管理员密码（至少 8 字节）：")
			raw, e := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(os.Stderr)
			if e != nil {
				return e
			}
			fmt.Fprint(os.Stderr, "再次输入密码：")
			again, e := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(os.Stderr)
			if e != nil {
				return e
			}
			if string(raw) != string(again) {
				return errors.New("两次密码不一致")
			}
			password = string(raw)
		}
		if err = st.Init(*username, password); err != nil {
			return err
		}
		st.Record(*username, "初始化管理员", "", "")
		fmt.Println("管理员已初始化，未启动服务。")
		return nil
	case "check":
		if !st.Ready() {
			return errors.New("请先执行 light init 初始化管理员")
		}
		fmt.Println("管理数据与实例锁检查通过；镜像完整性需通过拉取验证。")
		return nil
	}
	if !st.Ready() {
		return errors.New("请先执行 light init 初始化管理员")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runtimeSettings := st.Settings()

	reg, err := registry.New(ctx, st, *reserve)
	if err != nil {
		return err
	}
	defer reg.Close()
	reg.MaxUploadBytes = *maxUpload
	reg.IdleTimeout = *idle
	if args[0] == "serve" {
		if err = reg.ConfigureHub(hubproxy.FromSettings(runtimeSettings, *reserve), nil); err != nil {
			return err
		}
	}

	if args[0] == "gc" || args[0] == "purge-uploads" {
		if *dry == *execute {
			return errors.New("GC 必须且只能选择 -dry-run 或 -execute")
		}
		job := map[string]any{"startedAt": time.Now().UTC(), "dryRun": *dry, "status": "running"}
		jobName := "last-gc.json"
		if args[0] == "purge-uploads" {
			jobName = "last-upload-cleanup.json"
		}
		job["command"] = args[0]
		path := filepath.Join(st.Dir, "jobs", jobName)
		if err = jsonfile.Write(path, job); err != nil {
			return err
		}
		st.Record("cli", "开始离线维护", fmt.Sprintf("dryRun=%t", *dry), "")

		kind := "gc"
		cutoff := time.Time{}
		if args[0] == "purge-uploads" {
			kind = "uploads"
			cutoff = time.Now().UTC().Add(-*older)
		}
		plan, planErr := reg.PlanCleanup(ctx, kind, cutoff)
		err = planErr
		if err == nil {
			job["items"] = len(plan.Targets)
			job["files"] = plan.Files
			job["bytes"] = plan.Bytes
			if *execute {
				err = reg.ExecuteCleanup(ctx, plan)
			}
		}

		job["finishedAt"] = time.Now().UTC()
		job["status"] = "completed"
		if err != nil {
			job["status"] = "failed"
			job["error"] = err.Error()
		}
		if e := jsonfile.Write(path, job); e != nil {
			return e
		}
		st.Record("cli", "结束离线维护", job["status"].(string), "")
		raw, _ := json.MarshalIndent(job, "", "  ")
		fmt.Println(string(raw))
		return err
	}
	app := server.New(st, reg)
	defer app.Close()
	httpServer := server.HTTPServer(app)
	done := make(chan error, 1)
	go func() {
		log.Printf("light 监听 %s", server.ListenAddress)
		done <- httpServer.ListenAndServe()
	}()
	select {
	case err = <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		app.Close()
		shutdown, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err = httpServer.Shutdown(shutdown); err != nil {
			_ = httpServer.Close()
			return err
		}
		return nil
	}
}
