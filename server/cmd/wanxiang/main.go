package main

import (
	"log"
	"net/http"
	"os"

	"wanxiang-agent/server/internal/app"
	"wanxiang-agent/server/internal/config"
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
