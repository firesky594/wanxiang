package config

import (
	"errors"
	"os"
	"path/filepath"
)

type Config struct {
	RootDir    string
	DataDir    string
	AgentDir   string
	ProjectDir string
	RemoteURL  string
	HTTPAddr   string
}

// Load 从根目录与环境变量加载服务配置。
func Load(root string) (Config, error) {
	if root == "" {
		return Config{}, errors.New("root directory is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return Config{}, err
	}
	remote := envOrDefault("WANXIANG_REMOTE_URL", "https://github.com/firesky594/wanxiang.git")
	addr := envOrDefault("WANXIANG_HTTP_ADDR", ":8088")
	return Config{
		RootDir:    abs,
		DataDir:    filepath.Join(abs, "data"),
		AgentDir:   filepath.Join(abs, "agents"),
		ProjectDir: filepath.Join(abs, "projects"),
		RemoteURL:  remote,
		HTTPAddr:   addr,
	}, nil
}

func envOrDefault(key string, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}
