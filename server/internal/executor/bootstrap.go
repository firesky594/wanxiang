package executor

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"time"

	"wanxiang-agent/server/internal/config"
	"wanxiang-agent/server/internal/db"
	"wanxiang-agent/server/internal/leases"
	"wanxiang-agent/server/internal/providers"
	"wanxiang-agent/server/internal/workspaces"
)

// RunWorkerProcess 装配并运行独立 Agent Worker 进程。
func RunWorkerProcess(ctx context.Context, cfg config.Config, input io.Reader, output io.Writer) error {
	conn, err := db.Open(filepath.Join(cfg.DataDir, "app.db"))
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := db.Migrate(ctx, conn); err != nil {
		return err
	}
	workspaceService := workspaces.NewService(cfg, conn, nil)
	leaseService := leases.NewService(conn, leases.SystemClock{}, workspaceService)
	files := NewFileTools(conn, leaseService)
	checks := NewCheckRunner(conn, leaseService)
	checkpoints := NewCheckpointRunner(conn, leaseService, leaseService)
	registry := providers.NewRegistry(&http.Client{Timeout: 20 * time.Second})
	runner := NewRunner(conn, NewEnvChatter(registry, ProcessAgentEnv()), files, checks, checkpoints)
	return RunWorker(ctx, input, output, runner, leaseService, checkpoints, leases.HeartbeatInterval)
}
