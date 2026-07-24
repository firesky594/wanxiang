package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"wanxiang-agent/server/internal/app"
	"wanxiang-agent/server/internal/config"
	"wanxiang-agent/server/internal/executor"
	"wanxiang-agent/server/internal/httpapi"
)

func main() {
	// main 是程序的入口函数。
	// 主要职责：
	// 1) 读取运行时根目录（通过 WANXIANG_ROOT 环境变量，可回退到当前目录）。
	// 2) 加载配置并根据命令行判断是否进入 worker 子进程模式（agent-worker）。
	// 3) 在 worker 模式下：从指定的文件描述符读取输入并启动 worker 流程。
	// 4) 在 HTTP 模式下：初始化 application 并启动 HTTP 服务。

	// 注意：worker 模式会直接退出主进程（return），HTTP 模式在退出前会关闭 application。

	root := os.Getenv("WANXIANG_ROOT")
	if root == "" {
		root = "."
	}
	cfg, err := config.Load(root)
	if err != nil {
		log.Fatal(err)
	}
	// 判断是否以 worker 子进程模式运行（通过子命令 agent-worker）。
	// workerMode 返回 (fd, isWorker, error)。若 isWorker 为 true，则进入 worker 分支。
	if fd, worker, parseErr := workerMode(os.Args[1:]); worker {
		if parseErr != nil {
			// 参数解析失败直接退出（致命错误）
			log.Fatal(parseErr)
		}
		// 通过数值 fd 构造 *os.File，用以读取外部传入的任务数据流。
		input := os.NewFile(uintptr(fd), "worker-input")
		if input == nil {
			log.Fatal("invalid worker input fd")
		}
		defer input.Close()

		// 监听中断信号，确保在收到 SIGINT/SIGTERM 时能够优雅地停止 worker。
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		// 运行 worker 主循环，注意要对返回错误进行脱敏处理后记录日志。
		if err := executor.RunWorkerProcess(ctx, cfg, input, os.Stdout); err != nil {
			// 这里使用 executor.Redact 对敏感信息进行脱敏后再记录
			log.Printf("agent worker stopped: %s", executor.Redact(err.Error()))
			os.Exit(1)
		}
		// worker 模式完成后直接返回，主进程不继续启动 HTTP 服务
		return
	}
	application, err := app.New(cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := application.Close(); err != nil {
			log.Printf("close application: %v", err)
		}
	}()
	log.Printf("wanxiang-agent listening addr=%s root=%s", cfg.HTTPAddr, cfg.RootDir)
	if err := http.ListenAndServe(cfg.HTTPAddr, httpapi.NewRouter(application.HTTP)); err != nil {
		log.Printf("http server stopped: %v", err)
	}
}

func workerMode(args []string) (int, bool, error) {
	// 仅当第一个参数是 "agent-worker" 时才视为 worker 模式
	if len(args) == 0 || args[0] != "agent-worker" {
		return 0, false, nil
	}

	// 使用独立的 FlagSet 解析子命令参数，避免污染全局 flag
	set := flag.NewFlagSet("agent-worker", flag.ContinueOnError)
	fd := set.String("input-fd", "", "worker input fd")
	if err := set.Parse(args[1:]); err != nil {
		// 解析错误（例如无效参数）返回错误，由调用方决定如何处理
		return 0, true, err
	}

	// 要求没有多余的位置参数且必须提供 --input-fd
	if set.NArg() != 0 || *fd == "" {
		return 0, true, errors.New("agent-worker requires --input-fd")
	}

	// 将 fd 字符串转换为整数，并验证其合理范围（保留 0-2 为标准流，要求 >=3）
	value, err := strconv.Atoi(*fd)
	if err != nil || value < 3 {
		return 0, true, errors.New("invalid worker input fd")
	}
	return value, true, nil
}
