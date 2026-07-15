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
	root := os.Getenv("WANXIANG_ROOT")
	if root == "" {
		root = "."
	}
	cfg, err := config.Load(root)
	if err != nil {
		log.Fatal(err)
	}
	if fd, worker, parseErr := workerMode(os.Args[1:]); worker {
		if parseErr != nil {
			log.Fatal(parseErr)
		}
		input := os.NewFile(uintptr(fd), "worker-input")
		if input == nil {
			log.Fatal("invalid worker input fd")
		}
		defer input.Close()
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := executor.RunWorkerProcess(ctx, cfg, input, os.Stdout); err != nil {
			log.Printf("agent worker stopped: %s", executor.Redact(err.Error()))
			os.Exit(1)
		}
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
	if len(args) == 0 || args[0] != "agent-worker" {
		return 0, false, nil
	}
	set := flag.NewFlagSet("agent-worker", flag.ContinueOnError)
	fd := set.String("input-fd", "", "worker input fd")
	if err := set.Parse(args[1:]); err != nil {
		return 0, true, err
	}
	if set.NArg() != 0 || *fd == "" {
		return 0, true, errors.New("agent-worker requires --input-fd")
	}
	value, err := strconv.Atoi(*fd)
	if err != nil || value < 3 {
		return 0, true, errors.New("invalid worker input fd")
	}
	return value, true, nil
}
